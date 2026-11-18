//go:build !android && !ios

package main

import "eggbox/internal/gui"

// phoneOptions changes nothing on a desktop.
func (d *desktop) phoneOptions(*gui.Options) {}
