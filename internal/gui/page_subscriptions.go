package gui

import (
	"context"
	"strconv"

	"github.com/egoist/mygo/ui"

	"eggbox/internal/library"
	"eggbox/internal/routing"
)

// subscriptionsPage manages where the nodes come from.
func (a *App) subscriptionsPage(c *ui.Context) {
	sub := a.t("subs.sub.empty")
	if n := len(a.subs); n > 0 {
		sub = a.tn("subs.sub", n, "nodes", strconv.Itoa(a.nodeCount()))
	}
	a.pageFrame(c, a.t("nav.subscriptions"), sub, func() {
		remote := false
		for _, s := range a.subs {
			if s.Kind == library.KindRemote && s.Enabled {
				remote = true
			}
		}
		if remote {
			busy := a.busy("refresh-all") || len(a.snap.Busy.Refreshing) > 0
			if a.btn(c, btnSpec{kind: btnGhost, label: a.t("subs.refresh_all"), icon: "refresh", busy: busy}).Clicked() {
				a.refreshAll()
			}
		}
		if a.btn(c, btnSpec{kind: btnSecondary, label: a.t("subs.import"), icon: "download"}).Clicked() {
			a.openImport()
		}
		if a.btn(c, btnSpec{kind: btnPrimary, label: a.t("subs.add"), icon: "plus"}).Clicked() {
			a.openAddSubscription()
		}
	}, func() {
		if len(a.subs) == 0 {
			a.card(c).Padding(24).Children(func() {
				a.emptyState(c, "rss", a.t("subs.empty.title"), a.t("subs.empty.body"), func() {
					if a.btn(c, btnSpec{kind: btnPrimary, label: a.t("subs.add"), icon: "plus"}).Clicked() {
						a.openAddSubscription()
					}
					if a.btn(c, btnSpec{kind: btnSecondary, label: a.t("subs.import"), icon: "download"}).Clicked() {
						a.openImport()
					}
				})
			})
			a.subsHints(c)
			return
		}
		for i, s := range a.subs {
			a.subCard(c, i, s)
		}
		a.subsHints(c)
	})
}

// subsHints is the line of advice under the list.
func (a *App) subsHints(c *ui.Context) {
	ui.Row(c).Gap(10).Padding(4, 4).AlignItems(ui.Center).Children(func() {
		ui.Icon(c, icon("lock")).Size(14, 14).TextColor(a.pl.Text3)
		a.txt(c, a.t("subs.privacy"), 12, 400, a.pl.Text3).LineHeight(1.45)
	})
}

// subCard is one subscription.
func (a *App) subCard(c *ui.Context, idx int, s library.Subscription) {
	p := a.pl
	st := a.statusOf(s)
	refreshing := a.busy("refresh-"+s.ID) || a.snap.Busy.Refreshing[s.ID]
	remote := s.Kind == library.KindRemote
	card := a.card(c.Key("sub-" + s.ID)).Padding(0).Gap(0)
	if !s.Enabled {
		card.Opacity(0.72)
	}
	card.Children(func() {
		if a.compact() {
			a.phoneSubHeader(c, idx, s, st, refreshing)
		} else {
			ui.Row(c).Padding(18, 20, 14, 18).Gap(14).AlignItems(ui.Center).Children(func() {
				a.monogram(c, s.Name, 46)
				ui.Column(c).Grow(1).Basis(0).Gap(5).Children(func() {
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						a.txt(c, s.Name, 15.5, 650, p.Text).SingleLine()
						if remote {
							a.chip(c, a.t("subs.kind.remote"), toneNeutral, "cloud")
						} else {
							a.chip(c, a.t("subs.kind.local"), toneNeutral, "inbox")
						}
						a.statusChip(c, st)
					})
					a.txt(c, a.subLine(s), 12.5, 400, p.Text3).SingleLine()
				})
				if remote {
					tip := a.t("subs.update")
					if a.btn(c, btnSpec{kind: btnGhost, icon: "refresh", iconOnly: true, tip: tip, busy: refreshing, disabled: !s.Enabled}).Clicked() {
						a.refreshOne(s.ID)
					}
				}
				more := a.btn(c, btnSpec{kind: btnGhost, icon: "more", iconOnly: true, tip: a.t("subs.more")})
				more.PopoverMenuButton(a.subMenu(c, idx, s))
				on := s.Enabled
				if a.toggle(c, "sub-on-"+s.ID, &on, a.t("subs.enabled")) {
					id := s.ID
					a.job("sub-edit-"+id, func(ctx context.Context) error {
						return a.be.EditSubscription(ctx, id, func(x *library.Subscription) { x.Enabled = on })
					}, func(err error) { a.fail(err) })
				}
			})
		}
		if s.LastError != "" {
			a.subError(c, s)
		}
		if s.Usage != nil && (s.Usage.Total > 0 || !s.Usage.Expire.IsZero()) {
			a.hr(c)
			ui.Column(c).Padding(14, 20, 16, 20).Gap(10).Children(func() {
				a.usageRow(c, "sub-"+s.ID, "", s)
			})
		}
		if plan := a.rt().PlanByID(routing.SubPlanID(s.ID)); plan != nil && plan.Kind == routing.PlanSubscription {
			a.hr(c)
			a.subPlanRow(c, s, plan)
		}
	})
}

// phoneSubHeader is the head of a subscription's card on a phone: the name
// and its line beside the switch, its kind, state and actions under them.
func (a *App) phoneSubHeader(c *ui.Context, idx int, s library.Subscription, st subStatus, refreshing bool) {
	p := a.pl
	remote := s.Kind == library.KindRemote
	ui.Column(c).Padding(14, 16, 10, 14).Gap(10).Children(func() {
		ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
			a.monogram(c, s.Name, 40)
			ui.Column(c).Grow(1).Basis(0).Gap(3).Children(func() {
				a.txt(c, s.Name, 15.5, 650, p.Text).SingleLine()
				a.txt(c, a.subLine(s), 12, 400, p.Text3).MaxLines(2).LineHeight(1.35)
			})
			on := s.Enabled
			if a.toggle(c, "sub-on-"+s.ID, &on, a.t("subs.enabled")) {
				id := s.ID
				a.job("sub-edit-"+id, func(ctx context.Context) error {
					return a.be.EditSubscription(ctx, id, func(x *library.Subscription) { x.Enabled = on })
				}, func(err error) { a.fail(err) })
			}
		})
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			if remote {
				a.chip(c, a.t("subs.kind.remote"), toneNeutral, "cloud")
			} else {
				a.chip(c, a.t("subs.kind.local"), toneNeutral, "inbox")
			}
			a.statusChip(c, st)
			ui.Spacer(c)
			if remote {
				if a.btn(c, btnSpec{kind: btnGhost, icon: "refresh", iconOnly: true, small: true, tip: a.t("subs.update"), busy: refreshing, disabled: !s.Enabled}).Clicked() {
					a.refreshOne(s.ID)
				}
			}
			more := a.btn(c, btnSpec{kind: btnGhost, icon: "more", iconOnly: true, small: true, tip: a.t("subs.more")})
			more.PopoverMenuButton(a.subMenu(c, idx, s))
		})
	})
}

// subPlanRow tells that the subscription came with a routing plan and offers
// to use it.
func (a *App) subPlanRow(c *ui.Context, s library.Subscription, plan *routing.Plan) {
	p := a.pl
	active := a.rt().ActiveID() == plan.ID
	ui.Row(c.Key("sub-plan-"+s.ID)).Padding(12, 20, 14, 20).Gap(12).AlignItems(ui.Center).Children(func() {
		a.iconTile(c, "layers", boolOr(active, toneBlue, toneNeutral), 32)
		ui.Column(c).Grow(1).Basis(0).Gap(2).Children(func() {
			title := a.txt(c, a.tn("subs.plan", len(plan.Items)), 13.5, 650, p.Text)
			hint := a.txt(c, boolOr(active, a.t("subs.plan.active"), a.t("subs.plan.hint")), 12, 400, p.Text3)
			if a.compact() {
				title.MaxLines(2)
				hint.MaxLines(2)
			} else {
				title.SingleLine()
				hint.SingleLine()
			}
		})
		if active {
			a.chip(c, a.t("subs.plan.in_use"), toneGood, "check-circle")
			return
		}
		if a.btn(c, btnSpec{kind: btnSoft, small: true, label: a.t("subs.plan.use")}).Clicked() {
			id := plan.ID
			a.editRouting(func(st *routing.State) error { return st.Switch(id) }, nil)
		}
	})
}

// subLine summarizes a subscription in one line.
func (a *App) subLine(s library.Subscription) string {
	parts := []string{}
	if s.Host != "" {
		parts = append(parts, s.Host)
	}
	parts = append(parts, a.tn("subs.nodes", s.NodeCount))
	if s.Kind == library.KindRemote {
		if s.CheckedAt.IsZero() {
			parts = append(parts, a.t("subs.never_updated"))
		} else {
			parts = append(parts, a.t("subs.updated", "when", a.ago(s.CheckedAt)))
		}
		switch {
		case s.IntervalHours > 0 && s.IntervalAuto:
			parts = append(parts, a.t("subs.every_auto", "h", strconv.Itoa(s.IntervalHours)))
		case s.IntervalHours > 0:
			parts = append(parts, a.t("subs.every", "h", strconv.Itoa(s.IntervalHours)))
		default:
			parts = append(parts, a.t("subs.manual"))
		}
	} else {
		parts = append(parts, a.t("subs.added", "when", a.date(s.CreatedAt)))
	}
	if s.SkippedCount > 0 {
		parts = append(parts, a.tn("subs.skipped", s.SkippedCount))
	}
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += "  ·  "
		}
		out += p
	}
	return out
}

func (a *App) statusChip(c *ui.Context, st subStatus) {
	switch st {
	case subStale:
		a.chip(c, a.t("subs.status.stale"), toneWarn, "")
	case subFailing:
		a.chip(c, a.t("subs.status.failing"), toneBad, "alert-circle")
	case subDisabled:
		a.chip(c, a.t("subs.status.disabled"), toneNeutral, "pause")
	case subExpired:
		a.chip(c, a.t("subs.status.expired"), toneBad, "clock")
	case subExhausted:
		a.chip(c, a.t("subs.status.exhausted"), toneWarn, "gauge")
	default:
		a.chip(c, a.t("subs.status.ok"), toneGood, "check-circle")
	}
}

func (a *App) subError(c *ui.Context, s library.Subscription) {
	p := a.pl
	ui.Row(c).Margin(0, 20, 14, 20).Padding(10, 12).Gap(10).Radius(12).Background(p.Bad.Alpha(0.1)).Border(1, p.Bad.Alpha(0.22)).Children(func() {
		ui.Icon(c, icon("alert-circle")).Size(16, 16).TextColor(p.Bad)
		ui.Column(c).Grow(1).Basis(0).Gap(2).Children(func() {
			a.txt(c, a.t("subs.error.title"), 12.5, 650, p.Bad)
			a.txt(c, a.explainCode(s.LastError, ""), 12.5, 400, p.Text2).LineHeight(1.45)
		})
	})
}

// subMenu is the menu of a subscription's ⋯ button.
func (a *App) subMenu(c *ui.Context, idx int, s library.Subscription) func(m *ui.Menu) {
	id := s.ID
	remote := s.Kind == library.KindRemote
	return func(m *ui.Menu) {
		if remote && m.Item(a.t("subs.update")).Disabled(!s.Enabled).Chosen() {
			a.refreshOne(id)
		}
		if m.Item(a.t("common.edit")).Chosen() {
			a.openEditSubscription(s)
		}
		if !remote {
			// Every node of the import, those it leaves out or that are off
			// included: fixing one may be why they are.
			mine, _ := a.be.Nodes(id)
			switch {
			case len(mine) == 1:
				if m.Item(a.t("subs.edit_node")).Chosen() {
					a.openNodeLink(id, s.Name, mine[0])
				}
			case len(mine) > 1:
				m.Submenu(a.t("subs.edit_node"), func(sm *ui.Menu) {
					for _, n := range mine {
						if sm.Item(n.Name).Chosen() {
							a.openNodeLink(id, s.Name, n)
						}
					}
				})
			}
		}
		if remote {
			if m.Item(a.t("subs.copy_link")).Chosen() {
				if link, err := a.be.Link(id); err == nil {
					c.WriteClipboard(link)
					a.toastInfo(a.t("subs.link_copied"))
				} else {
					a.fail(err)
				}
			}
			if s.HomePage != "" && m.Item(a.t("subs.open_home")).Chosen() {
				c.OpenURL(s.HomePage)
			}
		}
		m.Separator()
		if m.Item(a.t("subs.move_up")).Disabled(idx == 0).Chosen() {
			a.moveSub(id, idx-1)
		}
		if m.Item(a.t("subs.move_down")).Disabled(idx == len(a.subs)-1).Chosen() {
			a.moveSub(id, idx+1)
		}
		m.Separator()
		if m.Item(a.t("common.delete")).Chosen() {
			body := a.t("subs.delete.body")
			if pl := a.rt().PlanByID(routing.SubPlanID(id)); pl != nil && pl.Kind == routing.PlanSubscription {
				body = a.t("subs.delete.body_plan")
			}
			a.confirm(a.t("subs.delete.title", "name", s.Name), body, a.t("common.delete"), true, func() {
				a.job("sub-del-"+id, func(ctx context.Context) error { return a.be.RemoveSubscription(ctx, id) },
					func(err error) { a.fail(err) })
			})
		}
	}
}

func (a *App) moveSub(id string, to int) {
	if err := a.be.MoveSubscription(id, to); err != nil {
		a.fail(err)
	}
}
