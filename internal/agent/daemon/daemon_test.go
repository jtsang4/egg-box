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
	"github.com/jtsang4/sing-box-ui/internal/agent/store"
	"github.com/jtsang4/sing-box-ui/internal/brand"
	"github.com/jtsang4/sing-box-ui/internal/macsys"
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

// seedIntent leaves the data folder the way a stopped version left it.
func seedIntent(t *testing.T, dir string, in store.Intent) {
	t.Helper()
	f, err := store.OpenFile(dir, "intent.json", store.Intent{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Update(func(i *store.Intent) error { *i = in; return nil }); err != nil {
		t.Fatal(err)
	}
}

// intentOfTheNextStart builds the agent over dir, as the new version does, and
// says what it will do about the connection.
func intentOfTheNextStart(t *testing.T, dir string) store.Intent {
	t.Helper()
	d, err := Build(cfg(dir))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	f, err := store.OpenFile(dir, "intent.json", store.Intent{})
	if err != nil {
		t.Fatal(err)
	}
	return f.Load()
}

func TestAnInstallersMarkerBringsBackTheConnectionThePersonHad(t *testing.T) {
	dir := t.TempDir()
	seedIntent(t, dir, store.Intent{Connected: true})
	marker := filepath.Join(dir, UpgradeMarker)
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if in := intentOfTheNextStart(t, dir); !in.Connected || !in.ResumeAfterUpgrade {
		t.Fatalf("the connection must be restored: %+v", in)
	}
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Fatalf("the marker must be used up: %v", err)
	}
}

func TestAnInstallersMarkerWhileNotConnectedBringsBackNothing(t *testing.T) {
	dir := t.TempDir()
	seedIntent(t, dir, store.Intent{Connected: false})
	marker := filepath.Join(dir, UpgradeMarker)
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if in := intentOfTheNextStart(t, dir); in.ResumeAfterUpgrade {
		t.Fatalf("nothing was connected, nothing to restore: %+v", in)
	}
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Fatalf("the marker must be used up: %v", err)
	}
}

func TestWithoutAMarkerAStopIsNotAnUpgrade(t *testing.T) {
	dir := t.TempDir()
	seedIntent(t, dir, store.Intent{Connected: true})
	if in := intentOfTheNextStart(t, dir); in.ResumeAfterUpgrade {
		t.Fatalf("a start with no marker must not resume by itself: %+v", in)
	}
}

func TestOnlyAPlainFileCountsAsAMarker(t *testing.T) {
	t.Run("a folder", func(t *testing.T) {
		dir := t.TempDir()
		seedIntent(t, dir, store.Intent{Connected: true})
		marker := filepath.Join(dir, UpgradeMarker)
		if err := os.Mkdir(marker, 0o700); err != nil {
			t.Fatal(err)
		}
		if in := intentOfTheNextStart(t, dir); in.ResumeAfterUpgrade {
			t.Fatalf("a folder is not a marker: %+v", in)
		}
		if _, err := os.Lstat(marker); !os.IsNotExist(err) {
			t.Fatalf("the folder must be cleared away: %v", err)
		}
	})
	t.Run("a link", func(t *testing.T) {
		dir := t.TempDir()
		seedIntent(t, dir, store.Intent{Connected: true})
		elsewhere := filepath.Join(t.TempDir(), "elsewhere")
		if err := os.WriteFile(elsewhere, []byte("mine"), 0o600); err != nil {
			t.Fatal(err)
		}
		marker := filepath.Join(dir, UpgradeMarker)
		if err := os.Symlink(elsewhere, marker); err != nil {
			t.Skipf("no symbolic links here: %v", err)
		}
		if in := intentOfTheNextStart(t, dir); in.ResumeAfterUpgrade {
			t.Fatalf("a link is not a marker: %+v", in)
		}
		if _, err := os.Lstat(marker); !os.IsNotExist(err) {
			t.Fatalf("the link must be removed: %v", err)
		}
		if b, err := os.ReadFile(elsewhere); err != nil || string(b) != "mine" {
			t.Fatalf("what the link pointed at must be left alone: %q %v", b, err)
		}
	})
}

// The installers cannot import this package, so they spell out the marker's
// name and where the data folder is. If either side changes, the installers
// would quietly stop restoring connections: this is the alarm.
func TestTheInstallersLeaveTheMarkerWhereTheAgentLooksForIt(t *testing.T) {
	read := func(parts ...string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(append([]string{"..", "..", ".."}, parts...)...))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	mac := read("packaging", "macos", "scripts", "preinstall")
	if !strings.Contains(mac, UpgradeMarker) || !strings.Contains(mac, macsys.DataDir) {
		t.Errorf("the macOS preinstall must leave %q in %q", UpgradeMarker, macsys.DataDir)
	}
	post := read("packaging", "macos", "scripts", "postinstall")
	if !strings.Contains(post, UpgradeMarker) || !strings.Contains(post, macsys.DataDir) {
		t.Errorf("the macOS postinstall must look for %q in %q", UpgradeMarker, macsys.DataDir)
	}
	win := read("packaging", "windows", "installer.nsi")
	// ProgramData\<DataDirName>\agent, which is what winsys.DataDir is.
	want := `$APPDATA\${SLUG}\agent`
	if !strings.Contains(win, UpgradeMarker) || !strings.Contains(win, want) || !strings.Contains(win, "SetShellVarContext all") {
		t.Errorf("the Windows installer must leave %q in %q (all users' application data)", UpgradeMarker, want)
	}
	if !strings.Contains(win, `!define SLUG      "`+brand.DataDirName+`"`) {
		t.Errorf("the installer's SLUG must be the data folder's name %q", brand.DataDirName)
	}
	// It only leaves the note when the service runs, and asks `agentd status`.
	if !strings.Contains(win, `!define SERVICE   "`+brand.WindowsServiceName+`"`) {
		t.Errorf("the installer must know the service as %q", brand.WindowsServiceName)
	}
	if !strings.Contains(read("cmd", "agentd", "platform_windows.go"), `running %t`) || !strings.Contains(win, `"running true"`) {
		t.Errorf("the installer reads the line `running true` that `agentd status` prints")
	}
}
