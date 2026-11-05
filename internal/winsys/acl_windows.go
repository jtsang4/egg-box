//go:build windows

package winsys

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// dirSDDL is the access list of the service's data: the system and the
// administrators, nobody else, and nothing inherited from the parent. Whatever
// is created inside inherits it.
const dirSDDL = "D:PAI(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"

// ErrUntrustedDir means the directory was made, or can be changed, by someone
// who should not hold the service's data.
var ErrUntrustedDir = errors.New("the service's data directory is not protected")

// EnsureProtectedDir creates dir if needed and makes sure only SYSTEM and the
// administrators can change it. It refuses a directory that is a link, or
// that belongs to anybody else: a user could have made it before the
// installation to keep a hand on the files of a service that runs as SYSTEM.
func EnsureProtectedDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := RejectReparsePoint(dir); err != nil {
		return err
	}
	owner, err := ownerOf(dir)
	if err != nil {
		return err
	}
	if !owner.IsWellKnown(windows.WinLocalSystemSid) && !owner.IsWellKnown(windows.WinBuiltinAdministratorsSid) {
		return fmt.Errorf("%w: %s belongs to %s", ErrUntrustedDir, dir, owner)
	}
	sd, err := windows.SecurityDescriptorFromString(dirSDDL)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}

func ownerOf(path string) (*windows.SID, error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return nil, err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return nil, err
	}
	if owner == nil {
		return nil, errors.New("the directory has no owner")
	}
	return owner, nil
}

// RejectReparsePoint fails when path, or any directory above it up to the
// volume's root, is a symbolic link or junction: through one, a protected
// directory could be made to lead somewhere that is not.
func RejectReparsePoint(path string) error {
	p := filepath.Clean(path)
	for {
		u, err := windows.UTF16PtrFromString(p)
		if err != nil {
			return err
		}
		attrs, err := windows.GetFileAttributes(u)
		if err != nil {
			return fmt.Errorf("inspect %s: %w", p, err)
		}
		if attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return fmt.Errorf("%w: %s is a link", ErrUntrustedDir, p)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return nil
		}
		p = parent
	}
}
