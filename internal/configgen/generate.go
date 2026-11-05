package configgen

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/jtsang4/sing-box-ui/internal/subscription"
)

// Resource is a file the configuration references as "res:<Name>".
type Resource struct {
	Name string
	Data []byte
}

// Input is everything Generate needs.
type Input struct {
	Nodes []subscription.Node
	Prefs Prefs
	Rules []CustomRule
	// RuleSets holds the downloaded binary rule sets by id (RuleSetCN...).
	RuleSets map[string][]byte
	// Selected is what the Proxy group starts on: the Key of a node, the tag of a
	// region group, or empty for the fastest node ("Auto").
	Selected string
}

// Dropped is a node left out of the configuration.
type Dropped struct {
	Name   string
	Key    string
	Reason string
}

// Warning is something the user should know about a generated configuration.
type Warning struct {
	// Code is stable so the GUI can translate it.
	Code   string
	Detail string
}

// Warning codes.
const (
	WarnRuleSetMissing   = "rule_set_missing"
	WarnRuleInvalid      = "rule_invalid"
	WarnRuleTargetAbsent = "rule_target_unknown"
	WarnPrefsAdjusted    = "prefs_adjusted"
)

// Layout records where each node sits in the generated arrays, so an error
// reported at "$.outbounds[7]" can be traced back to a node.
type Layout struct {
	Outbounds []string // node key at each index of "outbounds", "" for groups
	Endpoints []string
}

// Output is a generated configuration.
type Output struct {
	JSON      []byte
	Resources []Resource
	// Tags maps a node key to its outbound tag.
	Tags map[string]string
	// Groups are the group tags in display order: Proxy, Auto, region groups.
	Groups   []string
	Dropped  []Dropped
	Warnings []Warning
	Layout   Layout
}

// ErrNoNodes means there is nothing to connect through.
var ErrNoNodes = errors.New("there are no nodes to generate a configuration from")

const (
	dnsDomestic  = "dns-domestic"
	dnsRemote    = "dns-remote"
	dnsBootstrap = "dns-bootstrap"
	tagMixed     = "mixed-in"
)

// Generate builds the configuration. It never mutates its input.
func Generate(in Input) (*Output, error) {
	prefs, notes := in.Prefs.Normalize()
	out := &Output{Tags: map[string]string{}}
	for _, n := range notes {
		out.Warnings = append(out.Warnings, Warning{Code: WarnPrefsAdjusted, Detail: n})
	}

	nodes := placeNodes(in.Nodes, out)
	if len(nodes) == 0 {
		return nil, ErrNoNodes
	}

	// ---- outbounds, endpoints and groups
	outbounds := []any{map[string]any{"type": "direct", "tag": TagDirect}}
	layout := Layout{Outbounds: []string{""}}
	var endpoints []any
	var memberTags []string
	byRegion := map[string][]string{}
	for _, p := range nodes {
		memberTags = append(memberTags, p.tag)
		if r := subscription.Region(p.node.Name); r != "" {
			byRegion[r] = append(byRegion[r], p.tag)
		}
	}
	urltest := func(tag string, members []string) map[string]any {
		return map[string]any{
			"type": "urltest", "tag": tag, "outbounds": anyStrings(members),
			"url": prefs.TestURL, "interval": strconv.Itoa(prefs.TestIntervalMin) + "m",
			"tolerance": prefs.ToleranceMs, "idle_timeout": "30m",
		}
	}
	groupTags := []string{TagProxy, TagAuto}
	selectorMembers := []string{TagAuto}
	var regionGroups []any
	if prefs.RegionGroups {
		var codes []string
		for code, tags := range byRegion {
			if len(tags) >= 2 {
				codes = append(codes, code)
			}
		}
		sort.Strings(codes)
		for _, code := range codes {
			tag := RegionPrefix + code
			regionGroups = append(regionGroups, urltest(tag, byRegion[code]))
			selectorMembers = append(selectorMembers, tag)
			groupTags = append(groupTags, tag)
		}
	}
	selectorMembers = append(selectorMembers, memberTags...)
	defaultTag := TagAuto
	if t, ok := out.Tags[in.Selected]; ok && in.Selected != "" {
		defaultTag = t
	} else if slices.Contains(groupTags, in.Selected) && in.Selected != TagProxy {
		defaultTag = in.Selected
	}
	outbounds = append(outbounds,
		map[string]any{"type": "selector", "tag": TagProxy, "outbounds": anyStrings(selectorMembers), "default": defaultTag},
		urltest(TagAuto, memberTags),
	)
	layout.Outbounds = append(layout.Outbounds, "", "")
	for _, g := range regionGroups {
		outbounds = append(outbounds, g)
		layout.Outbounds = append(layout.Outbounds, "")
	}
	for _, p := range nodes {
		cfg := cloneConfig(p.node.Config)
		cfg["tag"] = p.tag
		if p.node.Endpoint {
			endpoints = append(endpoints, cfg)
			layout.Endpoints = append(layout.Endpoints, p.node.Key())
			continue
		}
		outbounds = append(outbounds, cfg)
		layout.Outbounds = append(layout.Outbounds, p.node.Key())
	}
	out.Groups, out.Layout = groupTags, layout

	// ---- rule sets
	have := func(id string) bool {
		if len(in.RuleSets[id]) > 0 {
			return true
		}
		out.Warnings = append(out.Warnings, Warning{Code: WarnRuleSetMissing, Detail: id})
		return false
	}
	var cnDomains, cnIPs, ads, foreign bool
	switch prefs.Preset {
	case PresetChina:
		cnDomains, cnIPs = have(RuleSetCN), have(RuleSetCNIP)
	case PresetForeignOnly:
		foreign = have(RuleSetForeign)
	}
	if prefs.BlockAds {
		ads = have(RuleSetAds)
	}
	var ruleSets []any
	for _, s := range []struct {
		id string
		on bool
	}{{RuleSetCN, cnDomains}, {RuleSetCNIP, cnIPs}, {RuleSetAds, ads}, {RuleSetForeign, foreign}} {
		if !s.on {
			continue
		}
		ruleSets = append(ruleSets, map[string]any{"type": "local", "tag": s.id, "format": "binary", "path": "res:" + RuleSetFile(s.id)})
		out.Resources = append(out.Resources, Resource{Name: RuleSetFile(s.id), Data: in.RuleSets[s.id]})
	}

	// ---- DNS servers
	domestic, _ := dnsServerFor(prefs.DomesticDNS)
	remote, _ := dnsServerFor(prefs.RemoteDNS)
	dom := cloneConfig(domestic.obj)
	dom["tag"] = dnsDomestic
	if domestic.hostname {
		dom["domain_resolver"] = dnsBootstrap
	}
	rem := cloneConfig(remote.obj)
	rem["tag"] = dnsRemote
	if rem["type"] != "local" {
		rem["detour"] = TagProxy
	}
	if remote.hostname {
		rem["domain_resolver"] = dnsBootstrap
	}
	servers := []any{map[string]any{"type": "udp", "tag": dnsBootstrap, "server": "223.5.5.5"}, dom, rem}

	// ---- rules
	dnsRules := []any{
		map[string]any{"clash_mode": ModeDirect, "server": dnsDomestic},
		map[string]any{"clash_mode": ModeGlobal, "server": dnsRemote},
	}
	var routeRules []any
	if prefs.Sniff {
		routeRules = append(routeRules, map[string]any{"action": "sniff"})
	}
	routeRules = append(routeRules,
		map[string]any{"protocol": "dns", "action": "hijack-dns"},
		map[string]any{"clash_mode": ModeDirect, "outbound": TagDirect},
		map[string]any{"clash_mode": ModeGlobal, "outbound": TagProxy},
	)
	if prefs.LANDirect {
		routeRules = append(routeRules,
			map[string]any{"ip_is_private": true, "outbound": TagDirect},
			map[string]any{"domain_suffix": []any{"local", "lan", "localdomain", "home.arpa"}, "outbound": TagDirect},
		)
	}
	resolve := map[string]any{"action": "resolve"}
	if !prefs.IPv6 {
		resolve["strategy"] = "ipv4_only"
	}
	resolved := false

	targets := map[string]bool{TargetDirect: true, TargetReject: true}
	for _, g := range groupTags {
		targets[g] = true
	}
	for _, t := range memberTags {
		targets[t] = true
	}
	findProcess := false
	for _, r := range in.Rules {
		r.Value = strings.TrimSpace(r.Value)
		if err := r.Valid(); err != nil {
			out.Warnings = append(out.Warnings, Warning{Code: WarnRuleInvalid, Detail: err.Error()})
			continue
		}
		if !targets[r.Target] {
			out.Warnings = append(out.Warnings, Warning{Code: WarnRuleTargetAbsent, Detail: r.Target})
			continue
		}
		if r.Kind == KindIPCIDR && !resolved {
			// Destinations given as names must be resolved before an address rule can match them.
			routeRules = append(routeRules, resolve)
			resolved = true
		}
		route, dns := customRule(r)
		routeRules = append(routeRules, route)
		if dns != nil {
			dnsRules = append(dnsRules, dns)
		}
		findProcess = findProcess || r.Kind == KindProcessName
	}

	if ads {
		routeRules = append(routeRules, map[string]any{"rule_set": []any{RuleSetAds}, "action": "reject"})
		dnsRules = append(dnsRules, map[string]any{"rule_set": []any{RuleSetAds}, "action": "reject"})
	}
	final, dnsFinal := TagProxy, dnsRemote
	switch prefs.Preset {
	case PresetChina:
		if cnDomains {
			routeRules = append(routeRules, map[string]any{"rule_set": []any{RuleSetCN}, "outbound": TagDirect})
			dnsRules = append(dnsRules, map[string]any{"rule_set": []any{RuleSetCN}, "server": dnsDomestic})
		}
		if cnIPs {
			if !resolved {
				routeRules = append(routeRules, resolve)
			}
			routeRules = append(routeRules, map[string]any{"rule_set": []any{RuleSetCNIP}, "outbound": TagDirect})
		}
	case PresetForeignOnly:
		final, dnsFinal = TagDirect, dnsDomestic
		if foreign {
			routeRules = append(routeRules, map[string]any{"rule_set": []any{RuleSetForeign}, "outbound": TagProxy})
			dnsRules = append(dnsRules, map[string]any{"rule_set": []any{RuleSetForeign}, "server": dnsRemote})
		}
	}

	// ---- assemble
	tun := map[string]any{"type": "tun", "tag": TagTun, "mtu": prefs.MTU, "stack": prefs.TunStack}
	inbounds := []any{tun}
	if prefs.MixedPort > 0 {
		inbounds = append(inbounds, map[string]any{"type": "mixed", "tag": tagMixed, "listen": "127.0.0.1", "listen_port": prefs.MixedPort})
	}
	dns := map[string]any{"servers": servers, "rules": dnsRules, "final": dnsFinal, "reverse_mapping": true}
	if !prefs.IPv6 {
		dns["strategy"] = "ipv4_only"
	}
	route := map[string]any{
		"rules":                   routeRules,
		"final":                   final,
		"default_domain_resolver": map[string]any{"server": dnsDomestic},
	}
	if len(ruleSets) > 0 {
		route["rule_set"] = ruleSets
	}
	if findProcess {
		route["find_process"] = true
	}
	root := map[string]any{
		"log":          map[string]any{"level": "info", "timestamp": true},
		"dns":          dns,
		"inbounds":     inbounds,
		"outbounds":    outbounds,
		"route":        route,
		"experimental": map[string]any{"cache_file": map[string]any{"enabled": true}},
	}
	if len(endpoints) > 0 {
		root["endpoints"] = endpoints
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(root); err != nil {
		return nil, fmt.Errorf("encode configuration: %w", err)
	}
	out.JSON = bytes.TrimRight(buf.Bytes(), "\n")
	return out, nil
}

type placedNode struct {
	node subscription.Node
	tag  string
}

// placeNodes validates the nodes and gives each a unique tag that cannot
// collide with the fixed ones.
func placeNodes(in []subscription.Node, out *Output) []placedNode {
	reserved := map[string]bool{TagProxy: true, TagAuto: true, TagDirect: true, TagTun: true, TargetReject: true, tagMixed: true}
	used := map[string]bool{}
	var nodes []placedNode
	for _, n := range in {
		n = n.Clone()
		if err := n.Validate(); err != nil {
			out.Dropped = append(out.Dropped, Dropped{Name: n.Name, Key: n.Key(), Reason: err.Error()})
			continue
		}
		tag := strings.TrimSpace(n.Name)
		if tag == "" {
			tag = n.Protocol() + " " + n.Server
		}
		if reserved[tag] || strings.HasPrefix(tag, RegionPrefix) || strings.HasPrefix(tag, "dns-") {
			tag += " (node)"
		}
		base := tag
		for i := 2; used[strings.ToLower(tag)]; i++ {
			tag = fmt.Sprintf("%s %d", base, i)
		}
		used[strings.ToLower(tag)] = true
		nodes = append(nodes, placedNode{node: n, tag: tag})
		out.Tags[n.Key()] = tag
	}
	return nodes
}

// customRule renders one user rule for routing and, for host-name rules, for DNS.
func customRule(r CustomRule) (route, dns map[string]any) {
	v := strings.TrimSpace(r.Value)
	item := map[string]any{}
	domainRule := false
	switch r.Kind {
	case KindDomain:
		item["domain"], domainRule = []any{strings.ToLower(v)}, true
	case KindDomainSuffix:
		v = strings.ToLower(strings.TrimPrefix(strings.TrimPrefix(v, "*"), "."))
		item["domain_suffix"], domainRule = []any{v}, true
	case KindDomainKeyword:
		item["domain_keyword"], domainRule = []any{strings.ToLower(v)}, true
	case KindDomainRegex:
		item["domain_regex"], domainRule = []any{v}, true
	case KindIPCIDR:
		item["ip_cidr"] = []any{v}
	case KindProcessName:
		item["process_name"] = []any{v}
	case KindPort:
		if lo, hi, ranged := strings.Cut(strings.ReplaceAll(v, "-", ":"), ":"); ranged {
			item["port_range"] = []any{lo + ":" + hi}
		} else {
			n, _ := strconv.Atoi(lo)
			item["port"] = []any{n}
		}
	}
	route = map[string]any{}
	for k, e := range item {
		route[k] = e
	}
	if r.Target == TargetReject {
		route["action"] = "reject"
	} else {
		route["outbound"] = r.Target
	}
	if !domainRule {
		return route, nil
	}
	dns = map[string]any{}
	for k, e := range item {
		dns[k] = e
	}
	switch r.Target {
	case TargetReject:
		dns["action"] = "reject"
	case TargetDirect:
		dns["server"] = dnsDomestic
	default:
		dns["server"] = dnsRemote
	}
	return route, dns
}

func anyStrings(in []string) []any {
	out := make([]any, len(in))
	for i, s := range in {
		out[i] = s
	}
	return out
}

// cloneConfig deep-copies a node's settings, keeping numbers exact.
func cloneConfig(m map[string]any) map[string]any {
	raw, _ := json.Marshal(m)
	var out map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	_ = dec.Decode(&out)
	if out == nil {
		out = map[string]any{}
	}
	return out
}
