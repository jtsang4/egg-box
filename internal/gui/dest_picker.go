package gui

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"eggbox/internal/routing"
)

// destOpts shapes a destination picker.
type destOpts struct {
	key string
	// width of the button; 0 takes the usual one.
	width float32
	// title says what the choice is for, in the node list's header.
	title string
}

const destWidth = 196

// destButton is the choice of where traffic goes, the same in every place that
// has one: a button showing the destination that opens one menu of choices.
func (a *App) destButton(c *ui.Context, o destOpts, cur routing.Dest, pick func(routing.Dest)) ui.Element {
	p := a.pl
	t := destTone(cur)
	missing := a.destMissing(cur)
	label := a.destLabel(cur)
	col := a.toneColor(t)
	b := ui.ButtonBase(c.Key(o.key))
	b.Label(label)
	w := o.width
	if w == 0 {
		w = destWidth
	}
	if a.compact() && o.width == 0 {
		// Across the rest of the row, a finger's height.
		b.Grow(1).Basis(0).MinWidth(0).Height(36)
	} else {
		b.Width(w).Height(32).Shrink(0)
	}
	b.Padding(0, 10).Gap(7).Radius(10).AlignItems(ui.Center).Cursor(ui.CursorPointer)
	hov := b.AnimateTo("hov", boolf(b.Hovered()), ui.Animation{Duration: 130 * time.Millisecond})
	switch {
	case missing:
		b.Background(p.Warn.Alpha(0.12+0.06*hov)).Border(1, p.Warn.Alpha(0.35))
		col = p.Warn
	case t == toneNeutral:
		b.Background(p.Field.Mix(p.PanelHi, hov)).Border(1, p.Line.Mix(p.LineHi, hov))
	default:
		b.Background(col.Alpha(0.1+0.07*hov)).Border(1, col.Alpha(0.2+0.1*hov))
	}
	if cur.Kind == routing.DestFollow {
		b.Tooltip(a.t("dest.follow.tip", "now", a.mainBrief()))
	} else if missing {
		b.Tooltip(a.t("dest.node.gone.tip"))
	}
	b.Children(func() {
		ui.Icon(c, icon(destIcon(cur))).Size(14, 14).TextColor(col).Shrink(0)
		a.txt(c, label, 13, 600, boolOr(t == toneNeutral, p.Text, p.Text.Mix(col, 0.25))).SingleLine().Grow(1).Basis(0)
		ui.Icon(c, icon("chevron-down")).Size(14, 14).TextColor(p.Text3).Shrink(0)
	})
	b.PopoverMenuButton(a.destMenu(o, cur, pick))
	return b
}

// destMenu builds the one menu of destinations.
func (a *App) destMenu(o destOpts, cur routing.Dest, pick func(routing.Dest)) func(m *ui.Menu) {
	return func(m *ui.Menu) {
		if m.Item(a.t("dest.follow.menu", "main", a.destLabel(a.rt().Main))).Radio(cur.Kind == routing.DestFollow).Chosen() {
			pick(routing.Follow())
		}
		if m.Item(a.t("dest.direct")).Radio(cur.Kind == routing.DestDirect).Chosen() {
			pick(routing.Direct())
		}
		if m.Item(a.t("dest.reject")).Radio(cur.Kind == routing.DestReject).Chosen() {
			pick(routing.Reject())
		}
		m.Separator()
		keep := ""
		if cur.Kind == routing.DestRegion {
			keep = cur.Region
		}
		nodes := a.liveNodes()
		regions := regionsWithNodes(nodes, keep)
		m.Submenu(a.t("dest.region.menu"), func(sm *ui.Menu) {
			if len(regions) == 0 {
				sm.Item(a.t("dest.region.none")).Disabled(true)
			}
			for _, rc := range regions {
				label := a.regionName(rc.Code)
				if rc.N > 0 {
					label += "  (" + strconv.Itoa(rc.N) + ")"
				}
				if sm.Item(label).Radio(cur.Kind == routing.DestRegion && cur.Region == rc.Code).Chosen() {
					pick(routing.ToRegion(rc.Code))
				}
			}
			// The region of a node is read from its name; where that says
			// nothing, the person can tell it.
			if slices.ContainsFunc(nodes, func(n nodeEntry) bool { return n.Region == "" }) {
				sm.Separator()
				if sm.Item(a.t("dest.region.set")).Chosen() {
					a.showUnplacedNodes()
				}
			}
		})
		if m.Item(a.t("dest.node.menu")).Radio(cur.Kind == routing.DestNode).Chosen() {
			a.pickNodeFor(o.title, cur, true, pick)
		}
	}
}

// ---------------------------------------------------------------- nodes

// nodeDialog lists the nodes to pin one: by subscription, with how fast each
// answers. It opens over whatever asked for it and gives back to it.
type nodeDialog struct {
	forWhat string
	cur     routing.Dest
	// stand shows the choice of a stand-in node (the main exit has its own
	// logic and does not offer it).
	stand   bool
	replace bool
	pick    func(routing.Dest)
	query   string
	list    ui.ListState
	sig     string
}

// pickNodeFor opens the list of nodes; pick gets the destination chosen.
func (a *App) pickNodeFor(forWhat string, cur routing.Dest, stand bool, pick func(routing.Dest)) {
	a.pushDialog(&nodeDialog{
		forWhat: forWhat, cur: cur, stand: stand, pick: pick,
		replace: !(cur.Kind == routing.DestNode && cur.NoFallback),
	})
}

// nodeRow is a line of the list: a subscription's name or a node.
type nodeRow struct {
	header string
	node   nodeEntry
}

func (d *nodeDialog) rows(a *App, nodes []nodeEntry) []nodeRow {
	q := strings.ToLower(strings.TrimSpace(d.query))
	subs := map[string]bool{}
	for _, n := range nodes {
		subs[n.SubID] = true
	}
	var out []nodeRow
	last := "\x00"
	for _, n := range nodes {
		if q != "" && !a.matchNode(n, q) && !strings.Contains(strings.ToLower(a.regionName(n.Region)), q) {
			continue
		}
		if len(subs) > 1 && n.SubID != last {
			out = append(out, nodeRow{header: n.Source()})
			last = n.SubID
		}
		out = append(out, nodeRow{node: n})
	}
	return out
}

func (d *nodeDialog) frame(a *App, c *ui.Context) dlgFrame {
	p := a.pl
	nodes := a.liveNodes()
	rows := d.rows(a, nodes)
	curKey := ""
	if d.cur.Kind == routing.DestNode && d.cur.Node != nil {
		if n, m := a.nodeFor(*d.cur.Node); m != routing.MatchNone {
			curKey = n.Key
		}
	}
	return dlgFrame{
		title: a.t("nodedlg.title"), subtitle: d.forWhat, icon: "server", tone: toneBlue, width: 520,
		body: func() {
			a.searchFirst(c, "np-q", &d.query, a.t("nodes.search"), 0)
			jumpOnChange(&d.list, &d.sig, d.query)
			if len(rows) == 0 {
				a.txt(c, a.t("nodedlg.none"), 13, 400, p.Text3).Padding(24, 0).TextAlign(ui.Center)
			} else {
				ui.List(c.Key("np-list"), &d.list, len(rows), func(i int) {
					r := rows[i]
					if r.header != "" {
						a.eyebrow(c, strings.ToUpper(r.header)).Padding(10, 4, 4, 4)
						return
					}
					d.nodeButton(a, c, r.node, r.node.Key == curKey)
				}).Height(330).Gap(4).Padding(0, 0, 4, 0)
			}
			if d.stand {
				a.hr(c)
				on := d.replace
				if a.toggleRow(c, "np-stand", a.t("nodedlg.stand"), a.t("nodedlg.stand.desc"), &on, false) {
					d.replace = on
				}
			}
		},
		buttons: func() {
			if a.btn(c, btnSpec{kind: btnGhost, label: a.t("common.cancel")}).Clicked() {
				a.dlgOpen = false
			}
		},
	}
}

func (d *nodeDialog) nodeButton(a *App, c *ui.Context, n nodeEntry, current bool) {
	p := a.pl
	b := ui.ButtonBase(c.Key("np-" + n.Key))
	b.Label(n.Label())
	b.Height(48).Padding(0, 12).Gap(12).Radius(12).AlignItems(ui.Center).Cursor(ui.CursorPointer)
	hov := b.AnimateTo("hov", boolf(b.Hovered()), ui.Animation{Duration: 120 * time.Millisecond})
	b.Background(p.Field.Mix(p.PanelHi, hov).Mix(p.Blue.Alpha(0.14), boolf(current)))
	b.Border(1, p.Line.Mix(p.LineHi, hov).Mix(p.Blue.Alpha(0.6), boolf(current)))
	b.Children(func() {
		a.regionBadge(c, n.Region, 30)
		ui.Column(c).Grow(1).Basis(0).Gap(2).Children(func() {
			a.txt(c, n.Name, 13.5, 600, p.Text).SingleLine()
			sub := n.Protocol
			if n.Region != "" {
				sub = a.regionName(n.Region) + " · " + sub
			}
			if n.Relay.Active() {
				if a.isRelayUnavailable(n) {
					sub += " · " + a.t("relay.unavailable")
				} else {
					sub += " · " + a.t("relay.via", "relay", a.relayShortLabel(n.Relay.Relay))
				}
			}
			a.txt(c, sub, 11.5, 400, p.Text3).SingleLine()
		})
		switch {
		case n.Delay > 0:
			a.number(c, fmtMillis(n.Delay), 13, a.toneColor(latencyTone(n.Delay)))
		case n.Delay < 0:
			a.txt(c, a.t("nodes.timeout"), 12, 600, p.Bad).SingleLine()
		}
		if current {
			ui.Icon(c, icon("check")).Size(16, 16).TextColor(p.BlueText)
		}
	})
	if b.Clicked() {
		dest := routing.ToNode(n.ID)
		dest.NoFallback = d.stand && !d.replace
		a.dlgOpen = false
		d.pick(dest)
	}
}
