package gui

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// harness drives the whole interface headlessly: the real view, rendered on
// the CPU, with a fixture behind it.
type harness struct {
	t   *testing.T
	ui  *ui.Tester
	app *App
	be  *fixtureBackend
}

type harnessOpt struct {
	w, h     int
	dark     bool
	lang     string
	platform string
	scale    float32
	motion   bool
	setup    func(*fixtureBackend, *Options)
	// panel shows the tray panel instead of the window.
	panel bool
}

// newHarness builds an interface of the given size on a fixture.
func newHarness(t *testing.T, o harnessOpt) *harness {
	t.Helper()
	if o.w == 0 {
		o.w = 1180
	}
	if o.h == 0 {
		o.h = 760
	}
	// Interaction tests keep the full logical viewport and hit targets while
	// rasterizing fewer pixels. Motion/screenshot fixtures retain native DPI.
	if o.scale == 0 && !o.motion {
		o.scale = 0.5
	}
	if o.lang == "" {
		o.lang = "en"
	}
	if o.platform == "" {
		o.platform = "darwin"
	}
	be := newFixture()
	be.set.Language = o.lang
	be.set.Theme = "light"
	if o.dark {
		be.set.Theme = "dark"
	}
	opt := Options{Backend: be, Sync: true, Platform: o.platform, Version: "0.1.0", Now: func() time.Time { return fixtureNow }}
	opt.SaveFile = func(name string, data []byte) (string, error) {
		be.record("save-file %s", name)
		return "/tmp/" + name, nil
	}
	opt.SetLaunchAtLogin = func(on bool) error { be.record("launch-at-login %v", on); return nil }
	opt.ShowWindow = func() { be.record("show-window") }
	opt.ClosePanel = func() { be.record("close-panel") }
	opt.Quit = func() { be.record("quit") }
	boot := true
	opt.ServiceStartup = &ServiceStartup{
		Get: func() (bool, error) { return boot, nil },
		Set: func(on bool) error { boot = on; be.record("service-boot %v", on); return nil },
	}
	if o.setup != nil {
		o.setup(be, &opt)
	}
	a := New(opt)
	view := a.View
	if o.panel {
		view, o.w, o.h = a.PanelView, PanelWidth, PanelHeight
	}
	// Configure the window before building its view. Each configuration change
	// otherwise renders the whole application with an intermediate appearance.
	ready := false
	tt := ui.NewTester(func(c *ui.Context) {
		if ready {
			view(c)
		}
	}, o.w, o.h)
	tt.SetPreferences(ui.Preferences{ReduceMotion: !o.motion, TextScale: 1})
	if o.scale > 0 {
		tt.SetScale(o.scale)
	}
	tt.SetDark(o.dark)
	bar := ui.TitleBar{Height: 28, Left: 78}
	if o.platform == "windows" {
		bar = ui.TitleBar{Height: 32, Right: 138}
	}
	if !o.panel {
		tt.SetTitleBar(bar)
	}
	ready = true
	h := &harness{t: t, ui: tt, app: a, be: be}
	h.settle()
	return h
}

// settle lets every animation finish.
func (h *harness) settle() {
	h.t.Helper()
	h.ui.Frame()
	// Screenshots wait for every animation; assertions do not need to.
	d := 900 * time.Millisecond
	if os.Getenv("EGGBOX_SHOTS") != "" {
		d = 2500 * time.Millisecond
	}
	h.ui.Advance(d)
	h.ui.Frame()
}

// click presses the control showing text and settles.
func (h *harness) click(text string) {
	h.t.Helper()
	if err := h.ui.Click(text); err != nil {
		h.t.Fatalf("click %q: %v\ntexts: %s", text, err, h.digest())
	}
	h.ui.Advance(400 * time.Millisecond)
}

func (h *harness) mustHave(texts ...string) {
	h.t.Helper()
	for _, s := range texts {
		if !h.ui.HasText(s) {
			h.t.Errorf("missing %q on screen; have %s", s, h.digest())
		}
	}
}

func (h *harness) mustNotHave(texts ...string) {
	h.t.Helper()
	for _, s := range texts {
		if h.ui.HasText(s) {
			h.t.Errorf("unexpected %q on screen", s)
		}
	}
}

// shot writes a screenshot when EGGBOX_SHOTS names a directory.
func (h *harness) shot(name string) {
	h.t.Helper()
	dir := os.Getenv("EGGBOX_SHOTS")
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		h.t.Fatal(err)
	}
	writePNG(h.t, filepath.Join(dir, name+".png"), h.ui.Image())
}

func writePNG(t *testing.T, path string, img image.Image) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
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
			b.WriteString(" …")
			break
		}
		b.WriteString(strconv.Quote(s))
		b.WriteByte(' ')
	}
	return b.String()
}
