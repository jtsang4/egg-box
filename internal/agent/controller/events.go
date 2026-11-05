package controller

import (
	"context"
	"fmt"
	"time"

	"eggbox/internal/agent/netwatch"
	"eggbox/internal/agent/platform"
	"eggbox/internal/agentapi"
)

// watchEngineExit reacts to a core that died without being asked to.
func (c *Controller) watchEngineExit() {
	exited := c.cfg.Engine.Exited()
	for {
		select {
		case <-c.root.Done():
			return
		case ev := <-exited:
			c.submitInternal("engine-exit", true, c.coreExitedJob(ev.Err))
		}
	}
}

func (c *Controller) coreExitedJob(cause error) func(context.Context) *agentapi.OperationResult {
	return func(ctx context.Context) *agentapi.OperationResult {
		st := c.state()
		if st.conn != agentapi.ConnectionState_CONNECTION_STATE_CONNECTED {
			// Not ours to handle: a stop or a reconnect was already under way.
			return success("ignored")
		}
		msg := "the core stopped unexpectedly"
		if cause != nil {
			msg = fmt.Sprintf("the core stopped unexpectedly: %v", cause)
		}
		c.log.Error("core exited", "err", cause)
		c.logAgent("error", "%s", msg)
		c.update(func(s *state) { s.lastErrCode, s.lastErr = agentapi.ErrorCode_ERROR_CODE_CORE_START_FAILED, msg })
		if !st.intentConnected {
			c.teardown("core exited")
			c.update(func(s *state) {
				s.conn = agentapi.ConnectionState_CONNECTION_STATE_DISCONNECTED
				s.activeRev = 0
			})
			return success("cleaned up after the core exited")
		}
		return c.reconnectJob("core exited", 1)(ctx)
	}
}

// watchPlatform consumes OS notifications: network and power events go through
// the debouncer, session events act immediately.
func (c *Controller) watchPlatform() {
	events := c.cfg.Driver.Events(c.root)
	in := make(chan netwatch.Event, 32)
	batches := netwatch.Run(c.root, in, c.cfg.Netwatch, time.Now)

	forward := func(kind netwatch.Kind, ev platform.Event) {
		select {
		case in <- netwatch.Event{Kind: kind, At: ev.At, Detail: ev.Detail}:
		case <-c.root.Done():
		}
	}
	for {
		select {
		case <-c.root.Done():
			return
		case ev, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			switch ev.Kind {
			case platform.NetworkChanged:
				c.noteSettling()
				forward(netwatch.NetworkChanged, ev)
			case platform.Woke:
				c.noteSettling()
				forward(netwatch.Woke, ev)
			case platform.Slept:
				forward(netwatch.Slept, ev)
			case platform.UserLoggedIn:
				c.onLogin()
			case platform.UserLoggedOut:
				c.onLogout()
			}
		case b := <-batches:
			c.onNetworkBatch(b)
		}
	}
}

// noteSettling shows that events are being coalesced (only meaningful while a
// connection is wanted).
func (c *Controller) noteSettling() {
	if !c.intentConnected() {
		return
	}
	if c.cfg.Settings.Load().ReconnectOnNetworkChange {
		c.update(func(s *state) { s.network = agentapi.NetworkState_NETWORK_STATE_SETTLING })
	}
}

func (c *Controller) onNetworkBatch(b netwatch.Batch) {
	settings := c.cfg.Settings.Load()
	st := c.state()
	if !settings.ReconnectOnNetworkChange || !st.intentConnected {
		c.update(func(s *state) { s.network = c.networkNow() })
		return
	}

	if !c.cfg.Driver.Online(c.root) {
		// Nothing to reconnect to yet; the next batch (link restored) acts.
		c.update(func(s *state) { s.network = agentapi.NetworkState_NETWORK_STATE_OFFLINE })
		c.logAgent("warn", "the network went offline")
		return
	}
	c.update(func(s *state) { s.network = agentapi.NetworkState_NETWORK_STATE_ONLINE })

	fp, _ := c.cfg.Driver.Fingerprint(c.root)
	switch st.conn {
	case agentapi.ConnectionState_CONNECTION_STATE_CONNECTED:
		if !b.Woke && fp == st.fingerprint {
			return // the burst did not change the uplink
		}
		reason := "network changed"
		if b.Woke {
			reason = "woke from sleep"
		}
		c.submitInternal("reconnect", true, c.reconnectJob(reason, 1))
	case agentapi.ConnectionState_CONNECTION_STATE_FAILED:
		// The connection is wanted but down: the network changing is a good moment to retry.
		c.submitInternal("reconnect", true, c.reconnectJob("network changed", 1))
	}
}

func (c *Controller) networkNow() agentapi.NetworkState {
	if c.cfg.Driver.Online(c.root) {
		return agentapi.NetworkState_NETWORK_STATE_ONLINE
	}
	return agentapi.NetworkState_NETWORK_STATE_OFFLINE
}

func (c *Controller) onLogin() {
	if c.intentConnected() || isUp(c.state().conn) {
		return
	}
	if !c.cfg.Settings.Load().AutoConnect || c.cfg.Store.CurrentN() == 0 {
		return
	}
	c.logAgent("info", "the authorized user logged in; connecting automatically")
	c.submitInternal("connect", true, c.connectJob(&agentapi.ConnectRequest{}, "auto-connect"))
}

// onLogout ends the connection when the authorized user's session ends or
// another user takes over (design: logout and user switch disconnect).
func (c *Controller) onLogout() {
	st := c.state()
	if !st.intentConnected && !isUp(st.conn) && st.conn != agentapi.ConnectionState_CONNECTION_STATE_FAILED {
		return
	}
	c.cancelRetry()
	c.interrupt()
	c.logAgent("info", "the user session ended; disconnecting")
	c.submitInternal("disconnect", false, c.disconnectJob("session ended"))
}
