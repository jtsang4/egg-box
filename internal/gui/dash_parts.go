package gui

import (
	"math"
	"time"

	"github.com/egoist/mygo/ui"

	"eggbox/internal/app"
	"eggbox/internal/library"
)

// usageRow shows a subscription's quota: a ring, what was used, when it ends.
func (a *App) usageRow(c *ui.Context, key, name string, s library.Subscription) {
	p := a.pl
	u := s.Usage
	frac, hasQuota := 0.0, u != nil && u.Total > 0
	if hasQuota {
		frac = float64(u.Used()) / float64(u.Total)
	}
	t := toneBlue
	switch {
	case frac >= 0.95:
		t = toneBad
	case frac >= 0.8:
		t = toneWarn
	}
	ui.Row(c.Key("usage-" + key)).Gap(14).AlignItems(ui.Center).Children(func() {
		if hasQuota {
			a.ring(c, key, frac, 54, 6, t, fmtPercent(frac))
		} else {
			a.iconTile(c, "database", toneNeutral, 54)
		}
		ui.Column(c).Grow(1).Basis(0).Gap(3).Children(func() {
			if name != "" {
				a.txt(c, name, 14, 650, p.Text).SingleLine()
			}
			switch {
			case hasQuota:
				a.txt(c, a.t("usage.of", "used", fmtBytes(float64(u.Used())), "total", fmtBytes(float64(u.Total))), 12.5, 400, p.Text2).SingleLine().FontFeatures("tnum")
			case u != nil:
				a.txt(c, a.t("usage.used", "used", fmtBytes(float64(u.Used()))), 12.5, 400, p.Text2).SingleLine().FontFeatures("tnum")
			}
			if u != nil && !u.Expire.IsZero() {
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, icon("clock")).Size(12, 12).TextColor(p.Text3)
					exp := a.until(u.Expire)
					col := p.Text3
					if days := u.Expire.Sub(a.now).Hours() / 24; days < 7 {
						col = p.Warn
					}
					if !u.Expire.After(a.now) {
						col = p.Bad
					}
					a.txt(c, exp, 12, 500, col).SingleLine()
				})
			}
		})
	})
}

// ring is a circular gauge with a label in its middle.
func (a *App) ring(c *ui.Context, key string, frac float64, size, thick float32, t tone, label string) ui.Element {
	p := a.pl
	e := ui.Box(c.Key("ring-"+key)).Size(size, size)
	f := e.AnimateTo("f", float32(clamp01(frac)), ui.Animation{Duration: 900 * time.Millisecond, Ease: ui.EaseOut})
	col := a.toneColor(t)
	if t == toneBlue {
		col = p.Blue
	}
	e.Draw(func(pt *ui.Painter, r ui.Rect) {
		cx, cy := r.X+r.W/2, r.Y+r.H/2
		rad := r.W/2 - thick/2
		pt.StrokePath((&ui.Path{}).Circle(cx, cy, rad), thick, p.Line.Alpha(1.4))
		if f > 0.002 {
			arc := arcPath(cx, cy, rad, 0, 2*math.Pi*float32(f))
			g := ui.LinearGradient{From: col, To: col.Mix(p.Cyan, boolf(t == toneBlue)*0.8), Angle: 180}
			pt.StrokePathGradient(arc, thick, g)
		}
		w, h := pt.MeasureText(0, ui.Span{Text: label, Size: size * 0.215, Weight: 650})
		pt.RichText(cx-w/2, cy-h/2, 0, ui.Span{Text: label, Size: size * 0.215, Weight: 650, Color: p.Text})
	})
	return e
}

// activityCard lists what the service reported lately.
func (a *App) activityCard(c *ui.Context) {
	p := a.pl
	notices := a.snap.Notices
	a.card(c).Gap(6).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Padding(0, 0, 8, 0).Children(func() {
			a.heading(c, a.t("dash.activity")).Grow(1)
			if n := len(notices); n > 0 {
				a.caption(c, a.tn("dash.activity.count", n))
			}
		})
		if len(notices) == 0 {
			ui.Row(c).Gap(12).Padding(10, 0).AlignItems(ui.Center).Children(func() {
				a.iconTile(c, "sparkles", toneNeutral, 34)
				a.txt(c, a.t("dash.activity.empty"), 13, 400, p.Text2)
			})
			return
		}
		shown := 0
		for i := len(notices) - 1; i >= 0 && shown < 6; i-- {
			n := notices[i]
			if shown > 0 {
				a.hr(c)
			}
			shown++
			a.noticeRow(c, n)
		}
	})
}

func (a *App) noticeRow(c *ui.Context, n app.Notice) {
	p := a.pl
	t, ic := toneBlue, "info"
	switch n.Level {
	case app.LevelSuccess:
		t, ic = toneGood, "check-circle"
	case app.LevelWarning:
		t, ic = toneWarn, "alert"
	case app.LevelError:
		t, ic = toneBad, "alert-circle"
	}
	ui.Row(c.Key(n.ID)).Gap(12).Padding(11, 0).AlignItems(ui.Center).Children(func() {
		a.iconTile(c, ic, t, 30)
		ui.Column(c).Grow(1).Basis(0).Gap(2).Children(func() {
			a.txt(c, a.noticeTitle(n), 13, 550, p.Text).SingleLine()
			if body := a.noticeBody(n); body != "" {
				a.txt(c, body, 12, 400, p.Text3).SingleLine()
			}
		})
		a.txt(c, a.clock(n.Time), 12, 500, p.Text3).FontFeatures("tnum").SingleLine()
	})
}
