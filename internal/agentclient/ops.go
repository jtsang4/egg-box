package agentclient

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"eggbox/internal/agentapi"
)

// OpError is an operation the agent understood and refused or could not finish.
// It carries everything the interface needs to explain why.
type OpError struct {
	Code       agentapi.ErrorCode
	Message    string
	Issues     []*agentapi.Issue
	RolledBack bool
	Details    map[string]string
	// Result is the agent's full answer.
	Result *agentapi.OperationResult
}

func (e *OpError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = strings.ToLower(strings.TrimPrefix(e.Code.String(), "ERROR_CODE_"))
	}
	if e.RolledBack {
		msg += " (the previous configuration is running again)"
	}
	return msg
}

// Is lets callers test for a class of failure: errors.Is(err, &OpError{Code: ...}).
func (e *OpError) Is(target error) bool {
	t, ok := target.(*OpError)
	return ok && t.Code == e.Code
}

// CodeOf returns the agent's error code of err, or UNSPECIFIED.
func CodeOf(err error) agentapi.ErrorCode {
	var oe *OpError
	if errors.As(err, &oe) {
		return oe.Code
	}
	return agentapi.ErrorCode_ERROR_CODE_UNSPECIFIED
}

// retriable reports whether a call can safely be repeated after err: the
// connection was lost before an answer arrived, and requests carry an id the
// agent deduplicates on.
func retriable(err error) bool {
	if errors.Is(err, ErrNotReady) {
		return true
	}
	return status.Code(err) == codes.Unavailable
}

// do runs one state-changing call. On a lost connection it waits for the link
// and repeats the call with the same request id, so the agent answers from its
// idempotency cache when the first attempt did get through.
func (c *Client) do(ctx context.Context, call func(ctx context.Context, api agentapi.AgentClient, requestID string) (*agentapi.OperationResult, error)) (*agentapi.OperationResult, error) {
	id := NewRequestID()
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			if err := c.WaitReady(ctx); err != nil {
				return nil, lastErr
			}
		}
		api, err := c.API()
		if err == nil {
			var res *agentapi.OperationResult
			if res, err = call(ctx, api, id); err == nil {
				if !res.GetOk() {
					return res, &OpError{
						Code: res.GetErrorCode(), Message: res.GetMessage(), Issues: res.GetIssues(),
						RolledBack: res.GetRolledBack(), Details: res.GetDetails(), Result: res,
					}
				}
				return res, nil
			}
		}
		lastErr = err
		if !retriable(err) || ctx.Err() != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

// ConnectOptions are the choices a connect request carries.
type ConnectOptions struct {
	// ExpectedRevision, when non-zero, must equal the agent's stored revision.
	ExpectedRevision uint64
	// IgnoreRouteConflicts proceeds although another VPN owns overlapping routes.
	IgnoreRouteConflicts bool
}

// Connect asks the agent to bring the proxy up.
func (c *Client) Connect(ctx context.Context, o ConnectOptions) (*agentapi.OperationResult, error) {
	return c.do(ctx, func(ctx context.Context, api agentapi.AgentClient, id string) (*agentapi.OperationResult, error) {
		return api.Connect(ctx, &agentapi.ConnectRequest{RequestId: id, ExpectedRevision: o.ExpectedRevision, IgnoreRouteConflicts: o.IgnoreRouteConflicts})
	})
}

// Disconnect takes the proxy down.
func (c *Client) Disconnect(ctx context.Context) (*agentapi.OperationResult, error) {
	return c.do(ctx, func(ctx context.Context, api agentapi.AgentClient, id string) (*agentapi.OperationResult, error) {
		return api.Disconnect(ctx, &agentapi.DisconnectRequest{RequestId: id})
	})
}

// ApplyConfig stores (and, when connected, hot-applies) a configuration.
func (c *Client) ApplyConfig(ctx context.Context, expectedRevision uint64, bundle *agentapi.ConfigBundle, mode agentapi.ApplyMode) (*agentapi.OperationResult, error) {
	return c.do(ctx, func(ctx context.Context, api agentapi.AgentClient, id string) (*agentapi.OperationResult, error) {
		return api.ApplyConfig(ctx, &agentapi.ApplyConfigRequest{RequestId: id, ExpectedRevision: expectedRevision, Bundle: bundle, Mode: mode})
	})
}

// SelectOutbound switches the node of a selector group.
func (c *Client) SelectOutbound(ctx context.Context, group, outbound string) (*agentapi.OperationResult, error) {
	return c.do(ctx, func(ctx context.Context, api agentapi.AgentClient, id string) (*agentapi.OperationResult, error) {
		return api.SelectOutbound(ctx, &agentapi.SelectOutboundRequest{RequestId: id, GroupTag: group, OutboundTag: outbound})
	})
}

// URLTest measures the latency of an outbound or a whole group.
func (c *Client) URLTest(ctx context.Context, tag string) (*agentapi.OperationResult, error) {
	return c.do(ctx, func(ctx context.Context, api agentapi.AgentClient, id string) (*agentapi.OperationResult, error) {
		return api.URLTest(ctx, &agentapi.URLTestRequest{RequestId: id, OutboundTag: tag})
	})
}

// SetMode switches the routing mode (Rule, Global, Direct).
func (c *Client) SetMode(ctx context.Context, mode string) (*agentapi.OperationResult, error) {
	return c.do(ctx, func(ctx context.Context, api agentapi.AgentClient, id string) (*agentapi.OperationResult, error) {
		return api.SetMode(ctx, &agentapi.SetModeRequest{RequestId: id, Mode: mode})
	})
}

// CloseConnection drops one tracked connection.
func (c *Client) CloseConnection(ctx context.Context, connID string) (*agentapi.OperationResult, error) {
	return c.do(ctx, func(ctx context.Context, api agentapi.AgentClient, id string) (*agentapi.OperationResult, error) {
		return api.CloseConnection(ctx, &agentapi.CloseConnectionRequest{RequestId: id, Id: connID})
	})
}

// CloseAllConnections drops every tracked connection.
func (c *Client) CloseAllConnections(ctx context.Context) (*agentapi.OperationResult, error) {
	return c.do(ctx, func(ctx context.Context, api agentapi.AgentClient, id string) (*agentapi.OperationResult, error) {
		return api.CloseAllConnections(ctx, &agentapi.CloseAllConnectionsRequest{RequestId: id})
	})
}

// RetryCleanup runs the agent's resource cleanup again after a failure.
func (c *Client) RetryCleanup(ctx context.Context) (*agentapi.OperationResult, error) {
	return c.do(ctx, func(ctx context.Context, api agentapi.AgentClient, id string) (*agentapi.OperationResult, error) {
		return api.RetryCleanup(ctx, &agentapi.RetryCleanupRequest{RequestId: id})
	})
}

// PrepareUpgrade disconnects, cleans up and records the intent to resume, for an installer.
func (c *Client) PrepareUpgrade(ctx context.Context) (*agentapi.OperationResult, error) {
	return c.do(ctx, func(ctx context.Context, api agentapi.AgentClient, id string) (*agentapi.OperationResult, error) {
		return api.PrepareUpgrade(ctx, &agentapi.PrepareUpgradeRequest{RequestId: id})
	})
}

// call runs a read-only request, repeating it once the link is back.
func call[T any](ctx context.Context, c *Client, fn func(api agentapi.AgentClient) (*T, error)) (*T, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			if err := c.WaitReady(ctx); err != nil {
				return nil, lastErr
			}
		}
		api, err := c.API()
		if err == nil {
			var out *T
			if out, err = fn(api); err == nil {
				return out, nil
			}
		}
		lastErr = err
		if !retriable(err) || ctx.Err() != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

// Status reads the current status once.
func (c *Client) Status(ctx context.Context) (*agentapi.Status, error) {
	return call(ctx, c, func(api agentapi.AgentClient) (*agentapi.Status, error) { return api.GetStatus(ctx, &emptypb.Empty{}) })
}

// Settings reads the agent's settings.
func (c *Client) Settings(ctx context.Context) (*agentapi.Settings, error) {
	return call(ctx, c, func(api agentapi.AgentClient) (*agentapi.Settings, error) {
		return api.GetSettings(ctx, &emptypb.Empty{})
	})
}

// UpdateSettings changes the supplied settings.
func (c *Client) UpdateSettings(ctx context.Context, req *agentapi.UpdateSettingsRequest) (*agentapi.Settings, error) {
	if req.RequestId == "" {
		req.RequestId = NewRequestID()
	}
	return call(ctx, c, func(api agentapi.AgentClient) (*agentapi.Settings, error) { return api.UpdateSettings(ctx, req) })
}

// Validate checks a configuration bundle without storing or starting it.
func (c *Client) Validate(ctx context.Context, bundle *agentapi.ConfigBundle) (*agentapi.ValidateConfigResponse, error) {
	return call(ctx, c, func(api agentapi.AgentClient) (*agentapi.ValidateConfigResponse, error) {
		return api.ValidateConfig(ctx, &agentapi.ValidateConfigRequest{Bundle: bundle})
	})
}

// Diagnostics fetches the agent's redacted report.
func (c *Client) Diagnostics(ctx context.Context) (*agentapi.DiagnosticsReport, error) {
	return call(ctx, c, func(api agentapi.AgentClient) (*agentapi.DiagnosticsReport, error) {
		return api.GetDiagnostics(ctx, &agentapi.GetDiagnosticsRequest{})
	})
}

// ------------------------------------------------------------------- streams

// watch keeps a server stream flowing for as long as ctx lives: it opens the
// stream when the link is ready and opens it again after the link drops. The
// agent starts every stream with what a new reader needs (the current status,
// a reset marker), so re-opening never leaves the caller with stale state.
func watch[T any](ctx context.Context, c *Client, open func(ctx context.Context, api agentapi.AgentClient) (grpc.ServerStreamingClient[T], error), fn func(*T)) {
	for ctx.Err() == nil {
		if err := c.WaitReady(ctx); err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(500 * time.Millisecond):
			}
			continue
		}
		api, err := c.API()
		if err != nil {
			continue
		}
		sctx, cancel := context.WithCancel(ctx)
		stream, err := open(sctx, api)
		if err == nil {
			for {
				m, rerr := stream.Recv()
				if rerr != nil {
					err = rerr
					break
				}
				fn(m)
			}
		}
		cancel()
		switch status.Code(err) {
		case codes.InvalidArgument, codes.PermissionDenied:
			return // retrying cannot help
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(300 * time.Millisecond):
		}
	}
}

// WatchStatus calls fn with the current status and every change, until ctx ends.
func (c *Client) WatchStatus(ctx context.Context, fn func(*agentapi.Status)) {
	watch(ctx, c, func(ctx context.Context, api agentapi.AgentClient) (grpc.ServerStreamingClient[agentapi.Status], error) {
		return api.WatchStatus(ctx, &emptypb.Empty{})
	}, fn)
}

// WatchGroups calls fn with the outbound groups whenever they change.
func (c *Client) WatchGroups(ctx context.Context, fn func(*agentapi.Groups)) {
	watch(ctx, c, func(ctx context.Context, api agentapi.AgentClient) (grpc.ServerStreamingClient[agentapi.Groups], error) {
		return api.WatchGroups(ctx, &emptypb.Empty{})
	}, fn)
}

// WatchTraffic calls fn with a throughput sample every interval.
func (c *Client) WatchTraffic(ctx context.Context, interval time.Duration, fn func(*agentapi.Traffic)) {
	watch(ctx, c, func(ctx context.Context, api agentapi.AgentClient) (grpc.ServerStreamingClient[agentapi.Traffic], error) {
		return api.WatchTraffic(ctx, &agentapi.WatchTrafficRequest{IntervalMs: uint32(interval / time.Millisecond)})
	}, fn)
}

// WatchConnections calls fn with connection events. The first event batch after
// every (re)connection has Reset set: discard what you know.
func (c *Client) WatchConnections(ctx context.Context, interval time.Duration, fn func(*agentapi.ConnectionEvents)) {
	watch(ctx, c, func(ctx context.Context, api agentapi.AgentClient) (grpc.ServerStreamingClient[agentapi.ConnectionEvents], error) {
		return api.WatchConnections(ctx, &agentapi.WatchConnectionsRequest{IntervalMs: uint32(interval / time.Millisecond)})
	}, fn)
}

// WatchLogs calls fn with log batches; the first has Reset set and replays the
// newest tail lines.
func (c *Client) WatchLogs(ctx context.Context, tail int, minLevel string, fn func(*agentapi.LogBatch)) {
	watch(ctx, c, func(ctx context.Context, api agentapi.AgentClient) (grpc.ServerStreamingClient[agentapi.LogBatch], error) {
		return api.WatchLogs(ctx, &agentapi.WatchLogsRequest{Tail: uint32(max(tail, 0)), MinLevel: minLevel})
	}, fn)
}

// Describe renders a link state for people.
func (l Link) Describe() string {
	switch l.State {
	case LinkReady:
		return "connected to the agent"
	case LinkConnecting:
		return "connecting to the agent"
	case LinkClosed:
		return "closed"
	}
	if l.Err != nil {
		return fmt.Sprintf("%s: %v", l.State, l.Err)
	}
	return l.State.String()
}
