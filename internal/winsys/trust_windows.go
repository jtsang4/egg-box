//go:build windows

package winsys

import (
	"errors"
	"fmt"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// ErrUntrustedExecutable means the program is somewhere a user can change it.
var ErrUntrustedExecutable = errors.New("the program is not in a protected folder")

// RequireTrustedExecutable refuses to register a service whose program an
// ordinary user could replace: that program would then run as SYSTEM. Only the
// folders the installer writes to, Program Files, qualify.
func RequireTrustedExecutable(exe string) error {
	var dirs []string
	for _, id := range []*windows.KNOWNFOLDERID{windows.FOLDERID_ProgramFiles, windows.FOLDERID_ProgramFilesX86, windows.FOLDERID_ProgramFilesX64} {
		if d, err := windows.KnownFolderPath(id, windows.KF_FLAG_DEFAULT); err == nil {
			dirs = append(dirs, d)
		}
	}
	if !UnderAny(exe, dirs) {
		return fmt.Errorf("%w: %s", ErrUntrustedExecutable, exe)
	}
	// None of the folders on the way may be a link to somewhere else.
	for dir := filepath.Dir(exe); dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		if UnderAny(dir, dirs) {
			if err := RejectReparsePoint(dir); err != nil {
				return err
			}
		}
	}
	return RejectReparsePoint(exe)
}
