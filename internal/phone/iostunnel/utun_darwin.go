package iostunnel

import (
	"errors"
	"strings"

	"golang.org/x/sys/unix"
)

// findUtun returns the descriptor of the provider's utun interface: the one
// control socket of the process that names a utun device.
func findUtun() (int, error) {
	const sysprotoControl, utunOptIfname = 2, 2
	for fd := 0; fd < 1024; fd++ {
		name, err := unix.GetsockoptString(fd, sysprotoControl, utunOptIfname)
		if err == nil && strings.HasPrefix(name, "utun") {
			return fd, nil
		}
	}
	return -1, errors.New("no utun interface among the process's descriptors")
}
