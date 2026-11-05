package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jtsang4/sing-box-ui/internal/agent/engine/fake"
	"github.com/jtsang4/sing-box-ui/internal/agent/platform/sim"
	"github.com/jtsang4/sing-box-ui/internal/agent/policy"
)

func cfg(dir string) Config {
	return Config{
		DataDir: dir, Driver: sim.New(sim.Options{Sandbox: true}), Engine: fake.New(),
		Policy: policy.New(policy.Options{}), Version: "test",
	}
}

func TestBuildValidatesItsInputs(t *testing.T) {
	if _, err := Build(cfg("relative/dir")); err == nil {
		t.Fatal("a relative data directory must be refused")
	}
	if _, err := Build(cfg("")); err == nil {
		t.Fatal("an empty data directory must be refused")
	}
	c := cfg(t.TempDir())
	c.Driver = nil
	if _, err := Build(c); err == nil {
		t.Fatal("a driver is required")
	}
	c = cfg(t.TempDir())
	c.Engine = nil
	if _, err := Build(c); err == nil {
		t.Fatal("an engine is required")
	}
}

func TestBuildCreatesPrivateDirectoriesAndALogFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	d, err := Build(cfg(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Shutdown(context.Background())
	for _, sub := range []string{"", "core", "logs"} {
		st, err := os.Stat(filepath.Join(dir, sub))
		if err != nil || !st.IsDir() || st.Mode().Perm()&0o077 != 0 {
			t.Fatalf("%q: %v %v", sub, st, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "logs", "agent.log")); err != nil {
		t.Fatal(err)
	}
}

func TestStartTwiceIsRefusedAndShutdownIsSafeAtAnyTime(t *testing.T) {
	d, err := Build(cfg(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Shutdown before Start must not hang or panic.
	d2, err := Build(cfg(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if err := d2.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown before start: %v", err)
	}
	if err := d.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := d.Start(ctx); err == nil {
		t.Fatal("a second Start must be refused")
	}
	if err := d.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := d.Shutdown(ctx); err != nil {
		t.Fatalf("a second Shutdown must be harmless: %v", err)
	}
}

func TestAgentLogLinesLandInTheFileAndTheHub(t *testing.T) {
	dir := t.TempDir()
	d, err := Build(cfg(dir))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := d.Start(ctx); err != nil {
		t.Fatal(err)
	}
	<-d.Controller.Ready()
	if err := d.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "logs", "agent.log"))
	if err != nil || len(raw) == 0 {
		t.Fatalf("the agent log file is empty: %v", err)
	}
	if len(d.Hub.Tail(100, "debug")) == 0 {
		t.Fatal("the log hub saw nothing")
	}
}

func TestRotatingFileKeepsTheNewestFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.log")
	r, err := openRotating(path, 100, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for i := 0; i < 20; i++ {
		line := fmt.Sprintf("line %02d %s\n", i, strings.Repeat("x", 30))
		if _, err := r.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Dir(path)
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 3 {
		t.Fatalf("files = %v, want the log plus two rotated copies", names)
	}
	cur, _ := os.ReadFile(path)
	if !strings.Contains(string(cur), "line 19") {
		t.Fatalf("the newest line must be in the current file: %q", cur)
	}
	for _, n := range names {
		st, _ := os.Stat(filepath.Join(dir, n))
		if st.Size() > 100+60 {
			t.Fatalf("%s is %d bytes, over the cap", n, st.Size())
		}
	}
	// The oldest lines are gone.
	all := ""
	for _, n := range names {
		b, _ := os.ReadFile(filepath.Join(dir, n))
		all += string(b)
	}
	if strings.Contains(all, "line 00") {
		t.Fatal("the oldest line should have been rotated away")
	}
	if _, err := r.Write([]byte("after close")); err != nil {
		t.Fatal(err)
	}
	r.Close()
	if _, err := r.Write([]byte("x")); err == nil {
		t.Fatal("writing to a closed log must fail, not panic")
	}
}
