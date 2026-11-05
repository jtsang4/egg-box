package singbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/sagernet/sing-box/daemon"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"eggbox/internal/agent/engine"
	"eggbox/internal/agentapi"
)

// sink adapts a callback to the server-stream interface sing-box's daemon
// service expects, so its streaming methods can be consumed in-process without
// a gRPC connection. Send is called from the daemon's goroutine; returning an
// error ends the stream.
type sink[T any] struct {
	ctx  context.Context
	send func(*T) error
}

func newSink[T any](ctx context.Context, send func(*T) error) *sink[T] {
	return &sink[T]{ctx: ctx, send: send}
}

func (s *sink[T]) Send(m *T) error              { return s.send(m) }
func (s *sink[T]) SetHeader(metadata.MD) error  { return nil }
func (s *sink[T]) SendHeader(metadata.MD) error { return nil }
func (s *sink[T]) SetTrailer(metadata.MD)       {}
func (s *sink[T]) Context() context.Context     { return s.ctx }
func (s *sink[T]) SendMsg(any) error            { return nil }
func (s *sink[T]) RecvMsg(any) error            { return io.EOF }

// platformHandler is the daemon's hook for platform debugging; there is
// nothing to do here.
type platformHandler struct{}

func (platformHandler) WriteDebugMessage(string)        {}
func (platformHandler) ConnectSSHAgent() (int32, error) { return 0, errors.New("not supported") }

// ---------------------------------------------------------------- conversion

func convertGroups(g *daemon.Groups) *agentapi.Groups {
	out := &agentapi.Groups{}
	for _, in := range g.GetGroup() {
		group := &agentapi.Group{Tag: in.Tag, Type: in.Type, Selectable: in.Selectable, Selected: in.Selected}
		for _, it := range in.Items {
			group.Items = append(group.Items, &agentapi.GroupItem{
				Tag: it.Tag, Type: it.Type, DelayMs: it.UrlTestDelay, TestedUnix: it.UrlTestTime,
			})
		}
		out.Groups = append(out.Groups, group)
	}
	return out
}

func convertConnection(c *daemon.Connection) *agentapi.Connection {
	if c == nil {
		return nil
	}
	out := &agentapi.Connection{
		Id: c.Id, Inbound: c.Inbound, Network: c.Network, Source: c.Source,
		Destination: c.Destination, Domain: c.Domain, Protocol: c.Protocol, Rule: c.Rule,
		Outbound: c.Outbound, OutboundType: c.OutboundType, Chain: c.ChainList,
		CreatedUnixMs: c.CreatedAt, ClosedUnixMs: c.ClosedAt,
		UplinkTotal: c.UplinkTotal, DownlinkTotal: c.DownlinkTotal,
	}
	if p := c.ProcessInfo; p != nil && (p.ProcessId != 0 || p.ProcessPath != "") {
		out.Process = &agentapi.ProcessInfo{Pid: p.ProcessId, Path: p.ProcessPath, User: p.UserName}
	}
	return out
}

func convertConnectionEvents(in *daemon.ConnectionEvents) *agentapi.ConnectionEvents {
	out := &agentapi.ConnectionEvents{Reset_: in.Reset_}
	for _, ev := range in.Events {
		e := &agentapi.ConnectionEvent{
			Id: ev.Id, Connection: convertConnection(ev.Connection),
			UplinkDelta: ev.UplinkDelta, DownlinkDelta: ev.DownlinkDelta,
		}
		switch ev.Type {
		case daemon.ConnectionEventType_CONNECTION_EVENT_NEW:
			e.Type = agentapi.ConnectionEvent_TYPE_NEW
		case daemon.ConnectionEventType_CONNECTION_EVENT_UPDATE:
			e.Type = agentapi.ConnectionEvent_TYPE_UPDATE
		case daemon.ConnectionEventType_CONNECTION_EVENT_CLOSED:
			e.Type = agentapi.ConnectionEvent_TYPE_CLOSED
			if e.Connection != nil && e.Connection.ClosedUnixMs == 0 {
				e.Connection.ClosedUnixMs = ev.ClosedAt
			}
		}
		out.Events = append(out.Events, e)
	}
	return out
}

// coreLevel maps the core's levels onto the agent's five.
func coreLevel(l daemon.LogLevel) string {
	switch l {
	case daemon.LogLevel_PANIC, daemon.LogLevel_FATAL, daemon.LogLevel_ERROR:
		return "error"
	case daemon.LogLevel_WARN:
		return "warn"
	case daemon.LogLevel_INFO:
		return "info"
	case daemon.LogLevel_DEBUG:
		return "debug"
	default:
		return "trace"
	}
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

// cleanLine removes colour codes and line breaks the core adds for terminals.
func cleanLine(s string) string {
	return strings.TrimSpace(ansi.ReplaceAllString(s, ""))
}

// mapErr translates the daemon service's gRPC-style errors into the engine's.
func mapErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, os.ErrInvalid), errors.Is(err, os.ErrClosed):
		return engine.ErrNotRunning
	}
	if st, ok := status.FromError(err); ok {
		switch st.Code() {
		case codes.NotFound:
			return fmt.Errorf("%w: %s", engine.ErrNotFound, st.Message())
		case codes.InvalidArgument, codes.Unimplemented, codes.FailedPrecondition:
			return fmt.Errorf("%w: %s", engine.ErrInvalid, st.Message())
		}
	}
	return err
}
