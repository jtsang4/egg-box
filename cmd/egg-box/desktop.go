package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"eggbox/internal/app"
	"eggbox/internal/boot"
	"eggbox/internal/brand"
	"eggbox/internal/gui"
	"eggbox/internal/library"
	"eggbox/internal/version"
)

// desktop is the application as the operating system sees it: the main
// window, the tray icon with its menu and its panel, the menus, the system
// notifications. What happens inside the window is the gui package's.
type desktop struct {
	o   options
	rt  *boot.Runtime
	app *gui.App

	win   *mygo.Window
	tray  *mygo.Tray
	panel *mygo.Window // the tray panel, made once and then shown or hidden

	// Everything above is read and changed on the main thread.
	quitting bool

	// The tray follows the connection; changes arrive from any goroutine and
	// are folded into one update.
	syncing atomic.Bool
	lastOn  atomic.Int32 // the icon drawn: 0 none, 1 off, 2 on

	// openServiceSettings opens the system page that switches the privileged
	// service on or off, when the platform has one (macOS).
	openServiceSettings func()
	// openProxySettings opens the system page of the proxy settings, when the
	// platform has one (macOS).
	openProxySettings func()
	// removeService takes the privileged service out of the system, when the
	// platform lets the application do it (macOS).
	removeService func(ctx context.Context) error

	menu menuItems
	lang string
}

func (d *desktop) mode() boot.Mode {
	switch {
	case d.o.demo:
		return boot.Demo
	case d.o.dev.active():
		return boot.Dev
	}
	return boot.Installed
}

// bootConfig says what to assemble for the way the application runs.
func (d *desktop) bootConfig() (boot.Config, error) {
	cfg := boot.Config{Mode: d.mode(), Version: version.Version, Fresh: d.o.fresh, Seed: d.o.seed}
	switch cfg.Mode {
	case boot.Demo:
	case boot.Dev:
		if err := d.o.dev.config(&cfg); err != nil {
			return cfg, err
		}
	default:
		dir, err := mygo.App.Path(mygo.PathUserData)
		if err != nil {
			return cfg, fmt.Errorf("find the application's data directory: %w", err)
		}
		cfg.DataDir = dir
		cfg.Secrets = library.Chain{keychain{}, library.NewFileSecrets(filepath.Join(dir, "secrets.json"))}
		ps := installedServices()
		cfg.Dial, cfg.Authorize, cfg.ServiceStartup = ps.Dial, ps.Authorize, ps.ServiceStartup
		cfg.SystemProxies = ps.SystemProxies
		d.openServiceSettings = ps.OpenServiceSettings
		d.openProxySettings = ps.OpenProxySettings
		d.removeService = ps.RemoveService
	}
	return cfg, nil
}

// ready runs once the toolkit is up: it assembles the application and opens
// what the user sees.
func (d *desktop) ready() {
	cfg, err := d.bootConfig()
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		d.rt, err = boot.Start(ctx, cfg)
		cancel()
	}
	if err != nil {
		mygo.Dialog.Error(brand.DisplayName, err.Error())
		mygo.App.Quit()
		return
	}
	d.applyLook()

	d.app = gui.New(d.guiOptions(cfg))
	d.newTray()
	// Started into the tray (at login, --background), there is no window
	// until somebody opens it.
	if d.tray == nil || !(d.o.background || mygo.App.WasOpenedAtLogin()) {
		d.newWindow()
	}
	d.buildMenu()
	d.syncTray()
	d.devReady()

	if d.o.page != "" {
		d.app.Navigate(d.o.page)
	}
	if d.o.connect {
		go d.connectSoon()
	}
}

// applyLook sets the appearance and language asked for on the command line,
// for demonstrations and screenshots.
func (d *desktop) applyLook() {
	if d.o.theme == "" && d.o.lang == "" {
		return
	}
	err := d.rt.Library.UpdateSettings(func(s *library.Settings) {
		if d.o.theme != "" {
			s.Theme = d.o.theme
		}
		if d.o.lang != "" {
			s.Language = d.o.lang
		}
	})
	if err != nil {
		log.Printf("apply --theme/--lang: %v", err)
	}
}

func (d *desktop) guiOptions(cfg boot.Config) gui.Options {
	o := gui.Options{
		Backend:      gui.ServiceBackend{Service: d.rt.Service},
		Version:      version.String(),
		Platform:     runtime.GOOS,
		Demo:         d.o.demo,
		SaveFile:     d.saveFile,
		OnLanguage:   d.onLanguage,
		OnTheme:      onTheme,
		OnChange:     d.changed,
		Notify:       d.notify,
		ShowWindow:   func() { mygo.RunOnMain(d.showWindow) },
		SystemLocale: func() string { return mygo.App.Locale() },
		ClosePanel:   func() { mygo.RunOnMain(d.closePanel) },
		Quit:         mygo.App.Quit,
	}
	if cfg.Authorize != nil || d.mode() == boot.Demo {
		o.Authorize = d.rt.Authorize
	}
	if t := d.rt.ServiceStartup; t != nil {
		o.ServiceStartup = &gui.ServiceStartup{Get: t.Get, Set: t.Set}
	}
	if d.openServiceSettings != nil {
		o.OpenServiceSettings = func() { go d.openServiceSettings() }
	}
	o.RemoveService = d.removeService
	if d.openProxySettings != nil {
		o.OpenProxySettings = func() { go d.openProxySettings() }
	}
	if path := noticesPath(); path != "" {
		o.OpenNotices = func() {
			go func() {
				if err := mygo.Shell.OpenPath(path); err != nil {
					log.Printf("open the licenses: %v", err)
				}
			}()
		}
	}
	if d.mode() == boot.Installed {
		// Only the installed application is a login item: a demonstration or
		// a development build must leave nothing behind.
		o.SetLaunchAtLogin = mygo.App.SetOpenAtLogin
	}
	d.phoneOptions(&o)
	return o
}

// connectSoon connects as soon as the service answers (--connect).
func (d *desktop) connectSoon() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := d.rt.Agent.WaitReady(ctx); err != nil {
		log.Printf("--connect: the service did not answer: %v", err)
		return
	}
	if err := d.rt.Service.Connect(ctx, app.ConnectOptions{}); err != nil {
		log.Printf("--connect: %v", err)
	}
}

// shutdown releases what the application holds. The service goes on running:
// closing the application does not disconnect.
func (d *desktop) shutdown() {
	if d.tray != nil {
		d.tray.Destroy()
	}
	if d.app != nil {
		d.app.Close()
	}
	if d.rt != nil {
		d.rt.Close()
	}
}

// onTheme makes the window's chrome follow the appearance the user chose.
func onTheme(theme string) {
	switch theme {
	case "light":
		mygo.Theme.SetSource(mygo.ThemeLight)
	case "dark":
		mygo.Theme.SetSource(mygo.ThemeDark)
	default:
		mygo.Theme.SetSource(mygo.ThemeSystem)
	}
}

// onLanguage re-words the menus.
func (d *desktop) onLanguage(lang string) {
	if lang != d.lang {
		d.lang = lang
		d.buildMenu()
		d.syncTray()
	}
}

// saveFile asks where to keep a file and writes it (for the diagnostics
// export and the log). It blocks on the dialog, so it runs in a job.
func (d *desktop) saveFile(name string, data []byte) (string, error) {
	var parent *mygo.Window
	mygo.RunOnMain(func() { parent = d.win }) // the window comes and goes on the main thread
	path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{
		Parent:            parent,
		Title:             brand.DisplayName,
		DefaultPath:       name,
		CreateDirectories: true,
	})
	if err != nil || path == "" {
		return "", err
	}
	return path, os.WriteFile(path, data, 0o600)
}

// -------------------------------------------------------------- the window

// newWindow makes the main window and shows it. Closed to the tray, the
// window is destroyed rather than hidden: a hidden one keeps its last frame,
// its surface and the tree of its interface, about 20 MB, for as long as the
// application runs in the tray, which is most of the time. It is made again
// when it is opened; the App keeps the page and everything the user did.
func (d *desktop) newWindow() {
	w, h := 1200, 780
	if d.o.width > 0 && d.o.height > 0 {
		w, h = d.o.width, d.o.height
	}
	win := mygo.NewWindow(mygo.WindowOptions{
		Title:           brand.DisplayName,
		Width:           w,
		Height:          h,
		MinWidth:        gui.MinWindowWidth,
		MinHeight:       gui.MinWindowHeight,
		StateKey:        d.stateKey(),
		TitleBarStyle:   mygo.TitleBarHidden,
		BackgroundColor: "light-dark(#EDF2FB, #060A15)",
		Content:         ui.View(d.app.View),
	})
	d.win = win
	d.app.Attach(win)
	win.OnClose(func(e *mygo.CloseEvent) {
		switch {
		case d.quitting || d.tray == nil:
			// Without a tray, closing the last window quits (OnWindowAllClosed).
		case d.app.CloseToTray():
			// A menu bar application: nothing in the Dock while only the tray shows.
			mygo.App.Dock.Hide()
		default:
			// The tray panel is a window too, so closing this one alone would
			// not end the application.
			e.PreventDefault()
			mygo.App.Quit()
		}
	})
	win.OnClosed(func() {
		if d.win == win {
			d.win = nil
			d.app.WindowClosed()
		}
	})
}

// stateKey is where the window remembers its place. Demonstrations and
// screenshots, which ask for a size, do not disturb the installed window's.
func (d *desktop) stateKey() string {
	if d.mode() == boot.Installed {
		return "main"
	}
	return ""
}

func (d *desktop) showWindow() {
	if d.app == nil {
		return
	}
	mygo.App.Dock.Show()
	if d.win == nil {
		d.newWindow()
	}
	if d.win.IsMinimized() {
		d.win.Restore()
	}
	d.win.Show()
	d.win.Focus()
}
