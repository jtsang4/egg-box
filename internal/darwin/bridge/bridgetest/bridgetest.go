//go:build darwin

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

	"github.com/jtsang4/sing-box-ui/internal/darwin/bridge"
)

var (
	once sync.Once
	path string
	err  error
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
	dir, e := os.MkdirTemp("", "sbui-bridge-*")
	if e != nil {
		err = e
		return
	}
	arch := map[string]string{"arm64": "arm64", "amd64": "x86_64"}[runtime.GOARCH]
	script := filepath.Join(root, "native", "macos", "ServiceBridge", "build.sh")
	if out, e := exec.Command(script, dir, arch).CombinedOutput(); e != nil {
		err = fmt.Errorf("build the service bridge: %w\n%s", e, out)
		return
	}
	path = filepath.Join(dir, "ServiceBridge.dylib")
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
