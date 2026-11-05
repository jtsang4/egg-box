package gui

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
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
	setup    func(*fixtureBackend, *Options)
}

// newHarness builds an interface of the given size on a fixture.
func newHarness(t *testing.T, o harnessOpt) *harness {
	t.Helper()
	if o.w == 0 {
		o.w, o.h = 1180, 760
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
	boot := true
	opt.ServiceStartup = &ServiceStartup{
		Get: func() (bool, error) { return boot, nil },
		Set: func(on bool) error { boot = on; be.record("service-boot %v", on); return nil },
	}
	if o.setup != nil {
		o.setup(be, &opt)
	}
	a := New(opt)
	tt := ui.NewTester(a.View, o.w, o.h)
	if o.scale > 0 {
		tt.SetScale(o.scale)
	}
	tt.SetDark(o.dark)
	bar := ui.TitleBar{Height: 28, Left: 78}
	if o.platform == "windows" {
		bar = ui.TitleBar{Height: 32, Right: 138}
	}
	tt.SetTitleBar(bar)
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
	if os.Getenv("SBUI_SHOTS") != "" {
		d = 2500 * time.Millisecond
	}
	h.ui.Advance(d)
	h.ui.Frame()
}

// click presses the control showing text and settles.
func (h *harness) click(text string) {
	h.t.Helper()
	if err := h.ui.Click(text); err != nil {
		h.t.Fatalf("click %q: %v\ntexts: %q", text, err, h.ui.Texts())
	}
	h.ui.Advance(400 * time.Millisecond)
}

// has reports whether text is on screen.
func (h *harness) has(text string) bool { return h.ui.HasText(text) }

func (h *harness) mustHave(texts ...string) {
	h.t.Helper()
	for _, s := range texts {
		if !h.ui.HasText(s) {
			h.t.Errorf("missing %q on screen; have %q", s, h.ui.Texts())
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

// shot writes a screenshot when SBUI_SHOTS names a directory.
func (h *harness) shot(name string) {
	h.t.Helper()
	dir := os.Getenv("SBUI_SHOTS")
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

// slug turns a test name into a file name.
func slug(s string) string {
	return strings.NewReplacer("/", "-", " ", "-").Replace(strings.ToLower(s))
}
