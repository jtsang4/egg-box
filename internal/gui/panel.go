package gui

import (
	"strconv"
	"time"

	"github.com/egoist/mygo/ui"

	"eggbox/internal/app"
	"eggbox/internal/brand"
	"eggbox/internal/configgen"
	"eggbox/internal/library"
	"eggbox/internal/routing"
)

// The panel is the small window that opens from the tray icon: the
// connection, the node in use and the fastest alternatives, one click from
// anywhere. It is a second view of the same App, so it shows exactly what the
// main window shows, and every control in it calls what the main window's
// controls call.

// The panel's size, in device-independent pixels.
const (
	PanelWidth  = 344
	PanelHeight = 592
)

const panelPad = 16

// PanelView draws the content of the tray panel. It runs on the main thread,
// once per frame of the panel.
func (a *App) PanelView(c *ui.Context) {
	a.resolve(c, &a.psvc)
	ph := a.snap.Phase
	c.Root().Background(ui.Transparent)

	root := ui.Column(c.Key("panel")).Fill()
	a.panelSurface(root)
	root.Children(func() {
		a.panelHeader(c)
		a.panelHero(c)
		if ph.Online() {
			a.panelLive(c)
		} else {
			a.panelIdle(c)
		}
		ui.Spacer(c)
		a.panelFooter(c)
	})
}

// PanelClosed tells the App that the panel is gone, so that nothing tries to
// redraw it.
func (a *App) PanelClosed() { a.psvc.Store(nil) }

// panelSurface paints the panel's body: a deep blue that lights up as the
// connection comes alive. Windows rounds and outlines flyouts itself, macOS
// outlines them and follows the shape drawn here; elsewhere the panel draws
// its own edge.
func (a *App) panelSurface(e ui.Element) {
	p := a.pl
	on := e.AnimateTo("on", boolf(a.snap.Phase == app.PhaseConnected), ui.Animation{Duration: 900 * time.Millisecond, Ease: ui.EaseOut})
	var top, bottom ui.Color
	if p.Dark {
		top, bottom = ui.Hex("#0C1636").Mix(ui.Hex("#123068"), on), p.Window
	} else {
		top, bottom = ui.Hex("#FFFFFF").Mix(ui.Hex("#DDE9FF"), on), ui.Hex("#EEF3FD")
	}
	e.LinearGradient(ui.LinearGradient{From: top, To: bottom, Angle: 175, Oklab: true})
	switch a.platform {
	case "windows":
	case "darwin":
		e.Radius(16).Clip()
	default:
		e.Radius(14).Border(1, p.LineHi).Clip()
	}
}

func (a *App) panelHeader(c *ui.Context) {
	p := a.pl
	t, word := a.phaseTone(a.snap.Phase)
	ui.Row(c).Height(52).Padding(0, panelPad).Gap(10).AlignItems(ui.Center).Children(func() {
		a.logo(c, 24)
		a.txt(c, brand.DisplayName, 14, 700, p.Text).SingleLine().Grow(1)
		a.chip(c, word, t, "")
	})
}

// panelHero is the orb and what it says: the node in use, or what to do.
func (a *App) panelHero(c *ui.Context) {
	ph := a.snap.Phase
	ui.Column(c).AlignItems(ui.Center).Padding(0, panelPad, 10, panelPad).Gap(4).Children(func() {
		if a.orb(c, 120).Clicked() {
			a.toggleConnection()
		}
		t, label := a.phaseTone(ph)
		switch {
		case ph == app.PhaseNoAgent:
			a.eyebrowDot(c, a.t("hero.eyebrow.service"), toneWarn, false)
			a.panelTitle(c, a.t("hero.noagent.title"))
			a.panelSub(c, a.t("phase.noagent.hint"))
		case len(a.subs) == 0 && a.nodeCount() == 0:
			a.eyebrowDot(c, a.t("hero.eyebrow.welcome"), toneBlue, false)
			a.panelTitle(c, a.t("hero.empty.title"))
			a.panelSub(c, a.t("panel.empty"))
		case ph.Online():
			eyebrow := a.t("hero.eyebrow.on")
			if ph == app.PhaseReconnecting {
				eyebrow = a.t("hero.eyebrow.working")
			}
			a.eyebrowDot(c, eyebrow, t, ph == app.PhaseConnected)
			if ex, ok := a.mainExitNow(); ok {
				a.panelTitle(c, ex.Node.Label())
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					a.chip(c, ex.Node.Protocol, toneBlue, "")
					if ex.Via != "" {
						a.chip(c, a.viaLabel(ex.Via), toneNeutral, "zap")
					}
					if ex.StandIn {
						a.chip(c, a.t("hero.stand_in"), toneWarn, "alert").Tooltip(a.t("hero.stand_in.tip"))
					}
				})
			} else {
				a.panelTitle(c, label)
				a.panelSub(c, a.t("hero.sub.noexit"))
			}
		default:
			eyebrow := a.t("hero.eyebrow.off")
			switch ph {
			case app.PhaseConnecting, app.PhaseDisconnecting:
				eyebrow = a.t("hero.eyebrow.working")
			case app.PhaseFailed:
				eyebrow = a.t("hero.eyebrow.failed")
			}
			a.eyebrowDot(c, eyebrow, t, false)
			a.panelTitle(c, label)
			sub := a.phaseHint(ph)
			if st := a.snap.Status; ph == app.PhaseFailed && st != nil && st.GetLastError() != "" {
				sub = truncate(st.GetLastError(), 90)
			}
			a.panelSub(c, sub)
		}
	})
}

func (a *App) panelTitle(c *ui.Context, s string) ui.Element {
	return a.txt(c, s, 20, 700, a.pl.Text).LetterSpacing(-0.4).SingleLine()
}

func (a *App) panelSub(c *ui.Context, s string) ui.Element {
	return a.txt(c, s, 12.5, 400, a.pl.Text2).LineHeight(1.4).TextAlign(ui.Center)
}

// ------------------------------------------------------------- connected

func (a *App) panelLive(c *ui.Context) {
	ui.Column(c).Padding(0, panelPad).Gap(8).Children(func() {
		a.panelTraffic(c)
		a.panelMode(c)
		a.panelNodes(c)
	})
}

// panelTraffic is the live rates over a quiet sparkline.
func (a *App) panelTraffic(c *ui.Context) {
	p := a.pl
	tr := a.snap.Traffic
	card := ui.Column(c.Key("panel-traffic")).Padding(11, 14, 8, 14).Radius(16).Gap(4)
	card.Background(p.Panel.Alpha(0.9)).Border(1, p.Line)
	card.Children(func() {
		ui.Row(c).Gap(14).Children(func() {
			for _, it := range []struct {
				icon, key string
				val       float64
				col       ui.Color
			}{
				{"arrow-down", "dash.down", tr.Down, p.BlueText},
				{"arrow-up", "dash.up", tr.Up, p.Indigo},
			} {
				num, unit := splitRate(it.val)
				ui.Column(c).Grow(1).Basis(0).Gap(2).Children(func() {
					ui.Row(c).Gap(5).AlignItems(ui.Center).Children(func() {
						ui.Icon(c, icon(it.icon)).Size(12, 12).TextColor(it.col)
						a.eyebrow(c, a.t(it.key))
					})
					ui.Row(c).Gap(4).AlignItems(ui.Baseline).Children(func() {
						a.number(c, num, 22, p.Text)
						a.txt(c, unit, 12, 500, p.Text3).SingleLine()
					})
				})
			}
		})
		a.sparkline(c, "panel", tr.History, 24, true)
	})
}

// panelMode is the routing mode switch.
func (a *App) panelMode(c *ui.Context) {
	cur := 0
	if st := a.snap.Status; st != nil {
		for i, m := range modeNames {
			if st.GetMode() == m {
				cur = i
			}
		}
	}
	items := []segItem{
		{a.t("mode.rule"), "route"},
		{a.t("mode.global"), "globe"},
		{a.t("mode.direct"), "arrow-right"},
	}
	sel := cur
	if a.segmented(c, "panel-mode", &sel, items, segOpts{small: true, fill: true, width: PanelWidth - 2*panelPad - 8}) {
		a.SetMode(modeNames[sel])
	}
}

// quick is a way to switch the main exit from the panel.
type quick struct {
	key, title string
	region     string
	icon       string
	dest       routing.Dest
	selected   bool
	delay      int
}

// quickChoices are the few ways the panel offers to change the main exit:
// automatic, the choice in force, the nodes used last and a region. They are
// what a person switches between, so none of them needs a search.
func (a *App) quickChoices(limit int) []quick {
	main := a.rt().Main
	out := []quick{{key: "auto", title: a.t("nodes.auto"), icon: "zap", dest: routing.Auto(), selected: main.Kind == routing.DestAuto}}
	seen := map[string]bool{}
	node := func(id routing.NodeID, selected bool) {
		n, m := a.nodeFor(id)
		if m == routing.MatchNone || seen[n.Key] {
			return
		}
		seen[n.Key] = true
		out = append(out, quick{key: "n-" + n.Key, title: n.Label(), region: n.Region, dest: routing.ToNode(n.ID), selected: selected, delay: n.Delay})
	}
	region := func(code string, selected bool) {
		out = append(out, quick{key: "r-" + code, title: a.t("nodes.fastest_in", "name", a.regionName(code)), region: code, dest: routing.ToRegion(code), selected: selected})
	}
	// The region in force, or else the one with the most nodes.
	code := ""
	if main.Kind == routing.DestRegion {
		code = main.Region
	} else if rs := regionsWithNodes(a.liveNodes(), ""); len(rs) > 0 {
		code = rs[0].Code
	}
	if code != "" {
		region(code, main.Kind == routing.DestRegion)
	}
	if main.Kind == routing.DestNode && main.Node != nil {
		node(*main.Node, true)
	}
	for _, id := range a.rt().Recent {
		if len(out) >= limit {
			break
		}
		node(id, false)
	}
	return out
}

// panelNodes is the quick switch of the main exit; one click moves traffic.
func (a *App) panelNodes(c *ui.Context) {
	p := a.pl
	choices := a.quickChoices(4)
	busy := a.snap.Busy.Testing[configgen.TagProxy] || a.busy("test-all")
	ui.Column(c).Gap(2).Children(func() {
		ui.Row(c).Height(24).AlignItems(ui.Center).Padding(0, 2).Children(func() {
			a.eyebrow(c, a.t("panel.switch")).Grow(1)
			if a.btn(c, btnSpec{kind: btnGhost, label: a.t("nodes.test_all"), icon: "zap", small: true, busy: busy}).Clicked() {
				a.testAll()
			}
		})
		if len(choices) == 0 {
			a.txt(c, a.t("panel.switch.none"), 12.5, 400, p.Text3).Padding(6, 4)
			return
		}
		for _, q := range choices {
			a.panelQuickRow(c, q)
		}
	})
}

func (a *App) panelQuickRow(c *ui.Context, q quick) {
	p := a.pl
	b := ui.ButtonBase(c.Key("panel-q-" + q.key))
	b.Label(q.title).Height(38).Padding(0, 10).Gap(10).Radius(12).AlignItems(ui.Center).Cursor(ui.CursorPointer)
	h := b.AnimateTo("h", boolf(b.Hovered() && !q.selected), ui.Animation{Duration: 140 * time.Millisecond})
	if q.selected {
		b.Background(p.Blue.Alpha(0.14)).Border(1, p.Blue.Alpha(0.32))
	} else {
		b.Background(p.PanelHi.Alpha(0.9*h)).Border(1, ui.Transparent)
	}
	b.Children(func() {
		if q.region != "" {
			a.regionBadge(c, q.region, 24)
		} else {
			a.iconTile(c, q.icon, toneBlue, 24)
		}
		a.txt(c, q.title, 13, boolOr(q.selected, 650, 560), p.Text).SingleLine().Grow(1)
		if q.delay != 0 {
			a.number(c, boolOr(q.delay > 0, fmtMillis(q.delay), a.t("nodes.timeout")), 12.5, a.toneColor(latencyTone(q.delay)))
			a.signal(c, q.delay)
		}
		if q.selected && q.delay == 0 {
			ui.Icon(c, icon("check")).Size(15, 15).TextColor(p.BlueText)
		}
	})
	if b.Clicked() && !q.selected {
		a.setMain(q.dest)
	}
}

// ------------------------------------------------------------------ idle

// panelIdle is what the panel offers while there is no connection: the one
// button that matters, and what is worth knowing before pressing it.
func (a *App) panelIdle(c *ui.Context) {
	ph := a.snap.Phase
	empty := len(a.subs) == 0 && a.nodeCount() == 0
	ui.Column(c).Padding(4, panelPad, 0, panelPad).Gap(12).Children(func() {
		ui.Row(c).Children(func() {
			busy := a.busy("connect") || ph.Transitional() || a.busy("authorize")
			spec := btnSpec{kind: btnPrimary, label: a.panelActionLabel(), icon: a.panelActionIcon(), busy: busy, disabled: busy, grow: true}
			if a.btn(c, spec).Clicked() {
				a.toggleConnection()
			}
		})
		if empty {
			a.panelWelcome(c)
			return
		}
		a.panelFacts(c)
		a.panelUsage(c)
	})
}

// panelFacts is the four numbers that describe the setup.
func (a *App) panelFacts(c *ui.Context) {
	ui.Column(c.Key("panel-facts")).Padding(14).Radius(16).Gap(12).
		Background(a.pl.Panel.Alpha(0.9)).Border(1, a.pl.Line).Children(func() {
		ui.Row(c).Gap(10).Children(func() {
			ui.Column(c).Grow(1).Basis(0).Children(func() {
				a.heroFact(c, "globe", fmtCount(int64(a.nodeCount())), a.t("hero.fact.nodes"))
			})
			ui.Column(c).Grow(1).Basis(0).Children(func() {
				a.heroFact(c, "rss", fmtCount(int64(len(a.subs))), a.t("hero.fact.subs"))
			})
		})
		ui.Row(c).Gap(10).Children(func() {
			ui.Column(c).Grow(1).Basis(0).Children(func() {
				a.heroFact(c, "route", a.planName(a.rv().Plan), a.t("hero.fact.plan"))
			})
			ui.Column(c).Grow(1).Basis(0).Children(func() {
				a.heroFact(c, "clock", a.lastConnected(), a.t("hero.fact.last"))
			})
		})
	})
}

// panelUsage shows the quota that is closest to running out, which is the
// one worth a glance before connecting.
func (a *App) panelUsage(c *ui.Context) {
	subs := a.usageSubscriptions()
	if len(subs) == 0 {
		return
	}
	frac := func(s library.Subscription) float64 {
		if s.Usage == nil || s.Usage.Total <= 0 {
			return 0
		}
		return float64(s.Usage.Used()) / float64(s.Usage.Total)
	}
	best := subs[0]
	for _, s := range subs[1:] {
		if frac(s) > frac(best) {
			best = s
		}
	}
	ui.Column(c.Key("panel-usage")).Gap(2).Children(func() {
		ui.Row(c).Height(24).AlignItems(ui.Center).Padding(0, 2).Children(func() {
			a.eyebrow(c, a.t("panel.usage")).Grow(1)
			if len(subs) > 1 {
				a.txt(c, a.t("panel.usage.more", "n", strconv.Itoa(len(subs)-1)), 11.5, 500, a.pl.Text3).SingleLine()
			}
		})
		ui.Column(c).Padding(12, 14).Radius(16).Background(a.pl.Panel.Alpha(0.9)).Border(1, a.pl.Line).Children(func() {
			a.usageRow(c, "panel-"+best.ID, best.Name, best)
		})
	})
}

// panelWelcome says what the application does while there is nothing to
// connect to yet.
func (a *App) panelWelcome(c *ui.Context) {
	p := a.pl
	ui.Column(c.Key("panel-welcome")).Padding(10, 2, 0, 2).Gap(16).Children(func() {
		for _, f := range []struct{ icon, key string }{
			{"power", "welcome.f1"}, {"link", "welcome.f2"}, {"lock", "welcome.f3"},
		} {
			ui.Row(c).Gap(12).Children(func() {
				a.iconTile(c, f.icon, toneBlue, 34)
				ui.Column(c).Grow(1).Basis(0).Gap(2).Children(func() {
					a.txt(c, a.t(f.key+".title"), 13, 650, p.Text)
					a.txt(c, a.t(f.key+".body"), 12, 400, p.Text3).LineHeight(1.4)
				})
			})
		}
	})
}

func (a *App) panelActionLabel() string {
	switch {
	case a.snap.Phase == app.PhaseNoAgent && a.opt.Authorize != nil:
		return a.t("hero.authorize")
	case a.snap.Phase == app.PhaseNoAgent:
		return a.t("hero.recheck")
	case len(a.subs) == 0 && a.nodeCount() == 0:
		return a.t("subs.add")
	}
	return a.connectLabel()
}

func (a *App) panelActionIcon() string {
	switch {
	case a.snap.Phase == app.PhaseNoAgent && a.opt.Authorize != nil:
		return "shield-check"
	case a.snap.Phase == app.PhaseNoAgent:
		return "refresh"
	case len(a.subs) == 0 && a.nodeCount() == 0:
		return "plus"
	}
	return "power"
}

// ---------------------------------------------------------------- footer

func (a *App) panelFooter(c *ui.Context) {
	p := a.pl
	ui.Column(c).Children(func() {
		ui.Box(c).Height(1).Background(p.Line.Alpha(0.8))
		ui.Row(c).Height(48).Padding(0, 10).Gap(2).AlignItems(ui.Center).Children(func() {
			if a.btn(c, btnSpec{kind: btnGhost, label: a.t("tray.open", "app", brand.DisplayName), icon: "external", small: true}).Clicked() {
				a.reveal()
				a.closePanel()
			}
			ui.Spacer(c)
			if a.btn(c, btnSpec{kind: btnGhost, icon: "settings", small: true, iconOnly: true, tip: a.t("nav.settings")}).Clicked() {
				a.Navigate("settings")
				a.reveal()
				a.closePanel()
			}
			if a.opt.Quit != nil {
				if a.btn(c, btnSpec{kind: btnGhost, icon: "power", small: true, iconOnly: true, tip: a.t("tray.quit", "app", brand.DisplayName)}).Clicked() {
					a.opt.Quit()
				}
			}
		})
	})
}

func (a *App) closePanel() {
	if a.opt.ClosePanel != nil {
		a.opt.ClosePanel()
	}
}
