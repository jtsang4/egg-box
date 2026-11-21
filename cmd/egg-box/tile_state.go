package main

import (
	"sync"

	"eggbox/internal/app"
	"eggbox/internal/gui"
	"eggbox/internal/phone/droidvpn"
)

// tileStateFor turns the application's status into what Android's Quick
// Settings tile shows.
func tileStateFor(st gui.Status) droidvpn.TileState {
	switch st.Phase {
	case app.PhaseConnected:
		sub := st.Node
		if sub == "" {
			sub = st.Word
		}
		return droidvpn.TileState{State: droidvpn.TileActive, Subtitle: sub}
	case app.PhaseConnecting, app.PhaseReconnecting:
		return droidvpn.TileState{State: droidvpn.TileConnecting, Subtitle: st.Word}
	case app.PhaseDisconnecting:
		return droidvpn.TileState{State: droidvpn.TileDisconnecting, Subtitle: st.Word}
	default:
		return droidvpn.TileState{State: droidvpn.TileInactive}
	}
}

// tileSync deduplicates Quick Settings tile updates so periodic traffic ticks
// do not call across JNI when neither the phase nor the exit node changed.
type tileSync struct {
	mu   sync.Mutex
	sent bool
	last droidvpn.TileState
}

func (t *tileSync) push(next droidvpn.TileState, force bool, apply func(droidvpn.TileState) error) error {
	t.mu.Lock()
	if !force && t.sent && t.last == next {
		t.mu.Unlock()
		return nil
	}
	t.sent = true
	t.last = next
	t.mu.Unlock()
	if err := apply(next); err != nil {
		t.mu.Lock()
		if t.last == next {
			t.sent = false
		}
		t.mu.Unlock()
		return err
	}
	return nil
}
