// Command hydrusd runs the hydrus-go bootstrap daemon.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/official-elinas/hydrus-go/internal/app"
	"github.com/official-elinas/hydrus-go/internal/config"
	"github.com/official-elinas/hydrus-go/internal/logging"
)

func main() {
	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	cfg, err := config.LoadFromEnv()
	if err != nil {
		exitf("load config: %v", err)
	}

	logger, err := logging.New(cfg.LogLevel)
	if err != nil {
		exitf("configure logger: %v", err)
	}

	application, err := app.New(cfg, logger)
	if err != nil {
		exitf("create app: %v", err)
	}

	err = application.Run(ctx)
	if err != nil && !errors.Is(err, context.Canceled) {
		exitf("run hydrus-go: %v", err)
	}
}

func exitf(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
