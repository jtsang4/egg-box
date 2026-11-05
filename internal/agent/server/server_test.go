package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"eggbox/internal/agent/engine"
	"eggbox/internal/agent/store"
	"eggbox/internal/agentapi"
)

func TestToStatusMapsErrorsOntoGRPCCodes(t *testing.T) {
	cases := []struct {
		err  error
		want codes.Code
	}{
		{nil, codes.OK},
		{fmt.Errorf("%w: bad", engine.ErrInvalid), codes.InvalidArgument},
		{fmt.Errorf("%w: bad", store.ErrInvalidResource), codes.InvalidArgument},
		{fmt.Errorf("%w: gone", engine.ErrNotFound), codes.NotFound},
		{engine.ErrNotRunning, codes.FailedPrecondition},
		{context.Canceled, codes.Canceled},
		{context.DeadlineExceeded, codes.DeadlineExceeded},
		{errors.New("disk on fire"), codes.Internal},
	}
	for _, c := range cases {
		if got := status.Code(toStatus(c.err)); got != c.want {
			t.Errorf("%v -> %v, want %v", c.err, got, c.want)
		}
	}
}

func TestStreamEndedExplainsWhyAStreamStopped(t *testing.T) {
	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := status.Code(streamEnded(cctx)); got != codes.Canceled {
		t.Fatalf("a caller who left: %v", got)
	}
	if got := status.Code(streamEnded(context.Background())); got != codes.Unavailable {
		t.Fatalf("an agent that is going away: %v", got)
	}
}

func TestCredentialsVerifyEveryConnectionBeforeGRPCSeesIt(t *testing.T) {
	closed := func(c net.Conn) bool {
		_ = c.SetReadDeadline(time.Now().Add(time.Second))
		_, err := c.Read(make([]byte, 1))
		return err != nil
	}

	// No verifier configured: nothing gets in.
	a1, a2 := net.Pipe()
	if _, _, err := NewCredentials(nil).ServerHandshake(a1); err == nil || !closed(a2) {
		t.Fatalf("no verifier: err=%v", err)
	}

	// A verifier that says no: the connection is closed before any byte is read.
	b1, b2 := net.Pipe()
	reject := NewCredentials(func(net.Conn) (Peer, error) { return Peer{}, errors.New("nope") })
	if _, _, err := reject.ServerHandshake(b1); err == nil || !closed(b2) {
		t.Fatalf("rejecting verifier: err=%v", err)
	}

	// A verifier that says yes: the connection continues, carrying the identity.
	c1, c2 := net.Pipe()
	defer c2.Close()
	accept := NewCredentials(func(net.Conn) (Peer, error) { return Peer{Description: "uid=501"}, nil })
	conn, info, err := accept.ServerHandshake(c1)
	if err != nil || conn != c1 {
		t.Fatalf("accepting verifier: %v", err)
	}
	if info.AuthType() != "eggbox-peer" || info.(PeerInfo).Description != "uid=501" {
		t.Fatalf("auth info = %#v", info)
	}

	// These credentials are for the server side only.
	if _, _, err := accept.ClientHandshake(context.Background(), "agent", c2); err == nil {
		t.Fatal("client handshake must be refused")
	}
	if accept.Info().SecurityProtocol == "" {
		t.Fatal("credentials must name their protocol")
	}
	if _, ok := accept.Clone().(creds); !ok {
		t.Fatal("clone must keep the verifier")
	}
}

func TestGuardRequiresAVerifiedPeerAndTheRightProtocol(t *testing.T) {
	s := &Server{log: slog.New(slog.DiscardHandler)}

	// Without a verified identity nothing is served, not even the handshake.
	if err := s.guard(context.Background(), agentapi.HandshakeMethod); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("unverified: %v", err)
	}

	verified := peer.NewContext(context.Background(), &peer.Peer{AuthInfo: PeerInfo{Peer{Description: "x"}}})
	if err := s.guard(verified, agentapi.HandshakeMethod); err != nil {
		t.Fatalf("the handshake is always allowed for a verified peer: %v", err)
	}
	if err := s.guard(verified, "/eggbox.agent.v1.Agent/GetStatus"); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("no version: %v", err)
	}
}

func TestAPanicInAHandlerBecomesAnInternalError(t *testing.T) {
	s := &Server{log: slog.New(slog.DiscardHandler)}
	verified := peer.NewContext(context.Background(), &peer.Peer{AuthInfo: PeerInfo{Peer{Description: "x"}}})
	_, err := s.unaryGuard(verified, nil, &grpc.UnaryServerInfo{FullMethod: agentapi.HandshakeMethod},
		func(context.Context, any) (any, error) { panic("boom") })
	if status.Code(err) != codes.Internal {
		t.Fatalf("err = %v", err)
	}
}

func TestPeerFromReturnsTheVerifiedIdentity(t *testing.T) {
	if _, ok := PeerFrom(context.Background()); ok {
		t.Fatal("no peer in a bare context")
	}
	ctx := peer.NewContext(context.Background(), &peer.Peer{AuthInfo: PeerInfo{Peer{Description: "uid=1"}}})
	if p, ok := PeerFrom(ctx); !ok || p.Description != "uid=1" {
		t.Fatalf("peer = %v %v", p, ok)
	}
}
