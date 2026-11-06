// EggBox is the desktop application: a window, a tray icon and a panel
// that drive a privileged background service which owns the network (TUN,
// routes, DNS). Everything the user sees is native MyGo UI written in Go.
package main

import (
	"fmt"
	"os"
	"runtime/debug"

	"github.com/egoist/mygo"

	"eggbox/internal/boot"
	"eggbox/internal/brand"
	"eggbox/internal/version"
)

func main() {
	// The interface keeps little alive (about 40 MB, half of it the glyph
	// atlas) and allocates little once it runs: a collection at half the live
	// heap instead of all of it costs no measurable CPU and keeps 15 to 50 MB
	// less in the process for as long as it sits in the tray. GOGC still
	// decides when it is set.
	if os.Getenv("GOGC") == "" {
		debug.SetGCPercent(50)
	}
	o, err := parseArgs(os.Args[1:])
	if err != nil {
		fatal("%v", err)
	}
	if o.help {
		fmt.Print(usage)
		return
	}
	if o.version {
		fmt.Printf("%s %s\n", brand.DisplayName, version.String())
		return
	}
	d := &desktop{o: o}
	if (o.theme != "" || o.lang != "") && d.mode() == boot.Installed {
		fatal("--theme and --lang only apply to --demo and development runs: use the application's settings")
	}

	mygo.App.SetName(brand.DisplayName)
	if d.mode() == boot.Installed {
		// One installed application per user. Demonstrations and development
		// runs stand beside it.
		if !mygo.App.RequestSingleInstanceLock() {
			return
		}
		mygo.App.OnSecondInstance(func([]string, string) { mygo.RunOnMain(d.showWindow) })
	}
	// The tray keeps the application alive when its window is closed; without
	// a tray the window is the application.
	mygo.App.OnWindowAllClosed(func() {
		if d.tray == nil {
			mygo.App.Quit()
		}
	})
	mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) { d.quitting = true })
	mygo.App.OnWillQuit(func(*mygo.QuitEvent) { d.shutdown() })
	mygo.App.WhenReady(d.ready)
	if err := mygo.App.Run(); err != nil {
		fatal("%v", err)
	}
}
