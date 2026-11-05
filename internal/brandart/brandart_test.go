package brandart_test

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"eggbox/internal/brandart"
)

func alphaAt(img *image.NRGBA, x, y int) uint8 { return img.NRGBAAt(x, y).A }

func TestAppIconIsATileWithRoundedCorners(t *testing.T) {
	img := brandart.AppIcon(256)
	if img.Bounds().Dx() != 256 || img.Bounds().Dy() != 256 {
		t.Fatalf("size %v", img.Bounds())
	}
	if a := alphaAt(img, 2, 2); a != 0 {
		t.Errorf("the corner is not transparent (alpha %d)", a)
	}
	if a := alphaAt(img, 128, 40); a != 255 {
		t.Errorf("the tile is not opaque at its top (alpha %d)", a)
	}
	// The tile is blue: the blue channel dominates away from the mark.
	c := img.NRGBAAt(60, 190)
	if !(c.B > c.R+60 && c.B > c.G) {
		t.Errorf("the tile is not blue: %+v", c)
	}
	// The mark is white at its centre line.
	m := img.NRGBAAt(128, 160)
	if m.R < 235 || m.G < 235 || m.B < 235 {
		t.Errorf("the mark is not white: %+v", m)
	}
}

func TestTrayIcons(t *testing.T) {
	for _, on := range []bool{false, true} {
		tpl := brandart.TrayIcon(44, brandart.TrayTemplate, on)
		var ink int
		for y := range 44 {
			for x := range 44 {
				c := tpl.NRGBAAt(x, y)
				if c.A > 0 && (c.R != 0 || c.G != 0 || c.B != 0) {
					t.Fatalf("a template icon must be black, found %+v", c)
				}
				if c.A > 128 {
					ink++
				}
			}
		}
		if ink < 120 {
			t.Errorf("on=%v: the icon is nearly empty (%d pixels)", on, ink)
		}
	}
	filled := func(on bool) int {
		img := brandart.TrayIcon(44, brandart.TrayTemplate, on)
		n := 0
		for y := range 44 {
			for x := range 44 {
				if img.NRGBAAt(x, y).A > 128 {
					n++
				}
			}
		}
		return n
	}
	if filled(true) <= filled(false)+200 {
		t.Errorf("the connected icon (%d) should be much fuller than the idle one (%d)", filled(true), filled(false))
	}
	blue := brandart.TrayIcon(32, brandart.TrayBlue, false)
	var found bool
	for y := range 32 {
		for x := range 32 {
			if c := blue.NRGBAAt(x, y); c.A == 255 && c.B > 200 && c.R < 120 {
				found = true
			}
		}
	}
	if !found {
		t.Error("the blue tray icon has no blue")
	}
}

func TestPNGRoundTrips(t *testing.T) {
	img, err := png.Decode(bytes.NewReader(brandart.PNG(brandart.TrayIcon(16, brandart.TrayWhite, true))))
	if err != nil || img.Bounds().Dx() != 16 {
		t.Fatalf("decode: %v", err)
	}
}

// With EGGBOX_SHOTS set, a sheet of the icons is written there for a human to look at.
func TestPreview(t *testing.T) {
	dir := os.Getenv("EGGBOX_SHOTS")
	if dir == "" {
		t.Skip("set EGGBOX_SHOTS to write a preview")
	}
	sheet := image.NewNRGBA(image.Rect(0, 0, 4*140, 2*140))
	draw.Draw(sheet, image.Rect(0, 0, 4*140, 140), image.NewUniform(color.NRGBA{0xF2, 0xF2, 0xF4, 255}), image.Point{}, draw.Src)
	draw.Draw(sheet, image.Rect(0, 140, 4*140, 2*140), image.NewUniform(color.NRGBA{0x1E, 0x1F, 0x24, 255}), image.Point{}, draw.Src)
	i := 0
	for row, styles := range [][]brandart.TrayStyle{{brandart.TrayTemplate, brandart.TrayBlue}, {brandart.TrayWhite, brandart.TrayBlue}} {
		for _, style := range styles {
			for _, on := range []bool{false, true} {
				img := brandart.TrayIcon(128, style, on)
				draw.Draw(sheet, image.Rect(i%4*140+6, row*140+6, i%4*140+134, row*140+134), img, image.Point{}, draw.Over)
				i++
			}
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "tray-icons.png"), brandart.PNG(sheet), 0o644); err != nil {
		t.Fatal(err)
	}
}
