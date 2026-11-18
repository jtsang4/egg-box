package gui

import (
	"github.com/egoist/mygo/ui"

	"eggbox/internal/app"
	"eggbox/internal/brand"
)

// phoneDashboard is the home of a phone: the connection first, a thumb's
// reach from the button, then what flows through it, one card under another.
func (a *App) phoneDashboard(c *ui.Context) {
	title := brand.DisplayName
	a.phoneFrame(c, title, a.dashSubtitle(), func() { a.modeSwitch(c) }, func() {
		a.proxyBanners(c)
		a.phoneHero(c)
		a.statRow(c)
		a.exitCard(c)
		a.usageCard(c)
		a.activityCard(c)
	}, true)
}

// phoneHero is the hero standing up: the orb, what it says, the button, and
// the live rates and chart while connected.
func (a *App) phoneHero(c *ui.Context) {
	p := a.pl
	ph := a.snap.Phase
	h := ui.Column(c.Key("hero")).Radius(24).Clip()
	a.heroBackground(h, ph)
	h.Draw(func(pt *ui.Painter, r ui.Rect) {
		pt.FillGradient(ui.Rect{X: r.X, Y: r.Y, W: r.W, H: 120}, ui.LinearGradient{
			From: ui.RGBA(255, 255, 255, boolOr[float32](p.Dark, 0.035, 0.5)), To: ui.RGBA(255, 255, 255, 0), Angle: 180}, 0)
	})
	h.Children(func() {
		ui.Column(c).Padding(22, 18, 18, 18).Gap(8).AlignItems(ui.Center).Children(func() {
			a.orb(c, 176)
			ui.Box(c).Height(4)
			a.phoneHeroText(c)
		})
		if ph.Online() {
			a.phoneSpeeds(c)
			ui.Column(c.Key("hero-chart")).Height(84).Children(func() {
				a.trafficChart(c, "hero", a.snap.Traffic.History, 84)
			})
			return
		}
		a.phoneFacts(c)
	})
}

// phoneHeroText is heroText centred under the orb, its buttons across.
func (a *App) phoneHeroText(c *ui.Context) {
	p := a.pl
	ph := a.snap.Phase
	t, label := a.phaseTone(ph)
	center := func(e ui.Element) ui.Element { return e.TextAlign(ui.Center) }
	switch {
	case ph == app.PhaseNoAgent:
		a.eyebrowDot(c, a.t("hero.eyebrow.service"), toneWarn, false)
		center(a.txt(c, a.t("hero.noagent.title"), 22, 700, p.Text).LetterSpacing(-0.5).LineHeight(1.15))
		center(a.txt(c, a.agentProblem(), 13.5, 400, p.Text2).LineHeight(1.5))
		a.phoneButtons(c, func() {
			if a.btn(c, btnSpec{kind: btnSecondary, label: a.t("hero.recheck"), icon: "refresh"}).Grow(1).Clicked() {
				a.invalidate()
				a.toastInfo(a.t("hero.rechecking"))
			}
		})
	case len(a.subs) == 0 && a.nodeCount() == 0:
		a.eyebrowDot(c, a.t("hero.eyebrow.welcome"), toneBlue, false)
		center(a.txt(c, a.t("hero.empty.title"), 22, 700, p.Text).LetterSpacing(-0.5).LineHeight(1.15))
		center(a.txt(c, a.t("hero.empty.body"), 13.5, 400, p.Text2).LineHeight(1.5))
		// One above the other: side by side, their words do not fit.
		ui.Column(c).Gap(10).Padding(8, 8, 0, 8).AlignSelf(ui.Stretch).Children(func() {
			if a.btn(c, btnSpec{kind: btnPrimary, label: a.t("subs.add"), icon: "plus"}).Clicked() {
				a.openAddSubscription()
			}
			if a.btn(c, btnSpec{kind: btnSecondary, label: a.t("subs.import"), icon: "file-text"}).Clicked() {
				a.openImport()
			}
		})
	default:
		eb := a.t("hero.eyebrow.off")
		switch ph {
		case app.PhaseConnected:
			eb = a.t("hero.eyebrow.on")
		case app.PhaseConnecting, app.PhaseReconnecting, app.PhaseDisconnecting:
			eb = a.t("hero.eyebrow.working")
		case app.PhaseFailed:
			eb = a.t("hero.eyebrow.failed")
		}
		a.eyebrowDot(c, eb, t, ph == app.PhaseConnected)
		ui.Text(c, label).FontSize(28).FontWeight(700).TextColor(p.Text).LetterSpacing(-0.7).SingleLine()
		ui.Column(c).AlignItems(ui.Center).Children(func() { a.heroSubline(c) })
		a.phoneButtons(c, func() { a.heroButtons(c) })
	}
}

// phoneButtons lays the hero's buttons across its width, the first one, the
// main action, the widest.
func (a *App) phoneButtons(c *ui.Context, buttons func()) {
	ui.Row(c).Gap(10).Padding(8, 0, 0, 0).AlignSelf(ui.Stretch).Justify(ui.Center).Children(buttons)
}

// phoneSpeeds are the live rates, side by side.
func (a *App) phoneSpeeds(c *ui.Context) {
	p := a.pl
	tr := a.snap.Traffic
	ui.Row(c).Padding(4, 22, 6, 22).Gap(16).Children(func() {
		for _, it := range []struct {
			icon  string
			key   string
			val   float64
			color ui.Color
		}{
			{"arrow-down", "dash.down", tr.Down, p.BlueText},
			{"arrow-up", "dash.up", tr.Up, p.Indigo},
		} {
			num, unit := splitRate(it.val)
			ui.Column(c).Grow(1).Basis(0).Gap(4).Children(func() {
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, icon(it.icon)).Size(13, 13).TextColor(it.color)
					a.eyebrow(c, a.t(it.key))
				})
				ui.Row(c).Gap(5).AlignItems(ui.Baseline).Children(func() {
					a.number(c, num, 26, p.Text)
					a.txt(c, unit, 12.5, 500, p.Text3).SingleLine()
				})
			})
		}
	})
}

// phoneFacts are the hero's facts two by two while not connected.
func (a *App) phoneFacts(c *ui.Context) {
	ui.Box(c).Height(1).Margin(4, 18, 14, 18).Background(a.pl.Line)
	facts := [][3]string{
		{"globe", fmtCount(int64(a.nodeCount())), a.t("hero.fact.nodes")},
		{"rss", fmtCount(int64(len(a.subs))), a.t("hero.fact.subs")},
		{"route", a.planName(a.rv().Plan), a.t("hero.fact.plan")},
		{"clock", a.lastConnected(), a.t("hero.fact.last")},
	}
	ui.Column(c).Padding(0, 18, 18, 18).Gap(14).Children(func() {
		for i := 0; i < len(facts); i += 2 {
			ui.Row(c).Gap(12).Children(func() {
				for _, f := range facts[i : i+2] {
					ui.Row(c).Grow(1).Basis(0).Children(func() { a.heroFact(c, f[0], f[1], f[2]) })
				}
			})
		}
	})
}
