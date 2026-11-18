//go:build !darwin

package iostunnel

import "eggbox/internal/phone/tunnel"

func findUtun() (int, error) { return -1, tunnel.ErrUnsupported }
