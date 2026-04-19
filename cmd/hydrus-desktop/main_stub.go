//go:build !fyne

// Command hydrus-desktop explains how to build the Fyne prototype when desktop
// build tags are not enabled.
package main

import (
	"fmt"
	"os"
)

func main() {
	_, _ = fmt.Fprintln(
		os.Stderr,
		"hydrus-desktop requires the Fyne desktop build tag: go run -tags fyne ./cmd/hydrus-desktop",
	)
	os.Exit(1)
}
