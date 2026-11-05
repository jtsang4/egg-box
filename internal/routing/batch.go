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
}

// BatchBad is a line that was not understood.
type BatchBad struct {
	Line   int
	Text   string
	Reason string
}

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
// `DOMAIN-SUFFIX,example.com,DIRECT`. Blank lines, comments and the
// `payload:` line of a rule file are skipped. Nothing here fails: what cannot
// be read is returned, so it can be shown.
func ParseBatch(text string) BatchResult {
	res := BatchResult{Counts: map[EntryKind]int{}}
	for i, raw := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		n := i + 1
		line := strings.TrimSpace(strings.TrimPrefix(raw, "\ufeff"))
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
			res.Unrecognized = append(res.Unrecognized, BatchBad{Line: n, Text: clip(raw, 120), Reason: err.Error()})
			continue
		}
		res.Lines = append(res.Lines, BatchLine{Line: n, Entry: entry, Target: target})
		res.Counts[entry.Kind]++
	}
	return res
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

func parseClashLine(line string) (Entry, string, error) {
	parts := strings.Split(line, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	typ := strings.ToUpper(parts[0])
	kind, ok := clashTypes[typ]
	if !ok {
		return Entry{}, "", fmt.Errorf("%s rules are not supported here", typ)
	}
	if len(parts) < 2 || parts[1] == "" {
		return Entry{}, "", fmt.Errorf("%s needs a value", typ)
	}
	value := trimQuotes(parts[1])
	target := ""
	if len(parts) > 2 && !strings.EqualFold(parts[2], "no-resolve") {
		target = trimQuotes(parts[2])
	}
	if kind == EntryDomainRegex {
		// A regular expression may itself contain commas.
		value = trimQuotes(strings.Join(parts[1:], ","))
		target = ""
		if i := strings.LastIndex(value, ","); i >= 0 && !strings.ContainsAny(value[i+1:], `\^$()[]{}*+?|`) {
			target = trimQuotes(strings.TrimSpace(value[i+1:]))
			value = strings.TrimSpace(value[:i])
		}
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
