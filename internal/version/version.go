// Package version carries the build identity. Release builds set the variables
// with -ldflags "-X github.com/jtsang4/sing-box-ui/internal/version.Version=1.2.3".
package version

// Version is the application and agent version. The GUI and the agent of one
// installation always share it.
var Version = "0.1.0-dev"

// Commit is the source revision, when known.
var Commit = ""

// String is the version with the commit, for logs and the About page.
func String() string {
	if Commit == "" {
		return Version
	}
	c := Commit
	if len(c) > 9 {
		c = c[:9]
	}
	return Version + " (" + c + ")"
}
