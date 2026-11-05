//go:build windows

package winsys

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	wtsapi32                       = windows.NewLazySystemDLL("wtsapi32.dll")
	procWTSQuerySessionInformation = wtsapi32.NewProc("WTSQuerySessionInformationW")
	procWTSFreeMemory              = wtsapi32.NewProc("WTSFreeMemory")
)

const (
	wtsUserName   = 5
	wtsDomainName = 7
)

// SessionUser is the person sitting in the session of this process, as the
// system's logon records say it. It is not the user of the process's token:
// an installer approved with another administrator's password runs as that
// administrator, while the person who is to use the application is the one
// who is signed in.
func SessionUser() (Owner, error) {
	var session uint32
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &session); err != nil {
		return Owner{}, fmt.Errorf("find the session: %w", err)
	}
	if session == 0 {
		return Owner{}, errors.New("this process is not in a user's session")
	}
	return UserOfSession(session)
}

// ConsoleUser is the person signed in at the computer's own screen. The
// service, which lives in no user's session, asks this.
func ConsoleUser() (Owner, uint32, error) {
	session := windows.WTSGetActiveConsoleSessionId()
	if session == 0xFFFFFFFF {
		return Owner{}, 0, errors.New("nobody is at the console")
	}
	o, err := UserOfSession(session)
	return o, session, err
}

// UserOfSession is the person signed in to a session.
func UserOfSession(session uint32) (Owner, error) {
	name, err := queryString(session, wtsUserName)
	if err != nil {
		return Owner{}, err
	}
	domain, err := queryString(session, wtsDomainName)
	if err != nil {
		return Owner{}, err
	}
	if name == "" {
		return Owner{}, errors.New("nobody is signed in to this session")
	}
	account := name
	if domain != "" {
		account = domain + `\` + name
	}
	sid, err := lookupSID(account)
	if err != nil {
		return Owner{}, err
	}
	return Owner{SID: sid, Name: account}, nil
}

// CurrentUser is the user this process runs as.
func CurrentUser() (Owner, error) {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return Owner{}, fmt.Errorf("read the process's user: %w", err)
	}
	o := Owner{SID: u.User.Sid.String()}
	if account, domain, _, err := u.User.Sid.LookupAccount(""); err == nil {
		o.Name = account
		if domain != "" {
			o.Name = domain + `\` + account
		}
	}
	return o, nil
}

func queryString(session uint32, class uint32) (string, error) {
	var buf *uint16
	var n uint32
	r, _, e := procWTSQuerySessionInformation.Call(0, uintptr(session), uintptr(class),
		uintptr(unsafe.Pointer(&buf)), uintptr(unsafe.Pointer(&n)))
	if r == 0 {
		return "", fmt.Errorf("read the session's user: %w", e)
	}
	defer procWTSFreeMemory.Call(uintptr(unsafe.Pointer(buf)))
	return windows.UTF16PtrToString(buf), nil
}

func lookupSID(account string) (string, error) {
	sid, _, _, err := windows.LookupSID("", account)
	if err != nil {
		return "", fmt.Errorf("find the identifier of %s: %w", account, err)
	}
	return sid.String(), nil
}
