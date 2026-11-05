//go:build darwin

package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"eggbox/internal/app"
	"eggbox/internal/darwin/bridge"
)

// fake is a system that moves through the states it is told to.
type fake struct {
	mu       sync.Mutex
	statuses []bridge.ServiceStatus // what ServiceStatus answers, one per look; the last one repeats
	looks    int
	regErr   error
	regs     int
	opened   int
}

func (f *fake) ServiceStatus(string) bridge.ServiceStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.looks
	if i >= len(f.statuses) {
		i = len(f.statuses) - 1
	}
	f.looks++
	return f.statuses[i]
}
func (f *fake) Register(string) error { f.mu.Lock(); defer f.mu.Unlock(); f.regs++; return f.regErr }
func (f *fake) OpenLoginItems()       { f.mu.Lock(); defer f.mu.Unlock(); f.opened++ }

var quick = Timing{Poll: time.Millisecond, Patience: 80 * time.Millisecond}

func TestAlreadyAllowedChangesNothing(t *testing.T) {
	f := &fake{statuses: []bridge.ServiceStatus{bridge.ServiceEnabled}}
	if err := Authorize(context.Background(), f, "x.plist", quick); err != nil {
		t.Fatal(err)
	}
	if f.regs != 0 || f.opened != 0 {
		t.Errorf("registered %d times, opened settings %d times", f.regs, f.opened)
	}
}

func TestAServiceTheApplicationDoesNotContain(t *testing.T) {
	f := &fake{statuses: []bridge.ServiceStatus{bridge.ServiceNotFound}}
	err := Authorize(context.Background(), f, "x.plist", quick)
	if !errors.Is(err, app.ErrServiceSetup) {
		t.Fatalf("got %v", err)
	}
	if f.regs != 0 {
		t.Error("registered a service that is not there")
	}
}

func TestRegistersAndTheSystemAllowsAtOnce(t *testing.T) {
	f := &fake{statuses: []bridge.ServiceStatus{bridge.ServiceNotRegistered, bridge.ServiceEnabled}}
	if err := Authorize(context.Background(), f, "x.plist", quick); err != nil {
		t.Fatal(err)
	}
	if f.regs != 1 || f.opened != 0 {
		t.Errorf("regs %d opened %d", f.regs, f.opened)
	}
}

func TestOpensSystemSettingsOnceAndWaitsForThePerson(t *testing.T) {
	f := &fake{statuses: []bridge.ServiceStatus{
		bridge.ServiceNotRegistered,
		bridge.ServiceRequiresApproval, bridge.ServiceRequiresApproval, bridge.ServiceRequiresApproval,
		bridge.ServiceEnabled,
	}}
	if err := Authorize(context.Background(), f, "x.plist", quick); err != nil {
		t.Fatal(err)
	}
	if f.opened != 1 {
		t.Errorf("System Settings was opened %d times", f.opened)
	}
}

func TestThePersonNeverSwitchesItOn(t *testing.T) {
	f := &fake{statuses: []bridge.ServiceStatus{bridge.ServiceNotRegistered, bridge.ServiceRequiresApproval}}
	err := Authorize(context.Background(), f, "x.plist", quick)
	if !errors.Is(err, app.ErrAuthorizationDeclined) {
		t.Fatalf("got %v", err)
	}
	if f.opened != 1 {
		t.Errorf("opened %d times", f.opened)
	}
}

func TestARefusedRegistration(t *testing.T) {
	f := &fake{statuses: []bridge.ServiceStatus{bridge.ServiceNotRegistered}, regErr: &bridge.RegisterError{Code: 2, Message: "nope"}}
	err := Authorize(context.Background(), f, "x.plist", quick)
	if !errors.Is(err, app.ErrServiceSetup) {
		t.Fatalf("got %v", err)
	}
	var re *bridge.RegisterError
	if !errors.As(err, &re) || re.Code != 2 {
		t.Errorf("the system's own refusal was lost: %v", err)
	}
}

func TestTheServiceVanishesWhileWaiting(t *testing.T) {
	f := &fake{statuses: []bridge.ServiceStatus{bridge.ServiceNotRegistered, bridge.ServiceRequiresApproval, bridge.ServiceNotFound}}
	if err := Authorize(context.Background(), f, "x.plist", quick); !errors.Is(err, app.ErrServiceSetup) {
		t.Fatalf("got %v", err)
	}
}

func TestCancellingStopsTheWait(t *testing.T) {
	f := &fake{statuses: []bridge.ServiceStatus{bridge.ServiceNotRegistered, bridge.ServiceRequiresApproval}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Authorize(ctx, f, "x.plist", Timing{Poll: 5 * time.Millisecond, Patience: time.Minute})
	}()
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the wait did not stop")
	}
}

func TestADeadlineIsTheSameAsGivingUp(t *testing.T) {
	f := &fake{statuses: []bridge.ServiceStatus{bridge.ServiceNotRegistered, bridge.ServiceRequiresApproval}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := Authorize(ctx, f, "x.plist", Timing{Poll: 5 * time.Millisecond, Patience: time.Minute})
	if !errors.Is(err, app.ErrAuthorizationDeclined) {
		t.Errorf("got %v", err)
	}
}
