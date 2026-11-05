//go:build !sbui_dev

package main

import "github.com/jtsang4/sing-box-ui/internal/boot"

// devOptions are the flags of development builds; release builds have none.
type devOptions struct{}

func (devOptions) parse(string, func() (string, error)) (bool, error) { return false, nil }

// dev reports whether the run uses a development agent.
func (devOptions) active() bool { return false }

// devConfig is never reached in release builds.
func (devOptions) config(*boot.Config) error { return nil }

// devReady is the hook where development builds attach their tooling.
func (d *desktop) devReady() {}
