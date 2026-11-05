// Package gui is the application's window: a native MyGo interface, written
// in Go from the first pixel to the last. Nothing in it is a web page and no
// script runs behind it.
//
// An App turns a Backend (the application service, or a fixture) into views.
// The view function runs on the main thread once per frame; anything slow
// (network, the privileged service) runs in a job, off the main thread, and
// reports back through Window.Update.
package gui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/jtsang4/sing-box-ui/internal/app"
	"github.com/jtsang4/sing-box-ui/internal/configgen"
	"github.com/jtsang4/sing-box-ui/internal/library"
)

// Options configure an App.
type Options struct {
	Backend Backend
	// Version is the application's version, for the About section.
	Version string
	// Platform is runtime.GOOS; it shapes the words the interface uses for
	// the service ("launch daemon", "Windows service") and the window controls.
	Platform string
	// Demo marks a run against simulated data.
	Demo bool
	// Now is the clock; nil uses time.Now.
	Now func() time.Time
	// Authorize asks the system to let this application use the privileged
	// service (the approval step of the platform); nil when nothing is needed.
	Authorize func(ctx context.Context) error
	// SaveFile asks the user where to keep a file and writes it, returning the
	// chosen path ("" when the user cancelled); nil disables exporting.
	SaveFile func(name string, data []byte) (string, error)
	// SetLaunchAtLogin registers or removes the GUI's own login item.
	SetLaunchAtLogin func(on bool) error
	// ServiceStartup reads and changes whether the privileged service starts
	// together with the computer; nil hides the choice.
	ServiceStartup *ServiceStartup
	// OpenServiceSettings opens the system page where the person switches the
	// privileged service on or off, for platforms (macOS) where that is the
	// only place to do it; nil hides the row.
	OpenServiceSettings func()
	// RemoveService takes the privileged service out of the system, for
	// platforms where the application itself can (macOS); elsewhere the
	// uninstaller does it. nil hides the action.
	RemoveService func(ctx context.Context) error
	// OpenURL opens a link in the browser; nil uses the context's.
	OpenURL func(url string)
	// Sync runs jobs inline, for tests.
	Sync bool
	// OnLanguage is told the language the interface settled on ("en", "zh").
	OnLanguage func(lang string)
	// OnTheme is told the appearance the user chose ("", "light", "dark"), so
	// that the window's own chrome can follow it.
	OnTheme func(theme string)
	// OnChange is called, from any goroutine, after the backend changed; the
	// tray follows the connection with it.
	OnChange func()
	// Notify shows a system notification. The interface decides what is worth
	// one; nil turns notifications off.
	Notify func(title, body string)
	// ShowWindow brings the main window to the front.
	ShowWindow func()
	// SystemLocale is the language of the system, for text outside the window.
	SystemLocale func() string
	// ClosePanel closes the tray panel; Quit quits the application.
	ClosePanel func()
	Quit       func()
}

// page is a destination of the sidebar.
type page int

const (
	pageDashboard page = iota
	pageNodes
	pageSubscriptions
	pageRules
	pageConnections
	pageLogs
	pageSettings
	pageCount
)

// App is the interface.
type App struct {
	opt Options
	be  Backend
	win *mygo.Window
	svc atomic.Pointer[ui.Services] // the main window's persistent services: redraws from any goroutine
	// psvc is the tray panel's, while it is open.
	psvc atomic.Pointer[ui.Services]

	// Resolved at the start of every frame.
	snap      *app.Snapshot
	set       library.Settings
	subs      []library.Subscription
	prefs     configgen.Prefs
	lang      string
	themeSeen string
	dark      bool
	sysDark   bool
	pl        *palette
	now       time.Time
	platform  string
	bar       ui.TitleBar

	page     page
	pageFrom page
	pageAt   time.Time

	toasts     []*toast
	toastSeq   uint64
	seenNotice uint64
	started    bool

	scrolls  []ui.ScrollState
	nodes    []nodeEntry
	nodesSig string

	dlg     dialog
	dlgOpen bool
	jobs    map[string]bool
	flash   map[string]time.Time // brief confirmations: "copied", keyed by id

	dash dashState
	node nodesState
	sub  subsState
	rule rulesState
	conn connsState
	logs logsState
	sett settingsState
	boot onboardState

	pending atomic.Bool
	mu      sync.Mutex
	queue   []func()

	// What the system notifications have told so far (see notifyNew).
	nmu      sync.Mutex
	nStarted bool
	nLast    uint64
}

// New creates the interface for a backend.
func New(opt Options) *App {
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Platform == "" {
		opt.Platform = "darwin"
	}
	a := &App{opt: opt, be: opt.Backend, platform: opt.Platform, jobs: map[string]bool{}, flash: map[string]time.Time{}}
	a.initState()
	if a.be != nil {
		a.be.SetNotify(func() {
			a.invalidate()
			a.notifyNew()
			if a.opt.OnChange != nil {
				a.opt.OnChange()
			}
		})
	}
	return a
}

// Attach tells the App which window it draws, so that other goroutines can
// ask it to redraw.
func (a *App) Attach(win *mygo.Window) { a.win = win }

// Close detaches from the backend.
func (a *App) Close() {
	if a.be != nil {
		a.be.SetNotify(nil)
		a.be.WatchConnections(false)
	}
}

// invalidate asks for a redraw from any goroutine, coalescing bursts.
func (a *App) invalidate() {
	if a.svc.Load() == nil && a.psvc.Load() == nil || !a.pending.CompareAndSwap(false, true) {
		return
	}
	time.AfterFunc(24*time.Millisecond, func() {
		a.pending.Store(false)
		a.again()
	})
}

// post runs fn on the main thread.
func (a *App) post(fn func()) {
	if a.win == nil || a.opt.Sync {
		fn()
		return
	}
	a.win.Update(fn)
}

// job runs fn off the main thread and then done on it. A job is identified
// by key: starting one that is already running does nothing, which also lets
// buttons show a busy state with a.busy(key).
func (a *App) job(key string, fn func(ctx context.Context) error, done func(err error)) {
	if a.jobs[key] {
		return
	}
	a.jobs[key] = true
	a.again()
	finish := func(err error) {
		delete(a.jobs, key)
		if done != nil {
			done(err)
		}
	}
	if a.opt.Sync {
		finish(fn(context.Background()))
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		err := fn(ctx)
		a.post(func() { finish(err) })
	}()
}

// busy reports whether the job is running.
func (a *App) busy(key string) bool { return a.jobs[key] }

// fail shows an error as a toast, in the user's language.
func (a *App) fail(err error) {
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}
	a.toastError(a.explain(err))
}

// view draws the window. It is the function the window's content runs.
func (a *App) View(c *ui.Context) {
	a.frame(c, &a.svc)
	a.maybeWelcome()
	a.shell(c)
}

// frame reads everything the frame needs, once, so that the pieces of the
// view agree with each other, and does the bookkeeping of notices and toasts.
func (a *App) frame(c *ui.Context, slot *atomic.Pointer[ui.Services]) {
	a.resolve(c, slot)
	a.absorbNotices()
	a.pruneToasts()
	a.pruneFlash()
}

// resolve reads the backend and the preferences into the App's per-frame
// state. Every window of the application (the main one and the tray panel)
// does it at the start of its frame.
func (a *App) resolve(c *ui.Context, slot *atomic.Pointer[ui.Services]) {
	if slot.Load() == nil {
		s := c.Services()
		slot.Store(&s)
	}
	a.now = a.opt.Now()
	a.sysDark = c.Theme().Dark
	a.bar = c.TitleBar()

	a.snap = a.be.Snapshot()
	a.set = a.be.Settings()
	a.subs = a.be.Subscriptions()
	a.prefs = a.be.Prefs()

	lang := a.set.Language
	if lang == "" {
		lang = systemLanguage(c.Locale())
	}
	if lang != a.lang {
		a.lang = lang
		if a.opt.OnLanguage != nil {
			a.opt.OnLanguage(lang)
		}
	}
	if a.set.Theme != a.themeSeen {
		a.themeSeen = a.set.Theme
		if a.opt.OnTheme != nil {
			a.opt.OnTheme(a.set.Theme)
		}
	}
	switch a.set.Theme {
	case "dark":
		a.dark = true
	case "light":
		a.dark = false
	default:
		a.dark = a.sysDark
	}
	a.pl = a.pal()
	c.SetTheme(themeCache(a.pl))
}

// systemLanguage maps a locale tag to a language the interface has.
func systemLanguage(locale string) string {
	if strings.HasPrefix(strings.ToLower(locale), "zh") {
		return "zh"
	}
	return "en"
}

var (
	themeMu     sync.Mutex
	themeByDark = map[bool]*ui.Theme{}
)

// themeCache returns the toolkit theme of a palette, built once.
func themeCache(p *palette) *ui.Theme {
	themeMu.Lock()
	defer themeMu.Unlock()
	if t, ok := themeByDark[p.Dark]; ok {
		return t
	}
	t := themeFor(p)
	themeByDark[p.Dark] = t
	return t
}

// flashed reports whether a brief confirmation is showing.
func (a *App) flashed(key string) bool {
	_, ok := a.flash[key]
	return ok
}

// setFlash shows a brief confirmation for a moment.
func (a *App) setFlash(key string, d time.Duration) {
	a.flash[key] = a.now.Add(d)
	time.AfterFunc(d+50*time.Millisecond, a.invalidate)
}

func (a *App) pruneFlash() {
	for k, until := range a.flash {
		if !a.now.Before(until) {
			delete(a.flash, k)
		}
	}
}

// again asks for another frame: state that changed while the view was being
// built shows in the next one.
func (a *App) again() {
	if s := a.svc.Load(); s != nil {
		s.Invalidate()
	}
	if s := a.psvc.Load(); s != nil {
		s.Invalidate()
	}
}

// goTo switches the page.
func (a *App) goTo(p page) {
	if p == a.page {
		return
	}
	a.again()
	a.pageFrom, a.page, a.pageAt = a.page, p, a.now
	// The connection table only costs something while it is on screen.
	a.be.WatchConnections(p == pageConnections)
}
