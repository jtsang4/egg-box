package gui

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/jtsang4/sing-box-ui/internal/configgen"
)

// nodesState is what the node page remembers: the search, the order and the
// region being looked at.
type nodesState struct {
	query  string
	region string
	list   ui.ListState
	sig    string
}

func (a *App) initNodes() {}

const (
	sortDefault = iota
	sortLatency
	sortName
)

var sortNames = []string{"default", "latency", "name"}

// nodeSort is the order the user chose, which the settings keep.
func (a *App) nodeSort() int {
	for i, n := range sortNames {
		if a.set.SortNodes == n {
			return i
		}
	}
	return sortDefault
}

// nodesPage lists every node, to be picked as the exit.
func (a *App) nodesPage(c *ui.Context) {
	nodes := a.liveNodes()
	online := a.snap.Phase.Online()
	sub := a.t("nodes.sub.empty")
	if len(nodes) > 0 {
		sub = a.tn("nodes.sub", len(nodes), "subs", strconv.Itoa(a.enabledSubs()))
		if best, ok := bestDelay(nodes); ok && online {
			sub += "  ·  " + a.t("nodes.sub.best", "ms", strconv.Itoa(best))
		}
	}
	a.fixedFrame(c, a.t("nav.nodes"), sub, func() {
		if len(nodes) == 0 {
			return
		}
		busy := a.snap.Busy.Testing[configgen.TagProxy] || a.busy("test-all")
		btn := a.btn(c, btnSpec{kind: btnSoft, label: a.t("nodes.test_all"), icon: "zap", busy: busy, disabled: !online})
		if !online {
			btn.Tooltip(a.t("nodes.test_offline"))
		}
		if btn.Clicked() {
			a.testAll()
		}
	}, func() {
		if len(nodes) == 0 {
			ui.Column(c).Grow(1).Justify(ui.Center).Children(func() {
				a.emptyNodes(c)
			})
			return
		}
		a.routeStrip(c, nodes)
		a.nodeToolbar(c, nodes)
		vis := a.visibleNodes(nodes)
		if len(vis) == 0 {
			ui.Column(c).Grow(1).Justify(ui.Center).Children(func() {
				a.emptyState(c, "search", a.t("nodes.none.title"), a.t("nodes.none.body"), func() {
					if a.btn(c, btnSpec{kind: btnSecondary, label: a.t("nodes.clear_filters")}).Clicked() {
						a.node.query, a.node.region = "", ""
					}
				})
			})
			return
		}
		a.nodeGrid(c, vis)
	})
}

func (a *App) emptyNodes(c *ui.Context) {
	a.emptyState(c, "globe", a.t("nodes.empty.title"), a.t("nodes.empty.body"), func() {
		if a.btn(c, btnSpec{kind: btnPrimary, label: a.t("subs.add"), icon: "plus"}).Clicked() {
			a.goTo(pageSubscriptions)
			a.openAddSubscription()
		}
		if a.btn(c, btnSpec{kind: btnSecondary, label: a.t("subs.import"), icon: "download"}).Clicked() {
			a.goTo(pageSubscriptions)
			a.openImport()
		}
	})
}

func (a *App) enabledSubs() int {
	n := 0
	for _, s := range a.subs {
		if s.Enabled {
			n++
		}
	}
	return n
}

// bestDelay is the lowest working latency among the nodes.
func bestDelay(nodes []nodeEntry) (int, bool) {
	best := 0
	for _, n := range nodes {
		if n.Delay > 0 && (best == 0 || n.Delay < best) {
			best = n.Delay
		}
	}
	return best, best > 0
}

// visibleNodes applies the search, the region and the order.
func (a *App) visibleNodes(nodes []nodeEntry) []nodeEntry {
	q := strings.ToLower(strings.TrimSpace(a.node.query))
	out := make([]nodeEntry, 0, len(nodes))
	for _, n := range nodes {
		if a.node.region != "" && n.Region != a.node.region {
			continue
		}
		if q != "" && !matchNode(n, q) {
			continue
		}
		out = append(out, n)
	}
	switch a.nodeSort() {
	case sortLatency:
		sort.SliceStable(out, func(i, j int) bool { return delayLess(out[i].Delay, out[j].Delay) })
	case sortName:
		sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	}
	return out
}

func matchNode(n nodeEntry, q string) bool {
	for _, s := range []string{n.Name, n.Protocol, n.Transport, n.Server, n.Region, n.SubName} {
		if strings.Contains(strings.ToLower(s), q) {
			return true
		}
	}
	return false
}

// delayLess orders latencies: the working ones, fastest first, then the
// untested, then those that failed.
func delayLess(x, y int) bool {
	rank := func(d int) int {
		switch {
		case d > 0:
			return 0
		case d == 0:
			return 1
		}
		return 2
	}
	if rx, ry := rank(x), rank(y); rx != ry {
		return rx < ry
	}
	return x < y
}

// ---------------------------------------------------------------- strip

// routeStrip offers the smart routes: automatic, and the fastest of a region.
func (a *App) routeStrip(c *ui.Context, nodes []nodeEntry) {
	type route struct {
		key, title, sub, region string
	}
	routes := []route{{key: "", title: a.t("nodes.auto"), sub: a.t("nodes.auto.hint", "min", strconv.Itoa(a.prefs.TestIntervalMin))}}
	if a.prefs.RegionGroups {
		count := map[string]int{}
		for _, n := range nodes {
			if n.Region != "" {
				count[n.Region]++
			}
		}
		for _, rc := range regionsOf(nodes) {
			if rc.Code == "" || rc.N < 2 || len(routes) >= 5 {
				continue
			}
			routes = append(routes, route{
				key: configgen.RegionPrefix + rc.Code, region: rc.Code,
				title: a.t("nodes.fastest_in", "code", rc.Code),
				sub:   a.tn("nodes.count", rc.N),
			})
		}
	}
	if len(routes) == 1 && len(nodes) < 2 {
		return
	}
	cur := a.selectedKey()
	ui.Row(c.Key("routes")).Gap(14).Children(func() {
		for _, r := range routes {
			a.routeCard(c, r.key, r.title, r.sub, r.region, cur == r.key)
		}
		// Keep the cards from stretching when few.
		if len(routes) < 4 {
			for i := len(routes); i < 4; i++ {
				ui.Box(c).Grow(1).Basis(0)
			}
		}
	})
}

func (a *App) routeCard(c *ui.Context, key, title, sub, region string, selected bool) {
	p := a.pl
	b := ui.ButtonBase(c.Key("route-" + key))
	b.Label(title)
	b.Grow(1).Basis(0).Height(64).Padding(0, 14).Gap(12).Radius(radiusCard - 4).AlignItems(ui.Center).Cursor(ui.CursorPointer)
	hov := b.AnimateTo("hov", boolf(b.Hovered()), ui.Animation{Duration: 150 * time.Millisecond})
	sel := b.AnimateTo("sel", boolf(selected), ui.Animation{Duration: 260 * time.Millisecond, Ease: ui.EaseOut})
	a.selectableFace(b, hov, sel)
	b.Children(func() {
		if region == "" {
			a.iconTile(c, "zap", toneBlue, 36)
		} else {
			a.regionBadge(c, region, 36)
		}
		ui.Column(c).Grow(1).Basis(0).Gap(2).Children(func() {
			a.txt(c, title, 13.5, 650, p.Text).SingleLine()
			a.txt(c, sub, 11.5, 400, p.Text3).SingleLine()
		})
		if selected {
			a.checkBadge(c, 20)
		}
	})
	if b.Clicked() {
		a.pickNode(key)
	}
}

// selectableFace is the look of a card that can be chosen: a quiet panel,
// lit blue when it is the one in use.
func (a *App) selectableFace(b ui.Element, hov, sel float32) {
	p := a.pl
	b.Background(p.Panel.Mix(p.PanelHi, hov).Mix(p.Blue.Alpha(boolOr[float32](p.Dark, 0.16, 0.1)), sel))
	b.BorderColor(p.Line.Mix(p.LineHi, hov).Mix(p.Blue.Alpha(0.85), sel))
	b.Border(1, p.Line)
	if p.Dark {
		b.InsetShadow(0, 1, 0, 0, ui.RGBA(255, 255, 255, 0.05))
	} else {
		b.Shadow(0, 1, 2, 0, ui.RGBA(16, 40, 96, 0.05)).Shadow(0, 10, 24, -14, p.Shadow)
	}
	if sel > 0.01 {
		b.Shadow(0, 8, 24, -10, p.Glow.Alpha(0.6*sel))
	}
	pose := ui.Pose{}
	switch {
	case b.Pressed():
		pose = ui.Pose{Scale: 0.99}
	case b.Hovered():
		pose = ui.Pose{Y: -1}
	}
	b.AnimatePose(pose, ui.Snappy)
}

// checkBadge is the blue disc with a tick that marks the chosen card.
func (a *App) checkBadge(c *ui.Context, size float32) ui.Element {
	p := a.pl
	return ui.Box(c).Size(size, size).Radius(size/2).Center().
		LinearGradient(ui.LinearGradient{From: p.BlueHi, To: p.Blue, Angle: 160}).
		Shadow(0, 2, 8, 0, p.Glow).
		Transition(ui.ElementTransition{Enter: &ui.Motion{Scale: 0.4, Opacity: 0}, Animation: &ui.Bouncy}).
		Children(func() { ui.Icon(c, icon("check")).Size(size*0.6, size*0.6).TextColor(ui.Hex("#FFFFFF")) })
}

// --------------------------------------------------------------- toolbar

func (a *App) nodeToolbar(c *ui.Context, nodes []nodeEntry) {
	ui.Row(c.Key("node-toolbar")).Gap(12).AlignItems(ui.Center).Children(func() {
		a.search(c, "node-q", &a.node.query, a.t("nodes.search"), 0)
		sel := a.nodeSort()
		if a.segmented(c, "node-sort", &sel, []segItem{
			{a.t("nodes.sort.default"), ""}, {a.t("nodes.sort.latency"), ""}, {a.t("nodes.sort.name"), ""},
		}, segOpts{small: true}) {
			a.updateSettings(func(s *settingsT) { s.SortNodes = sortNames[sel] })
		}
	})
	regions := regionsOf(nodes)
	if len(regions) < 2 {
		return
	}
	ui.Row(c.Key("node-regions")).Gap(8).Wrap().AlignItems(ui.Center).Children(func() {
		if a.filterPill(c, "rg-all", a.t("nodes.all"), len(nodes), a.node.region == "", "") {
			a.node.region = ""
		}
		for _, rc := range regions {
			code := rc.Code
			label := code
			if code == "" {
				label = a.t("nodes.region_other")
			}
			if a.filterPill(c, "rg-"+code, label, rc.N, a.node.region == code && a.node.region != "", code) {
				if a.node.region == code {
					a.node.region = ""
				} else {
					a.node.region = code
				}
			}
		}
	})
}

// filterPill is a toggleable pill with a count; it reports a click.
func (a *App) filterPill(c *ui.Context, key, label string, n int, active bool, region string) bool {
	p := a.pl
	b := ui.ButtonBase(c.Key(key))
	b.Label(label)
	b.Height(30).Padding(0, 11).Gap(7).Radius(radiusPill).AlignItems(ui.Center).Cursor(ui.CursorPointer)
	hov := b.AnimateTo("hov", boolf(b.Hovered()), ui.Animation{Duration: 130 * time.Millisecond})
	on := b.AnimateTo("on", boolf(active), ui.Animation{Duration: 200 * time.Millisecond})
	b.Background(p.Field.Mix(p.PanelHi, hov).Mix(p.Blue.Alpha(boolOr[float32](p.Dark, 0.22, 0.14)), on))
	b.Border(1, p.Line.Mix(p.LineHi, hov).Mix(p.Blue.Alpha(0.6), on))
	b.Children(func() {
		a.txt(c, label, 12.5, 600, p.Text2.Mix(p.Text, hov).Mix(p.BlueText, on)).SingleLine()
		a.txt(c, strconv.Itoa(n), 11.5, 600, p.Text3.Mix(p.BlueText, on)).FontFeatures("tnum").SingleLine()
	})
	return b.Clicked()
}

// ------------------------------------------------------------------ grid

// nodeColumns is how many cards fit a row of the grid.
func (a *App) nodeColumns(c *ui.Context) int {
	w, _ := c.Size()
	avail := w - sidebarWidth - 2*pagePadding - 1
	const minCard, gap = 290, 14
	n := int((avail + gap) / (minCard + gap))
	if n < 1 {
		n = 1
	}
	if n > 4 {
		n = 4
	}
	return n
}

func (a *App) nodeGrid(c *ui.Context, vis []nodeEntry) {
	cols := a.nodeColumns(c)
	rows := (len(vis) + cols - 1) / cols
	jumpOnChange(&a.node.list, &a.node.sig, a.node.query+"|"+a.node.region+"|"+a.set.SortNodes+"|"+strconv.Itoa(cols))
	sel := a.selectedKey()
	exitKey := ""
	if it, _, ok := a.exitNode(); ok && a.snap.Phase.Online() && it.Node != nil {
		exitKey = it.Node.Key
	}
	list := ui.List(c.Key("node-list"), &a.node.list, rows, func(i int) {
		ui.Row(c).Gap(14).Children(func() {
			for k := 0; k < cols; k++ {
				idx := i*cols + k
				if idx >= len(vis) {
					ui.Box(c).Grow(1).Basis(0)
					continue
				}
				n := vis[idx]
				a.nodeCard(c, n, sel == n.Key, exitKey == n.Key)
			}
		})
	})
	list.Grow(1).Gap(14).Padding(2, 2, 24, 2)
}

func (a *App) nodeCard(c *ui.Context, n nodeEntry, selected, inUse bool) {
	p := a.pl
	b := ui.ButtonBase(c.Key("node-" + n.Key))
	b.Label(n.Name)
	b.Grow(1).Basis(0).Height(86).Padding(0, 16).Gap(14).Radius(radiusCard - 2).AlignItems(ui.Center).Cursor(ui.CursorPointer)
	hov := b.AnimateTo("hov", boolf(b.Hovered()), ui.Animation{Duration: 150 * time.Millisecond})
	sel := b.AnimateTo("sel", boolf(selected || inUse && a.selectedKey() == ""), ui.Animation{Duration: 260 * time.Millisecond, Ease: ui.EaseOut})
	a.selectableFace(b, hov, sel)
	b.PopoverMenu(func(m *ui.Menu) {
		if m.Item(a.t("nodes.use")).Chosen() {
			a.pickNode(n.Key)
		}
		m.Separator()
		if m.Item(a.t("nodes.copy_name")).Chosen() {
			c.WriteClipboard(n.Name)
		}
		if n.Server != "" && m.Item(a.t("nodes.copy_server")).Chosen() {
			c.WriteClipboard(n.Server + ":" + strconv.Itoa(n.Port))
		}
	})
	b.Children(func() {
		a.regionBadge(c, n.Region, 44)
		ui.Column(c).Grow(1).Basis(0).Gap(6).Children(func() {
			a.txt(c, n.Name, 14.5, 650, p.Text).SingleLine()
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				a.protoChip(c, n)
				if inUse {
					a.chip(c, a.t("nodes.in_use"), toneGood, "")
				} else if n.SubName != "" {
					a.txt(c, n.SubName, 11.5, 400, p.Text3).SingleLine()
				}
			})
		})
		ui.Column(c).Gap(6).AlignItems(ui.End).Children(func() {
			switch {
			case n.Delay > 0:
				a.number(c, fmtMillis(n.Delay), 14, a.toneColor(latencyTone(n.Delay)))
			case n.Delay < 0:
				a.txt(c, a.t("nodes.timeout"), 12.5, 600, p.Bad).SingleLine()
			default:
				a.txt(c, "—", 14, 500, p.Text3)
			}
			a.signal(c, n.Delay)
		})
	})
	if b.Clicked() {
		a.pickNode(n.Key)
	}
}

// protoChip names a node's protocol and transport in a small tag.
func (a *App) protoChip(c *ui.Context, n nodeEntry) {
	p := a.pl
	label := n.Protocol
	if n.Transport != "" && n.Transport != "TCP" {
		label += " · " + n.Transport
	}
	ui.Box(c).Padding(2, 8).Radius(7).Background(p.Field).Border(1, p.Line).Children(func() {
		a.txt(c, label, 11, 600, p.Text2).SingleLine()
	})
}
