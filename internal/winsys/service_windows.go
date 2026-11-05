//go:build windows

package winsys

import (
	"errors"
	"fmt"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/jtsang4/sing-box-ui/internal/brand"
)

// ServiceArgs are the arguments the system starts the service executable with.
const ServiceArgs = "service"

// Install registers the service: automatic start, LocalSystem, restarted by
// the system when it fails. It replaces a registration of an older version.
// It needs administrator rights, and it refuses a program an ordinary user
// could replace, because the service would run it as SYSTEM.
func Install(exe string) error {
	if err := RequireTrustedExecutable(exe); err != nil {
		return err
	}
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to the service manager: %w", err)
	}
	defer m.Disconnect()

	cfg := mgr.Config{
		ServiceType:      windows.SERVICE_WIN32_OWN_PROCESS,
		StartType:        mgr.StartAutomatic,
		ErrorControl:     mgr.ErrorNormal,
		DisplayName:      brand.WindowsServiceDisplayName,
		Description:      brand.WindowsServiceDescription,
		ServiceStartName: "", // LocalSystem
	}
	s, err := m.OpenService(brand.WindowsServiceName)
	if err == nil {
		defer s.Close()
		old, qerr := s.Config()
		if qerr != nil {
			return qerr
		}
		old.BinaryPathName = quoteCommand(exe, ServiceArgs)
		old.StartType, old.DisplayName, old.Description = cfg.StartType, cfg.DisplayName, cfg.Description
		if err := s.UpdateConfig(old); err != nil {
			return fmt.Errorf("update the service: %w", err)
		}
	} else {
		s, err = m.CreateService(brand.WindowsServiceName, exe, cfg, ServiceArgs)
		if err != nil {
			return fmt.Errorf("create the service: %w", err)
		}
		defer s.Close()
	}
	restart := mgr.RecoveryAction{Type: mgr.ServiceRestart, Delay: 5 * time.Second}
	if err := s.SetRecoveryActions([]mgr.RecoveryAction{restart, restart, restart}, 24*3600); err != nil {
		return fmt.Errorf("set the recovery actions: %w", err)
	}
	return s.SetRecoveryActionsOnNonCrashFailures(true)
}

// Uninstall stops the service and removes its registration. It needs
// administrator rights. A service that is not there is not an error.
func Uninstall() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to the service manager: %w", err)
	}
	defer m.Disconnect()
	s, err := m.OpenService(brand.WindowsServiceName)
	if err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return nil
		}
		return err
	}
	defer s.Close()
	if err := stop(s, 60*time.Second); err != nil {
		return err
	}
	return s.Delete()
}

// Start starts the service and waits until it runs. It needs administrator
// rights, which the user gives once when allowing the service.
func Start() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to the service manager: %w", err)
	}
	defer m.Disconnect()
	s, err := m.OpenService(brand.WindowsServiceName)
	if err != nil {
		return fmt.Errorf("open the service: %w", err)
	}
	defer s.Close()
	if st, err := s.Query(); err == nil && st.State == svc.Running {
		return nil
	}
	if err := s.Start(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
		return fmt.Errorf("start the service: %w", err)
	}
	return waitFor(s, svc.Running, 30*time.Second)
}

// Stop stops the service and waits until it has stopped. The service
// disconnects and cleans up first, which can take a while.
func Stop() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to the service manager: %w", err)
	}
	defer m.Disconnect()
	s, err := m.OpenService(brand.WindowsServiceName)
	if err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return nil
		}
		return err
	}
	defer s.Close()
	return stop(s, 60*time.Second)
}

func stop(s *mgr.Service, within time.Duration) error {
	st, err := s.Query()
	if err != nil {
		return err
	}
	if st.State == svc.Stopped {
		return nil
	}
	if st.State != svc.StopPending {
		if _, err := s.Control(svc.Stop); err != nil && !errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
			return fmt.Errorf("stop the service: %w", err)
		}
	}
	return waitFor(s, svc.Stopped, within)
}

func waitFor(s *mgr.Service, want svc.State, within time.Duration) error {
	deadline := time.Now().Add(within)
	for {
		st, err := s.Query()
		if err != nil {
			return err
		}
		if st.State == want {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the service did not reach the state %d in %s (it is %d)", want, within, st.State)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// Info is what anyone may ask the system about the service.
type Info struct {
	Installed bool
	Running   bool
	AtBoot    bool
}

// Query asks about the service without administrator rights: the system lets
// every signed-in user read a service's configuration and state.
func Query() (Info, error) {
	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return Info{}, fmt.Errorf("connect to the service manager: %w", err)
	}
	defer windows.CloseServiceHandle(scm)
	name, err := windows.UTF16PtrFromString(brand.WindowsServiceName)
	if err != nil {
		return Info{}, err
	}
	h, err := windows.OpenService(scm, name, windows.SERVICE_QUERY_CONFIG|windows.SERVICE_QUERY_STATUS)
	if err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return Info{}, nil
		}
		return Info{}, fmt.Errorf("open the service: %w", err)
	}
	s := &mgr.Service{Name: brand.WindowsServiceName, Handle: h}
	defer s.Close()
	cfg, err := s.Config()
	if err != nil {
		return Info{}, err
	}
	st, err := s.Query()
	if err != nil {
		return Info{}, err
	}
	return Info{Installed: true, Running: st.State == svc.Running, AtBoot: cfg.StartType == mgr.StartAutomatic}, nil
}

// SetAtBoot chooses whether the service starts with the computer. It needs
// administrator rights.
func SetAtBoot(on bool) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to the service manager: %w", err)
	}
	defer m.Disconnect()
	s, err := m.OpenService(brand.WindowsServiceName)
	if err != nil {
		return fmt.Errorf("open the service: %w", err)
	}
	defer s.Close()
	cfg, err := s.Config()
	if err != nil {
		return err
	}
	cfg.StartType = mgr.StartManual
	if on {
		cfg.StartType = mgr.StartAutomatic
	}
	return s.UpdateConfig(cfg)
}

// quoteCommand writes an executable path and its arguments as one command
// line the service manager can start.
func quoteCommand(exe string, args ...string) string {
	line := `"` + exe + `"`
	for _, a := range args {
		line += " " + a
	}
	return line
}
