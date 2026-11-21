package gui

import (
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"eggbox/internal/app"
	"eggbox/internal/routing"
)

// ------------------------------------------------------------ relay labels

// relayShortLabel names a relay target concisely ("Hong Kong 01", "Hong Kong ·
// auto", "Automatic", "Direct").
func (a *App) relayShortLabel(r routing.Relay) string {
	switch r.Kind {
	case routing.RelayAuto:
		return a.t("relay.auto")
	case routing.RelayRegion:
		return a.t("relay.region", "name", a.regionName(r.Region))
	case routing.RelayNode:
		if r.Node == nil {
			return a.t("relay.direct")
		}
		if n, m := a.nodeFor(*r.Node); m != routing.MatchNone {
			return n.Label()
		}
		return a.t("relay.node.gone", "name", r.Node.Name)
	}
	return a.t("relay.direct")
}

// isRelayUnavailable reports whether a relayed node has no usable plain node
// to carry its first hop (blocked rather than falling back to direct).
func (a *App) isRelayUnavailable(n nodeEntry) bool {
	if !n.Relay.Active() {
		return false
	}
	if n.RelayLive != nil {
		return n.RelayLive.Unavailable
	}
	for _, is := range a.snap.Plan.RelayIssues {
		if is.Blocked && is.Target.Same(n.ID) {
			return true
		}
	}
	return false
}

// relayTooltip explains the full chain of a relayed node for a hover tooltip.
func (a *App) relayTooltip(n nodeEntry) string {
	if !n.Relay.Active() {
		return ""
	}
	if a.isRelayUnavailable(n) {
		return a.t("relay.unavailable.tip")
	}
	if ri := n.RelayLive; ri != nil && ri.Node != nil {
		if ri.StandIn && ri.Wanted != nil {
			want := ri.Wanted.Name
			if wn, m := a.nodeFor(*ri.Wanted); m != routing.MatchNone {
				want = wn.Label()
			}
			return a.t("relay.chain.stand_in", "relay", ri.Node.Label(), "node", n.Label(), "want", want)
		}
		return a.t("relay.chain", "relay", ri.Node.Label(), "node", n.Label())
	}
	return a.t("relay.chain", "relay", a.relayShortLabel(n.Relay.Relay), "node", n.Label())
}

// nodeByKey finds a node in the current snapshot by its configuration key.
func (a *App) nodeByKey(key string) (nodeEntry, bool) {
	for _, n := range a.liveNodes() {
		if n.Key == key {
			return n, true
		}
	}
	return nodeEntry{}, false
}

// relayChip renders the first-hop relay chip(s) for a node in the hero, exit
// card, and tray panel. Nothing is drawn for plain direct nodes.
func (a *App) relayChip(c *ui.Context, nodeKey, nodeName string) {
	if a.snap.Phase.Online() {
		if ri, ok := a.snap.Relays[nodeKey]; ok {
			switch {
			case ri.Unavailable:
				a.chip(c, a.t("relay.unavailable"), toneWarn, "alert").Tooltip(a.t("relay.unavailable.tip"))
			case ri.Node != nil:
				tip := a.t("relay.chain", "relay", ri.Node.Label(), "node", nodeName)
				if ri.StandIn && ri.Wanted != nil {
					want := ri.Wanted.Name
					if wn, m := a.nodeFor(*ri.Wanted); m != routing.MatchNone {
						want = wn.Label()
					}
					tip = a.t("relay.chain.stand_in", "relay", ri.Node.Label(), "node", nodeName, "want", want)
				}
				a.chip(c, a.t("relay.via", "relay", truncate(ri.Node.Label(), 22)), toneNeutral, "relay").Tooltip(tip)
				if ri.StandIn {
					a.chip(c, a.t("relay.stand_in"), toneWarn, "alert").Tooltip(a.t("relay.stand_in.tip"))
				}
			default:
				a.chip(c, a.t("relay.via", "relay", truncate(a.relayShortLabel(ri.Config.Relay), 22)), toneNeutral, "relay")
			}
			return
		}
	}
	n, ok := a.nodeByKey(nodeKey)
	if !ok || !n.Relay.Active() {
		return
	}
	if a.isRelayUnavailable(n) {
		a.chip(c, a.t("relay.unavailable"), toneWarn, "alert").Tooltip(a.t("relay.unavailable.tip"))
		return
	}
	a.chip(c, a.t("relay.via", "relay", truncate(a.relayShortLabel(n.Relay.Relay), 22)), toneNeutral, "relay").Tooltip(a.relayTooltip(n))
}

// relaySubMaps returns subscription names and active default relays of enabled
// subscriptions, keyed by subscription ID, for conflict checks.
func (a *App) relaySubMaps() (map[string]string, map[string]*routing.Relay) {
	names := make(map[string]string, len(a.subs))
	relays := make(map[string]*routing.Relay, len(a.subs))
	for _, s := range a.subs {
		names[s.ID] = s.Name
		if s.Enabled && s.Relay != nil && s.Relay.Active() {
			relays[s.ID] = s.Relay
		}
	}
	return names, relays
}

// relayNodeInfos builds the snapshot of nodes needed by routing's reverse
// dependent checks.
func (a *App) relayNodeInfos(nodes []nodeEntry) []routing.RelayNodeInfo {
	out := make([]routing.RelayNodeInfo, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, routing.RelayNodeInfo{
			ID:            n.ID,
			Name:          n.Label(),
			SubID:         n.SubID,
			SubRelay:      n.SubRelay,
			ProviderRelay: n.ProviderRelay,
		})
	}
	return out
}

// formatRelayDeps joins up to three dependent names for a conflict warning.
func (a *App) formatRelayDeps(deps []routing.RelayDependent) string {
	names := make([]string, 0, min(len(deps), 3))
	for i, dep := range deps {
		if i == 3 {
			break
		}
		if dep.IsSub {
			names = append(names, a.t("relaydlg.blocked.sub_tag", "name", dep.Name))
			continue
		}
		names = append(names, dep.Name)
	}
	return strings.Join(names, a.t("proxy.list.sep"))
}

// ------------------------------------------------------------- relay dialog

// relayDialog chooses the first-hop relay for either one node or a
// subscription's default. Only plain direct nodes are selectable as relays so
// every connection stays at most two hops and cannot form a cycle.
type relayDialog struct {
	// Node mode sets node; subscription mode sets forSub (and subID when
	// editing an existing subscription).
	node    *nodeEntry
	forSub  bool
	subID   string
	subName string
	cur     *routing.Relay // subscription mode's current choice
	pick    func(*routing.Relay)

	replace bool // same-region stand-in when a pinned relay node is down
	query   string
	list    ui.ListState
	sig     string
}

// openNodeRelay opens the relay picker for a single node.
func (a *App) openNodeRelay(n nodeEntry) {
	cp := n
	replace := true
	if n.Relay.Relay.Kind == routing.RelayNode && n.Relay.Relay.NoFallback {
		replace = false
	}
	a.openDialog(&relayDialog{node: &cp, replace: replace})
}

// openSubRelay opens the default relay picker over the subscription dialog.
func (a *App) openSubRelay(subID, subName string, cur *routing.Relay, pick func(*routing.Relay)) {
	replace := true
	if cur != nil && cur.Kind == routing.RelayNode && cur.NoFallback {
		replace = false
	}
	a.pushDialog(&relayDialog{
		forSub:  true,
		subID:   subID,
		subName: subName,
		cur:     routing.CloneRelayPtr(cur),
		pick:    pick,
		replace: replace,
	})
}

func (d *relayDialog) apply(a *App, next *routing.Relay) {
	a.dlgOpen = false
	if d.forSub {
		if d.pick != nil {
			d.pick(routing.CloneRelayPtr(next))
		}
		return
	}
	n := *d.node
	wasActive := n.Relay.Active()
	preview := a.rt().Clone()
	_ = preview.SetNodeRelay(n.ID, next)
	effNext := routing.ResolveRelay(n.ID, preview, n.SubRelay, n.ProviderRelay)
	turnOffAuto := !wasActive && effNext.Active() && !a.rt().NodePref(n.ID).NoAuto
	id := n.ID
	val := routing.CloneRelayPtr(next)
	a.editRouting(func(s *routing.State) error {
		if err := s.SetNodeRelay(id, val); err != nil {
			return err
		}
		if turnOffAuto {
			s.SetNodeAuto(id, false)
		}
		return nil
	}, nil)
}

// candidateDisabled explains why a node cannot be chosen as a relay, or "".
func (d *relayDialog) candidateDisabled(a *App, n nodeEntry) string {
	if !d.forSub && d.node != nil && n.ID.Same(d.node.ID) {
		return a.t("relaydlg.disabled.self")
	}
	if d.forSub && d.subID != "" && n.SubID == d.subID {
		return a.t("relaydlg.disabled.same_sub")
	}
	if n.Relay.Active() {
		return a.t("relaydlg.disabled.relayed")
	}
	return ""
}

func (d *relayDialog) rows(a *App, nodes []nodeEntry) []nodeRow {
	q := strings.ToLower(strings.TrimSpace(d.query))
	subs := map[string]bool{}
	for _, n := range nodes {
		subs[n.SubID] = true
	}
	var out []nodeRow
	last := "\x00"
	for _, n := range nodes {
		if q != "" && !a.matchNode(n, q) {
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

func (d *relayDialog) frame(a *App, c *ui.Context) dlgFrame {
	p := a.pl
	nodes := a.liveNodes()
	// Refresh node snapshot in node mode so live state stays current.
	if !d.forSub && d.node != nil {
		if cur, m := a.nodeFor(d.node.ID); m != routing.MatchNone {
			d.node = &cur
		}
	}

	// Reverse dependency check: cannot make a node/subscription relayed while
	// other nodes or subscriptions explicitly pin it as their relay.
	var deps []routing.RelayDependent
	infos := a.relayNodeInfos(nodes)
	subNames, subRelays := a.relaySubMaps()
	if d.forSub {
		if d.subID != "" {
			deps = routing.RelayDependentsOfSub(d.subID, infos, subNames, subRelays, *a.rt())
		}
	} else if d.node != nil {
		deps = routing.RelayDependentsOfNode(d.node.ID, infos, subNames, subRelays, *a.rt())
	}
	blocked := len(deps) > 0

	// Eligible plain nodes (for Automatic and Region automatic pools).
	var plain []nodeEntry
	for _, n := range nodes {
		if d.candidateDisabled(a, n) == "" {
			plain = append(plain, n)
		}
	}
	curRegion := ""
	curNodeKey := ""
	var curDirect, curAuto, curRegionOn, curInherited bool
	if d.forSub {
		switch {
		case d.cur == nil || !d.cur.Active():
			curDirect = true
		case d.cur.Kind == routing.RelayAuto:
			curAuto = true
		case d.cur.Kind == routing.RelayRegion:
			curRegionOn, curRegion = true, d.cur.Region
		case d.cur.Kind == routing.RelayNode && d.cur.Node != nil:
			if n, m := a.nodeFor(*d.cur.Node); m != routing.MatchNone {
				curNodeKey = n.Key
			}
		}
	} else if d.node != nil {
		eff := d.node.Relay
		switch eff.Source {
		case routing.RelaySourceSub, routing.RelaySourceProvider:
			curInherited = true
		case routing.RelaySourceNode:
			switch eff.Relay.Kind {
			case routing.RelayDirect, "":
				curDirect = true
			case routing.RelayAuto:
				curAuto = true
			case routing.RelayRegion:
				curRegionOn, curRegion = true, eff.Relay.Region
			case routing.RelayNode:
				if eff.Relay.Node != nil {
					if n, m := a.nodeFor(*eff.Relay.Node); m != routing.MatchNone {
						curNodeKey = n.Key
					}
				}
			}
		default:
			curDirect = true
		}
	}

	plainRegions := regionsWithNodes(plain, curRegion)
	rows := d.rows(a, nodes)

	title, subtitle, bodyText := a.t("relaydlg.title"), "", a.t("relaydlg.body")
	if d.forSub {
		title, subtitle, bodyText = a.t("relaydlg.sub.title"), d.subName, a.t("relaydlg.sub.body")
	} else if d.node != nil {
		subtitle = d.node.Label()
	}

	return dlgFrame{
		title: title, subtitle: subtitle, icon: "relay", tone: toneBlue, width: 520,
		body: func() {
			a.txt(c, bodyText, 12.5, 400, p.Text2).LineHeight(1.45)
			if blocked {
				msg := a.tn("relaydlg.blocked", len(deps), "names", a.formatRelayDeps(deps))
				a.alertBanner(c, "relay-blocked", toneWarn, msg, false, func() {})
			}
			if !d.forSub && d.node != nil && d.node.NeedsUDP {
				a.alertBanner(c, "relay-udp", toneBlue, a.t("relaydlg.udp_note", "proto", d.node.Protocol), false, func() {})
			}

			ui.Column(c).Gap(5).Children(func() {
				// Inherited option (follow subscription default or provider relay).
				if !d.forSub && d.node != nil {
					switch {
					case d.node.SubRelay != nil && d.node.SubRelay.Active():
						lbl := a.t("relaydlg.follow_sub", "relay", a.relayShortLabel(*d.node.SubRelay))
						if d.optionRow(a, c, "follow", "rss", toneBlue, lbl, a.t("relaydlg.follow_sub.desc"), curInherited, blocked) {
							d.apply(a, nil)
						}
					case d.node.ProviderRelay != nil && d.node.ProviderRelay.Active():
						lbl := a.t("relaydlg.follow_provider", "relay", a.relayShortLabel(*d.node.ProviderRelay))
						if d.optionRow(a, c, "follow", "cloud", toneBlue, lbl, a.t("relaydlg.follow_provider.desc"), curInherited, blocked) {
							d.apply(a, nil)
						}
					}
				}
				// Direct.
				if d.optionRow(a, c, "direct", "arrow-right", toneNeutral, a.t("relay.direct"), a.t("relay.direct.desc"), curDirect, false) {
					if d.forSub {
						d.apply(a, nil)
					} else if d.node != nil && ((d.node.SubRelay != nil && d.node.SubRelay.Active()) || (d.node.ProviderRelay != nil && d.node.ProviderRelay.Active())) {
						r := routing.RelayDirectVal()
						d.apply(a, &r)
					} else {
						d.apply(a, nil)
					}
				}
				// Automatic (fastest plain direct node).
				autoDisabled := blocked || len(plain) == 0
				autoDesc := a.t("relay.auto.desc")
				if len(plain) == 0 {
					autoDesc = a.t("relay.region.none")
				}
				if d.optionRow(a, c, "auto", "zap", toneBlue, a.t("relay.auto"), autoDesc, curAuto, autoDisabled) {
					r := routing.RelayAutoVal()
					d.apply(a, &r)
				}
				// Region automatic (popover menu over plain regions).
				d.regionRow(a, c, plainRegions, curRegionOn, curRegion, blocked || len(plainRegions) == 0)
			})

			a.hr(c)
			a.eyebrow(c, a.t("relaydlg.section.nodes"))
			a.search(c, "rl-q", &d.query, a.t("relaydlg.search"), 0)
			jumpOnChange(&d.list, &d.sig, d.query)
			if len(rows) == 0 {
				a.txt(c, a.t("relaydlg.none"), 13, 400, p.Text3).Padding(18, 0).TextAlign(ui.Center)
			} else {
				ui.List(c.Key("rl-list"), &d.list, len(rows), func(i int) {
					r := rows[i]
					if r.header != "" {
						a.eyebrow(c, strings.ToUpper(r.header)).Padding(8, 4, 3, 4)
						return
					}
					reason := d.candidateDisabled(a, r.node)
					d.nodeButton(a, c, r.node, r.node.Key == curNodeKey, blocked || reason != "", reason)
				}).Height(220).Gap(4).Padding(0, 0, 4, 0)
			}
			a.hr(c)
			on := d.replace
			if a.toggleRow(c, "rl-stand", a.t("relaydlg.stand"), a.t("relaydlg.stand.desc"), &on, blocked) {
				d.replace = on
			}
		},
		buttons: func() {
			if a.btn(c, btnSpec{kind: btnGhost, label: a.t("common.cancel")}).Clicked() {
				a.dlgOpen = false
			}
		},
	}
}

func (d *relayDialog) optionRow(a *App, c *ui.Context, key, iconName string, t tone, title, desc string, current, disabled bool) bool {
	p := a.pl
	b := ui.ButtonBase(c.Key("rl-opt-" + key))
	b.Label(title).Disabled(disabled)
	b.Height(42).Padding(0, 12).Gap(10).Radius(12).AlignItems(ui.Center).Cursor(boolOr(disabled, ui.CursorDefault, ui.CursorPointer))
	hov := b.AnimateTo("hov", boolf(b.Hovered() && !disabled), ui.Animation{Duration: 120 * time.Millisecond})
	b.Background(p.Field.Mix(p.PanelHi, hov).Mix(p.Blue.Alpha(0.14), boolf(current)))
	b.Border(1, p.Line.Mix(p.LineHi, hov).Mix(p.Blue.Alpha(0.6), boolf(current)))
	if disabled {
		b.Opacity(0.48)
	}
	b.Children(func() {
		a.iconTile(c, iconName, t, 30)
		ui.Column(c).Grow(1).Basis(0).Gap(2).Children(func() {
			a.txt(c, title, 13.5, 600, p.Text).SingleLine()
			a.txt(c, desc, 11.5, 400, p.Text3).SingleLine()
		})
		if current {
			ui.Icon(c, icon("check")).Size(16, 16).TextColor(p.BlueText)
		}
	})
	return !disabled && b.Clicked()
}

func (d *relayDialog) regionRow(a *App, c *ui.Context, regions []regionCount, current bool, curCode string, disabled bool) {
	p := a.pl
	title := a.t("relay.region.menu")
	desc := a.t("relay.region.none")
	if current && curCode != "" {
		desc = a.t("relay.region", "name", a.regionName(curCode))
	} else if len(regions) > 0 {
		names := make([]string, 0, min(len(regions), 3))
		for i, rc := range regions {
			if i == 3 {
				break
			}
			names = append(names, a.regionName(rc.Code))
		}
		desc = strings.Join(names, " · ")
	}
	b := ui.ButtonBase(c.Key("rl-opt-region"))
	b.Label(title).Disabled(disabled)
	b.Height(42).Padding(0, 12).Gap(10).Radius(12).AlignItems(ui.Center).Cursor(boolOr(disabled, ui.CursorDefault, ui.CursorPointer))
	hov := b.AnimateTo("hov", boolf(b.Hovered() && !disabled), ui.Animation{Duration: 120 * time.Millisecond})
	b.Background(p.Field.Mix(p.PanelHi, hov).Mix(p.Blue.Alpha(0.14), boolf(current)))
	b.Border(1, p.Line.Mix(p.LineHi, hov).Mix(p.Blue.Alpha(0.6), boolf(current)))
	if disabled {
		b.Opacity(0.48)
	}
	b.Children(func() {
		if current && curCode != "" {
			a.regionBadge(c, curCode, 30)
		} else {
			a.iconTile(c, "globe", toneBlue, 30)
		}
		ui.Column(c).Grow(1).Basis(0).Gap(2).Children(func() {
			a.txt(c, title, 13.5, 600, p.Text).SingleLine()
			a.txt(c, desc, 11.5, 400, p.Text3).SingleLine()
		})
		if current {
			ui.Icon(c, icon("check")).Size(16, 16).TextColor(p.BlueText)
		}
		ui.Icon(c, icon("chevron-down")).Size(15, 15).TextColor(p.Text3)
	})
	if !disabled {
		b.PopoverMenuButton(func(m *ui.Menu) {
			for _, rc := range regions {
				label := a.regionName(rc.Code)
				if rc.N > 0 {
					label += "  (" + strconv.Itoa(rc.N) + ")"
				}
				if m.Item(label).Radio(current && curCode == rc.Code).Chosen() {
					r := routing.RelayRegionVal(rc.Code)
					d.apply(a, &r)
				}
			}
		})
	}
}

func (d *relayDialog) nodeButton(a *App, c *ui.Context, n nodeEntry, current, disabled bool, reason string) {
	p := a.pl
	b := ui.ButtonBase(c.Key("rl-n-" + n.Key))
	b.Label(n.Label()).Disabled(disabled)
	b.Height(46).Padding(0, 12).Gap(12).Radius(12).AlignItems(ui.Center).Cursor(boolOr(disabled, ui.CursorDefault, ui.CursorPointer))
	hov := b.AnimateTo("hov", boolf(b.Hovered() && !disabled), ui.Animation{Duration: 120 * time.Millisecond})
	b.Background(p.Field.Mix(p.PanelHi, hov).Mix(p.Blue.Alpha(0.14), boolf(current)))
	b.Border(1, p.Line.Mix(p.LineHi, hov).Mix(p.Blue.Alpha(0.6), boolf(current)))
	if disabled {
		b.Opacity(0.45)
	}
	b.Children(func() {
		a.regionBadge(c, n.Region, 30)
		ui.Column(c).Grow(1).Basis(0).Gap(2).Children(func() {
			a.txt(c, n.Name, 13.5, 600, p.Text).SingleLine()
			sub := n.Protocol
			if n.Region != "" {
				sub = a.regionName(n.Region) + " · " + sub
			}
			a.txt(c, sub, 11.5, 400, p.Text3).SingleLine()
		})
		if reason != "" {
			a.txt(c, reason, 11.5, 500, p.Text3).SingleLine().Shrink(0)
		} else {
			switch {
			case n.Delay > 0:
				a.number(c, fmtMillis(n.Delay), 13, a.toneColor(latencyTone(n.Delay)))
			case n.Delay < 0:
				a.txt(c, a.t("nodes.timeout"), 12, 600, p.Bad).SingleLine()
			}
			if current {
				ui.Icon(c, icon("check")).Size(16, 16).TextColor(p.BlueText)
			}
		}
	})
	if !disabled && b.Clicked() {
		r := routing.RelayNodeVal(n.ID)
		r.NoFallback = !d.replace
		d.apply(a, &r)
	}
}

// relayBannerText words a plan-level relay issue for the Nodes page banner.
func (a *App) relayBannerText(is app.RelayIssue, targetLabel string, live *app.RelayInfo) string {
	if is.Blocked {
		switch is.Kind {
		case routing.RelayNode:
			return a.t("banner.relay.blocked.node", "target", targetLabel, "node", is.Relay.Name)
		case routing.RelayRegion:
			return a.t("banner.relay.blocked.region", "target", targetLabel, "region", a.regionName(is.Region))
		default:
			return a.t("banner.relay.blocked.auto", "target", targetLabel)
		}
	}
	if live != nil && live.Node != nil {
		return a.t("banner.relay.gone.using", "target", targetLabel, "node", is.Relay.Name, "using", live.Node.Label())
	}
	return a.t("banner.relay.gone", "target", targetLabel, "node", is.Relay.Name)
}
