package rulefmt

import (
	"bufio"
	"bytes"
	"regexp"
	"strconv"
	"strings"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	"go.yaml.in/yaml/v3"
)

// parseClashYAML reads a Clash rule-provider file. The behavior (domain,
// ipcidr, classical) is not in the file, so it is inferred from the items.
func parseClashYAML(data []byte, style Style) (*Parsed, error) {
	var doc struct {
		Payload []string `yaml:"payload"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, ErrUnrecognized
	}
	b := &builder{}
	behavior := inferBehavior(doc.Payload)
	for _, item := range doc.Payload {
		switch behavior {
		case "classical":
			b.addClassical(item, style)
		case "ipcidr":
			if !b.addCIDR(&b.ipcidr, item) {
				b.ignored++
			}
		default:
			b.addDomainLine(item, style)
		}
	}
	p, err := b.finish(FormatClashYAML)
	if p != nil {
		p.Behavior = behavior
	}
	return p, err
}

// inferBehavior decides what a provider's payload items are, by the majority
// of the first few hundred.
func inferBehavior(items []string) string {
	var classical, cidr, dom int
	for i, it := range items {
		if i >= 500 {
			break
		}
		s := strings.TrimSpace(it)
		switch {
		case s == "":
		case reClassical.MatchString(s):
			classical++
		case isCIDRLine(s):
			cidr++
		default:
			dom++
		}
	}
	switch {
	case classical >= cidr && classical >= dom && classical > 0:
		return "classical"
	case cidr > dom:
		return "ipcidr"
	default:
		return "domain"
	}
}

func parseClassicalText(data []byte, style Style) (*Parsed, error) {
	b := &builder{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		b.addClassical(sc.Text(), style)
	}
	return b.finish(FormatClassical)
}

// cleanLine strips the decoration lists carry around an entry: a YAML dash,
// quotes, a trailing comment. It returns "" for comments and blank lines.
func cleanLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasPrefix(s, "#") || strings.HasPrefix(s, ";") || strings.HasPrefix(s, "//") {
		return ""
	}
	if strings.HasPrefix(s, "- ") {
		s = strings.TrimSpace(s[2:])
	}
	if len(s) >= 2 && (s[0] == '\'' || s[0] == '"') && s[len(s)-1] == s[0] {
		s = s[1 : len(s)-1]
	}
	if i := strings.Index(s, " #"); i > 0 {
		s = strings.TrimSpace(s[:i])
	}
	return s
}

// addClassical adds one `TYPE,value[,flags]` line. Policies and flags after
// the value (Proxy, no-resolve) do not matter to a rule set and are dropped.
func (b *builder) addClassical(line string, style Style) {
	s := cleanLine(line)
	if s == "" {
		return
	}
	typ, rest, ok := strings.Cut(s, ",")
	if !ok {
		b.ignored++
		return
	}
	typ = strings.ToUpper(strings.TrimSpace(typ))
	switch typ {
	case "AND", "OR", "NOT":
		r, name, ok := parseLogical(typ, rest, style)
		if !ok {
			b.unsupport(name)
			return
		}
		b.rules = append(b.rules, r)
		return
	case "GEOSITE", "GEOIP", "IP-ASN":
		b.addRef(typ, firstField(rest))
		return
	}
	leaf, ok, name := leafRule(typ, firstField(rest), style)
	if !ok {
		if name != "" {
			b.unsupport(name)
		} else {
			b.ignored++
		}
		return
	}
	d := leaf.DefaultOptions
	b.domain = append(b.domain, d.Domain...)
	b.suffix = append(b.suffix, d.DomainSuffix...)
	b.keyword = append(b.keyword, d.DomainKeyword...)
	b.regex = append(b.regex, d.DomainRegex...)
	b.ipcidr = append(b.ipcidr, d.IPCIDR...)
	b.srcCIDR = append(b.srcCIDR, d.SourceIPCIDR...)
	b.port = append(b.port, d.Port...)
	b.portRange = append(b.portRange, d.PortRange...)
	b.srcPort = append(b.srcPort, d.SourcePort...)
	b.srcPortRange = append(b.srcPortRange, d.SourcePortRange...)
	b.procName = append(b.procName, d.ProcessName...)
	b.procPath = append(b.procPath, d.ProcessPath...)
	b.procRegex = append(b.procRegex, d.ProcessPathRegex...)
	b.network = append(b.network, d.Network...)
}

func firstField(rest string) string {
	v, _, _ := strings.Cut(rest, ",")
	return strings.TrimSpace(v)
}

func (b *builder) addRef(typ, v string) {
	switch typ {
	case "GEOSITE":
		if v == "" {
			b.ignored++
			return
		}
		if strings.Contains(v, "@") { // geosite attributes (google@cn) are not in the sing-box files
			b.unsupport("GEOSITE@attribute")
			return
		}
		b.refs = append(b.refs, Ref{Kind: RefGeoSite, Name: strings.ToLower(v)})
	case "GEOIP":
		if v == "" {
			b.ignored++
			return
		}
		b.refs = append(b.refs, Ref{Kind: RefGeoIP, Name: strings.ToLower(v)})
	case "IP-ASN":
		n := strings.TrimPrefix(strings.ToUpper(v), "AS")
		if _, err := strconv.ParseUint(n, 10, 32); err != nil {
			b.ignored++
			return
		}
		b.refs = append(b.refs, Ref{Kind: RefASN, Name: n})
	}
}

// leafRule converts one single-condition Clash/Surge rule. When the type is
// known but cannot be expressed, name says which, so it can be counted.
func leafRule(typ, v string, style Style) (rule option.HeadlessRule, ok bool, unsupported string) {
	def := func(d option.DefaultHeadlessRule) (option.HeadlessRule, bool, string) {
		return option.HeadlessRule{Type: C.RuleTypeDefault, DefaultOptions: d}, true, ""
	}
	if v == "" {
		switch typ {
		case "MATCH", "FINAL":
			return rule, false, ""
		}
	}
	switch typ {
	case "DOMAIN":
		if !isDomainLike(v) {
			return rule, false, ""
		}
		return def(option.DefaultHeadlessRule{Domain: []string{strings.ToLower(v)}})
	case "DOMAIN-SUFFIX":
		v = strings.ToLower(strings.TrimPrefix(v, "+."))
		if !isDomainLike(strings.TrimPrefix(v, ".")) {
			return rule, false, ""
		}
		return def(option.DefaultHeadlessRule{DomainSuffix: []string{v}})
	case "DOMAIN-KEYWORD":
		if v == "" {
			return rule, false, ""
		}
		return def(option.DefaultHeadlessRule{DomainKeyword: []string{strings.ToLower(v)}})
	case "DOMAIN-REGEX":
		if _, err := regexp.Compile(v); err != nil {
			return rule, false, ""
		}
		return def(option.DefaultHeadlessRule{DomainRegex: []string{v}})
	case "DOMAIN-WILDCARD":
		re := wildcardRegex(strings.ToLower(v), ".*", ".")
		return def(option.DefaultHeadlessRule{DomainRegex: []string{re}})
	case "IP-CIDR", "IP-CIDR6":
		var tmp builder
		if !tmp.addCIDR(&tmp.ipcidr, v) {
			return rule, false, ""
		}
		return def(option.DefaultHeadlessRule{IPCIDR: tmp.ipcidr})
	case "SRC-IP-CIDR", "SRC-IP":
		var tmp builder
		if !tmp.addCIDR(&tmp.srcCIDR, v) {
			return rule, false, ""
		}
		return def(option.DefaultHeadlessRule{SourceIPCIDR: tmp.srcCIDR})
	case "DST-PORT", "SRC-PORT":
		ports, ranges, ok := parsePorts(v)
		if !ok {
			return rule, false, ""
		}
		if typ == "SRC-PORT" {
			return def(option.DefaultHeadlessRule{SourcePort: ports, SourcePortRange: ranges})
		}
		return def(option.DefaultHeadlessRule{Port: ports, PortRange: ranges})
	case "PROCESS-NAME":
		if v == "" {
			return rule, false, ""
		}
		return def(option.DefaultHeadlessRule{ProcessName: []string{v}})
	case "PROCESS-PATH":
		if v == "" {
			return rule, false, ""
		}
		return def(option.DefaultHeadlessRule{ProcessPath: []string{v}})
	case "NETWORK":
		switch strings.ToLower(v) {
		case "tcp", "udp":
			return def(option.DefaultHeadlessRule{Network: []string{strings.ToLower(v)}})
		}
		return rule, false, ""
	case "MATCH", "FINAL":
		return rule, false, ""
	}
	return rule, false, typ
}

// wildcardRegex turns a glob into an anchored regular expression, replacing
// `*` and `?` with the given expressions.
func wildcardRegex(glob, star, one string) string {
	var sb strings.Builder
	sb.WriteByte('^')
	for _, r := range glob {
		switch r {
		case '*':
			sb.WriteString(star)
		case '?':
			sb.WriteString(one)
		default:
			sb.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	sb.WriteByte('$')
	return sb.String()
}

// parsePorts reads "443", "80-90", "80/443" and "80,443".
func parsePorts(v string) (ports []uint16, ranges []string, ok bool) {
	for _, part := range strings.FieldsFunc(v, func(r rune) bool { return r == '/' || r == ',' || r == ' ' }) {
		if lo, hi, isRange := strings.Cut(part, "-"); isRange {
			l, e1 := strconv.ParseUint(strings.TrimSpace(lo), 10, 16)
			h, e2 := strconv.ParseUint(strings.TrimSpace(hi), 10, 16)
			if e1 != nil || e2 != nil || l > h {
				return nil, nil, false
			}
			ranges = append(ranges, strconv.FormatUint(l, 10)+":"+strconv.FormatUint(h, 10))
			continue
		}
		n, err := strconv.ParseUint(part, 10, 16)
		if err != nil {
			return nil, nil, false
		}
		ports = append(ports, uint16(n))
	}
	return ports, ranges, len(ports)+len(ranges) > 0
}

// parseLogical reads AND/OR/NOT with their parenthesised operands:
//
//	AND,((DOMAIN,a.com),(NETWORK,UDP))
//
// What follows the operands (a policy) is dropped.
func parseLogical(typ, rest string, style Style) (option.HeadlessRule, string, bool) {
	name := typ
	operands, ok := splitOperands(rest)
	if !ok || len(operands) == 0 {
		return option.HeadlessRule{}, name, false
	}
	var subs []option.HeadlessRule
	for _, op := range operands {
		t, r, ok := strings.Cut(op, ",")
		if !ok {
			return option.HeadlessRule{}, name, false
		}
		t = strings.ToUpper(strings.TrimSpace(t))
		switch t {
		case "AND", "OR", "NOT":
			sub, subName, ok := parseLogical(t, r, style)
			if !ok {
				return option.HeadlessRule{}, subName, false
			}
			subs = append(subs, sub)
		default:
			leaf, ok, un := leafRule(t, firstField(r), style)
			if !ok {
				if un == "" {
					un = name
				}
				return option.HeadlessRule{}, un, false
			}
			subs = append(subs, leaf)
		}
	}
	lr := option.LogicalHeadlessRule{Mode: C.LogicalTypeAnd, Rules: subs}
	switch typ {
	case "OR":
		lr.Mode = C.LogicalTypeOr
	case "NOT":
		if len(subs) != 1 {
			return option.HeadlessRule{}, name, false
		}
		lr.Invert = true
	}
	return option.HeadlessRule{Type: C.RuleTypeLogical, LogicalOptions: lr}, "", true
}

// splitOperands takes "((A,x),(B,y)),POLICY" and returns ["A,x", "B,y"].
func splitOperands(s string) ([]string, bool) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "(") {
		return nil, false
	}
	depth := 0
	var out []string
	start := -1
	end := -1
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
			if depth == 2 {
				start = i + 1
			}
		case ')':
			if depth == 2 && start >= 0 {
				out = append(out, strings.TrimSpace(s[start:i]))
				start = -1
			}
			depth--
			if depth == 0 {
				end = i
			}
		}
		if end >= 0 {
			break
		}
	}
	if end < 0 || depth != 0 {
		return nil, false
	}
	return out, true
}
