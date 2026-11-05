package planimport

import (
	"errors"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"

	"eggbox/internal/routing"
	"eggbox/internal/ruleurl"
)

// parseClash reads the groups, rule providers and rules of a Clash profile.
func parseClash(data []byte) (*doc, error) {
	var root map[string]any
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, errors.New("the profile is not valid YAML")
	}
	d := newDoc("clash")

	for _, raw := range list(root["proxies"]) {
		if m := asMap(raw); text(m["name"]) != "" {
			d.proxies[text(m["name"])] = true
		}
	}
	for _, raw := range list(root["proxy-groups"]) {
		m := asMap(raw)
		name := text(m["name"])
		if name == "" {
			continue
		}
		g := &group{name: name, members: texts(m["proxies"])}
		switch strings.ToLower(text(m["type"])) {
		case "url-test", "fallback", "load-balance":
			g.kind = kindAuto
		default:
			g.kind = kindSelect
		}
		d.addGroup(g)
	}
	providers := map[string]map[string]any{}
	for name, raw := range asMap(root["rule-providers"]) {
		providers[name] = asMap(raw)
	}
	for _, raw := range list(root["rules"]) {
		line, ok := raw.(string)
		if !ok {
			continue
		}
		d.clashRule(line, providers)
	}
	return d, nil
}

func asMap(v any) map[string]any {
	switch t := v.(type) {
	case map[string]any:
		return t
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[fmt.Sprint(k)] = e
		}
		return out
	}
	return nil
}

// splitRule splits a rule line at its top-level commas: the parts of a logical
// rule stay together in parentheses.
func splitRule(line string) []string {
	var parts []string
	depth, start := 0, 0
	for i, r := range line {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, strings.TrimSpace(line[start:i]))
				start = i + 1
			}
		}
	}
	return append(parts, strings.TrimSpace(line[start:]))
}

// clashRule reads one line of the rules list.
func (d *doc) clashRule(line string, providers map[string]map[string]any) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return
	}
	d.nRules++
	parts := splitRule(line)
	typ := strings.ToUpper(parts[0])
	if typ == "MATCH" || typ == "FINAL" {
		if len(parts) >= 2 {
			d.final = parts[1]
		}
		return
	}
	if len(parts) < 3 {
		d.skip("malformed")
		return
	}
	value, target := parts[1], parts[2]
	r := rule{target: target}
	var entries []routing.Entry
	switch typ {
	case "DOMAIN":
		entries = d.entriesOf(routing.EntryDomain, value)
	case "DOMAIN-SUFFIX":
		entries = d.entriesOf(routing.EntryDomainSuffix, value)
	case "DOMAIN-KEYWORD":
		entries = d.entriesOf(routing.EntryDomainKeyword, value)
	case "DOMAIN-REGEX":
		entries = d.entriesOf(routing.EntryDomainRegex, value)
	case "DOMAIN-WILDCARD":
		entries = d.entriesOf(routing.EntryDomainRegex, glob(value))
	case "IP-CIDR", "IP-CIDR6":
		entries = d.entriesOf(routing.EntryIPCIDR, value)
	case "DST-PORT":
		entries = d.portEntries(value)
	case "PROCESS-NAME":
		entries = d.entriesOf(routing.EntryProcessName, value)
	case "PROCESS-PATH":
		entries = d.entriesOf(routing.EntryProcessPath, value)
	case "GEOSITE":
		if src, _, ok := d.geoSource("geosite", value); ok {
			r.sources = append(r.sources, src)
		}
	case "GEOIP":
		if src, lan, ok := d.geoSource("geoip", value); ok {
			if lan {
				r.lan = true
			} else {
				r.sources = append(r.sources, src)
			}
		}
	case "IP-ASN":
		n := strings.TrimPrefix(strings.ToUpper(value), "AS")
		if n == "" {
			d.skip("malformed")
			return
		}
		r.sources = append(r.sources, routing.URLSource(ruleurl.ASN(n), "asn: "+n, routing.MetaCubeX))
	case "RULE-SET":
		p := providers[value]
		switch {
		case p == nil:
			d.skip("unknown_rule_set")
			return
		case text(p["url"]) == "":
			d.skip("local_rule_set")
			return
		}
		src, lan, ok := d.fileSource(text(p["url"]), value)
		switch {
		case !ok:
			return
		case lan:
			r.lan = true
		default:
			r.sources = append(r.sources, src)
		}
	default:
		// AND, OR, NOT, USER-AGENT, URL-REGEX, IN-PORT, SRC-*, SUB-RULE...
		d.skip(typ)
		return
	}
	if src, ok := inlineSource(entries); ok {
		r.sources = append(r.sources, src)
	}
	d.rules = append(d.rules, r)
}
