package rulefmt

import (
	"bytes"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strings"

	"github.com/sagernet/sing-box/common/srs"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	singjson "github.com/sagernet/sing/common/json"
)

// StyleFor chooses how a leading dot in a domain list reads, from where the
// list comes. Surge's own projects and any path that names surge mean Surge;
// everything else, including paths that name clash and sources nobody can
// place, means Clash. When a path names both, the one nearer the file name
// wins ("rule/Surge/x" and "clash-rules/surge-style/x" differ that way).
func StyleFor(source string) Style {
	s := strings.ToLower(source)
	surge := max(strings.LastIndex(s, "surge"), strings.LastIndex(s, "skk.moe"))
	clash := strings.LastIndex(s, "clash")
	if surge > clash {
		return StyleSurge
	}
	return StyleClash
}

func (h Hint) style() Style {
	if h.Style != nil {
		return *h.Style
	}
	return StyleFor(h.Source)
}

// ErrMalformed means a file looked like a known format and was not. The
// decoders of binary formats can panic on hostile input; that is turned into
// this error, because every file here comes from the network.
var ErrMalformed = errors.New("malformed rule file")

// Parse reads one downloaded file in whatever format it is.
func Parse(data []byte, hint Hint) (p *Parsed, err error) {
	defer func() {
		if r := recover(); r != nil {
			p, err = nil, fmt.Errorf("%w: %v", ErrMalformed, r)
		}
	}()
	return parse(data, hint)
}

func parse(data []byte, hint Hint) (*Parsed, error) {
	data = bytes.TrimPrefix(data, bom)
	switch f := Detect(data); f {
	case FormatSRS:
		return parseSRS(data)
	case FormatSingBox:
		return parseSingBoxJSON(data)
	case FormatMRS:
		return nil, ErrMRS
	case FormatClashYAML:
		return parseClashYAML(data, hint.style())
	case FormatClassical:
		return parseClassicalText(data, hint.style())
	case FormatAdGuard:
		return parseAdGuard(data)
	case FormatHosts:
		return parseHosts(data)
	case FormatDomainList:
		return parseDomainList(data, hint.style())
	case FormatCIDRList:
		return parseCIDRList(data)
	default:
		return nil, ErrUnrecognized
	}
}

func parseSRS(data []byte) (*Parsed, error) {
	compat, err := srs.Read(bytes.NewReader(data), true)
	if err != nil {
		return nil, fmt.Errorf("read sing-box rule set: %w", err)
	}
	rules, dropped := normalize(compat.Options.Rules)
	if len(rules) == 0 {
		return nil, ErrEmpty
	}
	st := Count(rules)
	st.Ignored = dropped
	return &Parsed{Format: FormatSRS, Rules: rules, Stats: st}, nil
}

func parseSingBoxJSON(data []byte) (*Parsed, error) {
	compat, err := singjson.UnmarshalExtended[option.PlainRuleSetCompat](data)
	if err != nil {
		return nil, fmt.Errorf("read sing-box rule set: %w", err)
	}
	set, err := compat.Upgrade()
	if err != nil {
		return nil, err
	}
	rules, dropped := normalize(set.Rules)
	if len(rules) == 0 {
		return nil, ErrEmpty
	}
	st := Count(rules)
	st.Ignored = dropped
	return &Parsed{Format: FormatSingBox, Rules: rules, Stats: st}, nil
}

// normalize gives every rule an explicit type, drops the entries the core
// would refuse to load (a bad regular expression, a malformed port range or
// CIDR, an empty name) and the rules left with nothing to match. A file from
// the network must never be able to make the whole rule set fail to load.
// The second result counts what was dropped.
func normalize(in []option.HeadlessRule) ([]option.HeadlessRule, int) {
	dropped := 0
	out := make([]option.HeadlessRule, 0, len(in))
	for _, r := range in {
		clean, n, ok := sanitizeRule(r)
		dropped += n
		if ok {
			out = append(out, clean)
		} else {
			dropped++
		}
	}
	return out, dropped
}

func sanitizeRule(r option.HeadlessRule) (option.HeadlessRule, int, bool) {
	switch r.Type {
	case "", C.RuleTypeDefault:
		d, n, ok := sanitizeDefault(r.DefaultOptions)
		if !ok {
			return r, n, false
		}
		return option.HeadlessRule{Type: C.RuleTypeDefault, DefaultOptions: d}, n, true
	case C.RuleTypeLogical:
		l := r.LogicalOptions
		if l.Mode != C.LogicalTypeAnd && l.Mode != C.LogicalTypeOr {
			return r, 0, false
		}
		dropped := 0
		subs := make([]option.HeadlessRule, 0, len(l.Rules))
		for _, s := range l.Rules {
			c, n, ok := sanitizeRule(s)
			dropped += n
			if ok {
				subs = append(subs, c)
				continue
			}
			// A part that can never match sinks an AND and any inversion; in a
			// plain OR it is only one alternative less.
			if l.Mode == C.LogicalTypeAnd || l.Invert {
				return r, dropped, false
			}
			dropped++
		}
		if len(subs) == 0 {
			return r, dropped, false
		}
		l.Rules = subs
		return option.HeadlessRule{Type: C.RuleTypeLogical, LogicalOptions: l}, dropped, true
	}
	return r, 0, false
}

func sanitizeDefault(d option.DefaultHeadlessRule) (option.DefaultHeadlessRule, int, bool) {
	dropped := 0
	// Entries of one list are alternatives: filter them, and the rule only
	// dies when a whole group of alternatives has nothing left.
	keep := func(in []string, ok func(string) bool) []string {
		var out []string
		for _, v := range in {
			if ok(v) {
				out = append(out, v)
			} else {
				dropped++
			}
		}
		return out
	}
	nonEmpty := func(v string) bool { return v != "" }
	goodRegex := func(v string) bool { _, err := regexp.Compile(v); return err == nil }
	goodCIDR := func(v string) bool { return validCIDR(v) }
	goodRange := func(v string) bool { _, _, ok := parsePortRange(v); return ok }
	goodNetwork := func(v string) bool { return v == "tcp" || v == "udp" || v == "icmp" }

	addrBefore := len(d.Domain) + len(d.DomainSuffix) + len(d.DomainKeyword) + len(d.DomainRegex) + len(d.AdGuardDomain) + len(d.IPCIDR)
	d.Domain = keep(d.Domain, nonEmpty)
	d.DomainSuffix = keep(d.DomainSuffix, nonEmpty)
	d.DomainKeyword = keep(d.DomainKeyword, nonEmpty)
	d.DomainRegex = keep(d.DomainRegex, goodRegex)
	d.AdGuardDomain = keep(d.AdGuardDomain, nonEmpty)
	d.IPCIDR = keep(d.IPCIDR, goodCIDR)
	addrAfter := len(d.Domain) + len(d.DomainSuffix) + len(d.DomainKeyword) + len(d.DomainRegex) + len(d.AdGuardDomain) + len(d.IPCIDR)
	if addrBefore > 0 && addrAfter == 0 {
		return d, dropped, false
	}
	portBefore, srcBefore := len(d.PortRange), len(d.SourcePortRange)
	groupPort, groupSrc := len(d.Port)+portBefore, len(d.SourcePort)+srcBefore
	d.PortRange = keep(d.PortRange, goodRange)
	d.SourcePortRange = keep(d.SourcePortRange, goodRange)
	if (groupPort > 0 && len(d.Port)+len(d.PortRange) == 0) || (groupSrc > 0 && len(d.SourcePort)+len(d.SourcePortRange) == 0) {
		return d, dropped, false
	}
	before := len(d.SourceIPCIDR) + len(d.ProcessPathRegex) + len(d.PackageNameRegex) + len(d.Network)
	d.SourceIPCIDR = keep(d.SourceIPCIDR, goodCIDR)
	d.ProcessPathRegex = keep(d.ProcessPathRegex, goodRegex)
	d.PackageNameRegex = keep(d.PackageNameRegex, goodRegex)
	d.Network = keep(d.Network, goodNetwork)
	if before > 0 && (len(d.SourceIPCIDR) == 0 && len(d.ProcessPathRegex) == 0 && len(d.PackageNameRegex) == 0 && len(d.Network) == 0) {
		// every one of these conditions was bad; a rule with none left would
		// match more than it was written to
		return d, dropped, false
	}
	d.ProcessName = keep(d.ProcessName, nonEmpty)
	d.ProcessPath = keep(d.ProcessPath, nonEmpty)
	d.PackageName = keep(d.PackageName, nonEmpty)
	// The matchers the .srs reader builds are rebuilt from the lists on write.
	d.DomainMatcher, d.AdGuardDomainMatcher, d.IPSet, d.SourceIPSet = nil, nil, nil, nil
	if !d.IsValid() {
		return d, dropped, false
	}
	return d, dropped, true
}

func validCIDR(v string) bool {
	if _, err := netip.ParsePrefix(v); err == nil {
		return true
	}
	_, err := netip.ParseAddr(v)
	return err == nil
}

// Count tallies a rule list in user terms. Logical rules count what their
// parts hold.
func Count(rules []option.HeadlessRule) Stats {
	var s Stats
	var walk func(rs []option.HeadlessRule)
	walk = func(rs []option.HeadlessRule) {
		for _, r := range rs {
			if r.Type == C.RuleTypeLogical {
				walk(r.LogicalOptions.Rules)
				continue
			}
			d := r.DefaultOptions
			s.Domain += len(d.Domain) + len(d.DomainSuffix) + len(d.DomainKeyword) + len(d.DomainRegex) + len(d.AdGuardDomain)
			s.IP += len(d.IPCIDR)
			s.Other += len(d.SourceIPCIDR) + len(d.SourcePort) + len(d.SourcePortRange) + len(d.Port) + len(d.PortRange) +
				len(d.ProcessName) + len(d.ProcessPath) + len(d.ProcessPathRegex) + len(d.PackageName) + len(d.PackageNameRegex) +
				len(d.Network) + len(d.QueryType)
		}
	}
	walk(rules)
	return s
}

// builder collects the single-condition lines of the list formats. Values of
// one kind are ORed inside one sing-box rule, so a thousand DOMAIN-SUFFIX
// lines become one rule with a thousand suffixes, not a thousand rules.
type builder struct {
	domain, suffix, keyword, regex []string
	ipcidr, srcCIDR                []string
	port, srcPort                  []uint16
	portRange, srcPortRange        []string
	procName, procPath, procRegex  []string
	network                        []string
	rules                          []option.HeadlessRule // logical rules, kept whole
	refs                           []Ref
	unsupported                    map[string]int
	ignored                        int
}

func (b *builder) unsupport(kind string) {
	if b.unsupported == nil {
		b.unsupported = map[string]int{}
	}
	b.unsupported[kind]++
}

func (b *builder) addCIDR(dst *[]string, v string) bool {
	v = strings.TrimSpace(v)
	if p, err := netip.ParsePrefix(v); err == nil {
		*dst = append(*dst, p.Masked().String())
		return true
	}
	if a, err := netip.ParseAddr(v); err == nil {
		*dst = append(*dst, netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()).String())
		return true
	}
	return false
}

func (b *builder) finish(f Format) (*Parsed, error) {
	var rules []option.HeadlessRule
	def := func(d option.DefaultHeadlessRule) {
		rules = append(rules, option.HeadlessRule{Type: C.RuleTypeDefault, DefaultOptions: d})
	}
	if len(b.domain)+len(b.suffix)+len(b.keyword)+len(b.regex)+len(b.ipcidr) > 0 {
		def(option.DefaultHeadlessRule{
			Domain: uniq(b.domain), DomainSuffix: uniq(b.suffix), DomainKeyword: uniq(b.keyword),
			DomainRegex: uniq(b.regex), IPCIDR: uniq(b.ipcidr),
		})
	}
	if len(b.srcCIDR) > 0 {
		def(option.DefaultHeadlessRule{SourceIPCIDR: uniq(b.srcCIDR)})
	}
	if len(b.port)+len(b.portRange) > 0 {
		def(option.DefaultHeadlessRule{Port: uniq(b.port), PortRange: uniq(b.portRange)})
	}
	if len(b.srcPort)+len(b.srcPortRange) > 0 {
		def(option.DefaultHeadlessRule{SourcePort: uniq(b.srcPort), SourcePortRange: uniq(b.srcPortRange)})
	}
	if len(b.procName) > 0 {
		def(option.DefaultHeadlessRule{ProcessName: uniq(b.procName)})
	}
	if len(b.procPath) > 0 {
		def(option.DefaultHeadlessRule{ProcessPath: uniq(b.procPath)})
	}
	if len(b.procRegex) > 0 {
		def(option.DefaultHeadlessRule{ProcessPathRegex: uniq(b.procRegex)})
	}
	if len(b.network) > 0 {
		def(option.DefaultHeadlessRule{Network: uniq(b.network)})
	}
	rules = append(rules, b.rules...)
	stats := Count(rules)
	stats.Unsupported = b.unsupported
	stats.Ignored = b.ignored
	refs := slices.Clone(b.refs)
	slices.SortFunc(refs, func(a, c Ref) int {
		if a.Kind != c.Kind {
			return strings.Compare(string(a.Kind), string(c.Kind))
		}
		return strings.Compare(a.Name, c.Name)
	})
	refs = slices.Compact(refs)
	if len(rules) == 0 && len(refs) == 0 {
		return nil, ErrEmpty
	}
	return &Parsed{Format: f, Rules: rules, Refs: refs, Stats: stats}, nil
}

// uniq returns a sorted copy without duplicates and without empty values (the
// core's domain matcher panics on an empty suffix), so equal inputs compile
// to equal files whatever order the lists came in.
func uniq[T ~string | ~uint16](in []T) []T {
	out := make([]T, 0, len(in))
	var zero T
	for _, v := range in {
		if v != zero {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// validRegexes keeps the expressions Go can compile. The core refuses a whole
// rule set for one bad expression, and community lists do contain them.
func validRegexes(in []string) []string {
	out := in[:0:0]
	for _, v := range in {
		if _, err := regexp.Compile(v); err == nil {
			out = append(out, v)
		}
	}
	return out
}
