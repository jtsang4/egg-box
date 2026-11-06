package gui

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"eggbox/internal/configgen"
	"eggbox/internal/plancompile"
	"eggbox/internal/routing"
)

// nodesState is what the node page remembers: the search, the order, the
// region and the subscription being looked at, and the banners put away.
type nodesState struct {
	query  string
	region string
	sub    string
	list   ui.ListState
	sig    string
	// dismissed are the banners the person closed, until the application is
	// started again.
	dismissed map[string]bool
}

func (a *App) initNodes() { a.node.dismissed = map[string]bool{} }

const (
	sortDefault = iota
	sortLatency
	sortName
)

// regionOther is the region filter of the nodes no region could be told for.
const regionOther = "-"

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

// nodesPage lists every node, to be picked as the exit, and says which
// nodes carry what.
func (a *App) nodesPage(c *ui.Context) {
	nodes := a.liveNodes()
	online := a.snap.Phase.Online()
	sub := a.t("nodes.sub.empty")
	if len(nodes) > 0 {
		sub = a.tn("nodes.sub", len(nodes))
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
		a.nodeBanners(c)
		a.mainStrip(c, nodes)
		a.inUseRow(c)
		a.nodeToolbar(c, nodes)
		vis := a.visibleNodes(nodes)
		if len(vis) == 0 {
			ui.Column(c).Grow(1).Justify(ui.Center).Children(func() {
				a.emptyState(c, "search", a.t("nodes.none.title"), a.t("nodes.none.body"), func() {
					if a.btn(c, btnSpec{kind: btnSecondary, label: a.t("nodes.clear_filters")}).Clicked() {
						a.node.query, a.node.region, a.node.sub = "", "", ""
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

// visibleNodes applies the search, the filters and the order.
func (a *App) visibleNodes(nodes []nodeEntry) []nodeEntry {
	q := strings.ToLower(strings.TrimSpace(a.node.query))
	out := make([]nodeEntry, 0, len(nodes))
	for _, n := range nodes {
		switch {
		case a.node.region == regionOther && n.Region != "":
			continue
		case a.node.region != "" && a.node.region != regionOther && n.Region != a.node.region:
			continue
		case a.node.sub != "" && n.SubID != a.node.sub:
			continue
		}
		if q != "" && !a.matchNode(n, q) {
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

// matchNode says whether a search finds a node: in its name, protocol,
// server, subscription or the name of its region in either language.
func (a *App) matchNode(n nodeEntry, q string) bool {
	for _, s := range []string{n.Name, n.Protocol, n.Transport, n.Server, n.Region, n.SubName, n.SubAlias, a.regionName(n.Region)} {
		if s != "" && strings.Contains(strings.ToLower(s), q) {
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

// ---------------------------------------------------------------- banners

// nodeBanner is something about the nodes people chose that they have to
// know: a choice that is not what carries the traffic.
type nodeBanner struct {
	key    string
	text   string
	repick func()
}

// ownerName is what a chosen node was chosen for, as the person reads it.
func (a *App) ownerName(kind plancompile.OwnerKind, id string) string {
	switch kind {
	case plancompile.OwnerMain:
		return a.t("owner.main")
	case plancompile.OwnerFallback:
		return a.t("fallback.title")
	case plancompile.OwnerItem:
		if row, _, ok := a.rv().Row(id); ok {
			return a.itemName(row.Item)
		}
	case plancompile.OwnerException:
		for _, x := range a.rt().Exceptions {
			if x.ID == id {
				return a.t("owner.exception", "value", exceptionTitle(x.Entry()))
			}
		}
	}
	return a.t("owner.unknown")
}

// ownerOfKey reads the keys of the snapshot's exits.
func ownerOfKey(key string) (plancompile.OwnerKind, string) {
	switch key {
	case "main":
		return plancompile.OwnerMain, ""
	case "fallback":
		return plancompile.OwnerFallback, ""
	}
	return plancompile.OwnerItem, key
}

// repick opens the list of nodes to choose again for whatever it was chosen for.
func (a *App) repick(kind plancompile.OwnerKind, id string) {
	title := a.ownerName(kind, id)
	switch kind {
	case plancompile.OwnerMain:
		a.pickNodeFor(title, a.rt().Main, false, a.setMain)
	case plancompile.OwnerFallback:
		a.pickNodeFor(title, a.rv().Fallback, true, func(d routing.Dest) {
			a.editRouting(func(s *routing.State) error { return s.SetFallback(d) }, nil)
		})
	case plancompile.OwnerException:
		for _, x := range a.rt().Exceptions {
			if x.ID == id {
				a.pickNodeFor(title, x.Dest, true, func(d routing.Dest) {
					a.editRouting(func(s *routing.State) error { return s.SetExceptionDest(id, d) }, nil)
				})
			}
		}
	default:
		if row, _, ok := a.rv().Row(id); ok {
			a.pickNodeFor(title, row.Item.Dest, true, func(d routing.Dest) {
				a.editRouting(func(s *routing.State) error { return s.SetItemDest(id, d) }, nil)
			})
		}
	}
}

// banners lists the choices of nodes that are not honoured now: the node is
// gone from the subscriptions, or it does not answer and another stands in.
func (a *App) banners() []nodeBanner {
	var out []nodeBanner
	gone := map[string]bool{}
	for _, pin := range a.snap.Plan.Gone {
		kind, id := pin.Owner, pin.ID
		key := string(kind) + "|" + id
		gone[key] = true
		sub := ""
		if ex, ok := a.snap.Exits[ownerKeyOf(kind, id)]; ok && ex.Node != nil {
			sub = ex.Node.Label()
		}
		text := a.t("banner.gone", "for", a.ownerName(kind, id), "node", pin.Node.Name)
		if sub != "" {
			text = a.t("banner.gone.using", "for", a.ownerName(kind, id), "node", pin.Node.Name, "using", sub)
		}
		out = append(out, nodeBanner{key: "gone|" + key + "|" + pin.Node.Name, text: text, repick: func() { a.repick(kind, id) }})
	}
	// The main exit comes first, then everything else, then the items in the
	// order of the plan.
	rank := map[string]int{"main": 0, "fallback": 1}
	for i, r := range a.rv().Rows {
		rank[r.Item.ID] = 2 + i
	}
	keys := make([]string, 0, len(a.snap.Exits))
	for k := range a.snap.Exits {
		keys = append(keys, k)
	}
	at := func(k string) int {
		if r, ok := rank[k]; ok {
			return r
		}
		return len(rank) + 2
	}
	sort.Slice(keys, func(i, j int) bool {
		if ri, rj := at(keys[i]), at(keys[j]); ri != rj {
			return ri < rj
		}
		return keys[i] < keys[j]
	})
	for _, k := range keys {
		ex := a.snap.Exits[k]
		if !ex.StandIn || ex.Wanted == nil || ex.Node == nil {
			continue
		}
		kind, id := ownerOfKey(k)
		if gone[string(kind)+"|"+id] {
			continue
		}
		want := ex.Wanted.Name
		if n, m := a.nodeFor(*ex.Wanted); m != routing.MatchNone {
			want = n.Label()
		}
		out = append(out, nodeBanner{
			key:    "stand|" + k + "|" + ex.Wanted.Name + "|" + ex.Node.Key,
			text:   a.t("banner.stand_in", "for", a.ownerName(kind, id), "node", want, "using", ex.Node.Label()),
			repick: func() { a.repick(kind, id) },
		})
	}
	return out
}

// ownerKeyOf is the key of the snapshot's exits for an owner.
func ownerKeyOf(kind plancompile.OwnerKind, id string) string {
	switch kind {
	case plancompile.OwnerMain:
		return "main"
	case plancompile.OwnerFallback:
		return "fallback"
	}
	return id
}

func (a *App) nodeBanners(c *ui.Context) {
	for _, b := range a.banners() {
		if a.node.dismissed[b.key] {
			continue
		}
		b := b
		a.alertBanner(c, "banner-"+b.key, toneWarn, b.text, false, func() {
			if a.btn(c, btnSpec{kind: btnSecondary, small: true, label: a.t("banner.repick")}).Clicked() {
				b.repick()
			}
			if a.btn(c, btnSpec{kind: btnGhost, small: true, label: a.t("banner.dismiss")}).Clicked() {
				a.node.dismissed[b.key] = true
			}
		})
	}
}

// alertBanner is a tinted strip with an icon, a sentence and the actions it
// offers, for something the person should know about a page. stacked puts the
// actions on a line of their own under the sentence, for a long sentence or
// several actions, which would otherwise squeeze the text in a small window.
func (a *App) alertBanner(c *ui.Context, key string, t tone, text string, stacked bool, actions func()) {
	p := a.pl
	col, glyph := a.toneColor(t), "alert"
	if t == toneBlue {
		col, glyph = p.Blue, "info"
	}
	strip := ui.Row(c.Key(key)).Gap(12).Padding(12, 14, 12, 16).Radius(radiusCard-4).Background(col.Alpha(0.1)).Border(1, col.Alpha(0.26))
	if !stacked {
		strip.AlignItems(ui.Center).Children(func() {
			ui.Icon(c, icon(glyph)).Size(16, 16).TextColor(a.toneColor(t)).Shrink(0)
			a.txt(c, text, 13, 500, p.Text).Grow(1).Basis(0).LineHeight(1.45)
			actions()
		})
		return
	}
	strip.AlignItems(ui.Start).Children(func() {
		// Level with the first line of the text.
		ui.Row(c).Padding(2, 0, 0, 0).Shrink(0).Children(func() {
			ui.Icon(c, icon(glyph)).Size(16, 16).TextColor(a.toneColor(t))
		})
		ui.Column(c).Grow(1).Basis(0).Gap(10).Children(func() {
			a.txt(c, text, 13, 500, p.Text).LineHeight(1.45)
			ui.Row(c).Gap(8).Wrap().AlignItems(ui.Center).Children(actions)
		})
	})
}

// ------------------------------------------------------------- main strip

// routeSpec is a card of the main exit strip.
type routeSpec struct {
	key, title, sub, tip string
	// label is the card's name for assistive technology, when the title alone
	// would not say what the card does.
	label          string
	region         string
	icon           string
	selected, warn bool
	click          func()
	menu           func(m *ui.Menu)
}

// mainStrip is the main exit at the top of the page: automatic, the fastest of
// a region, or one node. Regions that do not fit are in a menu.
func (a *App) mainStrip(c *ui.Context, nodes []nodeEntry) {
	main := a.rt().Main
	online := a.snap.Phase.Online()
	ex, known := a.mainExitNow()
	now := func() string {
		if online && known && ex.Node != nil {
			sub := a.t("nodes.now", "node", ex.Node.Label())
			if ex.DelayMS > 0 {
				sub += " · " + fmtMillis(ex.DelayMS)
			}
			return sub
		}
		return ""
	}
	var cards []routeSpec
	auto := routeSpec{
		key: "auto", title: a.t("nodes.auto"), sub: a.t("nodes.auto.hint"), icon: "zap",
		tip:      a.t("nodes.auto.tip", "min", strconv.Itoa(a.prefs.TestIntervalMin)),
		selected: main.Kind == routing.DestAuto,
		click:    func() { a.setMain(routing.Auto()) },
	}
	if auto.selected && now() != "" {
		auto.sub = now()
	}
	cards = append(cards, auto)

	if main.Kind == routing.DestNode && main.Node != nil {
		cards = append(cards, a.pinnedCard(*main.Node, now(), online && known && ex.StandIn))
	}

	keep := ""
	if main.Kind == routing.DestRegion {
		keep = main.Region
	}
	regions := regionsWithNodes(nodes, keep)
	sort.SliceStable(regions, func(i, j int) bool {
		// The region in use first, so that it never hides in the menu.
		return regions[i].Code == keep && regions[j].Code != keep
	})

	// Only as many cards as the window gives room for: a card that has to cut
	// its words is worse than a region that is one click further.
	w, _ := c.Size()
	const minRoute, gap = 176, 14
	fit := max(int((w-sidebarWidth-2*pagePadding-1+gap)/(minRoute+gap)), 2)
	room := fit - len(cards)
	shown := regions
	var rest []regionCount
	if len(regions) > room {
		room = max(room-1, 0) // one place goes to the menu
		shown, rest = regions[:room], regions[room:]
	}
	for _, rc := range shown {
		code := rc.Code
		spec := routeSpec{
			key: "region-" + code, title: a.regionName(code), label: a.t("nodes.fastest_in", "name", a.regionName(code)), region: code,
			sub:      a.tn("nodes.fastest_of", rc.N),
			selected: main.Kind == routing.DestRegion && main.Region == code,
			click:    func() { a.setMain(routing.ToRegion(code)) },
		}
		if rc.N == 0 {
			spec.sub, spec.warn = a.t("nodes.region.empty"), true
		}
		if spec.selected && now() != "" {
			spec.sub = now()
		}
		cards = append(cards, spec)
	}
	if len(rest) > 0 {
		cards = append(cards, routeSpec{
			key: "more", title: a.t("nodes.more_regions"), sub: a.tn("nodes.more_regions.n", len(rest)), icon: "globe",
			menu: func(m *ui.Menu) {
				for _, rc := range rest {
					label := a.regionName(rc.Code) + "  (" + strconv.Itoa(rc.N) + ")"
					if m.Item(label).Chosen() {
						a.setMain(routing.ToRegion(rc.Code))
					}
				}
			},
		})
	}
	ui.Row(c.Key("routes")).Gap(gap).Children(func() {
		for _, s := range cards {
			a.routeCard(c, s)
		}
		// Keep the cards from stretching when few.
		for i := len(cards); i < min(4, fit); i++ {
			ui.Box(c).Grow(1).Basis(0)
		}
	})
}

// pinnedCard is the card of a node chosen as the main exit. While another
// node stands in for it, the card says which.
func (a *App) pinnedCard(id routing.NodeID, now string, standIn bool) routeSpec {
	n, m := a.nodeFor(id)
	spec := routeSpec{key: "pinned", selected: true, click: func() { a.repick(plancompile.OwnerMain, "") }}
	if m == routing.MatchNone {
		spec.title, spec.sub, spec.warn, spec.icon = id.Name, a.t("nodes.pinned.gone"), true, "alert"
		return spec
	}
	spec.title, spec.region = n.Label(), n.Region
	spec.sub = a.t("nodes.pinned")
	if n.Delay > 0 {
		spec.sub += " · " + fmtMillis(n.Delay)
	}
	if standIn && now != "" {
		spec.sub, spec.warn = now, true
	}
	return spec
}

func (a *App) routeCard(c *ui.Context, s routeSpec) {
	p := a.pl
	b := ui.ButtonBase(c.Key("route-" + s.key))
	b.Label(boolOr(s.label != "", s.label, s.title))
	if s.tip != "" {
		b.Tooltip(s.tip)
	}
	b.Grow(1).Basis(0).Height(64).Padding(0, 14).Gap(12).Radius(radiusCard - 4).AlignItems(ui.Center).Cursor(ui.CursorPointer)
	hov := b.AnimateTo("hov", boolf(b.Hovered()), ui.Animation{Duration: 150 * time.Millisecond})
	sel := b.AnimateTo("sel", boolf(s.selected), ui.Animation{Duration: 260 * time.Millisecond, Ease: ui.EaseOut})
	a.selectableFace(b, hov, sel)
	b.Children(func() {
		// The tick sits on the badge, like a status dot on an avatar, so the
		// words keep the whole width of the card.
		ui.Box(c).Size(36, 36).Children(func() {
			if s.region == "" {
				a.iconTile(c, boolOr(s.icon != "", s.icon, "zap"), boolOr(s.warn, toneWarn, toneBlue), 36)
			} else {
				a.regionBadge(c, s.region, 36)
			}
			if s.selected {
				a.checkBadge(c, 17).Border(2, p.Panel).Absolute().Right(-5).Bottom(-5)
			}
		})
		ui.Column(c).Grow(1).Basis(0).Gap(2).Children(func() {
			a.txt(c, s.title, 13.5, 650, p.Text).SingleLine()
			a.txt(c, s.sub, 11.5, 400, boolOr(s.warn, p.Warn, p.Text3)).SingleLine()
		})
		if s.menu != nil {
			ui.Icon(c, icon("chevron-down")).Size(15, 15).TextColor(p.Text3).Shrink(0)
		}
	})
	if s.menu != nil {
		b.PopoverMenuButton(s.menu)
		return
	}
	if b.Clicked() && s.click != nil {
		s.click()
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

// ----------------------------------------------------------- items in use

// inUseRow lists the items that do not follow the main exit and go through a
// node, with the node each one uses now.
func (a *App) inUseRow(c *ui.Context) {
	p := a.pl
	type use struct {
		id, name, where string
		warn            bool
	}
	var uses []use
	online := a.snap.Phase.Online()
	for _, row := range a.rv().Rows {
		it := row.Item
		if !it.Enabled {
			continue
		}
		switch it.Dest.Kind {
		case routing.DestAuto, routing.DestRegion, routing.DestNode:
		default:
			continue
		}
		u := use{id: it.ID, name: a.itemName(it), where: a.destLabel(it.Dest)}
		if ex, ok := a.snap.Exits[it.ID]; ok && online && ex.Node != nil {
			u.where = ex.Node.Label()
			if ex.DelayMS > 0 {
				u.where += " · " + fmtMillis(ex.DelayMS)
			}
			u.warn = ex.StandIn
		} else if a.destMissing(it.Dest) {
			u.warn = true
		}
		uses = append(uses, u)
	}
	if len(uses) == 0 {
		return
	}
	const most = 8
	extra := 0
	if len(uses) > most {
		extra = len(uses) - most
		uses = uses[:most]
	}
	ui.Row(c.Key("usage")).Gap(12).Padding(2, 2).AlignItems(ui.Center).Children(func() {
		a.txt(c, a.t("nodes.usage"), 12.5, 600, p.Text2).SingleLine().Shrink(0)
		ui.Row(c).Gap(8).Wrap().Grow(1).Basis(0).AlignItems(ui.Center).Children(func() {
			for _, u := range uses {
				b := ui.ButtonBase(c.Key("use-" + u.id))
				b.Label(u.name)
				b.Height(28).Padding(0, 10).Gap(6).Radius(radiusPill).AlignItems(ui.Center).Cursor(ui.CursorPointer)
				hov := b.AnimateTo("hov", boolf(b.Hovered()), ui.Animation{Duration: 120 * time.Millisecond})
				b.Background(p.Field.Mix(p.PanelHi, hov))
				b.Border(1, p.Line.Mix(p.LineHi, hov))
				b.Children(func() {
					a.txt(c, u.name, 12, 650, p.Text).SingleLine()
					ui.Icon(c, icon("arrow-right")).Size(12, 12).TextColor(p.Text3)
					a.txt(c, u.where, 12, 500, boolOr(u.warn, p.Warn, p.Text2)).SingleLine()
				})
				if b.Clicked() {
					a.openItem(u.id)
				}
			}
			if extra > 0 {
				a.txt(c, a.tn("nodes.usage.more", extra), 12, 500, p.Text3).SingleLine()
			}
		})
	})
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
	if len(regions) >= 2 {
		ui.Row(c.Key("node-regions")).Gap(8).Wrap().AlignItems(ui.Center).Children(func() {
			if a.filterPill(c, "rg-all", a.t("nodes.all"), len(nodes), a.node.region == "", "") {
				a.node.region = ""
			}
			for _, rc := range regions {
				code := rc.Code
				value, label, badge := code, a.regionName(code), code
				if code == "" {
					value, label, badge = regionOther, a.t("nodes.region_other"), ""
				}
				if a.filterPill(c, "rg-"+value, label, rc.N, a.node.region == value, badge) {
					if a.node.region == value {
						a.node.region = ""
					} else {
						a.node.region = value
					}
				}
			}
		})
	}
	subs := a.subsWithNodes(nodes)
	if len(subs) >= 2 {
		ui.Row(c.Key("node-subs")).Gap(8).Wrap().AlignItems(ui.Center).Children(func() {
			if a.filterPill(c, "sb-all", a.t("nodes.all_subs"), len(nodes), a.node.sub == "", "") {
				a.node.sub = ""
			}
			for _, sc := range subs {
				id := sc.id
				if a.filterPill(c, "sb-"+id, sc.name, sc.n, a.node.sub == id, "") {
					if a.node.sub == id {
						a.node.sub = ""
					} else {
						a.node.sub = id
					}
				}
			}
		})
	}
}

type subCount struct {
	id, name string
	n        int
}

// subsWithNodes lists the subscriptions that have nodes, in the order of the
// subscriptions page.
func (a *App) subsWithNodes(nodes []nodeEntry) []subCount {
	counts := map[string]int{}
	for _, n := range nodes {
		counts[n.SubID]++
	}
	var out []subCount
	for _, s := range a.subs {
		if counts[s.ID] > 0 {
			name := s.Name
			if s.Alias != "" {
				name = s.Alias
			}
			out = append(out, subCount{s.ID, name, counts[s.ID]})
		}
	}
	return out
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
	jumpOnChange(&a.node.list, &a.node.sig, a.node.query+"|"+a.node.region+"|"+a.node.sub+"|"+a.set.SortNodes+"|"+strconv.Itoa(cols))
	mainKey := ""
	if m := a.rt().Main; m.Kind == routing.DestNode && m.Node != nil {
		if n, ok := a.nodeFor(*m.Node); ok != routing.MatchNone {
			mainKey = n.Key
		}
	}
	exitKey := ""
	if ex, ok := a.mainExitNow(); ok && a.snap.Phase.Online() && ex.Node != nil {
		exitKey = ex.Node.Key
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
				a.nodeCard(c, n, mainKey == n.Key, exitKey == n.Key)
			}
		})
	})
	list.Grow(1).Gap(14).Padding(2, 2, 24, 2)
}

func (a *App) nodeCard(c *ui.Context, n nodeEntry, selected, inUse bool) {
	p := a.pl
	// The card is the button that makes the node the main exit, with the
	// button of its menu beside it rather than in it, as on an item's row: a
	// menu found only by a right click is a menu few find.
	card := ui.Row(c.Key("node-" + n.Key))
	card.Grow(1).Basis(0).Height(86).Padding(0, 8, 0, 0).Gap(2).Radius(radiusCard - 2).AlignItems(ui.Center)
	hov := card.AnimateTo("hov", boolf(card.Hovered()), ui.Animation{Duration: 150 * time.Millisecond})
	sel := card.AnimateTo("sel", boolf(selected), ui.Animation{Duration: 260 * time.Millisecond, Ease: ui.EaseOut})
	a.selectableFace(card, hov, sel)
	card.PopoverMenu(a.nodeMenu(c, n))
	card.Children(func() {
		b := ui.ButtonBase(c.Key("node-use-" + n.Key))
		b.Label(n.Name)
		b.Grow(1).Basis(0).AlignSelf(ui.Stretch).Padding(0, 6, 0, 16).Gap(14).Radius(radiusCard - 2).AlignItems(ui.Center).Cursor(ui.CursorPointer)
		b.Children(func() {
			a.regionBadge(c, n.Region, 44)
			ui.Column(c).Grow(1).Basis(0).Gap(6).Children(func() {
				ui.Row(c).Gap(7).AlignItems(ui.Center).Children(func() {
					if inUse {
						a.dot(c, toneGood, 7, true).Tooltip(a.t("nodes.in_use"))
					}
					a.txt(c, n.Name, 14.5, 650, p.Text).SingleLine()
				})
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					a.protoChip(c, n)
					if src := n.Source(); src != "" {
						a.txt(c, src, 11.5, 400, p.Text3).SingleLine()
					}
					if n.NoAuto {
						ui.Icon(c, icon("bolt-slash")).Size(13, 13).TextColor(p.Text3).Shrink(0).Tooltip(a.t("nodes.noauto.tip"))
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
			a.setMain(routing.ToNode(n.ID))
		}
		// Quiet until the card is pointed at, so that a grid of cards does not
		// read as a grid of buttons.
		more := a.btn(c, btnSpec{kind: btnGhost, icon: "more", iconOnly: true, small: true, tip: a.t("subs.more")})
		more.Opacity(0.5 + 0.5*hov).PopoverMenuButton(a.nodeMenu(c, n))
	})
}

// showUnplacedNodes leaves what the person was doing for the nodes page, showing
// the nodes no region could be read from, and says where to set one.
func (a *App) showUnplacedNodes() {
	a.dlgStack, a.dlgOpen = nil, false
	a.node.query, a.node.sub, a.node.region = "", "", ""
	// The filter shows only beside another region; alone it would hide nodes
	// once they get one, with nothing on screen saying why.
	if len(regionsOf(a.liveNodes())) >= 2 {
		a.node.region = regionOther
	}
	a.goTo(pageNodes)
	a.toastInfo(a.t("dest.region.tip"))
}

// nodeMenu is the menu of a node's card.
func (a *App) nodeMenu(c *ui.Context, n nodeEntry) func(m *ui.Menu) {
	return func(m *ui.Menu) {
		if m.Item(a.t("nodes.use")).Chosen() {
			a.setMain(routing.ToNode(n.ID))
		}
		items := append([]routing.Row(nil), a.rv().Rows...)
		m.Submenu(a.t("nodes.use_for"), func(sm *ui.Menu) {
			if len(items) == 0 {
				sm.Item(a.t("nodes.use_for.none")).Disabled(true)
				return
			}
			for _, r := range items {
				id := r.Item.ID
				cur := r.Item.Dest.Kind == routing.DestNode && r.Item.Dest.Node != nil && r.Item.Dest.Node.Same(n.ID)
				if sm.Item(a.itemName(r.Item)).Radio(cur).Chosen() && !cur {
					dest := routing.ToNode(n.ID)
					a.editRouting(func(s *routing.State) error { return s.SetItemDest(id, dest) }, nil)
				}
			}
		})
		m.Separator()
		own := a.rt().NodePref(n.ID)
		bySub := n.NoAuto && !own.NoAuto
		if m.Item(a.t("nodes.noauto")).Checked(own.NoAuto || bySub).Disabled(bySub).Chosen() {
			id, off := n.ID, !own.NoAuto
			a.editRouting(func(s *routing.State) error { s.SetNodeAuto(id, !off); return nil }, nil)
		}
		if m.Item(a.t("nodes.set_region")).Chosen() {
			a.openRegionPicker(n)
		}
		m.Separator()
		if m.Item(a.t("nodes.copy_name")).Chosen() {
			c.WriteClipboard(n.Name)
		}
		if n.Server != "" && m.Item(a.t("nodes.copy_server")).Chosen() {
			c.WriteClipboard(n.Server + ":" + strconv.Itoa(n.Port))
		}
	}
}

// protoChip names a node's protocol and transport in a small tag.
func (a *App) protoChip(c *ui.Context, n nodeEntry) {
	p := a.pl
	label := n.Protocol
	if t := shortTransport(n.Transport); t != "" {
		label += " · " + t
	}
	// The tag keeps its size; the provider's name beside it gives way first.
	ui.Box(c).Padding(2, 8).Radius(7).Background(p.Field).Border(1, p.Line).Shrink(0).Children(func() {
		a.txt(c, label, 11, 600, p.Text2).SingleLine()
	})
}

// shortTransport abbreviates a transport for the small tag. Plain TCP says
// nothing, and the QUIC protocols are QUIC by definition.
func shortTransport(t string) string {
	switch t {
	case "", "TCP", "QUIC":
		return ""
	case "WebSocket":
		return "WS"
	case "HTTP/2":
		return "H2"
	case "HTTPUpgrade":
		return "HTTP↑"
	}
	return t
}
