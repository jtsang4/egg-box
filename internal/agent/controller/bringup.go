package controller

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jtsang4/sing-box-ui/internal/agent/engine"
	"github.com/jtsang4/sing-box-ui/internal/agent/ledger"
	"github.com/jtsang4/sing-box-ui/internal/agent/platform"
	"github.com/jtsang4/sing-box-ui/internal/agent/policy"
	"github.com/jtsang4/sing-box-ui/internal/agent/routecheck"
	"github.com/jtsang4/sing-box-ui/internal/agent/store"
	"github.com/jtsang4/sing-box-ui/internal/agentapi"
)

type upOptions struct {
	// ignoreConflicts proceeds although another tunnel owns default routes.
	ignoreConflicts bool
}

// recorder adapts the ledger to platform.Recorder.
type recorder struct{ l *ledger.Ledger }

func (r recorder) Add(e ledger.Entry) error { return r.l.Add(e) }
func (r recorder) Remove(id string) error   { return r.l.Remove(id) }

func (c *Controller) recorder() platform.Recorder { return recorder{c.cfg.Ledger} }

// bringUp starts the core for rev and applies the OS-level pieces. It either
// leaves a fully running connection behind or tears down everything it
// created and returns why.
func (c *Controller) bringUp(ctx context.Context, rev store.Revision, opts upOptions) (oerr *opError) {
	defer func() {
		if oerr != nil {
			c.teardown("bring-up failed")
		}
	}()

	// Never stack a new session on top of resources we failed to release.
	if len(c.cfg.Ledger.Entries()) > 0 {
		if rep := c.runCleanup(); !rep.OK() {
			return newOpError(agentapi.ErrorCode_ERROR_CODE_CLEANUP_FAILED, nil,
				"resources of an earlier session could not be removed (%s)", rep.Summary())
		}
	}

	raw, err := os.ReadFile(rev.ConfigPath())
	if err != nil {
		return newOpError(agentapi.ErrorCode_ERROR_CODE_INTERNAL, err, "the stored configuration cannot be read")
	}

	settings := c.cfg.Settings.Load()
	defaults := c.cfg.Driver.TunDefaults()
	sandbox := c.cfg.Driver.Sandbox()

	env := policy.Env{
		ResourceDir: rev.ResourceDir(),
		Resources:   resourceNames(rev.ResourceDir()),
		CachePath:   c.cfg.CachePath,
		LogLevel:    settings.LogLevel,
		Tun: policy.TunSettings{
			Addresses:     prefixStrings(c.cfg.Candidates[0].Addresses()),
			StrictRoute:   defaults.StrictRoute,
			DNSMode:       defaults.DNSMode,
			InterfaceName: defaults.InterfaceName,
		},
	}
	var sandboxPort uint16
	if sandbox {
		sandboxPort, err = c.pickSandboxPort()
		if err != nil {
			return newOpError(agentapi.ErrorCode_ERROR_CODE_PLATFORM, err, "no loopback port is available for the sandbox proxy")
		}
		env.SandboxPort = int(sandboxPort)
	}

	res := c.cfg.Policy.Normalize(raw, env)
	if res.HasErrors() {
		return policyError(res)
	}

	var active platform.ActiveTun
	if res.HasTun {
		// A real TUN: judge the machine's routes first, then fix the plan.
		plan, perr := c.planTun(ctx, opts)
		if perr != nil {
			return perr
		}
		env.Tun.Addresses = prefixStrings(plan.Addresses())
		res = c.cfg.Policy.Normalize(raw, env)
		if res.HasErrors() {
			return policyError(res)
		}
		active = platform.ActiveTun{Addresses: plan.Addresses(), DNS: plan.DNS()}

		// From here on the machine may change: record intent before acting.
		if err := c.cfg.Ledger.BeginSession(ledger.Session{ID: c.cfg.InstanceID, PID: os.Getpid(), Started: time.Now().UTC()}); err != nil {
			return newOpError(agentapi.ErrorCode_ERROR_CODE_INTERNAL, err, "cannot write the resource ledger")
		}
		if name := defaults.InterfaceName; name != "" {
			if err := c.cfg.Ledger.Add(ledger.Entry{ID: platform.EntryTunInterface, Kind: ledger.KindTunInterface, Key: name}); err != nil {
				return newOpError(agentapi.ErrorCode_ERROR_CODE_INTERNAL, err, "cannot write the resource ledger")
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return newOpError(agentapi.ErrorCode_ERROR_CODE_CANCELLED, err, "interrupted")
	}

	prepared := engine.Prepared{Revision: rev.N, JSON: res.JSON, HasTun: res.HasTun, TunTag: res.TunTag, LogLevel: settings.LogLevel}
	sctx, cancel := context.WithTimeout(ctx, c.cfg.StartTimeout)
	err = c.cfg.Engine.Start(sctx, prepared)
	cancel()
	if err != nil {
		if ctx.Err() != nil {
			return newOpError(agentapi.ErrorCode_ERROR_CODE_CANCELLED, ctx.Err(), "interrupted while starting the core")
		}
		return newOpError(agentapi.ErrorCode_ERROR_CODE_CORE_START_FAILED, err, "the core did not start")
	}

	if res.HasTun {
		active.Interface = c.cfg.Engine.TunInterface()
		if active.Interface == "" {
			active.Interface = defaults.InterfaceName
		}
		if active.Interface == "" {
			return newOpError(agentapi.ErrorCode_ERROR_CODE_PLATFORM, nil, "the core did not report its TUN interface")
		}
		actx, acancel := context.WithTimeout(ctx, c.cfg.StartTimeout)
		err := c.cfg.Driver.Activate(actx, active, c.recorder())
		acancel()
		if err != nil {
			if ctx.Err() != nil {
				return newOpError(agentapi.ErrorCode_ERROR_CODE_CANCELLED, ctx.Err(), "interrupted while configuring the system")
			}
			return newOpError(agentapi.ErrorCode_ERROR_CODE_PLATFORM, err, "the system could not be pointed at the tunnel")
		}
	}

	fp, _ := c.cfg.Driver.Fingerprint(ctx)
	mode, _, _ := c.cfg.Engine.Mode()
	if err := c.cfg.Store.MarkGood(rev.N); err != nil {
		c.log.Warn("could not mark revision as good", "revision", rev.N, "err", err)
	}
	c.update(func(s *state) {
		s.conn = agentapi.ConnectionState_CONNECTION_STATE_CONNECTED
		s.activeRev = rev.N
		s.connectedSince = time.Now()
		s.tunIface = active.Interface
		s.mode = mode
		s.warnings = warningStrings(res.Issues)
		s.lastErrCode = agentapi.ErrorCode_ERROR_CODE_UNSPECIFIED
		s.lastErr = ""
		s.reconnectAttempts = 0
		s.sandboxPort = uint32(sandboxPort)
		s.fingerprint = fp
		s.network = agentapi.NetworkState_NETWORK_STATE_ONLINE
		if s.cleanup == agentapi.CleanupState_CLEANUP_STATE_RECOVERED {
			s.cleanup = agentapi.CleanupState_CLEANUP_STATE_IDLE
			s.cleanupDetail = ""
		}
	})
	c.bumpGeneration()
	c.logAgent("info", "connected with revision %d%s", rev.N, map[bool]string{true: " (sandbox)", false: ""}[sandbox])
	return nil
}

// planTun inspects the machine and picks a conflict-free TUN subnet.
func (c *Controller) planTun(ctx context.Context, opts upOptions) (routecheck.Plan, *opError) {
	snap, err := c.cfg.Driver.Snapshot(ctx)
	if err != nil {
		return routecheck.Plan{}, newOpError(agentapi.ErrorCode_ERROR_CODE_PLATFORM, err, "the network configuration cannot be read")
	}
	owned := map[string]bool{}
	for _, e := range c.cfg.Ledger.Entries() {
		if e.Kind == ledger.KindTunInterface {
			owned[e.Key] = true
		}
	}
	for i := range snap.Interfaces {
		if owned[snap.Interfaces[i].Name] {
			snap.Interfaces[i].Ours = true
		}
	}

	if conflicts := routecheck.Analyze(snap); len(conflicts) > 0 && !opts.ignoreConflicts {
		names := make([]string, 0, len(conflicts))
		lines := make([]string, 0, len(conflicts))
		for _, cf := range conflicts {
			names = append(names, cf.Interface)
			lines = append(lines, cf.String())
		}
		return routecheck.Plan{}, &opError{
			code: agentapi.ErrorCode_ERROR_CODE_ROUTE_CONFLICT,
			msg:  "another VPN or tunnel already carries the default route: " + strings.Join(names, ", "),
			details: map[string]string{
				"kind":       string(routecheck.ConflictForeignDefaultRoute),
				"interfaces": strings.Join(names, ","),
				"conflicts":  strings.Join(lines, "; "),
			},
		}
	}
	plan, ok := routecheck.ChoosePlan(snap, c.cfg.Candidates)
	if !ok {
		return routecheck.Plan{}, &opError{
			code:    agentapi.ErrorCode_ERROR_CODE_ROUTE_CONFLICT,
			msg:     "every private subnet the tunnel could use is already taken on this machine",
			details: map[string]string{"kind": string(routecheck.ConflictNoSubnet)},
		}
	}
	return plan, nil
}

// teardown stops the core and releases everything the agent created. It is
// safe in any state and reports whether the machine is clean afterwards.
func (c *Controller) teardown(reason string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), c.cfg.StopTimeout)
	defer cancel()

	owns := c.cfg.Engine.Running() || len(c.cfg.Ledger.Entries()) > 0
	if !owns {
		c.update(func(s *state) { s.tunIface = ""; s.sandboxPort = 0 })
		c.bumpGeneration()
		return true
	}
	c.update(func(s *state) { s.cleanup = agentapi.CleanupState_CLEANUP_STATE_RUNNING })
	c.log.Info("tearing down", "reason", reason)

	var problems []string
	// The resolver goes back first so DNS never points at a tunnel that is about to disappear.
	if err := c.cfg.Driver.Deactivate(ctx, c.recorder()); err != nil {
		c.log.Warn("deactivate failed", "err", err)
		problems = append(problems, "restore system settings: "+err.Error())
	}
	if err := c.cfg.Engine.Stop(ctx); err != nil {
		c.log.Warn("core stop failed", "err", err)
		problems = append(problems, "stop core: "+err.Error())
	}
	if c.cfg.Engine.Running() {
		problems = append(problems, "the core is still running")
	}
	rep := c.cfg.Ledger.Cleanup(ctx, c.cfg.Driver)
	if !rep.OK() {
		problems = append(problems, rep.Summary())
	}
	ok := rep.OK() && !c.cfg.Engine.Running()
	if ok {
		// Individual problems were resolved by the ledger pass; do not alarm.
		problems = nil
	}
	c.applyCleanupResult(ok, strings.Join(problems, "; "))
	c.update(func(s *state) { s.tunIface = ""; s.sandboxPort = 0 })
	c.bumpGeneration()
	return ok
}

// runCleanup re-runs the ledger pass (leftovers from a failed teardown).
func (c *Controller) runCleanup() ledger.Report {
	ctx, cancel := context.WithTimeout(context.Background(), c.cfg.StopTimeout)
	defer cancel()
	c.update(func(s *state) { s.cleanup = agentapi.CleanupState_CLEANUP_STATE_RUNNING })
	rep := c.cfg.Ledger.Cleanup(ctx, c.cfg.Driver)
	c.applyCleanupResult(rep.OK(), rep.Summary())
	return rep
}

func (c *Controller) applyCleanupResult(ok bool, detail string) {
	c.update(func(s *state) {
		if ok {
			s.cleanup = agentapi.CleanupState_CLEANUP_STATE_IDLE
			s.cleanupDetail = ""
			if s.service == agentapi.ServiceState_SERVICE_STATE_DEGRADED {
				s.service = agentapi.ServiceState_SERVICE_STATE_READY
			}
			return
		}
		s.cleanup = agentapi.CleanupState_CLEANUP_STATE_FAILED
		s.cleanupDetail = detail
		if s.service != agentapi.ServiceState_SERVICE_STATE_STOPPING {
			s.service = agentapi.ServiceState_SERVICE_STATE_DEGRADED
		}
	})
	if !ok {
		c.logAgent("error", "cleanup incomplete: %s", detail)
	}
}

// ------------------------------------------------------------------- helpers

func resourceNames(dir string) map[string]bool {
	out := map[string]bool{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() {
			out[e.Name()] = true
		}
	}
	return out
}

func prefixStrings(ps []netip.Prefix) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.String()
	}
	return out
}

func (c *Controller) pickSandboxPort() (uint16, error) {
	if c.cfg.SandboxPort != 0 {
		return c.cfg.SandboxPort, nil
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return uint16(l.Addr().(*net.TCPAddr).Port), nil
}

// structural problems the user can fix by editing the configuration; any
// other policy finding means the agent refuses the content on purpose.
var invalidConfigCodes = map[string]bool{
	"config_empty": true, "config_too_large": true, "json_invalid": true, "json_encode_failed": true,
	"type_mismatch": true, "value_invalid": true, "resource_missing": true, "tun_multiple": true,
}

func policyError(res *policy.Result) *opError {
	code := agentapi.ErrorCode_ERROR_CODE_INVALID_CONFIG
	issues := toProtoIssues(res.Issues)
	for _, i := range res.Errors() {
		if !invalidConfigCodes[i.Code] {
			code = agentapi.ErrorCode_ERROR_CODE_POLICY_DENIED
			break
		}
	}
	first := res.Errors()[0]
	return &opError{code: code, msg: fmt.Sprintf("the configuration was rejected: %s (%s)", first.Message, first.Path), issues: issues}
}

func toProtoIssues(in []policy.Issue) []*agentapi.Issue {
	out := make([]*agentapi.Issue, 0, len(in))
	for _, i := range in {
		sev := agentapi.Issue_SEVERITY_ERROR
		if i.Severity == policy.SeverityWarning {
			sev = agentapi.Issue_SEVERITY_WARNING
		}
		out = append(out, &agentapi.Issue{Severity: sev, Path: i.Path, Code: i.Code, Message: i.Message})
	}
	return out
}

func warningStrings(in []policy.Issue) []string {
	var out []string
	for _, i := range in {
		if i.Severity == policy.SeverityWarning {
			out = append(out, i.Code+": "+i.Message)
		}
	}
	sort.Strings(out)
	return out
}

// engineError maps core errors to API error codes.
func engineError(err error) *agentapi.OperationResult {
	switch {
	case errors.Is(err, engine.ErrNotRunning):
		return failure(agentapi.ErrorCode_ERROR_CODE_NOT_CONNECTED, "the proxy is not connected")
	case errors.Is(err, engine.ErrNotFound):
		return failure(agentapi.ErrorCode_ERROR_CODE_NOT_FOUND, "%v", err)
	case errors.Is(err, engine.ErrInvalid):
		return failure(agentapi.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "%v", err)
	default:
		return failure(agentapi.ErrorCode_ERROR_CODE_INTERNAL, "%v", err)
	}
}
