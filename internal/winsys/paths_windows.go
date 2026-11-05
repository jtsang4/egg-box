//go:build windows

package winsys

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"

	"eggbox/internal/brand"
)

// DataDir is where the service keeps its state: under ProgramData, which the
// system, not the environment of whoever launched us, says where it is.
func DataDir() (string, error) {
	base, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, windows.KF_FLAG_DEFAULT)
	if err != nil {
		return "", fmt.Errorf("find ProgramData: %w", err)
	}
	return filepath.Join(base, brand.DataDirName, "agent"), nil
}

// Executable is the path of the running program, with links resolved.
func Executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

// GUIExecutable is the application next to the service in the installation.
func GUIExecutable(agentExe string) string {
	return filepath.Join(filepath.Dir(agentExe), brand.DisplayName+".exe")
}
