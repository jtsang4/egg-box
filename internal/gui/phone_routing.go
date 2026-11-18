package gui

import (
	"context"
	"strconv"

	"github.com/egoist/mygo/ui"

	"eggbox/internal/routing"
)

// The routing page on a phone: each row in two lines, what it is with its
// switch, then where it goes across the width. Rows move with their menu
// (Move up, Move down), which a long press opens: a finger held on a row
// cannot both drag it and open its menu.

// phonePlanBar is planBar standing up: the plan with its menu, then what is
// known about it and the button that updates its rules.
func (a *App) phonePlanBar(c *ui.Context) {
	p := a.pl
	v := a.rv()
	note, nt := a.planNote(v.Plan)
	busy := a.snap.Busy.RuleData || a.snap.Plan.Updating || a.busy("rule-data")
	a.card(c).Padding(14, 14).Gap(10).Children(func() {
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			a.iconTile(c, "layers", toneBlue, 34)
			a.txt(c, a.t("plan.current"), 11.5, 600, p.Text3).LetterSpacing(0.3).SingleLine().Grow(1).Basis(0)
			a.btn(c, btnSpec{kind: btnGhost, icon: "more", iconOnly: true, small: true, tip: a.t("subs.more")}).PopoverMenuButton(a.planMenu())
		})
		a.planButton(c)
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			col := a.toneColor(nt)
			if nt == toneNeutral {
				col = p.Text3
			}
			a.txt(c, note, 12.5, 400, col).MaxLines(2).Grow(1).Basis(0)
			if a.btn(c, btnSpec{kind: btnSoft, small: true, label: a.t("plan.update"), icon: "refresh", busy: busy}).Clicked() {
				a.job("rule-data", func(ctx context.Context) error { return a.be.UpdateRuleData(ctx, true) }, func(err error) { a.fail(err) })
			}
		})
	})
}

// phoneExceptionsHead is the head of the exceptions card: the fold, then its
// two actions under it.
func (a *App) phoneExceptionsActions(c *ui.Context) {
	ui.Row(c).Padding(0, 14, 12, 14).Gap(8).Justify(ui.End).AlignItems(ui.Center).Children(func() {
		if a.btn(c, btnSpec{kind: btnGhost, small: true, label: a.t("exceptions.edit_text"), icon: "file-text"}).Clicked() {
			a.openExceptionsEditor()
		}
		if a.btn(c, btnSpec{kind: btnSoft, small: true, label: a.t("common.add"), icon: "plus"}).Clicked() {
			a.openException(nil)
			a.rte.exOpen = true
		}
	})
}

// phoneExceptionRow is exceptionRow in two lines.
func (a *App) phoneExceptionRow(c *ui.Context, i int, x routing.Exception, n int) {
	p := a.pl
	on := x.Enabled
	row := ui.Column(c.Key("ex-"+x.ID)).Padding(10, 14, 10, 14).Gap(8)
	row.PopoverMenu(a.exceptionMenu(c, i, x, n))
	row.Children(func() {
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			a.txt(c, strconv.Itoa(i+1), 12, 600, p.Text3).FontFeatures("tnum").Width(20).SingleLine().Shrink(0)
			ui.Column(c).Grow(1).Basis(0).Gap(3).Children(func() {
				a.txt(c, exceptionTitle(x.Entry()), 13.5, 600, boolOr(on, p.Text, p.Text3)).Font("monospace").SingleLine()
				a.caption(c, a.entryKindLabel(x.Kind)).SingleLine()
			})
			if a.toggle(c, "ex-on-"+x.ID, &on, exceptionTitle(x.Entry())) {
				id := x.ID
				a.editRouting(func(s *routing.State) error { return s.SetExceptionEnabled(id, on) }, nil)
			}
		})
		ui.Row(c).Gap(6).Padding(0, 0, 0, 30).AlignItems(ui.Center).Children(func() {
			a.destButton(c, destOpts{key: "ex-dest-" + x.ID, title: exceptionTitle(x.Entry())}, x.Dest, func(d routing.Dest) {
				id := x.ID
				a.editRouting(func(s *routing.State) error { return s.SetExceptionDest(id, d) }, nil)
			})
			a.btn(c, btnSpec{kind: btnGhost, icon: "more", iconOnly: true, small: true, tip: a.t("subs.more")}).PopoverMenuButton(a.exceptionMenu(c, i, x, n))
		})
	})
}

// phoneExceptionH is the height of a phone's exception row.
const phoneExceptionH = 98

// phoneItemRow is itemRow in two lines.
func (a *App) phoneItemRow(c *ui.Context, i int, row routing.Row, n int) {
	p := a.pl
	it := row.Item
	name := a.itemName(it)
	on := it.Enabled
	r := ui.Column(c.Key("it-"+it.ID)).Padding(10, 14, 12, 10).Gap(6)
	r.PopoverMenu(a.itemMenu(c, i, row, n))
	chips := a.itemChips(row)
	r.Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			open := ui.ButtonBase(c.Key("it-open-" + it.ID))
			open.Label(name)
			open.Grow(1).Basis(0).Padding(4, 4).Gap(12).Radius(12).AlignItems(ui.Center)
			open.Children(func() {
				a.iconTile(c, boolOr(it.Icon != "", it.Icon, "route"), boolOr(on, toneBlue, toneNeutral), 36)
				ui.Column(c).Grow(1).Basis(0).Gap(4).Children(func() {
					ui.Row(c).GapX(6).GapY(4).Wrap().AlignItems(ui.Center).Children(func() {
						a.txt(c, name, 14.5, 650, boolOr(on, p.Text, p.Text3)).SingleLine().Shrink(1)
						if row.Origin == routing.OriginOverlay {
							a.chip(c, a.t("item.yours"), toneNeutral, "").Shrink(0)
						}
						for _, ch := range chips {
							a.chip(c, ch.text, ch.tone, ch.icon).Shrink(0)
						}
					})
					a.txt(c, a.itemSummary(row), 12, 400, p.Text3).SingleLine()
				})
			})
			if open.Clicked() {
				a.openItem(it.ID)
			}
			if a.toggle(c, "it-on-"+it.ID, &on, name) {
				id := it.ID
				a.editRouting(func(s *routing.State) error { return s.SetItemEnabled(id, on) }, nil)
			}
		})
		ui.Row(c).Gap(6).Padding(0, 0, 0, 52).AlignItems(ui.Center).Children(func() {
			a.destButton(c, destOpts{key: "it-dest-" + it.ID, title: name}, it.Dest, func(d routing.Dest) {
				id := it.ID
				a.editRouting(func(s *routing.State) error { return s.SetItemDest(id, d) }, nil)
			})
			a.btn(c, btnSpec{kind: btnGhost, icon: "more", iconOnly: true, small: true, tip: a.t("subs.more")}).PopoverMenuButton(a.itemMenu(c, i, row, n))
		})
	})
}

// phoneFallbackRow is fallbackRow in two lines.
func (a *App) phoneFallbackRow(c *ui.Context) {
	p := a.pl
	v := a.rv()
	ui.Column(c.Key("fallback")).Padding(12, 14, 14, 14).Gap(8).Children(func() {
		ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
			a.iconTile(c, "layers", toneNeutral, 36)
			ui.Column(c).Grow(1).Basis(0).Gap(3).Children(func() {
				a.txt(c, a.t("fallback.title"), 14.5, 650, p.Text).SingleLine()
				a.txt(c, a.t("fallback.sub"), 12, 400, p.Text3).MaxLines(2)
			})
		})
		ui.Row(c).Gap(6).Padding(0, 0, 0, 48).AlignItems(ui.Center).Children(func() {
			a.destButton(c, destOpts{key: "fallback-dest", title: a.t("fallback.title")}, v.Fallback, func(d routing.Dest) {
				a.editRouting(func(s *routing.State) error { return s.SetFallback(d) }, nil)
			})
			if v.FallbackOverridden {
				if a.btn(c, btnSpec{kind: btnGhost, icon: "rotate", iconOnly: true, small: true, tip: a.t("fallback.restore")}).Clicked() {
					def := v.FallbackDefault.Clone()
					a.editRouting(func(s *routing.State) error { return s.SetFallback(def) }, nil)
				}
			}
		})
	})
}
