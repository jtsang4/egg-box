//go:build !windows && !darwin

package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"runtime"
)

// installedServices: until the platform's own transport is wired in, the
// interface reports the service as unavailable. Development builds reach a
// development agent instead (see dev_on.go).
func installedServices() platformServices {
	return platformServices{Dial: func(context.Context) (net.Conn, error) {
		return nil, fmt.Errorf("there is no privileged service for %s: %w", runtime.GOOS, os.ErrNotExist)
	}}
}
