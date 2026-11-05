//go:build !sbui_dev

package main

import (
	"fmt"
	"os"

	"github.com/jtsang4/sing-box-ui/internal/version"
)

// run dispatches the command line of a release build.
func run(args []string) int {
	if len(args) == 0 {
		usage(os.Stderr, "")
		return 2
	}
	switch args[0] {
	case "version":
		fmt.Println(version.String())
		return 0
	default:
		usage(os.Stderr, "")
		return 2
	}
}
