//go:build darwin && !ios

package e2e

import (
	"os"
	"testing"

	"eggbox/internal/darwin/bridge/bridgetest"
)

// TestMain removes the Swift library the XPC tests built, once all the tests
// are done: it is the only point at which it is no longer needed, and a
// directory per run would otherwise stay in the temporary one.
func TestMain(m *testing.M) {
	code := m.Run()
	bridgetest.Cleanup()
	os.Exit(code)
}
