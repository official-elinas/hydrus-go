// Command hydrusd runs the hydrus-go bootstrap daemon.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
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

	cfg, err := loadRuntimeConfig(os.Args[1:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
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

type cliOptions struct {
	listenAddr string
	listenSet  bool
}

func loadRuntimeConfig(args []string, stderr io.Writer) (config.Config, error) {
	options, err := parseCLIOptions(args, stderr)
	if err != nil {
		return config.Config{}, err
	}

	cfg, err := config.LoadFromEnvUnvalidated()
	if err != nil {
		return config.Config{}, err
	}

	if options.listenSet {
		cfg.ListenAddr = options.listenAddr
		cfg.AllowNonLocalConnections = true
	}

	if err := cfg.Validate(); err != nil {
		return config.Config{}, err
	}

	return cfg, nil
}

func parseCLIOptions(args []string, stderr io.Writer) (cliOptions, error) {
	flagSet := flag.NewFlagSet("hydrusd", flag.ContinueOnError)
	flagSet.SetOutput(stderr)
	flagSet.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "Usage: hydrusd [--listen host:port]")
		flagSet.PrintDefaults()
	}

	var options cliOptions
	flagSet.StringVar(
		&options.listenAddr,
		"listen",
		"",
		"override daemon listen address for this invocation (for example 0.0.0.0:5555)",
	)

	if err := flagSet.Parse(args); err != nil {
		return cliOptions{}, err
	}

	flagSet.Visit(func(f *flag.Flag) {
		if f.Name == "listen" {
			options.listenSet = true
		}
	})
	options.listenAddr = strings.TrimSpace(options.listenAddr)

	if flagSet.NArg() > 0 {
		return cliOptions{}, fmt.Errorf("unexpected arguments: %s", strings.Join(flagSet.Args(), " "))
	}

	return options, nil
}

func exitf(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
