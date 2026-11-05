package controller

import (
	"context"
	"time"

	"eggbox/internal/agentapi"
)

// follow keeps a stream from the core alive across restarts of the core. While
// no core is running it emits the idle value (if any) and waits; whenever the
// core starts or stops it looks again. A slow consumer never stalls the source:
// values that pile up are replaced by the newest one, or combined by merge.
func follow[T any](ctx context.Context, c *Controller, out chan<- T, subscribe func(context.Context) (<-chan T, error), idle func() T, merge func(pending, next T) T) {
	var pending *T
	put := func(v T) {
		if pending != nil && merge != nil {
			v = merge(*pending, v)
		}
		pending = &v
	}

	for ctx.Err() == nil {
		_, changed := c.generation()
		pending = nil // anything queued belongs to the previous run of the core

		var src <-chan T
		cancel := func() {}
		if c.cfg.Engine.Running() {
			subCtx, cc := context.WithCancel(ctx)
			if s, err := subscribe(subCtx); err == nil {
				src, cancel = s, cc
			} else {
				cc()
			}
		}
		if src == nil && idle != nil {
			put(idle())
		}

		stale := false
		for !stale {
			var sendCh chan<- T
			var val T
			if pending != nil {
				sendCh, val = out, *pending
			}
			select {
			case <-ctx.Done():
				cancel()
				return
			case <-changed:
				stale = true
			case v, ok := <-src: // a nil channel blocks forever, which is what we want
				if !ok {
					src = nil // the stream ended; the next generation change restarts it
					continue
				}
				put(v)
			case sendCh <- val:
				pending = nil
			}
		}
		cancel()
	}
}

// WatchGroups streams outbound groups (selected node, delays).
func (c *Controller) WatchGroups(ctx context.Context) <-chan *agentapi.Groups {
	out := make(chan *agentapi.Groups, 1)
	go func() {
		defer close(out)
		follow(ctx, c, out,
			func(sub context.Context) (<-chan *agentapi.Groups, error) { return c.cfg.Engine.WatchGroups(sub) },
			func() *agentapi.Groups { return &agentapi.Groups{} }, nil)
	}()
	return out
}

// WatchTraffic streams throughput samples; while disconnected it emits an
// "unavailable" sample so the UI can zero its meters.
func (c *Controller) WatchTraffic(ctx context.Context, interval time.Duration) <-chan *agentapi.Traffic {
	interval = clampInterval(interval, 250*time.Millisecond, 10*time.Second, time.Second)
	out := make(chan *agentapi.Traffic, 1)
	go func() {
		defer close(out)
		follow(ctx, c, out,
			func(sub context.Context) (<-chan *agentapi.Traffic, error) {
				return c.cfg.Engine.WatchTraffic(sub, interval)
			},
			func() *agentapi.Traffic {
				return &agentapi.Traffic{Available: false, TimeUnixMs: time.Now().UnixMilli()}
			}, nil)
	}()
	return out
}

// WatchConnections streams connection events. After every core restart the
// first message carries reset=true so the client drops stale rows.
func (c *Controller) WatchConnections(ctx context.Context, interval time.Duration) <-chan *agentapi.ConnectionEvents {
	interval = clampInterval(interval, 250*time.Millisecond, 10*time.Second, time.Second)
	out := make(chan *agentapi.ConnectionEvents, 1)
	go func() {
		defer close(out)
		follow(ctx, c, out,
			func(sub context.Context) (<-chan *agentapi.ConnectionEvents, error) {
				return c.cfg.Engine.WatchConnections(sub, interval)
			},
			func() *agentapi.ConnectionEvents { return &agentapi.ConnectionEvents{Reset_: true} },
			mergeConnectionEvents)
	}()
	return out
}

// mergeConnectionEvents lets a slow consumer catch up without losing events:
// batches that pile up are concatenated, and a reset starts the list over.
func mergeConnectionEvents(pending, next *agentapi.ConnectionEvents) *agentapi.ConnectionEvents {
	if next.GetReset_() {
		return next
	}
	merged := &agentapi.ConnectionEvents{Reset_: pending.GetReset_()}
	merged.Events = append(append(merged.Events, pending.GetEvents()...), next.GetEvents()...)
	return merged
}

func clampInterval(v, lo, hi, def time.Duration) time.Duration {
	if v <= 0 {
		return def
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
