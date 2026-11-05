// Package winsys holds what the Windows installation of Sing-Box UI needs from
// the operating system and that both the service and the application share:
// who the authorized user is, where the service's files live, how the service
// is installed and started, and how the application asks for administrator
// rights once.
//
// The parts that need no Windows call are in files without a suffix so they
// can be tested anywhere; the rest is in *_windows.go files.
package winsys

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// OwnerFile is the name of the record of the authorized user, in the service's
// data directory (writable by SYSTEM and administrators only).
const OwnerFile = "owner.json"

// Owner is the one user who may control the service.
type Owner struct {
	// SID is the user's security identifier. It is what access decisions use.
	SID string `json:"sid"`
	// Name is "DOMAIN\user", for display only.
	Name string `json:"name,omitempty"`
}

var sidPattern = regexp.MustCompile(`^S-1-[0-9]+(-[0-9]+){1,14}$`)

// Valid reports whether the record can be used.
func (o Owner) Valid() bool { return sidPattern.MatchString(o.SID) }

// ErrNoOwner means nobody was authorized yet.
var ErrNoOwner = errors.New("no user is authorized to control the service")

// LoadOwner reads the record in dataDir.
func LoadOwner(dataDir string) (Owner, error) {
	raw, err := os.ReadFile(filepath.Join(dataDir, OwnerFile))
	if errors.Is(err, os.ErrNotExist) {
		return Owner{}, ErrNoOwner
	}
	if err != nil {
		return Owner{}, err
	}
	var o Owner
	if err := json.Unmarshal(raw, &o); err != nil {
		return Owner{}, fmt.Errorf("%s is damaged: %w", OwnerFile, err)
	}
	if !o.Valid() {
		return Owner{}, fmt.Errorf("%s holds no valid user", OwnerFile)
	}
	return o, nil
}

// SaveOwner writes the record in dataDir, replacing any earlier one.
func SaveOwner(dataDir string, o Owner) error {
	if !o.Valid() {
		return fmt.Errorf("%q is not a security identifier", o.SID)
	}
	raw, err := json.MarshalIndent(o, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dataDir, OwnerFile+".tmp")
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dataDir, OwnerFile))
}
