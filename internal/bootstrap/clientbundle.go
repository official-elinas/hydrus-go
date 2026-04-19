// Package bootstrap prepares a fresh Hydrus client bundle.
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var requiredBundleFiles = []string{
	"client.db",
	"client.master.db",
	"client.caches.db",
	"client.mappings.db",
}

// BundleState describes the observed state of a configured Hydrus DB directory.
type BundleState string

const (
	// StateReady means the canonical Hydrus client bundle files already exist.
	StateReady BundleState = "ready"
	// StateEmpty means the configured directory is empty and eligible for a fresh
	// bootstrap.
	StateEmpty BundleState = "empty"
	// StatePartial means some but not all canonical bundle files exist.
	StatePartial BundleState = "partial"
	// StateNonEmptyWithoutBundle means the directory contains files, but none of
	// the canonical bundle files required by hydrus-go.
	StateNonEmptyWithoutBundle BundleState = "non_empty_without_bundle"
)

// Options controls fresh-client bootstrap behavior.
type Options struct {
	DBDir   string
	Enabled bool
	Timeout time.Duration
}

// Result reports what EnsureFreshClientBundle observed and whether it had to
// bootstrap a new bundle.
type Result struct {
	State        BundleState
	Bootstrapped bool
}

type inspection struct {
	State        BundleState
	Present      []string
	Missing      []string
	ExtraEntries []string
}

// EnsureFreshClientBundle verifies the configured Hydrus DB directory is ready
// for hydrus-go, optionally bootstrapping a fresh canonical bundle when the
// target directory is empty or missing.
func EnsureFreshClientBundle(ctx context.Context, options Options) (Result, error) {
	dbDir, err := filepath.Abs(filepath.Clean(strings.TrimSpace(options.DBDir)))
	if err != nil {
		return Result{}, fmt.Errorf("resolve hydrus DB directory: %w", err)
	}

	if err := ensureBootstrapDBDir(dbDir, options.Enabled); err != nil {
		return Result{}, err
	}

	observed, err := inspectBundleDir(dbDir)
	if err != nil {
		return Result{}, fmt.Errorf("inspect hydrus DB directory: %w", err)
	}

	switch observed.State {
	case StateReady:
		return Result{State: StateReady}, nil
	case StatePartial:
		return Result{}, fmt.Errorf(
			"hydrus DB directory %q contains a partial client bundle (present: %s; missing: %s); delete the incomplete files or point hydrus-go at a valid bundle",
			dbDir,
			joinOrNone(observed.Present),
			joinOrNone(observed.Missing),
		)
	case StateNonEmptyWithoutBundle:
		return Result{}, fmt.Errorf(
			"hydrus DB directory %q is not empty but does not contain a canonical client bundle (entries: %s); fresh bootstrap only runs against an empty directory",
			dbDir,
			joinOrNone(observed.ExtraEntries),
		)
	case StateEmpty:
		if !options.Enabled {
			return Result{}, fmt.Errorf(
				"hydrus DB directory %q is empty and fresh-client bootstrap is disabled; enable it with HYDRUS_GO_ENABLE_FRESH_CLIENT_BOOTSTRAP=true or --bootstrap-fresh-client",
				dbDir,
			)
		}
	default:
		return Result{}, fmt.Errorf("hydrus DB directory %q is in an unknown state", dbDir)
	}

	if options.Timeout <= 0 {
		return Result{}, fmt.Errorf("bootstrap timeout must be greater than zero")
	}

	bootstrapCtx, cancel := context.WithTimeout(ctx, options.Timeout)
	defer cancel()

	if err := createFreshClientBundle(bootstrapCtx, dbDir); err != nil {
		return Result{}, fmt.Errorf("create fresh hydrus client bundle: %w", err)
	}

	verified, err := inspectBundleDir(dbDir)
	if err != nil {
		return Result{}, fmt.Errorf("verify bootstrapped hydrus DB directory: %w", err)
	}

	if verified.State != StateReady {
		cleanupErr := cleanupFreshBundleArtifacts(dbDir)
		return Result{}, fmt.Errorf(
			"fresh bootstrap finished, but hydrus DB directory %q is not ready (state: %s; present: %s; missing: %s)%s",
			dbDir,
			verified.State,
			joinOrNone(verified.Present),
			joinOrNone(verified.Missing),
			formatCleanupError(cleanupErr),
		)
	}

	if err := verifyFreshClientBundle(bootstrapCtx, dbDir); err != nil {
		cleanupErr := cleanupFreshBundleArtifacts(dbDir)
		return Result{}, errors.Join(
			fmt.Errorf("verify fresh hydrus client bundle: %w", err),
			cleanupErr,
		)
	}

	return Result{
		State:        StateReady,
		Bootstrapped: true,
	}, nil
}

func inspectBundleDir(dbDir string) (inspection, error) {
	entries, err := os.ReadDir(dbDir)
	if err != nil {
		return inspection{}, fmt.Errorf("read directory %q: %w", dbDir, err)
	}

	present := []string{}
	missing := []string{}
	for _, filename := range requiredBundleFiles {
		path := filepath.Join(dbDir, filename)
		info, statErr := os.Stat(path)
		switch {
		case statErr == nil:
			if info.IsDir() {
				return inspection{}, fmt.Errorf("%s must be a file", filename)
			}
			present = append(present, filename)
		case os.IsNotExist(statErr):
			missing = append(missing, filename)
		default:
			return inspection{}, fmt.Errorf("stat %s: %w", filename, statErr)
		}
	}

	sort.Strings(present)
	sort.Strings(missing)

	switch {
	case len(present) == len(requiredBundleFiles):
		return inspection{State: StateReady, Present: present}, nil
	case len(present) > 0:
		return inspection{State: StatePartial, Present: present, Missing: missing}, nil
	case len(entries) == 0:
		return inspection{State: StateEmpty, Missing: missing}, nil
	default:
		extraEntries := make([]string, 0, len(entries))
		for _, entry := range entries {
			extraEntries = append(extraEntries, entry.Name())
		}
		sort.Strings(extraEntries)

		return inspection{
			State:        StateNonEmptyWithoutBundle,
			Missing:      missing,
			ExtraEntries: extraEntries,
		}, nil
	}
}

func ensureBootstrapDBDir(dbDir string, enabled bool) error {
	info, err := os.Stat(dbDir)
	switch {
	case err == nil:
		if !info.IsDir() {
			return fmt.Errorf("hydrus DB directory %q must be a directory", dbDir)
		}

		return nil
	case os.IsNotExist(err):
		if !enabled {
			return nil
		}

		if mkdirErr := os.MkdirAll(dbDir, 0o755); mkdirErr != nil {
			return fmt.Errorf("create hydrus DB directory %q: %w", dbDir, mkdirErr)
		}

		return nil
	default:
		return fmt.Errorf("stat hydrus DB directory %q: %w", dbDir, err)
	}
}

func cleanupFreshBundleArtifacts(dbDir string) error {
	paths := bundleFilePaths(dbDir)
	cleanupErrors := []error{}
	for _, path := range []string{
		paths.main,
		paths.master,
		paths.caches,
		paths.mappings,
		paths.temp,
		paths.files,
	} {
		if err := os.RemoveAll(path); err != nil && !os.IsNotExist(err) {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("remove %q: %w", path, err))
		}
	}

	return errors.Join(cleanupErrors...)
}

func formatCleanupError(err error) string {
	if err == nil {
		return ""
	}

	return "; cleanup failed: " + err.Error()
}

func joinOrNone(values []string) string {
	if len(values) == 0 {
		return "none"
	}

	return strings.Join(values, ", ")
}
