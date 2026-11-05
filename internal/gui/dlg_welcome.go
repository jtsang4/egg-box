package gui

import (
	"math"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/jtsang4/sing-box-ui/internal/app"
)

// onboardState tracks the first-run introduction.
type onboardState struct {
	step    int
	checked bool // the first-run check ran in this session
}

const welcomeSteps = 3

// maybeWelcome opens the introduction the first time the application runs.
func (a *App) maybeWelcome() {
	if a.boot.checked || a.be == nil {
		return
	}
	a.boot.checked = true
	if !a.set.OnboardingDone && a.dlg == nil {
		a.openWelcome()
	}
}

// openWelcome starts the introduction from its first step.
func (a *App) openWelcome() {
	a.boot.step = 0
	a.openDialog(&welcomeDialog{})
}

// finishWelcome records that the introduction was seen and closes it.
func (a *App) finishWelcome() {
	if !a.set.OnboardingDone {
		a.updateSettings(func(s *settingsT) { s.OnboardingDone = true })
	}
	a.dlgOpen = false
}

// welcomeDialog is the three-step introduction: what the application is,
// the one permission it needs, and the first subscription.
type welcomeDialog struct{}

func (d *welcomeDialog) frame(a *App, c *ui.Context) dlgFrame {
	step := a.boot.step
	return dlgFrame{
		width: 560,
		hero:  func() { a.welcomeHero(c, step) },
		body: func() {
			// The steps share a height, so that the panel does not jump.
			ui.Column(c).MinHeight(268).Gap(14).Children(func() {
				switch step {
				case 0:
					a.welcomeIntro(c)
				case 1:
					a.welcomeService(c)
				default:
					a.welcomeSubscription(c)
				}
			})
		},
		buttons: func() { a.welcomeButtons(c, step) },
	}
}

// welcomeHero is the artwork: the mark on a field of light and slow rings,
// and the progress of the steps beneath it.
func (a *App) welcomeHero(c *ui.Context, step int) {
	p := a.pl
	h := ui.Column(c.Key("welcome-hero")).Height(212).Justify(ui.Center).AlignItems(ui.Center).Gap(18).Clip()
	h.Draw(func(pt *ui.Painter, r ui.Rect) {
		pt.AnimationFrameRate(30)
		t := float64(pt.Now().UnixMilli()) / 1000
		cx, cy := r.X+r.W/2, r.Y+92
		// The field: deep blue at the top, easing into the panel.
		pt.FillGradient(r, ui.LinearGradient{
			From: p.Blue.Alpha(boolOr[float32](p.Dark, 0.26, 0.16)), To: p.Blue.Alpha(0), Angle: 180}, 0)
		big := r.W * 0.9
		pt.FillRadialGradient(ui.Rect{X: cx - big/2, Y: cy - big/2, W: big, H: big}, ui.RadialGradient{
			Radius: big / 2,
			Stops: []ui.GradientStop{
				{Color: p.BlueHi.Alpha(boolOr[float32](p.Dark, 0.42, 0.3)), At: 0},
				{Color: p.Blue.Alpha(0.1), At: 0.5},
				{Color: p.Blue.Alpha(0), At: 1},
			},
		}, 0)
		// Rings leaving the mark.
		const period = 5.0
		for i := 0; i < 4; i++ {
			ph := math.Mod(t/period+float64(i)/4, 1)
			rad := lerp(46, r.W*0.46, float32(1-math.Pow(1-ph, 2)))
			alpha := float32(math.Pow(1-ph, 1.6)) * 0.34
			pt.StrokePath((&ui.Path{}).Circle(cx, cy, rad), 1.2, p.BlueHi.Alpha(alpha))
		}
		// A quiet dot grid on both sides.
		for gx := 0; gx < 9; gx++ {
			for gy := 0; gy < 5; gy++ {
				x := r.X + 24 + float32(gx)*13
				y := r.Y + 24 + float32(gy)*13
				fade := float32(1) - float32(gx)/9
				pt.Fill(ui.Rect{X: x, Y: y, W: 2, H: 2}, p.Text.Alpha(0.07*fade), 1)
				pt.Fill(ui.Rect{X: r.X + r.W - x - 2, Y: y, W: 2, H: 2}, p.Text.Alpha(0.07*fade), 1)
			}
		}
	})
	h.Children(func() {
		a.logo(c, 76)
		a.stepDots(c, step)
	})
}

// stepDots shows the progress: the current step is a long pill.
func (a *App) stepDots(c *ui.Context, step int) {
	p := a.pl
	ui.Row(c.Key("welcome-dots")).Gap(6).AlignItems(ui.Center).Children(func() {
		for i := 0; i < welcomeSteps; i++ {
			d := ui.Box(c.Key("dot" + string(rune('0'+i)))).Height(6).Radius(3)
			w := d.AnimateTo("w", boolOr[float32](i == step, 22, 6), ui.Animation{Duration: 260 * time.Millisecond, Ease: ui.EaseOut})
			col := p.Line.Mix(p.LineHi, 0.6)
			if i <= step {
				col = p.BlueHi
			}
			d.Width(w).Background(d.AnimateColor("c", col, ui.Animation{Duration: 260 * time.Millisecond}))
		}
	})
}

// welcomeIntro is the first step.
func (a *App) welcomeIntro(c *ui.Context) {
	p := a.pl
	ui.Column(c).Gap(6).AlignItems(ui.Center).Children(func() {
		a.txt(c, a.t("welcome.title"), 24, 720, p.Text).LetterSpacing(-0.6)
		a.txt(c, a.t("welcome.body"), 13.5, 400, p.Text2).LineHeight(1.55).TextAlign(ui.Center)
	})
	ui.Column(c).Gap(10).Padding(8, 0, 4, 0).Children(func() {
		for _, f := range []struct{ icon, key string }{
			{"zap", "welcome.f1"}, {"layers", "welcome.f2"}, {"shield-check", "welcome.f3"},
		} {
			ui.Row(c).Gap(14).AlignItems(ui.Center).Padding(12, 14).Radius(16).Background(p.Field).Border(1, p.Line).Children(func() {
				a.iconTile(c, f.icon, toneBlue, 36)
				ui.Column(c).Grow(1).Basis(0).Gap(2).Children(func() {
					a.txt(c, a.t(f.key+".title"), 13.5, 650, p.Text)
					a.txt(c, a.t(f.key+".body"), 12.5, 400, p.Text3).LineHeight(1.4)
				})
			})
		}
	})
}

// serviceAllowed reports whether the service can already be used.
func (a *App) serviceAllowed() bool { return a.snap.Phase != app.PhaseNoAgent }

// welcomeService explains the one permission the application asks for.
func (a *App) welcomeService(c *ui.Context) {
	p := a.pl
	ok := a.serviceAllowed()
	ui.Column(c).Gap(6).AlignItems(ui.Center).Children(func() {
		a.txt(c, a.t("welcome.service.title"), 22, 720, p.Text).LetterSpacing(-0.5)
		a.txt(c, a.pk("welcome.service.body"), 13.5, 400, p.Text2).LineHeight(1.55).TextAlign(ui.Center)
	})
	tn, ic, title, body := toneWarn, "lock", a.t("welcome.service.pending"), a.pk("welcome.service.how")
	if ok {
		tn, ic, title, body = toneGood, "check-circle", a.t("welcome.service.ready"), a.t("welcome.service.ready.body")
	}
	ui.Row(c.Key("welcome-service")).Gap(14).AlignItems(ui.Center).Padding(16, 16).Radius(18).
		Background(a.toneFill(tn)).Border(1, a.toneColor(tn).Alpha(0.3)).Children(func() {
		a.iconTile(c, ic, tn, 40)
		ui.Column(c).Grow(1).Basis(0).Gap(3).Children(func() {
			a.txt(c, title, 14, 680, p.Text)
			a.txt(c, body, 12.5, 400, p.Text2).LineHeight(1.45)
		})
	})
	ui.Row(c).Gap(8).AlignItems(ui.Center).Padding(2, 4).Children(func() {
		ui.Icon(c, icon("lock")).Size(13, 13).TextColor(p.Text3)
		a.txt(c, a.t("welcome.service.note"), 12, 400, p.Text3).Grow(1).Basis(0).LineHeight(1.4)
	})
}

// welcomeSubscription is the last step.
func (a *App) welcomeSubscription(c *ui.Context) {
	p := a.pl
	ui.Column(c).Gap(6).AlignItems(ui.Center).Children(func() {
		a.txt(c, a.t("welcome.sub.title"), 22, 720, p.Text).LetterSpacing(-0.5)
		a.txt(c, a.t("welcome.sub.body"), 13.5, 400, p.Text2).LineHeight(1.55).TextAlign(ui.Center)
	})
	ui.Row(c).Gap(12).AlignItems(ui.Stretch).Children(func() {
		for _, o := range []struct{ icon, key string }{
			{"link", "welcome.sub.link"}, {"file-text", "welcome.sub.paste"},
		} {
			ui.Column(c).Grow(1).Basis(0).Gap(6).Padding(16, 16).Radius(18).Background(p.Field).Border(1, p.Line).Children(func() {
				a.iconTile(c, o.icon, toneBlue, 34)
				a.txt(c, a.t(o.key+".title"), 13.5, 650, p.Text).Margin(4, 0, 0, 0)
				a.txt(c, a.t(o.key+".body"), 12, 400, p.Text3).LineHeight(1.4)
			})
		}
	})
}

// welcomeButtons are the controls of a step.
func (a *App) welcomeButtons(c *ui.Context, step int) {
	grow := func() { ui.Box(c).Grow(1) }
	switch step {
	case 0:
		if a.btn(c, btnSpec{kind: btnGhost, label: a.t("welcome.skip")}).Clicked() {
			a.finishWelcome()
		}
		grow()
		if a.btn(c, btnSpec{kind: btnPrimary, label: a.t("welcome.next"), icon: "arrow-right"}).Clicked() {
			a.boot.step = 1
		}
	case 1:
		if a.btn(c, btnSpec{kind: btnGhost, label: a.t("welcome.back")}).Clicked() {
			a.boot.step = 0
		}
		grow()
		if !a.serviceAllowed() && a.opt.Authorize != nil {
			if a.btn(c, btnSpec{kind: btnGhost, label: a.t("welcome.later")}).Clicked() {
				a.boot.step = 2
			}
			if a.btn(c, btnSpec{kind: btnPrimary, label: a.t("hero.authorize"), icon: "shield-check", busy: a.busy("authorize")}).Clicked() {
				a.authorize()
			}
			return
		}
		if a.btn(c, btnSpec{kind: btnPrimary, label: a.t("welcome.next"), icon: "arrow-right"}).Clicked() {
			a.boot.step = 2
		}
	default:
		if a.btn(c, btnSpec{kind: btnGhost, label: a.t("welcome.back")}).Clicked() {
			a.boot.step = 1
		}
		grow()
		if a.btn(c, btnSpec{kind: btnGhost, label: a.t("welcome.later")}).Clicked() {
			a.finishWelcome()
		}
		if a.btn(c, btnSpec{kind: btnSecondary, label: a.t("subs.import"), icon: "file-text"}).Clicked() {
			a.finishWelcome()
			a.openImport()
		}
		if a.btn(c, btnSpec{kind: btnPrimary, label: a.t("subs.add"), icon: "plus"}).Clicked() {
			a.finishWelcome()
			a.openAddSubscription()
		}
	}
}
