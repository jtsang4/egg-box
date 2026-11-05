// Package netwatch turns the noisy stream of network, power and session events
// into at most one reconnect decision per burst.
//
// Waking a laptop or switching Wi-Fi emits dozens of events within seconds.
// Reconnecting on each one would thrash the TUN; reconnecting too early would
// race the network coming back. The Debouncer waits for a quiet period (longer
// after a wake) and caps the total wait so a flapping link still gets handled.
package netwatch

import (
	"context"
	"sort"
	"time"
)

// Kind classifies a raw event.
type Kind int

const (
	// NetworkChanged: interface, address or route change.
	NetworkChanged Kind = iota + 1
	// Woke: the machine resumed from sleep.
	Woke
	// Slept: the machine is about to sleep (informational).
	Slept
)

func (k Kind) String() string {
	switch k {
	case NetworkChanged:
		return "network-changed"
	case Woke:
		return "woke"
	case Slept:
		return "slept"
	default:
		return "unknown"
	}
}

// Event is one raw platform notification.
type Event struct {
	Kind   Kind
	At     time.Time
	Detail string
}

// Batch is a burst collapsed into one decision input.
type Batch struct {
	Events []Event
	// Reasons lists the distinct kinds seen, in a stable order.
	Reasons []Kind
	// Woke is true when the burst contained a wake-up.
	Woke  bool
	First time.Time
	Last  time.Time
}

// Config tunes the debouncer.
type Config struct {
	// Quiet is how long no event may arrive before the burst is emitted.
	Quiet time.Duration
	// WakeQuiet replaces Quiet while a wake event is part of the burst: the
	// network usually needs longer to come back after sleep.
	WakeQuiet time.Duration
	// Max bounds the total wait measured from the first event of the burst.
	Max time.Duration
}

// DefaultConfig is what the agent uses.
func DefaultConfig() Config {
	return Config{Quiet: 1500 * time.Millisecond, WakeQuiet: 3 * time.Second, Max: 12 * time.Second}
}

// Debouncer is a pure state machine; it owns no timers and no goroutines.
// It is not safe for concurrent use.
type Debouncer struct {
	cfg    Config
	events []Event
	first  time.Time
	last   time.Time
	woke   bool
}

// NewDebouncer applies defaults for zero fields.
func NewDebouncer(cfg Config) *Debouncer {
	d := DefaultConfig()
	if cfg.Quiet > 0 {
		d.Quiet = cfg.Quiet
	}
	if cfg.WakeQuiet > 0 {
		d.WakeQuiet = cfg.WakeQuiet
	}
	if cfg.Max > 0 {
		d.Max = cfg.Max
	}
	return &Debouncer{cfg: d}
}

// Add records an event. Slept events are kept in the batch but never start one
// on their own (nothing to reconnect before the machine is awake again).
func (d *Debouncer) Add(e Event) {
	if len(d.events) == 0 {
		d.first = e.At
	}
	d.last = e.At
	if e.Kind == Woke {
		d.woke = true
	}
	d.events = append(d.events, e)
}

// Pending reports whether a burst is being collected.
func (d *Debouncer) Pending() bool { return len(d.events) > 0 }

// Deadline is when the current burst becomes due; the zero time means nothing is pending.
func (d *Debouncer) Deadline() time.Time {
	if len(d.events) == 0 {
		return time.Time{}
	}
	quiet := d.cfg.Quiet
	if d.woke {
		quiet = d.cfg.WakeQuiet
	}
	due := d.last.Add(quiet)
	if limit := d.first.Add(d.cfg.Max); due.After(limit) {
		due = limit
	}
	return due
}

// Poll returns the batch if it is due at now, resetting the debouncer.
func (d *Debouncer) Poll(now time.Time) (Batch, bool) {
	if len(d.events) == 0 || now.Before(d.Deadline()) {
		return Batch{}, false
	}
	b := Batch{Events: d.events, First: d.first, Last: d.last, Woke: d.woke}
	seen := map[Kind]bool{}
	for _, e := range d.events {
		if !seen[e.Kind] {
			seen[e.Kind] = true
			b.Reasons = append(b.Reasons, e.Kind)
		}
	}
	sort.Slice(b.Reasons, func(i, j int) bool { return b.Reasons[i] < b.Reasons[j] })
	d.events, d.woke = nil, false
	d.first, d.last = time.Time{}, time.Time{}
	return b, true
}

// Actionable reports whether the batch should cause a reconnect decision at
// all: a burst made only of "about to sleep" notices does not.
func (b Batch) Actionable() bool {
	for _, e := range b.Events {
		if e.Kind != Slept {
			return true
		}
	}
	return false
}

// Run reads raw events and sends debounced batches until ctx ends or in closes.
// now is injectable for tests; pass time.Now in production.
func Run(ctx context.Context, in <-chan Event, cfg Config, now func() time.Time) <-chan Batch {
	out := make(chan Batch, 4)
	go func() {
		defer close(out)
		d := NewDebouncer(cfg)
		timer := time.NewTimer(time.Hour)
		stopTimer(timer)
		defer timer.Stop()

		arm := func() {
			stopTimer(timer)
			if dl := d.Deadline(); !dl.IsZero() {
				wait := dl.Sub(now())
				if wait < 0 {
					wait = 0
				}
				timer.Reset(wait)
			}
		}
		for {
			select {
			case <-ctx.Done():
				return
			case e, ok := <-in:
				if !ok {
					return
				}
				if e.At.IsZero() {
					e.At = now()
				}
				d.Add(e)
				arm()
			case <-timer.C:
				if b, ok := d.Poll(now()); ok && b.Actionable() {
					select {
					case out <- b:
					case <-ctx.Done():
						return
					}
				}
				arm()
			}
		}
	}()
	return out
}

func stopTimer(t *time.Timer) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
}
