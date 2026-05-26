//go:build fyne && windows

package main

import (
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"time"

	"github.com/official-elinas/hydrus-go/internal/desktop/fyneapp"
)

func main() {
	logPath := filepath.Join(os.TempDir(), "hydrus-desktop.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err == nil {
		log.SetOutput(logFile)
		log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
		defer logFile.Close()
	}

	log.Printf("hydrus-desktop starting %s", time.Now().Format(time.RFC3339))

	defer func() {
		if r := recover(); r != nil {
			log.Printf("PANIC: %v\n%s", r, debug.Stack())
		}
	}()

	fyneapp.Run()

	log.Printf("hydrus-desktop exited normally")
}
