package planimport

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"eggbox/internal/routing"
)

// parseSingBox reads the groups, rule sets and routing rules of a sing-box
// configuration.
func parseSingBox(data []byte) (*doc, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var root map[string]any
	if err := dec.Decode(&root); err != nil {
		return nil, errors.New("the file is not valid JSON")
	}
	d := newDoc("singbox")

	for _, raw := range list(root["outbounds"]) {
		o, _ := raw.(map[string]any)
		typ, tag := text(o["type"]), text(o["tag"])
		if tag == "" {
			continue
		}
		switch typ {
		case "selector":
			d.addGroup(&group{name: tag, kind: kindSelect, members: texts(o["outbounds"]), def: text(o["default"])})
		case "urltest":
			d.addGroup(&group{name: tag, kind: kindAuto, members: texts(o["outbounds"])})
		case "direct":
			d.special[tag] = targetDirect
		case "block":
			d.special[tag] = targetReject
		case "dns":
		default:
			d.proxies[tag] = true
		}
	}
	for _, raw := range list(root["endpoints"]) {
		if o, _ := raw.(map[string]any); text(o["tag"]) != "" {
			d.proxies[text(o["tag"])] = true
		}
	}

	route, _ := root["route"].(map[string]any)
	if route == nil {
		return nil, ErrNoPlan
	}
	sets := map[string]map[string]any{}
	for _, raw := range list(route["rule_set"]) {
		if o, _ := raw.(map[string]any); text(o["tag"]) != "" {
			sets[text(o["tag"])] = o
		}
	}
	d.final = text(route["final"])
	for _, raw := range list(route["rules"]) {
		if o, _ := raw.(map[string]any); o != nil {
			d.singBoxRule(o, sets)
		}
	}
	return d, nil
}

func (d *doc) addGroup(g *group) {
	if _, dup := d.groups[g.name]; !dup {
		d.order = append(d.order, g.name)
	}
	d.groups[g.name] = g
}

// singBoxRule reads one routing rule. A rule with a condition the plan cannot
// express is left out as a whole: applying part of it would send more traffic
// to its target than the provider meant.
func (d *doc) singBoxRule(o map[string]any, sets map[string]map[string]any) {
	d.nRules++
	if text(o["type"]) == "logical" {
		d.skip("logical")
		return
	}
	var target string
	switch text(o["action"]) {
	case "", "route":
		target = text(o["outbound"])
	case "reject":
		target = targetReject
	default:
		// sniff, hijack-dns, resolve, route-options: the application makes its own.
		return
	}
	if mode := text(o["clash_mode"]); mode != "" {
		if strings.EqualFold(mode, "global") {
			d.globalTarget = target
		}
		return
	}
	if target == "" {
		return
	}

	r := rule{target: target}
	var entries []routing.Entry
	unsupported := false
	for _, key := range sortedMapKeys(o) {
		v := o[key]
		switch key {
		case "type", "action", "outbound", "method", "no_drop":
		case "invert":
			if b, _ := v.(bool); b {
				d.skip("invert")
				unsupported = true
			}
		case "ip_is_private":
			if b, _ := v.(bool); b {
				r.lan = true
			}
		case "rule_set":
			for _, tag := range texts(v) {
				src, lan, ok := d.ruleSet(tag, sets)
				switch {
				case !ok:
					unsupported = true
				case lan:
					r.lan = true
				default:
					r.sources = append(r.sources, src)
				}
			}
		case "domain":
			entries = append(entries, d.entriesOf(routing.EntryDomain, texts(v)...)...)
		case "domain_suffix":
			entries = append(entries, d.entriesOf(routing.EntryDomainSuffix, texts(v)...)...)
		case "domain_keyword":
			entries = append(entries, d.entriesOf(routing.EntryDomainKeyword, texts(v)...)...)
		case "domain_regex":
			entries = append(entries, d.entriesOf(routing.EntryDomainRegex, texts(v)...)...)
		case "ip_cidr":
			entries = append(entries, d.entriesOf(routing.EntryIPCIDR, texts(v)...)...)
		case "port", "port_range":
			entries = append(entries, d.portEntries(texts(v)...)...)
		case "process_name":
			entries = append(entries, d.entriesOf(routing.EntryProcessName, texts(v)...)...)
		case "process_path":
			entries = append(entries, d.entriesOf(routing.EntryProcessPath, texts(v)...)...)
		case "geosite":
			for _, n := range texts(v) {
				if src, _, ok := d.geoSource("geosite", n); ok {
					r.sources = append(r.sources, src)
				}
			}
		case "geoip":
			for _, n := range texts(v) {
				if src, lan, ok := d.geoSource("geoip", n); ok {
					if lan {
						r.lan = true
					} else {
						r.sources = append(r.sources, src)
					}
				}
			}
		default:
			d.skip(key)
			unsupported = true
		}
	}
	if unsupported {
		return
	}
	if src, ok := inlineSource(entries); ok {
		r.sources = append(r.sources, src)
	}
	d.rules = append(d.rules, r)
}

// ruleSet turns a rule set the configuration declares into a source.
func (d *doc) ruleSet(tag string, sets map[string]map[string]any) (src routing.Source, lan, ok bool) {
	def := sets[tag]
	if def == nil {
		d.skip("unknown_rule_set")
		return routing.Source{}, false, false
	}
	switch text(def["type"]) {
	case "remote":
		return d.fileSource(text(def["url"]), tag)
	case "inline":
		var entries []routing.Entry
		for _, raw := range list(def["rules"]) {
			rl, _ := raw.(map[string]any)
			if text(rl["type"]) == "logical" {
				d.skip("inline_logical")
				continue
			}
			for _, key := range sortedMapKeys(rl) {
				v := rl[key]
				switch key {
				case "domain":
					entries = append(entries, d.entriesOf(routing.EntryDomain, texts(v)...)...)
				case "domain_suffix":
					entries = append(entries, d.entriesOf(routing.EntryDomainSuffix, texts(v)...)...)
				case "domain_keyword":
					entries = append(entries, d.entriesOf(routing.EntryDomainKeyword, texts(v)...)...)
				case "domain_regex":
					entries = append(entries, d.entriesOf(routing.EntryDomainRegex, texts(v)...)...)
				case "ip_cidr":
					entries = append(entries, d.entriesOf(routing.EntryIPCIDR, texts(v)...)...)
				case "port", "port_range":
					entries = append(entries, d.portEntries(texts(v)...)...)
				case "process_name":
					entries = append(entries, d.entriesOf(routing.EntryProcessName, texts(v)...)...)
				case "process_path":
					entries = append(entries, d.entriesOf(routing.EntryProcessPath, texts(v)...)...)
				case "type", "invert":
				default:
					d.skip("inline_" + key)
				}
			}
		}
		s, has := inlineSource(entries)
		return s, false, has
	}
	// A file on the provider's machine is of no use here.
	d.skip("local_rule_set")
	return routing.Source{}, false, false
}

// -------------------------------------------------------------- JSON helpers

// conditionOrder is the order the conditions of a rule are read in, so that
// the plan comes out the same every time and its sources read naturally: the
// names, then the addresses, then ports and processes.
var conditionOrder = []string{
	"rule_set", "geosite", "domain", "domain_suffix", "domain_keyword", "domain_regex",
	"geoip", "ip_cidr", "ip_is_private", "port", "port_range", "process_name", "process_path",
}

// sortedMapKeys lists a rule's keys: the known conditions in their order, then
// whatever else is there, alphabetically.
func sortedMapKeys(m map[string]any) []string {
	var keys []string
	known := map[string]bool{}
	for _, k := range conditionOrder {
		known[k] = true
		if _, ok := m[k]; ok {
			keys = append(keys, k)
		}
	}
	var rest []string
	for k := range m {
		if !known[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	return append(keys, rest...)
}

func list(v any) []any {
	l, _ := v.([]any)
	return l
}

func text(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case nil:
		return ""
	}
	return fmt.Sprint(v)
}

// texts reads a string or a list of strings (and numbers) as a list.
func texts(v any) []string {
	switch t := v.(type) {
	case nil:
		return nil
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s := text(e); s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	if s := text(v); s != "" {
		return []string{s}
	}
	return nil
}
