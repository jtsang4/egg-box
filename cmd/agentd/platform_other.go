//go:build !windows

package main

// platformUsage lists the commands of this operating system's service.
const platformUsage = ""

// runPlatform runs a command specific to this operating system's service. The
// second result is false when the command is not one of them.
func runPlatform([]string) (int, bool) { return 0, false }
