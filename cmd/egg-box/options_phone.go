//go:build android || ios

package main

import (
	"os"
	"path/filepath"

	"github.com/egoist/mygo"

	"eggbox/internal/gui"
)

// phoneOptions shapes the interface for a phone: its layout, the system's VPN
// permission asked before connecting, and none of what only a desktop has
// (login item, tray, the privileged service's settings).
func (d *desktop) phoneOptions(o *gui.Options) {
	o.Phone = true
	o.AllowVPN = allowVPN
	o.SetLaunchAtLogin = nil
	o.ServiceStartup = nil
	o.OpenServiceSettings = nil
	o.RemoveService = nil
	o.OpenProxySettings = nil
	o.Authorize = nil
	o.SaveFile = phoneSaveFile
}

// phoneSaveFile writes the file in the application's temporary files and lets
// the person choose where a copy goes, with the system's picker: a phone's
// application has no path into other applications' folders.
func phoneSaveFile(name string, data []byte) (string, error) {
	dir, err := os.MkdirTemp("", "eggbox-export-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return mygo.Dialog.SaveCopy(path, name)
}
