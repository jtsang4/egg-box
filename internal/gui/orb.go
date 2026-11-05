package gui

import (
	"math"
	"time"

	"github.com/egoist/mygo/ui"

	"eggbox/internal/app"
)

// orbLook is how the orb appears in a phase; the orb eases between looks.
type orbLook struct {
	glow    float32 // brightness of the halo, 0..1
	ripples float32 // visibility of the outgoing rings
	spin    float32 // visibility of the spinning arc
	ring    float32 // visibility of the full conic ring
	hue     tone    // colour family of the core
	icon    string
}

func orbLookOf(ph app.Phase) orbLook {
	switch ph {
	case app.PhaseConnected:
		return orbLook{glow: 1, ripples: 1, ring: 1, hue: toneBlue, icon: "power"}
	case app.PhaseConnecting, app.PhaseDisconnecting:
		return orbLook{glow: 0.7, spin: 1, hue: toneBlue, icon: "power"}
	case app.PhaseReconnecting:
		return orbLook{glow: 0.6, spin: 1, hue: toneWarn, icon: "power"}
	case app.PhaseFailed:
		return orbLook{glow: 0.45, hue: toneBad, icon: "power"}
	case app.PhaseNoAgent:
		return orbLook{glow: 0.3, hue: toneWarn, icon: "lock"}
	}
	return orbLook{glow: 0.22, hue: toneNeutral, icon: "power"}
}

// orb is the hero control: a luminous disc that connects and disconnects.
// It draws itself, so that its halo, ripples and ring can move without the
// view running again.
func (a *App) orb(c *ui.Context, size float32) ui.Element {
	p := a.pl
	ph := a.snap.Phase
	look := orbLookOf(ph)
	b := ui.ButtonBase(c.Key("orb")).Size(size, size).Radius(size / 2).Cursor(ui.CursorPointer)
	b.Label(a.t("orb.label"))
	anim := ui.Animation{Duration: 700 * time.Millisecond, Ease: ui.EaseOut}
	glow := b.AnimateTo("glow", look.glow, anim)
	ripples := b.AnimateTo("ripples", look.ripples, anim)
	spin := b.AnimateTo("spin", look.spin, ui.Animation{Duration: 400 * time.Millisecond})
	ring := b.AnimateTo("ring", look.ring, anim)
	hover := b.AnimateTo("hover", boolf(b.Hovered()), ui.Animation{Duration: 200 * time.Millisecond})
	hue := look.hue
	// The core's two colours, mixed across hue changes.
	topT := b.AnimateColor("top", a.orbTop(hue), anim)
	botT := b.AnimateColor("bot", a.orbBottom(hue), anim)
	haloC := b.AnimateColor("halo", a.orbHalo(hue), anim)
	pose := ui.Pose{}
	switch {
	case b.Pressed():
		pose = ui.Pose{Scale: 0.965}
	case b.Hovered():
		pose = ui.Pose{Scale: 1.02}
	}
	b.AnimatePose(pose, ui.Snappy)
	iconName := look.icon

	b.Draw(func(pt *ui.Painter, r ui.Rect) {
		// The halo breathes and the rings drift slowly: that looks the same at
		// half the display's rate for half of the power, which a window that
		// sits open all day should not waste. Only the spinning arc, which
		// turns fast, asks for the smoother frames.
		if spin > 0.01 {
			pt.AnimationFrameRate(60)
		} else {
			pt.AnimationFrameRate(30)
		}
		t := float64(pt.Now().UnixMilli()) / 1000
		cx, cy := r.X+r.W/2, r.Y+r.H/2
		core := r.W * 0.27

		// Halo: a soft disc of light that breathes.
		breathe := float32(1 + 0.035*math.Sin(t*1.3))
		hr := r.W * 0.5 * breathe
		pt.FillRadialGradient(ui.Rect{X: cx - hr, Y: cy - hr, W: 2 * hr, H: 2 * hr}, ui.RadialGradient{
			Radius: hr,
			Stops: []ui.GradientStop{
				{Color: haloC.Alpha(0.5 * glow), At: 0.3},
				{Color: haloC.Alpha(0.16 * glow), At: 0.62},
				{Color: haloC.Alpha(0), At: 1},
			},
		}, 0)

		// Ripples: rings leaving the core and fading.
		if ripples > 0.01 {
			const period = 3.6
			for i := 0; i < 3; i++ {
				ph := math.Mod(t/period+float64(i)/3, 1)
				e := float32(1 - math.Pow(1-ph, 2.2))
				rad := lerp(core*1.18, r.W*0.5-2, e)
				alpha := float32(math.Pow(1-ph, 1.7)) * 0.42 * ripples
				pt.StrokePath((&ui.Path{}).Circle(cx, cy, rad), 1.4, p.BlueHi.Alpha(alpha))
			}
		}

		// The track and the conic ring.
		ringR := core + (r.W*0.5-core)*0.34
		pt.StrokePath((&ui.Path{}).Circle(cx, cy, ringR), 3, p.LineHi.Alpha(0.55*(1-ring*0.6)))
		if ring > 0.01 {
			rot := float32(math.Mod(t*24, 360))
			g := ui.ConicGradient{Angle: rot, Stops: []ui.GradientStop{
				{Color: p.Cyan.Alpha(ring), At: 0},
				{Color: p.BlueHi.Alpha(ring), At: 0.35},
				{Color: p.Blue.Alpha(0.35 * ring), At: 0.7},
				{Color: p.Cyan.Alpha(ring), At: 1},
			}}
			pt.StrokePathConicGradient((&ui.Path{}).Circle(cx, cy, ringR), 3.2, g)
		}
		if spin > 0.01 {
			start := float32(math.Mod(t*2.4, 2*math.Pi))
			g := ui.ConicGradient{Angle: float32(math.Mod(t*2.4, 2*math.Pi)) * 180 / math.Pi, Stops: []ui.GradientStop{
				{Color: haloC.Alpha(0), At: 0},
				{Color: haloC.Alpha(0), At: 0.45},
				{Color: p.BlueHi.Alpha(spin), At: 0.97},
				{Color: p.BlueHi.Alpha(0), At: 1},
			}}
			_ = start
			pt.StrokePathConicGradient((&ui.Path{}).Circle(cx, cy, ringR), 3.6, g)
		}

		// The core.
		cr := core * (1 + 0.02*hover)
		coreRect := ui.Rect{X: cx - cr, Y: cy - cr, W: 2 * cr, H: 2 * cr}
		pt.Shadow(coreRect, cr, 0, 12, 34, -4, haloC.Alpha(0.55*glow+0.1))
		pt.Shadow(coreRect, cr, 0, 2, 6, 0, ui.RGBA(0, 0, 0, 0.35))
		pt.FillGradient(coreRect, ui.LinearGradient{From: topT, To: botT, Angle: 160, Oklab: true}, cr)
		// Specular highlight, upper left.
		pt.Clip(coreRect, cr, func() {
			pt.FillRadialGradient(coreRect, ui.RadialGradient{
				Center: ui.Point{X: 0.34, Y: 0.2}, Radius: cr * 1.25,
				Stops: []ui.GradientStop{
					{Color: ui.RGBA(255, 255, 255, 0.34), At: 0},
					{Color: ui.RGBA(255, 255, 255, 0.06), At: 0.55},
					{Color: ui.RGBA(255, 255, 255, 0), At: 1},
				},
			}, 0)
		})
		pt.Stroke(coreRect, ui.RGBA(255, 255, 255, 0.28), cr, 1.2)

		isz := cr * 0.72
		pt.Icon(icon(iconName), ui.Rect{X: cx - isz/2, Y: cy - isz/2, W: isz, H: isz}, ui.Hex("#FFFFFF"))
	})
	return b
}

func (a *App) orbTop(t tone) ui.Color {
	p := a.pl
	switch t {
	case toneBlue:
		return p.BlueHi.Mix(p.Cyan, 0.15)
	case toneWarn:
		return ui.Hex("#FFC85A")
	case toneBad:
		return ui.Hex("#FF8C98")
	}
	if p.Dark {
		return ui.Hex("#4A5C86")
	}
	return ui.Hex("#9FB0D4")
}

func (a *App) orbBottom(t tone) ui.Color {
	p := a.pl
	switch t {
	case toneBlue:
		return p.BlueLo
	case toneWarn:
		return ui.Hex("#D9811A")
	case toneBad:
		return ui.Hex("#D43A56")
	}
	if p.Dark {
		return ui.Hex("#222E4C")
	}
	return ui.Hex("#6E80A8")
}

func (a *App) orbHalo(t tone) ui.Color {
	p := a.pl
	switch t {
	case toneBlue:
		return p.Blue
	case toneWarn:
		return ui.Hex("#FFB02E")
	case toneBad:
		return ui.Hex("#FF5470")
	}
	return p.BlueLo.Mix(p.Text3, 0.5)
}
