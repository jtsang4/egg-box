package gui

import (
	"hash/fnv"
	"math"
	"time"

	"github.com/egoist/mygo/ui"
)

// tone is a semantic colour: what a chip, a dot or a badge means.
type tone int

const (
	toneNeutral tone = iota
	toneBlue
	toneGood
	toneWarn
	toneBad
)

// toneColor is the colour of a tone for text and glyphs on a surface.
func (a *App) toneColor(t tone) ui.Color {
	p := a.pl
	switch t {
	case toneBlue:
		return p.BlueText
	case toneGood:
		return p.Good
	case toneWarn:
		return p.Warn
	case toneBad:
		return p.Bad
	}
	return p.Text2
}

// toneFill is the tinted face behind a tone's text.
func (a *App) toneFill(t tone) ui.Color {
	if t == toneNeutral {
		return a.pl.Field
	}
	k := float32(0.14)
	if !a.pl.Dark {
		k = 0.12
	}
	return a.toneColor(t).Alpha(k)
}

// ------------------------------------------------------------------ text

// txt is a line of text in a size, weight and colour.
func (a *App) txt(c *ui.Context, s string, size float32, weight int, col ui.Color) ui.Element {
	return ui.Text(c, s).FontSize(size).FontWeight(weight).TextColor(col)
}

func (a *App) title(c *ui.Context, s string) ui.Element {
	return a.txt(c, s, 26, 700, a.pl.Text).LetterSpacing(-0.5).SingleLine()
}

func (a *App) heading(c *ui.Context, s string) ui.Element {
	return a.txt(c, s, 15, 650, a.pl.Text).LetterSpacing(-0.1).SingleLine()
}

func (a *App) caption(c *ui.Context, s string) ui.Element {
	return a.txt(c, s, 12, 400, a.pl.Text3)
}

// number is a figure in tabular numerals.
// rolling is how a live figure changes: the digits that differ roll over
// quickly, so that a number updated every second is at rest almost all of the
// time instead of always being half way between two values.
var rolling = ui.ContentTransition{
	Numeric:   true,
	Animation: &ui.Animation{Spring: ui.SpringOptions{Duration: 180 * time.Millisecond}},
}

func (a *App) number(c *ui.Context, s string, size float32, col ui.Color) ui.Element {
	return a.txt(c, s, size, 650, col).FontFeatures("tnum").LetterSpacing(-0.4).SingleLine()
}

// eyebrow is a small uppercase-ish label above a figure or a group.
func (a *App) eyebrow(c *ui.Context, s string) ui.Element {
	return a.txt(c, s, 11, 600, a.pl.Text3).LetterSpacing(0.6).SingleLine()
}

// --------------------------------------------------------------- surfaces

// card is the standard panel.
func (a *App) card(c *ui.Context) ui.Element {
	p := a.pl
	e := ui.Column(c).Padding(cardPadding).Radius(radiusCard).Background(p.Panel).Border(1, p.Line)
	if p.Dark {
		e.InsetShadow(0, 1, 0, 0, ui.RGBA(255, 255, 255, 0.055))
	} else {
		e.Shadow(0, 1, 2, 0, ui.RGBA(16, 40, 96, 0.05)).Shadow(0, 12, 28, -14, p.Shadow)
	}
	return e
}

func boolf(b bool) float32 {
	if b {
		return 1
	}
	return 0
}

// hr is a hairline across a row or column.
func (a *App) hr(c *ui.Context) ui.Element {
	return ui.Box(c).Height(1).Background(a.pl.Line)
}

// vr is a vertical hairline.
func (a *App) vr(c *ui.Context, h float32) ui.Element {
	return ui.Box(c).Width(1).Height(h).Background(a.pl.Line)
}

// ------------------------------------------------------------------ chips

// chip is a small pill carrying a status or a tag.
func (a *App) chip(c *ui.Context, s string, t tone, iconName string) ui.Element {
	col := a.toneColor(t)
	e := ui.Row(c).Gap(5).Padding(3, 9).Radius(radiusPill).Background(a.toneFill(t)).AlignItems(ui.Center)
	e.Children(func() {
		if iconName != "" {
			ui.Icon(c, icon(iconName)).Size(12, 12).TextColor(col)
		}
		a.txt(c, s, 11.5, 600, col).SingleLine()
	})
	return e
}

// dot is a status light.
func (a *App) dot(c *ui.Context, t tone, size float32, glow bool) ui.Element {
	col := a.toneColor(t)
	if t == toneNeutral {
		col = a.pl.Text3
	}
	e := ui.Box(c).Size(size, size).Radius(size / 2).Background(col)
	if glow {
		e.Shadow(0, 0, size*1.6, 0, col.Alpha(0.75))
	}
	return e
}

// iconTile is a rounded square holding an icon, tinted by a tone.
func (a *App) iconTile(c *ui.Context, name string, t tone, size float32) ui.Element {
	col := a.toneColor(t)
	e := ui.Box(c).Size(size, size).Radius(size * 0.3).Center()
	if t == toneNeutral {
		e.Background(a.pl.Field)
	} else {
		e.LinearGradient(ui.LinearGradient{From: col.Alpha(0.24), To: col.Alpha(0.1), Angle: 135})
		e.Border(1, col.Alpha(0.22))
	}
	e.Children(func() {
		ui.Icon(c, icon(name)).Size(size*0.5, size*0.5).TextColor(col)
	})
	return e
}

// ----------------------------------------------------------------- buttons

type btnKind int

const (
	btnPrimary btnKind = iota
	btnSecondary
	btnGhost
	btnDanger
	btnSoft
)

// btnSpec describes a button.
type btnSpec struct {
	kind     btnKind
	label    string
	icon     string
	busy     bool
	small    bool
	disabled bool
	tip      string
	grow     bool
	iconOnly bool
}

// btn is a button; test Clicked() on the result.
func (a *App) btn(c *ui.Context, o btnSpec) ui.Element {
	p := a.pl
	b := ui.ButtonBase(c)
	if o.label != "" {
		b.Label(o.label)
	} else if o.tip != "" {
		b.Label(o.tip)
	}
	hov := b.Hovered() && !o.disabled
	dn := b.Pressed() && !o.disabled
	h := b.AnimateTo("hover", boolf(hov), ui.Animation{Duration: 140 * time.Millisecond})

	height, padX, fs := float32(38), float32(16), float32(13)
	if o.small {
		height, padX, fs = 30, 12, 12.5
	}
	if o.iconOnly {
		padX = 0
		b.Width(height)
	}
	b.Height(height).Padding(0, padX).Radius(radiusControl).Gap(7).AlignItems(ui.Center).Justify(ui.Center).Cursor(ui.CursorPointer)
	if o.grow {
		b.Grow(1)
	}
	fg := p.Text
	switch o.kind {
	case btnPrimary:
		fg = ui.Hex("#FFFFFF")
		top, bot := p.BlueHi, p.Blue
		if p.Dark {
			top, bot = p.Blue.Mix(p.BlueHi, 0.55), p.BlueLo.Mix(p.Blue, 0.35)
		}
		top, bot = top.Mix(ui.Hex("#FFFFFF"), 0.12*h), bot.Mix(ui.Hex("#FFFFFF"), 0.1*h)
		if dn {
			top, bot = top.Mix(p.BlueLo, 0.35), bot.Mix(p.BlueLo, 0.35)
		}
		b.LinearGradient(ui.LinearGradient{From: top, To: bot, Angle: 180})
		b.Border(1, p.BlueHi.Alpha(0.5)).InsetShadow(0, 1, 0, 0, ui.RGBA(255, 255, 255, 0.28))
		b.Shadow(0, 6+2*h, 18, -6, p.Glow)
	case btnSecondary:
		b.Background(p.Panel.Mix(p.PanelHi, h)).Border(1, p.Line.Mix(p.LineHi, h))
		if !p.Dark {
			b.Shadow(0, 1, 2, 0, ui.RGBA(16, 40, 96, 0.06))
		}
	case btnGhost:
		b.Background(p.PanelHi.Alpha(h))
		fg = p.Text2.Mix(p.Text, h)
	case btnDanger:
		fg = p.Bad
		b.Background(p.Bad.Alpha(0.10+0.08*h)).Border(1, p.Bad.Alpha(0.25))
	case btnSoft:
		fg = p.BlueText
		b.Background(p.Blue.Alpha(0.14+0.08*h)).Border(1, p.Blue.Alpha(0.22))
	}
	b.TextColor(fg)
	b.Disabled(o.disabled)
	if o.tip != "" {
		b.Tooltip(o.tip)
	}
	pose := ui.Pose{}
	if dn {
		pose = ui.Pose{Scale: 0.965}
	}
	b.AnimatePose(pose, ui.Snappy)
	b.Children(func() {
		switch {
		case o.busy:
			a.spinner(c, 15, fg)
		case o.icon != "":
			ui.Icon(c, icon(o.icon)).Size(16, 16)
		}
		if o.label != "" {
			ui.Text(c, o.label).FontSize(fs).FontWeight(600).SingleLine()
		}
	})
	return b
}

// ---------------------------------------------------------------- controls

// toggle is an on/off switch; it reports whether the user flipped it.
func (a *App) toggle(c *ui.Context, key string, on *bool, label string) bool {
	p := a.pl
	sw := ui.SwitchBase(c.Key(key), on)
	sw.Label(label).Size(44, 26).Radius(13).Padding(3).Cursor(ui.CursorPointer)
	k := sw.AnimateTo("on", boolf(*on), ui.Animation{Duration: 200 * time.Millisecond})
	hov := sw.AnimateTo("hov", boolf(sw.Hovered()), ui.Animation{Duration: 120 * time.Millisecond})
	off := p.Line.Mix(p.LineHi, 0.7+0.3*hov)
	if !p.Dark {
		off = p.LineHi.Mix(p.Text3, 0.35+0.2*hov)
	}
	sw.Background(off.Mix(p.Blue, k))
	if k > 0.02 {
		sw.Shadow(0, 0, 14*k, 0, p.Glow.Alpha(0.55*k))
	}
	knob := sw.AnimateTo("x", boolf(*on), ui.Snappy)
	sw.Children(func() {
		ui.Box(c).Size(20, 20).Radius(10).Background(ui.Hex("#FFFFFF")).
			Shadow(0, 1, 3, 0, ui.RGBA(0, 0, 0, 0.28)).Translate(18*knob, 0)
	})
	return sw.Changed()
}

// toggleRow is a settings row that is itself the switch: the whole row takes
// the click, its title names it for assistive technology, and the track
// sits at its end. It reports whether the user flipped it.
func (a *App) toggleRow(c *ui.Context, key, title, desc string, on *bool, disabled bool) bool {
	p := a.pl
	row := ui.SwitchBase(c.Key(key), on)
	row.Label(title).Padding(14, 0).Gap(16).AlignItems(ui.Center).Cursor(boolOr(disabled, ui.CursorDefault, ui.CursorPointer)).Disabled(disabled)
	k := row.AnimateTo("on", boolf(*on), ui.Animation{Duration: 200 * time.Millisecond})
	knob := row.AnimateTo("x", boolf(*on), ui.Snappy)
	hov := row.AnimateTo("hov", boolf(row.Hovered()), ui.Animation{Duration: 120 * time.Millisecond})
	row.Children(func() {
		ui.Column(c).Grow(1).Basis(0).Gap(3).Children(func() {
			a.txt(c, title, 13.5, 600, p.Text)
			if desc != "" {
				a.txt(c, desc, 12, 400, p.Text3).LineHeight(1.4)
			}
		})
		off := p.Line.Mix(p.LineHi, 0.7+0.3*hov)
		if !p.Dark {
			off = p.LineHi.Mix(p.Text3, 0.35+0.2*hov)
		}
		track := ui.Box(c).Size(44, 26).Radius(13).Padding(3).Background(off.Mix(p.Blue, k))
		if k > 0.02 {
			track.Shadow(0, 0, 14*k, 0, p.Glow.Alpha(0.55*k))
		}
		track.Children(func() {
			ui.Box(c).Size(20, 20).Radius(10).Background(ui.Hex("#FFFFFF")).
				Shadow(0, 1, 3, 0, ui.RGBA(0, 0, 0, 0.28)).Translate(18*knob, 0)
		})
	})
	return row.Changed()
}

// segItem is one option of a segmented control.
type segItem struct {
	label string
	icon  string
}

// segmented is a sliding-thumb segmented control; it reports a new choice.
func (a *App) segmented(c *ui.Context, key string, sel *int, items []segItem, opt segOpts) bool {
	p := a.pl
	n := len(items)
	if *sel < 0 || *sel >= n {
		*sel = 0
	}
	fs := float32(12.5)
	pad := float32(14)
	if opt.small {
		fs, pad = 12, 11
	}
	h := float32(34)
	if opt.small {
		h = 28
	}
	widths := make([]float32, n)
	var total float32
	for i, it := range items {
		w, _ := c.MeasureText(0, ui.Span{Text: it.label, Size: fs, Weight: 600})
		w += 2 * pad
		if it.icon != "" {
			w += 20
		}
		if opt.minSeg > w {
			w = opt.minSeg
		}
		widths[i] = float32(math.Ceil(float64(w)))
		total += widths[i]
	}
	if opt.fill {
		// Distribute evenly across the requested width.
		for i := range widths {
			widths[i] = opt.width / float32(n)
		}
		total = opt.width
	}
	seg := ui.SegmentedBase(c.Key(key), sel, n)
	seg.Track.Radius(radiusControl).Padding(3).Background(p.Field).Border(1, p.Line)
	seg.Track.Height(h + 6).Width(total + 6 + 2)
	seg.Track.Role(ui.RoleRadioGroup)
	var x float32
	for i := 0; i < *sel; i++ {
		x += widths[i]
	}
	tx := seg.Track.AnimateTo("thumb", x, ui.Animation{Spring: ui.SpringOptions{Duration: 320 * time.Millisecond, Bounce: 0.12}})
	tw := seg.Track.AnimateTo("thumbw", widths[*sel], ui.Animation{Spring: ui.SpringOptions{Duration: 320 * time.Millisecond, Bounce: 0.05}})
	// The thumb is painted by the track, under the segments' labels.
	seg.Track.Draw(func(pp *ui.Painter, r ui.Rect) {
		th := ui.Rect{X: r.X + 4 + tx, Y: r.Y + 4, W: tw, H: h}
		rad := float32(radiusControl - 2)
		if p.Dark {
			pp.Shadow(th, rad, 0, 4, 12, -4, p.Glow)
			pp.FillGradient(th, ui.LinearGradient{From: p.Blue.Mix(p.BlueHi, 0.4), To: p.BlueLo.Mix(p.Blue, 0.5), Angle: 180}, rad)
			pp.Stroke(th, ui.RGBA(255, 255, 255, 0.16), rad, 1)
		} else {
			pp.Shadow(th, rad, 0, 6, 14, -8, p.Shadow)
			pp.Fill(th, p.Raised, rad)
			pp.Stroke(th, p.Line, rad, 1)
		}
	})
	seg.Track.Children(func() {
		for i, it := range items {
			s := seg.Segment(i).Width(widths[i]).Height(h).Radius(radiusControl - 2).Gap(6).Center().Cursor(ui.CursorPointer)
			s.Label(it.label)
			active := i == *sel
			col := p.Text2
			hov := s.AnimateTo("hov", boolf(s.Hovered() && !active), ui.Animation{Duration: 120 * time.Millisecond})
			col = col.Mix(p.Text, hov)
			if active {
				col = p.Text
				if p.Dark {
					col = ui.Hex("#FFFFFF")
				}
			}
			s.TextColor(col)
			s.Children(func() {
				if it.icon != "" {
					ui.Icon(c, icon(it.icon)).Size(14, 14)
				}
				ui.Text(c, it.label).FontSize(fs).FontWeight(600).SingleLine()
			})
		}
	})
	return seg.Track.Changed()
}

type segOpts struct {
	small  bool
	fill   bool
	width  float32
	minSeg float32
}

// input is a styled single-line text input; it returns the element.
func (a *App) input(c *ui.Context, key string, val *string, placeholder string) ui.Element {
	p := a.pl
	in := ui.TextInput(c.Key(key), val).Placeholder(placeholder).Label(placeholder)
	in.Height(38).Padding(0, 12).Radius(radiusControl).Border(1, p.Line)
	foc := in.AnimateTo("foc", boolf(in.Focused()), ui.Animation{Duration: 140 * time.Millisecond})
	in.Background(p.Field)
	in.BorderColor(p.Line.Mix(p.Blue, foc))
	if foc > 0.02 {
		in.Shadow(0, 0, 0, 3*foc, p.Blue.Alpha(0.22*foc))
	}
	in.TextColor(p.Text)
	in.PlaceholderColor(p.Text3)
	return in
}

// area is a styled multi-line text input of between min and max lines.
func (a *App) area(c *ui.Context, key string, val *string, placeholder string, min, max int) ui.Element {
	p := a.pl
	in := ui.TextArea(c.Key(key), val).Placeholder(placeholder).Label(placeholder).Lines(min, max)
	in.Padding(10, 12).Radius(radiusControl).Border(1, p.Line)
	foc := in.AnimateTo("foc", boolf(in.Focused()), ui.Animation{Duration: 140 * time.Millisecond})
	in.Background(p.Field)
	in.BorderColor(p.Line.Mix(p.Blue, foc))
	if foc > 0.02 {
		in.Shadow(0, 0, 0, 3*foc, p.Blue.Alpha(0.22*foc))
	}
	in.TextColor(p.Text)
	in.PlaceholderColor(p.Text3)
	in.Font("monospace").FontSize(12.5)
	return in
}

// search is a styled search field.
func (a *App) search(c *ui.Context, key string, val *string, placeholder string, width float32) ui.Element {
	p := a.pl
	row := ui.Row(c.Key(key+"-wrap")).Height(36).Padding(0, 12).Gap(8).Radius(radiusControl).Border(1, p.Line).Background(p.Field).AlignItems(ui.Center)
	if width > 0 {
		row.Width(width)
	} else {
		row.Grow(1)
	}
	row.Children(func() {
		ui.Icon(c, icon("search")).Size(15, 15).TextColor(p.Text3)
		in := ui.TextInputBase(c.Key(key), val).Placeholder(placeholder).Label(placeholder).Grow(1).Height(34)
		in.TextColor(p.Text)
		in.PlaceholderColor(p.Text3)
		if *val != "" {
			if a.btn(c, btnSpec{kind: btnGhost, icon: "x", small: true, iconOnly: true, tip: a.t("common.clear")}).Clicked() {
				*val = ""
			}
		}
	})
	foc := row.AnimateTo("foc", boolf(row.FocusWithin()), ui.Animation{Duration: 140 * time.Millisecond})
	row.BorderColor(p.Line.Mix(p.Blue, foc))
	if foc > 0.02 {
		row.Shadow(0, 0, 0, 3*foc, p.Blue.Alpha(0.2*foc))
	}
	return row
}

// field labels a control.
func (a *App) field(c *ui.Context, label, hint string, body func()) ui.Element {
	return ui.Column(c).Gap(6).Children(func() {
		a.txt(c, label, 12, 600, a.pl.Text2).SingleLine()
		body()
		if hint != "" {
			a.caption(c, hint)
		}
	})
}

// settingRow is a labelled row of a settings card: text on the left, a
// control on the right.
func (a *App) settingRow(c *ui.Context, title, desc string, control func()) ui.Element {
	return ui.Row(c).Gap(16).Padding(14, 0).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Grow(1).Gap(3).Children(func() {
			a.txt(c, title, 13.5, 600, a.pl.Text)
			if desc != "" {
				a.txt(c, desc, 12, 400, a.pl.Text3)
			}
		})
		control()
	})
}

// ---------------------------------------------------------------- indicators

// spinner is an indeterminate progress arc.
func (a *App) spinner(c *ui.Context, size float32, col ui.Color) ui.Element {
	e := ui.Box(c).Size(size, size)
	return e.Draw(func(p *ui.Painter, r ui.Rect) {
		p.AnimationFrame()
		t := float64(p.Now().UnixMilli()%900) / 900
		cx, cy := r.X+r.W/2, r.Y+r.H/2
		rad := r.W/2 - 1.6
		w := size / 8
		if w < 1.6 {
			w = 1.6
		}
		start := float32(t * 2 * math.Pi)
		path := arcPath(cx, cy, rad, start, start+float32(1.5*math.Pi))
		p.StrokePath(path, w, col.Alpha(0.2))
		p.StrokePath(arcPath(cx, cy, rad, start, start+float32(math.Pi*0.9)), w, col)
	})
}

// arcPath is a circular arc as a polyline path; angles in radians, clockwise
// from the top.
func arcPath(cx, cy, r, a0, a1 float32) *ui.Path {
	var path ui.Path
	steps := int(math.Ceil(math.Abs(float64(a1-a0)) / (math.Pi / 90)))
	if steps < 2 {
		steps = 2
	}
	for i := 0; i <= steps; i++ {
		ang := float64(a0) + float64(a1-a0)*float64(i)/float64(steps)
		x := float64(cx) + float64(r)*math.Sin(ang)
		y := float64(cy) - float64(r)*math.Cos(ang)
		if i == 0 {
			path.MoveTo(float32(x), float32(y))
		} else {
			path.LineTo(float32(x), float32(y))
		}
	}
	return &path
}

// signal draws four ascending bars lit by how fast a node answers.
func (a *App) signal(c *ui.Context, ms int) ui.Element {
	p := a.pl
	lit, col := 0, p.Text3
	switch {
	case ms < 0:
		lit, col = 1, p.Bad
	case ms == 0:
		lit, col = 0, p.Text3
	case ms < 120:
		lit, col = 4, p.Good
	case ms < 250:
		lit, col = 3, p.Good
	case ms < 500:
		lit, col = 2, p.Warn
	default:
		lit, col = 1, p.Bad
	}
	dim := p.LineHi
	return ui.Box(c).Size(18, 14).Draw(func(pt *ui.Painter, r ui.Rect) {
		for i := 0; i < 4; i++ {
			h := 4 + float32(i)*3.3
			x := r.X + float32(i)*4.6
			col2 := dim
			if i < lit {
				col2 = col
			}
			pt.Fill(ui.Rect{X: x, Y: r.Y + r.H - h, W: 3, H: h}, col2, 1.2)
		}
	})
}

// latencyTone classifies a latency.
func latencyTone(ms int) tone {
	switch {
	case ms < 0:
		return toneBad
	case ms == 0:
		return toneNeutral
	case ms < 250:
		return toneGood
	case ms < 500:
		return toneWarn
	}
	return toneBad
}

// regionBadge is a round badge for a node's region: two letters on a colour
// that follows the code, so that regions are recognised at a glance.
func (a *App) regionBadge(c *ui.Context, code string, size float32) ui.Element {
	p := a.pl
	e := ui.Box(c).Size(size, size).Radius(size / 2).Center()
	if code == "" {
		e.Background(p.Field).Border(1, p.Line)
		e.Children(func() { ui.Icon(c, icon("globe")).Size(size*0.5, size*0.5).TextColor(p.Text3) })
		return e
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(code))
	// Cyan through blue to violet: every region stays in the brand's family.
	hue := 195 + float32(h.Sum32()%100)
	var top, bot ui.Color
	if p.Dark {
		top, bot = ui.Oklch(0.62, 0.17, hue), ui.Oklch(0.42, 0.16, hue+18)
	} else {
		top, bot = ui.Oklch(0.66, 0.16, hue), ui.Oklch(0.5, 0.17, hue+18)
	}
	e.LinearGradient(ui.LinearGradient{From: top, To: bot, Angle: 150, Oklab: true})
	e.InsetShadow(0, 1, 0, 0, ui.RGBA(255, 255, 255, 0.35))
	e.Children(func() {
		ui.Text(c, code).FontSize(size * 0.36).FontWeight(700).TextColor(ui.Hex("#FFFFFF")).LetterSpacing(0.3).SingleLine()
	})
	return e
}

// avatar is a round monogram on a gradient of the brand colours.
func (a *App) monogram(c *ui.Context, name string, size float32) ui.Element {
	p := a.pl
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	hue := 215 + float32(h.Sum32()%70)
	top, bot := ui.Oklch(0.64, 0.16, hue), ui.Oklch(0.46, 0.17, hue+16)
	if !p.Dark {
		top, bot = ui.Oklch(0.68, 0.15, hue), ui.Oklch(0.52, 0.17, hue+16)
	}
	return ui.Box(c).Size(size, size).Radius(size*0.32).Center().
		LinearGradient(ui.LinearGradient{From: top, To: bot, Angle: 150, Oklab: true}).
		InsetShadow(0, 1, 0, 0, ui.RGBA(255, 255, 255, 0.3)).
		Children(func() {
			ui.Text(c, initials(name)).FontSize(size * 0.38).FontWeight(700).TextColor(ui.Hex("#FFFFFF")).SingleLine()
		})
}

// ------------------------------------------------------------------ states

// emptyState explains an empty page and offers the next step.
func (a *App) emptyState(c *ui.Context, iconName, title, desc string, actions func()) ui.Element {
	p := a.pl
	return ui.Column(c).AlignItems(ui.Center).Gap(10).Padding(40, 24).Children(func() {
		ui.Box(c).Size(76, 76).Radius(24).Center().
			LinearGradient(ui.LinearGradient{From: p.Blue.Alpha(0.28), To: p.Blue.Alpha(0.06), Angle: 150}).
			Border(1, p.Blue.Alpha(0.3)).Shadow(0, 14, 34, -12, p.Glow).
			Children(func() { ui.Icon(c, icon(iconName)).Size(34, 34).TextColor(p.BlueText) })
		ui.Box(c).Height(6)
		a.txt(c, title, 17, 650, p.Text).LetterSpacing(-0.2).TextAlign(ui.Center)
		a.txt(c, desc, 13, 400, p.Text2).TextAlign(ui.Center).MaxWidth(380).LineHeight(1.5)
		if actions != nil {
			ui.Row(c).Gap(10).Padding(12, 0, 0, 0).Children(actions)
		}
	})
}
