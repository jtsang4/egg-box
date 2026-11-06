// Package planimport reads the routing a provider's subscription came with (the
// groups and rules of a sing-box configuration or a Clash profile) and turns
// it into a routing plan: items that each send one class of traffic somewhere,
// a fallback, and the main exit the provider would have started with.
//
// Only categories and rules are taken. The provider's DNS, inbounds, ports and
// TUN settings, its mode and log level are the application's business and are
// never read, so a profile cannot change how the machine is set up. The
// result never binds a plan to the provider's own nodes: a group becomes "go
// where the main exit goes", "straight out", "blocked", or "the fastest node of
// a region", which makes the plan usable with any other subscription too.
//
// Subscription content is treated as a credential: what this package reports
// are counts and classes, never names, addresses or group members.
package planimport

import (
	"errors"
	"hash/fnv"
	"strconv"
	"strings"

	"eggbox/internal/routing"
	"eggbox/internal/subscription"
)

// Options say whose plan this is.
type Options struct {
	// SubID is the subscription the profile came from; the plan's identity
	// and the nodes it may pin are tied to it.
	SubID string
	// SubName names the plan.
	SubName string
}

// Report counts what an import did, for diagnostics and the interface; it
// holds no names.
type Report struct {
	// Format is "singbox" or "clash".
	Format string
	// Groups and Rules are what the profile had.
	Groups, Rules int
	// Items and Sources are what the plan got.
	Items, Sources int
	// LAN is set when the profile sends the local network straight out, which
	// the application does on its own.
	LAN bool
	// Skipped counts rules the plan cannot carry, by the kind of condition.
	Skipped map[string]int
}

// Result is an imported plan.
type Result struct {
	Plan   *routing.Plan
	Report Report
}

// ErrNoPlan means the document carries no routing worth importing: it is a
// list of share links, a bare list of servers, or has no rules at all.
var ErrNoPlan = errors.New("the subscription has no routing plan")

// Import reads a profile. It never fails on content it does not understand:
// what cannot be carried over is counted in the report.
func Import(body []byte, opts Options) (*Result, error) {
	trimmed := strings.TrimSpace(string(body))
	var d *doc
	var err error
	switch {
	case strings.HasPrefix(trimmed, "{"):
		d, err = parseSingBox(body)
	case strings.Contains(trimmed, "proxy-groups") || strings.Contains(trimmed, "rules:"):
		d, err = parseClash(body)
	default:
		return nil, ErrNoPlan
	}
	if err != nil {
		return nil, err
	}
	return build(d, opts)
}

// ------------------------------------------------------------------- model

type groupKind int

const (
	// kindSelect lets the person pick a member.
	kindSelect groupKind = iota
	// kindAuto picks by itself: url-test, fallback, load-balance.
	kindAuto
)

// group is a proxy group of the profile.
type group struct {
	name    string
	kind    groupKind
	members []string
	// def is the member a sing-box selector starts on; empty means the first.
	def string
}

func (g *group) first() string {
	if g.def != "" {
		return g.def
	}
	if len(g.members) > 0 {
		return g.members[0]
	}
	return ""
}

const (
	targetDirect = "\x00direct"
	targetReject = "\x00reject"
)

// rule is one routing rule of the profile, with the conditions turned into
// sources.
type rule struct {
	target  string
	sources []routing.Source
	lan     bool
}

// doc is what both formats are read into.
type doc struct {
	format  string
	groups  map[string]*group
	order   []string
	proxies map[string]bool
	// special maps outbound names that are not servers or groups to
	// targetDirect or targetReject.
	special map[string]string
	rules   []rule
	final   string
	// globalTarget is where a global mode sends everything (sing-box's
	// clash_mode rule), which is a hint for the main group.
	globalTarget string
	skipped      map[string]int
	nRules       int
}

func newDoc(format string) *doc {
	return &doc{format: format, groups: map[string]*group{}, proxies: map[string]bool{}, special: map[string]string{}, skipped: map[string]int{}}
}

func (d *doc) skip(kind string) { d.skipped[strings.ToLower(kind)]++ }

// classify says where a target goes when it is not a group.
func (d *doc) classify(name string) string {
	if name == targetDirect || name == targetReject {
		return name
	}
	if t, ok := d.special[name]; ok {
		return t
	}
	switch strings.ToUpper(name) {
	case "DIRECT":
		return targetDirect
	case "REJECT", "REJECT-DROP":
		return targetReject
	}
	return ""
}

// ------------------------------------------------------------------- build

type builder struct {
	d    *doc
	opts Options
	main string
}

func build(d *doc, opts Options) (*Result, error) {
	if len(d.rules) == 0 && d.final == "" {
		return nil, ErrNoPlan
	}
	b := &builder{d: d, opts: opts, main: mainGroup(d)}

	plan := &routing.Plan{
		ID: routing.SubPlanID(opts.SubID), Kind: routing.PlanSubscription, Name: strings.TrimSpace(opts.SubName),
		SubID: opts.SubID, Fallback: routing.Follow(),
	}
	rep := Report{Format: d.format, Groups: len(d.groups), Rules: d.nRules}

	// Rules that go to the same place make one item, placed where the first of
	// them is. An item that gets more rules after others were placed is noted.
	type slot struct {
		item    routing.Item
		idx     int
		lastRun int
	}
	slots := map[string]*slot{}
	var order []string
	run := 0
	lastTarget := ""
	for _, r := range d.rules {
		srcs := r.sources
		if r.lan {
			rep.LAN = true
		}
		if len(srcs) == 0 {
			continue
		}
		// Rules that say DIRECT or REJECT outright have no group to take a name from.
		target := r.target
		if c := d.classify(target); c != "" {
			target = c
		}
		if target != lastTarget {
			run++
			lastTarget = target
		}
		s := slots[target]
		if s == nil {
			dest, notes := b.destOf(target, 0, false)
			name, key := b.nameOf(target, srcs)
			s = &slot{item: routing.Item{Name: name, Key: key, Dest: dest, Enabled: true, Notes: notes, Domestic: dest.Kind == routing.DestDirect}, idx: len(order), lastRun: run}
			slots[target] = s
			order = append(order, target)
		} else if s.lastRun != run {
			if !hasNote(s.item.Notes, routing.NoteReordered) {
				s.item.Notes = append(s.item.Notes, routing.NoteReordered)
			}
			s.lastRun = run
		}
		for _, src := range srcs {
			s.item.Sources = appendSource(s.item.Sources, src)
		}
	}
	taken := map[string]int{}
	for _, t := range order {
		it := slots[t].item
		norm := routing.NormName(it.Name)
		taken[norm]++
		it.ID = itemID(opts.SubID, norm, taken[norm])
		for i := range it.Sources {
			if it.Sources[i].Kind == routing.SourceInline {
				it.Sources[i].ID = "n" + hashOf(it.ID, strconv.Itoa(i))
			}
		}
		plan.Items = append(plan.Items, it)
		rep.Sources += len(it.Sources)
	}
	rep.Items = len(plan.Items)

	if d.final != "" {
		plan.Fallback, _ = b.destOf(d.final, 0, false)
	}
	if mg := d.groups[b.main]; mg != nil && mg.first() != mg.name {
		if m, _ := b.destOf(mg.first(), 0, true); m.Kind == routing.DestAuto || m.Kind == routing.DestRegion || m.Kind == routing.DestNode {
			plan.Main = &m
		}
	}
	if rep.Items == 0 && !rep.LAN && d.final == "" {
		return nil, ErrNoPlan
	}
	rep.Skipped = d.skipped
	return &Result{Plan: plan, Report: rep}, nil
}

func hasNote(notes []string, n string) bool {
	for _, x := range notes {
		if x == n {
			return true
		}
	}
	return false
}

func appendSource(list []routing.Source, s routing.Source) []routing.Source {
	if s.Kind == routing.SourceURL {
		for _, o := range list {
			if o.Kind == routing.SourceURL && o.URL == s.URL {
				return list
			}
		}
		return append(list, s)
	}
	// Hand-written entries of one item are kept together.
	for i, o := range list {
		if o.Kind == routing.SourceInline {
			list[i].Entries = append(list[i].Entries, s.Entries...)
			return list
		}
	}
	return append(list, s)
}

func hashOf(parts ...string) string {
	h := fnv.New64a()
	for _, p := range parts {
		_, _ = h.Write([]byte(p))
		_, _ = h.Write([]byte{0})
	}
	return strconv.FormatUint(h.Sum64(), 36)
}

// itemID is stable across refreshes: it follows the item's name, so a
// provider reordering its groups does not make the person's choices
// unreachable.
func itemID(sub, norm string, n int) string {
	return "i" + hashOf(sub, norm, strconv.Itoa(n))
}

// nameOf names an item after the group its rules go to. Rules that go
// straight out or are blocked have no group to take a name from.
func (b *builder) nameOf(target string, srcs []routing.Source) (name, key string) {
	switch target {
	case targetDirect:
		return routing.BuiltinName("direct", "zh"), "direct"
	case targetReject:
		if allAdLists(srcs) {
			return routing.BuiltinName("ads", "zh"), "ads"
		}
		return routing.BuiltinName("reject", "zh"), "reject"
	}
	return target, ""
}

// ------------------------------------------------------------ destinations

// mainGroup finds the group the profile means as its main choice: the one the
// others list as a member (service groups are "main + all nodes + direct"),
// else the one a global mode points at, else the first.
func mainGroup(d *doc) string {
	count := map[string]int{}
	for _, g := range d.groups {
		for _, m := range g.members {
			if m != g.name && d.groups[m] != nil && d.groups[m].kind == kindSelect {
				count[m]++
			}
		}
	}
	best, n := "", 1
	for _, name := range d.order {
		if count[name] > n {
			best, n = name, count[name]
		}
	}
	if best != "" {
		return best
	}
	if g := d.groups[d.globalTarget]; g != nil && g.kind == kindSelect {
		return g.name
	}
	for _, name := range d.order {
		if d.groups[name].kind == kindSelect {
			return name
		}
	}
	return ""
}

// destOf says where a target goes: a group goes where its default member does.
// forMain asks for what the main exit should start as, where an automatic
// group simply means "the fastest node".
func (b *builder) destOf(target string, depth int, forMain bool) (routing.Dest, []string) {
	if depth > 8 {
		return routing.Follow(), nil
	}
	switch b.d.classify(target) {
	case targetDirect:
		return routing.Direct(), nil
	case targetReject:
		return routing.Reject(), nil
	}
	if target == b.main && !forMain {
		return routing.Follow(), nil
	}
	g := b.d.groups[target]
	if g == nil {
		if b.d.proxies[target] {
			return routing.ToNode(routing.NodeID{Sub: b.opts.SubID, Name: target, Region: regionOf(target)}), nil
		}
		return routing.Follow(), nil
	}
	if g.kind == kindAuto {
		return b.autoDest(g, forMain)
	}
	def := g.first()
	if def == "" || def == g.name {
		return routing.Follow(), nil
	}
	d, notes := b.destOf(def, depth+1, forMain)
	// A group that lists "reject" among its choices is not a blocking group:
	// only its default decides, except for groups plainly made to block ads.
	if d.Kind == routing.DestFollow && !forMain && b.blocksAds(g) {
		return routing.Reject(), notes
	}
	return d, notes
}

// regionOf recognises the region a server name says it is in.
func regionOf(name string) string { return subscription.Region(name) }

// autoDest maps an automatic group to the fastest node of its region when all
// its nodes are in one; otherwise it follows the main exit (or, for the main
// exit itself, is "automatic").
func (b *builder) autoDest(g *group, forMain bool) (routing.Dest, []string) {
	region, n, mixed := "", 0, false
	for _, m := range g.members {
		if b.d.classify(m) != "" {
			continue
		}
		n++
		r := regionOf(m)
		if b.d.groups[m] != nil || r == "" || (region != "" && r != region) {
			mixed = true
			break
		}
		region = r
	}
	switch {
	case n > 0 && !mixed:
		return routing.ToRegion(region), nil
	case forMain:
		return routing.Auto(), nil
	case n == 0:
		return routing.Follow(), nil
	}
	return routing.Follow(), []string{routing.NoteMultiRegionAuto}
}

// blocksAds reports whether a group is plainly for blocking ads: it can reject
// and it is named, or fed, like an ad-block list.
func (b *builder) blocksAds(g *group) bool {
	canReject := false
	for _, m := range g.members {
		if b.d.classify(m) == targetReject {
			canReject = true
		}
	}
	if !canReject {
		return false
	}
	if looksLikeAds(g.name) {
		return true
	}
	// Judged by what its rules read.
	var srcs []routing.Source
	for _, r := range b.d.rules {
		if r.target == g.name {
			srcs = append(srcs, r.sources...)
		}
	}
	return len(srcs) > 0 && allAdLists(srcs)
}

func looksLikeAds(name string) bool {
	n := strings.ToLower(name)
	for _, w := range []string{"广告", "拦截", "屏蔽", "ads", "adblock", "block"} {
		if strings.Contains(n, w) {
			return true
		}
	}
	return false
}

// allAdLists reports whether every URL source is a well-known ad list.
func allAdLists(srcs []routing.Source) bool {
	any := false
	for _, s := range srcs {
		if s.Kind != routing.SourceURL {
			return false
		}
		u := strings.ToLower(s.URL)
		ok := false
		for _, w := range []string{"category-ads", "adblock", "anti-ad", "awavenue", "hagezi", "/ads", "reject", "-ad.", "adservers", "easylist"} {
			if strings.Contains(u, w) {
				ok = true
			}
		}
		if !ok {
			return false
		}
		any = true
	}
	return any
}
