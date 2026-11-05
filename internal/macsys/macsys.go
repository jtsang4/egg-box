// Package macsys holds what the macOS installation of Sing-Box UI needs that
// both the privileged agent and the application share: who the one authorized
// user is, how the code signing requirements that guard the channel are
// written, and how the agent notices that user leaving the screen.
//
// Nothing here calls the system. The calls are in the bridge (Swift) and the
// drivers; this is the logic around them, which can be tested anywhere.
package macsys

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/jtsang4/sing-box-ui/internal/brand"
)

// DataDir is where the agent keeps its state: a folder only root can change,
// which is why nothing a user does can put a file in it.
const DataDir = "/Library/Application Support/" + brand.DataDirName + "/agent"

// DaemonPlist is the file name of the agent's launchd property list, inside
// the application bundle at Contents/Library/LaunchDaemons.
const DaemonPlist = brand.AgentLabel + ".plist"

// OwnerFile is the record of the authorized user, in DataDir.
const OwnerFile = "owner.json"

// Owner is the one user who may control the agent.
type Owner struct {
	UID  uint32 `json:"uid"`
	Name string `json:"name,omitempty"`
}

// ErrNoOwner means nobody was authorized yet.
var ErrNoOwner = errors.New("no user is authorized to control the service")

// LoadOwner reads the record in dir.
func LoadOwner(dir string) (Owner, error) {
	raw, err := os.ReadFile(filepath.Join(dir, OwnerFile))
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
	if o.UID == 0 {
		return Owner{}, fmt.Errorf("%s holds no ordinary user", OwnerFile)
	}
	return o, nil
}

// SaveOwner writes the record in dir, replacing any earlier one. Root's own
// account is never a valid owner: the application does not run as root.
func SaveOwner(dir string, o Owner) error {
	if o.UID == 0 {
		return errors.New("root cannot be the authorized user")
	}
	raw, err := json.MarshalIndent(o, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, OwnerFile+".tmp")
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, OwnerFile))
}

// OwnerStore is the record of the authorized user, as the running service
// uses it. The file is read each time and never cached, so removing it (an
// administrator does, to hand the service to somebody else) takes effect for
// the next connection without waiting for anything.
type OwnerStore struct{ dir string }

// NewOwnerStore returns the store in dir.
func NewOwnerStore(dir string) *OwnerStore { return &OwnerStore{dir: dir} }

// Owner says who is authorized. ok is false when nobody is, or when the
// record cannot be trusted: a damaged record authorizes nobody.
func (s *OwnerStore) Owner() (Owner, bool) {
	o, err := LoadOwner(s.dir)
	return o, err == nil
}

// UID is Owner reduced to the user id.
func (s *OwnerStore) UID() (uint32, bool) {
	o, ok := s.Owner()
	return o.UID, ok
}

// Claim makes uid the authorized user. It never replaces one: that is done
// by removing the record first, which only root can.
func (s *OwnerStore) Claim(uid uint32, name string) error {
	if _, err := LoadOwner(s.dir); err == nil {
		return errors.New("a user is already authorized")
	} else if !errors.Is(err, ErrNoOwner) {
		return err
	}
	return SaveOwner(s.dir, Owner{UID: uid, Name: name})
}

// RemoveOwner forgets the authorized user, so the next one to use the
// application becomes it.
func RemoveOwner(dir string) error {
	if err := os.Remove(filepath.Join(dir, OwnerFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// ------------------------------------------------------------- requirements

var (
	identPattern = regexp.MustCompile(`^[A-Za-z0-9.-]+$`)
	teamPattern  = regexp.MustCompile(`^[A-Z0-9]{10}$`)
)

// Requirement writes the code signing requirement that a program of the given
// identifier must satisfy. With a team identifier it is anything signed by
// that team with an Apple-issued certificate under that identifier, which is
// what only the team's own build can be. Without one (an unsigned build made
// for development) the identifier alone is all there is to ask, and the
// caller should say so loudly.
func Requirement(identifier, team string) (string, error) {
	if !identPattern.MatchString(identifier) {
		return "", fmt.Errorf("%q is not a code signing identifier", identifier)
	}
	if team == "" {
		return fmt.Sprintf(`identifier "%s"`, identifier), nil
	}
	if !teamPattern.MatchString(team) {
		return "", fmt.Errorf("%q is not a team identifier", team)
	}
	return fmt.Sprintf(`anchor apple generic and identifier "%s" and certificate leaf[subject.OU] = "%s"`, identifier, team), nil
}

// AppRequirement is what the agent demands of the application.
func AppRequirement(team string) (string, error) { return Requirement(brand.AppID, team) }

// AgentRequirement is what the application demands of the agent.
func AgentRequirement(team string) (string, error) { return Requirement(brand.AgentLabel, team) }

// ---------------------------------------------------------------- the screen

// ConsoleTracker follows whether the authorized user is the one at the
// screen. The agent hears of every change of who that is; the connection is
// taken down when the authorized user leaves (signing out or another person
// switching in) and may be started when they come back.
type ConsoleTracker struct {
	owner   func() (uid uint32, ok bool)
	present bool
}

// NewConsoleTracker returns a tracker. owner says who is authorized, or ok =
// false when nobody is yet.
func NewConsoleTracker(owner func() (uint32, bool)) *ConsoleTracker {
	return &ConsoleTracker{owner: owner}
}

// Present reports whether the authorized user is believed to be at the screen.
func (t *ConsoleTracker) Present() bool { return t.present }

// Apply takes the user now at the screen (ok = false for the login window) and
// reports whether the authorized user's presence changed, and what it is now.
func (t *ConsoleTracker) Apply(uid uint32, ok bool) (changed, present bool) {
	owner, known := t.owner()
	now := ok && known && uid == owner
	changed = now != t.present
	t.present = now
	return changed, now
}
