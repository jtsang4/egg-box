// Sing-Box UI is the desktop application: a window, a tray icon and a panel
// that drive a privileged background service which owns the network (TUN,
// routes, DNS). Everything the user sees is native MyGo UI written in Go.
package main

import (
	"fmt"
	"os"

	"github.com/egoist/mygo"

	"github.com/jtsang4/sing-box-ui/internal/boot"
	"github.com/jtsang4/sing-box-ui/internal/brand"
	"github.com/jtsang4/sing-box-ui/internal/version"
)

func main() {
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
