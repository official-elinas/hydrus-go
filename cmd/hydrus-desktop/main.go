//go:build fyne && !windows

// Command hydrus-desktop runs the thin Fyne prototype client for hydrusd.
package main

import "github.com/official-elinas/hydrus-go/internal/desktop/fyneapp"

func main() {
	fyneapp.Run()
}
