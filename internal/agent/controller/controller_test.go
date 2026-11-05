package controller

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jtsang4/sing-box-ui/internal/agent/engine"
	"github.com/jtsang4/sing-box-ui/internal/agent/platform"
	"github.com/jtsang4/sing-box-ui/internal/agent/routecheck"
	"github.com/jtsang4/sing-box-ui/internal/agent/store"
	"github.com/jtsang4/sing-box-ui/internal/agentapi"
)

func TestStartsReadyAndIdle(t *testing.T) {
	h := newHarness(t)
	st := h.c.Status()
	if st.Service != agentapi.ServiceState_SERVICE_STATE_READY ||
		st.Connection != agentapi.ConnectionState_CONNECTION_STATE_DISCONNECTED ||
		st.Cleanup != agentapi.CleanupState_CLEANUP_STATE_IDLE || st.HasConfig {
		t.Fatalf("status = %+v", st)
	}
	if st.AgentVersion != "test" || st.CoreVersion != "fake-1.0" {
		t.Fatalf("versions = %q / %q", st.AgentVersion, st.CoreVersion)
	}
}

func TestConnectWithoutConfig(t *testing.T) {
	h := newHarness(t)
	res := h.connect()
	if res.Ok || res.ErrorCode != agentapi.ErrorCode_ERROR_CODE_NO_CONFIG {
		t.Fatalf("result = %+v", res)
	}
}

func TestApplyWhileDisconnectedOnlyStores(t *testing.T) {
	h := newHarness(t)
	res := h.mustApply(tunConfig, 0)
	if res.Revision != 1 || !strings.Contains(res.Message, "stored revision 1") {
		t.Fatalf("result = %+v", res)
	}
	if len(h.eng.Started()) != 0 {
		t.Fatal("storing a configuration must not start the core")
	}
	if st := h.c.Status(); st.StoredRevision != 1 || !st.HasConfig || st.ActiveRevision != 0 {
		t.Fatalf("status = %+v", st)
	}
}

func TestConnectAndDisconnectWithTun(t *testing.T) {
	h := newHarness(t)
	h.connected(tunConfig)

	st := h.c.Status()
	if st.ActiveRevision != 1 || st.TunInterface != "utun-fake" || !st.IntentConnected || st.ConnectedSinceUnix == 0 {
		t.Fatalf("status = %+v", st)
	}
	h.requireOwns()

	// The core received a TUN with the platform plan, never the GUI's choices.
	got := h.coreJSON()
	for _, want := range []string{`"172.19.0.1/30"`, `"auto_route":true`, `"stack":"system"`} {
		if !strings.Contains(got, want) {
			t.Errorf("core config misses %s:\n%s", want, got)
		}
	}
	if !h.eng.Current().HasTun || h.eng.Current().TunTag != "tun-in" {
		t.Fatalf("prepared = %+v", h.eng.Current())
	}

	if res := h.disconnect(); !res.Ok {
		t.Fatalf("disconnect: %+v", res)
	}
	st = h.c.Status()
	if st.Connection != agentapi.ConnectionState_CONNECTION_STATE_DISCONNECTED || st.IntentConnected || st.ActiveRevision != 0 ||
		st.Cleanup != agentapi.CleanupState_CLEANUP_STATE_IDLE || st.TunInterface != "" {
		t.Fatalf("status after disconnect = %+v", st)
	}
	h.requireClean()
}

func TestDisconnectOrderRestoresDNSBeforeStoppingTheCore(t *testing.T) {
	h := newHarness(t)
	h.connected(tunConfig)
	h.disconnect()
	calls := h.drv.Calls()
	engineCalls := h.eng.Calls()
	// deactivate (resolver back) must have happened, and the core stopped afterwards.
	if !contains(calls, "deactivate") {
		t.Fatalf("driver calls = %v", calls)
	}
	if !contains(engineCalls, "stop") {
		t.Fatalf("engine calls = %v", engineCalls)
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func TestConnectIsIdempotentPerRequestID(t *testing.T) {
	h := newHarness(t)
	h.mustApply(tunConfig, 0)
	req := &agentapi.ConnectRequest{RequestId: "same-id"}
	first := h.c.Connect(context.Background(), req)
	second := h.c.Connect(context.Background(), req)
	if !first.Ok || !second.Ok {
		t.Fatalf("results: %+v %+v", first, second)
	}
	if first.Replayed || !second.Replayed {
		t.Fatalf("replayed flags: %v %v", first.Replayed, second.Replayed)
	}
	if n := len(h.eng.Started()); n != 1 {
		t.Fatalf("the core must start once, started %d times", n)
	}
}

func TestSecondConnectWhileConnectedIsANoop(t *testing.T) {
	h := newHarness(t)
	h.connected(tunConfig)
	res := h.connect()
	if !res.Ok || !strings.Contains(res.Message, "already connected") {
		t.Fatalf("result = %+v", res)
	}
	if n := len(h.eng.Started()); n != 1 {
		t.Fatalf("started %d times", n)
	}
}

func TestApplyRevisionConflict(t *testing.T) {
	h := newHarness(t)
	h.mustApply(tunConfig, 0)
	res := h.apply(tunConfig, 0) // stale: revision 1 is stored
	if res.Ok || res.ErrorCode != agentapi.ErrorCode_ERROR_CODE_REVISION_CONFLICT || res.Details["stored_revision"] != "1" {
		t.Fatalf("result = %+v", res)
	}
	if h.st.CurrentN() != 1 {
		t.Fatal("a conflicting apply must not change the stored revision")
	}
}

func TestConnectExpectedRevisionMismatch(t *testing.T) {
	h := newHarness(t)
	h.mustApply(tunConfig, 0)
	res := h.c.Connect(context.Background(), &agentapi.ConnectRequest{RequestId: rid(), ExpectedRevision: 9})
	if res.Ok || res.ErrorCode != agentapi.ErrorCode_ERROR_CODE_REVISION_CONFLICT {
		t.Fatalf("result = %+v", res)
	}
}

func TestApplyRejectsInvalidConfigurations(t *testing.T) {
	h := newHarness(t)

	res := h.apply(`{"outbounds":[{"type":"made-up","tag":"x"}]}`, 0)
	if res.Ok || res.ErrorCode != agentapi.ErrorCode_ERROR_CODE_POLICY_DENIED || len(res.Issues) == 0 {
		t.Fatalf("policy violation: %+v", res)
	}
	res = h.apply(`{"outbounds":[`, 0)
	if res.Ok || res.ErrorCode != agentapi.ErrorCode_ERROR_CODE_INVALID_CONFIG {
		t.Fatalf("broken JSON: %+v", res)
	}
	h.eng.FailCheck(errors.New("outbound[0]: missing server"))
	res = h.apply(mixedConfig, 0)
	if res.Ok || res.ErrorCode != agentapi.ErrorCode_ERROR_CODE_INVALID_CONFIG || !strings.Contains(res.Message, "core rejected") {
		t.Fatalf("core rejection: %+v", res)
	}
	if h.st.CurrentN() != 0 {
		t.Fatal("rejected configurations must not become revisions")
	}
}

func TestApplyRejectsMalformedResources(t *testing.T) {
	h := newHarness(t)
	res := h.c.ApplyConfig(context.Background(), &agentapi.ApplyConfigRequest{
		RequestId: rid(),
		Bundle: &agentapi.ConfigBundle{
			ConfigJson: []byte(mixedConfig),
			Resources:  []*agentapi.Resource{{Name: "../escape", Data: []byte("x")}},
		},
	})
	if res.Ok || res.ErrorCode != agentapi.ErrorCode_ERROR_CODE_INVALID_ARGUMENT {
		t.Fatalf("result = %+v", res)
	}
}

func TestResourcesAreStoredAndReferenced(t *testing.T) {
	h := newHarness(t)
	cfg := `{"inbounds":[{"type":"mixed","tag":"m","listen_port":7890}],"outbounds":[{"type":"direct","tag":"d"}],
	  "route":{"rule_set":[{"type":"local","tag":"cn","format":"binary","path":"res:cn.srs"}]}}`
	res := h.c.ApplyConfig(context.Background(), &agentapi.ApplyConfigRequest{
		RequestId: rid(),
		Bundle:    &agentapi.ConfigBundle{ConfigJson: []byte(cfg), Resources: []*agentapi.Resource{{Name: "cn.srs", Data: []byte("SRS")}}},
	})
	if !res.Ok {
		t.Fatalf("apply: %+v", res)
	}
	h.mustConnect()
	json := h.coreJSON()
	rev, _ := h.st.Current()
	if !strings.Contains(json, rev.ResourceDir()) || strings.Contains(json, "res:cn.srs") {
		t.Fatalf("the rule-set path must point into the revision's protected directory:\n%s", json)
	}
}

func TestHotApplyReplacesTheRunningConfiguration(t *testing.T) {
	h := newHarness(t)
	h.connected(tunConfig)
	res := h.mustApply(mixedConfig, 1)
	if res.Revision != 2 || !strings.Contains(res.Message, "revision 2 is running") {
		t.Fatalf("result = %+v", res)
	}
	st := h.c.Status()
	if st.ActiveRevision != 2 || st.Connection != agentapi.ConnectionState_CONNECTION_STATE_CONNECTED {
		t.Fatalf("status = %+v", st)
	}
	if n := len(h.eng.Started()); n != 2 {
		t.Fatalf("started %d times", n)
	}
	// The new revision has no TUN, so nothing of the old session may remain.
	if len(h.drv.Resolvers()) != 0 || len(h.drv.Interfaces()) != 0 || len(h.led.Entries()) != 0 {
		t.Fatalf("leftovers: %v %v %+v", h.drv.Resolvers(), h.drv.Interfaces(), h.led.Entries())
	}
	if good, _ := h.st.LastGood(); good.N != 2 {
		t.Fatalf("last good = %d", good.N)
	}
}

func TestHotApplyRollsBackToTheLastGoodRevision(t *testing.T) {
	h := newHarness(t)
	h.connected(tunConfig)
	h.eng.FailNextStart(errors.New("listen tcp 127.0.0.1:7890: bind: address already in use"))
	res := h.apply(mixedConfig, 1)
	if res.Ok || !res.RolledBack || res.ErrorCode != agentapi.ErrorCode_ERROR_CODE_CORE_START_FAILED {
		t.Fatalf("result = %+v", res)
	}
	if !strings.Contains(res.Message, "revision 1 is running again") {
		t.Fatalf("message = %q", res.Message)
	}
	st := h.c.Status()
	if st.Connection != agentapi.ConnectionState_CONNECTION_STATE_CONNECTED || st.ActiveRevision != 1 || st.StoredRevision != 1 {
		t.Fatalf("status = %+v", st)
	}
	if !strings.Contains(st.LastError, "revision 2 failed") {
		t.Fatalf("last error = %q", st.LastError)
	}
	h.requireOwns() // the old TUN session is back
	if rev, _ := h.st.Current(); rev.N != 1 {
		t.Fatalf("current revision = %d", rev.N)
	}
	// The next apply reuses revision number 2.
	if res := h.mustApply(mixedConfig, 1); res.Revision != 2 {
		t.Fatalf("next revision = %d", res.Revision)
	}
}

func TestHotApplyFailureWithoutAnyGoodRevisionLeavesItFailed(t *testing.T) {
	h := newHarness(t)
	h.mustApply(tunConfig, 0)
	h.mustConnect() // marks revision 1 good
	h.waitConn(agentapi.ConnectionState_CONNECTION_STATE_CONNECTED)
	h.eng.FailNextStart(errors.New("boom"))
	res := h.apply(mixedConfig, 1)
	if res.Ok || !res.RolledBack {
		t.Fatalf("result = %+v", res)
	}
}

func TestStoreOnlyApplyDoesNotTouchTheRunningCore(t *testing.T) {
	h := newHarness(t)
	h.connected(tunConfig)
	res := h.c.ApplyConfig(context.Background(), &agentapi.ApplyConfigRequest{
		RequestId: rid(), ExpectedRevision: 1, Mode: agentapi.ApplyMode_APPLY_MODE_STORE_ONLY,
		Bundle: &agentapi.ConfigBundle{ConfigJson: []byte(mixedConfig)},
	})
	if !res.Ok || res.Revision != 2 {
		t.Fatalf("result = %+v", res)
	}
	if st := h.c.Status(); st.ActiveRevision != 1 || st.StoredRevision != 2 {
		t.Fatalf("status = %+v", st)
	}
	if n := len(h.eng.Started()); n != 1 {
		t.Fatalf("started %d times", n)
	}
	// Connect then moves the running core to the stored revision.
	h.mustConnect()
	if st := h.c.Status(); st.ActiveRevision != 2 {
		t.Fatalf("active revision after connect = %d", st.ActiveRevision)
	}
}

func TestStartFailureCleansUpAndClearsIntent(t *testing.T) {
	h := newHarness(t)
	h.mustApply(tunConfig, 0)
	h.eng.FailNextStart(errors.New("tun: access denied"))
	res := h.connect()
	if res.Ok || res.ErrorCode != agentapi.ErrorCode_ERROR_CODE_CORE_START_FAILED {
		t.Fatalf("result = %+v", res)
	}
	st := h.c.Status()
	if st.Connection != agentapi.ConnectionState_CONNECTION_STATE_FAILED || st.IntentConnected ||
		!strings.Contains(st.LastError, "access denied") || st.LastErrorCode != agentapi.ErrorCode_ERROR_CODE_CORE_START_FAILED {
		t.Fatalf("status = %+v", st)
	}
	if h.intent.Load().Connected {
		t.Fatal("a failed first attempt must not persist the intent")
	}
	h.requireClean()
}

func TestActivateFailureStopsTheCoreAndReleasesEverything(t *testing.T) {
	h := newHarness(t)
	h.mustApply(tunConfig, 0)
	h.drv.FailActivate(errors.New("scutil: not permitted"))
	res := h.connect()
	if res.Ok || res.ErrorCode != agentapi.ErrorCode_ERROR_CODE_PLATFORM {
		t.Fatalf("result = %+v", res)
	}
	h.requireClean()
	h.drv.FailActivate(nil)
	h.mustConnect() // and a later attempt works
	h.requireOwns()
}

func TestRouteConflictRefusesUnlessConfirmed(t *testing.T) {
	h := newHarness(t)
	snap, _ := h.drv.Snapshot(context.Background())
	snap.Interfaces = append(snap.Interfaces, routecheck.Interface{Name: "utun9", Up: true, Kind: routecheck.KindTunnel})
	snap.Routes = append(snap.Routes, routecheck.Route{Dest: mustPrefix("0.0.0.0/1"), IfName: "utun9"})
	h.drv.SetSnapshot(snap)
	h.mustApply(tunConfig, 0)

	res := h.connect()
	if res.Ok || res.ErrorCode != agentapi.ErrorCode_ERROR_CODE_ROUTE_CONFLICT || res.Details["interfaces"] != "utun9" {
		t.Fatalf("result = %+v", res)
	}
	if len(h.eng.Started()) != 0 {
		t.Fatal("nothing may start while a conflict is unconfirmed")
	}
	if st := h.c.Status(); st.Connection != agentapi.ConnectionState_CONNECTION_STATE_FAILED {
		t.Fatalf("status = %+v", st)
	}

	res = h.c.Connect(context.Background(), &agentapi.ConnectRequest{RequestId: rid(), IgnoreRouteConflicts: true})
	if !res.Ok {
		t.Fatalf("confirmed connect: %+v", res)
	}
}

func TestNoFreeSubnetIsReported(t *testing.T) {
	h := newHarness(t)
	snap, _ := h.drv.Snapshot(context.Background())
	for i, p := range routecheck.DefaultCandidates() {
		snap.Routes = append(snap.Routes, routecheck.Route{Dest: p.V4.Masked(), IfName: "en0"})
		_ = i
	}
	h.drv.SetSnapshot(snap)
	h.mustApply(tunConfig, 0)
	res := h.connect()
	if res.Ok || res.ErrorCode != agentapi.ErrorCode_ERROR_CODE_ROUTE_CONFLICT || res.Details["kind"] != string(routecheck.ConflictNoSubnet) {
		t.Fatalf("result = %+v", res)
	}
}

func TestSubnetMovesAwayFromAnExistingNetwork(t *testing.T) {
	h := newHarness(t)
	snap, _ := h.drv.Snapshot(context.Background())
	snap.Routes = append(snap.Routes, routecheck.Route{Dest: mustPrefix("172.19.0.0/16"), IfName: "en0"})
	h.drv.SetSnapshot(snap)
	h.connected(tunConfig)
	if !strings.Contains(string(h.eng.Current().JSON), "172.29.0.1/30") {
		t.Fatalf("the second candidate subnet should be used:\n%s", h.eng.Current().JSON)
	}
}

func TestDisconnectInterruptsAConnectInProgress(t *testing.T) {
	h := newHarness(t)
	h.mustApply(tunConfig, 0)
	release := h.eng.BlockStart()
	defer release()

	var connectRes *agentapi.OperationResult
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); connectRes = h.connect() }()
	h.waitConn(agentapi.ConnectionState_CONNECTION_STATE_CONNECTING)

	start := time.Now()
	res := h.disconnect()
	wg.Wait()
	if time.Since(start) > 2*time.Second {
		t.Fatal("disconnect had to wait for the blocked start")
	}
	if !res.Ok {
		t.Fatalf("disconnect: %+v", res)
	}
	if connectRes.Ok || connectRes.ErrorCode != agentapi.ErrorCode_ERROR_CODE_CANCELLED {
		t.Fatalf("connect: %+v", connectRes)
	}
	h.requireClean()
	if st := h.c.Status(); st.Connection != agentapi.ConnectionState_CONNECTION_STATE_DISCONNECTED || st.IntentConnected {
		t.Fatalf("status = %+v", st)
	}
}

func TestOperationsRunOneAtATimeInOrder(t *testing.T) {
	h := newHarness(t)
	h.mustApply(tunConfig, 0)
	release := h.eng.BlockStart()

	var order []string
	var mu sync.Mutex
	note := func(s string) { mu.Lock(); order = append(order, s); mu.Unlock() }

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); h.connect(); note("connect") }()
	h.waitConn(agentapi.ConnectionState_CONNECTION_STATE_CONNECTING)

	// These queue behind the blocked connect.
	for _, name := range []string{"select", "mode"} {
		name := name
		wg.Add(1)
		go func() {
			defer wg.Done()
			if name == "select" {
				h.c.SelectOutbound(context.Background(), &agentapi.SelectOutboundRequest{RequestId: rid(), GroupTag: "proxy", OutboundTag: "node-b"})
			} else {
				h.c.SetMode(context.Background(), &agentapi.SetModeRequest{RequestId: rid(), Mode: "Global"})
			}
			note(name)
		}()
		time.Sleep(30 * time.Millisecond)
	}
	release()
	wg.Wait()
	if len(order) != 3 || order[0] != "connect" {
		t.Fatalf("order = %v (the connect must finish first)", order)
	}
}

func TestBusyWhenTheQueueIsFullAndNotCached(t *testing.T) {
	h := newHarness(t, func(o *hopts) { o.queue = 2 })
	h.mustApply(tunConfig, 0)
	release := h.eng.BlockStart()
	defer release()
	go h.connect()
	h.waitConn(agentapi.ConnectionState_CONNECTION_STATE_CONNECTING)

	var results []*agentapi.OperationResult
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res := h.c.CloseAllConnections(context.Background(), &agentapi.CloseAllConnectionsRequest{RequestId: rid()})
			mu.Lock()
			results = append(results, res)
			mu.Unlock()
		}()
	}
	// Give the queue time to fill; the surplus is answered BUSY right away.
	deadline := time.Now().Add(2 * time.Second)
	busy := 0
	for time.Now().Before(deadline) && busy < 3 {
		mu.Lock()
		busy = 0
		for _, r := range results {
			if r.ErrorCode == agentapi.ErrorCode_ERROR_CODE_BUSY {
				busy++
			}
		}
		mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	if busy < 3 {
		t.Fatalf("expected at least 3 BUSY answers, got %d", busy)
	}
	release()
	wg.Wait()
}

func TestBusyAnswersAreNotReplayed(t *testing.T) {
	c := newIdemCache()
	e, joined := c.begin("x", time.Now())
	if joined {
		t.Fatal("first use must not join")
	}
	c.abort("x", e, &agentapi.OperationResult{ErrorCode: agentapi.ErrorCode_ERROR_CODE_BUSY})
	if _, joined := c.begin("x", time.Now()); joined {
		t.Fatal("an aborted request id must be usable again")
	}
}

func TestCoreCrashTriggersReconnect(t *testing.T) {
	h := newHarness(t)
	h.connected(tunConfig)
	h.eng.Crash(errors.New("panic: boom"))
	h.waitStarts(2)
	st := h.waitConn(agentapi.ConnectionState_CONNECTION_STATE_CONNECTED)
	if st.ReconnectAttempts != 0 || st.ActiveRevision != 1 {
		t.Fatalf("status = %+v", st)
	}
	h.requireOwns()
}

func TestCoreCrashWithoutIntentJustCleansUp(t *testing.T) {
	h := newHarness(t)
	h.connected(tunConfig)
	// The user's intent was cleared behind the controller's back (e.g. a race with disconnect).
	h.c.update(func(s *state) { s.intentConnected = false })
	h.eng.Crash(errors.New("boom"))
	h.waitConn(agentapi.ConnectionState_CONNECTION_STATE_DISCONNECTED)
	h.requireClean()
}

func TestReconnectBacksOffAndGivesUpThenRetriesOnNetworkChange(t *testing.T) {
	h := newHarness(t, func(o *hopts) { o.maxRetry = 2 })
	h.connected(tunConfig)
	h.eng.FailStartWhen(func(engine.Prepared) error { return errors.New("no route to host") })
	h.eng.Crash(errors.New("conn reset"))

	st := h.waitConn(agentapi.ConnectionState_CONNECTION_STATE_FAILED)
	if !st.IntentConnected || !strings.Contains(st.LastError, "no route") {
		t.Fatalf("status = %+v", st)
	}
	attempts := len(h.eng.Calls())
	settle()
	if len(h.eng.Calls()) != attempts {
		t.Fatal("after giving up nothing may retry by itself")
	}
	h.requireClean()

	// The network changes and the problem is gone: the connection comes back.
	h.eng.FailStartWhen(nil)
	h.drv.SetFingerprint("en1|10.0.0.1")
	h.drv.Emit(platform.NetworkChanged, "wifi")
	h.waitConn(agentapi.ConnectionState_CONNECTION_STATE_CONNECTED)
	h.requireOwns()
}

func TestNetworkChangeReconnectsOnceForABurst(t *testing.T) {
	h := newHarness(t)
	h.connected(tunConfig)
	h.drv.SetFingerprint("en1|10.0.0.1")
	for i := 0; i < 8; i++ {
		h.drv.Emit(platform.NetworkChanged, "burst")
	}
	h.waitStarts(2)
	h.waitConn(agentapi.ConnectionState_CONNECTION_STATE_CONNECTED)
	settle()
	if n := len(h.eng.Started()); n != 2 {
		t.Fatalf("a burst of events must cause one reconnect, core started %d times", n)
	}
}

func TestUnchangedUplinkDoesNotReconnect(t *testing.T) {
	h := newHarness(t)
	h.connected(tunConfig)
	h.drv.Emit(platform.NetworkChanged, "noise")
	settle()
	if n := len(h.eng.Started()); n != 1 {
		t.Fatalf("started %d times", n)
	}
}

func TestWakeAlwaysReconnects(t *testing.T) {
	h := newHarness(t)
	h.connected(tunConfig)
	h.drv.Emit(platform.Woke, "")
	h.waitStarts(2)
	h.waitConn(agentapi.ConnectionState_CONNECTION_STATE_CONNECTED)
}

func TestNetworkEventsAreIgnoredWhenTheSettingIsOff(t *testing.T) {
	h := newHarness(t, func(o *hopts) { o.settings = func(s *store.Settings) { s.ReconnectOnNetworkChange = false } })
	h.connected(tunConfig)
	h.drv.SetFingerprint("en1|10.0.0.1")
	h.drv.Emit(platform.NetworkChanged, "")
	h.drv.Emit(platform.Woke, "")
	settle()
	if n := len(h.eng.Started()); n != 1 {
		t.Fatalf("started %d times", n)
	}
}

func TestOfflineWaitsForTheNetworkToReturn(t *testing.T) {
	h := newHarness(t)
	h.connected(tunConfig)
	h.drv.SetOnline(false)
	h.drv.SetFingerprint("")
	h.drv.Emit(platform.NetworkChanged, "link down")
	h.wait("offline", func(s *agentapi.Status) bool { return s.Network == agentapi.NetworkState_NETWORK_STATE_OFFLINE })
	settle()
	if n := len(h.eng.Started()); n != 1 {
		t.Fatalf("no reconnect while offline, started %d times", n)
	}
	h.drv.SetOnline(true)
	h.drv.SetFingerprint("en0|192.168.7.1")
	h.drv.Emit(platform.NetworkChanged, "link up")
	h.waitStarts(2)
	h.wait("online", func(s *agentapi.Status) bool {
		return s.Network == agentapi.NetworkState_NETWORK_STATE_ONLINE && s.Connection == agentapi.ConnectionState_CONNECTION_STATE_CONNECTED
	})
}

func TestDisconnectedAgentIgnoresNetworkEvents(t *testing.T) {
	h := newHarness(t)
	h.mustApply(tunConfig, 0)
	h.drv.SetFingerprint("other")
	h.drv.Emit(platform.NetworkChanged, "")
	h.drv.Emit(platform.Woke, "")
	settle()
	if len(h.eng.Started()) != 0 {
		t.Fatal("a disconnected agent must stay disconnected")
	}
}

func TestLoginAutoConnectsWhenEnabled(t *testing.T) {
	h := newHarness(t, func(o *hopts) { o.settings = func(s *store.Settings) { s.AutoConnect = true } })
	h.mustApply(tunConfig, 0)
	h.drv.Emit(platform.UserLoggedIn, "")
	h.waitConn(agentapi.ConnectionState_CONNECTION_STATE_CONNECTED)
}

func TestLoginDoesNothingByDefault(t *testing.T) {
	h := newHarness(t)
	h.mustApply(tunConfig, 0)
	h.drv.Emit(platform.UserLoggedIn, "")
	settle()
	if len(h.eng.Started()) != 0 {
		t.Fatal("auto connect is off by default")
	}
}

func TestLogoutDisconnects(t *testing.T) {
	h := newHarness(t)
	h.connected(tunConfig)
	h.drv.Emit(platform.UserLoggedOut, "")
	h.waitConn(agentapi.ConnectionState_CONNECTION_STATE_DISCONNECTED)
	h.requireClean()
	if h.c.Status().IntentConnected {
		t.Fatal("a logout ends the intent too")
	}
}

func TestSandboxRunsTunConfigurationsAsALoopbackProxy(t *testing.T) {
	h := newHarness(t, withSandbox)
	h.connected(tunConfig)
	st := h.c.Status()
	if st.SandboxPort == 0 || st.TunInterface != "" {
		t.Fatalf("status = %+v", st)
	}
	got := h.coreJSON()
	if h.eng.Current().HasTun || !strings.Contains(got, `"type":"mixed"`) || !strings.Contains(got, `"listen":"127.0.0.1"`) {
		t.Fatalf("sandbox config:\n%s", got)
	}
	if len(h.drv.Resolvers()) != 0 || len(h.drv.Interfaces()) != 0 || len(h.led.Entries()) != 0 {
		t.Fatal("sandbox mode must not touch the simulated OS")
	}
	if len(st.Warnings) == 0 || !strings.HasPrefix(st.Warnings[0], "sandbox_tun_replaced") {
		t.Fatalf("warnings = %v", st.Warnings)
	}
	h.disconnect()
	if st := h.c.Status(); st.SandboxPort != 0 {
		t.Fatalf("sandbox port must be cleared, got %d", st.SandboxPort)
	}
}

func TestWatchStatusDeliversChangesAndLatestToSlowConsumers(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := h.c.WatchStatus(ctx)
	first := <-ch
	if first.Connection != agentapi.ConnectionState_CONNECTION_STATE_DISCONNECTED {
		t.Fatalf("first = %+v", first)
	}
	// Do not read while many changes happen.
	h.connected(tunConfig)
	h.disconnect()
	time.Sleep(50 * time.Millisecond)
	last := <-ch
	// Drain whatever is pending; the final value must reflect the end state.
	for {
		select {
		case s := <-ch:
			last = s
			continue
		case <-time.After(100 * time.Millisecond):
		}
		break
	}
	if last.Connection != agentapi.ConnectionState_CONNECTION_STATE_DISCONNECTED || last.Seq <= first.Seq {
		t.Fatalf("last = %+v", last)
	}
}

func TestSettingsAreValidatedAndPersisted(t *testing.T) {
	h := newHarness(t)
	bad := "shout"
	if _, err := h.c.UpdateSettings(&agentapi.UpdateSettingsRequest{LogLevel: &bad}); err == nil {
		t.Fatal("an unknown log level must be rejected")
	}
	on := true
	level := "debug"
	got, err := h.c.UpdateSettings(&agentapi.UpdateSettingsRequest{AutoConnect: &on, LogLevel: &level})
	if err != nil || !got.AutoConnect || got.LogLevel != "debug" {
		t.Fatalf("settings = %+v, %v", got, err)
	}
	h.crash()
	h.restart()
	if s := h.c.GetSettings(); !s.AutoConnect || s.LogLevel != "debug" {
		t.Fatalf("settings after restart = %+v", s)
	}
}

func TestLogLevelSettingReachesTheCore(t *testing.T) {
	h := newHarness(t, func(o *hopts) { o.settings = func(s *store.Settings) { s.LogLevel = "debug" } })
	h.connected(tunConfig)
	if !strings.Contains(h.coreJSON(), `"level":"debug"`) {
		t.Fatalf("config:\n%s", h.coreJSON())
	}
}

func TestRuntimeOperationsMapErrors(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	sel := func(group, out string) *agentapi.OperationResult {
		return h.c.SelectOutbound(ctx, &agentapi.SelectOutboundRequest{RequestId: rid(), GroupTag: group, OutboundTag: out})
	}
	if res := sel("proxy", "node-b"); res.ErrorCode != agentapi.ErrorCode_ERROR_CODE_NOT_CONNECTED {
		t.Fatalf("while disconnected: %+v", res)
	}
	h.connected(tunConfig)
	if res := sel("proxy", "node-b"); !res.Ok {
		t.Fatalf("select: %+v", res)
	}
	if res := sel("proxy", "ghost"); res.ErrorCode != agentapi.ErrorCode_ERROR_CODE_NOT_FOUND {
		t.Fatalf("unknown outbound: %+v", res)
	}
	if res := h.c.SetMode(ctx, &agentapi.SetModeRequest{RequestId: rid(), Mode: "Nonsense"}); res.ErrorCode != agentapi.ErrorCode_ERROR_CODE_INVALID_ARGUMENT {
		t.Fatalf("bad mode: %+v", res)
	}
	if res := h.c.SetMode(ctx, &agentapi.SetModeRequest{RequestId: rid(), Mode: "Global"}); !res.Ok || h.c.Status().Mode != "Global" {
		t.Fatalf("mode: %+v / %q", res, h.c.Status().Mode)
	}
	if res := h.c.CloseConnection(ctx, &agentapi.CloseConnectionRequest{RequestId: rid(), Id: "nope"}); res.ErrorCode != agentapi.ErrorCode_ERROR_CODE_NOT_FOUND {
		t.Fatalf("close unknown: %+v", res)
	}
	if res := h.c.URLTest(ctx, &agentapi.URLTestRequest{RequestId: rid(), OutboundTag: "proxy"}); !res.Ok {
		t.Fatalf("urltest: %+v", res)
	}
	h.disconnect()
	if res := h.c.URLTest(ctx, &agentapi.URLTestRequest{RequestId: rid(), OutboundTag: "proxy"}); res.ErrorCode != agentapi.ErrorCode_ERROR_CODE_NOT_CONNECTED {
		t.Fatalf("urltest while disconnected: %+v", res)
	}
}

func TestValidateConfigStoresNothing(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	ok := h.c.ValidateConfig(ctx, &agentapi.ConfigBundle{ConfigJson: []byte(tunConfig)})
	if !ok.Valid {
		t.Fatalf("valid config reported invalid: %+v", ok)
	}
	bad := h.c.ValidateConfig(ctx, &agentapi.ConfigBundle{ConfigJson: []byte(`{"outbounds":[{"type":"made-up","tag":"x"}]}`)})
	if bad.Valid || len(bad.Issues) == 0 || bad.Issues[0].Code != "type_not_allowed" {
		t.Fatalf("invalid config: %+v", bad)
	}
	h.eng.FailCheck(errors.New("nope"))
	rejected := h.c.ValidateConfig(ctx, &agentapi.ConfigBundle{ConfigJson: []byte(tunConfig)})
	if rejected.Valid {
		t.Fatalf("core rejection must invalidate: %+v", rejected)
	}
	if h.st.CurrentN() != 0 {
		t.Fatal("validation must not create revisions")
	}
	badRes := h.c.ValidateConfig(ctx, &agentapi.ConfigBundle{ConfigJson: []byte(tunConfig), Resources: []*agentapi.Resource{{Name: "../x", Data: []byte("1")}}})
	if badRes.Valid || badRes.Issues[0].Code != "resource_invalid" {
		t.Fatalf("bad resource: %+v", badRes)
	}
}

func TestShutdownLeavesAMachineAndLedgerClean(t *testing.T) {
	h := newHarness(t)
	h.connected(tunConfig)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.c.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	h.abandoned = true
	h.requireClean()
	h.restart()
	if st := h.c.Status(); st.Cleanup != agentapi.CleanupState_CLEANUP_STATE_IDLE {
		t.Fatalf("a clean shutdown must not look like a crash: %+v", st)
	}
}

func TestRequestsAfterShutdownAreRefused(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.c.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	h.abandoned = true
	if res := h.connect(); res.Ok || res.ErrorCode != agentapi.ErrorCode_ERROR_CODE_UNAVAILABLE {
		t.Fatalf("result = %+v", res)
	}
}

func TestObservationFollowsTheCoreAcrossRestarts(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	traffic := h.c.WatchTraffic(ctx, 250*time.Millisecond)

	recv := func(want func(*agentapi.Traffic) bool, what string) {
		t.Helper()
		deadline := time.After(5 * time.Second)
		for {
			select {
			case tr := <-traffic:
				if want(tr) {
					return
				}
			case <-deadline:
				t.Fatalf("no traffic sample: %s", what)
			}
		}
	}
	recv(func(tr *agentapi.Traffic) bool { return !tr.Available }, "unavailable sample while disconnected")
	h.connected(tunConfig)
	recv(func(tr *agentapi.Traffic) bool { return tr.Available && tr.UplinkTotal > 0 }, "samples while connected")
	h.disconnect()
	recv(func(tr *agentapi.Traffic) bool { return !tr.Available }, "unavailable sample after disconnect")
	h.mustConnect()
	recv(func(tr *agentapi.Traffic) bool { return tr.Available }, "samples after reconnect")
}

func TestConnectionEventsStartWithAResetAfterEveryRestart(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := h.c.WatchConnections(ctx, 250*time.Millisecond)
	first := <-events
	if !first.Reset_ {
		t.Fatal("the first message while disconnected must reset the client's table")
	}
	h.connected(tunConfig)
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Reset_ {
				return
			}
		case <-deadline:
			t.Fatal("no reset after connecting")
		}
	}
}

func TestGroupsAreStreamed(t *testing.T) {
	h := newHarness(t)
	h.connected(tunConfig)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	groups := h.c.WatchGroups(ctx)
	select {
	case g := <-groups:
		if len(g.Groups) != 1 || g.Groups[0].Tag != "proxy" {
			t.Fatalf("groups = %+v", g)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no groups")
	}
}

func TestMergeConnectionEvents(t *testing.T) {
	a := &agentapi.ConnectionEvents{Events: []*agentapi.ConnectionEvent{{Id: "1"}}}
	b := &agentapi.ConnectionEvents{Events: []*agentapi.ConnectionEvent{{Id: "2"}}}
	m := mergeConnectionEvents(a, b)
	if len(m.Events) != 2 || m.Reset_ {
		t.Fatalf("merged = %+v", m)
	}
	r := &agentapi.ConnectionEvents{Reset_: true, Events: []*agentapi.ConnectionEvent{{Id: "9"}}}
	if m := mergeConnectionEvents(a, r); len(m.Events) != 1 || !m.Reset_ {
		t.Fatalf("a reset replaces what is pending: %+v", m)
	}
	// A pending reset keeps its flag when more events follow.
	if m := mergeConnectionEvents(r, b); !m.Reset_ || len(m.Events) != 2 {
		t.Fatalf("merged = %+v", m)
	}
}

func mustPrefix(s string) netip.Prefix { return netip.MustParsePrefix(s) }
