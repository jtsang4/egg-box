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
	switch c.ServiceStatus(plist) {
	case bridge.ServiceEnabled:
		return nil
	case bridge.ServiceNotFound:
		return fmt.Errorf("%w: this copy of the application does not contain the service; install the application with its installer", app.ErrServiceSetup)
	}
	if err := c.Register(plist); err != nil {
		return fmt.Errorf("%w: %w", app.ErrServiceSetup, err)
	}

	opened := false
	deadline := time.Now().Add(t.Patience)
	for {
		switch c.ServiceStatus(plist) {
		case bridge.ServiceEnabled:
			return nil
		case bridge.ServiceNotFound:
			return fmt.Errorf("%w: the system lost track of the service", app.ErrServiceSetup)
		case bridge.ServiceRequiresApproval:
			if !opened {
				opened = true
				c.OpenLoginItems()
			}
		}
		if time.Now().After(deadline) {
			return app.ErrAuthorizationDeclined
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return app.ErrAuthorizationDeclined
			}
			return ctx.Err()
		case <-time.After(t.Poll):
		}
	}
}
