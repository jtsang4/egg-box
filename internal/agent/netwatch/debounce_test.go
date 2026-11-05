package netwatch

import (
	"context"
	"testing"
	"time"
)

var t0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func at(d time.Duration) time.Time { return t0.Add(d) }

func newTest() *Debouncer {
	return NewDebouncer(Config{Quiet: time.Second, WakeQuiet: 3 * time.Second, Max: 10 * time.Second})
}

func TestNothingPendingByDefault(t *testing.T) {
	d := newTest()
	if d.Pending() || !d.Deadline().IsZero() {
		t.Fatal("fresh debouncer must be idle")
	}
	if _, ok := d.Poll(at(time.Hour)); ok {
		t.Fatal("nothing to emit")
	}
}

func TestBurstCollapsesIntoOneBatch(t *testing.T) {
	d := newTest()
	for i := 0; i < 20; i++ {
		d.Add(Event{Kind: NetworkChanged, At: at(time.Duration(i) * 100 * time.Millisecond)})
	}
	// Last event at 1.9s, quiet 1s => due at 2.9s.
	if _, ok := d.Poll(at(2800 * time.Millisecond)); ok {
		t.Fatal("must wait for the quiet period after the last event")
	}
	b, ok := d.Poll(at(2900 * time.Millisecond))
	if !ok {
		t.Fatal("batch should be due")
	}
	if len(b.Events) != 20 || len(b.Reasons) != 1 || b.Reasons[0] != NetworkChanged || b.Woke {
		t.Fatalf("batch = %+v", b)
	}
	if d.Pending() {
		t.Fatal("debouncer must reset after emitting")
	}
}

func TestEachNewEventExtendsTheQuietWindow(t *testing.T) {
	d := newTest()
	d.Add(Event{Kind: NetworkChanged, At: at(0)})
	if got := d.Deadline(); !got.Equal(at(time.Second)) {
		t.Fatalf("deadline = %v", got)
	}
	d.Add(Event{Kind: NetworkChanged, At: at(800 * time.Millisecond)})
	if got := d.Deadline(); !got.Equal(at(1800 * time.Millisecond)) {
		t.Fatalf("deadline = %v", got)
	}
}

func TestWakeUsesLongerQuietPeriod(t *testing.T) {
	d := newTest()
	d.Add(Event{Kind: Woke, At: at(0)})
	if _, ok := d.Poll(at(2 * time.Second)); ok {
		t.Fatal("after a wake the network gets more time to come back")
	}
	b, ok := d.Poll(at(3 * time.Second))
	if !ok || !b.Woke {
		t.Fatalf("batch = %+v ok=%v", b, ok)
	}
}

func TestMaxWaitBoundsAFlappingLink(t *testing.T) {
	d := newTest()
	for i := 0; i < 100; i++ {
		d.Add(Event{Kind: NetworkChanged, At: at(time.Duration(i) * 500 * time.Millisecond)})
	}
	// Events keep arriving until 49.5s, but the burst may not wait beyond first+10s.
	if got := d.Deadline(); !got.Equal(at(10 * time.Second)) {
		t.Fatalf("deadline = %v, want first+max", got)
	}
	if _, ok := d.Poll(at(10 * time.Second)); !ok {
		t.Fatal("burst must be released at the max wait")
	}
}

func TestSleepAloneIsNotActionable(t *testing.T) {
	d := newTest()
	d.Add(Event{Kind: Slept, At: at(0)})
	b, ok := d.Poll(at(time.Hour))
	if !ok {
		t.Fatal("the burst is still emitted by the state machine")
	}
	if b.Actionable() {
		t.Fatal("a lone sleep notice must not trigger a reconnect")
	}

	d.Add(Event{Kind: Slept, At: at(0)})
	d.Add(Event{Kind: Woke, At: at(5 * time.Second)})
	b, _ = d.Poll(at(time.Hour))
	if !b.Actionable() || !b.Woke {
		t.Fatalf("sleep followed by wake must be actionable: %+v", b)
	}
	if len(b.Reasons) != 2 {
		t.Fatalf("reasons = %v", b.Reasons)
	}
}

func TestRunEmitsOneBatchForABurst(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	in := make(chan Event)
	out := Run(ctx, in, Config{Quiet: 40 * time.Millisecond, WakeQuiet: 80 * time.Millisecond, Max: time.Second}, time.Now)

	go func() {
		for i := 0; i < 10; i++ {
			in <- Event{Kind: NetworkChanged}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	select {
	case b := <-out:
		if len(b.Events) != 10 {
			t.Fatalf("batch has %d events, want 10", len(b.Events))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no batch emitted")
	}

	select {
	case b := <-out:
		t.Fatalf("unexpected second batch: %+v", b)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestRunStopsWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	out := Run(ctx, make(chan Event), Config{}, time.Now)
	cancel()
	select {
	case _, ok := <-out:
		if ok {
			t.Fatal("no batch expected")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop")
	}
}
