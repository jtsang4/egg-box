//go:build darwin && !ios

package bridgetest

import (
	"os"
	"testing"
)

// What Load builds is taken away again by Cleanup: the directory it made in the
// temporary one does not stay behind a test run.
func TestCleanupRemovesWhatLoadBuilt(t *testing.T) {
	Load(t) // skips where the Swift library cannot be built
	built := libDir
	if built == "" {
		t.Fatal("the library was built, and no directory is known")
	}
	if _, err := os.Stat(built); err != nil {
		t.Fatalf("the directory of the library is not there after Load: %v", err)
	}
	Cleanup()
	if _, err := os.Stat(built); !os.IsNotExist(err) {
		t.Fatalf("the directory of the library is still there after Cleanup: %v", err)
	}
}
