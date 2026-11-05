package gui

import (
	"github.com/egoist/mygo/ui"
)

// state of the interface that no backend keeps: what is typed, filtered,
// scrolled or open.

func (a *App) initState() {
	a.initNodes()
	a.initConns()
	a.initLogs()
	a.initRouting()
}

// scrollFor returns the scroll state of a page, so that each page keeps its
// own place.
func (a *App) scrollFor(p page) *ui.ScrollState {
	if a.scrolls == nil {
		a.scrolls = make([]ui.ScrollState, pageCount)
	}
	return &a.scrolls[p]
}

// jumpOnChange scrolls a list back to its first row when what it shows was
// changed by a filter or a search: the list would otherwise keep the row it
// was looking at, far from the top of the new result.
func jumpOnChange(l *ui.ListState, prev *string, sig string) {
	if *prev == sig {
		return
	}
	*prev = sig
	l.ScrollTo(0, ui.Start)
}

// ---------------------------------------------------------------- dialogs

// dialog is a modal window of the application.
type dialog interface {
	// frame describes the dialog; it runs every frame while it is open.
	frame(a *App, c *ui.Context) dlgFrame
}

// dlgFrame is the standard shell of a dialog.
type dlgFrame struct {
	title    string
	subtitle string
	icon     string
	tone     tone
	width    float32
	// hero is artwork across the top of the panel; with a hero the standard
	// header is only drawn when there is a title.
	hero    func()
	body    func()
	buttons func()
}

// openDialog shows a dialog in place of whatever was open.
func (a *App) openDialog(d dialog) {
	a.dlgStack = nil
	a.dlg = d
	a.dlgOpen = true
	a.again()
}

// pushDialog shows a dialog over the open one; closing it comes back to that
// one, with whatever the person typed there still in place.
func (a *App) pushDialog(d dialog) {
	if a.dlg != nil {
		a.dlgStack = append(a.dlgStack, a.dlg)
	}
	a.dlg = d
	a.dlgOpen = true
	a.again()
}

// closeDialog closes the open dialog: the one under it comes back, if there
// is one.
func (a *App) closeDialog() {
	if n := len(a.dlgStack); n > 0 {
		a.dlg, a.dlgStack = a.dlgStack[n-1], a.dlgStack[:n-1]
		a.dlgOpen = true
		a.again()
		return
	}
	a.dlg = nil
	a.dlgOpen = false
	a.again()
}

// dialogLayer draws the open dialog over the window.
func (a *App) dialogLayer(c *ui.Context) {
	if a.dlg == nil {
		return
	}
	p := a.pl
	ui.DialogBase(c, &a.dlgOpen, func(backdrop, panel ui.Element) {
		f := a.dlg.frame(a, c)
		backdrop.Background(ui.RGBA(3, 7, 20, boolOr[float32](p.Dark, 0.62, 0.38)))
		w := f.width
		if w == 0 {
			w = 460
		}
		panel.Width(w).MaxWidthPercent(94).Radius(24).Background(p.Raised).Border(1, p.LineHi).Padding(0).Clip()
		panel.Shadow(0, 40, 90, -20, ui.RGBA(0, 0, 0, 0.6)).Shadow(0, 8, 24, -8, ui.RGBA(0, 0, 0, 0.3))
		if p.Dark {
			panel.InsetShadow(0, 1, 0, 0, ui.RGBA(255, 255, 255, 0.08))
		}
		panel.Transition(ui.ElementTransition{
			Enter:     &ui.Motion{Y: 16, Scale: 0.97},
			Animation: &ui.Snappy,
		})
		ui.Column(c).Gap(0).Children(func() {
			if f.hero != nil {
				f.hero()
			}
			// Header.
			if f.hero == nil || f.title != "" {
				ui.Row(c).Gap(14).Padding(24, 24, 8, 24).AlignItems(ui.Center).Children(func() {
					if f.icon != "" {
						a.iconTile(c, f.icon, f.tone, 42)
					}
					ui.Column(c).Grow(1).Gap(3).Children(func() {
						a.txt(c, f.title, 18, 700, p.Text).LetterSpacing(-0.3)
						if f.subtitle != "" {
							a.txt(c, f.subtitle, 13, 400, p.Text2).LineHeight(1.4)
						}
					})
					if a.btn(c, btnSpec{kind: btnGhost, icon: "x", small: true, iconOnly: true, tip: a.t("common.close")}).Clicked() {
						a.dlgOpen = false
					}
				})
			}
			ui.Column(c).Padding(10, 24, 8, 24).Gap(14).Children(f.body)
			if f.buttons != nil {
				ui.Row(c).Padding(14, 24, 20, 24).Gap(10).Justify(ui.End).AlignItems(ui.Center).Children(f.buttons)
			}
		})
	})
	if !a.dlgOpen {
		a.closeDialog()
	}
}

// confirmDialog asks before something that cannot be undone.
type confirmDialog struct {
	title, body, confirm string
	icon                 string
	tone                 tone
	danger               bool
	onConfirm            func()
}

func (d *confirmDialog) frame(a *App, c *ui.Context) dlgFrame {
	kind := btnPrimary
	if d.danger {
		kind = btnDanger
	}
	return dlgFrame{
		title: d.title, icon: d.icon, tone: d.tone, width: 440,
		body: func() { a.txt(c, d.body, 13.5, 400, a.pl.Text2).LineHeight(1.55) },
		buttons: func() {
			if a.btn(c, btnSpec{kind: btnGhost, label: a.t("common.cancel")}).Clicked() {
				a.dlgOpen = false
			}
			if a.btn(c, btnSpec{kind: kind, label: d.confirm}).Clicked() {
				a.dlgOpen = false
				if d.onConfirm != nil {
					d.onConfirm()
				}
			}
		},
	}
}

// confirm opens a confirmation.
func (a *App) confirm(title, body, label string, danger bool, onConfirm func()) {
	ic, tn := "alert", toneWarn
	if danger {
		ic, tn = "trash", toneBad
	}
	a.openDialog(&confirmDialog{title: title, body: body, confirm: label, icon: ic, tone: tn, danger: danger, onConfirm: onConfirm})
}
