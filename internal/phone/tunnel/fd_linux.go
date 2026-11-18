package tunnel

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/unix"
)

// interfaceName reads the name of the TUN device behind fd (Android, Linux).
func interfaceName(fd int) (string, error) {
	var ifr [unix.IFNAMSIZ + 64]byte
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), uintptr(unix.TUNGETIFF), uintptr(unsafe.Pointer(&ifr[0])))
	if errno != 0 {
		return "", fmt.Errorf("TUNGETIFF: %w", errno)
	}
	return unix.ByteSliceToString(ifr[:]), nil
}
