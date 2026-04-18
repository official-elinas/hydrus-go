// Package bootstrap prepares a fresh Hydrus client bundle via upstream Python.
package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	// Python-backed bootstrap.
	StateEmpty BundleState = "empty"
	// StatePartial means some but not all canonical bundle files exist.
	StatePartial BundleState = "partial"
	// StateNonEmptyWithoutBundle means the directory contains files, but none of
	// the canonical bundle files required by hydrus-go.
	StateNonEmptyWithoutBundle BundleState = "non_empty_without_bundle"
)

// Options controls fresh-client bootstrap behavior.
type Options struct {
	DBDir         string
	Enabled       bool
	PythonCommand string
	HydrusRoot    string
	Timeout       time.Duration
}

// Result reports what EnsureFreshClientBundle observed and whether it had to
// bootstrap a new bundle.
type Result struct {
	State        BundleState
	Bootstrapped bool
	HydrusRoot   string
}

type inspection struct {
	State        BundleState
	Present      []string
	Missing      []string
	ExtraEntries []string
}

type runRequest struct {
	DBDir         string
	HydrusRoot    string
	PythonCommand string
	Timeout       time.Duration
}

var runPythonFreshClientBootstrap = defaultRunPythonFreshClientBootstrap

// EnsureFreshClientBundle verifies the configured Hydrus DB directory is ready
// for hydrus-go, optionally bootstrapping a fresh canonical bundle with the
// upstream Python client creation path when the directory is empty.
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
				"hydrus DB directory %q is empty and Python fresh-client bootstrap is disabled; enable it with HYDRUS_GO_ENABLE_PYTHON_FRESH_CLIENT_BOOTSTRAP=true or --bootstrap-fresh-client",
				dbDir,
			)
		}
	default:
		return Result{}, fmt.Errorf("hydrus DB directory %q is in an unknown state", dbDir)
	}

	hydrusRoot, err := resolveHydrusRoot(options.HydrusRoot)
	if err != nil {
		return Result{}, err
	}

	pythonCommand := strings.TrimSpace(options.PythonCommand)
	if pythonCommand == "" {
		pythonCommand = defaultPythonCommand()
	}

	if options.Timeout <= 0 {
		return Result{}, fmt.Errorf("bootstrap timeout must be greater than zero")
	}

	err = runPythonFreshClientBootstrap(ctx, runRequest{
		DBDir:         dbDir,
		HydrusRoot:    hydrusRoot,
		PythonCommand: pythonCommand,
		Timeout:       options.Timeout,
	})
	if err != nil {
		return Result{}, err
	}

	verified, err := inspectBundleDir(dbDir)
	if err != nil {
		return Result{}, fmt.Errorf("verify bootstrapped hydrus DB directory: %w", err)
	}

	if verified.State != StateReady {
		return Result{}, fmt.Errorf(
			"python bootstrap finished, but hydrus DB directory %q is not ready (state: %s; present: %s; missing: %s)",
			dbDir,
			verified.State,
			joinOrNone(verified.Present),
			joinOrNone(verified.Missing),
		)
	}

	return Result{
		State:        StateReady,
		Bootstrapped: true,
		HydrusRoot:   hydrusRoot,
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

func resolveHydrusRoot(configured string) (string, error) {
	configured = strings.TrimSpace(configured)
	if configured != "" {
		validatedRoot, err := validateHydrusRoot(configured)
		if err != nil {
			return "", fmt.Errorf("validate configured Hydrus root: %w", err)
		}

		return validatedRoot, nil
	}

	candidates := []string{}
	if cwd, err := os.Getwd(); err == nil {
		candidates = appendAncestors(candidates, cwd, 3)
	}

	if executablePath, err := os.Executable(); err == nil {
		candidates = appendAncestors(candidates, filepath.Dir(executablePath), 4)
	}

	seen := map[string]struct{}{}
	for _, candidate := range candidates {
		cleanCandidate := filepath.Clean(candidate)
		if _, ok := seen[cleanCandidate]; ok {
			continue
		}
		seen[cleanCandidate] = struct{}{}

		validatedRoot, err := validateHydrusRoot(cleanCandidate)
		if err == nil {
			return validatedRoot, nil
		}
	}

	return "", fmt.Errorf(
		"could not resolve the upstream Hydrus Python root; set HYDRUS_GO_BOOTSTRAP_HYDRUS_ROOT or pass --bootstrap-hydrus-root",
	)
}

func validateHydrusRoot(root string) (string, error) {
	resolvedRoot, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", fmt.Errorf("resolve path %q: %w", root, err)
	}

	if err := requireDirectory(resolvedRoot); err != nil {
		return "", err
	}

	if err := requireDirectory(filepath.Join(resolvedRoot, "hydrus")); err != nil {
		return "", fmt.Errorf("%q is not a Hydrus root: %w", resolvedRoot, err)
	}

	if err := requireFile(filepath.Join(resolvedRoot, "hydrus_client.py")); err != nil {
		return "", fmt.Errorf("%q is not a Hydrus root: %w", resolvedRoot, err)
	}

	if err := requireFile(filepath.Join(resolvedRoot, "hydrus", "client", "db", "ClientDB.py")); err != nil {
		return "", fmt.Errorf("%q is not a Hydrus root: %w", resolvedRoot, err)
	}

	return resolvedRoot, nil
}

func requireDirectory(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat %q: %w", path, err)
	}

	if !info.IsDir() {
		return fmt.Errorf("%q must be a directory", path)
	}

	return nil
}

func requireFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat %q: %w", path, err)
	}

	if info.IsDir() {
		return fmt.Errorf("%q must be a file", path)
	}

	return nil
}

func appendAncestors(paths []string, start string, maxDepth int) []string {
	current := filepath.Clean(strings.TrimSpace(start))
	if current == "" || current == "." {
		return paths
	}

	for depth := 0; depth <= maxDepth; depth++ {
		paths = append(paths, current)

		parent := filepath.Dir(current)
		if parent == current {
			break
		}

		current = parent
	}

	return paths
}

func defaultPythonCommand() string {
	if runtime.GOOS == "windows" {
		return "python"
	}

	return "python3"
}

func defaultRunPythonFreshClientBootstrap(ctx context.Context, request runRequest) error {
	runCtx, cancel := context.WithTimeout(ctx, request.Timeout)
	defer cancel()

	commandArgs, err := splitCommandLine(request.PythonCommand)
	if err != nil {
		return fmt.Errorf("parse bootstrap python command: %w", err)
	}

	if len(commandArgs) == 0 {
		return fmt.Errorf("bootstrap python command must not be empty")
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	execArgs := append(commandArgs[1:], "-u", "-", "--db-dir", request.DBDir)

	cmd := exec.CommandContext(
		runCtx,
		commandArgs[0],
		execArgs...,
	)
	cmd.Dir = request.HydrusRoot
	cmd.Env = buildBootstrapEnv(os.Environ(), request.HydrusRoot)
	cmd.Stdin = strings.NewReader(pythonFreshClientBootstrapScript)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	if err == nil {
		return nil
	}

	formattedOutput := formatCommandOutput(stdout.String(), stderr.String())

	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf(
			"python bootstrap timed out after %s%s: %w",
			request.Timeout,
			formattedOutput,
			runCtx.Err(),
		)
	}

	if errors.Is(runCtx.Err(), context.Canceled) {
		return fmt.Errorf("python bootstrap canceled%s: %w", formattedOutput, runCtx.Err())
	}

	return fmt.Errorf("python bootstrap command failed%s: %w", formattedOutput, err)
}

func buildBootstrapEnv(base []string, hydrusRoot string) []string {
	env := append([]string{}, base...)
	pythonPath := hydrusRoot
	if existing, ok := lookupEnv(base, "PYTHONPATH"); ok && strings.TrimSpace(existing) != "" {
		pythonPath = hydrusRoot + string(os.PathListSeparator) + existing
	}
	env = upsertEnv(env, "PYTHONPATH", pythonPath)

	env = upsertEnv(env, "QT_QPA_PLATFORM", "offscreen")

	return env
}

func splitCommandLine(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	if info, err := os.Stat(raw); err == nil && !info.IsDir() {
		return []string{raw}, nil
	}

	args := []string{}
	var current strings.Builder
	inSingleQuote := false
	inDoubleQuote := false

	flushCurrent := func() {
		if current.Len() == 0 {
			return
		}

		args = append(args, current.String())
		current.Reset()
	}

	for _, r := range raw {
		switch {
		case r == '\'' && !inDoubleQuote:
			inSingleQuote = !inSingleQuote
		case r == '"' && !inSingleQuote:
			inDoubleQuote = !inDoubleQuote
		case (r == ' ' || r == '\t') && !inSingleQuote && !inDoubleQuote:
			flushCurrent()
		default:
			current.WriteRune(r)
		}
	}

	if inSingleQuote || inDoubleQuote {
		return nil, fmt.Errorf("unclosed quote in %q", raw)
	}

	flushCurrent()

	return args, nil
}

func lookupEnv(env []string, key string) (string, bool) {
	normalizedKey := normalizeEnvKey(key)
	for _, entry := range env {
		entryKey, entryValue, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}

		if normalizeEnvKey(entryKey) == normalizedKey {
			return entryValue, true
		}
	}

	return "", false
}

func upsertEnv(env []string, key string, value string) []string {
	normalizedKey := normalizeEnvKey(key)
	assignment := key + "=" + value
	result := make([]string, 0, len(env)+1)
	replaced := false
	for _, entry := range env {
		entryKey, _, ok := strings.Cut(entry, "=")
		if !ok {
			result = append(result, entry)
			continue
		}

		if normalizeEnvKey(entryKey) == normalizedKey {
			if !replaced {
				result = append(result, assignment)
				replaced = true
			}
			continue
		}

		result = append(result, entry)
	}

	if !replaced {
		result = append(result, assignment)
	}

	return result
}

func normalizeEnvKey(key string) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper(key)
	}

	return key
}

func formatCommandOutput(stdout string, stderr string) string {
	stdout = truncateOutput(stdout)
	stderr = truncateOutput(stderr)

	parts := []string{}
	if stdout != "" {
		parts = append(parts, fmt.Sprintf("stdout=%q", stdout))
	}

	if stderr != "" {
		parts = append(parts, fmt.Sprintf("stderr=%q", stderr))
	}

	if len(parts) == 0 {
		return ""
	}

	return "; " + strings.Join(parts, "; ")
}

func truncateOutput(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}

	const maxLen = 2000
	if len(trimmed) <= maxLen {
		return trimmed
	}

	return trimmed[:maxLen] + "..."
}

func joinOrNone(values []string) string {
	if len(values) == 0 {
		return "none"
	}

	return strings.Join(values, ", ")
}

const pythonFreshClientBootstrapScript = `
import argparse
import os
import threading
import time
import traceback


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Create a fresh Hydrus client bundle using upstream Python code.",
    )
    parser.add_argument("--db-dir", required=True)
    args = parser.parse_args()

    db_dir = os.path.abspath(args.db_dir)

    from hydrus.core import HydrusBoot

    HydrusBoot.AddBaseDirToEnvPath()
    HydrusBoot.DoPreImportEnvWork()

    from hydrus.client.gui import QtInit

    from hydrus.core import HydrusConstants as HC
    from hydrus.core import HydrusGlobals as HG

    HC.RUNNING_CLIENT = True

    HG.db_journal_mode = "WAL"
    HG.db_cache_size = 256
    HG.db_transaction_commit_period = 30
    HG.db_synchronous = 1
    HG.no_db_temp_files = False
    HG.boot_debug = False
    HG.model_shutdown = False

    from hydrus.client import ClientDefaults
    from hydrus.client import ClientGlobals as CG
    from hydrus.client import ClientOptions
    from hydrus.client.db import ClientDB

    class SplashStatus:
        def SetText(self, text, print_to_log=True):
            return None

        def SetSubtext(self, text):
            return None

        def SetTitleText(self, text, clear_undertexts=True, print_to_log=True):
            return None

        def Reset(self):
            return None

    class BootstrapController:
        def __init__(self, root):
            self.db_dir = root
            self.frame_splash_status = SplashStatus()
            self.new_options = ClientOptions.ClientOptions()
            self.options = ClientDefaults.GetClientDefaultOptions()

        def CallToThreadLongRunning(self, callable, *args, **kwargs):
            thread = threading.Thread(
                target=callable,
                args=args,
                kwargs=kwargs,
                daemon=True,
                name="hydrus-go-bootstrap-db",
            )
            thread.start()
            return thread

        def CurrentlyIdle(self):
            return False

        def pub(self, *args, **kwargs):
            return None

        def sub(self, *args, **kwargs):
            return None

        def BlockingSafeShowCriticalMessage(self, title, message):
            raise RuntimeError(f"{title}: {message}")

        def BlockingSafeShowMessage(self, message):
            return None

        def CallBlockingToQtTLW(self, callable, *args, **kwargs):
            return callable(*args, **kwargs)

        def ShouldStopThisWork(self, maintenance_mode, stop_time=None):
            return False

        def LastShutdownWasBad(self):
            return False

    controller = BootstrapController(db_dir)
    HG.controller = controller
    CG.client_controller = controller

    db = ClientDB.DB(controller, db_dir, "client")
    try:
        return 0
    finally:
        db.Shutdown()

        deadline = time.time() + 30.0
        while not db.LoopIsFinished():
            if time.time() > deadline:
                raise TimeoutError(
                    "timed out waiting for the Hydrus bootstrap DB loop to stop",
                )

            time.sleep(0.1)


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception:
        traceback.print_exc()
        raise
`
