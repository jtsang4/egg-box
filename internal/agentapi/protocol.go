package agentapi

import (
	"context"
	"strconv"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// ProtocolMetadataKey carries the caller's protocol version on every call, so
// the agent can refuse a peer that speaks another version of the contract
// before it acts on anything it might misread. Handshake is the one method
// allowed through: it is how a client learns what the agent speaks.
const ProtocolMetadataKey = "eggbox-protocol"

// HandshakeMethod is the full gRPC method name of Handshake.
const HandshakeMethod = "/eggbox.agent.v1.Agent/Handshake"

// WithProtocol returns client interceptors that stamp every call with version.
func WithProtocol(version uint32) []grpc.DialOption {
	value := strconv.FormatUint(uint64(version), 10)
	stamp := func(ctx context.Context) context.Context {
		return metadata.AppendToOutgoingContext(ctx, ProtocolMetadataKey, value)
	}
	return []grpc.DialOption{
		grpc.WithChainUnaryInterceptor(func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
			return invoker(stamp(ctx), method, req, reply, cc, opts...)
		}),
		grpc.WithChainStreamInterceptor(func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
			return streamer(stamp(ctx), desc, cc, method, opts...)
		}),
	}
}

// ProtocolFrom reads the version a call announced; ok is false when it did not.
func ProtocolFrom(ctx context.Context) (version uint32, ok bool) {
	md, found := metadata.FromIncomingContext(ctx)
	if !found {
		return 0, false
	}
	vals := md.Get(ProtocolMetadataKey)
	if len(vals) != 1 {
		return 0, false
	}
	n, err := strconv.ParseUint(vals[0], 10, 32)
	if err != nil {
		return 0, false
	}
	return uint32(n), true
}
