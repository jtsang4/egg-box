package loghub

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jtsang4/sing-box-ui/internal/agentapi"
)

func TestRingKeepsNewestLines(t *testing.T) {
	h := New(5)
	for i := 0; i < 8; i++ {
		h.Write("info", SourceCore, fmt.Sprintf("line %d", i))
	}
	got := h.Tail(0, "")
	if len(got) != 5 || got[0].Message != "line 3" || got[4].Message != "line 7" {
		t.Fatalf("tail = %v", messages(got))
	}
	if got := h.Tail(2, ""); len(got) != 2 || got[0].Message != "line 6" || got[1].Message != "line 7" {
		t.Fatalf("tail(2) = %v", messages(got))
	}
}

func messages(es []*agentapi.LogEntry) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.Message)
	}
	return out
}

func TestTailFiltersByLevel(t *testing.T) {
	h := New(10)
	h.Write("debug", SourceAgent, "d")
	h.Write("info", SourceAgent, "i")
	h.Write("warn", SourceAgent, "w")
	h.Write("error", SourceAgent, "e")
	got := h.Tail(0, "warn")
	if len(got) != 2 || got[0].Message != "w" || got[1].Message != "e" {
		t.Fatalf("tail = %v", messages(got))
	}
}

func TestLongLinesAreTruncated(t *testing.T) {
	h := New(2)
	h.Write("info", SourceCore, strings.Repeat("x", 100000))
	if got := h.Tail(1, "")[0].Message; len(got) > maxLine+4 {
		t.Fatalf("line length = %d", len(got))
	}
}

func TestClear(t *testing.T) {
	h := New(3)
	h.Write("info", SourceCore, "a")
	h.Clear()
	if len(h.Tail(0, "")) != 0 {
		t.Fatal("clear should drop everything")
	}
	h.Write("info", SourceCore, "b")
	if got := h.Tail(0, ""); len(got) != 1 || got[0].Message != "b" {
		t.Fatalf("tail = %v", messages(got))
	}
}

func TestSubscribeReplaysThenStreams(t *testing.T) {
	h := New(10)
	h.Write("info", SourceCore, "old1")
	h.Write("info", SourceCore, "old2")
	ctx, cancel := context.WithCancel(context.Background())
	replay, ch := h.Subscribe(ctx, 1, "")
	if len(replay) != 1 || replay[0].Message != "old2" {
		t.Fatalf("replay = %v", messages(replay))
	}
	h.Write("info", SourceCore, "new")
	select {
	case e := <-ch:
		if e.Message != "new" {
			t.Fatalf("got %q", e.Message)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no live line")
	}
	cancel()
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("channel should close after cancel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("channel did not close")
	}
}

func TestSlowSubscriberNeverBlocksWriters(t *testing.T) {
	h := New(10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, _ = h.Subscribe(ctx, 0, "") // never read
	done := make(chan struct{})
	go func() {
		for i := 0; i < 5000; i++ {
			h.Write("info", SourceCore, "spam")
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a stalled subscriber must not block Write")
	}
}

func TestSubscribeLevelFilter(t *testing.T) {
	h := New(10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, ch := h.Subscribe(ctx, 0, "error")
	h.Write("info", SourceCore, "ignored")
	h.Write("error", SourceCore, "kept")
	select {
	case e := <-ch:
		if e.Message != "kept" {
			t.Fatalf("got %q", e.Message)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no line")
	}
}

func TestConcurrentWriters(t *testing.T) {
	h := New(100)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				h.Write("info", SourceAgent, "x")
				_ = h.Tail(10, "")
			}
		}()
	}
	wg.Wait()
	if got := len(h.Tail(0, "")); got != 100 {
		t.Fatalf("lines = %d", got)
	}
}

func TestSlogHandlerFeedsHub(t *testing.T) {
	h := New(10)
	logger := slog.New(h.SlogHandler(SourceAgent, nil)).With("component", "x")
	logger.Info("connected", "revision", 4)
	logger.Warn("careful")
	got := h.Tail(0, "")
	if len(got) != 2 {
		t.Fatalf("lines = %v", messages(got))
	}
	if got[0].Level != "info" || got[0].Source != SourceAgent || !strings.Contains(got[0].Message, "connected") ||
		!strings.Contains(got[0].Message, "revision=4") || !strings.Contains(got[0].Message, "component=x") {
		t.Fatalf("entry = %+v", got[0])
	}
	if got[1].Level != "warn" {
		t.Fatalf("level = %s", got[1].Level)
	}
}
