//go:build eggbox_dev && ((darwin && !ios) || (linux && !android))

package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/egoist/mygo"

	"eggbox/internal/agent/transport"
	"eggbox/internal/boot"
	"eggbox/internal/library"
)

// devOptions are the flags of development builds (the eggbox_dev tag), which
// release builds do not have:
//
//	--dev-socket PATH   use the development agent (agentd dev) listening there,
//	                    which replaces the TUN with a loopback proxy
//	--dev-data DIR      where the library lives (default ~/.eggbox-dev/gui)
//	--capture DIR       take screenshots of every page, in both appearances,
//	                    write them to DIR and quit
//	--capture-warmup S  with --connect, seconds to let the charts fill first
//	                    (default 7)
type devOptions struct {
	socket  string
	dataDir string
	capture string
	warmup  int
}

func (o *devOptions) parse(name string, next func() (string, error)) (bool, error) {
	var err error
	switch name {
	case "dev-socket":
		o.socket, err = next()
	case "dev-data":
		o.dataDir, err = next()
	case "capture":
		o.capture, err = next()
	case "capture-warmup":
		var v string
		if v, err = next(); err == nil {
			o.warmup, err = strconv.Atoi(v)
		}
	default:
		return false, nil
	}
	return true, err
}

func (o devOptions) active() bool { return o.socket != "" }

// config points the assembly at the development agent. Subscription links stay
// in a file of the development directory, never in the user's keychain.
func (o devOptions) config(cfg *boot.Config) error {
	dir := o.dataDir
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		dir = filepath.Join(home, ".eggbox-dev", "gui")
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(o.socket) {
		return errors.New("--dev-socket needs an absolute path")
	}
	cfg.DataDir = dir
	cfg.Secrets = library.NewFileSecrets(filepath.Join(dir, "secrets.json"))
	cfg.Dial = transport.DialDev(o.socket)
	return nil
}

// devReady starts the screenshot tour when asked for.
func (d *desktop) devReady() {
	if d.o.dev.capture != "" {
		go d.tour(d.o.dev.capture)
	}
}

// onMainSync runs fn on the main thread and waits for it.
func onMainSync(fn func()) {
	done := make(chan struct{})
	mygo.RunOnMain(func() { fn(); close(done) })
	<-done
}

// tour takes a screenshot of every page of the window, and of the tray panel,
// in both appearances, and quits. It renders the windows' own content, so it
// needs no permission to record the screen and sees nothing else.
func (d *desktop) tour(dir string) {
	defer mygo.App.Quit()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("tour: %v", err)
		return
	}
	pause := func(ms int) { time.Sleep(time.Duration(ms) * time.Millisecond) }
	save := func(name string, png []byte, err error) {
		if err != nil {
			log.Printf("tour: %s: %v", name, err)
			return
		}
		if err := os.WriteFile(filepath.Join(dir, name+".png"), png, 0o644); err != nil {
			log.Printf("tour: %v", err)
		}
	}

	pause(1500)
	if d.o.connect {
		// Wait for the connection, and for the charts to have something to show.
		for i := 0; i < 120 && !d.app.Status().On; i++ {
			pause(250)
		}
		warm := d.o.dev.warmup
		if warm <= 0 {
			warm = 7
		}
		pause(warm * 1000)
	}
	pages := []string{"dashboard", "nodes", "subscriptions", "routing", "connections", "logs", "settings"}
	for _, theme := range []string{"dark", "light"} {
		if err := d.rt.Library.UpdateSettings(func(s *library.Settings) { s.Theme = theme }); err != nil {
			log.Printf("tour: %v", err)
		}
		pause(900)
		for _, page := range pages {
			onMainSync(func() { d.app.Navigate(page) })
			pause(1300)
			png, err := d.win.CapturePage()
			save(fmt.Sprintf("%s-%s", theme, page), png, err)
		}
		onMainSync(func() { d.app.Navigate("dashboard") })
		if d.tray != nil {
			onMainSync(d.togglePanel)
			pause(1200)
			var panel *mygo.Window
			onMainSync(func() { panel = d.panel })
			if panel != nil {
				png, err := panel.CapturePage()
				save(theme+"-panel", png, err)
			}
			onMainSync(d.closePanel)
			pause(300)
		}
	}
}
