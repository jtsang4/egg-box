package gui

import (
	"bytes"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"eggbox/internal/app"
)

func TestMotionPreferences(t *testing.T) {
	for _, state := range []struct {
		name  string
		setup func(*fixtureBackend, *Options)
	}{
		{"connected", nil},
		{"connecting", func(be *fixtureBackend, _ *Options) { be.snap.Phase = app.PhaseConnecting }},
		{"welcome", introduction},
	} {
		for _, motion := range []bool{false, true} {
			name := state.name + "/reduced"
			if motion {
				name = state.name + "/animated"
			}
			t.Run(name, func(t *testing.T) {
				h := newHarness(t, harnessOpt{motion: motion, scale: 0.5, setup: state.setup})
				before := bytes.Clone(h.ui.Image().Pix)
				h.ui.Advance(200 * time.Millisecond)
				changed := !bytes.Equal(before, h.ui.Image().Pix)
				if changed != motion {
					t.Fatalf("image changed=%v with motion=%v", changed, motion)
				}
			})
		}
	}
}

func TestAnimatedInteractions(t *testing.T) {
	h := newHarness(t, harnessOpt{motion: true, scale: 0.5})
	h.click("Connections")
	if !h.be.took("watch true") {
		t.Fatal("the animated page did not subscribe to connections")
	}
	h.click("Logs")
	if !h.be.took("watch false") {
		t.Fatal("leaving the animated page did not stop its subscription")
	}
	h.click("Dashboard")
	h.click("Disconnect")
	h.settle()
	h.mustHave("NOT PROTECTED", "Connect")
	h.click("Connect")
	h.settle()
	h.mustHave("PROTECTED", "Disconnect")
}

func TestMotionPreferenceChangesWhileOpen(t *testing.T) {
	h := newHarness(t, harnessOpt{motion: true, scale: 0.5})
	for _, reduce := range []bool{true, false} {
		h.ui.SetPreferences(ui.Preferences{ReduceMotion: reduce, TextScale: 1})
		h.settle()
		before := bytes.Clone(h.ui.Image().Pix)
		h.ui.Advance(200 * time.Millisecond)
		changed := !bytes.Equal(before, h.ui.Image().Pix)
		if changed == reduce {
			t.Fatalf("image changed=%v after setting reduced motion=%v", changed, reduce)
		}
	}
}
