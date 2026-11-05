package rulefmt

import (
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/domain"
)

// Query is a connection's destination as far as it is known.
type Query struct {
	Domain  string       // lower case; "" when the target is an IP address
	Addrs   []netip.Addr // the destination's addresses, literal or resolved
	Port    uint16       // 0 when unknown
	Network string       // "tcp", "udp" or "" when unknown
	// Process and ProcessPath are the program that made a real connection;
	// a URL has none, and rules about programs are then Unknown.
	Process     string
	ProcessPath string
}

// Verdict is the answer of a match. Unknown is not a failure: the rule asks
// about something a URL cannot tell (a process, the source port), so the
// answer would need a real connection.
type Verdict uint8

const (
	NoMatch Verdict = iota
	Match
	Unknown
)

// Matcher evaluates headless rules for a Query the way the core does, so that
// "test this URL" and a real connection pick the same rule.
type Matcher struct{ rules []*mrule }

// NewMatcher prepares rules for matching.
func NewMatcher(rules []option.HeadlessRule) (m *Matcher, err error) {
	defer func() {
		if r := recover(); r != nil {
			m, err = nil, fmt.Errorf("%w: %v", ErrMalformed, r)
		}
	}()
	m = &Matcher{}
	for _, r := range rules {
		mr, err := newMRule(r)
		if err != nil {
			return nil, err
		}
		m.rules = append(m.rules, mr)
	}
	return m, nil
}

// Match is Match if any rule matches, Unknown if none does but some could
// not be decided, NoMatch otherwise.
func (m *Matcher) Match(q Query) Verdict {
	q.Domain = strings.ToLower(strings.TrimSuffix(q.Domain, "."))
	res := NoMatch
	for _, r := range m.rules {
		switch r.eval(q) {
		case Match:
			return Match
		case Unknown:
			res = Unknown
		}
	}
	return res
}

type mrule struct {
	logical bool
	and     bool
	invert  bool
	subs    []*mrule

	exact    map[string]struct{}
	suffix   map[string]struct{} // "a.com": a.com and its sub-domains
	subOnly  map[string]struct{} // ".a.com": its sub-domains only
	keyword  []string
	regex    []*regexp.Regexp
	adguard  *domain.AdGuardMatcher
	hasName  bool
	prefixes []netip.Prefix
	hasIP    bool
	ports    map[uint16]struct{}
	ranges   [][2]uint16
	hasPort  bool
	network  []string
	procName []string
	procPath []string
	procRe   []*regexp.Regexp
	hasProc  bool
	unknown  bool // conditions a URL cannot decide
}

func newMRule(r option.HeadlessRule) (*mrule, error) {
	if r.Type == C.RuleTypeLogical {
		mr := &mrule{logical: true, and: r.LogicalOptions.Mode != C.LogicalTypeOr, invert: r.LogicalOptions.Invert}
		for _, s := range r.LogicalOptions.Rules {
			sub, err := newMRule(s)
			if err != nil {
				return nil, err
			}
			mr.subs = append(mr.subs, sub)
		}
		return mr, nil
	}
	d := r.DefaultOptions
	mr := &mrule{invert: d.Invert}
	if len(d.Domain) > 0 {
		mr.exact = make(map[string]struct{}, len(d.Domain))
		for _, v := range d.Domain {
			mr.exact[strings.ToLower(v)] = struct{}{}
		}
		mr.hasName = true
	}
	if len(d.DomainSuffix) > 0 {
		mr.suffix = map[string]struct{}{}
		mr.subOnly = map[string]struct{}{}
		for _, v := range d.DomainSuffix {
			v = strings.ToLower(v)
			if strings.HasPrefix(v, ".") {
				mr.subOnly[v] = struct{}{}
			} else {
				mr.suffix[v] = struct{}{}
			}
		}
		mr.hasName = true
	}
	for _, v := range d.DomainKeyword {
		mr.keyword = append(mr.keyword, strings.ToLower(v))
		mr.hasName = true
	}
	for _, v := range d.DomainRegex {
		re, err := regexp.Compile(v)
		if err != nil {
			return nil, err
		}
		mr.regex = append(mr.regex, re)
		mr.hasName = true
	}
	if len(d.AdGuardDomain) > 0 {
		mr.adguard = domain.NewAdGuardMatcher(d.AdGuardDomain)
		mr.hasName = true
	}
	for _, v := range d.IPCIDR {
		if p, err := netip.ParsePrefix(v); err == nil {
			mr.prefixes = append(mr.prefixes, p)
		} else if a, err := netip.ParseAddr(v); err == nil {
			mr.prefixes = append(mr.prefixes, netip.PrefixFrom(a, a.BitLen()))
		}
	}
	mr.hasIP = len(d.IPCIDR) > 0
	if len(d.Port) > 0 || len(d.PortRange) > 0 {
		mr.hasPort = true
		mr.ports = make(map[uint16]struct{}, len(d.Port))
		for _, p := range d.Port {
			mr.ports[p] = struct{}{}
		}
		for _, v := range d.PortRange {
			lo, hi, ok := parsePortRange(v)
			if !ok {
				return nil, strconv.ErrSyntax
			}
			mr.ranges = append(mr.ranges, [2]uint16{lo, hi})
		}
	}
	mr.network = append(mr.network, d.Network...)
	mr.procName = append(mr.procName, d.ProcessName...)
	mr.procPath = append(mr.procPath, d.ProcessPath...)
	for _, v := range d.ProcessPathRegex {
		re, err := regexp.Compile(v)
		if err != nil {
			return nil, err
		}
		mr.procRe = append(mr.procRe, re)
	}
	mr.hasProc = len(mr.procName) > 0 || len(mr.procPath) > 0 || len(mr.procRe) > 0
	mr.unknown = len(d.QueryType) > 0 || len(d.SourceIPCIDR) > 0 || len(d.SourcePort) > 0 || len(d.SourcePortRange) > 0 ||
		len(d.PackageName) > 0 || len(d.PackageNameRegex) > 0 || len(d.NetworkType) > 0 ||
		d.NetworkIsExpensive || d.NetworkIsConstrained || len(d.WIFISSID) > 0 || len(d.WIFIBSSID) > 0 ||
		d.NetworkInterfaceAddress != nil || len(d.DefaultInterfaceAddress) > 0
	return mr, nil
}

func parsePortRange(v string) (lo, hi uint16, ok bool) {
	a, b, found := strings.Cut(v, ":")
	if !found {
		return 0, 0, false
	}
	lo, hi = 0, 65535
	if a != "" {
		n, err := strconv.ParseUint(a, 10, 16)
		if err != nil {
			return 0, 0, false
		}
		lo = uint16(n)
	}
	if b != "" {
		n, err := strconv.ParseUint(b, 10, 16)
		if err != nil {
			return 0, 0, false
		}
		hi = uint16(n)
	}
	return lo, hi, lo <= hi
}

func (r *mrule) eval(q Query) Verdict {
	var v Verdict
	if r.logical {
		unknown := false
		if r.and {
			v = Match
			for _, s := range r.subs {
				switch s.eval(q) {
				case NoMatch:
					v = NoMatch
				case Unknown:
					unknown = true
				}
				if v == NoMatch {
					break
				}
			}
			if v == Match && unknown {
				v = Unknown
			}
		} else {
			v = NoMatch
			for _, s := range r.subs {
				switch s.eval(q) {
				case Match:
					v = Match
				case Unknown:
					unknown = true
				}
				if v == Match {
					break
				}
			}
			if v == NoMatch && unknown {
				v = Unknown
			}
		}
	} else {
		v = r.evalDefault(q)
	}
	if r.invert {
		switch v {
		case Match:
			v = NoMatch
		case NoMatch:
			v = Match
		}
	}
	return v
}

func (r *mrule) evalDefault(q Query) Verdict {
	unknown := r.unknown
	if r.hasName || r.hasIP {
		matched := r.hasName && q.Domain != "" && r.matchName(q.Domain)
		if !matched && r.hasIP {
			for _, a := range q.Addrs {
				if r.matchIP(a) {
					matched = true
					break
				}
			}
			if !matched && len(q.Addrs) == 0 {
				unknown = true // the address could decide it
				matched = true // not a miss yet
			}
		}
		if !matched {
			return NoMatch
		}
	}
	if r.hasPort {
		if q.Port == 0 {
			unknown = true
		} else if !r.matchPort(q.Port) {
			return NoMatch
		}
	}
	if len(r.network) > 0 {
		if q.Network == "" {
			unknown = true
		} else {
			hit := false
			for _, n := range r.network {
				if strings.EqualFold(n, q.Network) {
					hit = true
				}
			}
			if !hit {
				return NoMatch
			}
		}
	}
	if r.hasProc {
		switch {
		case q.Process == "" && q.ProcessPath == "":
			unknown = true
		case !r.matchProcess(q):
			return NoMatch
		}
	}
	if unknown {
		return Unknown
	}
	return Match
}

// matchProcess is true when the program is one the rule names, by its file
// name, its whole path or a pattern for the path. Rules that name programs in
// more than one way match any of them, as the core's rule items do.
func (r *mrule) matchProcess(q Query) bool {
	name := q.Process
	if name == "" {
		name = q.ProcessPath[strings.LastIndexAny(q.ProcessPath, `/\`)+1:]
	}
	for _, v := range r.procName {
		if v == name {
			return true
		}
	}
	for _, v := range r.procPath {
		if v == q.ProcessPath {
			return true
		}
	}
	for _, re := range r.procRe {
		if re.MatchString(q.ProcessPath) {
			return true
		}
	}
	return false
}

func (r *mrule) matchName(d string) bool {
	if _, ok := r.exact[d]; ok {
		return true
	}
	if r.suffix != nil {
		for i := 0; ; {
			cand := d[i:]
			if _, ok := r.suffix[cand]; ok {
				return true
			}
			if i > 0 {
				if _, ok := r.subOnly[d[i-1:]]; ok {
					return true
				}
			}
			j := strings.IndexByte(cand, '.')
			if j < 0 {
				break
			}
			i += j + 1
		}
	}
	for _, k := range r.keyword {
		if strings.Contains(d, k) {
			return true
		}
	}
	for _, re := range r.regex {
		if re.MatchString(d) {
			return true
		}
	}
	return r.adguard != nil && r.adguard.Match(d)
}

func (r *mrule) matchIP(a netip.Addr) bool {
	a = a.Unmap()
	for _, p := range r.prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func (r *mrule) matchPort(p uint16) bool {
	if _, ok := r.ports[p]; ok {
		return true
	}
	for _, rg := range r.ranges {
		if p >= rg[0] && p <= rg[1] {
			return true
		}
	}
	return false
}
