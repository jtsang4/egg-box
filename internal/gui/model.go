package gui

import (
	"slices"
	"sort"
	"strings"
	"time"

	"eggbox/internal/app"
	"eggbox/internal/configgen"
	"eggbox/internal/library"
	"eggbox/internal/plancompile"
	"eggbox/internal/routing"
)

// settingsT is the application's preferences.
type settingsT = library.Settings

// nodeEntry is a node as the interface shows it: what the library knows,
// joined with what the running core reports (latency, the tag to select).
type nodeEntry struct {
	// Key identifies the node's configuration; ID is what a person's choices
	// about the node are kept under (the subscription and the name).
	Key       string
	ID        routing.NodeID
	Name      string
	Protocol  string
	Transport string
	// Region is the one the node counts as, a correction of the person's
	// included.
	Region string
	Server string
	Port   int
	SubID  string
	// SubName is the subscription's name; SubAlias the short name the person
	// gave it.
	SubName  string
	SubAlias string
	// Ambiguous is set when another subscription offers a node of the same
	// name, so the name alone does not say which one it is.
	Ambiguous bool
	// Imported is set for a node of an import, which can be edited as its
	// share link.
	Imported bool
	// NoAuto keeps the node out of the automatic choice, by its own setting or
	// its subscription's.
	NoAuto bool
	// Corrected is set when the person set the region by hand.
	Corrected bool
	// Relay is the effective first-hop relay for this node and where it came
	// from; SubRelay and ProviderRelay are the inherited choices behind it.
	Relay         routing.EffectiveRelay
	SubRelay      *routing.Relay
	ProviderRelay *routing.Relay
	// NeedsUDP is set for protocols whose transport is UDP (Hysteria, TUIC,
	// WireGuard), so the relay picker can note UDP forwarding requirements.
	NeedsUDP bool

	// Live data; zero while disconnected.
	Tag       string
	Delay     int // ms; 0 unknown, -1 failed
	Tested    time.Time
	RelayLive *app.RelayInfo
}

// Label is the node's name, with its subscription when the name alone would
// not tell it apart.
func (n nodeEntry) Label() string {
	if !n.Ambiguous {
		return n.Name
	}
	return n.Name + " · " + n.Source()
}

// Source is the subscription as a person calls it: the short name when they
// gave one.
func (n nodeEntry) Source() string {
	if n.SubAlias != "" {
		return n.SubAlias
	}
	return n.SubName
}

// ------------------------------------------------------------------ routing

// routingCache is the routing state and the plan in force as it shows,
// kept until the library changes: the state is copied to be read and the view
// is worked out from it, which is more than every frame should pay.
type routingCache struct {
	ver  uint64
	ok   bool
	st   routing.State
	view routing.View
}

// readRouting refreshes the cache when the library changed.
func (a *App) readRouting() {
	v := a.be.LibraryVersion()
	if a.rtc.ok && a.rtc.ver == v {
		return
	}
	st := a.be.Routing()
	a.rtc = routingCache{ver: v, ok: true, st: st, view: st.View()}
}

// rt is the routing state; rv the plan in force with the person's choices
// applied.
func (a *App) rt() *routing.State { return &a.rtc.st }
func (a *App) rv() *routing.View  { return &a.rtc.view }

// ------------------------------------------------------------------- nodes

// refreshNodes rebuilds the node list when the library changed.
func (a *App) refreshNodes() {
	v := a.be.LibraryVersion()
	if a.nodes != nil && v == a.nodesVer {
		return
	}
	a.nodesVer = v
	nodes, err := a.be.ActiveNodes()
	if err != nil {
		a.nodes = []nodeEntry{}
		a.live = nil
		return
	}
	st := a.rt()
	names := map[string]map[string]bool{}
	for _, n := range nodes {
		if names[n.Node.Name] == nil {
			names[n.Node.Name] = map[string]bool{}
		}
		names[n.Node.Name][n.SubID] = true
	}
	out := make([]nodeEntry, 0, len(nodes))
	live := make([]routing.LiveNode, 0, len(nodes))
	for _, n := range nodes {
		id := n.ID()
		pref := st.NodePref(id)
		region := app.RegionOf(*st, n)
		prov := plancompile.ProviderRelay(n.SubID, n.Node.Relay)
		eff := routing.ResolveRelay(id, *st, n.SubRelay, prov)
		out = append(out, nodeEntry{
			Key: n.Key(), ID: id, Name: n.Node.Name, Protocol: n.Node.Protocol(), Transport: n.Node.Transport(),
			Region: region, Server: n.Node.Server, Port: n.Node.Port,
			SubID: n.SubID, SubName: n.SubName, SubAlias: n.SubAlias,
			Ambiguous:     len(names[n.Node.Name]) > 1,
			NoAuto:        pref.NoAuto || !n.Auto,
			Corrected:     pref.Region != "",
			Imported:      n.Imported,
			Relay:         eff,
			SubRelay:      routing.CloneRelayPtr(n.SubRelay),
			ProviderRelay: prov,
			NeedsUDP:      n.Node.NeedsUDP(),
		})
		live = append(live, routing.LiveNode{Sub: n.SubID, Name: n.Node.Name, Hash: n.Node.Key(), Region: region})
	}
	a.nodes, a.live = out, live
}

// liveNodes returns the nodes with the core's latest measurements applied.
// The list is shared until the snapshot or the library changes: callers must
// not change it.
func (a *App) liveNodes() []nodeEntry {
	a.refreshNodes()
	if a.withLive != nil && a.withLiveSnap == a.snap && a.withLiveVer == a.nodesVer {
		return a.withLive
	}
	delays := map[string]app.GroupItem{}
	for _, g := range a.snap.Groups {
		for _, it := range g.Items {
			if it.Node != nil {
				if cur, ok := delays[it.Node.Key]; !ok || (cur.DelayMS == 0 && it.DelayMS != 0) {
					delays[it.Node.Key] = it
				}
			}
		}
	}
	out := make([]nodeEntry, len(a.nodes))
	copy(out, a.nodes)
	for i := range out {
		if it, ok := delays[out[i].Key]; ok {
			out[i].Tag, out[i].Delay, out[i].Tested = it.Tag, it.DelayMS, it.Tested
		}
		if ri, ok := a.snap.Relays[out[i].Key]; ok {
			cp := ri
			out[i].RelayLive = &cp
		}
	}
	a.withLive, a.withLiveSnap, a.withLiveVer = out, a.snap, a.nodesVer
	return out
}

// nodeCount is how many nodes the enabled subscriptions provide.
func (a *App) nodeCount() int {
	a.refreshNodes()
	return len(a.nodes)
}

// nodeFor finds the node a choice names, the way the configuration does: by
// subscription and name first, then by settings.
func (a *App) nodeFor(id routing.NodeID) (nodeEntry, routing.Match) {
	nodes := a.liveNodes()
	i, m := routing.Resolve(id, a.live)
	if m == routing.MatchNone || i < 0 || i >= len(nodes) {
		return nodeEntry{}, routing.MatchNone
	}
	return nodes[i], m
}

// group finds a group of the running core.
func (a *App) group(tag string) (app.Group, bool) { return groupIn(a.snap, tag) }

func groupIn(snap *app.Snapshot, tag string) (app.Group, bool) {
	for _, g := range snap.Groups {
		if g.Tag == tag {
			return g, true
		}
	}
	return app.Group{}, false
}

// exitNode resolves the node the main exit sends traffic through, following
// the selector and any fastest-node group it points at.
func (a *App) exitNode() (item app.GroupItem, via string, ok bool) { return exitNodeIn(a.snap) }

func exitNodeIn(snap *app.Snapshot) (item app.GroupItem, via string, ok bool) {
	tag := configgen.TagProxy
	for depth := 0; depth < 4; depth++ {
		g, found := groupIn(snap, tag)
		if !found || g.Selected == "" {
			return app.GroupItem{}, "", false
		}
		idx := slices.IndexFunc(g.Items, func(it app.GroupItem) bool { return it.Tag == g.Selected })
		if idx < 0 {
			return app.GroupItem{}, "", false
		}
		it := g.Items[idx]
		if it.Node != nil {
			return it, via, true
		}
		if via == "" {
			via = it.Tag
		}
		tag = it.Tag
	}
	return app.GroupItem{}, "", false
}

// regions lists the regions among the nodes, most nodes first.
func regionsOf(nodes []nodeEntry) []regionCount {
	counts := map[string]int{}
	for _, n := range nodes {
		counts[n.Region]++
	}
	out := make([]regionCount, 0, len(counts))
	for r, n := range counts {
		out = append(out, regionCount{r, n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].N != out[j].N {
			return out[i].N > out[j].N
		}
		return out[i].Code < out[j].Code
	})
	return out
}

type regionCount struct {
	Code string
	N    int
}

// regionsWithNodes are the regions a person can send traffic to: those with a
// node, most nodes first, and the one a choice already names even when its
// nodes are gone.
func regionsWithNodes(nodes []nodeEntry, keep string) []regionCount {
	var out []regionCount
	seen := false
	for _, rc := range regionsOf(nodes) {
		if rc.Code == "" {
			continue
		}
		if rc.Code == keep {
			seen = true
		}
		out = append(out, rc)
	}
	if keep != "" && !seen {
		out = append(out, regionCount{keep, 0})
	}
	return out
}

// ----------------------------------------------------------- subscriptions

// subStatus classifies a subscription for display.
type subStatus int

const (
	subOK subStatus = iota
	subStale
	subFailing
	subDisabled
	subExpired
	subExhausted
)

func (a *App) statusOf(s library.Subscription) subStatus {
	switch {
	case !s.Enabled:
		return subDisabled
	case s.LastError != "":
		return subFailing
	case s.Usage != nil && !s.Usage.Expire.IsZero() && !s.Usage.Expire.After(a.now):
		return subExpired
	case s.Usage != nil && s.Usage.Total > 0 && s.Usage.Used() >= s.Usage.Total:
		return subExhausted
	}
	return subOK
}

// primarySubscription is the first enabled subscription that reports usage.
func (a *App) usageSubscriptions() []library.Subscription {
	var out []library.Subscription
	for _, s := range a.subs {
		if s.Enabled && s.Usage != nil && (s.Usage.Total > 0 || !s.Usage.Expire.IsZero()) {
			out = append(out, s)
		}
	}
	return out
}

// subscriptionByID finds one of the subscriptions.
func (a *App) subscriptionByID(id string) (library.Subscription, bool) {
	for _, s := range a.subs {
		if s.ID == id {
			return s, true
		}
	}
	return library.Subscription{}, false
}

// ---------------------------------------------------------- the main exit

// mainExit is where the main exit leads right now, from what the core
// reports: the node and how fast it answers. ok is false while nothing is
// known (no connection, or no configuration yet).
type mainExit struct {
	Node    *app.NodeRef
	DelayMS int
	// Via is the group that picked the node ("Auto", "region-JP"), "" for a
	// node chosen by hand.
	Via string
	// StandIn is set when the node is not the one somebody chose.
	StandIn bool
	Wanted  *routing.NodeID
}

// mainExitNow reads the main exit from the snapshot.
func (a *App) mainExitNow() (mainExit, bool) {
	if ex, ok := a.snap.Exits["main"]; ok && ex.Node != nil {
		via := ""
		if strings.HasPrefix(ex.Member, configgen.RegionPrefix) || ex.Member == configgen.TagAuto {
			via = ex.Member
		}
		return mainExit{Node: ex.Node, DelayMS: ex.DelayMS, Via: via, StandIn: ex.StandIn, Wanted: ex.Wanted}, true
	}
	if it, via, ok := a.exitNode(); ok && it.Node != nil {
		return mainExit{Node: it.Node, DelayMS: it.DelayMS, Via: via}, true
	}
	return mainExit{}, false
}
