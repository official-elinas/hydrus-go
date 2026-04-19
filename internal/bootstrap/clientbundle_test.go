package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/official-elinas/hydrus-go/internal/buildinfo"
	"github.com/official-elinas/hydrus-go/internal/core/services"
	"github.com/official-elinas/hydrus-go/internal/db/hydrusdb"
	"github.com/official-elinas/hydrus-go/internal/storage/clientfiles"
	_ "modernc.org/sqlite"
)

func TestEnsureFreshClientBundle_ReadyBundleSkipsBootstrap(t *testing.T) {
	dbDir := t.TempDir()
	createReadyNativeBundle(t, dbDir)

	originalCreate := createFreshClientBundle
	createFreshClientBundle = func(context.Context, string) error {
		t.Fatal("createFreshClientBundle() called, want skip for existing bundle")
		return nil
	}
	defer func() {
		createFreshClientBundle = originalCreate
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

	if !strings.Contains(err.Error(), "HYDRUS_GO_ENABLE_FRESH_CLIENT_BOOTSTRAP") {
		t.Fatalf("EnsureFreshClientBundle() error = %v, want env guidance", err)
	}

	if !strings.Contains(err.Error(), "--bootstrap-fresh-client") {
		t.Fatalf("EnsureFreshClientBundle() error = %v, want runtime flag guidance", err)
	}
}

func TestEnsureFreshClientBundle_EmptyDirBootstrapsAndVerifies(t *testing.T) {
	dbDir := t.TempDir()

	originalCreate := createFreshClientBundle
	originalVerify := verifyFreshClientBundle
	defer func() {
		createFreshClientBundle = originalCreate
		verifyFreshClientBundle = originalVerify
	}()

	called := false
	createFreshClientBundle = func(_ context.Context, dir string) error {
		called = true

		if dir != dbDir {
			t.Fatalf("dir = %q, want %q", dir, dbDir)
		}

		mustWriteFile(t, filepath.Join(dir, "client.db"))
		mustWriteFile(t, filepath.Join(dir, "client.master.db"))
		mustWriteFile(t, filepath.Join(dir, "client.caches.db"))
		mustWriteFile(t, filepath.Join(dir, "client.mappings.db"))
		return nil
	}
	verifyFreshClientBundle = func(_ context.Context, dir string) error {
		if dir != dbDir {
			t.Fatalf("verify dir = %q, want %q", dir, dbDir)
		}

		return nil
	}

	result, err := EnsureFreshClientBundle(context.Background(), Options{
		DBDir:   dbDir,
		Enabled: true,
		Timeout: 45 * time.Second,
	})
	if err != nil {
		t.Fatalf("EnsureFreshClientBundle() error = %v", err)
	}

	if !called {
		t.Fatal("createFreshClientBundle() was not called")
	}

	if !result.Bootstrapped {
		t.Fatal("result.Bootstrapped = false, want true")
	}

	if result.State != StateReady {
		t.Fatalf("result.State = %q, want %q", result.State, StateReady)
	}
}

func TestEnsureFreshClientBundle_CreatesMissingDBDirWhenBootstrapEnabled(t *testing.T) {
	parentDir := t.TempDir()
	dbDir := filepath.Join(parentDir, "fresh-bundle")

	originalCreate := createFreshClientBundle
	originalVerify := verifyFreshClientBundle
	defer func() {
		createFreshClientBundle = originalCreate
		verifyFreshClientBundle = originalVerify
	}()

	createFreshClientBundle = func(_ context.Context, dir string) error {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("Stat(%q) error = %v", dir, err)
		}

		if !info.IsDir() {
			t.Fatalf("dir %q is not a directory", dir)
		}

		mustWriteFile(t, filepath.Join(dir, "client.db"))
		mustWriteFile(t, filepath.Join(dir, "client.master.db"))
		mustWriteFile(t, filepath.Join(dir, "client.caches.db"))
		mustWriteFile(t, filepath.Join(dir, "client.mappings.db"))
		return nil
	}
	verifyFreshClientBundle = func(context.Context, string) error { return nil }

	result, err := EnsureFreshClientBundle(context.Background(), Options{
		DBDir:   dbDir,
		Enabled: true,
		Timeout: time.Minute,
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

	originalCreate := createFreshClientBundle
	defer func() {
		createFreshClientBundle = originalCreate
	}()

	createFreshClientBundle = func(context.Context, string) error {
		return errors.New("forced native bootstrap failure")
	}

	_, err := EnsureFreshClientBundle(context.Background(), Options{
		DBDir:   dbDir,
		Enabled: true,
		Timeout: time.Minute,
	})
	if err == nil {
		t.Fatal("EnsureFreshClientBundle() error = nil, want error")
	}

	if !strings.Contains(err.Error(), "forced native bootstrap failure") {
		t.Fatalf("EnsureFreshClientBundle() error = %v, want runner failure", err)
	}
}

func TestEnsureFreshClientBundle_VerifiesRunnerCreatedBundle(t *testing.T) {
	dbDir := t.TempDir()

	originalCreate := createFreshClientBundle
	defer func() {
		createFreshClientBundle = originalCreate
	}()

	createFreshClientBundle = func(context.Context, string) error {
		mustWriteFile(t, filepath.Join(dbDir, "client.db"))
		return nil
	}

	_, err := EnsureFreshClientBundle(context.Background(), Options{
		DBDir:   dbDir,
		Enabled: true,
		Timeout: time.Minute,
	})
	if err == nil {
		t.Fatal("EnsureFreshClientBundle() error = nil, want error")
	}

	if !strings.Contains(err.Error(), "is not ready") {
		t.Fatalf("EnsureFreshClientBundle() error = %v, want verification failure", err)
	}
}

func TestEnsureFreshClientBundle_CleansUpFailedSemanticVerification(t *testing.T) {
	dbDir := t.TempDir()

	originalCreate := createFreshClientBundle
	originalVerify := verifyFreshClientBundle
	defer func() {
		createFreshClientBundle = originalCreate
		verifyFreshClientBundle = originalVerify
	}()

	createFreshClientBundle = func(_ context.Context, dir string) error {
		for _, filename := range append(requiredBundleFiles, "client.temp.db") {
			mustWriteFile(t, filepath.Join(dir, filename))
		}

		if err := os.MkdirAll(filepath.Join(dir, "client_files"), 0o755); err != nil {
			t.Fatalf("MkdirAll(client_files) error = %v", err)
		}

		return nil
	}
	verifyFreshClientBundle = func(context.Context, string) error {
		return errors.New("forced semantic verification failure")
	}

	_, err := EnsureFreshClientBundle(context.Background(), Options{
		DBDir:   dbDir,
		Enabled: true,
		Timeout: time.Minute,
	})
	if err == nil {
		t.Fatal("EnsureFreshClientBundle() error = nil, want error")
	}

	if !strings.Contains(err.Error(), "forced semantic verification failure") {
		t.Fatalf("EnsureFreshClientBundle() error = %v, want verification failure", err)
	}

	observed, inspectErr := inspectBundleDir(dbDir)
	if inspectErr != nil {
		t.Fatalf("inspectBundleDir() error = %v", inspectErr)
	}

	if observed.State != StateEmpty {
		t.Fatalf("observed.State = %q, want %q after cleanup", observed.State, StateEmpty)
	}

	if _, statErr := os.Stat(filepath.Join(dbDir, "client.temp.db")); !os.IsNotExist(statErr) {
		t.Fatalf("client.temp.db stat error = %v, want not exists", statErr)
	}

	if _, statErr := os.Stat(filepath.Join(dbDir, "client_files")); !os.IsNotExist(statErr) {
		t.Fatalf("client_files stat error = %v, want not exists", statErr)
	}
}

func TestEnsureFreshClientBundle_PartialBundleFailsWithoutRunningBootstrap(t *testing.T) {
	dbDir := t.TempDir()
	mustWriteFile(t, filepath.Join(dbDir, "client.db"))

	originalCreate := createFreshClientBundle
	createFreshClientBundle = func(context.Context, string) error {
		t.Fatal("createFreshClientBundle() called, want skip for partial bundle")
		return nil
	}
	defer func() {
		createFreshClientBundle = originalCreate
	}()

	_, err := EnsureFreshClientBundle(context.Background(), Options{
		DBDir:   dbDir,
		Enabled: true,
		Timeout: time.Minute,
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
		DBDir:   dbDir,
		Enabled: true,
		Timeout: time.Minute,
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

func TestEnsureFreshClientBundle_NativeBootstrapCreatesUsableBundle(t *testing.T) {
	dbDir := t.TempDir()

	result, err := EnsureFreshClientBundle(context.Background(), Options{
		DBDir:   dbDir,
		Enabled: true,
		Timeout: time.Minute,
	})
	if err != nil {
		t.Fatalf("EnsureFreshClientBundle() error = %v", err)
	}

	if !result.Bootstrapped {
		t.Fatal("result.Bootstrapped = false, want true")
	}

	if result.State != StateReady {
		t.Fatalf("result.State = %q, want %q", result.State, StateReady)
	}

	for _, filename := range append(requiredBundleFiles, "client.temp.db") {
		info, err := os.Stat(filepath.Join(dbDir, filename))
		if err != nil {
			t.Fatalf("Stat(%q) error = %v", filename, err)
		}

		if info.IsDir() {
			t.Fatalf("%q is a directory, want file", filename)
		}
	}

	clientFilesRoot := clientfiles.DefaultFileRoot(dbDir)
	thumbnailRoot := clientfiles.DefaultThumbnailRoot(dbDir)
	storedClientFilesRoot := portableStorageLocation(dbDir, clientFilesRoot)
	storedThumbnailRoot := portableStorageLocation(dbDir, thumbnailRoot)
	info, err := os.Stat(clientFilesRoot)
	if err != nil {
		t.Fatalf("Stat(client_files) error = %v", err)
	}

	if !info.IsDir() {
		t.Fatalf("client_files root %q is not a directory", clientFilesRoot)
	}

	thumbnailInfo, err := os.Stat(thumbnailRoot)
	if err != nil {
		t.Fatalf("Stat(thumbnails) error = %v", err)
	}

	if !thumbnailInfo.IsDir() {
		t.Fatalf("thumbnails root %q is not a directory", thumbnailRoot)
	}

	mainDB, err := sql.Open("sqlite", filepath.Join(dbDir, "client.db"))
	if err != nil {
		t.Fatalf("sql.Open(client.db) error = %v", err)
	}
	defer mainDB.Close()

	var version int
	if err := mainDB.QueryRow(`SELECT version FROM version LIMIT 1`).Scan(&version); err != nil {
		t.Fatalf("QueryRow(version) error = %v", err)
	}

	if version != buildinfo.ReferenceHydrusVersion {
		t.Fatalf("version = %d, want %d", version, buildinfo.ReferenceHydrusVersion)
	}

	var serviceCount int
	if err := mainDB.QueryRow(`SELECT COUNT(*) FROM services`).Scan(&serviceCount); err != nil {
		t.Fatalf("QueryRow(service count) error = %v", err)
	}

	if serviceCount != len(services.BootstrapCatalog()) {
		t.Fatalf("service count = %d, want %d", serviceCount, len(services.BootstrapCatalog()))
	}

	var visibleServiceCount int
	if err := mainDB.QueryRow(
		`SELECT COUNT(*) FROM services WHERE name IN (?, ?)`,
		"downloader tags",
		"favourites",
	).Scan(&visibleServiceCount); err != nil {
		t.Fatalf("QueryRow(visible service count) error = %v", err)
	}

	if visibleServiceCount != 2 {
		t.Fatalf("visible service count = %d, want 2", visibleServiceCount)
	}

	var hiddenServiceCount int
	if err := mainDB.QueryRow(
		`SELECT COUNT(*) FROM services WHERE name IN (?, ?, ?)`,
		"deleted from anywhere",
		"local notes",
		"client api",
	).Scan(&hiddenServiceCount); err != nil {
		t.Fatalf("QueryRow(hidden service count) error = %v", err)
	}

	if hiddenServiceCount != 3 {
		t.Fatalf("hidden service count = %d, want 3", hiddenServiceCount)
	}

	var favouritesDictionary string
	if err := mainDB.QueryRow(
		`SELECT dictionary_string FROM services WHERE name = ? LIMIT 1`,
		"favourites",
	).Scan(&favouritesDictionary); err != nil {
		t.Fatalf("QueryRow(favourites dictionary) error = %v", err)
	}

	if favouritesDictionary != favouritesServiceDictionaryString {
		t.Fatalf("favourites dictionary = %q, want %q", favouritesDictionary, favouritesServiceDictionaryString)
	}

	readBundle, err := hydrusdb.Open(context.Background(), dbDir)
	if err != nil {
		t.Fatalf("hydrusdb.Open() error = %v", err)
	}
	defer func() {
		if closeErr := readBundle.Close(); closeErr != nil {
			t.Fatalf("readBundle.Close() error = %v", closeErr)
		}
	}()

	for _, hiddenServiceName := range []string{
		"deleted from anywhere",
		"local notes",
		"client api",
	} {
		service, ok, lookupErr := readBundle.ByName(context.Background(), hiddenServiceName)
		if lookupErr != nil {
			t.Fatalf("readBundle.ByName(%q) error = %v", hiddenServiceName, lookupErr)
		}

		if !ok {
			t.Fatalf("readBundle.ByName(%q) ok = false, want true", hiddenServiceName)
		}

		if service.Name != hiddenServiceName {
			t.Fatalf("service.Name = %q, want %q", service.Name, hiddenServiceName)
		}
	}

	var granularity int
	if err := mainDB.QueryRow(`SELECT granularity FROM current_storage_granularity LIMIT 1`).Scan(&granularity); err != nil {
		t.Fatalf("QueryRow(storage granularity) error = %v", err)
	}

	if granularity != clientfiles.DefaultPrefixLength {
		t.Fatalf(
			"storage granularity = %d, want %d",
			granularity,
			clientfiles.DefaultPrefixLength,
		)
	}

	var fileLocation string
	if err := mainDB.QueryRow(
		`SELECT cfl.location
		FROM current_client_files_locations cfl
		JOIN client_files_subfolders cfs USING (location_id)
		WHERE cfs.prefix LIKE 'f%'
		ORDER BY cfs.rowid ASC
		LIMIT 1`,
	).Scan(&fileLocation); err != nil {
		t.Fatalf("QueryRow(file location) error = %v", err)
	}

	if fileLocation != storedClientFilesRoot {
		t.Fatalf("file location = %q, want %q", fileLocation, storedClientFilesRoot)
	}

	var thumbnailLocation string
	if err := mainDB.QueryRow(
		`SELECT cfl.location
		FROM current_client_files_locations cfl
		JOIN client_files_subfolders cfs USING (location_id)
		WHERE cfs.prefix LIKE 't%'
		ORDER BY cfs.rowid ASC
		LIMIT 1`,
	).Scan(&thumbnailLocation); err != nil {
		t.Fatalf("QueryRow(thumbnail location) error = %v", err)
	}

	if thumbnailLocation != storedThumbnailRoot {
		t.Fatalf("thumbnail location = %q, want %q", thumbnailLocation, storedThumbnailRoot)
	}

	var prefixCount int
	if err := mainDB.QueryRow(`SELECT COUNT(*) FROM client_files_subfolders`).Scan(&prefixCount); err != nil {
		t.Fatalf("QueryRow(client-files prefix count) error = %v", err)
	}

	wantPrefixCount := expectedPrefixCount(clientfiles.DefaultPrefixLength) * 2
	if prefixCount != wantPrefixCount {
		t.Fatalf("client-files prefix count = %d, want %d", prefixCount, wantPrefixCount)
	}

	var locationCount int
	if err := mainDB.QueryRow(`SELECT COUNT(*) FROM current_client_files_locations`).Scan(&locationCount); err != nil {
		t.Fatalf("QueryRow(location count) error = %v", err)
	}

	if locationCount != 2 {
		t.Fatalf("location count = %d, want 2", locationCount)
	}

	var thumbnailOverrideLocation string
	if err := mainDB.QueryRow(
		`SELECT cfl.location
		FROM ideal_thumbnail_override_location itol
		JOIN current_client_files_locations cfl USING (location_id)
		LIMIT 1`,
	).Scan(&thumbnailOverrideLocation); err != nil {
		t.Fatalf("QueryRow(ideal thumbnail override) error = %v", err)
	}

	if thumbnailOverrideLocation != storedThumbnailRoot {
		t.Fatalf(
			"ideal thumbnail override = %q, want %q",
			thumbnailOverrideLocation,
			storedThumbnailRoot,
		)
	}
}

func createReadyNativeBundle(t *testing.T, dir string) {
	t.Helper()

	if err := defaultCreateFreshClientBundle(context.Background(), dir); err != nil {
		t.Fatalf("defaultCreateFreshClientBundle() error = %v", err)
	}
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
