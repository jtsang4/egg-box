package gui

import (
	"context"
	"strconv"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/jtsang4/sing-box-ui/internal/app"
	"github.com/jtsang4/sing-box-ui/internal/configgen"
)

// dashboardPage is the home of the application: the connection and what
// flows through it.
func (a *App) dashboardPage(c *ui.Context) {
	a.pageFrame(c, a.t("dash.title"), a.dashSubtitle(), func() { a.modeSwitch(c) }, func() {
		a.hero(c)
		a.statRow(c)
		ui.Row(c).Gap(18).AlignItems(ui.Stretch).Children(func() {
			a.exitCard(c)
			a.usageCard(c)
		})
		a.activityCard(c)
	})
}

func (a *App) dashSubtitle() string {
	n := a.nodeCount()
	switch {
	case a.snap.Phase == app.PhaseNoAgent:
		return a.t("dash.sub.noagent")
	case len(a.subs) == 0:
		return a.t("dash.sub.empty")
	}
	return a.tn("dash.sub.ready", n, "subs", strconv.Itoa(len(a.subs)))
}

// modeNames are the routing modes in the order the switch shows them.
var modeNames = []string{configgen.ModeRule, configgen.ModeGlobal, configgen.ModeDirect}

// modeSwitch is the routing mode control.
func (a *App) modeSwitch(c *ui.Context) {
	online := a.snap.Phase.Online()
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
	wrap := ui.Row(c.Key("mode-wrap")).Gap(0)
	wrap.Disabled(!online)
	wrap.Children(func() {
		if a.segmented(c, "mode", &sel, items, segOpts{}) && online {
			mode := modeNames[sel]
			a.job("mode", func(ctx context.Context) error { return a.be.SetMode(ctx, mode) }, nil)
		}
	})
	if !online {
		wrap.Tooltip(a.t("mode.offline"))
	}
}

// ------------------------------------------------------------------- hero

func (a *App) heroBackground(h ui.Element, ph app.Phase) {
	p := a.pl
	on := h.AnimateTo("on", boolf(ph == app.PhaseConnected), ui.Animation{Duration: 900 * time.Millisecond, Ease: ui.EaseOut})
	var a0, a1, b0, b1 ui.Color
	if p.Dark {
		a0, a1 = ui.Hex("#0D1530"), ui.Hex("#090F22")
		b0, b1 = ui.Hex("#123068"), ui.Hex("#0A1A44")
	} else {
		a0, a1 = ui.Hex("#FFFFFF"), ui.Hex("#F3F7FF")
		b0, b1 = ui.Hex("#E2EDFF"), ui.Hex("#F4F8FF")
	}
	h.LinearGradient(ui.LinearGradient{From: a0.Mix(b0, on), To: a1.Mix(b1, on), Angle: 155, Oklab: true})
	h.Border(1, p.Line.Mix(p.Blue.Alpha(0.35), on))
	if p.Dark {
		h.InsetShadow(0, 1, 0, 0, ui.RGBA(255, 255, 255, 0.07))
		h.Shadow(0, 28, 60, -24, p.Glow.Alpha(0.15+0.5*on))
	} else {
		h.Shadow(0, 1, 2, 0, ui.RGBA(16, 40, 96, 0.06))
		h.Shadow(0, 24, 50, -22, p.Glow.Alpha(0.25+0.4*on))
	}
}

func (a *App) hero(c *ui.Context) {
	p := a.pl
	ph := a.snap.Phase
	h := ui.Column(c.Key("hero")).Radius(26).Clip()
	a.heroBackground(h, ph)
	// Soft sheen across the top of the card.
	h.Draw(func(pt *ui.Painter, r ui.Rect) {
		pt.FillGradient(ui.Rect{X: r.X, Y: r.Y, W: r.W, H: 120}, ui.LinearGradient{
			From: ui.RGBA(255, 255, 255, boolOr[float32](p.Dark, 0.035, 0.5)), To: ui.RGBA(255, 255, 255, 0), Angle: 180}, 0)
	})
	h.Children(func() {
		ui.Row(c).Padding(30, 32, 18, 26).Gap(28).AlignItems(ui.Center).Children(func() {
			a.orb(c, 232)
			ui.Column(c).Grow(1).Basis(0).Gap(10).Children(func() { a.heroText(c) })
			if ph.Online() {
				a.vr(c, 150)
				a.heroSpeeds(c)
			}
		})
		a.heroFoot(c)
	})
}

func (a *App) heroText(c *ui.Context) {
	p := a.pl
	ph := a.snap.Phase
	t, label := a.phaseTone(ph)
	switch {
	case ph == app.PhaseNoAgent:
		a.eyebrowDot(c, a.t("hero.eyebrow.service"), toneWarn, false)
		a.txt(c, a.t("hero.noagent.title"), 30, 700, p.Text).LetterSpacing(-0.7).LineHeight(1.1)
		a.txt(c, a.agentProblem(), 13.5, 400, p.Text2).LineHeight(1.5)
		ui.Row(c).Gap(10).Padding(6, 0, 0, 0).Children(func() {
			if a.opt.Authorize != nil {
				if a.btn(c, btnSpec{kind: btnPrimary, label: a.t("hero.authorize"), icon: "shield-check", busy: a.busy("authorize")}).Clicked() {
					a.authorize()
				}
			}
			if a.btn(c, btnSpec{kind: btnSecondary, label: a.t("hero.recheck"), icon: "refresh"}).Clicked() {
				a.invalidate()
				a.toastInfo(a.t("hero.rechecking"))
			}
		})
	case len(a.subs) == 0 && a.nodeCount() == 0:
		a.eyebrowDot(c, a.t("hero.eyebrow.welcome"), toneBlue, false)
		a.txt(c, a.t("hero.empty.title"), 30, 700, p.Text).LetterSpacing(-0.7).LineHeight(1.1)
		a.txt(c, a.t("hero.empty.body"), 13.5, 400, p.Text2).LineHeight(1.5)
		ui.Row(c).Gap(10).Padding(6, 0, 0, 0).Children(func() {
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
		ui.Text(c, label).FontSize(34).FontWeight(700).TextColor(p.Text).LetterSpacing(-0.9).SingleLine()
		a.heroSubline(c)
		ui.Row(c).Gap(10).Padding(8, 0, 0, 0).AlignItems(ui.Center).Children(func() {
			a.heroButtons(c)
		})
	}
}

// eyebrowDot is the small status line above the hero title.
func (a *App) eyebrowDot(c *ui.Context, s string, t tone, glow bool) {
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		a.dot(c, t, 7, glow)
		a.eyebrow(c, s)
	})
}

// agentProblem explains why the service cannot be used.
func (a *App) agentProblem() string {
	if e := a.snap.Link.Error; e != "" {
		return a.t("hero.noagent.body") + " " + e
	}
	return a.t("hero.noagent.body")
}

// heroSubline says where traffic goes (or what went wrong).
func (a *App) heroSubline(c *ui.Context) {
	p := a.pl
	ph := a.snap.Phase
	switch ph {
	case app.PhaseConnected, app.PhaseReconnecting:
		it, via, ok := a.exitNode()
		ui.Row(c).Gap(8).AlignItems(ui.Center).Wrap().Children(func() {
			if ok {
				a.regionBadge(c, it.Node.Region, 22)
				a.txt(c, it.Node.Name, 15, 600, p.Text).SingleLine()
				a.chip(c, it.Node.Protocol, toneBlue, "")
				if via != "" {
					a.chip(c, a.viaLabel(via), toneNeutral, "zap")
				}
			} else {
				a.txt(c, a.t("hero.sub.noexit"), 14, 400, p.Text2)
			}
		})
	case app.PhaseFailed:
		msg := a.t("phase.failed.hint")
		if st := a.snap.Status; st != nil && st.GetLastError() != "" {
			msg = st.GetLastError()
		}
		a.txt(c, truncate(msg, 160), 13.5, 400, p.Bad).LineHeight(1.45)
	default:
		a.txt(c, a.phaseHint(ph), 14, 400, p.Text2).LineHeight(1.45)
	}
}

// viaLabel names the group that picked the node.
func (a *App) viaLabel(tag string) string {
	switch tag {
	case configgen.TagAuto:
		return a.t("group.auto")
	}
	if len(tag) > len(configgen.RegionPrefix) && tag[:len(configgen.RegionPrefix)] == configgen.RegionPrefix {
		return a.t("group.region", "code", tag[len(configgen.RegionPrefix):])
	}
	return tag
}

func (a *App) heroButtons(c *ui.Context) {
	ph := a.snap.Phase
	busy := a.busy("connect") || ph.Transitional()
	switch ph {
	case app.PhaseConnected, app.PhaseReconnecting:
		if a.btn(c, btnSpec{kind: btnSecondary, label: a.t("hero.disconnect"), icon: "power", busy: a.busy("connect"), disabled: ph == app.PhaseDisconnecting}).Clicked() {
			a.toggleConnection()
		}
		if a.btn(c, btnSpec{kind: btnGhost, label: a.t("hero.choose"), icon: "globe"}).Clicked() {
			a.goTo(pageNodes)
		}
	default:
		if a.btn(c, btnSpec{kind: btnPrimary, label: a.connectLabel(), icon: "power", busy: busy, disabled: busy}).Clicked() {
			a.toggleConnection()
		}
		if a.btn(c, btnSpec{kind: btnGhost, label: a.t("hero.choose"), icon: "globe"}).Clicked() {
			a.goTo(pageNodes)
		}
	}
}

func (a *App) connectLabel() string {
	if a.snap.Phase == app.PhaseFailed {
		return a.t("hero.retry")
	}
	return a.t("hero.connect")
}

// heroSpeeds are the live rates on the right of the hero.
func (a *App) heroSpeeds(c *ui.Context) {
	p := a.pl
	tr := a.snap.Traffic
	ui.Column(c).Width(176).Gap(18).Children(func() {
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
			ui.Column(c).Gap(5).Children(func() {
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, icon(it.icon)).Size(14, 14).TextColor(it.color)
					a.eyebrow(c, a.t(it.key))
				})
				ui.Row(c).Gap(5).AlignItems(ui.Baseline).Children(func() {
					a.number(c, num, 32, p.Text).ContentTransition(rolling)
					a.txt(c, unit, 13, 500, p.Text3).SingleLine()
				})
			})
		}
	})
}

// heroFoot is the strip under the hero: the live chart while online, a few
// facts otherwise.
func (a *App) heroFoot(c *ui.Context) {
	p := a.pl
	if a.snap.Phase.Online() {
		ui.Column(c.Key("hero-chart")).Height(112).Children(func() {
			a.trafficChart(c, "hero", a.snap.Traffic.History, 112)
		})
		return
	}
	ui.Row(c).Padding(0, 32, 22, 32).Gap(12).AlignItems(ui.Center).Children(func() {
		a.hr(c).Grow(0)
	})
	_ = p
	ui.Row(c).Padding(0, 32, 24, 32).Gap(28).Children(func() {
		a.heroFact(c, "globe", fmtCount(int64(a.nodeCount())), a.t("hero.fact.nodes"))
		a.heroFact(c, "rss", fmtCount(int64(len(a.subs))), a.t("hero.fact.subs"))
		a.heroFact(c, "route", a.presetName(a.prefs.Preset), a.t("hero.fact.preset"))
		a.heroFact(c, "clock", a.lastConnected(), a.t("hero.fact.last"))
	})
}

func (a *App) heroFact(c *ui.Context, iconName, value, label string) {
	p := a.pl
	ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
		a.iconTile(c, iconName, toneNeutral, 32)
		ui.Column(c).Gap(1).Children(func() {
			a.txt(c, value, 14, 650, p.Text).SingleLine().FontFeatures("tnum")
			a.caption(c, label)
		})
	})
}

func (a *App) lastConnected() string {
	if !a.snap.Applied.At.IsZero() {
		return a.ago(a.snap.Applied.At)
	}
	return a.t("time.never")
}

func (a *App) presetName(preset configgen.Preset) string {
	switch preset {
	case configgen.PresetProxyAll:
		return a.t("preset.proxyall")
	case configgen.PresetForeignOnly:
		return a.t("preset.foreign")
	}
	return a.t("preset.china")
}

// ----------------------------------------------------------------- stats

func (a *App) statRow(c *ui.Context) {
	tr := a.snap.Traffic
	online := a.snap.Phase.Online() && tr.Available
	ui.Row(c).Gap(18).AlignItems(ui.Stretch).Children(func() {
		dn, du := splitBytes(float64(tr.DownTotal))
		un, uu := splitBytes(float64(tr.UpTotal))
		a.statTile(c, "arrow-down", toneBlue, a.t("stat.downloaded"), valOr(online, dn), unitOr(online, du))
		a.statTile(c, "arrow-up", toneBlue, a.t("stat.uploaded"), valOr(online, un), unitOr(online, uu))
		a.statTile(c, "swap", toneNeutral, a.t("stat.connections"), valOr(online, fmtCount(int64(tr.Connections))), "")
		lat, lt := "—", toneNeutral
		if it, _, ok := a.exitNode(); ok && online && it.DelayMS > 0 {
			lat, lt = strconv.Itoa(it.DelayMS), latencyTone(it.DelayMS)
		}
		a.statTile(c, "zap", lt, a.t("stat.latency"), lat, boolOr(lat != "—", "ms", ""))
	})
}

func valOr(ok bool, v string) string {
	if ok {
		return v
	}
	return "—"
}

func unitOr(ok bool, u string) string {
	if ok {
		return u
	}
	return ""
}

func (a *App) statTile(c *ui.Context, iconName string, t tone, label, value, unit string) {
	p := a.pl
	a.card(c).Grow(1).Basis(0).Padding(18, 18, 18, 18).Gap(14).Children(func() {
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			a.iconTile(c, iconName, t, 30)
			a.txt(c, label, 12.5, 550, p.Text2).SingleLine()
		})
		ui.Row(c).Gap(5).AlignItems(ui.Baseline).Children(func() {
			col := p.Text
			if value == "—" {
				col = p.Text3
			}
			a.number(c, value, 28, col).ContentTransition(rolling)
			if unit != "" {
				a.txt(c, unit, 13, 500, p.Text3).SingleLine()
			}
		})
	})
}

// -------------------------------------------------------------- exit node

func (a *App) exitCard(c *ui.Context) {
	p := a.pl
	a.card(c).Grow(1.25).Basis(0).Gap(16).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
			a.heading(c, a.t("dash.exit")).Grow(1)
			if a.btn(c, btnSpec{kind: btnGhost, label: a.t("dash.change"), icon: "chevron-right", small: true}).Clicked() {
				a.goTo(pageNodes)
			}
		})
		it, via, ok := a.exitNode()
		switch {
		case ok && a.snap.Phase.Online():
			a.exitRow(c, it.Node.Region, it.Node.Name, it.Node.Protocol+"  ·  "+it.Node.Transport, it.DelayMS, via)
		default:
			if n, pinned := a.selectedEntry(); pinned {
				a.exitRow(c, n.Region, n.Name, n.Protocol+"  ·  "+n.Transport, n.Delay, "")
			} else {
				ui.Row(c).Gap(14).AlignItems(ui.Center).Children(func() {
					a.iconTile(c, "zap", toneBlue, 46)
					ui.Column(c).Gap(3).Children(func() {
						a.txt(c, a.t("dash.exit.auto"), 15, 650, p.Text)
						a.caption(c, a.t("dash.exit.auto.hint"))
					})
				})
			}
		}
		if g, found := a.group(configgen.TagProxy); found && a.snap.Phase.Online() {
			a.hr(c)
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				a.caption(c, a.tn("dash.exit.available", len(g.Items)))
				ui.Spacer(c)
				busy := a.snap.Busy.Testing[configgen.TagProxy] || a.busy("test-all")
				if a.btn(c, btnSpec{kind: btnSoft, label: a.t("nodes.test_all"), icon: "zap", small: true, busy: busy}).Clicked() {
					a.testAll()
				}
			})
		}
	})
}

func (a *App) exitRow(c *ui.Context, region, name, sub string, delay int, via string) {
	p := a.pl
	ui.Row(c).Gap(14).AlignItems(ui.Center).Children(func() {
		a.regionBadge(c, region, 46)
		ui.Column(c).Grow(1).Gap(4).Children(func() {
			a.txt(c, name, 15.5, 650, p.Text).SingleLine()
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				a.caption(c, sub)
				if via != "" {
					a.chip(c, a.viaLabel(via), toneNeutral, "zap")
				}
			})
		})
		if delay != 0 {
			ui.Column(c).Gap(4).AlignItems(ui.End).Children(func() {
				if delay > 0 {
					a.number(c, fmtMillis(delay), 15, a.toneColor(latencyTone(delay)))
				} else {
					a.txt(c, a.t("nodes.timeout"), 13, 600, p.Bad)
				}
				a.signal(c, delay)
			})
		}
	})
}

// ------------------------------------------------------------------ usage

func (a *App) usageCard(c *ui.Context) {
	p := a.pl
	subs := a.usageSubscriptions()
	a.card(c).Grow(1).Basis(0).Gap(14).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			a.heading(c, a.t("dash.usage")).Grow(1)
			if len(a.subs) > 0 {
				busy := a.busy("refresh-all") || len(a.snap.Busy.Refreshing) > 0
				if a.btn(c, btnSpec{kind: btnGhost, icon: "refresh", small: true, iconOnly: true, busy: busy, tip: a.t("subs.refresh_all")}).Clicked() {
					a.refreshAll()
				}
			}
		})
		if len(subs) == 0 {
			ui.Column(c).Gap(6).Padding(8, 0).Children(func() {
				a.txt(c, a.t("dash.usage.none"), 13, 400, p.Text2).LineHeight(1.5)
			})
			return
		}
		shown := subs
		if len(shown) > 3 {
			shown = shown[:3]
		}
		for i, s := range shown {
			if i > 0 {
				a.hr(c)
			}
			a.usageRow(c, s.ID, s.Name, s)
		}
	})
}
