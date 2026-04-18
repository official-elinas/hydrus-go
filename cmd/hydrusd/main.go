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
	"path/filepath"
	"strings"
	"syscall"
	"time"

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

	application, err := app.New(ctx, cfg, logger)
	if err != nil {
		exitf("create app: %v", err)
	}

	err = application.Run(ctx)
	if err != nil && !errors.Is(err, context.Canceled) {
		exitf("run hydrus-go: %v", err)
	}
}

type cliOptions struct {
	listenAddr              string
	listenSet               bool
	bootstrapFreshClient    bool
	bootstrapFreshClientSet bool
	bootstrapPython         string
	bootstrapPythonSet      bool
	bootstrapHydrusRoot     string
	bootstrapHydrusRootSet  bool
	bootstrapTimeout        time.Duration
	bootstrapTimeoutSet     bool
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

	if options.bootstrapFreshClientSet {
		cfg.EnableFreshClientBootstrap = options.bootstrapFreshClient
	}

	if options.bootstrapPythonSet {
		cfg.BootstrapPythonCommand = strings.TrimSpace(options.bootstrapPython)
	}

	if options.bootstrapHydrusRootSet {
		cfg.BootstrapHydrusRoot = strings.TrimSpace(options.bootstrapHydrusRoot)
		if cfg.BootstrapHydrusRoot != "" {
			cfg.BootstrapHydrusRoot = filepath.Clean(cfg.BootstrapHydrusRoot)
		}
	}

	if options.bootstrapTimeoutSet {
		cfg.BootstrapTimeout = options.bootstrapTimeout
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
		_, _ = fmt.Fprintln(
			stderr,
			"Usage: hydrusd [--listen host:port] [--bootstrap-fresh-client] [--bootstrap-python path] [--bootstrap-hydrus-root path] [--bootstrap-timeout duration]",
		)
		flagSet.PrintDefaults()
	}

	var options cliOptions
	flagSet.StringVar(
		&options.listenAddr,
		"listen",
		"",
		"override daemon listen address for this invocation (for example 0.0.0.0:5555)",
	)
	flagSet.BoolVar(
		&options.bootstrapFreshClient,
		"bootstrap-fresh-client",
		false,
		"if HYDRUS_GO_DB_DIR is empty, create a fresh canonical client bundle via the upstream Python bootstrap",
	)
	flagSet.StringVar(
		&options.bootstrapPython,
		"bootstrap-python",
		"",
		"override the Python interpreter used for fresh-client bootstrap (for example /path/to/python3)",
	)
	flagSet.StringVar(
		&options.bootstrapHydrusRoot,
		"bootstrap-hydrus-root",
		"",
		"override the upstream Hydrus Python root used for fresh-client bootstrap",
	)
	flagSet.DurationVar(
		&options.bootstrapTimeout,
		"bootstrap-timeout",
		0,
		"override the timeout for fresh-client bootstrap (for example 2m)",
	)

	if err := flagSet.Parse(args); err != nil {
		return cliOptions{}, err
	}

	flagSet.Visit(func(f *flag.Flag) {
		if f.Name == "listen" {
			options.listenSet = true
		}

		if f.Name == "bootstrap-fresh-client" {
			options.bootstrapFreshClientSet = true
		}

		if f.Name == "bootstrap-python" {
			options.bootstrapPythonSet = true
		}

		if f.Name == "bootstrap-hydrus-root" {
			options.bootstrapHydrusRootSet = true
		}

		if f.Name == "bootstrap-timeout" {
			options.bootstrapTimeoutSet = true
		}
	})
	options.listenAddr = strings.TrimSpace(options.listenAddr)
	options.bootstrapPython = strings.TrimSpace(options.bootstrapPython)
	options.bootstrapHydrusRoot = strings.TrimSpace(options.bootstrapHydrusRoot)

	if flagSet.NArg() > 0 {
		return cliOptions{}, fmt.Errorf("unexpected arguments: %s", strings.Join(flagSet.Args(), " "))
	}

	return options, nil
}

func exitf(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
