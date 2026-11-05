package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// options are the command line of the application. Almost nothing needs one:
// the application is started from the Dock, the Start menu or at login. The
// flags are for trying it out (--demo) and for development builds.
type options struct {
	// demo runs against an invented world in this process: nothing leaves the
	// computer, nothing is installed, nothing is kept.
	demo bool
	// fresh makes the demonstration start as a first run.
	fresh bool
	// connect connects as soon as the application is up.
	connect bool
	// seed makes the demonstration's traffic repeatable.
	seed uint64
	// background starts without showing the window (a login launch).
	background bool
	// version prints the version and exits; help prints the flags.
	version, help bool

	// Window shaping, for screenshots and small screens.
	width, height int
	page          string
	theme, lang   string

	dev devOptions
}

// parseArgs reads the command line. Unknown words are ignored: the system
// may pass the application a URL or a file to open.
func parseArgs(args []string) (options, error) {
	var o options
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, val, hasVal := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if !strings.HasPrefix(a, "-") {
			continue
		}
		next := func() (string, error) {
			if hasVal {
				return val, nil
			}
			if i+1 >= len(args) {
				return "", fmt.Errorf("--%s needs a value", name)
			}
			i++
			return args[i], nil
		}
		var err error
		switch name {
		case "demo":
			o.demo = true
		case "fresh":
			o.fresh = true
		case "connect":
			o.connect = true
		case "background":
			o.background = true
		case "version", "v":
			o.version = true
		case "help", "h":
			o.help = true
		case "seed":
			var v string
			if v, err = next(); err == nil {
				o.seed, err = strconv.ParseUint(v, 10, 64)
			}
		case "size":
			var v string
			if v, err = next(); err == nil {
				w, h, ok := strings.Cut(v, "x")
				if o.width, err = strconv.Atoi(w); err == nil && ok {
					o.height, err = strconv.Atoi(h)
				} else if !ok {
					err = fmt.Errorf("--size wants WIDTHxHEIGHT")
				}
			}
		case "page":
			o.page, err = next()
		case "theme":
			o.theme, err = next()
		case "lang":
			o.lang, err = next()
		default:
			var handled bool
			handled, err = o.dev.parse(name, next)
			if !handled && err == nil {
				continue // not ours
			}
		}
		if err != nil {
			return o, err
		}
	}
	if o.fresh && !o.demo {
		return o, fmt.Errorf("--fresh only makes sense with --demo")
	}
	return o, nil
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "egg-box: "+format+"\n", args...)
	os.Exit(1)
}

// usage is what --help prints. Nothing in it is needed to run the application.
const usage = `EggBox

Usage: egg-box [flags]

The application starts from the Dock, the Start menu or at login and needs no
flags. These are for trying it out.

  --demo           run against an invented world inside this process: nothing
                   leaves the computer, nothing is installed, nothing is kept
  --fresh          with --demo, start as a first run
  --connect        with --demo, connect as soon as the window is up
  --seed N         with --demo, make the traffic repeatable
  --size WxH       window size in pixels (default 1200x780)
  --page NAME      open on a page: dashboard, nodes, subscriptions, routing,
                   connections, logs or settings
  --theme NAME     with --demo: system, light or dark
  --lang CODE      with --demo: en or zh
  --background     start without showing the window
  --version        print the version and exit
  --help           print this text and exit
`
