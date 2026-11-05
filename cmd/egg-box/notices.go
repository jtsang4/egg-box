package main

import (
	"os"
	"path/filepath"
	"runtime"
)

// noticesFile holds the licenses of the software the application is built
// from. The installers put it next to the program (Windows) or in the
// bundle's resources (macOS).
const noticesFile = "THIRD-PARTY-NOTICES.txt"

// noticesPath returns the file of this installation, or "" when there is none:
// a build that was not installed (go run, a development build) has no
// notices to show.
func noticesPath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return noticesFor(exe, runtime.GOOS)
}

// noticesFor looks for the file where the installer of goos puts it.
func noticesFor(exe, goos string) string {
	dir := filepath.Dir(exe)
	if goos == "darwin" {
		dir = filepath.Join(dir, "..", "Resources")
	}
	path := filepath.Join(dir, noticesFile)
	if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
		return ""
	}
	return filepath.Clean(path)
}
