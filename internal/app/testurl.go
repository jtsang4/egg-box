package app

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sagernet/sing-box/option"
	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"

	"eggbox/internal/configgen"
	"eggbox/internal/plancompile"
	"eggbox/internal/routing"
	"eggbox/internal/rulefmt"
)

// ErrBadTarget means what was typed is not an address, a domain or a web address.
var ErrBadTarget = errors.New("that is not a web address, a domain or an IP address")

// TestSource says which of an item's sources matched.
type TestSource struct {
	// Name is the source's name; empty for a hand-written one.
	Name    string
	Project string
	URL     string
	// Entry is the hand-written entry that matched, if the source is one.
	Entry *routing.Entry
}

// TestURLResult is what a test of an address found.
type TestURLResult struct {
	Host  string
	Addrs []netip.Addr
	Port  int
	// Resolved is set when the addresses were looked up to decide ("by the
	// answer of the name server"), not given.
	Resolved   bool
	ResolveErr string
	// Why is what decided: the routing mode, an item, an exception, the
	// fallback or the local network.
	Why Why
	// Dest is where the matching row sends traffic.
	Dest   routing.Dest
	Source *TestSource
	// Exit is the node traffic goes through now, when connected and known.
	Exit *ExitInfo
	// Skipped counts the rules that could not be decided because they ask
	// about a program or a port the address does not tell.
	Skipped int
	// Waiting counts the items that are on but have no data yet, and so could
	// not take part.
	Waiting int
}

type target struct {
	host string
	ip   netip.Addr
	port int
}

var hostPattern = regexp.MustCompile(`^[a-z0-9_]([a-z0-9_-]*[a-z0-9_])?(\.[a-z0-9_]([a-z0-9_-]*[a-z0-9_])?)*$`)

// parseTarget reads what the person typed: a web address, a domain, an IP
// address, with or without a port.
func parseTarget(raw string) (target, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return target{}, ErrBadTarget
	}
	var t target
	if !strings.Contains(s, "://") {
		s = "http://" + s
	} else if i := strings.Index(s, "://"); strings.ToLower(s[:i]) != "http" && strings.ToLower(s[:i]) != "https" &&
		strings.ToLower(s[:i]) != "ws" && strings.ToLower(s[:i]) != "wss" {
		return target{}, ErrBadTarget
	}
	u, err := url.Parse(s)
	if err != nil || u.Hostname() == "" {
		return target{}, ErrBadTarget
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return target{}, ErrBadTarget
		}
		t.port = n
	} else {
		switch strings.ToLower(u.Scheme) {
		case "https", "wss":
			t.port = 443
		default:
			t.port = 80
		}
	}
	if ip, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		t.ip = ip.Unmap()
		return t, nil
	}
	ascii, err := idna.Lookup.ToASCII(host)
	if err != nil || !hostPattern.MatchString(ascii) {
		return target{}, ErrBadTarget
	}
	t.host = ascii
	return t, nil
}

// matcherKey identifies compiled rules by where they live: the compiler keeps
// the same slice for as long as an item's rules do not change.
type matcherKey struct {
	first *option.HeadlessRule
	n     int
}

// TestURL says where an address would go, by evaluating the plan the way the
// core does: the routing mode, the local network, the exceptions in order, the
// host-name part of every item, then (after looking the name up) the address
// part, then the fallback. It needs no connection; with one, it also says
// which node the traffic uses now.
//
// The mode is the first thing the core asks: in Global every connection goes
// through the main exit and in Direct none does, whatever the plan says. The
// core's mode is only known while it runs; without it the plan is what is
// being asked about.
//
// The core looks a name up once, where the first rule about addresses is, and
// turns the connection away when the name cannot be looked up: that is what
// the answer says then, a block that no rule of the plan wrote. Where the plan
// has no rule about addresses, no name is looked up and one that leads
// nowhere goes where its host name says, or to the fallback.
func (s *Service) TestURL(ctx context.Context, raw string) (*TestURLResult, error) {
	t, err := parseTarget(raw)
	if err != nil {
		return nil, err
	}
	v := s.view.Load()
	if v == nil {
		if v, err = s.buildView(); err != nil {
			return nil, err
		}
	}
	res := &TestURLResult{Host: t.host, Port: t.port}
	q := rulefmt.Query{Domain: t.host, Port: uint16(t.port), Network: "tcp"}
	if t.ip.IsValid() {
		res.Host = t.ip.String()
		res.Addrs = []netip.Addr{t.ip}
		q.Addrs = res.Addrs
	}
	resolve := func() {
		if len(q.Addrs) > 0 || res.Resolved || t.host == "" {
			return
		}
		rctx, cancel := context.WithTimeout(ctx, 4*time.Second)
		defer cancel()
		lookup := s.cfg.Resolve
		if lookup == nil {
			lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
				return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
			}
		}
		addrs, err := lookup(rctx, t.host)
		res.Resolved = true
		if err != nil || len(addrs) == 0 {
			res.ResolveErr = "no answer"
			return
		}
		for _, a := range addrs {
			a = a.Unmap()
			if a.Is6() && !s.lib.Prefs().IPv6 {
				continue
			}
			q.Addrs = append(q.Addrs, a)
		}
		res.Addrs = q.Addrs
	}

	if snap := s.Snapshot(); snap.Phase.Online() {
		switch snap.Status.GetMode() {
		case configgen.ModeGlobal:
			res.Why, res.Dest = Why{Kind: WhyMode}, routing.Follow()
			if e, ok := snap.Exits["main"]; ok {
				res.Exit = &e
			}
			return res, nil
		case configgen.ModeDirect:
			res.Why, res.Dest = Why{Kind: WhyMode}, routing.Direct()
			return res, nil
		}
	}

	view := v.state.View()
	finish := func(why Why, dest routing.Dest, id string) (*TestURLResult, error) {
		res.Why, res.Dest = why, dest
		if id != "" && dest.Kind != routing.DestDirect && dest.Kind != routing.DestReject {
			if e, ok := s.Snapshot().Exits[id]; ok && s.Snapshot().Phase.Online() {
				res.Exit = &e
			}
		}
		for _, row := range view.Rows {
			if row.Item.Enabled && v.res.Items[row.Item.ID].State == plancompile.ItemUnavailable {
				res.Waiting++
			}
		}
		return res, nil
	}

	// A name that cannot be looked up where the plan needs its address.
	refused := func() (*TestURLResult, error) {
		return finish(Why{Kind: WhyUnresolved}, routing.Reject(), "")
	}

	// The local network.
	if s.lib.Prefs().LANDirect {
		private := false
		for _, a := range q.Addrs {
			private = private || a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast()
		}
		if !private && t.host != "" {
			for _, suffix := range []string{"local", "lan", "localdomain", "home.arpa"} {
				if t.host == suffix || strings.HasSuffix(t.host, "."+suffix) {
					private = true
				}
			}
		}
		if private {
			return finish(Why{Kind: WhyLAN}, routing.Direct(), "")
		}
	}

	// The exceptions, in order.
	for _, x := range v.state.Exceptions {
		if !x.Enabled {
			continue
		}
		rule, err := plancompile.EntryRule(x.Entry())
		if err != nil {
			continue
		}
		if rulefmt.Inspect([]option.HeadlessRule{rule}).Addr {
			if resolve(); res.ResolveErr != "" {
				return refused()
			}
		}
		m, err := rulefmt.NewMatcher([]option.HeadlessRule{rule})
		if err != nil {
			continue
		}
		switch m.Match(q) {
		case rulefmt.Match:
			e := x.Entry()
			res.Source = &TestSource{Entry: &e}
			return finish(Why{Kind: WhyException, ID: x.ID, Name: x.Value, Index: v.state.ExceptionIndex(x.ID)}, x.Dest, x.ID)
		case rulefmt.Unknown:
			res.Skipped++
		}
	}

	// The host-name part of every item, in the plan's order.
	for _, row := range view.Rows {
		rules, ok := v.res.Rules[row.Item.ID]
		if !ok || len(rules.Domain) == 0 {
			continue
		}
		verdict := s.verdict(rules.Domain, q)
		if verdict == rulefmt.Unknown {
			res.Skipped++
		}
		if verdict == rulefmt.Match {
			res.Source = s.sourceOf(row.Item, q)
			return finish(Why{Kind: WhyItem, ID: row.Item.ID, Name: row.Item.Name}, row.Item.Dest, row.Item.ID)
		}
	}

	// Then the address part, once the name is looked up (if any item has one).
	for _, row := range view.Rows {
		if rules, ok := v.res.Rules[row.Item.ID]; ok && len(rules.IP) > 0 {
			if resolve(); res.ResolveErr != "" {
				return refused()
			}
			break
		}
	}
	for _, row := range view.Rows {
		rules, ok := v.res.Rules[row.Item.ID]
		if !ok || len(rules.IP) == 0 {
			continue
		}
		verdict := s.verdict(rules.IP, q)
		if verdict == rulefmt.Unknown {
			res.Skipped++
		}
		if verdict == rulefmt.Match {
			res.Source = s.sourceOf(row.Item, q)
			return finish(Why{Kind: WhyItem, ID: row.Item.ID, Name: row.Item.Name}, row.Item.Dest, row.Item.ID)
		}
	}
	return finish(Why{Kind: WhyFallback}, view.Fallback, "fallback")
}

// verdict evaluates compiled rules, keeping the matcher while the rules stay
// the same.
func (s *Service) verdict(rules []option.HeadlessRule, q rulefmt.Query) rulefmt.Verdict {
	if len(rules) == 0 {
		return rulefmt.NoMatch
	}
	s.mu.Lock()
	key := matcherKey{first: &rules[0], n: len(rules)}
	m := s.matchers[key]
	s.mu.Unlock()
	if m == nil {
		var err error
		if m, err = rulefmt.NewMatcher(rules); err != nil {
			return rulefmt.NoMatch
		}
		s.mu.Lock()
		if len(s.matchers) >= 64 {
			s.matchers = map[matcherKey]*rulefmt.Matcher{}
		}
		s.matchers[key] = m
		s.mu.Unlock()
	}
	return m.Match(q)
}

// sourceOf finds the first source of an item that matches the query.
func (s *Service) sourceOf(it routing.Item, q rulefmt.Query) *TestSource {
	for _, src := range it.Sources {
		rules, _ := plancompile.SourceRules(src, s.rules)
		if len(rules) == 0 {
			continue
		}
		if src.Kind == routing.SourceInline {
			for _, e := range src.Entries {
				r, err := plancompile.EntryRule(e)
				if err != nil {
					continue
				}
				if m, err := rulefmt.NewMatcher([]option.HeadlessRule{r}); err == nil && m.Match(q) == rulefmt.Match {
					entry := e
					return &TestSource{Entry: &entry}
				}
			}
			continue
		}
		m, err := rulefmt.NewMatcher(rules)
		if err != nil {
			continue
		}
		if m.Match(q) == rulefmt.Match {
			return &TestSource{Name: src.Name, Project: src.Project, URL: src.URL}
		}
	}
	return nil
}

// ------------------------------------------------------------- candidates

// Scope says how wide an exception made from a connection is.
type Scope string

const (
	// ScopeHost is the name the connection was made to, exactly.
	ScopeHost Scope = "host"
	// ScopeDomain is the registered domain and all of its subdomains.
	ScopeDomain Scope = "domain"
	// ScopeIP is the address.
	ScopeIP Scope = "ip"
	// ScopeProcess is the program that made the connection.
	ScopeProcess Scope = "process"
)

// Candidate is an exception that can be made from a connection.
type Candidate struct {
	Scope Scope
	Entry routing.Entry
}

// ConnCandidates lists the exceptions a connection offers to make: its name,
// its registered domain (by the public suffix list), its address and its
// program.
func ConnCandidates(c Conn) []Candidate {
	var out []Candidate
	host := strings.TrimSuffix(strings.ToLower(c.Host), ".")
	if host != "" && net.ParseIP(host) == nil {
		out = append(out, Candidate{ScopeHost, routing.Entry{Kind: routing.EntryDomain, Value: host}})
		// A name that is itself a public suffix ("github.io", "co.uk") has no
		// registered domain: widening it would take in everybody else's sites,
		// so only the name is offered.
		if base, err := publicsuffix.EffectiveTLDPlusOne(host); err == nil && base != "" {
			out = append(out, Candidate{ScopeDomain, routing.Entry{Kind: routing.EntryDomainSuffix, Value: base}})
		}
	}
	if ip := connIP(c.Destination); ip.IsValid() {
		out = append(out, Candidate{ScopeIP, routing.Entry{Kind: routing.EntryIPCIDR, Value: netip.PrefixFrom(ip, ip.BitLen()).String()}})
	}
	if c.Process != "" {
		out = append(out, Candidate{ScopeProcess, routing.Entry{Kind: routing.EntryProcessPath, Value: c.Process}})
	}
	return out
}

// connIP is the address in "1.2.3.4:443" or "[2001:db8::1]:443".
func connIP(dest string) netip.Addr {
	if ap, err := netip.ParseAddrPort(dest); err == nil {
		return ap.Addr().Unmap()
	}
	if host, _, err := net.SplitHostPort(dest); err == nil {
		if a, err := netip.ParseAddr(host); err == nil {
			return a.Unmap()
		}
	}
	return netip.Addr{}
}

// ProcessName is the program's name of a path, for showing.
func ProcessName(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	return path.Base(p)
}
