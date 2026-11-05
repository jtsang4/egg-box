//go:build !windows

package macsys

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// AdminGID is the group of the computer's administrators on macOS. The
// Applications folder is writable by it, and nothing it can change is out of
// reach of anybody who can already become root.
const AdminGID = 80

// EnsureProtectedDir makes sure path (and the folder holding it) is a real
// directory that only owner can change, creating it when it is missing. It is
// how the service keeps its state out of reach of the users it serves: a
// folder an ordinary user could write in is a folder in which one could plant
// a file for a program that runs as root.
func EnsureProtectedDir(path string, owner uint32) error {
	for _, dir := range []string{filepath.Dir(path), path} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		fi, err := os.Lstat(dir)
		if err != nil {
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is a link, not a folder", dir)
		}
		if !fi.IsDir() {
			return fmt.Errorf("%s is not a folder", dir)
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok {
			return errors.New("cannot read who owns " + dir)
		}
		if st.Uid != owner {
			return fmt.Errorf("%s belongs to user %d, not to %d", dir, st.Uid, owner)
		}
		if fi.Mode().Perm()&0o022 != 0 {
			return fmt.Errorf("%s can be changed by others (mode %o)", dir, fi.Mode().Perm())
		}
	}
	return nil
}

// CheckInstallLocation refuses a program that somebody other than root could
// have changed: every folder from the top down to the file must belong to
// root and be closed to everyone but root and the administrators. The service
// runs as root; a copy of it in a folder a user can write in would be a way
// for that user to run what they like as root.
func CheckInstallLocation(exe string, root uint32) error {
	return checkLocation(exe, "/", root)
}

// checkLocation checks the file and every folder above it up to and including
// top. Tests use a top of their own: they cannot choose who owns the folders
// of the machine they run on.
func checkLocation(exe, top string, root uint32) error {
	real, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return err
	}
	real, err = filepath.Abs(real)
	if err != nil {
		return err
	}
	if top, err = filepath.EvalSymlinks(top); err != nil {
		return err
	}
	paths := []string{real}
	for p := filepath.Dir(real); ; p = filepath.Dir(p) {
		paths = append(paths, p)
		if p == top || p == filepath.Dir(p) {
			break
		}
	}
	for _, p := range paths {
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok {
			return errors.New("cannot read who owns " + p)
		}
		perm := fi.Mode().Perm()
		switch {
		case st.Uid != root:
			return fmt.Errorf("%s belongs to user %d, not to root", p, st.Uid)
		case perm&0o002 != 0:
			return fmt.Errorf("%s can be changed by everybody", p)
		case perm&0o020 != 0 && st.Gid != AdminGID:
			return fmt.Errorf("%s can be changed by group %d", p, st.Gid)
		}
	}
	return nil
}
