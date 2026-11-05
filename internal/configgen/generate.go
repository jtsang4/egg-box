package configgen

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"

	"eggbox/internal/rulefmt"
)

const (
	dnsDomestic  = "dns-domestic"
	dnsRemote    = "dns-remote"
	dnsBootstrap = "dns-bootstrap"
	tagMixed     = "mixed-in"
	tagReject    = "reject"

	// How often a node a group sends traffic to is measured, while traffic
	// flows and the node has a stand-in.
	standInInterval = "1m"
)

// Generate builds the configuration. It never mutates its input.
//
// The shape of what it writes, from the outside in:
//
//   - Proxy is the main exit, a group over Auto, the region groups and every
//     node. Auto and the region groups are url-test groups over the nodes that
//     take part in the automatic choice.
//   - Every item, and the fallback, sends its traffic through a group of its
//     own over Proxy, direct, Auto, the region groups and every node. Choosing
//     where an item goes is selecting a member of that group, which the core
//     does while running. An item that is blocked is a reject rule instead.
//   - Rules run in layers: sniff and DNS (always), the local network, the
//     exceptions, the host-name rules of all items, one resolve, the address
//     rules of all items, the fallback.
//   - DNS rules mirror the host-name rules: names that go direct are asked of
//     the domestic server, blocked ones are refused, all others are asked of
//     the remote one through the proxy.
func Generate(in Input) (*Output, error) {
	g := &generator{
		in:        in,
		tagByKey:  map[string]string{},
		regionTag: map[string]string{},
		nodeGroup: map[string]string{},
		resources: map[string]bool{},
		out: &Output{
			Tags:       map[string]string{},
			Selectors:  map[string]string{},
			Selections: map[string]string{},
			Owners:     map[string]Owner{},
		},
	}
	return g.generate()
}

type placedNode struct {
	node Node
	tag  string
}

type generator struct {
	in    Input
	prefs Prefs
	out   *Output

	nodes      []placedNode
	eligible   []placedNode
	tagByKey   map[string]string // node key -> tag
	regionTag  map[string]string // region code -> group tag
	regionTags []string          // the region groups, sorted by code
	nodeGroup  map[string]string // node tag -> region group that contains it

	resources   map[string]bool
	resBytes    int
	ruleSets    []any
	selectors   []map[string]any // groups whose default the digest ignores
	pins        []any
	pinTag      map[string]string
	findProcess bool
}

func (g *generator) warn(code, detail string) {
	g.out.Warnings = append(g.out.Warnings, Warning{Code: code, Detail: detail})
}

func (g *generator) generate() (*Output, error) {
	out := g.out
	prefs, notes := g.in.Prefs.Normalize()
	g.prefs = prefs
	for _, n := range notes {
		g.warn(WarnPrefsAdjusted, n)
	}

	g.nodes = placeNodes(g.in.Nodes, out)
	if len(g.nodes) == 0 {
		return nil, ErrNoNodes
	}
	for _, p := range g.nodes {
		g.tagByKey[p.node.Key()] = p.tag
		if !p.node.NoAuto {
			g.eligible = append(g.eligible, p)
		}
	}
	if len(g.eligible) == 0 {
		g.eligible = g.nodes
		g.warn(WarnAutoEmpty, "")
	}
	g.groupByRegion()

	items := g.admit()

	// ---- groups and nodes
	outbounds := []any{map[string]any{"type": "direct", "tag": TagDirect}}
	layout := Layout{Outbounds: []string{""}}
	var endpoints []any
	add := func(o any) {
		outbounds = append(outbounds, o)
		layout.Outbounds = append(layout.Outbounds, "")
	}

	nodeTags := make([]string, len(g.nodes))
	for i, p := range g.nodes {
		nodeTags[i] = p.tag
	}
	eligibleTags := make([]string, len(g.eligible))
	for i, p := range g.eligible {
		eligibleTags[i] = p.tag
	}
	out.Groups = []string{TagProxy, TagAuto}
	out.Groups = append(out.Groups, g.regionTags...)

	// Proxy: the main exit.
	mainMember := g.member(g.in.Main, true, "main")
	proxyMembers := append(append([]string{TagAuto}, g.regionTags...), nodeTags...)
	add(g.selector(TagProxy, proxyMembers, mainMember, g.in.Main))
	out.Selections[TagProxy] = mainMember

	add(g.urltest(TagAuto, eligibleTags))
	for _, code := range g.regionCodes() {
		add(g.urltest(g.regionTag[code], g.regionMembers(code)))
	}

	// The groups of the items and of the fallback.
	itemMembers := append(append([]string{TagProxy, TagDirect, TagAuto}, g.regionTags...), nodeTags...)
	for _, it := range items {
		if it.Exit.Kind == ExitReject {
			continue
		}
		member := g.member(it.Exit, false, it.Name)
		tag := g.selectorTag(it.ID)
		add(g.selector(tag, itemMembers, member, it.Exit))
		out.Selectors[it.ID] = tag
		out.Selections[tag] = member
	}
	fallbackTag := TagDirect
	switch g.in.Fallback.Kind {
	case ExitDirect, ExitReject:
	default:
		member := g.member(g.in.Fallback, false, "fallback")
		fallbackTag = SelectorPrefix + FallbackID
		add(g.selector(fallbackTag, itemMembers, member, g.in.Fallback))
		out.Selectors[FallbackID] = fallbackTag
		out.Selections[fallbackTag] = member
	}

	// ---- rules
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

	// Layer 2: the exceptions.
	for _, x := range g.in.Exceptions {
		tr := rulefmt.Inspect([]option.HeadlessRule{x.Rule})
		if !exceptionValid(x.Rule) {
			g.warn(WarnExceptionInvalid, x.ID)
			continue
		}
		raw, err := json.Marshal(x.Rule)
		if err != nil {
			g.warn(WarnExceptionInvalid, x.ID)
			continue
		}
		tag := "x-" + safeID(x.ID)
		if g.hasSet(tag) {
			g.warn(WarnExceptionInvalid, x.ID)
			continue
		}
		g.ruleSets = append(g.ruleSets, map[string]any{"type": "inline", "tag": tag, "rules": []json.RawMessage{raw}})
		out.Owners[tag] = Owner{Kind: OwnerException, ID: x.ID}
		if tr.Addr && !resolved {
			routeRules = append(routeRules, resolve)
			resolved = true
		}
		target, reject := g.staticTarget(x.Exit, "exception "+x.ID)
		routeRules = append(routeRules, g.ruleTo(tag, target, reject))
		if len(rulefmt.HostRules([]option.HeadlessRule{x.Rule})) == 1 {
			dnsRules = append(dnsRules, g.dnsRuleFor(tag, x.Exit.Kind))
		}
		g.findProcess = g.findProcess || tr.Process
	}

	// Layer 3: the host-name part of every item.
	anyIP := false
	for _, it := range items {
		target, reject := g.itemTarget(it)
		if len(it.Domain) > 0 {
			tag := "d-" + safeID(it.ID)
			g.addSet(tag, it.Domain, Owner{Kind: OwnerItem, ID: it.ID})
			routeRules = append(routeRules, g.ruleTo(tag, target, reject))
			if len(it.DNS) > 0 {
				dnsTag := tag
				if !bytes.Equal(it.DNS, it.Domain) {
					dnsTag = "h-" + safeID(it.ID)
					g.addSet(dnsTag, it.DNS, Owner{Kind: OwnerItem, ID: it.ID})
				}
				dnsRules = append(dnsRules, g.dnsRuleFor(dnsTag, itemDNSKind(it)))
			}
		}
		anyIP = anyIP || len(it.IP) > 0
		g.findProcess = g.findProcess || it.Process
	}
	// Layers 4 and 5: one resolve, then the address part of every item.
	if anyIP && !resolved {
		routeRules = append(routeRules, resolve)
	}
	for _, it := range items {
		if len(it.IP) == 0 {
			continue
		}
		target, reject := g.itemTarget(it)
		tag := "p-" + safeID(it.ID)
		g.addSet(tag, it.IP, Owner{Kind: OwnerItem, ID: it.ID})
		routeRules = append(routeRules, g.ruleTo(tag, target, reject))
	}

	// Layer 6: whatever nothing claimed.
	final, dnsFinal := fallbackTag, dnsRemote
	switch g.in.Fallback.Kind {
	case ExitDirect:
		final, dnsFinal = TagDirect, dnsDomestic
	case ExitReject:
		final = TagDirect
		routeRules = append(routeRules, map[string]any{"action": "reject"})
	}

	// ---- nodes
	for _, p := range g.nodes {
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
	for _, pin := range g.pins {
		outbounds = append(outbounds, pin)
		layout.Outbounds = append(layout.Outbounds, "")
	}
	out.Layout = layout

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
	if len(g.ruleSets) > 0 {
		route["rule_set"] = g.ruleSets
	}
	if g.findProcess {
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

	var err error
	if out.JSON, err = encode(root); err != nil {
		return nil, err
	}
	// The digest is the configuration without the choices a running core can
	// be told to make.
	saved := make([]any, len(g.selectors))
	for i, m := range g.selectors {
		saved[i] = m["default"]
		m["default"] = ""
	}
	masked, err := encode(root)
	for i, m := range g.selectors {
		m["default"] = saved[i]
	}
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(masked)
	out.Digest = hex.EncodeToString(sum[:])
	return out, nil
}

func encode(root map[string]any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(root); err != nil {
		return nil, fmt.Errorf("encode configuration: %w", err)
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// ---------------------------------------------------------------- groups

// groupByRegion makes a url-test group for every region that has at least one
// node taking part in the automatic choice.
func (g *generator) groupByRegion() {
	byRegion := map[string][]string{}
	for _, p := range g.eligible {
		if code := regionCode(p.node.Region); code != "" {
			byRegion[code] = append(byRegion[code], p.tag)
		}
	}
	codes := make([]string, 0, len(byRegion))
	for code := range byRegion {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	for _, code := range codes {
		tag := RegionPrefix + code
		g.regionTag[code] = tag
		g.regionTags = append(g.regionTags, tag)
		for _, nodeTag := range byRegion[code] {
			g.nodeGroup[nodeTag] = tag
		}
	}
}

func (g *generator) regionCodes() []string {
	codes := make([]string, 0, len(g.regionTag))
	for code := range g.regionTag {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	return codes
}

func (g *generator) regionMembers(code string) []string {
	var members []string
	for _, p := range g.eligible {
		if regionCode(p.node.Region) == code {
			members = append(members, p.tag)
		}
	}
	return members
}

// regionCode is a region as the tags spell it: two upper-case letters.
func regionCode(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	if len(s) != 2 || s[0] < 'A' || s[0] > 'Z' || s[1] < 'A' || s[1] > 'Z' {
		return ""
	}
	return s
}

func (g *generator) urltest(tag string, members []string) map[string]any {
	return map[string]any{
		"type": "urltest", "tag": tag, "outbounds": anyStrings(members),
		"url": g.prefs.TestURL, "interval": strconv.Itoa(g.prefs.TestIntervalMin) + "m",
		"tolerance": g.prefs.ToleranceMs, "idle_timeout": "30m",
	}
}

// selector is a group whose choice the person (or the application for them)
// makes at runtime. While the choice is a node that does not answer, traffic
// goes to the fastest node of the node's region, unless the exit says not to.
func (g *generator) selector(tag string, members []string, def string, exit Exit) map[string]any {
	m := map[string]any{
		"type": "preferred", "tag": tag, "outbounds": anyStrings(members), "default": def,
		"url": g.prefs.TestURL, "interval": standInInterval,
	}
	if len(g.regionTags) > 0 && !(exit.Kind == ExitNode && exit.NoStandIn) {
		m["stand_ins"] = anyStrings(g.regionTags)
	}
	g.selectors = append(g.selectors, m)
	return m
}

func (g *generator) selectorTag(id string) string {
	return SelectorPrefix + safeID(id)
}

// member is the member of a group an exit stands for. An exit that cannot be
// honoured (a region nobody takes part in, a node that is gone) follows the
// main exit, or for the main exit itself the automatic choice, and says so.
func (g *generator) member(e Exit, main bool, what string) string {
	follow := TagProxy
	if main {
		follow = TagAuto
	}
	switch e.Kind {
	case ExitDirect:
		if main {
			return follow
		}
		return TagDirect
	case ExitAuto:
		return TagAuto
	case ExitRegion:
		if tag, ok := g.regionTag[regionCode(e.Region)]; ok {
			return tag
		}
		g.warn(WarnRegionMissing, what)
	case ExitNode:
		if tag, ok := g.tagByKey[e.Node]; ok {
			return tag
		}
		g.warn(WarnNodeMissing, what)
	}
	return follow
}

// itemTarget is what a rule of an item points at.
func (g *generator) itemTarget(it Item) (tag string, reject bool) {
	if it.Exit.Kind == ExitReject {
		return "", true
	}
	return g.out.Selectors[it.ID], false
}

// staticTarget is where an exception goes. Exceptions have no group of their
// own: changing one regenerates the configuration, which is rare. A node with
// a stand-in gets a small group for it, shared by whoever pins the same node.
func (g *generator) staticTarget(e Exit, what string) (tag string, reject bool) {
	switch e.Kind {
	case ExitReject:
		return "", true
	case ExitDirect:
		return TagDirect, false
	case ExitMain:
		return TagProxy, false
	case ExitAuto:
		return TagAuto, false
	case ExitRegion:
		if tag, ok := g.regionTag[regionCode(e.Region)]; ok {
			return tag, false
		}
		g.warn(WarnRegionMissing, what)
		return TagProxy, false
	case ExitNode:
		nodeTag, ok := g.tagByKey[e.Node]
		if !ok {
			g.warn(WarnNodeMissing, what)
			return TagProxy, false
		}
		group, has := g.nodeGroup[nodeTag]
		if e.NoStandIn || !has {
			return nodeTag, false
		}
		if g.pinTag == nil {
			g.pinTag = map[string]string{}
		}
		if pin, done := g.pinTag[nodeTag]; done {
			return pin, false
		}
		pin := PinPrefix + strconv.Itoa(len(g.pins)+1)
		g.pinTag[nodeTag] = pin
		g.pins = append(g.pins, map[string]any{
			"type": "preferred", "tag": pin, "outbounds": []any{nodeTag}, "default": nodeTag,
			"stand_ins": []any{group}, "url": g.prefs.TestURL, "interval": standInInterval,
		})
		return pin, false
	}
	return TagProxy, false
}

func (g *generator) ruleTo(setTag, target string, reject bool) map[string]any {
	r := map[string]any{"rule_set": []any{setTag}}
	if reject {
		r["action"] = "reject"
	} else {
		r["outbound"] = target
	}
	return r
}

// itemDNSKind is the exit kind the DNS rule of an item is written for: blocked
// stays blocked, and the others are told apart by what the item is made of,
// not by where it goes right now (see Item.Domestic).
func itemDNSKind(it Item) ExitKind {
	switch {
	case it.Exit.Kind == ExitReject:
		return ExitReject
	case it.Domestic:
		return ExitDirect
	}
	return ExitMain
}

// dnsRuleFor sends the names of a rule set to the DNS server that fits where
// the traffic goes.
func (g *generator) dnsRuleFor(setTag string, exit ExitKind) map[string]any {
	r := map[string]any{"rule_set": []any{setTag}}
	switch exit {
	case ExitReject:
		r["action"] = "reject"
	case ExitDirect:
		r["server"] = dnsDomestic
	default:
		r["server"] = dnsRemote
	}
	return r
}

// ------------------------------------------------------- items and resources

// admit decides which items the configuration can carry and stores their rule
// data as resources. Items come in priority order, so when there is no room the
// ones at the end are left out.
func (g *generator) admit() []Item {
	var items []Item
	seen := map[string]bool{}
	for _, it := range g.in.Items {
		if len(it.Domain) == 0 && len(it.IP) == 0 {
			continue
		}
		if it.ID == "" || seen[it.ID] {
			g.skip(it, "identifier")
			continue
		}
		var files [][]byte
		for _, data := range [][]byte{it.Domain, it.IP, it.DNS} {
			if len(data) > 0 {
				files = append(files, data)
			}
		}
		if !g.room(files) {
			g.skip(it, "room")
			continue
		}
		for _, data := range files {
			g.store(data)
		}
		seen[it.ID] = true
		items = append(items, it)
	}
	return items
}

func (g *generator) skip(it Item, why string) {
	g.out.Skipped = append(g.out.Skipped, it.ID)
	g.warn(WarnItemSkipped, it.Name+": "+why)
}

// room reports whether the files fit next to the ones already stored.
func (g *generator) room(files [][]byte) bool {
	added, size := 0, 0
	fresh := map[string]bool{}
	for _, data := range files {
		if len(data) > MaxResourceBytes {
			return false
		}
		name := resourceName(data)
		if g.resources[name] || fresh[name] {
			continue
		}
		fresh[name] = true
		added++
		size += len(data)
	}
	return len(g.resources)+added <= MaxResources && g.resBytes+size <= MaxResourceTotal
}

func (g *generator) store(data []byte) string {
	name := resourceName(data)
	if !g.resources[name] {
		g.resources[name] = true
		g.resBytes += len(data)
		g.out.Resources = append(g.out.Resources, Resource{Name: name, Data: data})
	}
	return name
}

func resourceName(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8]) + ".srs"
}

func (g *generator) hasSet(tag string) bool {
	_, taken := g.out.Owners[tag]
	return taken
}

func (g *generator) addSet(tag string, data []byte, owner Owner) {
	g.ruleSets = append(g.ruleSets, map[string]any{
		"type": "local", "tag": tag, "format": "binary", "path": "res:" + g.store(data),
	})
	g.out.Owners[tag] = owner
}

// exceptionValid reports whether a rule says anything the core can match.
func exceptionValid(r option.HeadlessRule) bool {
	if r.Type == C.RuleTypeLogical {
		return len(r.LogicalOptions.Rules) > 0
	}
	return r.DefaultOptions.IsValid()
}

// safeID is an identifier fit to be part of a tag.
func safeID(id string) string {
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// ------------------------------------------------------------------- nodes

// placeNodes validates the nodes and gives each a unique tag that cannot
// collide with the fixed ones.
func placeNodes(in []Node, out *Output) []placedNode {
	reserved := map[string]bool{
		TagProxy: true, TagAuto: true, TagDirect: true, TagTun: true, tagReject: true, tagMixed: true,
	}
	prefixes := []string{RegionPrefix, SelectorPrefix, PinPrefix, "dns-"}
	used := map[string]bool{}
	var nodes []placedNode
	for _, n := range in {
		n.Node = n.Node.Clone()
		if err := n.Validate(); err != nil {
			out.Dropped = append(out.Dropped, Dropped{Name: n.Name, Key: n.Key(), Reason: err.Error()})
			continue
		}
		tag := strings.TrimSpace(n.Name)
		if tag == "" {
			tag = n.Protocol() + " " + n.Server
		}
		if reserved[tag] || slices.ContainsFunc(prefixes, func(p string) bool { return strings.HasPrefix(tag, p) }) {
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
