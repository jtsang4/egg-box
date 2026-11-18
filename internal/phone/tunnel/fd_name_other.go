//go:build !linux && !darwin

package tunnel

func interfaceName(int) (string, error) { return "", ErrUnsupported }
