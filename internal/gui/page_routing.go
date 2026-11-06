package gui

import (
	"context"
	"strconv"
	"time"

	"github.com/egoist/mygo/ui"

	"eggbox/internal/routing"
)

// routingState is what the routing page remembers.
type routingState struct {
	// queue holds the changes waiting their turn (see editRouting).
	queue   []editJob
	running bool
	// exOpen is set while the exceptions are unfolded.
	exOpen bool
	exList ui.ListState
}

func (a *App) initRouting() {}

// routingPage is where a person decides where each kind of traffic goes: the
// plan in force, their own exceptions ahead of it, and the items of the plan.
func (a *App) routingPage(c *ui.Context) {
	v := a.rv()
	sub := a.tn("routing.sub", len(v.Rows), "plan", a.planName(v.Plan))
	a.pageFrame(c, a.t("nav.routing"), sub, func() {
		if a.btn(c, btnSpec{kind: btnSecondary, label: a.t("routing.test"), icon: "compass"}).Clicked() {
			a.openTestURL("")
		}
		if a.btn(c, btnSpec{kind: btnPrimary, label: a.t("routing.add"), icon: "plus"}).Clicked() {
			a.openAddItem()
		}
	}, func() {
		a.planBar(c)
		a.waitingBanner(c)
		a.exceptionsCard(c)
		a.itemsCard(c)
	})
}

// -------------------------------------------------------------- plan bar

// planBar names the plan in force, says where it comes from, and offers the
// other plans and what can be done with this one.
func (a *App) planBar(c *ui.Context) {
	p := a.pl
	v := a.rv()
	note, nt := a.planNote(v.Plan)
	busy := a.snap.Busy.RuleData || a.snap.Plan.Updating || a.busy("rule-data")
	a.card(c).Padding(14, 18).Children(func() {
		ui.Row(c).Gap(14).AlignItems(ui.Center).Children(func() {
			a.iconTile(c, "layers", toneBlue, 40)
			ui.Column(c).Grow(1).Basis(0).Gap(5).Children(func() {
				a.txt(c, a.t("plan.current"), 11.5, 600, p.Text3).LetterSpacing(0.3).SingleLine()
				ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
					a.planButton(c)
					col := a.toneColor(nt)
					if nt == toneNeutral {
						col = p.Text3
					}
					a.txt(c, note, 12.5, 400, col).SingleLine().Grow(1).Basis(0)
				})
			})
			if a.btn(c, btnSpec{kind: btnSoft, small: true, label: a.t("plan.update"), icon: "refresh", busy: busy}).Clicked() {
				a.job("rule-data", func(ctx context.Context) error { return a.be.UpdateRuleData(ctx, true) }, func(err error) { a.fail(err) })
			}
			a.btn(c, btnSpec{kind: btnGhost, icon: "more", iconOnly: true, small: true, tip: a.t("subs.more")}).PopoverMenuButton(a.planMenu())
		})
	})
}

// planButton is the plan in force, which opens the list of plans.
func (a *App) planButton(c *ui.Context) {
	p := a.pl
	v := a.rv()
	name := a.planName(v.Plan)
	b := ui.ButtonBase(c.Key("plan-switch"))
	b.Label(name)
	b.Height(36).Padding(0, 12).Gap(8).Radius(radiusControl).Border(1, p.Line).AlignItems(ui.Center).Cursor(ui.CursorPointer).MaxWidth(320).Shrink(0)
	hov := b.AnimateTo("hov", boolf(b.Hovered()), ui.Animation{Duration: 130 * time.Millisecond})
	b.Background(p.Field.Mix(p.PanelHi, hov))
	b.BorderColor(p.Line.Mix(p.LineHi, hov))
	b.Children(func() {
		a.txt(c, name, 14, 650, p.Text).SingleLine()
		ui.Icon(c, icon("chevron-down")).Size(15, 15).TextColor(p.Text3)
	})
	b.PopoverMenuButton(func(m *ui.Menu) {
		// The plans by who keeps them (the application, a subscription, the
		// person), then the way to a plan of one's own.
		var kind routing.PlanKind
		for i, pl := range a.allPlans() {
			if i > 0 && pl.Kind != kind {
				m.Separator()
			}
			kind = pl.Kind
			id := pl.ID
			if m.Item(a.planName(pl)).Radio(id == v.Plan.ID).Chosen() && id != v.Plan.ID {
				a.editRouting(func(s *routing.State) error { return s.Switch(id) }, nil)
			}
		}
		m.Separator()
		if m.Item(a.t("plan.new")).Chosen() {
			a.openNewPlan("")
		}
	})
}

// planMenu has what can be done with the plan in force: a plan kept up to date
// elsewhere can be made one's own or have its choices undone; one's own can
// be renamed, copied and deleted.
func (a *App) planMenu() func(m *ui.Menu) {
	return func(m *ui.Menu) {
		v := a.rv()
		if v.Plan.Kind != routing.PlanMine {
			if m.Item(a.t("plan.save_as")).Chosen() {
				a.openPlanName(false)
			}
			overridden := v.FallbackOverridden
			for _, r := range v.Rows {
				overridden = overridden || r.Overridden
			}
			if m.Item(a.t("plan.restore")).Disabled(!overridden).Chosen() {
				a.confirm(a.t("plan.restore.title"), a.t("plan.restore.body"), a.t("plan.restore"), false, func() {
					a.editRouting(func(s *routing.State) error { return s.RestoreDefault() }, nil)
				})
			}
			return
		}
		if m.Item(a.t("plan.rename")).Chosen() {
			a.openPlanName(true)
		}
		if m.Item(a.t("plan.duplicate")).Chosen() {
			a.openNewPlan(v.Plan.ID)
		}
		m.Separator()
		id, name := v.Plan.ID, v.Plan.Name
		if m.Item(a.t("plan.delete")).Chosen() {
			a.confirm(a.t("plan.delete.title", "name", name), a.t("plan.delete.body"), a.t("common.delete"), true, func() {
				a.editRouting(func(s *routing.State) error { return s.DeletePlan(id) }, nil)
			})
		}
	}
}

// waitingBanner tells that rule files are still to come, and offers to fetch
// them now.
func (a *App) waitingBanner(c *ui.Context) {
	p := a.pl
	pl := a.snap.Plan
	if !pl.Ready || pl.Missing == 0 {
		return
	}
	busy := a.snap.Busy.RuleData || pl.Updating || a.busy("rule-data")
	ui.Row(c).Gap(12).Padding(12, 16).Radius(radiusCard-4).Background(p.Warn.Alpha(0.1)).Border(1, p.Warn.Alpha(0.26)).AlignItems(ui.Center).Children(func() {
		if busy {
			a.spinner(c, 16, p.Warn)
		} else {
			ui.Icon(c, icon("download")).Size(16, 16).TextColor(p.Warn)
		}
		a.txt(c, boolOr(busy, a.tn("routing.waiting.busy", pl.Missing), a.tn("routing.waiting", pl.Missing)), 13, 500, p.Text).Grow(1).Basis(0).LineHeight(1.45)
		if !busy && a.btn(c, btnSpec{kind: btnSecondary, small: true, label: a.t("routing.waiting.go")}).Clicked() {
			a.job("rule-data", func(ctx context.Context) error { return a.be.UpdateRuleData(ctx, false) }, func(err error) { a.fail(err) })
		}
	})
}

// ------------------------------------------------------------ exceptions

const exceptionRowH = 52

// exceptionsCard lists the person's exceptions: global rules that come before
// every plan. It is folded while it is not being worked on.
func (a *App) exceptionsCard(c *ui.Context) {
	p := a.pl
	st := a.rt()
	n := len(st.Exceptions)
	open := a.rte.exOpen && n > 0
	a.card(c).Padding(0).Gap(0).Children(func() {
		ui.Row(c).Padding(10, 12, 10, 12).Gap(8).AlignItems(ui.Center).Children(func() {
			head := ui.ButtonBase(c.Key("ex-head"))
			head.Label(a.t("exceptions.title"))
			head.Grow(1).Basis(0).Height(44).Padding(0, 8).Gap(12).Radius(12).AlignItems(ui.Center).Cursor(boolOr(n > 0, ui.CursorPointer, ui.CursorDefault))
			hov := head.AnimateTo("hov", boolf(head.Hovered() && n > 0), ui.Animation{Duration: 130 * time.Millisecond})
			head.Background(p.PanelHi.Alpha(hov))
			head.Children(func() {
				a.iconTile(c, "pin", toneBlue, 34)
				ui.Column(c).Grow(1).Basis(0).Gap(2).Children(func() {
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						a.heading(c, a.t("exceptions.title"))
						if n > 0 {
							a.chip(c, strconv.Itoa(n), toneNeutral, "")
						}
					})
					a.txt(c, a.t("exceptions.sub"), 12, 400, p.Text3).SingleLine()
				})
				if n > 0 {
					ui.Icon(c, icon(boolOr(open, "chevron-up", "chevron-down"))).Size(16, 16).TextColor(p.Text3)
				}
			})
			if head.Clicked() && n > 0 {
				a.rte.exOpen = !a.rte.exOpen
			}
			if a.btn(c, btnSpec{kind: btnGhost, small: true, label: a.t("exceptions.paste"), icon: "copy"}).Clicked() {
				a.openBatchException("")
			}
			if a.btn(c, btnSpec{kind: btnSoft, small: true, label: a.t("common.add"), icon: "plus"}).Clicked() {
				a.openException(nil)
				a.rte.exOpen = true
			}
		})
		if n == 0 {
			a.hr(c)
			ui.Row(c).Padding(12, 20).Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, icon("info")).Size(15, 15).TextColor(p.Text3)
				a.txt(c, a.t("exceptions.empty"), 12.5, 400, p.Text2).Grow(1).Basis(0).LineHeight(1.45)
			})
			return
		}
		if !open {
			return
		}
		a.hr(c)
		list := ui.List(c.Key("ex-list"), &a.rte.exList, n, func(i int) {
			if i > 0 {
				a.hr(c)
			}
			a.exceptionRow(c, i, st.Exceptions[i], n)
		})
		list.Height(float32(min(n, 8)*exceptionRowH + min(n, 8) - 1)).Gap(0)
	})
}

func (a *App) exceptionRow(c *ui.Context, i int, x routing.Exception, n int) {
	p := a.pl
	on := x.Enabled
	row := ui.Row(c.Key("ex-"+x.ID)).Height(exceptionRowH).Padding(0, 12, 0, 20).Gap(12).AlignItems(ui.Center)
	row.PopoverMenu(a.exceptionMenu(c, i, x, n))
	row.Children(func() {
		a.txt(c, strconv.Itoa(i+1), 12, 600, p.Text3).FontFeatures("tnum").Width(22).SingleLine().Shrink(0)
		ui.Column(c).Grow(1).Basis(0).Gap(3).Children(func() {
			a.txt(c, exceptionTitle(x.Entry()), 13.5, 600, boolOr(on, p.Text, p.Text3)).Font("monospace").SingleLine()
			a.caption(c, a.entryKindLabel(x.Kind)).SingleLine()
		})
		a.destButton(c, destOpts{key: "ex-dest-" + x.ID, title: exceptionTitle(x.Entry())}, x.Dest, func(d routing.Dest) {
			id := x.ID
			a.editRouting(func(s *routing.State) error { return s.SetExceptionDest(id, d) }, nil)
		})
		if a.toggle(c, "ex-on-"+x.ID, &on, exceptionTitle(x.Entry())) {
			id := x.ID
			a.editRouting(func(s *routing.State) error { return s.SetExceptionEnabled(id, on) }, nil)
		}
		a.btn(c, btnSpec{kind: btnGhost, icon: "more", iconOnly: true, small: true, tip: a.t("subs.more")}).PopoverMenuButton(a.exceptionMenu(c, i, x, n))
	})
}

func (a *App) exceptionMenu(c *ui.Context, i int, x routing.Exception, n int) func(m *ui.Menu) {
	return func(m *ui.Menu) {
		id := x.ID
		if m.Item(a.t("common.edit")).Chosen() {
			a.openException(&x)
		}
		if m.Item(a.t("common.copy_value")).Chosen() {
			c.WriteClipboard(x.Value)
		}
		m.Separator()
		if m.Item(a.t("subs.move_up")).Disabled(i == 0).Chosen() {
			a.editRouting(func(s *routing.State) error { return s.MoveException(id, -1) }, nil)
		}
		if m.Item(a.t("subs.move_down")).Disabled(i == n-1).Chosen() {
			a.editRouting(func(s *routing.State) error { return s.MoveException(id, 1) }, nil)
		}
		m.Separator()
		if m.Item(a.t("common.delete")).Chosen() {
			a.editRouting(func(s *routing.State) error { return s.DeleteException(id) }, nil)
		}
	}
}

// ----------------------------------------------------------------- items

// itemsCard lists the items of the plan in force, in the order they are
// tried, and last of all what happens to everything else.
func (a *App) itemsCard(c *ui.Context) {
	p := a.pl
	v := a.rv()
	n := len(v.Rows)
	a.card(c).Padding(0).Gap(0).Children(func() {
		ui.Row(c).Padding(16, 20, 12, 20).Gap(10).AlignItems(ui.Center).Children(func() {
			a.heading(c, a.t("routing.items")).Grow(1)
			if a.snap.Plan.Updating {
				a.spinner(c, 14, p.BlueText)
				a.caption(c, a.t("routing.updating"))
			} else if n > 0 {
				a.caption(c, a.t("routing.order"))
			}
		})
		if n == 0 {
			a.hr(c)
			a.emptyState(c, "route", a.t("routing.empty.title"), a.t("routing.empty.body"), func() {
				if a.btn(c, btnSpec{kind: btnSecondary, label: a.t("routing.add"), icon: "plus"}).Clicked() {
					a.openAddItem()
				}
			})
		}
		for i, row := range v.Rows {
			a.hr(c)
			a.itemRow(c, i, row, n)
		}
		a.hr(c)
		a.fallbackRow(c)
	})
}

// itemRow is one item: its name and what it is made of, where it goes and
// whether it is on.
func (a *App) itemRow(c *ui.Context, i int, row routing.Row, n int) {
	p := a.pl
	it := row.Item
	name := a.itemName(it)
	on := it.Enabled
	r := ui.Row(c.Key("it-"+it.ID)).Padding(8, 14, 8, 12).Gap(10).AlignItems(ui.Center)
	r.PopoverMenu(a.itemMenu(c, i, row, n))
	chips := a.itemChips(row)
	r.Children(func() {
		open := ui.ButtonBase(c.Key("it-open-" + it.ID))
		open.Label(name)
		open.Grow(1).Basis(0).Padding(6, 8).Gap(12).Radius(12).AlignItems(ui.Center).Cursor(ui.CursorPointer)
		hov := open.AnimateTo("hov", boolf(open.Hovered()), ui.Animation{Duration: 130 * time.Millisecond})
		open.Background(p.PanelHi.Alpha(hov))
		open.Children(func() {
			a.iconTile(c, boolOr(it.Icon != "", it.Icon, "route"), boolOr(on, toneBlue, toneNeutral), 38)
			ui.Column(c).Grow(1).Basis(0).Gap(4).Children(func() {
				// A narrow window puts the chips on a line of their own rather
				// than cutting the name down to a few letters.
				ui.Row(c).GapX(8).GapY(4).Wrap().AlignItems(ui.Center).Children(func() {
					a.txt(c, name, 14, 650, boolOr(on, p.Text, p.Text3)).SingleLine().Shrink(1)
					if row.Origin == routing.OriginOverlay {
						a.chip(c, a.t("item.yours"), toneNeutral, "").Shrink(0)
					}
					for k, ch := range chips {
						e := a.chip(c, ch.text, ch.tone, ch.icon).Shrink(0)
						e.Tooltip(ch.tip)
						_ = k
					}
				})
				a.txt(c, a.itemSummary(row), 12, 400, p.Text3).SingleLine()
			})
		})
		if open.Clicked() {
			a.openItem(it.ID)
		}
		a.destButton(c, destOpts{key: "it-dest-" + it.ID, title: name}, it.Dest, func(d routing.Dest) {
			id := it.ID
			a.editRouting(func(s *routing.State) error { return s.SetItemDest(id, d) }, nil)
		})
		if a.toggle(c, "it-on-"+it.ID, &on, name) {
			id := it.ID
			a.editRouting(func(s *routing.State) error { return s.SetItemEnabled(id, on) }, nil)
		}
		a.btn(c, btnSpec{kind: btnGhost, icon: "more", iconOnly: true, small: true, tip: a.t("subs.more")}).PopoverMenuButton(a.itemMenu(c, i, row, n))
	})
}

// itemMenu is the menu of an item's row.
func (a *App) itemMenu(c *ui.Context, i int, row routing.Row, n int) func(m *ui.Menu) {
	it := row.Item
	return func(m *ui.Menu) {
		id := it.ID
		if m.Item(a.t("item.details")).Chosen() {
			a.openItem(id)
		}
		m.Separator()
		if m.Item(a.t("subs.move_up")).Disabled(row.Locked || i == 0).Chosen() {
			a.editRouting(func(s *routing.State) error { return s.Move(id, -1) }, nil)
		}
		if m.Item(a.t("subs.move_down")).Disabled(row.Locked || i == n-1).Chosen() {
			a.editRouting(func(s *routing.State) error { return s.Move(id, 1) }, nil)
		}
		if row.Origin == routing.OriginPlan && (!it.Dest.Equal(row.DefaultDest) || it.Enabled != row.DefaultOn) {
			m.Separator()
			if m.Item(a.t("item.restore")).Chosen() {
				dest, on := row.DefaultDest.Clone(), row.DefaultOn
				a.editRouting(func(s *routing.State) error {
					if err := s.SetItemDest(id, dest); err != nil {
						return err
					}
					return s.SetItemEnabled(id, on)
				}, nil)
			}
		}
		m.Separator()
		active := a.rt().ActiveID()
		var own []*routing.Plan
		for _, pl := range a.rt().Plans {
			if pl.Kind == routing.PlanMine && pl.ID != active {
				own = append(own, pl)
			}
		}
		if len(own) == 0 {
			m.Item(a.t("item.copy.none")).Disabled(true)
		} else {
			m.Submenu(a.t("item.copy"), func(sm *ui.Menu) {
				for _, pl := range own {
					plan, planName := pl.ID, pl.Name
					if sm.Item(planName).Chosen() {
						a.editRouting(func(s *routing.State) error {
							_, err := s.CopyItemToMine(id, plan)
							return err
						}, func(err error) {
							if err != nil {
								a.fail(err)
								return
							}
							a.toastInfo(a.t("item.copied", "name", a.itemName(it), "plan", planName))
						})
					}
				}
			})
		}
		if !row.Locked {
			m.Separator()
			if m.Item(a.t("common.delete")).Chosen() {
				a.confirm(a.t("item.delete.title", "name", a.itemName(it)), a.t("item.delete.body"), a.t("common.delete"), true, func() {
					a.editRouting(func(s *routing.State) error { return s.DeleteItem(id) }, nil)
				})
			}
		}
	}
}

// fallbackRow is the last row: where everything that no item claimed goes.
func (a *App) fallbackRow(c *ui.Context) {
	p := a.pl
	v := a.rv()
	// The gaps and the widths at the end are those of an item's row, so that
	// the buttons of both line up.
	ui.Row(c.Key("fallback")).Padding(10, 14, 10, 20).Gap(10).AlignItems(ui.Center).Children(func() {
		ui.Row(c).Grow(1).Basis(0).Gap(12).AlignItems(ui.Center).Children(func() {
			a.iconTile(c, "layers", toneNeutral, 38)
			ui.Column(c).Grow(1).Basis(0).Gap(3).Children(func() {
				a.txt(c, a.t("fallback.title"), 14, 650, p.Text).SingleLine()
				a.txt(c, a.t("fallback.sub"), 12, 400, p.Text3).SingleLine()
			})
		})
		if v.FallbackOverridden {
			if a.btn(c, btnSpec{kind: btnGhost, icon: "rotate", iconOnly: true, small: true, tip: a.t("fallback.restore")}).Clicked() {
				def := v.FallbackDefault.Clone()
				a.editRouting(func(s *routing.State) error { return s.SetFallback(def) }, nil)
			}
		}
		a.destButton(c, destOpts{key: "fallback-dest", title: a.t("fallback.title")}, v.Fallback, func(d routing.Dest) {
			a.editRouting(func(s *routing.State) error { return s.SetFallback(d) }, nil)
		})
		// Aligned with the switch and the menu of the rows above.
		ui.Box(c).Width(44).Height(1)
		ui.Box(c).Width(30).Height(1)
	})
}
