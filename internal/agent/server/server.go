// Package server exposes the controller to the GUI over gRPC.
//
// The service is deliberately thin: it translates, it never decides. Every
// decision (what to run, in what order, what is allowed) lives in the
// controller and the policy, so a second front end or a test client gets
// exactly the same behaviour.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"eggbox/internal/agent/controller"
	"eggbox/internal/agent/engine"
	"eggbox/internal/agent/loghub"
	"eggbox/internal/agent/store"
	"eggbox/internal/agentapi"
	"eggbox/internal/brand"
)

// maxLogReplay bounds how many buffered log lines one call may ask for.
const maxLogReplay = 5000

// Config describes the agent as the handshake reports it.
type Config struct {
	Controller *controller.Controller
	// Version is the agent build version.
	Version string
	// Driver names the platform driver ("darwin", "windows" or "sim").
	Driver string
	// DevMode marks a development agent (sandbox driver, development transport).
	DevMode bool
	// InstanceID changes every time the agent process starts.
	InstanceID string
	StartedAt  time.Time
	Logger     *slog.Logger
}

// Server implements agentapi.AgentServer.
type Server struct {
	agentapi.UnimplementedAgentServer
	cfg Config
	c   *controller.Controller
	log *slog.Logger
}

// New returns the service. Serve it with GRPC.
func New(cfg Config) (*Server, error) {
	if cfg.Controller == nil {
		return nil, errors.New("server: a controller is required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.StartedAt.IsZero() {
		cfg.StartedAt = time.Now()
	}
	return &Server{cfg: cfg, c: cfg.Controller, log: cfg.Logger}, nil
}

var _ agentapi.AgentServer = (*Server)(nil)

// ------------------------------------------------------------------ handshake

func (s *Server) Handshake(ctx context.Context, req *agentapi.HandshakeRequest) (*agentapi.HandshakeResponse, error) {
	p, _ := PeerFrom(ctx)
	return &agentapi.HandshakeResponse{
		ProtocolVersion:  brand.ProtocolVersion,
		AgentVersion:     s.cfg.Version,
		CoreVersion:      s.c.Status().GetCoreVersion(),
		Platform:         runtime.GOOS,
		Arch:             runtime.GOARCH,
		AgentPid:         uint32(os.Getpid()),
		AgentStartedUnix: s.cfg.StartedAt.Unix(),
		InstanceId:       s.cfg.InstanceID,
		Peer:             p.Description,
		Driver:           s.cfg.Driver,
		DevMode:          s.cfg.DevMode,
	}, nil
}

// --------------------------------------------------------------------- status

func (s *Server) GetStatus(context.Context, *emptypb.Empty) (*agentapi.Status, error) {
	return s.c.Status(), nil
}

func (s *Server) WatchStatus(_ *emptypb.Empty, stream grpc.ServerStreamingServer[agentapi.Status]) error {
	return pump(stream.Context(), s.c.WatchStatus(stream.Context()), stream.Send)
}

// pump forwards a controller stream until it ends or the client goes away.
func pump[T any](ctx context.Context, ch <-chan *T, send func(*T) error) error {
	for v := range ch {
		if err := send(v); err != nil {
			return err
		}
	}
	return streamEnded(ctx)
}

// streamEnded explains why a stream stopped: the caller left, or the agent is going away.
func streamEnded(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return status.FromContextError(err).Err()
	}
	return status.Error(codes.Unavailable, "the agent is shutting down")
}

// ------------------------------------------------------------------- settings

func (s *Server) GetSettings(context.Context, *emptypb.Empty) (*agentapi.Settings, error) {
	return s.c.GetSettings(), nil
}

func (s *Server) UpdateSettings(_ context.Context, req *agentapi.UpdateSettingsRequest) (*agentapi.Settings, error) {
	out, err := s.c.UpdateSettings(req)
	if err != nil {
		return nil, toStatus(err)
	}
	return out, nil
}

// toStatus maps a plain error from the controller onto a gRPC status.
func toStatus(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, engine.ErrInvalid), errors.Is(err, store.ErrInvalidResource):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, engine.ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, engine.ErrNotRunning):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, err.Error())
	}
	return status.Error(codes.Internal, err.Error())
}

// --------------------------------------------------------------------- config

func (s *Server) ValidateConfig(ctx context.Context, req *agentapi.ValidateConfigRequest) (*agentapi.ValidateConfigResponse, error) {
	if req.GetBundle() == nil {
		return nil, status.Error(codes.InvalidArgument, "a configuration bundle is required")
	}
	return s.c.ValidateConfig(ctx, req.GetBundle()), nil
}

func (s *Server) ApplyConfig(ctx context.Context, req *agentapi.ApplyConfigRequest) (*agentapi.OperationResult, error) {
	if req.GetBundle() == nil {
		return nil, status.Error(codes.InvalidArgument, "a configuration bundle is required")
	}
	return s.c.ApplyConfig(ctx, req), nil
}

// ----------------------------------------------------------------- operations

func (s *Server) Connect(ctx context.Context, req *agentapi.ConnectRequest) (*agentapi.OperationResult, error) {
	return s.c.Connect(ctx, req), nil
}

func (s *Server) Disconnect(ctx context.Context, req *agentapi.DisconnectRequest) (*agentapi.OperationResult, error) {
	return s.c.Disconnect(ctx, req), nil
}

func (s *Server) SelectOutbound(ctx context.Context, req *agentapi.SelectOutboundRequest) (*agentapi.OperationResult, error) {
	return s.c.SelectOutbound(ctx, req), nil
}

func (s *Server) URLTest(ctx context.Context, req *agentapi.URLTestRequest) (*agentapi.OperationResult, error) {
	return s.c.URLTest(ctx, req), nil
}

func (s *Server) SetMode(ctx context.Context, req *agentapi.SetModeRequest) (*agentapi.OperationResult, error) {
	return s.c.SetMode(ctx, req), nil
}

func (s *Server) CloseConnection(ctx context.Context, req *agentapi.CloseConnectionRequest) (*agentapi.OperationResult, error) {
	return s.c.CloseConnection(ctx, req), nil
}

func (s *Server) CloseAllConnections(ctx context.Context, req *agentapi.CloseAllConnectionsRequest) (*agentapi.OperationResult, error) {
	return s.c.CloseAllConnections(ctx, req), nil
}

func (s *Server) RetryCleanup(ctx context.Context, req *agentapi.RetryCleanupRequest) (*agentapi.OperationResult, error) {
	return s.c.RetryCleanup(ctx, req), nil
}

func (s *Server) PrepareUpgrade(ctx context.Context, req *agentapi.PrepareUpgradeRequest) (*agentapi.OperationResult, error) {
	return s.c.PrepareUpgrade(ctx, req), nil
}

// ---------------------------------------------------------------- observation

func (s *Server) WatchGroups(_ *emptypb.Empty, stream grpc.ServerStreamingServer[agentapi.Groups]) error {
	return pump(stream.Context(), s.c.WatchGroups(stream.Context()), stream.Send)
}

func (s *Server) WatchTraffic(req *agentapi.WatchTrafficRequest, stream grpc.ServerStreamingServer[agentapi.Traffic]) error {
	interval := time.Duration(req.GetIntervalMs()) * time.Millisecond
	return pump(stream.Context(), s.c.WatchTraffic(stream.Context(), interval), stream.Send)
}

func (s *Server) WatchConnections(req *agentapi.WatchConnectionsRequest, stream grpc.ServerStreamingServer[agentapi.ConnectionEvents]) error {
	interval := time.Duration(req.GetIntervalMs()) * time.Millisecond
	return pump(stream.Context(), s.c.WatchConnections(stream.Context(), interval), stream.Send)
}

// WatchLogs replays the newest lines in the first batch (marked reset) and then
// streams new ones, several to a batch when they arrive faster than the client
// reads them.
func (s *Server) WatchLogs(req *agentapi.WatchLogsRequest, stream grpc.ServerStreamingServer[agentapi.LogBatch]) error {
	ctx := stream.Context()
	tail := int(req.GetTail())
	if tail <= 0 || tail > maxLogReplay {
		tail = maxLogReplay
	}
	if lvl := req.GetMinLevel(); lvl != "" && !loghub.ValidLevel(lvl) {
		return status.Errorf(codes.InvalidArgument, "unknown log level %q", lvl)
	}
	replay, ch := s.c.Logs().Subscribe(ctx, tail, req.GetMinLevel())
	if err := stream.Send(&agentapi.LogBatch{Entries: replay, Reset_: true}); err != nil {
		return err
	}
	for first := range ch {
		batch := []*agentapi.LogEntry{first}
	drain:
		for len(batch) < 256 {
			select {
			case e, ok := <-ch:
				if !ok {
					break drain
				}
				batch = append(batch, e)
			default:
				break drain
			}
		}
		if err := stream.Send(&agentapi.LogBatch{Entries: batch}); err != nil {
			return err
		}
	}
	return streamEnded(ctx)
}

// ---------------------------------------------------------------- diagnostics

func (s *Server) GetDiagnostics(ctx context.Context, _ *agentapi.GetDiagnosticsRequest) (*agentapi.DiagnosticsReport, error) {
	report, err := s.c.GetDiagnostics(ctx)
	if err != nil {
		return nil, toStatus(fmt.Errorf("diagnostics: %w", err))
	}
	return report, nil
}
