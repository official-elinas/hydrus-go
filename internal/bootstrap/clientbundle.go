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
	"syscall"
	"time"

	"github.com/official-elinas/hydrus-go/internal/storage/clientfiles"
)

var requiredBundleFiles = []string{
	"client.db",
	"client.master.db",
	"client.caches.db",
	"client.mappings.db",
}

const bootstrapLockFilename = ".hydrus-go-bootstrap.lock"
const bootstrapLockStaleAfter = 15 * time.Minute
const bootstrapLockHeartbeatInterval = time.Minute
const bootstrapCreatedThumbnailMarker = ".hydrus-go-bootstrap-created-thumbnail-root"

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
	rawDBDir := strings.TrimSpace(options.DBDir)
	if rawDBDir == "" {
		return Result{}, fmt.Errorf("hydrus DB directory must not be empty")
	}

	dbDir, err := filepath.Abs(filepath.Clean(rawDBDir))
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

	return withBootstrapLock(bootstrapCtx, dbDir, func() (Result, error) {
		lockedObserved, err := inspectBundleDir(dbDir)
		if err != nil {
			return Result{}, fmt.Errorf("inspect hydrus DB directory after lock: %w", err)
		}

		switch lockedObserved.State {
		case StateReady:
			return Result{State: StateReady}, nil
		case StatePartial:
			return Result{}, fmt.Errorf(
				"hydrus DB directory %q contains a partial client bundle (present: %s; missing: %s); delete the incomplete files or point hydrus-go at a valid bundle",
				dbDir,
				joinOrNone(lockedObserved.Present),
				joinOrNone(lockedObserved.Missing),
			)
		case StateNonEmptyWithoutBundle:
			return Result{}, fmt.Errorf(
				"hydrus DB directory %q is not empty but does not contain a canonical client bundle (entries: %s); fresh bootstrap only runs against an empty directory",
				dbDir,
				joinOrNone(lockedObserved.ExtraEntries),
			)
		case StateEmpty:
		default:
			return Result{}, fmt.Errorf("hydrus DB directory %q is in an unknown state after lock", dbDir)
		}

		if err := createFreshClientBundle(bootstrapCtx, dbDir); err != nil {
			cleanupErr := cleanupFreshBundleArtifacts(dbDir)
			return Result{}, errors.Join(
				fmt.Errorf("create fresh hydrus client bundle: %w", err),
				cleanupErr,
			)
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
		}, clearBootstrapThumbnailMarker(dbDir)
	})
}

func withBootstrapLock(
	ctx context.Context,
	dbDir string,
	fn func() (Result, error),
) (Result, error) {
	lockPath := filepath.Join(dbDir, bootstrapLockFilename)

	for {
		lockFile, err := os.OpenFile(
			lockPath,
			os.O_CREATE|os.O_EXCL|os.O_WRONLY,
			0o600,
		)
		if err == nil {
			if closeErr := lockFile.Close(); closeErr != nil {
				_ = os.Remove(lockPath)
				return Result{}, fmt.Errorf("close bootstrap lock file: %w", closeErr)
			}

			stopHeartbeat := startBootstrapLockHeartbeat(lockPath)
			defer stopHeartbeat()

			result, fnErr := fn()
			if removeErr := os.Remove(lockPath); removeErr != nil && !os.IsNotExist(removeErr) {
				if fnErr == nil {
					return Result{}, fmt.Errorf("remove bootstrap lock file %q: %w", lockPath, removeErr)
				}

				return Result{}, errors.Join(
					fnErr,
					fmt.Errorf("remove bootstrap lock file %q: %w", lockPath, removeErr),
				)
			}

			return result, fnErr
		}

		if !os.IsExist(err) {
			return Result{}, fmt.Errorf("create bootstrap lock file %q: %w", lockPath, err)
		}

		info, statErr := os.Stat(lockPath)
		switch {
		case statErr == nil && time.Since(info.ModTime()) > bootstrapLockStaleAfter:
			if removeErr := os.Remove(lockPath); removeErr != nil && !os.IsNotExist(removeErr) {
				return Result{}, fmt.Errorf("remove stale bootstrap lock file %q: %w", lockPath, removeErr)
			}
			continue
		case statErr == nil || os.IsNotExist(statErr):
		case statErr != nil:
			return Result{}, fmt.Errorf("stat bootstrap lock file %q: %w", lockPath, statErr)
		}

		select {
		case <-ctx.Done():
			return Result{}, fmt.Errorf("wait for bootstrap lock: %w", ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func inspectBundleDir(dbDir string) (inspection, error) {
	entries, err := os.ReadDir(dbDir)
	if err != nil {
		return inspection{}, fmt.Errorf("read directory %q: %w", dbDir, err)
	}

	visibleEntries := make([]os.DirEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Name() == bootstrapLockFilename {
			continue
		}

		visibleEntries = append(visibleEntries, entry)
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
	case len(visibleEntries) == 0:
		return inspection{State: StateEmpty, Missing: missing}, nil
	default:
		extraEntries := make([]string, 0, len(visibleEntries))
		for _, entry := range visibleEntries {
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
			return fmt.Errorf(
				"hydrus DB directory %q does not exist and fresh-client bootstrap is disabled; enable it with HYDRUS_GO_ENABLE_FRESH_CLIENT_BOOTSTRAP=true or --bootstrap-fresh-client",
				dbDir,
			)
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
	removeThumbnailRoot, err := shouldRemoveBootstrapThumbnailRoot(dbDir)
	if err != nil {
		return err
	}

	cleanupErrors := []error{}
	for _, path := range []string{
		paths.main,
		paths.master,
		paths.caches,
		paths.mappings,
		paths.temp,
		paths.files,
		filepath.Join(dbDir, bootstrapCreatedThumbnailMarker),
	} {
		if err := os.RemoveAll(path); err != nil && !os.IsNotExist(err) {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("remove %q: %w", path, err))
		}
	}

	if removeThumbnailRoot {
		thumbnailRoot := clientfiles.DefaultThumbnailRoot(dbDir)
		if err := os.Remove(thumbnailRoot); err != nil && !os.IsNotExist(err) && !errors.Is(err, syscall.ENOTEMPTY) {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("remove empty thumbnail root %q: %w", thumbnailRoot, err))
		}
	}

	return errors.Join(cleanupErrors...)
}

func startBootstrapLockHeartbeat(lockPath string) func() {
	stop := make(chan struct{})
	done := make(chan struct{})

	go func() {
		defer close(done)

		ticker := time.NewTicker(bootstrapLockHeartbeatInterval)
		defer ticker.Stop()

		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				now := time.Now()
				_ = os.Chtimes(lockPath, now, now)
			}
		}
	}()

	return func() {
		close(stop)
		<-done
	}
}

func shouldRemoveBootstrapThumbnailRoot(dbDir string) (bool, error) {
	markerPath := filepath.Join(dbDir, bootstrapCreatedThumbnailMarker)
	_, err := os.Stat(markerPath)
	switch {
	case err == nil:
		return true, nil
	case os.IsNotExist(err):
		return false, nil
	default:
		return false, fmt.Errorf("stat bootstrap thumbnail marker %q: %w", markerPath, err)
	}
}

func clearBootstrapThumbnailMarker(dbDir string) error {
	markerPath := filepath.Join(dbDir, bootstrapCreatedThumbnailMarker)
	if err := os.Remove(markerPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove bootstrap thumbnail marker %q: %w", markerPath, err)
	}

	return nil
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
