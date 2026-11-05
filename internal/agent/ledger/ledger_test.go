package ledger

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func openTemp(t *testing.T) (*Ledger, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return l, path
}

func TestFreshLedgerNeedsNoRecovery(t *testing.T) {
	l, _ := openTemp(t)
	if l.Recovery().Needed {
		t.Fatal("a fresh ledger must not ask for recovery")
	}
	if l.Phase() != PhaseClean {
		t.Fatalf("phase = %q", l.Phase())
	}
}

func TestEntriesSurviveRestart(t *testing.T) {
	l, path := openTemp(t)
	if err := l.BeginSession(Session{ID: "s1", PID: 42, Started: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := l.Add(Entry{ID: "a", Kind: KindTunInterface, Key: "utun7", Created: time.Unix(100, 0)}); err != nil {
		t.Fatal(err)
	}
	if err := l.Add(Entry{ID: "b", Kind: KindDNS, Key: "State:/Network/Service/x/DNS", Token: "tok", Created: time.Unix(200, 0)}); err != nil {
		t.Fatal(err)
	}

	// Simulate a crash: reopen without ever calling MarkClean.
	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	rec := again.Recovery()
	if !rec.Needed || rec.Corrupt {
		t.Fatalf("recovery = %+v", rec)
	}
	if rec.Previous.PID != 42 || len(rec.Entries) != 2 {
		t.Fatalf("recovery lost data: %+v", rec)
	}
	entries := again.Entries()
	if entries[0].ID != "b" || entries[1].ID != "a" {
		t.Fatalf("entries must be newest first, got %v then %v", entries[0].ID, entries[1].ID)
	}
}

func TestAddIsIdempotentPerID(t *testing.T) {
	l, _ := openTemp(t)
	_ = l.Add(Entry{ID: "a", Kind: KindDNS, Key: "k1"})
	_ = l.Add(Entry{ID: "a", Kind: KindDNS, Key: "k2"})
	got := l.Entries()
	if len(got) != 1 || got[0].Key != "k2" {
		t.Fatalf("entries = %+v", got)
	}
}

func TestAddValidatesEntry(t *testing.T) {
	l, _ := openTemp(t)
	if err := l.Add(Entry{ID: "a"}); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestMarkCleanRefusesWhileEntriesRemain(t *testing.T) {
	l, _ := openTemp(t)
	_ = l.Add(Entry{ID: "a", Kind: KindFile, Key: "/x"})
	if err := l.MarkClean(); err == nil {
		t.Fatal("MarkClean must refuse with entries recorded")
	}
	_ = l.Remove("a")
	if err := l.MarkClean(); err != nil {
		t.Fatal(err)
	}
	if l.Phase() != PhaseClean {
		t.Fatal("phase should be clean")
	}
}

func TestCleanShutdownLeavesNoRecovery(t *testing.T) {
	l, path := openTemp(t)
	_ = l.BeginSession(Session{ID: "s", PID: 1})
	_ = l.Add(Entry{ID: "a", Kind: KindFile, Key: "/x"})
	_ = l.Remove("a")
	if err := l.MarkClean(); err != nil {
		t.Fatal(err)
	}
	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if again.Recovery().Needed {
		t.Fatal("clean shutdown must not trigger recovery")
	}
}

func TestActivePhaseAloneTriggersRecovery(t *testing.T) {
	// The agent crashed after BeginSession but before recording anything: the
	// resource may have been created without a record, so recovery must still run.
	l, path := openTemp(t)
	_ = l.BeginSession(Session{ID: "s", PID: 1})
	again, _ := Open(path)
	if !again.Recovery().Needed {
		t.Fatal("an active phase must trigger recovery")
	}
}

func TestCorruptLedgerIsQuarantined(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ledger.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	rec := l.Recovery()
	if !rec.Needed || !rec.Corrupt {
		t.Fatalf("recovery = %+v", rec)
	}
	matches, _ := filepath.Glob(path + ".corrupt-*")
	if len(matches) != 1 {
		t.Fatalf("expected the corrupt file to be preserved, got %v", matches)
	}
	if l.Phase() != PhaseActive {
		t.Fatal("a corrupt ledger must stay active until a sweep succeeds")
	}
}

func TestFilePermissionsAreOwnerOnly(t *testing.T) {
	l, path := openTemp(t)
	_ = l.Add(Entry{ID: "a", Kind: KindFile, Key: "/x"})
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("ledger is accessible to others: %v", info.Mode())
	}
}

type fakeReleaser struct {
	outcomes map[string]Outcome
	errs     map[string]error
	order    []string
}

func (f *fakeReleaser) Release(_ context.Context, e Entry) (Outcome, error) {
	f.order = append(f.order, e.ID)
	if err := f.errs[e.ID]; err != nil {
		return 0, err
	}
	if o, ok := f.outcomes[e.ID]; ok {
		return o, nil
	}
	return Released, nil
}

func TestCleanupReleasesNewestFirstAndMarksClean(t *testing.T) {
	l, path := openTemp(t)
	_ = l.BeginSession(Session{ID: "s", PID: 1})
	_ = l.Add(Entry{ID: "iface", Kind: KindTunInterface, Key: "utun3", Created: time.Unix(1, 0)})
	_ = l.Add(Entry{ID: "dns", Kind: KindDNS, Key: "k", Created: time.Unix(2, 0)})
	_ = l.Add(Entry{ID: "gone", Kind: KindFile, Key: "f", Created: time.Unix(3, 0)})
	_ = l.Add(Entry{ID: "foreign", Kind: KindDNS, Key: "k2", Created: time.Unix(4, 0)})

	rel := &fakeReleaser{outcomes: map[string]Outcome{"gone": AlreadyGone, "foreign": NotOurs}}
	rep := l.Cleanup(context.Background(), rel)

	if !rep.OK() || rep.Total() != 4 {
		t.Fatalf("report = %+v", rep)
	}
	if got := strings.Join(rel.order, ","); got != "foreign,gone,dns,iface" {
		t.Fatalf("release order = %s", got)
	}
	if len(rep.Released) != 2 || len(rep.Gone) != 1 || len(rep.NotOurs) != 1 {
		t.Fatalf("report = %+v", rep)
	}
	if l.Phase() != PhaseClean || len(l.Entries()) != 0 {
		t.Fatal("ledger should be clean after a fully successful pass")
	}
	again, _ := Open(path)
	if again.Recovery().Needed {
		t.Fatal("a clean ledger must not need recovery after restart")
	}
}

func TestCleanupKeepsFailedEntries(t *testing.T) {
	l, _ := openTemp(t)
	_ = l.BeginSession(Session{ID: "s", PID: 1})
	_ = l.Add(Entry{ID: "a", Kind: KindDNS, Key: "k", Created: time.Unix(1, 0)})
	_ = l.Add(Entry{ID: "b", Kind: KindTunInterface, Key: "utun3", Created: time.Unix(2, 0)})

	rel := &fakeReleaser{errs: map[string]error{"b": errors.New("device busy")}}
	rep := l.Cleanup(context.Background(), rel)

	if rep.OK() {
		t.Fatal("expected a failed pass")
	}
	if len(rep.Failed) != 1 || rep.Failed[0].Entry.ID != "b" {
		t.Fatalf("report = %+v", rep)
	}
	left := l.Entries()
	if len(left) != 1 || left[0].ID != "b" {
		t.Fatalf("the failed entry must stay recorded, got %+v", left)
	}
	if l.Phase() != PhaseActive {
		t.Fatal("ledger must stay active after a failed pass")
	}
	if !strings.Contains(rep.Summary(), "device busy") {
		t.Fatalf("summary should name the failure: %s", rep.Summary())
	}

	// A later retry succeeds and finishes the job.
	rel.errs = nil
	if rep := l.Cleanup(context.Background(), rel); !rep.OK() {
		t.Fatalf("retry failed: %+v", rep)
	}
	if l.Phase() != PhaseClean {
		t.Fatal("ledger should be clean after the retry")
	}
}

func TestCleanupHonoursCancellation(t *testing.T) {
	l, _ := openTemp(t)
	_ = l.Add(Entry{ID: "a", Kind: KindDNS, Key: "k"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rep := l.Cleanup(ctx, &fakeReleaser{})
	if rep.OK() || len(l.Entries()) != 1 {
		t.Fatalf("cancelled cleanup must not release anything: %+v", rep)
	}
}

func TestConcurrentAddsAreSafe(t *testing.T) {
	l, path := openTemp(t)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := string(rune('a' + i))
			if err := l.Add(Entry{ID: id, Kind: KindFile, Key: id}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(again.Entries()); got != 20 {
		t.Fatalf("entries after reopen = %d", got)
	}
}
