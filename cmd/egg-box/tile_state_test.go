package main

import (
	"errors"
	"testing"

	"eggbox/internal/app"
	"eggbox/internal/gui"
	"eggbox/internal/phone/droidvpn"
)

func TestTileStateForPhases(t *testing.T) {
	cases := []struct {
		name string
		st   gui.Status
		want droidvpn.TileState
	}{
		{
			name: "connected with node",
			st:   gui.Status{Phase: app.PhaseConnected, On: true, Word: "Connected", Node: "Tokyo 01"},
			want: droidvpn.TileState{State: droidvpn.TileActive, Subtitle: "Tokyo 01"},
		},
		{
			name: "connected without node falls back to status word",
			st:   gui.Status{Phase: app.PhaseConnected, On: true, Word: "已连接"},
			want: droidvpn.TileState{State: droidvpn.TileActive, Subtitle: "已连接"},
		},
		{
			name: "connecting",
			st:   gui.Status{Phase: app.PhaseConnecting, Working: true, Word: "Connecting"},
			want: droidvpn.TileState{State: droidvpn.TileConnecting, Subtitle: "Connecting"},
		},
		{
			name: "reconnecting",
			st:   gui.Status{Phase: app.PhaseReconnecting, Working: true, Word: "Reconnecting"},
			want: droidvpn.TileState{State: droidvpn.TileConnecting, Subtitle: "Reconnecting"},
		},
		{
			name: "disconnecting",
			st:   gui.Status{Phase: app.PhaseDisconnecting, Working: true, Word: "Disconnecting"},
			want: droidvpn.TileState{State: droidvpn.TileDisconnecting, Subtitle: "Disconnecting"},
		},
		{
			name: "disconnected",
			st:   gui.Status{Phase: app.PhaseDisconnected, Word: "Disconnected"},
			want: droidvpn.TileState{State: droidvpn.TileInactive},
		},
		{
			name: "failed",
			st:   gui.Status{Phase: app.PhaseFailed, Word: "Failed"},
			want: droidvpn.TileState{State: droidvpn.TileInactive},
		},
	}
	for _, tc := range cases {
		if got := tileStateFor(tc.st); got != tc.want {
			t.Errorf("%s: got %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

func TestTileSyncDeduplicatesAndSupportsForce(t *testing.T) {
	var syncer tileSync
	var calls []droidvpn.TileState
	apply := func(s droidvpn.TileState) error {
		calls = append(calls, s)
		return nil
	}

	s1 := droidvpn.TileState{State: droidvpn.TileActive, Subtitle: "Tokyo 01"}
	s2 := droidvpn.TileState{State: droidvpn.TileActive, Subtitle: "Hong Kong 01"}

	if err := syncer.push(s1, false, apply); err != nil {
		t.Fatal(err)
	}
	// Duplicate traffic tick does not trigger another push.
	if err := syncer.push(s1, false, apply); err != nil {
		t.Fatal(err)
	}
	// Force re-pushes the same state (e.g. resetting Kotlin's optimistic state).
	if err := syncer.push(s1, true, apply); err != nil {
		t.Fatal(err)
	}
	// Changing exit node pushes again.
	if err := syncer.push(s2, false, apply); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 || calls[0] != s1 || calls[1] != s1 || calls[2] != s2 {
		t.Fatalf("calls = %+v", calls)
	}

	// A failed push does not poison the cache, so the next identical update retries.
	s3 := droidvpn.TileState{State: droidvpn.TileInactive}
	if err := syncer.push(s3, false, func(droidvpn.TileState) error { return errors.New("boom") }); err == nil {
		t.Fatal("expected error")
	}
	if err := syncer.push(s3, false, apply); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 4 || calls[3] != s3 {
		t.Fatalf("retry after failure did not run: %+v", calls)
	}
}
