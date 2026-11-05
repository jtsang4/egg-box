package gui

import (
	"context"
	"strconv"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/jtsang4/sing-box-ui/internal/app"
	"github.com/jtsang4/sing-box-ui/internal/configgen"
)

type rulesState struct{}

// presets are the routing presets in the order the page shows them.
var presets = []struct {
	id   configgen.Preset
	icon string
	key  string
}{
	{configgen.PresetChina, "shield-check", "china"},
	{configgen.PresetProxyAll, "globe", "proxyall"},
	{configgen.PresetForeignOnly, "route", "foreign"},
}

// rulesPage decides where traffic goes: the preset, its options, and the
// user's own rules.
func (a *App) rulesPage(c *ui.Context) {
	rules := a.be.Rules()
	sub := a.t("rules.sub.none")
	if n := len(rules); n > 0 {
		sub = a.tn("rules.sub", n)
	}
	a.pageFrame(c, a.t("nav.rules"), sub, func() {
		if a.btn(c, btnSpec{kind: btnPrimary, label: a.t("rules.add"), icon: "plus"}).Clicked() {
			a.openRule(-1, configgen.CustomRule{Kind: configgen.KindDomainSuffix, Target: configgen.TargetProxy})
		}
	}, func() {
		a.presetRow(c)
		ui.Row(c).Gap(18).AlignItems(ui.Stretch).Children(func() {
			a.optionsCard(c)
			a.ruleDataCard(c)
		})
		a.customRules(c, rules)
	})
}

// savePrefs stores a change to the routing preferences.
func (a *App) savePrefs(p configgen.Prefs) {
	a.job("prefs", func(ctx context.Context) error {
		notes, err := a.be.SavePrefs(ctx, p)
		if err == nil && len(notes) > 0 {
			a.post(func() { a.toastInfo(a.t("prefs.adjusted")) })
		}
		return err
	}, func(err error) { a.fail(err) })
}

// ------------------------------------------------------------- presets

func (a *App) presetRow(c *ui.Context) {
	cur := a.prefs.Preset
	ui.Row(c.Key("presets")).Gap(14).Children(func() {
		for _, pr := range presets {
			a.presetCard(c, pr.id, pr.icon, pr.key, cur == pr.id)
		}
	})
}

func (a *App) presetCard(c *ui.Context, id configgen.Preset, iconName, key string, selected bool) {
	p := a.pl
	b := ui.ButtonBase(c.Key("preset-" + string(id)))
	b.Label(a.t("preset." + key))
	b.Grow(1).Basis(0).Padding(18, 18).Gap(14).Radius(radiusCard).AlignItems(ui.Start).Cursor(ui.CursorPointer)
	hov := b.AnimateTo("hov", boolf(b.Hovered()), ui.Animation{Duration: 150 * time.Millisecond})
	sel := b.AnimateTo("sel", boolf(selected), ui.Animation{Duration: 260 * time.Millisecond, Ease: ui.EaseOut})
	a.selectableFace(b, hov, sel)
	b.Children(func() {
		a.iconTile(c, iconName, boolOr(selected, toneBlue, toneNeutral), 42)
		ui.Column(c).Grow(1).Basis(0).Gap(5).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				a.txt(c, a.t("preset."+key), 14.5, 650, p.Text).SingleLine().Grow(1)
				if selected {
					a.checkBadge(c, 20)
				}
			})
			a.txt(c, a.t("preset."+key+".desc"), 12.5, 400, p.Text2).LineHeight(1.45)
		})
	})
	if b.Clicked() && !selected {
		pr := a.prefs
		pr.Preset = id
		a.savePrefs(pr)
	}
}

// -------------------------------------------------------------- options

func (a *App) optionsCard(c *ui.Context) {
	a.card(c).Grow(1).Basis(0).Padding(20, 20, 6, 20).Gap(0).Children(func() {
		ui.Row(c).Height(30).AlignItems(ui.Center).Margin(0, 0, 6, 0).Children(func() { a.heading(c, a.t("rules.options")) })
		a.prefToggle(c, "lan", "rules.lan", "rules.lan.desc", a.prefs.LANDirect, func(p *configgen.Prefs, v bool) { p.LANDirect = v })
		a.hr(c)
		a.prefToggle(c, "ads", "rules.ads", "rules.ads.desc", a.prefs.BlockAds, func(p *configgen.Prefs, v bool) { p.BlockAds = v })
		a.hr(c)
		a.prefToggle(c, "ipv6", "rules.ipv6", "rules.ipv6.desc", a.prefs.IPv6, func(p *configgen.Prefs, v bool) { p.IPv6 = v })
	})
}

// prefToggle is a settings row bound to a routing preference.
func (a *App) prefToggle(c *ui.Context, key, title, desc string, val bool, set func(*configgen.Prefs, bool)) {
	on := val
	if a.toggleRow(c, "pref-"+key, a.t(title), a.t(desc), &on, false) {
		pr := a.prefs
		set(&pr, on)
		a.savePrefs(pr)
	}
}

// ------------------------------------------------------------ rule data

func (a *App) ruleDataCard(c *ui.Context) {
	p := a.pl
	data := a.be.RuleDataStatus()
	need := 0
	for _, d := range data {
		if d.Needed {
			need++
		}
	}
	busy := a.snap.Busy.RuleData || a.busy("rule-data")
	a.card(c).Grow(1).Basis(0).Gap(12).Children(func() {
		ui.Row(c).Height(30).AlignItems(ui.Center).Gap(8).Children(func() {
			a.heading(c, a.t("rules.data")).Grow(1)
			if a.btn(c, btnSpec{kind: btnSoft, small: true, label: a.t("rules.data.update"), icon: "refresh", busy: busy}).Clicked() {
				a.job("rule-data", func(ctx context.Context) error { return a.be.UpdateRuleData(ctx, true) }, func(err error) { a.fail(err) })
			}
		})
		if need == 0 {
			a.txt(c, a.t("rules.data.none"), 12.5, 400, p.Text2).LineHeight(1.5)
			return
		}
		for i, d := range data {
			if !d.Needed {
				continue
			}
			if i > 0 {
				a.hr(c)
			}
			a.ruleDataRow(c, d)
		}
	})
}

func (a *App) ruleDataRow(c *ui.Context, d app.RuleData) {
	p := a.pl
	ui.Row(c.Key("rd-"+d.ID)).Gap(12).Padding(2, 0).AlignItems(ui.Center).Children(func() {
		t, ic := toneGood, "check-circle"
		if !d.Present {
			t, ic = toneWarn, "download"
		}
		a.iconTile(c, ic, t, 32)
		ui.Column(c).Grow(1).Basis(0).Gap(2).Children(func() {
			a.txt(c, a.t("ruleset."+d.ID), 13, 600, p.Text).SingleLine()
			sub := a.t("rules.data.missing")
			if d.Present {
				sub = a.t("rules.data.updated", "when", a.ago(d.Updated)) + "  ·  " + fmtBytes(float64(d.Size))
			}
			a.txt(c, sub, 11.5, 400, p.Text3).SingleLine()
		})
	})
}

// ---------------------------------------------------------- custom rules

func (a *App) customRules(c *ui.Context, rules []configgen.CustomRule) {
	a.card(c).Padding(0).Gap(0).Children(func() {
		ui.Row(c).Padding(18, 20, 12, 20).Gap(10).AlignItems(ui.Center).Children(func() {
			a.heading(c, a.t("rules.custom")).Grow(1)
			if len(rules) > 0 {
				a.caption(c, a.t("rules.order"))
			}
		})
		if len(rules) == 0 {
			a.hr(c)
			a.emptyState(c, "route", a.t("rules.empty.title"), a.t("rules.empty.body"), func() {
				if a.btn(c, btnSpec{kind: btnSecondary, label: a.t("rules.add"), icon: "plus"}).Clicked() {
					a.openRule(-1, configgen.CustomRule{Kind: configgen.KindDomainSuffix, Target: configgen.TargetProxy})
				}
			})
			return
		}
		for i, r := range rules {
			a.hr(c)
			a.ruleRow(c, i, r, len(rules))
		}
	})
}

func (a *App) ruleRow(c *ui.Context, i int, r configgen.CustomRule, n int) {
	p := a.pl
	row := ui.Row(c.Key("rule-"+strconv.Itoa(i)+"-"+string(r.Kind)+"-"+r.Value)).Padding(12, 14, 12, 20).Gap(14).AlignItems(ui.Center)
	row.PopoverMenu(a.ruleMenu(c, i, r, n))
	row.Children(func() {
		a.txt(c, strconv.Itoa(i+1), 12, 600, p.Text3).FontFeatures("tnum").Width(22).SingleLine()
		ui.Column(c).Grow(1).Basis(0).Gap(3).Children(func() {
			a.txt(c, r.Value, 13.5, 600, p.Text).Font("monospace").SingleLine()
			a.caption(c, a.t("rulekind."+string(r.Kind)))
		})
		ui.Icon(c, icon("arrow-right")).Size(16, 16).TextColor(p.Text3)
		a.targetChip(c, r.Target)
		a.btn(c, btnSpec{kind: btnGhost, icon: "more", iconOnly: true, small: true, tip: a.t("subs.more")}).PopoverMenuButton(a.ruleMenu(c, i, r, n))
	})
}

func (a *App) targetChip(c *ui.Context, target string) {
	switch target {
	case configgen.TargetDirect:
		a.chip(c, a.t("target.direct"), toneNeutral, "arrow-right")
	case configgen.TargetReject:
		a.chip(c, a.t("target.reject"), toneBad, "shield-off")
	default:
		a.chip(c, a.t("target.proxy"), toneBlue, "shield-check")
	}
}

func (a *App) ruleMenu(c *ui.Context, i int, r configgen.CustomRule, n int) func(m *ui.Menu) {
	return func(m *ui.Menu) {
		if m.Item(a.t("common.edit")).Chosen() {
			a.openRule(i, r)
		}
		if m.Item(a.t("rules.copy_value")).Chosen() {
			c.WriteClipboard(r.Value)
		}
		m.Separator()
		if m.Item(a.t("subs.move_up")).Disabled(i == 0).Chosen() {
			a.moveRule(i, i-1)
		}
		if m.Item(a.t("subs.move_down")).Disabled(i == n-1).Chosen() {
			a.moveRule(i, i+1)
		}
		m.Separator()
		if m.Item(a.t("common.delete")).Chosen() {
			a.mutateRules(func(rs []configgen.CustomRule) []configgen.CustomRule { return append(rs[:i:i], rs[i+1:]...) })
		}
	}
}

func (a *App) moveRule(from, to int) {
	a.mutateRules(func(rs []configgen.CustomRule) []configgen.CustomRule {
		r := rs[from]
		rs = append(rs[:from:from], rs[from+1:]...)
		out := append([]configgen.CustomRule{}, rs[:to]...)
		out = append(out, r)
		return append(out, rs[to:]...)
	})
}

// mutateRules changes the list of custom rules and stores it.
func (a *App) mutateRules(fn func([]configgen.CustomRule) []configgen.CustomRule) {
	rules := fn(append([]configgen.CustomRule(nil), a.be.Rules()...))
	a.job("rules", func(ctx context.Context) error { return a.be.SaveRules(ctx, rules) }, func(err error) { a.fail(err) })
}
