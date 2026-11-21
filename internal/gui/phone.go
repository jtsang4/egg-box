package gui

import (
	"time"

	"github.com/egoist/mygo/ui"
)

// On a phone the same pages show one at a time under a bar of tabs at the
// bottom, where a thumb reaches, instead of beside a sidebar. Seven pages
// share five tabs: the nodes and the subscriptions they come from are one
// tab, and so are the connections and the log, each pair switched at the top
// of the page. Everything below the frame is the desktop's code, laid out
// for a narrow screen where it needs to be (see compact).

// phoneTab is one tab of the bar.
type phoneTab struct {
	icon, key string
	pages     []page
}

var phoneTabs = []phoneTab{
	{"home", "tab.home", []page{pageDashboard}},
	{"globe", "tab.nodes", []page{pageNodes, pageSubscriptions}},
	{"route", "tab.routing", []page{pageRouting}},
	{"activity", "tab.activity", []page{pageConnections, pageLogs}},
	{"settings", "tab.settings", []page{pageSettings}},
}

const (
	// tabBarH is the height of the bar of tabs, above the system's own bar.
	tabBarH = 58
	// phonePad is the margin of a page on a phone.
	phonePad = 16
)

// compact reports whether the interface is laid out for a phone.
func (a *App) compact() bool { return a.opt.Phone }

// phoneShell is the whole screen of a phone: the page, the bar of tabs, and
// the dialogs and toasts over them.
func (a *App) phoneShell(c *ui.Context) {
	p := a.pl
	_, _, bottom, _ := c.SafeArea()
	r := c.Root()
	r.Background(p.Window)
	// The root covers the whole screen, under the system's bars: the bar of
	// tabs continues under the home indicator or the navigation bar.
	r.Draw(func(pt *ui.Painter, rc ui.Rect) {
		a.ambient(pt, rc)
		band := bottom + tabBarH
		pt.Fill(ui.Rect{X: rc.X, Y: rc.Y + rc.H - band, W: rc.W, H: band}, a.tabBarColor(), 0)
		pt.Fill(ui.Rect{X: rc.X, Y: rc.Y + rc.H - band, W: rc.W, H: 1}, p.Line, 0)
	})
	ui.Column(c).Fill().Clip().Children(func() {
		ui.Column(c).Grow(1).Basis(0).MinHeight(0).Clip().Children(func() {
			a.pageHost(c)
		})
		a.tabBar(c)
		a.dialogLayer(c)
		a.toastLayer(c)
	})
}

func (a *App) tabBarColor() ui.Color {
	p := a.pl
	if p.Dark {
		return p.Sidebar.Alpha(0.96)
	}
	return p.Raised.Alpha(0.96)
}

// tabIndex is the tab of a page.
func tabIndex(pg page) int {
	for i, t := range phoneTabs {
		for _, q := range t.pages {
			if q == pg {
				return i
			}
		}
	}
	return 0
}

// tabBar is the row of tabs.
func (a *App) tabBar(c *ui.Context) {
	cur := tabIndex(a.page)
	bar := ui.Row(c.Key("tab-bar")).Height(tabBarH).Padding(4, 6, 2, 6).AlignItems(ui.Stretch)
	bar.Role(ui.RoleTabList)
	bar.Children(func() {
		for i, t := range phoneTabs {
			a.tabButton(c, i, t, i == cur)
		}
	})
}

func (a *App) tabButton(c *ui.Context, i int, t phoneTab, active bool) {
	p := a.pl
	b := ui.ButtonBase(c.Key("tab-" + t.key))
	b.Label(a.t(t.key)).Role(ui.RoleTab).AccessSelected(active)
	b.Grow(1).Basis(0).AlignItems(ui.Stretch)
	on := b.AnimateTo("on", boolf(active), ui.Animation{Duration: 180 * time.Millisecond, Ease: ui.EaseOut})
	col := p.Text3.Mix(p.BlueText, on)
	if p.Dark {
		col = p.Text3.Mix(p.BlueHi, on)
	}
	b.TextColor(col)
	b.Children(func() {
		ui.Column(c).Grow(1).Gap(3).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
			// The active tab's icon sits on a soft pill, as Android's bars show it.
			pill := ui.Box(c).Width(54).Height(28).Radius(radiusPill).Center()
			pill.Background(p.Blue.Alpha(boolOr[float32](p.Dark, 0.26, 0.13) * on))
			pill.Children(func() {
				ui.Icon(c, icon(t.icon)).Size(21, 21)
			})
			ui.Text(c, a.t(t.key)).FontSize(11).FontWeight(boolOr(active, 650, 520)).SingleLine()
		})
	})
	if b.Clicked() {
		pages := phoneTabs[i].pages
		target := pages[0]
		if active {
			// A second tap on the tab goes back to its first page, at its top.
			if a.page == target {
				a.scrollPageToTop(a.page)
			}
		} else if a.lastOf[i] != 0 {
			target = a.lastOf[i] - 1
		}
		a.goTo(target)
	}
}

// scrollPageToTop scrolls a phone page back to its top, whether it scrolls
// through pageFrame or a virtualized List of its own.
func (a *App) scrollPageToTop(pg page) {
	switch pg {
	case pageNodes:
		a.node.list.ScrollTo(0, ui.Start)
	case pageConnections:
		a.conn.list.ScrollTo(0, ui.Start)
	case pageLogs:
		a.logs.list.ScrollTo(0, ui.Start)
	default:
		a.scrollFor(pg).ScrollTo(0, 0, ui.Animation{Duration: 300 * time.Millisecond, Ease: ui.EaseOut})
	}
}

// rememberTab notes the page shown last in its tab, which the tab opens on.
func (a *App) rememberTab() {
	a.lastOf[tabIndex(a.page)] = a.page + 1
}

// phoneFrame is pageFrame on a phone: the title with the page's actions,
// what switches between the pages of a tab, and the body, which scrolls when
// scroll is set.
func (a *App) phoneFrame(c *ui.Context, title, subtitle string, actions, body func(), scroll bool) {
	p := a.pl
	ui.Column(c).Padding(12, phonePad, 6, phonePad).Gap(10).Children(func() {
		ui.Row(c).Gap(10).AlignItems(ui.Center).MinHeight(40).Children(func() {
			ui.Column(c).Grow(1).Basis(0).Gap(2).Children(func() {
				a.txt(c, title, 24, 700, p.Text).LetterSpacing(-0.5).SingleLine()
				if subtitle != "" {
					a.txt(c, subtitle, 12.5, 400, p.Text2).SingleLine()
				}
			})
		})
		a.siblingSwitch(c)
		if actions != nil {
			ui.Row(c).Gap(8).Wrap().AlignItems(ui.Center).Children(actions)
		}
	})
	if !scroll {
		ui.Column(c).Grow(1).Basis(0).MinHeight(0).Padding(4, phonePad, phonePad, phonePad).Gap(12).Children(body)
		return
	}
	sc := ui.Scroll(c.Key("scroll")).Grow(1).Basis(0).MinHeight(0).Padding(4, phonePad, phonePad, phonePad)
	sc.TrackScroll(a.scrollFor(a.page)).Children(func() {
		ui.Column(c).Gap(14).Children(body)
	})
}

// siblingSwitch switches between the pages that share a tab.
func (a *App) siblingSwitch(c *ui.Context) {
	t := phoneTabs[tabIndex(a.page)]
	if len(t.pages) < 2 {
		return
	}
	items := make([]segItem, len(t.pages))
	sel := 0
	for i, pg := range t.pages {
		items[i] = segItem{label: a.t(pageNavKey(pg))}
		if pg == a.page {
			sel = i
		}
	}
	w, _ := c.Size()
	if a.segmented(c, "siblings", &sel, items, segOpts{fill: true, width: w - 2*phonePad - 8}) {
		a.goTo(t.pages[sel])
	}
}

// pageNavKey is the translation key of a page's name.
func pageNavKey(pg page) string {
	for _, it := range navMain {
		if it.page == pg {
			return it.key
		}
	}
	for _, it := range navMonitor {
		if it.page == pg {
			return it.key
		}
	}
	return "nav.settings"
}
