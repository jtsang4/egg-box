// A headless test harness for a MyGo view, to copy into the package that owns
// the interface (rename the package, then replace the counter with the real
// view and its fixture). It renders the real view on the CPU at a given size,
// scale and appearance, with the title-bar inset of a platform, lets
// animations finish, clicks by visible text, asserts on the texts on screen,
// and writes a screenshot when UI_SHOTS names a directory. It also shows the
// idle test: a settled screen must ask for no more frames. Verified against
// MyGo v0.7.4.
//
//	go test ./...                                  # fast: shots are skipped with -short
//	UI_SHOTS=/tmp/shots go test -run Shots ./pkg   # writes the matrix as PNGs
package uiharness

import (
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

type counter struct{ n int }

func (s *counter) view(c *ui.Context) {
	ui.Column(c).Fill().Center().Gap(12).Children(func() {
		ui.Text(c, fmt.Sprint(s.n)).FontSize(40).Bold()
		if ui.PrimaryButton(c, "Increment").Clicked() {
			s.n++
		}
	})
}

// harness drives a view headlessly: the real view, rendered on the CPU.
type harness struct {
	t  *testing.T
	ui *ui.Tester
}

type harnessOpt struct {
	w, h  int
	dark  bool
	scale float32
	view  func(c *ui.Context)
}

func newHarness(t *testing.T, o harnessOpt) *harness {
	t.Helper()
	if o.w == 0 {
		o.w = 1180
	}
	if o.h == 0 {
		o.h = 760
	}
	tt := ui.NewTester(o.view, o.w, o.h)
	if o.scale > 0 {
		tt.SetScale(o.scale)
	}
	tt.SetDark(o.dark)
	tt.SetTitleBar(ui.TitleBar{Height: 28, Left: 78}) // macOS; Windows: ui.TitleBar{Height: 32, Right: 138}
	h := &harness{t: t, ui: tt}
	h.settle()
	return h
}

// settle lets every animation finish. Screenshots wait longer than assertions.
func (h *harness) settle() {
	h.t.Helper()
	h.ui.Frame()
	d := 900 * time.Millisecond
	if os.Getenv("UI_SHOTS") != "" {
		d = 2500 * time.Millisecond
	}
	h.ui.Advance(d)
	h.ui.Frame()
}

// click presses the control showing text. When it cannot, the failure lists
// what is on screen, which usually explains it at once.
func (h *harness) click(text string) {
	h.t.Helper()
	if err := h.ui.Click(text); err != nil {
		h.t.Fatalf("click %q: %v\ntexts: %s", text, err, h.digest())
	}
	h.ui.Advance(400 * time.Millisecond)
}

// digest is the texts on screen, shortened for a failure message.
func (h *harness) digest() string {
	var b strings.Builder
	seen := map[string]bool{}
	for _, s := range h.ui.Texts() {
		if seen[s] {
			continue
		}
		seen[s] = true
		if b.Len() > 700 {
			b.WriteString(" ...")
			break
		}
		b.WriteString(strconv.Quote(s))
		b.WriteByte(' ')
	}
	return b.String()
}

// shot writes a screenshot when UI_SHOTS names a directory.
func (h *harness) shot(name string) {
	h.t.Helper()
	dir := os.Getenv("UI_SHOTS")
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		h.t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, name+".png"))
	if err != nil {
		h.t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, h.ui.Image()); err != nil {
		h.t.Fatal(err)
	}
}

func TestShots(t *testing.T) {
	if testing.Short() {
		t.Skip("renders every screen in both themes")
	}
	for _, dark := range []bool{false, true} {
		name := "counter-light"
		if dark {
			name = "counter-dark"
		}
		t.Run(name, func(t *testing.T) {
			s := &counter{}
			h := newHarness(t, harnessOpt{dark: dark, view: s.view})
			h.click("Increment")
			h.settle()
			if !h.ui.HasText("1") {
				t.Fatalf("want 1 on screen, have %q", h.ui.Texts())
			}
			h.shot(name)
		})
	}
}

// An idle screen asks for no more frames: a screen that keeps asking costs
// battery for nothing.
func TestIdleScreenAsksForNoFrames(t *testing.T) {
	s := &counter{}
	h := newHarness(t, harnessOpt{view: s.view})
	if h.ui.FramePending() {
		t.Fatal("the settled screen still asks for frames")
	}
}

// The control case: the same assertion fails for a screen that animates, so
// it is not trivially true.
func TestAnimatedScreenKeepsAsking(t *testing.T) {
	view := func(c *ui.Context) {
		ui.Box(c).Width(100).Height(100).Draw(func(p *ui.Painter, r ui.Rect) {
			p.AnimationFrameRate(30)
		})
	}
	h := newHarness(t, harnessOpt{view: view})
	if !h.ui.FramePending() {
		t.Fatal("an animated screen should still ask for frames")
	}
}
