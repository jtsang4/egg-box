package main

import (
	"runtime"

	"github.com/egoist/mygo"

	"eggbox/internal/brand"
)

// menuItems are the items of the menu bar whose text follows the connection.
type menuItems struct {
	connect *mygo.MenuItem
	modes   []*mygo.MenuItem
}

// buildMenu installs the menu bar. Only macOS has a menu bar of the
// application: on Windows and Linux the window draws its own chrome and the
// tray carries the connection's menu.
func (d *desktop) buildMenu() {
	if runtime.GOOS != "darwin" {
		mygo.App.SetMenu(nil)
		return
	}
	say := d.app.Say
	goTo := func(page string) func(*mygo.MenuItem, *mygo.Window) {
		return func(*mygo.MenuItem, *mygo.Window) {
			d.app.Navigate(page)
			d.showWindow()
		}
	}

	d.menu.connect = &mygo.MenuItem{
		Label:       say("hero.connect"),
		Accelerator: "CmdOrCtrl+Shift+C",
		Click:       func(*mygo.MenuItem, *mygo.Window) { d.app.Toggle() },
	}
	d.menu.modes = nil
	modes := []*mygo.MenuItem{}
	for _, m := range d.app.Modes() {
		item := &mygo.MenuItem{
			Label: d.app.ModeName(m),
			Type:  mygo.MenuItemRadio,
			Click: func(*mygo.MenuItem, *mygo.Window) { d.app.SetMode(m) },
		}
		d.menu.modes = append(d.menu.modes, item)
		modes = append(modes, item)
	}

	pages := []struct{ key, slug, accel string }{
		{"nav.dashboard", "dashboard", "CmdOrCtrl+1"},
		{"nav.nodes", "nodes", "CmdOrCtrl+2"},
		{"nav.subscriptions", "subscriptions", "CmdOrCtrl+3"},
		{"nav.rules", "rules", "CmdOrCtrl+4"},
		{"nav.connections", "connections", "CmdOrCtrl+5"},
		{"nav.logs", "logs", "CmdOrCtrl+6"},
	}
	var pageItems []*mygo.MenuItem
	for _, p := range pages {
		pageItems = append(pageItems, &mygo.MenuItem{Label: say(p.key), Accelerator: p.accel, Click: goTo(p.slug)})
	}

	mygo.App.SetMenu(mygo.NewMenu([]*mygo.MenuItem{
		{Label: brand.DisplayName, Submenu: []*mygo.MenuItem{
			{Role: mygo.RoleAbout},
			mygo.Separator(),
			{Label: say("tray.settings"), Accelerator: "CmdOrCtrl+,", Click: goTo("settings")},
			mygo.Separator(),
			{Role: mygo.RoleServices},
			mygo.Separator(),
			{Role: mygo.RoleHide},
			{Role: mygo.RoleHideOthers},
			{Role: mygo.RoleUnhide},
			mygo.Separator(),
			{Role: mygo.RoleQuit},
		}},
		{Role: mygo.RoleEditMenu},
		{Label: say("menu.connection"), Submenu: append([]*mygo.MenuItem{
			d.menu.connect,
			mygo.Separator(),
			{Label: say("tray.mode"), Disabled: true},
		}, modes...)},
		{Label: say("menu.go"), Submenu: pageItems},
		{Role: mygo.RoleWindowMenu},
	}))
	d.syncMenu()
}

// syncMenu brings the connection's items of the menu bar up to date.
func (d *desktop) syncMenu() {
	if d.menu.connect == nil || d.app == nil {
		return
	}
	st := d.app.Status()
	d.menu.connect.SetLabel(d.app.ConnectionLabel(st))
	d.menu.connect.SetEnabled(!st.Working)
	for i, m := range d.app.Modes() {
		if i < len(d.menu.modes) {
			d.menu.modes[i].SetEnabled(st.Phase.Online())
			d.menu.modes[i].SetChecked(st.Mode == m)
		}
	}
}
