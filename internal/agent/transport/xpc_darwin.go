//go:build darwin && !ios

package transport

import (
	"errors"
	"fmt"
	"net"

	"eggbox/internal/agent/server"
	"eggbox/internal/darwin/bridge"
)

// ListenXPC makes the agent reachable over the XPC channel of the service
// bridge. The system has already checked the code signature of every caller
// against requirement by the time a connection arrives here; the verifier
// then asks the user policy, so only the authorized user's application gets
// to speak the protocol. With an empty service name the channel is anonymous
// and private to this process, which is how tests and the development agent
// use it.
func ListenXPC(b *bridge.Bridge, service, requirement string, policy UIDPolicy) (net.Listener, server.Verifier, error) {
	l, err := b.Serve(service, requirement)
	if err != nil {
		return nil, nil, fmt.Errorf("listen on the XPC service %q: %w", service, err)
	}
	return l, XPCVerifier(policy), nil
}

// XPCVerifier turns the policy into a Verifier: it asks the system who is on
// the other end of the connection, and only then asks the policy.
func XPCVerifier(policy UIDPolicy) server.Verifier {
	return func(conn net.Conn) (server.Peer, error) {
		p, ok := bridge.PeerOf(conn)
		if !ok {
			return server.Peer{}, errors.New("not a connection introduced over XPC")
		}
		return policy.Check(p.UID, p.PID)
	}
}
