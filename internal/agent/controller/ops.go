package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	"eggbox/internal/agent/store"
	"eggbox/internal/agentapi"
)

var (
	codeCancelled = agentapi.ErrorCode_ERROR_CODE_CANCELLED
)

func (c *Controller) state() state {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.st
}

func isUp(s agentapi.ConnectionState) bool {
	return s == agentapi.ConnectionState_CONNECTION_STATE_CONNECTED ||
		s == agentapi.ConnectionState_CONNECTION_STATE_CONNECTING ||
		s == agentapi.ConnectionState_CONNECTION_STATE_RECONNECTING
}

// ----------------------------------------------------------------- connect

func (c *Controller) connectJob(req *agentapi.ConnectRequest, reason string) func(context.Context) *agentapi.OperationResult {
	return func(ctx context.Context) *agentapi.OperationResult {
		rev, ok := c.cfg.Store.Current()
		if !ok {
			return failure(agentapi.ErrorCode_ERROR_CODE_NO_CONFIG, "no configuration has been applied yet")
		}
		if want := req.GetExpectedRevision(); want != 0 && want != rev.N {
			res := failure(agentapi.ErrorCode_ERROR_CODE_REVISION_CONFLICT, "expected revision %d but %d is stored", want, rev.N)
			res.Details = map[string]string{"stored_revision": fmt.Sprint(rev.N)}
			return res
		}
		c.cancelRetry()

		st := c.state()
		if st.conn == agentapi.ConnectionState_CONNECTION_STATE_CONNECTED && st.activeRev == rev.N && c.cfg.Engine.Running() {
			c.setIntent(true, rev.N)
			return success("already connected")
		}

		c.setIntent(true, rev.N)
		c.update(func(s *state) {
			s.conn = agentapi.ConnectionState_CONNECTION_STATE_CONNECTING
			s.lastErr, s.lastErrCode = "", agentapi.ErrorCode_ERROR_CODE_UNSPECIFIED
			s.reconnectAttempts = 0
		})
		c.logAgent("info", "connecting (%s) with revision %d", reason, rev.N)
		c.teardown("connect")

		if oerr := c.bringUp(ctx, rev, upOptions{ignoreConflicts: req.GetIgnoreRouteConflicts()}); oerr != nil {
			return c.connectFailed(ctx, oerr)
		}
		return success("connected")
	}
}

// connectFailed records a failed first attempt. The user asked for a connection
// and did not get one, so the intent is cleared: nothing retries by itself.
func (c *Controller) connectFailed(ctx context.Context, oerr *opError) *agentapi.OperationResult {
	if ctx.Err() != nil || oerr.code == codeCancelled {
		c.update(func(s *state) {
			s.conn = agentapi.ConnectionState_CONNECTION_STATE_DISCONNECTED
			s.activeRev = 0
		})
		return failure(codeCancelled, "the connection attempt was interrupted")
	}
	c.setIntent(false, 0)
	c.update(func(s *state) {
		s.conn = agentapi.ConnectionState_CONNECTION_STATE_FAILED
		s.activeRev = 0
		s.lastErrCode, s.lastErr = oerr.code, oerr.Error()
	})
	c.logAgent("error", "connect failed: %s", oerr.Error())
	return oerr.result()
}

// -------------------------------------------------------------- disconnect

func (c *Controller) disconnectJob(reason string) func(context.Context) *agentapi.OperationResult {
	return func(context.Context) *agentapi.OperationResult {
		c.cancelRetry()
		c.setIntent(false, 0)
		c.update(func(s *state) {
			if isUp(s.conn) || s.conn == agentapi.ConnectionState_CONNECTION_STATE_FAILED {
				s.conn = agentapi.ConnectionState_CONNECTION_STATE_DISCONNECTING
			}
		})
		ok := c.teardown("disconnect: " + reason)
		c.update(func(s *state) {
			s.conn = agentapi.ConnectionState_CONNECTION_STATE_DISCONNECTED
			s.activeRev = 0
			s.connectedSince = time.Time{}
			s.lastErr, s.lastErrCode = "", agentapi.ErrorCode_ERROR_CODE_UNSPECIFIED
			s.reconnectAttempts = 0
			s.warnings = nil
			s.fingerprint = ""
			s.mode = ""
			s.routeTakeover = false
		})
		c.logAgent("info", "disconnected (%s)", reason)
		if !ok {
			res := failure(agentapi.ErrorCode_ERROR_CODE_CLEANUP_FAILED, "disconnected, but some system changes could not be undone: %s", c.state().cleanupDetail)
			return res
		}
		return success("disconnected")
	}
}

// ------------------------------------------------------------------- apply

func (c *Controller) applyJob(req *agentapi.ApplyConfigRequest) func(context.Context) *agentapi.OperationResult {
	return func(ctx context.Context) *agentapi.OperationResult {
		bundle := req.GetBundle()
		if bundle == nil || len(bundle.GetConfigJson()) == 0 {
			return failure(agentapi.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "the request carries no configuration")
		}
		resources := toStoreResources(bundle.GetResources())

		staged, err := c.cfg.Store.Stage(req.GetExpectedRevision(), bundle.GetProfileId(), bundle.GetProfileName(), resources)
		switch {
		case errors.Is(err, store.ErrRevisionConflict):
			res := failure(agentapi.ErrorCode_ERROR_CODE_REVISION_CONFLICT, "%v", err)
			res.Details = map[string]string{"stored_revision": fmt.Sprint(c.cfg.Store.CurrentN())}
			return res
		case errors.Is(err, store.ErrInvalidResource):
			return failure(agentapi.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "%v", err)
		case err != nil:
			return failure(agentapi.ErrorCode_ERROR_CODE_INTERNAL, "cannot stage the revision: %v", err)
		}

		issues, oerr := c.validateIn(ctx, bundle.GetConfigJson(), resources, staged.PendingResourceDir())
		if oerr != nil {
			c.cfg.Store.Discard(staged)
			return oerr.result()
		}
		if err := staged.WriteConfig(bundle.GetConfigJson()); err != nil {
			c.cfg.Store.Discard(staged)
			return failure(agentapi.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "%v", err)
		}
		rev, err := c.cfg.Store.Promote(staged)
		if err != nil {
			c.cfg.Store.Discard(staged)
			return failure(agentapi.ErrorCode_ERROR_CODE_INTERNAL, "cannot store the revision: %v", err)
		}
		c.update(func(*state) {})
		c.logAgent("info", "stored configuration revision %d (%s)", rev.N, bundle.GetProfileName())

		hot := req.GetMode() != agentapi.ApplyMode_APPLY_MODE_STORE_ONLY && c.intentConnected()
		if !hot {
			res := success("stored revision %d", rev.N)
			res.Issues = issues
			return res
		}
		return c.hotApply(ctx, rev, issues)
	}
}

// hotApply restarts the connection on the new revision and rolls back to the
// last good one if it does not come up.
func (c *Controller) hotApply(ctx context.Context, rev store.Revision, issues []*agentapi.Issue) *agentapi.OperationResult {
	c.cancelRetry()
	good, hadGood := c.cfg.Store.LastGood()
	opts := upOptions{ignoreConflicts: c.state().routeTakeover}

	c.update(func(s *state) {
		s.conn = agentapi.ConnectionState_CONNECTION_STATE_CONNECTING
		s.lastErr, s.lastErrCode = "", agentapi.ErrorCode_ERROR_CODE_UNSPECIFIED
	})
	c.teardown("apply")
	oerr := c.bringUp(ctx, rev, opts)
	if oerr == nil {
		res := success("revision %d is running", rev.N)
		res.Issues = issues
		return res
	}
	if ctx.Err() != nil || oerr.code == codeCancelled {
		c.update(func(s *state) {
			s.conn = agentapi.ConnectionState_CONNECTION_STATE_DISCONNECTED
			s.activeRev = 0
		})
		return failure(codeCancelled, "applying the configuration was interrupted; revision %d is stored", rev.N)
	}

	c.logAgent("error", "revision %d failed to start: %s", rev.N, oerr.Error())
	// Only configuration-shaped failures justify going back; an environment
	// problem (route conflict, platform error) would hit the old revision too.
	rollbackWorthy := oerr.code == agentapi.ErrorCode_ERROR_CODE_CORE_START_FAILED ||
		oerr.code == agentapi.ErrorCode_ERROR_CODE_INVALID_CONFIG ||
		oerr.code == agentapi.ErrorCode_ERROR_CODE_POLICY_DENIED
	if !rollbackWorthy || !hadGood || good.N == rev.N {
		c.update(func(s *state) {
			s.conn = agentapi.ConnectionState_CONNECTION_STATE_FAILED
			s.activeRev = 0
			s.lastErrCode, s.lastErr = oerr.code, oerr.Error()
		})
		return oerr.result()
	}

	if err := c.cfg.Store.Revert(good.N); err != nil {
		c.log.Error("revert failed", "err", err)
	}
	c.update(func(s *state) { s.conn = agentapi.ConnectionState_CONNECTION_STATE_CONNECTING })
	if rbErr := c.bringUp(ctx, good, opts); rbErr != nil {
		c.update(func(s *state) {
			s.conn = agentapi.ConnectionState_CONNECTION_STATE_FAILED
			s.activeRev = 0
			s.lastErrCode, s.lastErr = rbErr.code, "rollback failed: "+rbErr.Error()
		})
		res := oerr.result()
		res.Message += "; rolling back to revision " + fmt.Sprint(good.N) + " failed as well: " + rbErr.Error()
		return res
	}
	c.update(func(s *state) {
		s.lastErrCode, s.lastErr = oerr.code, fmt.Sprintf("revision %d failed (%s); revision %d is running again", rev.N, oerr.Error(), good.N)
	})
	c.logAgent("warn", "rolled back to revision %d", good.N)
	res := oerr.result()
	res.RolledBack = true
	res.Message = fmt.Sprintf("%s; revision %d is running again", oerr.Error(), good.N)
	return res
}

// ---------------------------------------------------------------- reconnect

// reconnectJob rebuilds the connection after the network changed, the machine
// woke up or the core died. attempt counts consecutive tries.
func (c *Controller) reconnectJob(reason string, attempt uint32) func(context.Context) *agentapi.OperationResult {
	return func(ctx context.Context) *agentapi.OperationResult {
		if !c.intentConnected() {
			return success("no longer wanted")
		}
		rev, ok := c.reconnectRevision()
		if !ok {
			return failure(agentapi.ErrorCode_ERROR_CODE_NO_CONFIG, "no configuration to reconnect with")
		}
		c.update(func(s *state) {
			s.conn = agentapi.ConnectionState_CONNECTION_STATE_RECONNECTING
			s.reconnectAttempts = attempt
		})
		c.logAgent("info", "reconnecting (%s), attempt %d", reason, attempt)
		c.teardown("reconnect: " + reason)

		oerr := c.bringUp(ctx, rev, upOptions{ignoreConflicts: c.state().routeTakeover})
		if oerr == nil {
			return success("reconnected")
		}
		if ctx.Err() != nil || oerr.code == codeCancelled {
			c.update(func(s *state) {
				s.conn = agentapi.ConnectionState_CONNECTION_STATE_DISCONNECTED
				s.activeRev = 0
			})
			return failure(codeCancelled, "reconnecting was interrupted")
		}

		c.update(func(s *state) { s.lastErrCode, s.lastErr = oerr.code, oerr.Error() })
		if retryable(oerr.code) && int(attempt) < c.cfg.MaxReconnects {
			delay := c.backoff(attempt)
			c.logAgent("warn", "reconnect attempt %d failed: %s; retrying in %s", attempt, oerr.Error(), delay)
			c.scheduleRetry(reason, attempt+1, delay)
			return oerr.result()
		}
		c.update(func(s *state) {
			s.conn = agentapi.ConnectionState_CONNECTION_STATE_FAILED
			s.activeRev = 0
		})
		c.logAgent("error", "giving up reconnecting: %s", oerr.Error())
		return oerr.result()
	}
}

func retryable(code agentapi.ErrorCode) bool {
	switch code {
	case agentapi.ErrorCode_ERROR_CODE_CORE_START_FAILED,
		agentapi.ErrorCode_ERROR_CODE_PLATFORM,
		agentapi.ErrorCode_ERROR_CODE_CLEANUP_FAILED,
		agentapi.ErrorCode_ERROR_CODE_UNAVAILABLE,
		agentapi.ErrorCode_ERROR_CODE_INTERNAL:
		return true
	}
	return false
}

func (c *Controller) backoff(attempt uint32) time.Duration {
	i := int(attempt) - 1
	if i < 0 {
		i = 0
	}
	if i >= len(c.cfg.Backoff) {
		i = len(c.cfg.Backoff) - 1
	}
	return c.cfg.Backoff[i]
}

// reconnectRevision is the revision that was running (so a reconnect never
// silently changes configuration), falling back to the stored one.
func (c *Controller) reconnectRevision() (store.Revision, bool) {
	if n := c.state().activeRev; n != 0 {
		if rev, ok := c.cfg.Store.Get(n); ok {
			return rev, true
		}
	}
	return c.cfg.Store.Current()
}

func (c *Controller) scheduleRetry(reason string, attempt uint32, delay time.Duration) {
	c.retryMu.Lock()
	defer c.retryMu.Unlock()
	if c.retryTimer != nil {
		c.retryTimer.Stop()
	}
	c.retrySeq++
	seq := c.retrySeq
	c.retryTimer = time.AfterFunc(delay, func() {
		c.retryMu.Lock()
		stale := seq != c.retrySeq
		c.retryMu.Unlock()
		if stale {
			return
		}
		c.submitInternal("reconnect", true, c.reconnectJob(reason, attempt))
	})
}

func (c *Controller) cancelRetry() {
	c.retryMu.Lock()
	defer c.retryMu.Unlock()
	c.retrySeq++
	if c.retryTimer != nil {
		c.retryTimer.Stop()
		c.retryTimer = nil
	}
}

// ----------------------------------------------------------- recovery, cleanup

func (c *Controller) recoverJob(ctx context.Context) *agentapi.OperationResult {
	defer close(c.ready)

	info := c.cfg.Ledger.Recovery()
	recovered := false
	if info.Needed {
		c.log.Warn("previous session did not end cleanly", "corrupt", info.Corrupt, "entries", len(info.Entries))
		c.logAgent("warn", "the previous session did not end cleanly; cleaning up")
		c.update(func(s *state) { s.cleanup = agentapi.CleanupState_CLEANUP_STATE_RUNNING })
		ok := true
		detail := ""
		if info.Corrupt {
			if err := c.cfg.Driver.Sweep(ctx); err != nil {
				ok, detail = false, "sweep: "+err.Error()
			}
		}
		rep := c.cfg.Ledger.Cleanup(ctx, c.cfg.Driver)
		if !rep.OK() {
			ok, detail = false, rep.Summary()
		}
		if ok {
			if detail == "" {
				detail = rep.Summary()
			}
			recovered = true
			c.update(func(s *state) {
				s.cleanup = agentapi.CleanupState_CLEANUP_STATE_RECOVERED
				s.cleanupDetail = detail
			})
		} else {
			c.applyCleanupResult(false, detail)
		}
	}
	c.update(func(s *state) {
		if s.service == agentapi.ServiceState_SERVICE_STATE_STARTING {
			s.service = agentapi.ServiceState_SERVICE_STATE_READY
		}
	})

	intent := c.cfg.Intent.Load()
	settings := c.cfg.Settings.Load()
	resume := false
	switch {
	case intent.Connected && intent.ResumeAfterUpgrade:
		resume = true
	case intent.Connected && recovered && settings.ResumeAfterCrash:
		resume = true
	}
	if intent.ResumeAfterUpgrade {
		_, _ = c.cfg.Intent.Update(func(i *store.Intent) error { i.ResumeAfterUpgrade = false; return nil })
	}
	if resume && c.cfg.Store.CurrentN() != 0 {
		c.logAgent("info", "restoring the connection that was active before the restart")
		c.update(func(s *state) { s.intentConnected = true })
		c.submitInternal("connect", true, c.connectJob(&agentapi.ConnectRequest{}, "resume"))
		return success("recovered; resuming")
	}
	// Whatever the stored intent said, we are not connected and will not
	// reconnect on our own now.
	c.setIntent(false, 0)
	return success("ready")
}

func (c *Controller) retryCleanupJob() func(context.Context) *agentapi.OperationResult {
	return func(context.Context) *agentapi.OperationResult {
		if isUp(c.state().conn) {
			return failure(agentapi.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "cleanup can only be retried while disconnected")
		}
		rep := c.runCleanup()
		if !rep.OK() {
			return failure(agentapi.ErrorCode_ERROR_CODE_CLEANUP_FAILED, "%s", rep.Summary())
		}
		return success("%s", rep.Summary())
	}
}

func (c *Controller) prepareUpgradeJob() func(context.Context) *agentapi.OperationResult {
	return func(context.Context) *agentapi.OperationResult {
		c.cancelRetry()
		wasConnected := c.intentConnected()
		c.update(func(s *state) {
			if isUp(s.conn) {
				s.conn = agentapi.ConnectionState_CONNECTION_STATE_DISCONNECTING
			}
		})
		ok := c.teardown("prepare upgrade")
		c.update(func(s *state) {
			s.conn = agentapi.ConnectionState_CONNECTION_STATE_DISCONNECTED
			s.activeRev = 0
			s.connectedSince = time.Time{}
			s.mode = ""
		})
		_, err := c.cfg.Intent.Update(func(i *store.Intent) error {
			i.Connected = wasConnected
			i.ResumeAfterUpgrade = wasConnected
			i.Updated = time.Now().UTC()
			return nil
		})
		if err != nil {
			return failure(agentapi.ErrorCode_ERROR_CODE_INTERNAL, "cannot record the connection intent: %v", err)
		}
		c.logAgent("info", "ready for upgrade (resume afterwards: %v)", wasConnected)
		if !ok {
			return failure(agentapi.ErrorCode_ERROR_CODE_CLEANUP_FAILED, "cleanup incomplete: %s", c.state().cleanupDetail)
		}
		return success("ready for upgrade")
	}
}

func toStoreResources(in []*agentapi.Resource) []store.Resource {
	out := make([]store.Resource, 0, len(in))
	for _, r := range in {
		out = append(out, store.Resource{Name: r.GetName(), Data: r.GetData(), SHA256: r.GetSha256()})
	}
	return out
}
