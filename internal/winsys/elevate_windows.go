//go:build windows

package winsys

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ErrDeclined is returned when the person said no to the system's prompt for
// administrator rights.
var ErrDeclined = errors.New("administrator permission was not given")

var (
	shell32           = windows.NewLazySystemDLL("shell32.dll")
	procShellExecuteE = shell32.NewProc("ShellExecuteExW")
)

const (
	seeMaskNoCloseProcess = 0x00000040
	seeMaskNoAsync        = 0x00000100
	seeMaskFlagNoUI       = 0x00000400
)

// shellExecuteInfo is SHELLEXECUTEINFOW.
type shellExecuteInfo struct {
	cbSize       uint32
	fMask        uint32
	hwnd         windows.Handle
	lpVerb       *uint16
	lpFile       *uint16
	lpParameters *uint16
	lpDirectory  *uint16
	nShow        int32
	hInstApp     windows.Handle
	lpIDList     uintptr
	lpClass      *uint16
	hkeyClass    windows.Handle
	dwHotKey     uint32
	hIcon        windows.Handle
	hProcess     windows.Handle
}

// RunElevated runs exe with arguments after the system asks the person for
// administrator rights (the UAC prompt), waits for it to end and returns its
// exit code. This is how the application asks, once, for what only an
// administrator may do: install or repair the service, or change who may use it.
func RunElevated(ctx context.Context, exe string, args ...string) (uint32, error) {
	// The shell may hand the request to components that want a single-threaded
	// apartment on the calling thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED|windows.COINIT_DISABLE_OLE1DDE); err == nil {
		defer windows.CoUninitialize()
	}

	verb, _ := windows.UTF16PtrFromString("runas")
	file, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return 0, err
	}
	params, err := windows.UTF16PtrFromString(joinArgs(args))
	if err != nil {
		return 0, err
	}
	info := shellExecuteInfo{
		fMask:        seeMaskNoCloseProcess | seeMaskNoAsync | seeMaskFlagNoUI,
		lpVerb:       verb,
		lpFile:       file,
		lpParameters: params,
		nShow:        windows.SW_HIDE,
	}
	info.cbSize = uint32(unsafe.Sizeof(info))
	r, _, e := procShellExecuteE.Call(uintptr(unsafe.Pointer(&info)))
	if r == 0 {
		if errno, ok := e.(syscall.Errno); ok && errno == windows.ERROR_CANCELLED {
			return 0, ErrDeclined
		}
		return 0, fmt.Errorf("start %s with administrator rights: %w", exe, e)
	}
	if info.hProcess == 0 {
		return 0, errors.New("the system did not give a handle to the elevated process")
	}
	defer windows.CloseHandle(info.hProcess)

	for {
		ev, err := windows.WaitForSingleObject(info.hProcess, 250)
		if err != nil {
			return 0, err
		}
		if ev == windows.WAIT_OBJECT_0 {
			break
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	var code uint32
	if err := windows.GetExitCodeProcess(info.hProcess, &code); err != nil {
		return 0, err
	}
	return code, nil
}

// joinArgs quotes arguments the way a Windows program parses them.
func joinArgs(args []string) string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = windows.EscapeArg(a)
	}
	return strings.Join(out, " ")
}
