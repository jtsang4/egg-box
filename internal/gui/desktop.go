package gui

import (
	"context"
	"fmt"
	"strings"

	"eggbox/internal/app"
	"eggbox/internal/brand"
)

// This file is what the desktop shell (the tray icon, its menu, the system
// notifications) asks of the interface. None of it draws anything: the shell
// lives outside the window, so it must read the state itself instead of
// waiting for a frame, and it speaks in the user's language without one.

// Status is the connection at a glance.
type Status struct {
	Phase app.Phase
	// On is true while traffic goes through the proxy.
	On bool
	// Working is true while the connection changes.
	Working bool
	// Word names the phase in the user's language.
	Word string
	// Node is the node traffic leaves through, "" when there is none.
	Node string
	// Down and Up are the current rates, in bytes per second.
	Down, Up float64
	// Mode is the routing mode in use (configgen.ModeRule…), "" while not
	// connected.
	Mode string
	// Language is the interface language ("en", "zh").
	Language string
}

// Status reads the connection now. It may be called from any goroutine.
func (a *App) Status() Status {
	snap := a.be.Snapshot()
	lang := a.languageNow()
	st := Status{Phase: snap.Phase, On: snap.Phase == app.PhaseConnected, Language: lang}
	st.Working = snap.Phase == app.PhaseConnecting || snap.Phase == app.PhaseReconnecting || snap.Phase == app.PhaseDisconnecting
	_, st.Word = phaseToneIn(lang, snap.Phase)
	if snap.Phase.Online() {
		if it, _, ok := exitNodeIn(snap); ok {
			st.Node = it.Node.Name
		}
		if snap.Traffic.Available {
			st.Down, st.Up = snap.Traffic.Down, snap.Traffic.Up
		}
		if s := snap.Status; s != nil {
			st.Mode = s.GetMode()
		}
	}
	return st
}

// CanConnect reports whether a click on Connect can do something: there are
// nodes to connect through, or the service only needs to be approved. It
// reads the subscriptions' files, so the shell asks when it opens a menu, not
// on every change.
func (a *App) CanConnect() bool {
	if a.be.Snapshot().Phase == app.PhaseNoAgent {
		return a.opt.Authorize != nil
	}
	nodes, err := a.be.ActiveNodes()
	return err == nil && len(nodes) > 0
}

// languageNow is the interface language without a frame: the user's choice,
// else the system's. It may be called from any goroutine.
func (a *App) languageNow() string {
	if l := a.be.Settings().Language; l != "" {
		return l
	}
	if a.opt.SystemLocale != nil {
		return systemLanguage(a.opt.SystemLocale())
	}
	return "en"
}

// Say words a key in the user's language, for the shell's menus.
func (a *App) Say(key string, kv ...string) string {
	return translate(a.languageNow(), key, append([]string{"app", brand.DisplayName}, kv...)...)
}

// Tooltip is the text of the tray icon's tooltip.
func (a *App) Tooltip(st Status) string {
	var b strings.Builder
	b.WriteString(brand.DisplayName)
	b.WriteString(" — ")
	b.WriteString(st.Word)
	if st.Node != "" {
		b.WriteString(" · ")
		b.WriteString(st.Node)
	}
	if st.On {
		fmt.Fprintf(&b, "\n↓ %s   ↑ %s", fmtRate(st.Down), fmtRate(st.Up))
	}
	return b.String()
}

// CloseToTray tells whether closing the window should only hide it.
func (a *App) CloseToTray() bool { return a.be.Settings().CloseToTray }

// Modes lists the routing modes the tray offers, with their names.
func (a *App) Modes() []string { return modeNames }

// ModeName words a routing mode.
func (a *App) ModeName(mode string) string { return a.Say("mode." + strings.ToLower(mode)) }

// SetMode changes the routing mode of the running connection.
func (a *App) SetMode(mode string) {
	a.refresh()
	if !a.snap.Phase.Online() {
		return
	}
	a.job("mode", func(ctx context.Context) error { return a.be.SetMode(ctx, mode) }, nil)
}

// Toggle connects or disconnects, as the connect button does. Main thread.
func (a *App) Toggle() {
	a.refresh()
	a.toggleConnection()
}

// Navigate shows a page of the window: "dashboard", "nodes", "subscriptions",
// "routing", "connections", "logs" or "settings". Main thread.
func (a *App) Navigate(name string) {
	a.refresh()
	for pg, n := range pageSlugs {
		if n == name {
			a.goTo(page(pg))
			break
		}
	}
	a.again()
}

// pageSlugs names the pages for the shell, in the order of the page type.
var pageSlugs = [...]string{"dashboard", "nodes", "subscriptions", "routing", "connections", "logs", "settings"}

// reveal brings the window to the front: what the shell does when a click
// needs the window (adding the first subscription, a decision to make).
func (a *App) reveal() {
	if a.opt.ShowWindow != nil {
		a.opt.ShowWindow()
	}
}

// refresh reads the backend into the per-frame state, for what runs outside
// a frame (a menu item, a shortcut of the shell).
func (a *App) refresh() {
	a.now = a.opt.Now()
	a.snap = a.be.Snapshot()
	a.set = a.be.Settings()
	a.subs = a.be.Subscriptions()
	a.prefs = a.be.Prefs()
	a.lang = a.languageNow()
}

// ---------------------------------------------------------- notifications

// notifyNew shows a system notification for what the service reported since
// the last time, if the user wants them. It runs on any goroutine, after the
// backend changed, because the window that would show a toast may be hidden.
func (a *App) notifyNew() {
	if a.opt.Notify == nil {
		return
	}
	snap := a.be.Snapshot()
	set := a.be.Settings()
	a.nmu.Lock()
	var fresh []app.Notice
	if !a.nStarted {
		// What happened before this run is not news.
		a.nStarted = true
		if n := len(snap.Notices); n > 0 {
			a.nLast = snap.Notices[n-1].ID
		}
	} else {
		for _, n := range snap.Notices {
			if n.ID > a.nLast {
				a.nLast = n.ID
				fresh = append(fresh, n)
			}
		}
	}
	a.nmu.Unlock()
	if !set.Notifications {
		return
	}
	lang := a.languageNow()
	for _, n := range fresh {
		if !worthANotification(n) {
			continue
		}
		title, body := notificationText(lang, n)
		a.opt.Notify(title, body)
	}
}

// worthANotification: what the user wants to hear about even with the window
// out of sight. A confirmation of what they just did is not.
func worthANotification(n app.Notice) bool {
	switch n.Level {
	case app.LevelError, app.LevelWarning:
		return true
	}
	return n.Code == "reconnected"
}

// notificationText words a notice for the system, in a language, with no
// window to ask. The words come from the same catalog as the toasts.
func notificationText(lang string, n app.Notice) (title, body string) {
	tr := &App{lang: lang}
	return tr.noticeTitle(n), tr.noticeBody(n)
}

// connectionKey is the catalog key of the toggle's label.
func (st Status) connectionKey() string {
	switch {
	case st.Phase == app.PhaseNoAgent:
		return "hero.authorize"
	case st.On || st.Working && st.Phase != app.PhaseDisconnecting:
		return "hero.disconnect"
	case st.Phase == app.PhaseFailed:
		return "hero.retry"
	}
	return "hero.connect"
}

// ConnectionLabel is the label of the menu item that toggles the connection.
func (a *App) ConnectionLabel(st Status) string { return a.Say(st.connectionKey()) }
