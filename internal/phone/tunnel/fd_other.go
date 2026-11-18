//go:build !unix

package tunnel

func dupFD(int) (int, error) { return -1, ErrUnsupported }
