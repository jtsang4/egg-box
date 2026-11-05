package gui

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"eggbox/internal/app"
	"eggbox/internal/configgen"
	"eggbox/internal/library"
	"eggbox/internal/subscription"
)

// settingsT is the application's preferences.
type settingsT = library.Settings

// nodeEntry is a node as the interface shows it: what the library knows,
// joined with what the running core reports (latency, the tag to select).
type nodeEntry struct {
	Key       string
	Name      string
	Protocol  string
	Transport string
	Region    string
	Server    string
	Port      int
	SubID     string
	SubName   string

	// Live data; zero while disconnected.
	Tag    string
	Delay  int // ms; 0 unknown, -1 failed
	Tested time.Time
}

// refreshNodes rebuilds the node list when the subscriptions changed.
func (a *App) refreshNodes() {
	sig := a.libraryVersion()
	if sig == a.nodesSig && a.nodes != nil {
		return
	}
	a.nodesSig = sig
	nodes, err := a.be.ActiveNodes()
	if err != nil {
		a.nodes = []nodeEntry{}
		return
	}
	out := make([]nodeEntry, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, nodeEntry{
			Key: n.Key(), Name: n.Node.Name, Protocol: n.Node.Protocol(), Transport: n.Node.Transport(),
			Region: subscription.Region(n.Node.Name), Server: n.Node.Server, Port: n.Node.Port,
			SubID: n.SubID, SubName: n.SubName,
		})
	}
	a.nodes = out
}

// libraryVersion identifies the content of the subscriptions list, to know
// when the node list is stale.
func (a *App) libraryVersion() string {
	var b strings.Builder
	for _, s := range a.subs {
		fmt.Fprintf(&b, "%s|%d|%t|%s|%s|%d;", s.ID, s.UpdatedAt.UnixNano(), s.Enabled, s.Include, s.Exclude, s.NodeCount)
	}
	return b.String()
}

// liveNodes returns the nodes with the core's latest measurements applied.
func (a *App) liveNodes() []nodeEntry {
	a.refreshNodes()
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
	}
	return out
}

// nodeCount is how many nodes the enabled subscriptions provide.
func (a *App) nodeCount() int {
	a.refreshNodes()
	return len(a.nodes)
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

// exitNode resolves the node traffic leaves through, following the selector
// and any fastest-node group it points at.
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

// selectedKey is the node the user pinned, "" for automatic.
func (a *App) selectedKey() string { return a.set.SelectedNode }

// selectedEntry is the pinned node, if it still exists.
func (a *App) selectedEntry() (nodeEntry, bool) {
	k := a.selectedKey()
	if k == "" {
		return nodeEntry{}, false
	}
	for _, n := range a.liveNodes() {
		if n.Key == k {
			return n, true
		}
	}
	return nodeEntry{}, false
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
