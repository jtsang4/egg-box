package remote

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"eggbox/internal/agent/engine"
)

// logBacklog is how many of the core's last lines a new reader gets first.
const logBacklog = 500

// Server answers an engine's calls. It owns the engine's log sink and its
// exit channel: the lines go to every reader of the log, and the one exit to
// every caller waiting for it.
type Server struct {
	eng engine.Engine

	mu     sync.Mutex
	lines  []LogLine
	tails  map[chan LogLine]bool
	exited chan struct{}
	exit   engine.ExitEvent
}

// NewServer takes over eng's log sink and exit channel.
func NewServer(eng engine.Engine) *Server {
	s := &Server{eng: eng, tails: map[chan LogLine]bool{}, exited: make(chan struct{})}
	eng.SetLogSink(s.log)
	go func() {
		ev := <-eng.Exited()
		s.mu.Lock()
		s.exit = ev
		s.mu.Unlock()
		close(s.exited)
	}()
	return s
}

func (s *Server) log(level, message string) {
	l := LogLine{Level: level, Message: message}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lines = append(s.lines, l)
	if len(s.lines) > logBacklog {
		s.lines = s.lines[len(s.lines)-logBacklog:]
	}
	for t := range s.tails {
		select {
		case t <- l:
		default: // a reader that cannot keep up loses lines, not the core
		}
	}
}

// Done is closed when the core stopped by itself; Exit says why.
func (s *Server) Done() <-chan struct{} { return s.exited }

// Exit is why the core stopped, once Done is closed.
func (s *Server) Exit() engine.ExitEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exit
}

// Serve answers on lis until it is closed.
func (s *Server) Serve(lis net.Listener) error {
	for {
		conn, err := lis.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go s.handle(conn)
	}
}

// handle answers one call. The call's context ends when the caller hangs up,
// which is how a stream is stopped.
func (s *Server) handle(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	var req request
	if err := readJSON(conn, &req); err != nil {
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		// Nothing more is read: a read returns once the caller hangs up.
		_, _ = io.Copy(io.Discard, conn)
		cancel()
	}()

	reply := func(a answer, err error) error {
		if err != nil {
			a.Err, a.Kind = err.Error(), errorKind(err)
		}
		return writeJSON(conn, a)
	}
	switch req.Op {
	case opStatus:
		_ = reply(answer{Running: s.eng.Running(), Tun: s.eng.TunInterface(), Version: s.eng.Version()}, nil)
	case opWatchGroups:
		ch, err := s.eng.WatchGroups(ctx)
		stream(conn, ch, err, reply)
	case opWatchTraffic:
		ch, err := s.eng.WatchTraffic(ctx, time.Duration(req.IntervalMS)*time.Millisecond)
		stream(conn, ch, err, reply)
	case opWatchConnections:
		ch, err := s.eng.WatchConnections(ctx, time.Duration(req.IntervalMS)*time.Millisecond)
		stream(conn, ch, err, reply)
	case opSelectOutbound:
		_ = reply(answer{}, s.eng.SelectOutbound(req.Group, req.Outbound))
	case opURLTest:
		_ = reply(answer{}, s.eng.URLTest(ctx, req.Tag))
	case opMode:
		cur, all, err := s.eng.Mode()
		_ = reply(answer{Mode: cur, Modes: all}, err)
	case opSetMode:
		_ = reply(answer{}, s.eng.SetMode(req.Mode))
	case opCloseConnection:
		_ = reply(answer{}, s.eng.CloseConnection(req.ID))
	case opCloseAll:
		_ = reply(answer{}, s.eng.CloseAllConnections())
	case opLogs:
		s.tail(ctx, conn, reply)
	case opExit:
		select {
		case <-s.exited:
			s.mu.Lock()
			ev := s.exit
			s.mu.Unlock()
			a := answer{Revoked: ev.Revoked}
			if ev.Err != nil {
				a.Cause = ev.Err.Error()
			}
			_ = reply(a, nil)
		case <-ctx.Done():
		}
	default:
		_ = reply(answer{}, errors.New("remote: unknown operation "+req.Op))
	}
}

// stream answers, then sends what ch delivers until it closes or the caller
// hangs up.
func stream[T proto.Message](conn net.Conn, ch <-chan T, err error, reply func(answer, error) error) {
	if reply(answer{}, err) != nil || err != nil {
		return
	}
	for m := range ch {
		b, err := proto.Marshal(m)
		if err != nil || writeFrame(conn, b) != nil {
			return
		}
	}
}

// tail sends the log's backlog, then each new line.
func (s *Server) tail(ctx context.Context, conn net.Conn, reply func(answer, error) error) {
	t := make(chan LogLine, 256)
	s.mu.Lock()
	backlog := append([]LogLine(nil), s.lines...)
	s.tails[t] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.tails, t)
		s.mu.Unlock()
	}()
	if reply(answer{}, nil) != nil {
		return
	}
	for _, l := range backlog {
		if writeJSON(conn, l) != nil {
			return
		}
	}
	for {
		select {
		case l := <-t:
			if writeJSON(conn, l) != nil {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}
