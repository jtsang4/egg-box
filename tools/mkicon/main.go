// Command mkicon writes the application icon, resources/icon.png, from the
// geometry in internal/brandart. Run it from the repository root:
//
//	go run ./tools/mkicon
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"eggbox/internal/brandart"
)

func main() {
	out := flag.String("o", filepath.Join("resources", "icon.png"), "where to write the icon")
	size := flag.Int("size", 1024, "side of the icon in pixels")
	flag.Parse()
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "mkicon:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, brandart.PNG(brandart.AppIcon(*size)), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "mkicon:", err)
		os.Exit(1)
	}
	fmt.Println("wrote", *out)
}
