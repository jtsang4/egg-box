// Package brandart draws the application's mark as pixels: the application
// icon and the icons of the tray. It is plain Go (image, math) so that the
// icon files and the tray icons come from the same geometry as the logo in
// the window, with no tool and no image editor involved.
package brandart

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
)

// The mark is a cube seen from a corner, drawn on a 24×24 grid like the logo
// in the interface (internal/gui/shell.go).
type pt struct{ x, y float64 }

var (
	hexagon = []pt{{12, 2.8}, {20.2, 7.4}, {20.2, 16.6}, {12, 21.2}, {3.8, 16.6}, {3.8, 7.4}}
	// Inner edges: the two upper ones meet at the centre, the lower one drops.
	edges = [][2]pt{{{3.9, 7.5}, {12, 12}}, {{12, 12}, {20.1, 7.5}}, {{12, 12}, {12, 21}}}
)

func dot(a, b pt) float64 { return a.x*b.x + a.y*b.y }

// segDist is the distance from p to the segment ab.
func segDist(p, a, b pt) float64 {
	ab := pt{b.x - a.x, b.y - a.y}
	ap := pt{p.x - a.x, p.y - a.y}
	t := math.Max(0, math.Min(1, dot(ap, ab)/dot(ab, ab)))
	d := pt{ap.x - ab.x*t, ap.y - ab.y*t}
	return math.Hypot(d.x, d.y)
}

// outlineDist is the distance to the strokes of the mark.
func outlineDist(p pt, withEdges bool) float64 {
	d := math.Inf(1)
	for i := range hexagon {
		d = math.Min(d, segDist(p, hexagon[i], hexagon[(i+1)%len(hexagon)]))
	}
	if withEdges {
		for _, e := range edges {
			d = math.Min(d, segDist(p, e[0], e[1]))
		}
	}
	return d
}

// inPolygon reports whether p is inside the polygon.
func inPolygon(p pt, poly []pt) bool {
	in := false
	for i, j := 0, len(poly)-1; i < len(poly); j, i = i, i+1 {
		a, b := poly[i], poly[j]
		if (a.y > p.y) != (b.y > p.y) && p.x < (b.x-a.x)*(p.y-a.y)/(b.y-a.y)+a.x {
			in = !in
		}
	}
	return in
}

// The faces of the cube: light falls on the top one.
var (
	topFace  = []pt{{12, 2.8}, {20.2, 7.4}, {12, 12}, {3.8, 7.4}}
	leftFace = []pt{{3.8, 7.4}, {12, 12}, {12, 21.2}, {3.8, 16.6}}
)

func clamp01(v float64) float64 { return math.Max(0, math.Min(1, v)) }

func smooth(edge0, edge1, x float64) float64 {
	t := clamp01((x - edge0) / (edge1 - edge0))
	return t * t * (3 - 2*t)
}

// roundRectDist is the signed distance to a rounded rectangle centred at c.
func roundRectDist(p, c pt, hw, hh, r float64) float64 {
	qx := math.Abs(p.x-c.x) - (hw - r)
	qy := math.Abs(p.y-c.y) - (hh - r)
	out := math.Hypot(math.Max(qx, 0), math.Max(qy, 0))
	return out + math.Min(math.Max(qx, qy), 0) - r
}

type rgba struct{ r, g, b, a float64 } // premultiplied

func over(dst, src rgba) rgba {
	k := 1 - src.a
	return rgba{src.r + dst.r*k, src.g + dst.g*k, src.b + dst.b*k, src.a + dst.a*k}
}

func mix(a, b [3]float64, t float64) [3]float64 {
	return [3]float64{a[0] + (b[0]-a[0])*t, a[1] + (b[1]-a[1])*t, a[2] + (b[2]-a[2])*t}
}

// render evaluates shade at 3×3 samples per pixel and averages them.
func render(size int, shade func(x, y float64) rgba) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	const n = 3
	for py := range size {
		for px := range size {
			var acc rgba
			for sy := range n {
				for sx := range n {
					c := shade((float64(px)+(float64(sx)+0.5)/n)/float64(size), (float64(py)+(float64(sy)+0.5)/n)/float64(size))
					acc.r += c.r
					acc.g += c.g
					acc.b += c.b
					acc.a += c.a
				}
			}
			acc.r /= n * n
			acc.g /= n * n
			acc.b /= n * n
			acc.a /= n * n
			if acc.a <= 0 {
				continue
			}
			img.SetNRGBA(px, py, color.NRGBA{
				R: uint8(math.Round(clamp01(acc.r/acc.a) * 255)),
				G: uint8(math.Round(clamp01(acc.g/acc.a) * 255)),
				B: uint8(math.Round(clamp01(acc.b/acc.a) * 255)),
				A: uint8(math.Round(clamp01(acc.a) * 255)),
			})
		}
	}
	return img
}

// AppIcon draws the application icon: the mark in white on a blue tile with a
// soft light from above, in the proportions of a macOS icon (the tile fills 80%
// of the canvas, the rest is for the shadow).
func AppIcon(size int) *image.NRGBA {
	hi := [3]float64{0x74 / 255.0, 0xA6 / 255.0, 0xFF / 255.0}
	mid := [3]float64{0x3B / 255.0, 0x7B / 255.0, 0xFF / 255.0}
	lo := [3]float64{0x1B / 255.0, 0x45 / 255.0, 0xD6 / 255.0}

	return render(size, func(u, v float64) rgba {
		p := pt{u, v}
		const half, radius = 0.4, 0.185 // of the canvas; Apple's grid: 824/1024 and ~185/1024
		// Shadow under the tile.
		sd := roundRectDist(pt{p.x, p.y - 0.012}, pt{0.5, 0.5}, half, half, radius)
		sa := 0.35 * (1 - smooth(-0.005, 0.03, sd))
		out := rgba{0, 0, 0.05 * sa, sa}

		d := roundRectDist(p, pt{0.5, 0.5}, half, half, radius)
		tile := 1 - smooth(-0.0012, 0.0012, d)
		if tile > 0 {
			// Gradient from the top-left to the bottom-right, plus a glow at the top.
			t := clamp01(((p.x-0.1)*0.55 + (p.y-0.1)*0.85) / 0.8)
			c := mix(hi, mid, smooth(0, 0.55, t))
			c = mix(c, lo, smooth(0.45, 1, t))
			gl := math.Exp(-math.Pow((p.x-0.42)/0.34, 2)-math.Pow((p.y-0.12)/0.2, 2)) * 0.28
			c = mix(c, [3]float64{1, 1, 1}, gl)
			// A hairline of light along the top edge.
			rim := (1 - smooth(0, 0.006, -d)) * (1 - smooth(0.0, 0.2, p.y-0.1)) * 0.35
			c = mix(c, [3]float64{1, 1, 1}, clamp01(rim))
			out = over(out, rgba{c[0] * tile, c[1] * tile, c[2] * tile, tile})
		}

		// The mark, centred, 56% of the tile.
		const scale = 0.8 * 0.56 / 24
		m := pt{(p.x-0.5)/scale + 12, (p.y-0.5)/scale + 12}
		const stroke = 1.9 / 2
		// Soft shadow of the mark.
		ds := outlineDist(pt{m.x, m.y - 0.7}, true)
		sh := math.Exp(-math.Pow(math.Max(ds-stroke, 0)/1.1, 2)) * 0.28 * tile
		out = over(out, rgba{0, 0, 0.08 * sh, sh})
		// The faces catch the light, the top one most.
		face := 0.0
		switch {
		case inPolygon(m, topFace):
			face = 0.24
		case inPolygon(m, leftFace):
			face = 0.07
		}
		face *= tile
		out = over(out, rgba{face, face, face, face})
		cov := (1 - smooth(-0.045, 0.045, outlineDist(m, true)-stroke)) * tile
		out = over(out, rgba{cov, cov, cov, cov})
		return out
	})
}

// TrayStyle chooses how the tray icon is drawn.
type TrayStyle int

const (
	// TrayTemplate is black on transparent: macOS tints it for light and dark
	// menu bars.
	TrayTemplate TrayStyle = iota
	// TrayBlue is the brand blue on transparent, which shows on light and dark
	// taskbars.
	TrayBlue
	// TrayWhite is white, for dark panels.
	TrayWhite
)

// TrayIcon draws the mark for the tray. When on is set the cube is filled,
// with its edges cut out, to say that the proxy is running; off, it is an
// outline.
func TrayIcon(size int, style TrayStyle, on bool) *image.NRGBA {
	ink := [3]float64{0, 0, 0}
	switch style {
	case TrayBlue:
		ink = [3]float64{0x4F / 255.0, 0x84 / 255.0, 0xFF / 255.0}
	case TrayWhite:
		ink = [3]float64{1, 1, 1}
	}
	// The mark fills most of the icon: 22 of its 24 units.
	scale := 0.94 / 24.0
	stroke := 1.9 / 2
	if size <= 20 {
		stroke = 2.3 / 2 // thicker lines survive small sizes
	}
	return render(size, func(u, v float64) rgba {
		m := pt{(u-0.5)/scale + 12, (v-0.5)/scale + 12}
		var a float64
		if on {
			fill := 0.0
			if inPolygon(m, hexagon) {
				fill = 1
			}
			// The inner edges cut a groove into the fill.
			dEdge := math.Inf(1)
			for _, e := range edges {
				dEdge = math.Min(dEdge, segDist(m, e[0], e[1]))
			}
			cut := 1 - smooth(-0.05, 0.05, dEdge-0.62)
			rim := 1 - smooth(-0.05, 0.05, outlineDist(m, false)-stroke)
			a = math.Max(fill*(1-cut), rim)
		} else {
			a = 1 - smooth(-0.05, 0.05, outlineDist(m, true)-stroke)
		}
		return rgba{ink[0] * a, ink[1] * a, ink[2] * a, a}
	})
}

// PNG encodes an image.
func PNG(img image.Image) []byte {
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}
