package controller

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"eggbox/internal/agent/store"
	"eggbox/internal/agentapi"
)

func noResume(o *hopts) { o.settings = func(s *store.Settings) { s.ResumeAfterCrash = false } }

func TestCrashLeftoversAreCleanedUpBeforeAnythingElse(t *testing.T) {
	h := newHarness(t, noResume)
	h.connected(tunConfig)
	h.crash()
	if len(h.drv.Resolvers()) != 1 || len(h.drv.Interfaces()) != 1 {
		t.Fatal("precondition: a crash leaves the resolver and interface behind")
	}

	h.restart()
	st := h.c.Status()
	if st.Cleanup != agentapi.CleanupState_CLEANUP_STATE_RECOVERED || !strings.Contains(st.CleanupDetail, "released 2") ||
		st.Service != agentapi.ServiceState_SERVICE_STATE_READY {
		t.Fatalf("status = %+v", st)
	}
	h.requireClean()
	settle()
	if len(h.eng.Started()) != 0 {
		t.Fatal("resume is off: the core must not start by itself")
	}
	if st := h.c.Status(); st.IntentConnected || h.intent.Load().Connected {
		t.Fatal("not resuming means the stale intent is dropped")
	}
}

func TestCrashIsFollowedByAResumeWhenEnabled(t *testing.T) {
	h := newHarness(t) // resume after crash is on by default
	h.connected(tunConfig)
	h.crash()
	h.restart()
	st := h.waitConn(agentapi.ConnectionState_CONNECTION_STATE_CONNECTED)
	if st.ActiveRevision != 1 || !st.IntentConnected {
		t.Fatalf("status = %+v", st)
	}
	// Cleanup came first: exactly one resolver and one interface, from the new session.
	h.requireOwns()
	if st.Cleanup != agentapi.CleanupState_CLEANUP_STATE_IDLE {
		t.Fatalf("cleanup = %v after a successful resume", st.Cleanup)
	}
}

func TestCleanupHappensBeforeResumeStarts(t *testing.T) {
	h := newHarness(t)
	h.connected(tunConfig)
	h.crash()
	h.restart()
	h.waitConn(agentapi.ConnectionState_CONNECTION_STATE_CONNECTED)
	calls := h.drv.Calls()
	releaseAt, activateAt := -1, -1
	for i, c := range calls {
		if strings.HasPrefix(c, "release") && releaseAt < 0 {
			releaseAt = i
		}
		if strings.HasPrefix(c, "activate") && activateAt < 0 {
			activateAt = i
		}
	}
	// The harness shares one OS across restarts, so the first activate belongs
	// to the crashed session; look for the second one.
	second := -1
	for i := activateAt + 1; i < len(calls); i++ {
		if strings.HasPrefix(calls[i], "activate") {
			second = i
			break
		}
	}
	if releaseAt < 0 || second < 0 || releaseAt > second {
		t.Fatalf("leftovers must be released before the new session is configured: %v", calls)
	}
}

func TestCorruptLedgerFallsBackToSweeping(t *testing.T) {
	h := newHarness(t, noResume)
	h.connected(tunConfig)
	h.crash()
	if err := os.WriteFile(filepath.Join(h.dir, "ledger.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.restart()
	if h.drv.Sweeps() != 1 {
		t.Fatalf("sweeps = %d", h.drv.Sweeps())
	}
	st := h.c.Status()
	if st.Cleanup != agentapi.CleanupState_CLEANUP_STATE_RECOVERED {
		t.Fatalf("status = %+v", st)
	}
	h.requireClean()
}

func TestFailedSweepIsVisible(t *testing.T) {
	h := newHarness(t, noResume)
	h.connected(tunConfig)
	h.crash()
	if err := os.WriteFile(filepath.Join(h.dir, "ledger.json"), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.drv.FailSweep(errors.New("permission denied"))
	h.restart()
	st := h.c.Status()
	if st.Cleanup != agentapi.CleanupState_CLEANUP_STATE_FAILED || st.Service != agentapi.ServiceState_SERVICE_STATE_DEGRADED ||
		!strings.Contains(st.CleanupDetail, "permission denied") {
		t.Fatalf("status = %+v", st)
	}
}

func TestRecoveryLeavesForeignResourcesAlone(t *testing.T) {
	h := newHarness(t, noResume)
	h.connected(tunConfig)
	h.crash()
	// After the crash another program took over the same resolver entry.
	h.drv.PlantResolver("sim:resolver:utun-fake", "someone-elses-token")
	h.restart()
	if got := h.drv.Resolvers(); len(got) != 1 {
		t.Fatalf("the foreign resolver entry must survive: %v", got)
	}
	st := h.c.Status()
	if st.Cleanup != agentapi.CleanupState_CLEANUP_STATE_RECOVERED || !strings.Contains(st.CleanupDetail, "not ours 1") {
		t.Fatalf("status = %+v", st)
	}
}

func TestFailedCleanupBlocksConnectingUntilRetried(t *testing.T) {
	h := newHarness(t, noResume)
	h.connected(tunConfig)
	h.crash()
	// A genuinely stuck system: neither the ledger-driven release nor the
	// driver's own undo can remove the resolver entry.
	h.drv.FailRelease("resolver", errors.New("scutil: busy"))
	h.drv.FailDeactivate(errors.New("scutil: busy"))
	h.restart()

	st := h.c.Status()
	if st.Cleanup != agentapi.CleanupState_CLEANUP_STATE_FAILED || st.Service != agentapi.ServiceState_SERVICE_STATE_DEGRADED ||
		!strings.Contains(st.CleanupDetail, "scutil: busy") {
		t.Fatalf("status = %+v", st)
	}
	if len(h.led.Entries()) == 0 {
		t.Fatal("failed entries must stay in the ledger so they remain diagnosable")
	}

	res := h.connect()
	if res.Ok || res.ErrorCode != agentapi.ErrorCode_ERROR_CODE_CLEANUP_FAILED {
		t.Fatalf("connecting on top of unreleased resources must be refused: %+v", res)
	}
	if len(h.eng.Started()) != 0 {
		t.Fatal("the core must not start")
	}

	h.drv.FailRelease("resolver", nil)
	h.drv.FailDeactivate(nil)
	if res := h.c.RetryCleanup(context.Background(), &agentapi.RetryCleanupRequest{RequestId: rid()}); !res.Ok {
		t.Fatalf("retry: %+v", res)
	}
	st = h.c.Status()
	if st.Cleanup != agentapi.CleanupState_CLEANUP_STATE_IDLE || st.Service != agentapi.ServiceState_SERVICE_STATE_READY {
		t.Fatalf("status after retry = %+v", st)
	}
	h.requireClean()
	h.mustConnect()
}

func TestDisconnectReportsAnIncompleteCleanup(t *testing.T) {
	h := newHarness(t)
	h.connected(tunConfig)
	h.drv.FailRelease("resolver", errors.New("scutil: busy"))
	h.drv.FailDeactivate(errors.New("scutil: busy"))
	res := h.disconnect()
	if res.Ok || res.ErrorCode != agentapi.ErrorCode_ERROR_CODE_CLEANUP_FAILED {
		t.Fatalf("result = %+v", res)
	}
	st := h.c.Status()
	// The user is disconnected as far as the core is concerned, but the problem stays visible.
	if st.Connection != agentapi.ConnectionState_CONNECTION_STATE_DISCONNECTED || st.Cleanup != agentapi.CleanupState_CLEANUP_STATE_FAILED ||
		st.Service != agentapi.ServiceState_SERVICE_STATE_DEGRADED {
		t.Fatalf("status = %+v", st)
	}
	if h.eng.Running() {
		t.Fatal("the core must be stopped even when system cleanup fails")
	}
	h.drv.FailRelease("resolver", nil)
	h.drv.FailDeactivate(nil)
	if res := h.c.RetryCleanup(context.Background(), &agentapi.RetryCleanupRequest{RequestId: rid()}); !res.Ok {
		t.Fatalf("retry: %+v", res)
	}
	h.requireClean()
}

func TestRetryCleanupIsRefusedWhileConnected(t *testing.T) {
	h := newHarness(t)
	h.connected(tunConfig)
	res := h.c.RetryCleanup(context.Background(), &agentapi.RetryCleanupRequest{RequestId: rid()})
	if res.Ok || res.ErrorCode != agentapi.ErrorCode_ERROR_CODE_INVALID_ARGUMENT {
		t.Fatalf("result = %+v", res)
	}
	h.requireOwns() // the live session was not disturbed
}

func TestUpgradeResumesTheConnection(t *testing.T) {
	h := newHarness(t, noResume) // proves the upgrade path does not depend on the crash setting
	h.connected(tunConfig)

	res := h.c.PrepareUpgrade(context.Background(), &agentapi.PrepareUpgradeRequest{RequestId: rid()})
	if !res.Ok {
		t.Fatalf("prepare: %+v", res)
	}
	if st := h.c.Status(); st.Connection != agentapi.ConnectionState_CONNECTION_STATE_DISCONNECTED {
		t.Fatalf("status = %+v", st)
	}
	if !h.intent.Load().ResumeAfterUpgrade {
		t.Fatal("the intent to resume must be recorded")
	}
	h.requireClean() // the installer may now replace the service

	ctx, cancel := context.WithTimeout(context.Background(), 5e9)
	defer cancel()
	if err := h.c.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	h.abandoned = true

	h.restart() // the new version of the agent
	h.waitConn(agentapi.ConnectionState_CONNECTION_STATE_CONNECTED)
	if h.intent.Load().ResumeAfterUpgrade {
		t.Fatal("the flag must be consumed so it never resumes twice")
	}
}

func TestPrepareUpgradeWhileDisconnectedResumesNothing(t *testing.T) {
	h := newHarness(t)
	h.mustApply(tunConfig, 0)
	if res := h.c.PrepareUpgrade(context.Background(), &agentapi.PrepareUpgradeRequest{RequestId: rid()}); !res.Ok {
		t.Fatalf("prepare: %+v", res)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5e9)
	defer cancel()
	_ = h.c.Shutdown(ctx)
	h.abandoned = true
	h.restart()
	settle()
	if len(h.eng.Started()) != 0 {
		t.Fatal("nothing to resume")
	}
}

func TestShutdownTearsDownEvenWhenTheCallerGivesUp(t *testing.T) {
	// With a context that is already done, whether the queued teardown or the
	// direct fallback does the work varies; the machine must be clean either way.
	for i := 0; i < 8; i++ {
		h := newHarness(t)
		h.connected(tunConfig)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_ = h.c.Shutdown(ctx)
		h.abandoned = true
		if h.eng.Running() {
			t.Fatalf("iteration %d: the core is still running", i)
		}
		h.requireClean()
	}
}

func TestStaleIntentFromACleanShutdownIsDropped(t *testing.T) {
	h := newHarness(t)
	h.connected(tunConfig)
	ctx, cancel := context.WithTimeout(context.Background(), 5e9)
	defer cancel()
	if err := h.c.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	h.abandoned = true
	h.restart()
	settle()
	st := h.c.Status()
	if st.Connection != agentapi.ConnectionState_CONNECTION_STATE_DISCONNECTED || st.IntentConnected || h.intent.Load().Connected {
		t.Fatalf("a clean shutdown is not a crash: %+v", st)
	}
	if len(h.eng.Started()) != 0 {
		t.Fatal("auto connect is off, so nothing starts")
	}
}
