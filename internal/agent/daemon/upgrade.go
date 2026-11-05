package daemon

import (
	"log/slog"
	"os"
	"path/filepath"

	"github.com/jtsang4/sing-box-ui/internal/agent/store"
)

// UpgradeMarker is the name of a file an installer leaves in the agent's data
// folder before it stops the running service to replace it. It says the stop is
// for an upgrade, so the connection the person had comes back when the new
// version starts. Any other stop (the computer shutting down, the person
// stopping the service) brings nothing back by itself: whether to reconnect is
// then up to the settings.
//
// Only an installer can leave it: the data folder can be written by root, or by
// administrators, and nobody else. The installers' scripts spell the name out
// because they cannot import this package; a test keeps them in step.
const UpgradeMarker = "upgrade-pending"

// consumeUpgradeMarker turns the installer's marker, if there is one, into the
// intent to resume what was connected. The marker is used up in any case, so it
// never outlives the start it was for, and only a plain file counts: a folder
// or a link in its place is removed and ignored.
func consumeUpgradeMarker(dir string, intent *store.File[store.Intent], log *slog.Logger) {
	path := filepath.Join(dir, UpgradeMarker)
	st, err := os.Lstat(path)
	if err != nil {
		return
	}
	defer func() { _ = os.Remove(path) }()
	if !st.Mode().IsRegular() {
		log.Warn("ignoring an upgrade marker that is not a plain file")
		return
	}
	var resume bool
	if _, err := intent.Update(func(i *store.Intent) error {
		// Nothing to bring back when the person was not connected.
		resume = i.Connected
		i.ResumeAfterUpgrade = i.ResumeAfterUpgrade || i.Connected
		return nil
	}); err != nil {
		log.Warn("could not record the intent to resume after the upgrade", "err", err)
		return
	}
	if resume {
		log.Info("the previous version was stopped for an upgrade while connected; the connection will be restored")
	}
}
