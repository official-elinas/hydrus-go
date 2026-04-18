package bootstrap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestEnsureFreshClientBundle_ReadyBundleSkipsBootstrap(t *testing.T) {
	dbDir := t.TempDir()
	createBundleFiles(t, dbDir)

	originalRunner := runPythonFreshClientBootstrap
	runPythonFreshClientBootstrap = func(context.Context, runRequest) error {
		t.Fatal("runPythonFreshClientBootstrap() called, want skip for existing bundle")
		return nil
	}
	defer func() {
		runPythonFreshClientBootstrap = originalRunner
	}()

	result, err := EnsureFreshClientBundle(context.Background(), Options{
		DBDir:   dbDir,
		Enabled: true,
		Timeout: time.Minute,
	})
	if err != nil {
		t.Fatalf("EnsureFreshClientBundle() error = %v", err)
	}

	if result.State != StateReady {
		t.Fatalf("result.State = %q, want %q", result.State, StateReady)
	}

	if result.Bootstrapped {
		t.Fatal("result.Bootstrapped = true, want false")
	}

	if result.HydrusRoot != "" {
		t.Fatalf("result.HydrusRoot = %q, want empty", result.HydrusRoot)
	}
}

func TestEnsureFreshClientBundle_EmptyDirRequiresOptIn(t *testing.T) {
	_, err := EnsureFreshClientBundle(context.Background(), Options{
		DBDir:   t.TempDir(),
		Enabled: false,
		Timeout: time.Minute,
	})
	if err == nil {
		t.Fatal("EnsureFreshClientBundle() error = nil, want error")
	}

	if !strings.Contains(err.Error(), "empty") {
		t.Fatalf("EnsureFreshClientBundle() error = %v, want empty-dir guidance", err)
	}

	if !strings.Contains(err.Error(), "--bootstrap-fresh-client") {
		t.Fatalf("EnsureFreshClientBundle() error = %v, want runtime flag guidance", err)
	}
}

func TestEnsureFreshClientBundle_EmptyDirBootstrapsAndVerifies(t *testing.T) {
	dbDir := t.TempDir()
	hydrusRoot := createFakeHydrusRoot(t)

	originalRunner := runPythonFreshClientBootstrap
	defer func() {
		runPythonFreshClientBootstrap = originalRunner
	}()

	called := false
	runPythonFreshClientBootstrap = func(_ context.Context, request runRequest) error {
		called = true

		if request.DBDir != dbDir {
			t.Fatalf("request.DBDir = %q, want %q", request.DBDir, dbDir)
		}

		if request.HydrusRoot != hydrusRoot {
			t.Fatalf("request.HydrusRoot = %q, want %q", request.HydrusRoot, hydrusRoot)
		}

		if request.PythonCommand != "/usr/bin/python-custom" {
			t.Fatalf(
				"request.PythonCommand = %q, want %q",
				request.PythonCommand,
				"/usr/bin/python-custom",
			)
		}

		if request.Timeout != 45*time.Second {
			t.Fatalf("request.Timeout = %v, want %v", request.Timeout, 45*time.Second)
		}

		createBundleFiles(t, request.DBDir)
		return nil
	}

	result, err := EnsureFreshClientBundle(context.Background(), Options{
		DBDir:         dbDir,
		Enabled:       true,
		PythonCommand: "/usr/bin/python-custom",
		HydrusRoot:    hydrusRoot,
		Timeout:       45 * time.Second,
	})
	if err != nil {
		t.Fatalf("EnsureFreshClientBundle() error = %v", err)
	}

	if !called {
		t.Fatal("runPythonFreshClientBootstrap() was not called")
	}

	if !result.Bootstrapped {
		t.Fatal("result.Bootstrapped = false, want true")
	}

	if result.State != StateReady {
		t.Fatalf("result.State = %q, want %q", result.State, StateReady)
	}

	if result.HydrusRoot != hydrusRoot {
		t.Fatalf("result.HydrusRoot = %q, want %q", result.HydrusRoot, hydrusRoot)
	}
}

func TestEnsureFreshClientBundle_CreatesMissingDBDirWhenBootstrapEnabled(t *testing.T) {
	parentDir := t.TempDir()
	dbDir := filepath.Join(parentDir, "fresh-bundle")
	hydrusRoot := createFakeHydrusRoot(t)

	originalRunner := runPythonFreshClientBootstrap
	defer func() {
		runPythonFreshClientBootstrap = originalRunner
	}()

	runPythonFreshClientBootstrap = func(_ context.Context, request runRequest) error {
		info, err := os.Stat(request.DBDir)
		if err != nil {
			t.Fatalf("Stat(%q) error = %v", request.DBDir, err)
		}

		if !info.IsDir() {
			t.Fatalf("request.DBDir %q is not a directory", request.DBDir)
		}

		createBundleFiles(t, request.DBDir)
		return nil
	}

	result, err := EnsureFreshClientBundle(context.Background(), Options{
		DBDir:      dbDir,
		Enabled:    true,
		HydrusRoot: hydrusRoot,
		Timeout:    time.Minute,
	})
	if err != nil {
		t.Fatalf("EnsureFreshClientBundle() error = %v", err)
	}

	if !result.Bootstrapped {
		t.Fatal("result.Bootstrapped = false, want true")
	}
}

func TestEnsureFreshClientBundle_PropagatesRunnerFailure(t *testing.T) {
	dbDir := t.TempDir()
	hydrusRoot := createFakeHydrusRoot(t)

	originalRunner := runPythonFreshClientBootstrap
	defer func() {
		runPythonFreshClientBootstrap = originalRunner
	}()

	runPythonFreshClientBootstrap = func(context.Context, runRequest) error {
		return errors.New("forced python bootstrap failure")
	}

	_, err := EnsureFreshClientBundle(context.Background(), Options{
		DBDir:      dbDir,
		Enabled:    true,
		HydrusRoot: hydrusRoot,
		Timeout:    time.Minute,
	})
	if err == nil {
		t.Fatal("EnsureFreshClientBundle() error = nil, want error")
	}

	if !strings.Contains(err.Error(), "forced python bootstrap failure") {
		t.Fatalf("EnsureFreshClientBundle() error = %v, want runner failure", err)
	}
}

func TestEnsureFreshClientBundle_VerifiesRunnerCreatedBundle(t *testing.T) {
	dbDir := t.TempDir()
	hydrusRoot := createFakeHydrusRoot(t)

	originalRunner := runPythonFreshClientBootstrap
	defer func() {
		runPythonFreshClientBootstrap = originalRunner
	}()

	runPythonFreshClientBootstrap = func(context.Context, runRequest) error {
		mustWriteFile(t, filepath.Join(dbDir, "client.db"))
		return nil
	}

	_, err := EnsureFreshClientBundle(context.Background(), Options{
		DBDir:      dbDir,
		Enabled:    true,
		HydrusRoot: hydrusRoot,
		Timeout:    time.Minute,
	})
	if err == nil {
		t.Fatal("EnsureFreshClientBundle() error = nil, want error")
	}

	if !strings.Contains(err.Error(), "is not ready") {
		t.Fatalf("EnsureFreshClientBundle() error = %v, want verification failure", err)
	}
}

func TestEnsureFreshClientBundle_PartialBundleFailsWithoutRunningBootstrap(t *testing.T) {
	dbDir := t.TempDir()
	mustWriteFile(t, filepath.Join(dbDir, "client.db"))

	originalRunner := runPythonFreshClientBootstrap
	runPythonFreshClientBootstrap = func(context.Context, runRequest) error {
		t.Fatal("runPythonFreshClientBootstrap() called, want skip for partial bundle")
		return nil
	}
	defer func() {
		runPythonFreshClientBootstrap = originalRunner
	}()

	_, err := EnsureFreshClientBundle(context.Background(), Options{
		DBDir:      dbDir,
		Enabled:    true,
		HydrusRoot: createFakeHydrusRoot(t),
		Timeout:    time.Minute,
	})
	if err == nil {
		t.Fatal("EnsureFreshClientBundle() error = nil, want error")
	}

	if !strings.Contains(err.Error(), "partial client bundle") {
		t.Fatalf("EnsureFreshClientBundle() error = %v, want partial bundle error", err)
	}
}

func TestEnsureFreshClientBundle_NonEmptyDirWithoutBundleFails(t *testing.T) {
	dbDir := t.TempDir()
	mustWriteFile(t, filepath.Join(dbDir, "notes.txt"))

	_, err := EnsureFreshClientBundle(context.Background(), Options{
		DBDir:      dbDir,
		Enabled:    true,
		HydrusRoot: createFakeHydrusRoot(t),
		Timeout:    time.Minute,
	})
	if err == nil {
		t.Fatal("EnsureFreshClientBundle() error = nil, want error")
	}

	if !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("EnsureFreshClientBundle() error = %v, want non-empty directory error", err)
	}
	if !strings.Contains(err.Error(), "notes.txt") {
		t.Fatalf("EnsureFreshClientBundle() error = %v, want extra entry details", err)
	}
}

func TestResolveHydrusRoot_AutoDetectsParentOfWorkingDirectory(t *testing.T) {
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}

	hydrusRoot := createFakeHydrusRoot(t)
	hydrusGoDir := filepath.Join(hydrusRoot, "hydrus-go")
	if err := os.MkdirAll(hydrusGoDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", hydrusGoDir, err)
	}

	if err := os.Chdir(hydrusGoDir); err != nil {
		t.Fatalf("Chdir(%q) error = %v", hydrusGoDir, err)
	}
	t.Cleanup(func() {
		if restoreErr := os.Chdir(originalWD); restoreErr != nil {
			t.Fatalf("Chdir(%q) restore error = %v", originalWD, restoreErr)
		}
	})

	resolvedRoot, err := resolveHydrusRoot("")
	if err != nil {
		t.Fatalf("resolveHydrusRoot() error = %v", err)
	}

	if resolvedRoot != hydrusRoot {
		t.Fatalf("resolvedRoot = %q, want %q", resolvedRoot, hydrusRoot)
	}
}

func TestDefaultPythonCommand_IsPlatformAware(t *testing.T) {
	want := "python3"
	if runtime.GOOS == "windows" {
		want = "python"
	}

	if got := defaultPythonCommand(); got != want {
		t.Fatalf("defaultPythonCommand() = %q, want %q", got, want)
	}
}

func TestSplitCommandLine_PreservesLauncherArguments(t *testing.T) {
	args, err := splitCommandLine(`py -3`)
	if err != nil {
		t.Fatalf("splitCommandLine() error = %v", err)
	}

	if len(args) != 2 || args[0] != "py" || args[1] != "-3" {
		t.Fatalf("splitCommandLine() = %#v, want [\"py\", \"-3\"]", args)
	}
}

func TestSplitCommandLine_PreservesExistingPathWithSpaces(t *testing.T) {
	executablePath := filepath.Join(t.TempDir(), "python with spaces.exe")
	mustWriteFile(t, executablePath)

	args, err := splitCommandLine(executablePath)
	if err != nil {
		t.Fatalf("splitCommandLine() error = %v", err)
	}

	if len(args) != 1 || args[0] != executablePath {
		t.Fatalf("splitCommandLine() = %#v, want [%q]", args, executablePath)
	}
}

func createBundleFiles(t *testing.T, dir string) {
	t.Helper()

	for _, filename := range requiredBundleFiles {
		mustWriteFile(t, filepath.Join(dir, filename))
	}
}

func createFakeHydrusRoot(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "hydrus", "client", "db"), 0o755); err != nil {
		t.Fatalf("MkdirAll(fake hydrus root) error = %v", err)
	}

	mustWriteFile(t, filepath.Join(root, "hydrus_client.py"))
	mustWriteFile(t, filepath.Join(root, "hydrus", "client", "db", "ClientDB.py"))

	return root
}

func mustWriteFile(t *testing.T, path string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(path), err)
	}

	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
}
