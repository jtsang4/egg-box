package macsys

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"time"

	"github.com/jtsang4/sing-box-ui/internal/brand"
)

// ShutdownBudget is how long the service takes, at most, to disconnect and put
// the network back when it is asked to stop.
const ShutdownBudget = 20 * time.Second

// ExitTimeOut is how long launchd waits after asking the service to stop
// before it ends it: longer than the service's own budget, so that launchd
// never cuts the service off in the middle of putting the network back.
const ExitTimeOut = ShutdownBudget + 5*time.Second

// BundleProgram is where the service's program is inside the application,
// relative to the bundle: the path launchd runs.
const BundleProgram = "Contents/MacOS/" + brand.AgentBinary

// LogPath is where launchd writes what the service prints, which is what
// there is to read when it fails before it can keep a log of its own.
const LogPath = "/var/log/" + brand.AgentLabel + ".log"

// DaemonPlistXML is the property list that makes the service a launch daemon
// of the application. It is part of the bundle, at
// Contents/Library/LaunchDaemons/DaemonPlist, and the system only runs the
// service once the person has allowed it in System Settings.
//
//   - MachServices makes the application's connection start the service on
//     demand; RunAtLoad starts it with the computer once it is allowed.
//   - KeepAlive restarts it after a crash (a clean exit, which is what asking
//     it to stop is, leaves it stopped) and the next start cleans up whatever
//     the crash left behind. ThrottleInterval keeps a service that cannot
//     start from being restarted in a loop.
//   - ExitTimeOut is how long launchd waits for the service to take the
//     network back before it ends it.
//   - ProcessType keeps the system from slowing a program that carries the
//     computer's traffic.
func DaemonPlistXML() []byte {
	var b bytes.Buffer
	str := func(s string) string {
		var e bytes.Buffer
		_ = xml.EscapeText(&e, []byte(s))
		return e.String()
	}
	fmt.Fprintf(&b, `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%[1]s</string>
	<key>BundleProgram</key>
	<string>%[2]s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%[3]s</string>
		<string>daemon</string>
	</array>
	<key>MachServices</key>
	<dict>
		<key>%[1]s</key>
		<true/>
	</dict>
	<key>AssociatedBundleIdentifiers</key>
	<array>
		<string>%[4]s</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>ThrottleInterval</key>
	<integer>30</integer>
	<key>ExitTimeOut</key>
	<integer>%[6]d</integer>
	<key>ProcessType</key>
	<string>Interactive</string>
	<key>StandardOutPath</key>
	<string>%[5]s</string>
	<key>StandardErrorPath</key>
	<string>%[5]s</string>
</dict>
</plist>
`, str(brand.AgentLabel), str(BundleProgram), str(brand.AgentBinary), str(brand.AppID), str(LogPath), int(ExitTimeOut/time.Second))
	return b.Bytes()
}
