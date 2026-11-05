//go:build sbui_dev && darwin

package transport

import (
	"net"

	"golang.org/x/sys/unix"
)

func peerCredentials(uc *net.UnixConn) (uid uint32, pid int, err error) {
	var cred *unix.Xucred
	var credErr, pidErr error
	var p int
	if err := rawControl(uc, func(fd int) {
		cred, credErr = unix.GetsockoptXucred(fd, unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		p, pidErr = unix.GetsockoptInt(fd, unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	}); err != nil {
		return 0, 0, err
	}
	if credErr != nil {
		return 0, 0, credErr
	}
	if pidErr != nil {
		p = 0
	}
	return cred.Uid, p, nil
}
