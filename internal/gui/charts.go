package gui

import (
	"math"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/chart"

	"eggbox/internal/app"
)

// series turns throughput samples into plot fractions: xs spread across the
// plot with the newest sample at the right edge, ys scaled to peak.
type series struct {
	xs, down, up []float32
	peak         float64
}

// buildSeries pads the history on the left so that the line scrolls in from
// the edge instead of stretching, and scales both lines to a common peak.
func buildSeries(hist []app.Sample, n int) series {
	if n < 2 {
		n = app.HistoryLen
	}
	s := series{xs: make([]float32, n), down: make([]float32, n), up: make([]float32, n)}
	off := n - len(hist)
	if off < 0 {
		hist = hist[-off:]
		off = 0
	}
	var peak float64
	for _, h := range hist {
		peak = math.Max(peak, math.Max(h.Down, h.Up))
	}
	// A quiet line stays quiet: the scale never drops below 64 KB/s, and
	// rises with headroom so that spikes do not touch the top.
	peak = math.Max(peak*1.18, 64*1024)
	s.peak = peak
	for i := 0; i < n; i++ {
		s.xs[i] = float32(i) / float32(n-1)
		if i >= off {
			h := hist[i-off]
			s.down[i] = float32(h.Down / peak)
			s.up[i] = float32(h.Up / peak)
		}
	}
	return s
}

// sparkline is a small throughput line without axes.
func (a *App) sparkline(c *ui.Context, key string, hist []app.Sample, h float32, both bool) ui.Element {
	p := a.pl
	e := ui.Box(c.Key("spark-" + key)).Height(h)
	s := buildSeries(hist, app.HistoryLen)
	return e.Draw(func(pt *ui.Painter, r ui.Rect) {
		a.drawLines(pt, r, s, both, 1.6, false)
		_ = p
	})
}

// drawLines paints the throughput lines and the area under the download line.
func (a *App) drawLines(pt *ui.Painter, r ui.Rect, s series, both bool, width float32, glow bool) {
	p := a.pl
	// Leave a little room under the line so that its stroke is not cut.
	rr := ui.Rect{X: r.X, Y: r.Y + 2, W: r.W, H: r.H - 4}
	area := chart.Area(rr, s.xs, nil, s.down, chart.CurveMonotone)
	pt.FillPathGradient(area, ui.LinearGradient{From: p.Blue.Alpha(boolOr[float32](p.Dark, 0.42, 0.34)), To: p.Blue.Alpha(0), Angle: 180})
	line := chart.Line(rr, s.xs, s.down, chart.CurveMonotone)
	if glow {
		pt.StrokePath(line, width+5, p.Blue.Alpha(0.12))
	}
	pt.StrokePathGradient(line, width, ui.LinearGradient{From: p.Blue, To: p.Cyan, Angle: 90})
	if both {
		upLine := chart.Line(rr, s.xs, s.up, chart.CurveMonotone)
		pt.StrokePath(upLine, width, p.Indigo.Alpha(0.9))
	}
}

// trafficChart is the live throughput chart with a hover read-out.
func (a *App) trafficChart(c *ui.Context, key string, hist []app.Sample, h float32) ui.Element {
	p := a.pl
	e := ui.Box(c.Key("traffic-" + key)).Height(h).Cursor(ui.CursorCrosshair)
	s := buildSeries(hist, app.HistoryLen)
	px, _, inside := e.PointerPosition()
	n := len(s.xs)
	e.Draw(func(pt *ui.Painter, r ui.Rect) {
		// Grid: three faint horizontal lines.
		for i := 1; i <= 3; i++ {
			y := r.Y + r.H*float32(i)/4
			pt.StrokeDashed(ui.Rect{X: r.X, Y: y, W: r.W, H: 0}, p.Line.Alpha(0.9), 0, 1)
		}
		a.drawLines(pt, r, s, true, 2.2, true)

		if inside && r.W > 0 {
			f := (px) / r.W
			i := int(math.Round(float64(f) * float64(n-1)))
			if i < 0 {
				i = 0
			}
			if i > n-1 {
				i = n - 1
			}
			x := chart.X(r, s.xs[i])
			pt.Line(x, r.Y+2, x, r.Y+r.H, 1, p.LineHi)
			yd := chart.Y(ui.Rect{X: r.X, Y: r.Y + 2, W: r.W, H: r.H - 4}, s.down[i])
			yu := chart.Y(ui.Rect{X: r.X, Y: r.Y + 2, W: r.W, H: r.H - 4}, s.up[i])
			pt.Fill(ui.Rect{X: x - 4, Y: yd - 4, W: 8, H: 8}, p.Blue, 4)
			pt.Stroke(ui.Rect{X: x - 4, Y: yd - 4, W: 8, H: 8}, p.Window, 4, 2)
			pt.Fill(ui.Rect{X: x - 3.5, Y: yu - 3.5, W: 7, H: 7}, p.Indigo, 3.5)
			pt.Stroke(ui.Rect{X: x - 3.5, Y: yu - 3.5, W: 7, H: 7}, p.Window, 3.5, 2)

			// The bubble.
			off := len(hist) - n
			idx := i + off
			var d, u float64
			if idx >= 0 && idx < len(hist) {
				d, u = hist[idx].Down, hist[idx].Up
			}
			age := n - 1 - i
			label := fmtRate(d) + "   " + fmtRate(u)
			w, _ := pt.MeasureText(0, ui.Span{Text: label, Size: 12, Weight: 600})
			bw, bh := w+58, float32(46)
			bx := x + 12
			if bx+bw > r.X+r.W {
				bx = x - 12 - bw
			}
			by := r.Y + 6
			pt.Shadow(ui.Rect{X: bx, Y: by, W: bw, H: bh}, 10, 0, 8, 22, -6, ui.RGBA(0, 0, 0, 0.5))
			pt.Fill(ui.Rect{X: bx, Y: by, W: bw, H: bh}, p.Raised, 10)
			pt.Stroke(ui.Rect{X: bx, Y: by, W: bw, H: bh}, p.LineHi, 10, 1)
			pt.Text(bx+10, by+7, a.agoSeconds(age), 11, p.Text3)
			pt.Fill(ui.Rect{X: bx + 10, Y: by + 29, W: 6, H: 6}, p.Blue, 3)
			pt.Text(bx+22, by+25, fmtRate(d), 12, p.Text)
			pt.Fill(ui.Rect{X: bx + 22 + w/2 + 10, Y: by + 29, W: 6, H: 6}, p.Indigo, 3)
			pt.Text(bx+22+w/2+22, by+25, fmtRate(u), 12, p.Text)
		}
	})
	return e
}

func (a *App) agoSeconds(n int) string {
	if n <= 0 {
		return a.t("time.now")
	}
	return a.tn("time.seconds_ago", n)
}
