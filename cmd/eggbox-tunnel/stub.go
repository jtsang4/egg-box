//go:build !ios

// Command eggbox-tunnel is the core of EggBox's packet tunnel extension, a C
// archive built for iOS only (see main.go).
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "eggbox-tunnel is the iOS packet tunnel's core, built with -buildmode=c-archive for GOOS=ios")
	os.Exit(2)
}
