package server

import (
	"context"
	"errors"
	"net"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
)

// Peer is the identity a transport verified for a connection: who is on the
// other end, in words an administrator can read in a log.
type Peer struct {
	Description string
}

// Verifier decides whether the process at the other end of a freshly accepted
// connection may talk to the agent. Each transport brings its own: the
// Windows pipe checks the client process's user and executable, the macOS
// bridge hands over connections it already verified through the code-signing
// requirement of the XPC peer. A non-nil error closes the connection before a
// single byte of the protocol is read.
type Verifier func(conn net.Conn) (Peer, error)

// PeerInfo is the credentials.AuthInfo of a verified connection.
type PeerInfo struct{ Peer }

// AuthType implements credentials.AuthInfo.
func (PeerInfo) AuthType() string { return "sbui-peer" }

// PeerFrom returns the verified identity of the caller of ctx.
func PeerFrom(ctx context.Context) (Peer, bool) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return Peer{}, false
	}
	info, ok := p.AuthInfo.(PeerInfo)
	return info.Peer, ok
}

// NewCredentials returns server-side transport credentials that verify every
// connection before gRPC sees it. There is no encryption: the channel is a
// local socket, pipe or inherited descriptor, protected by who may open it
// and by this check.
func NewCredentials(v Verifier) credentials.TransportCredentials {
	return creds{verify: v}
}

type creds struct{ verify Verifier }

func (c creds) ServerHandshake(conn net.Conn) (net.Conn, credentials.AuthInfo, error) {
	if c.verify == nil {
		_ = conn.Close()
		return nil, nil, errors.New("no peer verifier configured")
	}
	p, err := c.verify(conn)
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	return conn, PeerInfo{Peer: p}, nil
}

func (creds) ClientHandshake(_ context.Context, _ string, conn net.Conn) (net.Conn, credentials.AuthInfo, error) {
	return nil, nil, errors.New("these credentials are for servers; clients dial without transport security")
}

func (creds) Info() credentials.ProtocolInfo {
	return credentials.ProtocolInfo{SecurityProtocol: "sbui-local"}
}

func (c creds) Clone() credentials.TransportCredentials { return c }

func (creds) OverrideServerName(string) error { return nil }
