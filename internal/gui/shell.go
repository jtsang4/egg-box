package gui

import (
	"runtime"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/jtsang4/sing-box-ui/internal/app"
)

// navItem is one destination of the sidebar.
type navItem struct {
	page page
	icon string
	key  string
}

var navMain = []navItem{
	{pageDashboard, "home", "nav.dashboard"},
	{pageNodes, "globe", "nav.nodes"},
	{pageSubscriptions, "rss", "nav.subscriptions"},
	{pageRules, "route", "nav.rules"},
}

var navMonitor = []navItem{
	{pageConnections, "activity", "nav.connections"},
	{pageLogs, "terminal", "nav.logs"},
}

const (
	navItemH   = 40
	navItemGap = 4
)

// shell is the whole window: ambient light, sidebar, page, overlays.
func (a *App) shell(c *ui.Context) {
	p := a.pl
	// Keyboard: ⌘/Ctrl+1…7 switch pages, ⌘/Ctrl+, opens the settings.
	a.shortcuts(c)

	root := ui.Row(c).Fill().Background(p.Window).AlignItems(ui.Stretch).Clip()
	root.Draw(a.ambient)
	root.Children(func() {
		a.sidebar(c)
		ui.Column(c).Grow(1).Clip().Children(func() {
			a.pageHost(c)
		})
		a.dialogLayer(c)
		a.toastLayer(c)
	})
}

// shortcuts handles the window-wide keys.
func (a *App) shortcuts(c *ui.Context) {
	mod := ui.Cmd
	if runtime.GOOS != "darwin" {
		mod = ui.Ctrl
	}
	if a.dlg != nil {
		return
	}
	keys := []ui.Key{ui.Key1, ui.Key2, ui.Key3, ui.Key4, ui.Key5, ui.Key6}
	items := append(append([]navItem{}, navMain...), navMonitor...)
	for i, k := range keys {
		if c.Shortcut(mod, k) {
			a.goTo(items[i].page)
		}
	}
	if c.Shortcut(mod, ui.KeyComma) {
		a.goTo(pageSettings)
	}
}

// ambient paints the soft blue light behind the interface. It is painted by
// the window's root, under everything, so that it never takes a click.
func (a *App) ambient(pt *ui.Painter, r ui.Rect) {
	p := a.pl
	k := float32(1)
	if !p.Dark {
		k = 0.55
	}
	// Upper left: the brand's blue, large and faint.
	pt.FillRadialGradient(ui.Rect{X: r.X - 260, Y: r.Y - 340, W: 980, H: 980}, ui.RadialGradient{
		Center: ui.Point{X: 0.5, Y: 0.5}, Radius: 490,
		Stops: []ui.GradientStop{
			{Color: p.Blue.Alpha(0.2 * k), At: 0},
			{Color: p.Blue.Alpha(0.07 * k), At: 0.55},
			{Color: p.Blue.Alpha(0), At: 1},
		},
	}, 0)
	// Lower right: a cool cyan counterpoint.
	pt.FillRadialGradient(ui.Rect{X: r.X + r.W - 620, Y: r.Y + r.H - 520, W: 900, H: 900}, ui.RadialGradient{
		Center: ui.Point{X: 0.5, Y: 0.5}, Radius: 450,
		Stops: []ui.GradientStop{
			{Color: p.Cyan.Alpha(0.1 * k), At: 0},
			{Color: p.Cyan.Alpha(0.03 * k), At: 0.6},
			{Color: p.Cyan.Alpha(0), At: 1},
		},
	}, 0)
}

// sidebar is the navigation column.
func (a *App) sidebar(c *ui.Context) {
	p := a.pl
	side := ui.Column(c).Width(sidebarWidth).Background(p.Sidebar.Alpha(boolOr[float32](p.Dark, 0.72, 0.8))).Padding(0, 14, 16, 14)
	side.Children(func() {
		// The title bar zone: the window controls sit here on macOS.
		top := a.bar.Height
		if top < 28 {
			top = 28
		}
		ui.Box(c).Height(top+6).DragWindow().Margin(0, -14)

		a.brand(c)
		ui.Box(c).Height(14)

		cur := a.pageNavIndex()
		pos := a.navIndicatorY(cur)
		list := ui.Column(c).Gap(navItemGap)
		if cur >= 0 {
			// The gliding indicator under the active item, painted by the list
			// beneath its rows.
			y := list.AnimateTo("ind", pos, ui.Animation{Spring: ui.SpringOptions{Duration: 380 * time.Millisecond, Bounce: 0.14}})
			list.Draw(func(pt *ui.Painter, r ui.Rect) {
				rc := ui.Rect{X: r.X, Y: r.Y + y, W: r.W, H: navItemH}
				pt.FillGradient(rc, ui.LinearGradient{From: p.Blue.Alpha(boolOr[float32](p.Dark, 0.26, 0.18)), To: p.Blue.Alpha(boolOr[float32](p.Dark, 0.10, 0.08)), Angle: 90}, 12)
				pt.Stroke(rc, p.Blue.Alpha(boolOr[float32](p.Dark, 0.34, 0.26)), 12, 1)
			})
		}
		list.Children(func() {
			a.navSection(c, "nav.section.main", navMain)
			ui.Box(c).Height(12)
			a.navSection(c, "nav.section.monitor", navMonitor)
		})

		ui.Spacer(c)
		a.miniStatus(c)
		ui.Box(c).Height(10)
		a.navButton(c, navItem{pageSettings, "settings", "nav.settings"})
	})
	ui.Box(c).Width(1).Background(p.Line)
}

func boolOr[T any](b bool, yes, no T) T {
	if b {
		return yes
	}
	return no
}

// pageNavIndex is the position of the page in the main list, or -1.
func (a *App) pageNavIndex() int {
	i := 0
	for _, it := range navMain {
		if it.page == a.page {
			return i
		}
		i++
	}
	for j, it := range navMonitor {
		if it.page == a.page {
			return len(navMain) + j
		}
	}
	return -1
}

// navIndicatorY is where the indicator sits for the item at index i: the
// total of what the list lays out above it, a gap after each child.
func (a *App) navIndicatorY(i int) float32 {
	if i < 0 {
		return 0
	}
	// The list's children, top to bottom: a heading, the main items, a space,
	// a heading and the monitor items.
	heights := []float32{22}
	for range navMain {
		heights = append(heights, navItemH)
	}
	heights = append(heights, 12, 22)
	for range navMonitor {
		heights = append(heights, navItemH)
	}
	above := 1 + i
	if i >= len(navMain) {
		above = 1 + len(navMain) + 2 + (i - len(navMain))
	}
	var y float32
	for _, h := range heights[:above] {
		y += h + navItemGap
	}
	return y
}

func (a *App) navSection(c *ui.Context, key string, items []navItem) {
	ui.Box(c).Height(22).Padding(0, 0, 0, 12).Justify(ui.End).Children(func() {
		a.eyebrow(c, a.t(key))
	})
	for _, it := range items {
		a.navButton(c, it)
	}
}

// navButton is one row of the sidebar.
func (a *App) navButton(c *ui.Context, it navItem) {
	p := a.pl
	active := a.page == it.page
	b := ui.ButtonBase(c.Key("nav-" + it.key))
	b.Label(a.t(it.key))
	b.Height(navItemH).Padding(0, 12).Gap(12).Radius(12).AlignItems(ui.Center).Cursor(ui.CursorPointer)
	h := b.AnimateTo("h", boolf(b.Hovered() && !active), ui.Animation{Duration: 140 * time.Millisecond})
	b.Background(p.PanelHi.Alpha(0.9 * h))
	col := p.Text2.Mix(p.Text, h)
	iconCol := p.Text3.Mix(p.Text2, h)
	if active {
		col = p.Text
		iconCol = p.BlueText
	}
	b.TextColor(col)
	b.Children(func() {
		ui.Icon(c, icon(it.icon)).Size(18, 18).TextColor(iconCol)
		ui.Text(c, a.t(it.key)).FontSize(13.5).FontWeight(boolOr(active, 650, 520)).SingleLine().Grow(1)
		if n := a.navBadge(it.page); n != "" {
			a.txt(c, n, 11, 600, p.Text3).FontFeatures("tnum").SingleLine()
		}
	})
	if b.Clicked() {
		a.goTo(it.page)
	}
}

// navBadge is the small count at the right of a sidebar row.
func (a *App) navBadge(pg page) string {
	switch pg {
	case pageNodes:
		if n := a.nodeCount(); n > 0 {
			return fmtCount(int64(n))
		}
	case pageSubscriptions:
		if n := len(a.subs); n > 0 {
			return fmtCount(int64(n))
		}
	case pageConnections:
		if a.snap.Phase.Online() && a.snap.Traffic.Available {
			return fmtCount(int64(a.snap.Traffic.Connections))
		}
	}
	return ""
}

// brand is the logo and name.
func (a *App) brand(c *ui.Context) {
	p := a.pl
	ui.Row(c).Gap(11).Padding(2, 4).AlignItems(ui.Center).Children(func() {
		a.logo(c, 38)
		ui.Column(c).Gap(1).Children(func() {
			a.txt(c, "Sing-Box", 15.5, 700, p.Text).LetterSpacing(-0.2).SingleLine()
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				a.txt(c, "UI", 11.5, 700, p.BlueText).LetterSpacing(1.2).SingleLine()
				if a.opt.Demo {
					a.chip(c, a.t("brand.demo"), toneWarn, "")
				}
			})
		})
	})
}

// logo is the brand mark: an isometric box on a blue tile.
func (a *App) logo(c *ui.Context, size float32) ui.Element {
	p := a.pl
	return ui.Box(c).Size(size, size).Radius(size*0.3).Center().
		LinearGradient(ui.LinearGradient{From: p.BlueHi, To: p.BlueLo, Angle: 150}).
		InsetShadow(0, 1, 0, 0, ui.RGBA(255, 255, 255, 0.4)).
		Shadow(0, 8, 20, -6, p.Glow).
		Children(func() {
			ui.Icon(c, logoMark).Size(size*0.6, size*0.6).TextColor(ui.Hex("#FFFFFF"))
		})
}

// logoMark is a cube seen from a corner, with a pulse at its heart.
var logoMark = mustSVG(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><path d="M12 2.8 20.2 7.4v9.2L12 21.2 3.8 16.6V7.4L12 2.8Z"/><path d="M3.9 7.5 12 12l8.1-4.5"/><path d="M12 12v9"/></svg>`)

// miniStatus is the connection summary at the foot of the sidebar.
func (a *App) miniStatus(c *ui.Context) {
	p := a.pl
	ph := a.snap.Phase
	t, label := a.phaseTone(ph)
	b := ui.ButtonBase(c.Key("mini-status"))
	b.Label(label).Padding(12, 14).Radius(16).Gap(8).Cursor(ui.CursorPointer)
	h := b.AnimateTo("h", boolf(b.Hovered()), ui.Animation{Duration: 140 * time.Millisecond})
	b.Background(p.Panel.Mix(p.PanelHi, h)).Border(1, p.Line.Mix(p.LineHi, h))
	b.Children(func() {
		ui.Column(c).Grow(1).Gap(8).Children(func() {
			ui.Row(c).Gap(9).AlignItems(ui.Center).Children(func() {
				a.dot(c, t, 8, ph == app.PhaseConnected)
				a.txt(c, label, 13, 650, p.Text).SingleLine()
			})
			if ph.Online() && a.snap.Traffic.Available {
				tr := a.snap.Traffic
				ui.Row(c).Gap(12).Children(func() {
					a.speedPair(c, "arrow-down", tr.Down, p.BlueText)
					a.speedPair(c, "arrow-up", tr.Up, p.Cyan)
				})
				a.sparkline(c, "mini", tr.History, 36, true)
			} else {
				a.txt(c, a.phaseHint(ph), 12, 400, p.Text3).LineHeight(1.35)
			}
		})
	})
	if b.Clicked() {
		a.goTo(pageDashboard)
	}
}

func (a *App) speedPair(c *ui.Context, iconName string, v float64, col ui.Color) {
	ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
		ui.Icon(c, icon(iconName)).Size(12, 12).TextColor(col)
		a.txt(c, fmtRate(v), 12, 600, a.pl.Text2).FontFeatures("tnum").SingleLine()
	})
}

// phaseTone is the colour and the word of a connection phase.
func (a *App) phaseTone(ph app.Phase) (tone, string) { return phaseToneIn(a.lang, ph) }

// phaseToneIn is phaseTone in a language, for the places without a window.
func phaseToneIn(lang string, ph app.Phase) (tone, string) {
	switch ph {
	case app.PhaseConnected:
		return toneGood, translate(lang, "phase.connected")
	case app.PhaseConnecting:
		return toneBlue, translate(lang, "phase.connecting")
	case app.PhaseReconnecting:
		return toneWarn, translate(lang, "phase.reconnecting")
	case app.PhaseDisconnecting:
		return toneBlue, translate(lang, "phase.disconnecting")
	case app.PhaseFailed:
		return toneBad, translate(lang, "phase.failed")
	case app.PhaseNoAgent:
		return toneWarn, translate(lang, "phase.noagent")
	}
	return toneNeutral, translate(lang, "phase.disconnected")
}

// phaseHint is a one-line explanation of a phase.
func (a *App) phaseHint(ph app.Phase) string {
	switch ph {
	case app.PhaseNoAgent:
		return a.t("phase.noagent.hint")
	case app.PhaseFailed:
		return a.t("phase.failed.hint")
	case app.PhaseConnecting, app.PhaseDisconnecting, app.PhaseReconnecting:
		return a.t("phase.working.hint")
	}
	return a.t("phase.disconnected.hint")
}

// pageHost shows the current page with a short entrance.
func (a *App) pageHost(c *ui.Context) {
	host := ui.Column(c.Key(int(a.page))).Fill().Transition(ui.ElementTransition{
		Enter:     &ui.Motion{Y: 10},
		Animation: &ui.Animation{Duration: 240 * time.Millisecond, Ease: ui.EaseOut},
	})
	host.Children(func() {
		switch a.page {
		case pageDashboard:
			a.dashboardPage(c)
		case pageNodes:
			a.nodesPage(c)
		case pageSubscriptions:
			a.subscriptionsPage(c)
		case pageRules:
			a.rulesPage(c)
		case pageConnections:
			a.connectionsPage(c)
		case pageLogs:
			a.logsPage(c)
		case pageSettings:
			a.settingsPage(c)
		}
	})
}

// pageFrame lays out a page: a title bar zone, a header and a scrolling body.
func (a *App) pageFrame(c *ui.Context, title, subtitle string, actions func(), body func()) {
	top := a.bar.Height
	if top < 28 {
		top = 28
	}
	ui.Row(c).Height(top+76).Padding(top-6, pagePadding+a.bar.Right, 0, pagePadding).Gap(16).AlignItems(ui.Center).DragWindow().Children(func() {
		ui.Column(c).Grow(1).Gap(3).Children(func() {
			a.title(c, title)
			if subtitle != "" {
				a.txt(c, subtitle, 13, 400, a.pl.Text2).SingleLine()
			}
		})
		if actions != nil {
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(actions)
		}
	})
	scroll := ui.Scroll(c.Key("scroll")).Grow(1).Padding(6, pagePadding, pagePadding, pagePadding)
	scroll.TrackScroll(a.scrollFor(a.page)).Children(func() {
		ui.Column(c).Gap(18).Children(body)
	})
}

// fixedFrame is pageFrame for pages whose body manages its own scrolling.
func (a *App) fixedFrame(c *ui.Context, title, subtitle string, actions func(), body func()) {
	top := a.bar.Height
	if top < 28 {
		top = 28
	}
	ui.Row(c).Height(top+76).Padding(top-6, pagePadding+a.bar.Right, 0, pagePadding).Gap(16).AlignItems(ui.Center).DragWindow().Children(func() {
		ui.Column(c).Grow(1).Gap(3).Children(func() {
			a.title(c, title)
			if subtitle != "" {
				a.txt(c, subtitle, 13, 400, a.pl.Text2).SingleLine()
			}
		})
		if actions != nil {
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(actions)
		}
	})
	ui.Column(c).Grow(1).Padding(6, pagePadding, pagePadding, pagePadding).Gap(14).Children(body)
}
