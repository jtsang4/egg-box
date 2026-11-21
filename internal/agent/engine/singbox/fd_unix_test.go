//go:build darwin || linux

package singbox_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"eggbox/internal/agent/engine"
	"eggbox/internal/agent/engine/singbox"
	"eggbox/internal/agent/policy"
	"eggbox/internal/testsupport/srs"
)

func openFDCount(t *testing.T) int {
	t.Helper()
	dir := "/proc/self/fd"
	if runtime.GOOS == "darwin" {
		dir = "/dev/fd"
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	return len(entries)
}

func settledFDCount(t *testing.T) int {
	t.Helper()
	// Give async watcher close goroutines (kqueue readEvents) a moment to close
	// their kqueue and close-pipe descriptors after Stop/Close returns.
	deadline := time.Now().Add(2 * time.Second)
	prev := openFDCount(t)
	stable := 0
	for time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		cur := openFDCount(t)
		if cur == prev {
			stable++
			if stable >= 3 {
				return cur
			}
		} else {
			prev = cur
			stable = 0
		}
	}
	return openFDCount(t)
}

func preparedWithLocalRuleSets(t *testing.T, count int) (engine.Prepared, string) {
	t.Helper()
	dir := t.TempDir()
	resDir := filepath.Join(dir, "resources")
	if err := os.MkdirAll(resDir, 0o700); err != nil {
		t.Fatal(err)
	}
	resources := make(map[string]bool, count)
	ruleSets := make([]map[string]any, 0, count)
	rules := make([]map[string]any, 0, count)
	for i := range count {
		name := fmt.Sprintf("rs-%02d.srs", i)
		tag := fmt.Sprintf("rs-%02d", i)
		data := srs.DomainSuffixes(t, fmt.Sprintf("example-%02d.invalid", i))
		if err := os.WriteFile(filepath.Join(resDir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
		resources[name] = true
		ruleSets = append(ruleSets, map[string]any{
			"type":   "local",
			"tag":    tag,
			"format": "binary",
			"path":   "res:" + name,
		})
		rules = append(rules, map[string]any{
			"rule_set": []string{tag},
			"outbound": "direct",
		})
	}
	port := freePort(t)
	cfg := map[string]any{
		"dns": map[string]any{
			"servers": []any{
				map[string]any{"type": "udp", "tag": "dns-direct", "server": "127.0.0.1", "server_port": 53},
			},
			"rules": []any{
				map[string]any{"rule_set": []string{"rs-00"}, "server": "dns-direct"},
			},
			"final": "dns-direct",
		},
		"inbounds": []any{
			map[string]any{"type": "mixed", "tag": "mixed-in", "listen": "127.0.0.1", "listen_port": port},
		},
		"outbounds": []any{
			map[string]any{"type": "direct", "tag": "direct"},
		},
		"route": map[string]any{
			"auto_detect_interface": true,
			"rule_set":              ruleSets,
			"rules":                 rules,
			"final":                 "direct",
		},
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	pol := policy.New(singbox.PolicyOptions(singbox.RegistryOptions{}))
	env := policy.Env{
		ResourceDir: resDir,
		Resources:   resources,
		CachePath:   filepath.Join(dir, "cache.db"),
		LogLevel:    "warn",
	}
	res := pol.Normalize(raw, env)
	if res.HasErrors() {
		t.Fatalf("policy rejected test config: %v", res.Errors())
	}
	return engine.Prepared{Revision: 1, JSON: res.JSON, HasTun: res.HasTun, TunTag: res.TunTag}, resDir
}

func TestLocalRuleSetsDoNotLeakFDs(t *testing.T) {
	const (
		ruleSetCount = 10
		cycles       = 5
	)
	prepared, _ := preparedWithLocalRuleSets(t, ruleSetCount)
	eng := singbox.New(singbox.RegistryOptions{})
	t.Cleanup(func() { stop(t, eng) })
	ctx := context.Background()

	// Warm up one Check and one Start/Stop cycle so one-time process descriptors
	// (e.g. runtime/library initializers) are already open before baseline.
	if err := eng.Check(ctx, prepared); err != nil {
		t.Fatalf("warm-up Check: %v", err)
	}
	if err := eng.Start(ctx, prepared); err != nil {
		t.Fatalf("warm-up Start: %v", err)
	}
	if err := eng.Stop(ctx); err != nil {
		t.Fatalf("warm-up Stop: %v", err)
	}

	baseline := settledFDCount(t)
	t.Logf("baseline FDs: %d", baseline)

	for i := range cycles {
		if err := eng.Check(ctx, prepared); err != nil {
			t.Fatalf("Check cycle %d: %v", i, err)
		}
	}
	afterCheck := settledFDCount(t)
	t.Logf("FDs after %d Checks: %d (delta %+d)", cycles, afterCheck, afterCheck-baseline)
	if afterCheck > baseline+8 {
		t.Errorf("Check leaked file descriptors: grew from %d to %d over %d cycles with %d local rule-sets",
			baseline, afterCheck, cycles, ruleSetCount)
	}

	startBase := settledFDCount(t)
	for i := range cycles {
		sctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		err := eng.Start(sctx, prepared)
		cancel()
		if err != nil {
			t.Fatalf("Start cycle %d: %v", i, err)
		}
		if i == 0 {
			live := openFDCount(t)
			t.Logf("live FDs while running with %d local rule-sets: %d (delta %+d)", ruleSetCount, live, live-startBase)
		}
		stopCtx, stopCancel := context.WithTimeout(ctx, 20*time.Second)
		err = eng.Stop(stopCtx)
		stopCancel()
		if err != nil {
			t.Fatalf("Stop cycle %d: %v", i, err)
		}
	}
	afterStartStop := settledFDCount(t)
	t.Logf("FDs after %d Start/Stop cycles: %d (delta %+d)", cycles, afterStartStop, afterStartStop-startBase)
	if afterStartStop > startBase+8 {
		t.Errorf("Start/Stop leaked file descriptors: grew from %d to %d over %d cycles with %d local rule-sets",
			startBase, afterStartStop, cycles, ruleSetCount)
	}
}
