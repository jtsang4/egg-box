//go:build unix

package tunnel

import "golang.org/x/sys/unix"

// dupFD gives the core a descriptor of its own, closed with its TUN, while the
// system keeps the one it handed over.
func dupFD(fd int) (int, error) {
	n, err := unix.Dup(fd)
	if err != nil {
		return -1, err
	}
	unix.CloseOnExec(n)
	return n, nil
}
