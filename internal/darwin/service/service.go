//go:build darwin

// Package service is how the application gets the privileged service allowed
// on macOS. The system does not let a program install a launch daemon by
// itself: the application registers it, and the person switches it on in
// System Settings. This package walks through that, and says plainly when the
// person did not.
package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"eggbox/internal/app"
	"eggbox/internal/darwin/bridge"
)

// Control is the part of the bridge that registers the service.
type Control interface {
	ServiceStatus(plist string) bridge.ServiceStatus
	Register(plist string) error
	OpenLoginItems()
}

// Timing says how often to look and how long to wait for the person.
type Timing struct {
	// Poll is the wait between two looks at the service's state.
	Poll time.Duration
	// Patience is how long the person has to switch the service on.
	Patience time.Duration
}

// DefaultTiming gives the person three minutes, which is long enough to find
// System Settings and short enough not to leave the application waiting
// forever on somebody who walked away.
var DefaultTiming = Timing{Poll: time.Second, Patience: 3 * time.Minute}

// Authorize makes sure the service is registered and switched on. Registered
// and allowed already, it returns at once. Otherwise it registers the service,
// opens the page of System Settings where it is switched on, and waits for the
// person to do it.
func Authorize(ctx context.Context, c Control, plist string, t Timing) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	state := c.ServiceStatus(plist)
	if state == bridge.ServiceEnabled {
		return nil
	}
	if state != bridge.ServiceRequiresApproval {
		// NotFound can mean there is no registration yet. Only register can
		// distinguish that from a missing or invalid bundled service.
		err := c.Register(plist)
		state = c.ServiceStatus(plist)
		// macOS can return an error even though registration succeeded and
		// is waiting for approval. Follow its status to System Settings.
		if err != nil && state != bridge.ServiceRequiresApproval && state != bridge.ServiceEnabled {
			return fmt.Errorf("%w: %w", app.ErrServiceSetup, err)
		}
	}

	opened := false
	deadline := time.Now().Add(t.Patience)
	for {
		switch state {
		case bridge.ServiceEnabled:
			return nil
		case bridge.ServiceNotFound:
			return fmt.Errorf("%w: macOS cannot find the registered background service; reinstall the application", app.ErrServiceSetup)
		case bridge.ServiceRequiresApproval:
			if !opened {
				opened = true
				c.OpenLoginItems()
			}
		}
		if time.Now().After(deadline) {
			if state == bridge.ServiceRequiresApproval {
				return app.ErrAuthorizationDeclined
			}
			return fmt.Errorf("%w: macOS did not enable the background service (status %s)", app.ErrServiceSetup, state)
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return app.ErrAuthorizationDeclined
			}
			return ctx.Err()
		case <-time.After(t.Poll):
		}
		state = c.ServiceStatus(plist)
	}
}
