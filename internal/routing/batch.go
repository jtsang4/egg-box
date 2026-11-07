package routing

import (
	"fmt"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
)

// BatchLine is one line of a pasted list that was understood.
type BatchLine struct {
	// Line is the line's number in the pasted text, from 1.
	Line  int
	Entry Entry
	// Target is what the line said to do with it, as written ("DIRECT",
	// "REJECT", the name of a group); empty when the line said nothing.
	Target string
	// Disabled is set for a rule that was commented out ("# DOMAIN,…"), which
	// only the exceptions editor reads: everywhere else it is a comment.
	Disabled bool
	// Text is the line as it was written, shortened.
	Text string
}

// BatchBad is a line that was not understood, or that cannot be used.
type BatchBad struct {
	Line int
	Text string
	// Reason says why in English, for logs and tests.
	Reason string
	// Code says why in a form the interface words itself (one of the Bad…
	// codes); Arg and Choices are what the words need.
	Code    string
	Arg     string
	Choices []string
}

// Why a line of a list cannot be used.
const (
	// BadSyntax: not a rule the app can read.
	BadSyntax = "syntax"
	// BadType: a rule type that is not supported here; Arg is the type.
	BadType = "type"
	// BadDuplicate: the same match as an earlier line; Arg is that line's number.
	BadDuplicate = "duplicate"
	// BadTarget: a destination nothing answers to; Arg is the destination.
	BadTarget = "target"
	// BadAmbiguous: a node name more than one subscription has; Arg is the
	// name, Choices the ways to write each node.
	BadAmbiguous = "ambiguous"
	// BadNoNode: no node has the name; Arg is the name.
	BadNoNode = "no_node"
	// BadRegion: a region code that is not known; Arg is the code.
	BadRegion = "region"
)

// unsupportedType is the reason a rule of a type that is not supported here
// cannot be used.
type unsupportedType string

func (t unsupportedType) Error() string { return string(t) + " rules are not supported here" }

// BatchResult is what a pasted list came to, to be shown before anything is
// added.
type BatchResult struct {
	Lines        []BatchLine
	Unrecognized []BatchBad
	// Counts says how many lines there are of each kind.
	Counts map[EntryKind]int
}

// Targets lists the distinct targets the lines named, in order of appearance.
func (r BatchResult) Targets() []string {
	var out []string
	seen := map[string]bool{}
	for _, l := range r.Lines {
		if l.Target != "" && !seen[l.Target] {
			seen[l.Target] = true
			out = append(out, l.Target)
		}
	}
	return out
}

var hostnamePattern = regexp.MustCompile(`^[a-z0-9_]([a-z0-9_-]*[a-z0-9_])?(\.[a-z0-9_]([a-z0-9_-]*[a-z0-9_])?)*\.?$`)

// clashTypes maps the rule types of the Clash syntax a person may paste to
// entry kinds.
var clashTypes = map[string]EntryKind{
	"DOMAIN":         EntryDomain,
	"DOMAIN-SUFFIX":  EntryDomainSuffix,
	"DOMAIN-KEYWORD": EntryDomainKeyword,
	"DOMAIN-REGEX":   EntryDomainRegex,
	"IP-CIDR":        EntryIPCIDR,
	"IP-CIDR6":       EntryIPCIDR,
	"DST-PORT":       EntryPort,
	"PROCESS-NAME":   EntryProcessName,
	"PROCESS-PATH":   EntryProcessPath,
}

// ParseBatch reads a pasted list: one entry per line, either written plainly
// (a domain, an address range, a port, a program) or in the Clash syntax,
// `DOMAIN-SUFFIX,example.com,DIRECT`. A field may be put in double quotes
// when it holds a comma (`PROCESS-PATH,"/Apps/A, B.app/a",DIRECT`; a quote
// inside is written twice). Blank lines, comments and the `payload:` line of a
// rule file are skipped. Nothing here fails: what cannot be read is returned,
// so it can be shown.
func ParseBatch(text string) BatchResult { return parseList(text, false) }

// parseList reads a list. With disabled, a line that is a whole Clash rule
// commented out with "#" is read as a rule that is switched off.
func parseList(text string, disabled bool) BatchResult {
	res := BatchResult{Counts: map[EntryKind]int{}}
	for i, raw := range listLines(text) {
		n := i + 1
		line := strings.TrimSpace(strings.TrimPrefix(raw, "\ufeff"))
		if disabled && strings.HasPrefix(line, "#") {
			// A rule commented out is a rule switched off; anything else
			// after "#" is a comment.
			rest := strings.TrimSpace(strings.TrimLeft(line, "#"))
			if looksLikeRule(rest) {
				entry, target, err := parseClashLine(rest)
				if err != nil {
					res.Unrecognized = append(res.Unrecognized, BatchBad{Line: n, Text: clip(raw, 120), Reason: err.Error(), Code: BadSyntax})
					continue
				}
				res.Lines = append(res.Lines, BatchLine{Line: n, Entry: entry, Target: target, Disabled: true, Text: clip(raw, 120)})
				res.Counts[entry.Kind]++
				continue
			}
		}
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.EqualFold(strings.TrimRight(line, " "), "payload:") {
			continue
		}
		// An entry of a YAML list: `- '+.example.com'`.
		if rest, ok := strings.CutPrefix(line, "- "); ok {
			line = strings.TrimSpace(rest)
		}
		line = trimQuotes(line)
		if line == "" {
			continue
		}
		entry, target, err := parseBatchLine(line)
		if err != nil {
			bad := BatchBad{Line: n, Text: clip(raw, 120), Reason: err.Error(), Code: BadSyntax}
			if t, ok := err.(unsupportedType); ok {
				bad.Code, bad.Arg = BadType, string(t)
			}
			res.Unrecognized = append(res.Unrecognized, bad)
			continue
		}
		res.Lines = append(res.Lines, BatchLine{Line: n, Entry: entry, Target: target, Text: clip(raw, 120)})
		res.Counts[entry.Kind]++
	}
	return res
}

// listLines splits a list into its lines.
func listLines(text string) []string {
	return strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
}

func trimQuotes(s string) string {
	if len(s) >= 2 && (s[0] == '\'' || s[0] == '"') && s[len(s)-1] == s[0] {
		return strings.TrimSpace(s[1 : len(s)-1])
	}
	return s
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func parseBatchLine(line string) (Entry, string, error) {
	if strings.Contains(line, ",") {
		return parseClashLine(line)
	}
	e, err := guessEntry(line)
	return e, "", err
}

// field is one comma-separated part of a Clash line.
type field struct {
	text   string
	quoted bool
	// start is where the field begins in the line.
	start int
}

// splitFields splits a line at its commas. A field that starts with a double
// quote runs to the closing quote and may hold commas; a quote inside it is
// written twice. A quote that does not close is kept as it is.
func splitFields(s string) []field {
	var out []field
	i := 0
	for {
		j := i
		for j < len(s) && (s[j] == ' ' || s[j] == '\t') {
			j++
		}
		if j < len(s) && s[j] == '"' {
			if text, end, ok := readQuoted(s, j); ok {
				k := end
				for k < len(s) && (s[k] == ' ' || s[k] == '\t') {
					k++
				}
				if k == len(s) || s[k] == ',' {
					out = append(out, field{text: text, quoted: true, start: i})
					if k == len(s) {
						return out
					}
					i = k + 1
					continue
				}
			}
		}
		k := strings.IndexByte(s[i:], ',')
		if k < 0 {
			return append(out, field{text: strings.TrimSpace(s[i:]), start: i})
		}
		out = append(out, field{text: strings.TrimSpace(s[i : i+k]), start: i})
		i += k + 1
	}
}

// readQuoted reads the quoted text that starts at s[at]: what it says, and
// where it ends.
func readQuoted(s string, at int) (string, int, bool) {
	var b strings.Builder
	for p := at + 1; p < len(s); p++ {
		if s[p] != '"' {
			b.WriteByte(s[p])
			continue
		}
		if p+1 < len(s) && s[p+1] == '"' {
			b.WriteByte('"')
			p++
			continue
		}
		return b.String(), p + 1, true
	}
	return "", 0, false
}

// regexChars are the characters a regular expression is made of: text after
// a regex's last comma that has none of them is the destination, not a part
// of the expression.
const regexChars = `\^$()[]{}*+?|`

// looksLikeRule reports whether text after "#" is a rule rather than words:
// a known type, then fields with no space after their commas, which is how
// rules are written and sentences are not.
func looksLikeRule(s string) bool {
	fs := splitFields(s)
	if _, known := clashTypes[strings.ToUpper(fs[0].text)]; !known || len(fs) < 2 {
		return false
	}
	for i, f := range fs[1:] {
		if i > 0 && hasPrefixFold(strings.TrimSpace(s[f.start:]), "NODE:") {
			break // a node's name, the destination, may have ", " in it
		}
		if f.start < len(s) && (s[f.start] == ' ' || s[f.start] == '\t') {
			return false
		}
	}
	return true
}

// cutNoResolve takes a trailing ",no-resolve" off a destination.
func cutNoResolve(s string) string {
	const word = "no-resolve"
	t := strings.TrimSpace(s)
	if len(t) > len(word) && strings.EqualFold(t[len(t)-len(word):], word) {
		if rest := strings.TrimSpace(t[:len(t)-len(word)]); strings.HasSuffix(rest, ",") {
			return strings.TrimSpace(strings.TrimSuffix(rest, ","))
		}
	}
	return t
}

// namedTarget finds the comma before a destination that names a node or a
// region (",NODE:…", ",REGION:…") in the text after a rule's type; -1 when
// there is none.
func namedTarget(raw string) int {
	for i := len(raw) - 1; i >= 0; i-- {
		if raw[i] == ',' && (hasPrefixFold(raw[i+1:], "NODE:") || hasPrefixFold(raw[i+1:], "REGION:")) {
			return i
		}
	}
	return -1
}

func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

func parseClashLine(line string) (Entry, string, error) {
	fs := splitFields(line)
	typ := strings.ToUpper(fs[0].text)
	kind, ok := clashTypes[typ]
	if !ok {
		return Entry{}, "", unsupportedType(typ)
	}
	if len(fs) < 2 || fs[1].text == "" {
		return Entry{}, "", fmt.Errorf("%s needs a value", typ)
	}
	value, rest := fs[1].text, fs[2:]
	if !fs[1].quoted {
		value = trimQuotes(value)
	}
	target := ""
	if kind == EntryDomainRegex && !fs[1].quoted {
		// A regular expression may itself contain commas: the destination is
		// the last field, when it is quoted or cannot be a part of a regex.
		raw := line[fs[1].start:]
		last := fs[len(fs)-1]
		switch {
		case len(fs) > 2 && last.quoted:
			value, target = strings.TrimSpace(line[fs[1].start:last.start-1]), last.text
		case namedTarget(raw) >= 0:
			// A node's name may look like a part of a regex: what follows
			// ",NODE:" or ",REGION:" is the destination.
			i := namedTarget(raw)
			value, target = trimQuotes(strings.TrimSpace(raw[:i])), cutNoResolve(raw[i+1:])
		default:
			value = trimQuotes(strings.TrimSpace(raw))
			if i := strings.LastIndex(value, ","); i >= 0 && !strings.ContainsAny(value[i+1:], regexChars) {
				target = trimQuotes(strings.TrimSpace(value[i+1:]))
				value = strings.TrimSpace(value[:i])
			}
		}
		rest = nil
	}
	for _, f := range rest {
		if !f.quoted && strings.EqualFold(f.text, "no-resolve") {
			continue
		}
		target = f.text
		if !f.quoted {
			target = trimQuotes(target)
			if strings.HasPrefix(strings.ToUpper(target), "NODE:") {
				// A node's name may have commas in it.
				target = cutNoResolve(line[f.start:])
			}
		}
		break
	}
	e := Entry{Kind: kind, Value: value}.Normalize()
	if err := e.Valid(); err != nil {
		return Entry{}, "", err
	}
	return e, target, nil
}

// ruleTypeWords are the words a Clash rule starts with. Alone on a line they
// are a rule that lost its value, not the name of a program.
var ruleTypeWords = map[string]bool{
	"DOMAIN": true, "DOMAIN-SUFFIX": true, "DOMAIN-KEYWORD": true, "DOMAIN-REGEX": true, "DOMAIN-WILDCARD": true,
	"IP-CIDR": true, "IP-CIDR6": true, "SRC-IP-CIDR": true, "IP-ASN": true, "GEOIP": true, "GEOSITE": true,
	"MATCH": true, "FINAL": true, "RULE-SET": true, "DST-PORT": true, "SRC-PORT": true, "IN-PORT": true,
	"PROCESS-NAME": true, "PROCESS-PATH": true, "USER-AGENT": true, "URL-REGEX": true,
	"AND": true, "OR": true, "NOT": true, "NETWORK": true, "SUB-RULE": true,
}

var schemePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*://`)

// guessEntry decides what a plainly written entry is.
func guessEntry(s string) (Entry, error) {
	v := strings.TrimSpace(s)
	switch {
	case v == "":
		return Entry{}, fmt.Errorf("empty")
	case ruleTypeWords[strings.ToUpper(v)] && v == strings.ToUpper(v):
		return Entry{}, fmt.Errorf("%s needs a value", v)
	case schemePattern.MatchString(v):
		// A web address: the site is what is meant.
		u, err := url.Parse(v)
		if err != nil || u.Hostname() == "" {
			return Entry{}, fmt.Errorf("%q is not a web address", clip(s, 40))
		}
		return guessEntry(u.Hostname())
	case strings.ContainsAny(v, `/\`) && !isCIDR(v):
		// A path may have spaces in it.
		e := Entry{Kind: EntryProcessPath, Value: v}
		return e, e.Valid()
	case strings.ContainsAny(v, " \t"):
		return Entry{}, fmt.Errorf("an entry must not contain spaces")
	case isCIDR(v):
		e := Entry{Kind: EntryIPCIDR, Value: v}.Normalize()
		return e, e.Valid()
	case isDigitsOrRange(v):
		if _, ok := NormalizePort(v); !ok {
			return Entry{}, fmt.Errorf("%q is not a valid port or port range", v)
		}
		e := Entry{Kind: EntryPort, Value: v}.Normalize()
		return e, e.Valid()
	}
	lower := strings.ToLower(v)
	if strings.HasSuffix(lower, ".exe") || strings.HasSuffix(lower, ".app") || !strings.Contains(v, ".") && !strings.HasPrefix(v, "*") {
		e := Entry{Kind: EntryProcessName, Value: v}
		return e, e.Valid()
	}
	host := strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(lower, "+."), "*."), ".")
	if !hostnamePattern.MatchString(host) {
		return Entry{}, fmt.Errorf("%q is not a domain, address range, port or program", clip(s, 40))
	}
	e := Entry{Kind: EntryDomainSuffix, Value: v}.Normalize()
	return e, e.Valid()
}

func isCIDR(s string) bool {
	if strings.Contains(s, "/") {
		_, err := netip.ParsePrefix(s)
		return err == nil
	}
	_, err := netip.ParseAddr(s)
	return err == nil
}

func isDigitsOrRange(s string) bool {
	for _, r := range s {
		if (r < '0' || r > '9') && r != '-' && r != ':' {
			return false
		}
	}
	return true
}

// ResolveTarget turns the target a pasted line named into a destination:
// DIRECT is direct, REJECT is blocked, and the name of an item of the plan
// follows that item's destination as it stands. Any other target gives
// ok == false, and the destination chosen for the whole paste applies.
func ResolveTarget(target string, rows []Row) (Dest, bool) {
	switch strings.ToUpper(strings.TrimSpace(target)) {
	case "DIRECT":
		return Direct(), true
	case "REJECT", "REJECT-DROP":
		return Reject(), true
	}
	norm := NormName(target)
	if norm == "" {
		return Dest{}, false
	}
	for _, r := range rows {
		for _, k := range r.Item.Keys() {
			if k == norm {
				return r.Item.Dest.Clone(), true
			}
		}
	}
	return Dest{}, false
}
