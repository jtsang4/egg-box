//go:build darwin && !ios

// Package bridgetest builds the Swift service bridge for the machine's own
// architecture, once per test run, so tests of anything that uses it can load
// the real thing. Without a Swift toolchain the tests are skipped.
package bridgetest

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"eggbox/internal/darwin/bridge"
)

var (
	once sync.Once
	// libDir is where the library was built.
	libDir string
	path   string
	err    error
)

// Load returns the bridge, building the library first when this is the first
// call of the process.
func Load(t testing.TB) *bridge.Bridge {
	t.Helper()
	once.Do(build)
	if err != nil {
		t.Skipf("the Swift library could not be built here: %v", err)
	}
	b, loadErr := bridge.Load(path)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	return b
}

func build() {
	root, e := moduleRoot()
	if e != nil {
		err = e
		return
	}
	built, e := os.MkdirTemp("", "eggbox-bridge-*")
	if e != nil {
		err = e
		return
	}
	libDir = built
	arch := map[string]string{"arm64": "arm64", "amd64": "x86_64"}[runtime.GOARCH]
	script := filepath.Join(root, "native", "macos", "ServiceBridge", "build.sh")
	if out, e := exec.Command(script, libDir, arch).CombinedOutput(); e != nil {
		err = fmt.Errorf("build the service bridge: %w\n%s", e, out)
		return
	}
	path = filepath.Join(libDir, "ServiceBridge.dylib")
}

// Cleanup removes what Load built. A package that calls Load calls this from
// its TestMain once its tests are done: the library is needed until then, and
// nothing else would ever remove its directory from the temporary one.
func Cleanup() {
	if libDir != "" {
		_ = os.RemoveAll(libDir)
	}
}

// moduleRoot finds the repository root from the test's working directory.
func moduleRoot() (string, error) {
	dir, e := os.Getwd()
	if e != nil {
		return "", e
	}
	for {
		if _, e := os.Stat(filepath.Join(dir, "go.mod")); e == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod above the working directory")
		}
		dir = parent
	}
}
