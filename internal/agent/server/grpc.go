package server

import (
	"context"
	"runtime/debug"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"

	"eggbox/internal/agent/store"
	"eggbox/internal/agentapi"
	"eggbox/internal/brand"
)

const (
	// maxRecv fits the largest legal configuration bundle (configuration plus
	// every resource) and a little slack for the message framing.
	maxRecv = store.MaxConfigBytes + store.MaxResourceTotal + 1<<20
	// maxSend bounds what the agent ever sends: statuses, groups, log batches.
	maxSend = 16 << 20
	// maxStreams bounds concurrent RPCs per connection; the GUI opens about eight.
	maxStreams = 64
)

// GRPC returns a gRPC server that serves s on connections verified by creds.
// The caller owns the listener and decides when to stop.
func (s *Server) GRPC(creds credentials.TransportCredentials) *grpc.Server {
	g := grpc.NewServer(
		grpc.Creds(creds),
		grpc.ChainUnaryInterceptor(s.unaryGuard),
		grpc.ChainStreamInterceptor(s.streamGuard),
		grpc.MaxRecvMsgSize(maxRecv),
		grpc.MaxSendMsgSize(maxSend),
		grpc.MaxConcurrentStreams(maxStreams),
		grpc.KeepaliveParams(keepalive.ServerParameters{Time: 30 * time.Second, Timeout: 10 * time.Second}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: 5 * time.Second, PermitWithoutStream: true}),
	)
	agentapi.RegisterAgentServer(g, s)
	return g
}

// guard admits a call: the transport verified the caller, and the caller speaks
// this version of the contract (Handshake excepted, which is how it finds out).
func (s *Server) guard(ctx context.Context, method string) error {
	if _, ok := PeerFrom(ctx); !ok {
		return status.Error(codes.PermissionDenied, "the caller's identity was not verified")
	}
	if method == agentapi.HandshakeMethod {
		return nil
	}
	v, ok := agentapi.ProtocolFrom(ctx)
	if !ok {
		return status.Error(codes.FailedPrecondition, "the call does not state a protocol version; handshake first")
	}
	if v != brand.ProtocolVersion {
		return status.Errorf(codes.FailedPrecondition, "protocol version %d is not supported, the agent speaks %d", v, brand.ProtocolVersion)
	}
	return nil
}

func (s *Server) unaryGuard(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
	defer s.recoverPanic(info.FullMethod, &err)
	if err := s.guard(ctx, info.FullMethod); err != nil {
		return nil, err
	}
	return handler(ctx, req)
}

func (s *Server) streamGuard(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
	defer s.recoverPanic(info.FullMethod, &err)
	if err := s.guard(ss.Context(), info.FullMethod); err != nil {
		return err
	}
	return handler(srv, ss)
}

// recoverPanic keeps a bug in one handler from taking the agent (and with it
// the user's connection) down.
func (s *Server) recoverPanic(method string, err *error) {
	if r := recover(); r != nil {
		s.log.Error("panic in an API handler", "method", method, "panic", r, "stack", string(debug.Stack()))
		*err = status.Error(codes.Internal, "internal error")
	}
}
