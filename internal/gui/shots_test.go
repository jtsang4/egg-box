package gui

import (
	"context"
	"testing"

	"github.com/jtsang4/sing-box-ui/internal/agentapi"
	"github.com/jtsang4/sing-box-ui/internal/app"
)

// shot is a screen to photograph, reached the way a person would reach it:
// by clicking.
type shot struct {
	name     string
	w, h     int
	platform string
	setup    func(*fixtureBackend, *Options)
	run      func(h *harness)
}

func introduction(be *fixtureBackend, o *Options) {
	be.set.OnboardingDone = false
	be.snap = be.disconnectedSnapshot()
	be.snap.Phase = app.PhaseNoAgent
	be.snap.Status = nil
	be.snap.AgentSettings = nil
	o.Authorize = func(context.Context) error { return nil }
}

// failedConnection is the state after an attempt that did not work.
func failedConnection(be *fixtureBackend, _ *Options) {
	be.snap = be.disconnectedSnapshot()
	be.snap.Phase = app.PhaseFailed
	be.snap.Status = &agentapi.Status{
		Service: agentapi.ServiceState_SERVICE_STATE_READY, Connection: agentapi.ConnectionState_CONNECTION_STATE_FAILED,
		HasConfig: true, Mode: "Rule", LastError: "the core could not start: the outbound handshake timed out",
	}
}

var shots = []shot{
	{name: "empty-dashboard", setup: emptyLibrary},
	{name: "empty-nodes", setup: emptyLibrary, run: func(h *harness) { h.click("Nodes") }},
	{name: "empty-subscriptions", setup: emptyLibrary, run: func(h *harness) { h.click("Subscriptions") }},
	{name: "failed-dashboard", setup: failedConnection},
	{name: "dashboard"},
	{name: "dashboard-off", setup: func(be *fixtureBackend, _ *Options) { be.snap = be.disconnectedSnapshot() }},
	{name: "nodes", run: func(h *harness) { h.click("Nodes") }},
	{name: "subscriptions", run: func(h *harness) { h.click("Subscriptions") }},
	{name: "subscription-add", run: func(h *harness) { h.click("Subscriptions"); h.click("Add subscription") }},
	{name: "import", run: func(h *harness) { h.click("Subscriptions"); h.click("Import nodes") }},
	{name: "rules", run: func(h *harness) { h.click("Rules") }},
	{name: "connections", run: func(h *harness) { h.click("Connections") }},
	{name: "logs", run: func(h *harness) { h.click("Logs") }},
	{name: "settings", h: 2700, run: func(h *harness) { h.click("Settings") }},
	{name: "settings-macos", h: 2700, platform: "darwin", run: func(h *harness) { h.click("Settings") }, setup: func(be *fixtureBackend, o *Options) {
		o.ServiceStartup = nil // macOS keeps that switch in System Settings
		o.OpenServiceSettings = func() {}
		o.RemoveService = func(context.Context) error { return nil }
	}},
	{name: "welcome-1", setup: introduction},
	{name: "welcome-2", setup: introduction, run: func(h *harness) { h.click("Continue") }},
	{name: "welcome-3", setup: introduction, run: func(h *harness) { h.click("Continue"); h.click("Not now") }},
}

// TestShots renders the screens in both themes; with SBUI_SHOTS set the
// images are written for review.
func TestShots(t *testing.T) {
	if testing.Short() {
		t.Skip("renders every screen in both themes, which is too slow under the race detector")
	}
	for _, s := range shots {
		for _, dark := range []bool{true, false} {
			name := s.name + "-light"
			if dark {
				name = s.name + "-dark"
			}
			t.Run(name, func(t *testing.T) {
				w, hh := s.w, s.h
				if w == 0 {
					w = 1180
				}
				if hh == 0 {
					hh = 860
				}
				h := newHarness(t, harnessOpt{dark: dark, w: w, h: hh, platform: s.platform, setup: s.setup})
				if s.run != nil {
					s.run(h)
				}
				h.settle()
				h.shot(name)
			})
		}
	}
}
