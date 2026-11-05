package controller

import (
	"context"
	"path/filepath"

	"eggbox/internal/agent/engine"
	"eggbox/internal/agent/policy"
	"eggbox/internal/agent/store"
	"eggbox/internal/agentapi"
)

// Connect brings the proxy up with the stored configuration.
func (c *Controller) Connect(ctx context.Context, req *agentapi.ConnectRequest) *agentapi.OperationResult {
	return c.submit(ctx, req.GetRequestId(), "connect", true, c.connectJob(req, "requested"))
}

// Disconnect tears the proxy down. It preempts a connect or reconnect that is
// still in progress instead of waiting behind it.
func (c *Controller) Disconnect(ctx context.Context, req *agentapi.DisconnectRequest) *agentapi.OperationResult {
	c.cancelRetry()
	c.interrupt()
	return c.submit(ctx, req.GetRequestId(), "disconnect", false, c.disconnectJob("requested"))
}

// ApplyConfig validates and stores a new revision and hot-applies it when connected.
func (c *Controller) ApplyConfig(ctx context.Context, req *agentapi.ApplyConfigRequest) *agentapi.OperationResult {
	return c.submit(ctx, req.GetRequestId(), "apply", true, c.applyJob(req))
}

// RetryCleanup re-runs the resource cleanup after a failed attempt.
func (c *Controller) RetryCleanup(ctx context.Context, req *agentapi.RetryCleanupRequest) *agentapi.OperationResult {
	return c.submit(ctx, req.GetRequestId(), "cleanup", false, c.retryCleanupJob())
}

// PrepareUpgrade disconnects, cleans up and records the intent to resume afterwards.
func (c *Controller) PrepareUpgrade(ctx context.Context, req *agentapi.PrepareUpgradeRequest) *agentapi.OperationResult {
	c.cancelRetry()
	c.interrupt()
	return c.submit(ctx, req.GetRequestId(), "prepare-upgrade", false, c.prepareUpgradeJob())
}

// SelectOutbound switches the selected node of a selector group.
func (c *Controller) SelectOutbound(ctx context.Context, req *agentapi.SelectOutboundRequest) *agentapi.OperationResult {
	return c.submit(ctx, req.GetRequestId(), "select", false, func(context.Context) *agentapi.OperationResult {
		if err := c.cfg.Engine.SelectOutbound(req.GetGroupTag(), req.GetOutboundTag()); err != nil {
			return engineError(err)
		}
		return success("selected %s in %s", req.GetOutboundTag(), req.GetGroupTag())
	})
}

// SetMode switches the routing mode (rule / global / direct).
func (c *Controller) SetMode(ctx context.Context, req *agentapi.SetModeRequest) *agentapi.OperationResult {
	return c.submit(ctx, req.GetRequestId(), "mode", false, func(context.Context) *agentapi.OperationResult {
		if err := c.cfg.Engine.SetMode(req.GetMode()); err != nil {
			return engineError(err)
		}
		c.update(func(s *state) { s.mode = req.GetMode() })
		return success("mode is %s", req.GetMode())
	})
}

// CloseConnection closes one live connection.
func (c *Controller) CloseConnection(ctx context.Context, req *agentapi.CloseConnectionRequest) *agentapi.OperationResult {
	return c.submit(ctx, req.GetRequestId(), "close-connection", false, func(context.Context) *agentapi.OperationResult {
		if err := c.cfg.Engine.CloseConnection(req.GetId()); err != nil {
			return engineError(err)
		}
		return success("closed")
	})
}

// CloseAllConnections closes every live connection.
func (c *Controller) CloseAllConnections(ctx context.Context, req *agentapi.CloseAllConnectionsRequest) *agentapi.OperationResult {
	return c.submit(ctx, req.GetRequestId(), "close-all", false, func(context.Context) *agentapi.OperationResult {
		if err := c.cfg.Engine.CloseAllConnections(); err != nil {
			return engineError(err)
		}
		return success("closed all connections")
	})
}

// URLTest measures delay for an outbound or a whole group. It does not change
// state, so it runs outside the serialized queue (a slow test must never delay
// a disconnect) and is bound to the caller's context.
func (c *Controller) URLTest(ctx context.Context, req *agentapi.URLTestRequest) *agentapi.OperationResult {
	if err := c.cfg.Engine.URLTest(ctx, req.GetOutboundTag()); err != nil {
		res := engineError(err)
		res.RequestId = req.GetRequestId()
		return res
	}
	res := success("tested %s", req.GetOutboundTag())
	res.RequestId = req.GetRequestId()
	res.StatusSeq = c.Status().GetSeq()
	return res
}

// ValidateConfig runs the full preparation pipeline without storing anything.
func (c *Controller) ValidateConfig(ctx context.Context, bundle *agentapi.ConfigBundle) *agentapi.ValidateConfigResponse {
	resources := toStoreResources(bundle.GetResources())
	sc, err := c.cfg.Store.Scratch(resources)
	if err != nil {
		return &agentapi.ValidateConfigResponse{Issues: []*agentapi.Issue{{
			Severity: agentapi.Issue_SEVERITY_ERROR, Path: "$.resources", Code: "resource_invalid", Message: err.Error(),
		}}}
	}
	defer sc.Close()

	select {
	case c.validateSem <- struct{}{}:
		defer func() { <-c.validateSem }()
	case <-ctx.Done():
		return &agentapi.ValidateConfigResponse{Issues: []*agentapi.Issue{{
			Severity: agentapi.Issue_SEVERITY_ERROR, Path: "$", Code: "cancelled", Message: "validation was cancelled",
		}}}
	}

	issues, oerr := c.validateIn(ctx, bundle.GetConfigJson(), resources, sc.ResourceDir())
	if oerr != nil {
		list := oerr.issues
		if len(list) == 0 {
			list = []*agentapi.Issue{{Severity: agentapi.Issue_SEVERITY_ERROR, Path: "$", Code: "rejected", Message: oerr.Error()}}
		}
		return &agentapi.ValidateConfigResponse{Valid: false, Issues: list}
	}
	return &agentapi.ValidateConfigResponse{Valid: true, Issues: issues}
}

// validateIn normalizes the configuration under the policy and instantiates it
// in the core without starting it.
func (c *Controller) validateIn(ctx context.Context, raw []byte, resources []store.Resource, resourceDir string) ([]*agentapi.Issue, *opError) {
	settings := c.cfg.Settings.Load()
	defaults := c.cfg.Driver.TunDefaults()
	names := make(map[string]bool, len(resources))
	for _, r := range resources {
		names[r.Name] = true
	}
	env := policy.Env{
		ResourceDir: resourceDir,
		Resources:   names,
		// A dry run must never open the real cache database.
		CachePath: filepath.Join(filepath.Dir(resourceDir), "check-cache.db"),
		LogLevel:  settings.LogLevel,
		Tun: policy.TunSettings{
			Addresses:     prefixStrings(c.cfg.Candidates[0].Addresses()),
			StrictRoute:   defaults.StrictRoute,
			DNSMode:       defaults.DNSMode,
			InterfaceName: defaults.InterfaceName,
		},
	}
	if c.cfg.Driver.Sandbox() {
		env.SandboxPort = 17890 // placeholder: the dry run never listens
	}
	res := c.cfg.Policy.Normalize(raw, env)
	if res.HasErrors() {
		return nil, policyError(res)
	}
	err := c.cfg.Engine.Check(ctx, engine.Prepared{JSON: res.JSON, HasTun: res.HasTun, TunTag: res.TunTag, LogLevel: settings.LogLevel})
	if err != nil {
		issues := append(toProtoIssues(res.Issues), &agentapi.Issue{
			Severity: agentapi.Issue_SEVERITY_ERROR, Path: "$", Code: "core_rejected", Message: err.Error(),
		})
		return nil, &opError{code: agentapi.ErrorCode_ERROR_CODE_INVALID_CONFIG, msg: "the core rejected the configuration", cause: err, issues: issues}
	}
	return toProtoIssues(res.Issues), nil
}
