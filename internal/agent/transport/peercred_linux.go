//go:build sbui_dev && linux

package transport

import (
	"net"

	"golang.org/x/sys/unix"
)

func peerCredentials(uc *net.UnixConn) (uid uint32, pid int, err error) {
	var cred *unix.Ucred
	var credErr error
	if err := rawControl(uc, func(fd int) {
		cred, credErr = unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return 0, 0, err
	}
	if credErr != nil {
		return 0, 0, credErr
	}
	return cred.Uid, int(cred.Pid), nil
}
