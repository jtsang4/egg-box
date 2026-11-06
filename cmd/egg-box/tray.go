package main

import (
	"log"
	"runtime"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"eggbox/internal/brand"
	"eggbox/internal/brandart"
	"eggbox/internal/gui"
)

// The tray icon says whether the proxy is up; a click opens the panel, a right
// click the menu. Linux's tray icons tell nothing of clicks, only show a menu,
// so there the menu carries everything.

func (d *desktop) newTray() {
	t, err := mygo.NewTray(mygo.TrayOptions{
		ID:             brand.AppID,
		Icon:           trayIcon(false),
		IconIsTemplate: runtime.GOOS == "darwin",
		ToolTip:        brand.DisplayName,
	})
	if err != nil {
		// No notification area (some Linux desktops): the window is all there
		// is, and closing it quits.
		log.Printf("tray: %v", err)
		return
	}
	d.tray = t
	d.lastOn.Store(1)
	if runtime.GOOS == "linux" {
		t.SetMenu(d.trayMenu())
		return
	}
	t.OnClick(d.togglePanel)
	// Ready before the first click, so that it opens at once.
	d.newPanel()
	t.OnRightClick(func() {
		d.closePanel()
		// The menu for this popup only, so that clicks keep toggling the
		// panel.
		t.SetMenu(d.trayMenu())
		defer t.SetMenu(nil)
		t.PopUpMenu()
	})
}

// trayIcon draws the mark: black for macOS to tint, blue elsewhere.
func trayIcon(on bool) []byte {
	if runtime.GOOS == "darwin" {
		return brandart.PNG(brandart.TrayIcon(44, brandart.TrayTemplate, on))
	}
	return brandart.PNG(brandart.TrayIcon(32, brandart.TrayBlue, on))
}

// changed is called, from any goroutine, whenever the service's state moves.
// Several changes close together make one update.
func (d *desktop) changed() {
	if d.tray == nil && d.menu.connect == nil {
		return
	}
	if d.syncing.CompareAndSwap(false, true) {
		time.AfterFunc(200*time.Millisecond, func() {
			d.syncing.Store(false)
			mygo.RunOnMain(func() {
				d.syncTray()
				d.syncMenu()
			})
		})
	}
}

// syncTray brings the icon, its tooltip and (on Linux) its menu up to date.
func (d *desktop) syncTray() {
	if d.tray == nil || d.app == nil {
		return
	}
	st := d.app.Status()
	want := int32(1)
	if st.On {
		want = 2
	}
	if d.lastOn.Swap(want) != want {
		if err := d.tray.SetIcon(trayIcon(st.On), runtime.GOOS == "darwin"); err != nil {
			log.Printf("tray icon: %v", err)
		}
	}
	d.tray.SetToolTip(d.app.Tooltip(st))
	if runtime.GOOS == "linux" {
		d.tray.SetMenu(d.trayMenu())
	}
}

// trayMenu is the menu of the tray icon, built for the state of the moment.
func (d *desktop) trayMenu() *mygo.Menu {
	say := d.app.Say
	st := d.app.Status()
	status := st.Word
	if st.Node != "" {
		status += " · " + st.Node
	}
	items := []*mygo.MenuItem{
		{Label: status, Disabled: true},
		{
			Label:    d.app.ConnectionLabel(st),
			Disabled: st.Working || !d.app.CanConnect(),
			Click:    func(*mygo.MenuItem, *mygo.Window) { d.app.Toggle() },
		},
		mygo.Separator(),
	}
	if st.Phase.Online() {
		items = append(items, &mygo.MenuItem{Label: say("tray.mode"), Disabled: true})
		for _, m := range d.app.Modes() {
			items = append(items, &mygo.MenuItem{
				Label:   d.app.ModeName(m),
				Type:    mygo.MenuItemRadio,
				Checked: st.Mode == m,
				Click:   func(*mygo.MenuItem, *mygo.Window) { d.app.SetMode(m) },
			})
		}
		items = append(items, mygo.Separator())
	}
	items = append(items,
		&mygo.MenuItem{Label: say("tray.open"), Click: func(*mygo.MenuItem, *mygo.Window) { d.showWindow() }},
		&mygo.MenuItem{Label: say("tray.settings"), Click: func(*mygo.MenuItem, *mygo.Window) {
			d.app.Navigate("settings")
			d.showWindow()
		}},
		mygo.Separator(),
		&mygo.MenuItem{Label: say("tray.quit"), Click: func(*mygo.MenuItem, *mygo.Window) { mygo.App.Quit() }},
	)
	return mygo.NewMenu(items)
}

// ---------------------------------------------------------------- the panel

// newPanel makes the panel once, hidden; after that it is only shown and
// hidden, as the menu bar's own panels are. Making a window, its GPU surface
// and its first frame on every click made it open a beat late. A window made
// hidden draws its first frame at once, and while hidden it draws nothing:
// the redraws asked for wait until it shows, which then draws the state of
// the moment.
func (d *desktop) newPanel() {
	panel := mygo.NewFlyout(mygo.FlyoutOptions{
		Tray:      d.tray,
		Placement: mygo.PlacementBottom,
		Gap:       6,
		Width:     gui.PanelWidth,
		Height:    gui.PanelHeight,
		Focusable: true,
		Shadow:    true,
		Hidden:    true,
		Content:   ui.View(d.app.PanelView),
	})
	d.panel = panel
	// Dismissing it (a click elsewhere, Escape) hides it; only quitting
	// closes it, which must not be refused.
	panel.OnClose(func(e *mygo.CloseEvent) {
		if !d.quitting {
			e.PreventDefault()
			panel.Hide()
		}
	})
	panel.OnClosed(func() {
		if d.panel == panel {
			d.panel = nil
		}
		d.app.PanelClosed()
	})
}

// togglePanel shows the panel next to the tray icon, or hides it.
func (d *desktop) togglePanel() {
	if d.panel == nil {
		d.newPanel()
	}
	if d.panel.IsVisible() {
		d.panel.Hide()
		return
	}
	// Setting its size places it next to the icon again, which may have
	// moved since (another display, other menu bar items).
	d.panel.SetContentSize(gui.PanelWidth, gui.PanelHeight)
	d.panel.Show()
}

func (d *desktop) closePanel() {
	if d.panel != nil {
		d.panel.Hide()
	}
}

// ----------------------------------------------------------- notifications

// notify tells the user something with the window out of sight: a toast in
// the window would not be seen. While the application is in front its own
// toasts do the job.
func (d *desktop) notify(title, body string) {
	if mygo.App.IsActive() || !mygo.NotificationsSupported() {
		return
	}
	n := mygo.NewNotification(mygo.NotificationOptions{ID: brand.AppID + ".event", Title: title, Body: body})
	n.OnClick(func() { mygo.RunOnMain(d.showWindow) })
	go func() {
		if err := n.Show(); err != nil {
			log.Printf("notification: %v", err)
		}
	}()
}
