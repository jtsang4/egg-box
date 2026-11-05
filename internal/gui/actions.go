package gui

import (
	"context"

	"github.com/egoist/mygo/ui"

	"eggbox/internal/app"
	"eggbox/internal/configgen"
)

// toggleConnection connects or disconnects, as the orb and the buttons do.
func (a *App) toggleConnection() {
	switch a.snap.Phase {
	case app.PhaseNoAgent:
		if a.opt.Authorize != nil {
			a.authorize()
		}
		return
	case app.PhaseConnected, app.PhaseReconnecting, app.PhaseConnecting:
		a.disconnect()
	case app.PhaseDisconnecting:
	default:
		if len(a.subs) == 0 || a.nodeCount() == 0 {
			// Adding a subscription happens in the window.
			a.goTo(pageSubscriptions)
			a.openAddSubscription()
			a.reveal()
			a.closePanel()
			return
		}
		a.connect(false)
	}
}

func (a *App) connect(ignoreConflicts bool) {
	a.job("connect", func(ctx context.Context) error {
		return a.be.Connect(ctx, app.ConnectOptions{IgnoreRouteConflicts: ignoreConflicts})
	}, func(err error) {
		if err == nil {
			return
		}
		// The service already reported the failure as a notice; a route
		// conflict also needs a decision.
		if ex := app.Explain(err); ex.Code == "route_conflict" {
			a.openDialog(&routeConflictDialog{detail: ex.Detail})
			a.reveal()
			a.closePanel()
		}
	})
}

func (a *App) disconnect() {
	a.job("connect", func(ctx context.Context) error { return a.be.Disconnect(ctx) }, nil)
}

// authorize asks the system to let this application use the service.
func (a *App) authorize() {
	if a.opt.Authorize == nil {
		return
	}
	a.job("authorize", func(ctx context.Context) error { return a.opt.Authorize(ctx) }, func(err error) {
		if err != nil {
			a.fail(err)
			return
		}
		a.toastInfo(a.t("hero.authorize.done"))
	})
}

// removeService asks first, then puts the network back and takes the service
// out of the system. The application cannot connect again until the person
// allows the service anew; what they added stays.
func (a *App) removeService() {
	if a.opt.RemoveService == nil {
		return
	}
	a.confirm(a.t("settings.service.remove.title"), a.t("settings.service.remove.body"), a.t("settings.service.remove.confirm"), true, func() {
		active := a.snap.Phase.Online() || a.snap.Phase == app.PhaseConnecting
		a.job("remove-service", func(ctx context.Context) error {
			// The connection is closed first, so that the network is back as
			// it was before the service goes (it would do the same on its way
			// out, but this way a failure is reported here, with the cause).
			if active {
				if err := a.be.Disconnect(ctx); err != nil {
					return err
				}
			}
			return a.opt.RemoveService(ctx)
		}, func(err error) {
			if err != nil {
				a.fail(err)
				return
			}
			a.toastInfo(a.t("settings.service.removed"))
		})
	})
}

// testAll measures every node.
func (a *App) testAll() {
	a.job("test-all", func(ctx context.Context) error { return a.be.TestLatency(ctx, configgen.TagProxy) }, nil)
}

// refreshAll updates every subscription.
func (a *App) refreshAll() {
	a.job("refresh-all", func(ctx context.Context) error {
		_, err := a.be.RefreshAll(ctx)
		return err
	}, nil)
}

// refreshOne updates one subscription.
func (a *App) refreshOne(id string) {
	a.job("refresh-"+id, func(ctx context.Context) error {
		_, err := a.be.RefreshSubscription(ctx, id)
		return err
	}, nil)
}

// pickNode chooses the node traffic leaves through ("" is automatic).
func (a *App) pickNode(key string) {
	a.job("select", func(ctx context.Context) error { return a.be.SelectKey(ctx, key) }, func(err error) {
		if err != nil {
			a.fail(err)
		}
	})
}

// updateSettings changes a preference and reports a failure.
func (a *App) updateSettings(fn func(*settingsT)) {
	if err := a.be.UpdateSettings(fn); err != nil {
		a.fail(err)
	}
	a.again()
}

// routeConflictDialog asks whether to connect although another VPN owns
// overlapping routes.
type routeConflictDialog struct{ detail string }

func (d *routeConflictDialog) frame(a *App, c *ui.Context) dlgFrame {
	p := a.pl
	return dlgFrame{
		title: a.t("conflict.title"), subtitle: a.t("conflict.sub"), icon: "alert", tone: toneWarn, width: 480,
		body: func() {
			a.txt(c, a.t("conflict.body"), 13.5, 400, p.Text2).LineHeight(1.55)
			if d.detail != "" {
				ui.Box(c).Padding(12, 14).Radius(12).Background(p.Field).Border(1, p.Line).Children(func() {
					a.txt(c, truncate(d.detail, 400), 12, 400, p.Text3).Font("monospace").LineHeight(1.5)
				})
			}
		},
		buttons: func() {
			if a.btn(c, btnSpec{kind: btnGhost, label: a.t("common.cancel")}).Clicked() {
				a.dlgOpen = false
			}
			if a.btn(c, btnSpec{kind: btnPrimary, label: a.t("conflict.anyway")}).Clicked() {
				a.dlgOpen = false
				a.connect(true)
			}
		},
	}
}
