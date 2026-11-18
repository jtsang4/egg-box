package tunnel

import "golang.org/x/sys/unix"

// interfaceName reads the name of the utun device behind fd (the iOS
// extension's tunnel, and macOS).
func interfaceName(fd int) (string, error) {
	const sysprotoControl, utunOptIfname = 2, 2
	return unix.GetsockoptString(fd, sysprotoControl, utunOptIfname)
}
