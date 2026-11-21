//go:build !android && !ios

package main

import "eggbox/internal/gui"

// phoneOptions changes nothing on a desktop.
func (d *desktop) phoneOptions(*gui.Options) {}

// phoneReady has nothing to do on a desktop.
func (d *desktop) phoneReady() {}

func (d *desktop) hasPhoneStatus() bool { return false }
func (d *desktop) syncPhoneStatus()     {}
