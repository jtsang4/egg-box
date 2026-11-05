// Package loghub collects log lines from the core, the agent and the platform
// driver into one bounded in-memory buffer the GUI can tail.
//
// Lines are plain text from trusted components; the hub never stores
// configuration content. Nothing is persisted here (the agent's own log file is
// a separate slog sink).
package loghub

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"eggbox/internal/agentapi"
)

// Sources identify where a line came from.
const (
	SourceCore     = "core"
	SourceAgent    = "agent"
	SourcePlatform = "platform"
)

// Levels, lowest to highest.
var levelRank = map[string]int{"trace": 0, "debug": 1, "info": 2, "warn": 3, "error": 4, "fatal": 5, "panic": 6}

// ValidLevel reports whether level is one of the known level names.
func ValidLevel(level string) bool {
	_, ok := levelRank[strings.ToLower(level)]
	return ok
}

// Rank returns the numeric rank of a level name (unknown names rank as info).
func Rank(level string) int {
	if r, ok := levelRank[strings.ToLower(level)]; ok {
		return r
	}
	return levelRank["info"]
}

const (
	// DefaultCapacity is the number of lines kept.
	DefaultCapacity = 4000
	// maxLine bounds one stored line so a runaway component cannot exhaust memory.
	maxLine = 4096
)

// Hub is safe for concurrent use.
type Hub struct {
	mu   sync.Mutex
	buf  []*agentapi.LogEntry
	head int
	size int
	subs map[*subscriber]struct{}
}

type subscriber struct {
	ch       chan *agentapi.LogEntry
	minLevel int
	dropped  bool
}

// New creates a hub holding capacity lines.
func New(capacity int) *Hub {
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	return &Hub{buf: make([]*agentapi.LogEntry, capacity), subs: map[*subscriber]struct{}{}}
}

// Write records a line.
func (h *Hub) Write(level, source, message string) {
	message = strings.TrimRight(message, "\r\n")
	if len(message) > maxLine {
		message = message[:maxLine] + "…"
	}
	e := &agentapi.LogEntry{
		TimeUnixMs: time.Now().UnixMilli(),
		Level:      strings.ToLower(level),
		Source:     source,
		Message:    message,
	}
	h.mu.Lock()
	h.buf[(h.head+h.size)%len(h.buf)] = e
	if h.size < len(h.buf) {
		h.size++
	} else {
		h.head = (h.head + 1) % len(h.buf)
	}
	for s := range h.subs {
		if Rank(e.Level) < s.minLevel {
			continue
		}
		select {
		case s.ch <- e:
		default:
			// A slow reader loses lines instead of stalling the writer.
			s.dropped = true
		}
	}
	h.mu.Unlock()
}

// Tail returns up to n of the newest lines at or above minLevel, oldest first.
func (h *Hub) Tail(n int, minLevel string) []*agentapi.LogEntry {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.tailLocked(n, Rank(minLevel))
}

// Clear drops the buffered lines.
func (h *Hub) Clear() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := range h.buf {
		h.buf[i] = nil
	}
	h.head, h.size = 0, 0
}

// Subscribe returns the replay (the newest tail lines) and a channel of new
// lines. The channel closes when ctx ends.
func (h *Hub) Subscribe(ctx context.Context, tail int, minLevel string) ([]*agentapi.LogEntry, <-chan *agentapi.LogEntry) {
	s := &subscriber{ch: make(chan *agentapi.LogEntry, 256), minLevel: Rank(minLevel)}
	h.mu.Lock()
	replay := h.tailLocked(tail, s.minLevel)
	h.subs[s] = struct{}{}
	h.mu.Unlock()

	go func() {
		<-ctx.Done()
		h.mu.Lock()
		delete(h.subs, s)
		h.mu.Unlock()
		close(s.ch)
	}()
	return replay, s.ch
}

func (h *Hub) tailLocked(n, rank int) []*agentapi.LogEntry {
	var out []*agentapi.LogEntry
	for i := h.size - 1; i >= 0 && (n <= 0 || len(out) < n); i-- {
		e := h.buf[(h.head+i)%len(h.buf)]
		if Rank(e.Level) >= rank {
			out = append(out, e)
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// SlogHandler returns a slog handler that records into the hub under source and
// forwards to next (which may be nil).
func (h *Hub) SlogHandler(source string, next slog.Handler) slog.Handler {
	return &slogHandler{hub: h, source: source, next: next}
}

type slogHandler struct {
	hub    *Hub
	source string
	next   slog.Handler
	attrs  []slog.Attr
	group  string
}

func (s *slogHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return s.next == nil || s.next.Enabled(ctx, l) || l >= slog.LevelInfo
}

func (s *slogHandler) Handle(ctx context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Message)
	write := func(a slog.Attr) {
		fmt.Fprintf(&b, " %s=%v", a.Key, a.Value)
	}
	for _, a := range s.attrs {
		write(a)
	}
	r.Attrs(func(a slog.Attr) bool { write(a); return true })
	s.hub.Write(slogLevel(r.Level), s.source, b.String())
	if s.next != nil && s.next.Enabled(ctx, r.Level) {
		return s.next.Handle(ctx, r)
	}
	return nil
}

func (s *slogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	c := *s
	c.attrs = append(append([]slog.Attr(nil), s.attrs...), attrs...)
	if s.next != nil {
		c.next = s.next.WithAttrs(attrs)
	}
	return &c
}

func (s *slogHandler) WithGroup(name string) slog.Handler {
	c := *s
	c.group = name
	if s.next != nil {
		c.next = s.next.WithGroup(name)
	}
	return &c
}

func slogLevel(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return "error"
	case l >= slog.LevelWarn:
		return "warn"
	case l >= slog.LevelInfo:
		return "info"
	default:
		return "debug"
	}
}
