package gui

import (
	"fmt"
	"testing"
	"time"

	"eggbox/internal/app"
)

// liveTick is what changes every second while connected: throughput, totals,
// the number of connections and their rates, a log line and a node's delay.
func liveTick(s app.Snapshot, n int) *app.Snapshot {
	tr := s.Traffic
	tr.Down += float64(150_000 + 37_000*n)
	tr.Up += float64(9_000 + 1_300*n)
	tr.DownTotal += int64(3<<20 + n)
	tr.UpTotal += int64(70<<10 + n)
	tr.Connections += 1 + n%3
	tr.History = append(append([]app.Sample(nil), tr.History[1:]...), app.Sample{Down: tr.Down, Up: tr.Up})
	s.Traffic = tr

	conns := append([]app.Conn(nil), s.Conns...)
	for i := range conns {
		conns[i].Up += int64(1000 * (n + 1))
		conns[i].Down += int64(40_000 * (n + 1))
		conns[i].UpRate, conns[i].DownRate = float64(1000*(n+1)), float64(40_000*(n+1))
	}
	s.Conns = conns

	s.Logs = append(append([]app.LogLine(nil), s.Logs...), app.LogLine{
		Time: fixtureNow.Add(time.Duration(n) * time.Second), Level: "info", Source: "core",
		Message: fmt.Sprintf("inbound/tun[tun-in]: inbound connection to 203.0.113.%d:443", n+1),
	})

	groups := append([]app.Group(nil), s.Groups...)
	for g := range groups {
		items := append([]app.GroupItem(nil), groups[g].Items...)
		for i := range items {
			if items[i].DelayMS > 0 {
				items[i].DelayMS += 1 + n
			}
		}
		groups[g].Items = items
	}
	s.Groups = groups
	return &s
}

// TestLiveDataDoesNotKeepAnimating guards every screen that shows live
// figures: an update of the data paints one frame, and then the window rests
// until the next one. A figure that animates on every tick keeps a window
// that is open all day redrawing at the display's rate, which costs CPU and
// makes the figures look jittery; state changes may animate, data ticks not.
func TestLiveDataDoesNotKeepAnimating(t *testing.T) {
	type screen struct {
		name  string
		page  page
		panel bool
	}
	screens := []screen{{name: "panel", panel: true}}
	for p := page(0); p < pageCount; p++ {
		screens = append(screens, screen{name: fmt.Sprint("page ", p), page: p})
	}
	for _, sc := range screens {
		t.Run(sc.name, func(t *testing.T) {
			h := newHarness(t, harnessOpt{panel: sc.panel})
			if !sc.panel {
				h.app.goTo(sc.page)
				h.settle()
			}
			for n := 0; n < 3; n++ {
				h.be.set_(liveTick(*h.be.Snapshot(), n))
				h.ui.Frame()
				if h.ui.FramePending() {
					t.Fatalf("tick %d: the screen keeps asking for frames after a data update", n)
				}
			}
		})
	}
}
