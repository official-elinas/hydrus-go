package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/official-elinas/hydrus-go/internal/bootstrap"
	"github.com/official-elinas/hydrus-go/internal/config"
	"github.com/official-elinas/hydrus-go/internal/db/hydrusdb"
	"github.com/official-elinas/hydrus-go/internal/storage/clientfiles"
)

func TestRun_ShutsDownWhenContextIsCanceled(t *testing.T) {
	cfg := config.Config{
		ListenAddr:               "127.0.0.1:0",
		AccessName:               "test-client",
		LogLevel:                 "error",
		ShutdownTimeout:          time.Second,
		AllowNonLocalConnections: false,
		EnableCORS:               false,
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	application, err := New(context.Background(), cfg, logger)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- application.Run(ctx)
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for App.Run to stop")
	}
}

func TestNew_OpensConfiguredDBBundle(t *testing.T) {
	dbDir := t.TempDir()
	createEmptySQLiteDB(t, filepath.Join(dbDir, "client.db"))
	createEmptySQLiteDB(t, filepath.Join(dbDir, "client.master.db"))
	createEmptySQLiteDB(t, filepath.Join(dbDir, "client.caches.db"))
	createEmptySQLiteDB(t, filepath.Join(dbDir, "client.mappings.db"))

	cfg := config.Config{
		ListenAddr:               "127.0.0.1:0",
		DBDir:                    dbDir,
		AccessName:               "test-client",
		LogLevel:                 "error",
		ShutdownTimeout:          time.Second,
		AllowNonLocalConnections: false,
		EnableCORS:               false,
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	application, err := New(context.Background(), cfg, logger)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer application.closeResources()

	if application.readBundle == nil {
		t.Fatal("application.readBundle = nil, want opened read bundle")
	}

	if application.writeBundle == nil {
		t.Fatal("application.writeBundle = nil, want opened write bundle")
	}
}

func TestNew_BootstrapsEmptyConfiguredDBBundleWhenEnabled(t *testing.T) {
	dbDir := t.TempDir()

	originalEnsureFreshClientBundle := ensureFreshClientBundle
	ensureFreshClientBundle = func(
		_ context.Context,
		options bootstrap.Options,
	) (bootstrap.Result, error) {
		if options.DBDir != dbDir {
			t.Fatalf("options.DBDir = %q, want %q", options.DBDir, dbDir)
		}

		if !options.Enabled {
			t.Fatal("options.Enabled = false, want true")
		}

		if options.Timeout != time.Minute {
			t.Fatalf("options.Timeout = %v, want %v", options.Timeout, time.Minute)
		}

		createEmptySQLiteDB(t, filepath.Join(options.DBDir, "client.db"))
		createEmptySQLiteDB(t, filepath.Join(options.DBDir, "client.master.db"))
		createEmptySQLiteDB(t, filepath.Join(options.DBDir, "client.caches.db"))
		createEmptySQLiteDB(t, filepath.Join(options.DBDir, "client.mappings.db"))

		return bootstrap.Result{Bootstrapped: true}, nil
	}
	defer func() {
		ensureFreshClientBundle = originalEnsureFreshClientBundle
	}()

	cfg := config.Config{
		ListenAddr:                 "127.0.0.1:0",
		DBDir:                      dbDir,
		EnableFreshClientBootstrap: true,
		BootstrapTimeout:           time.Minute,
		AccessName:                 "test-client",
		LogLevel:                   "error",
		ShutdownTimeout:            time.Second,
		AllowNonLocalConnections:   false,
		EnableCORS:                 false,
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	application, err := New(context.Background(), cfg, logger)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer application.closeResources()

	if application.readBundle == nil {
		t.Fatal("application.readBundle = nil, want opened read bundle")
	}

	if application.writeBundle == nil {
		t.Fatal("application.writeBundle = nil, want opened write bundle")
	}
}

func TestNew_RejectsEmptyConfiguredDBBundleWhenBootstrapDisabled(t *testing.T) {
	cfg := config.Config{
		ListenAddr:               "127.0.0.1:0",
		DBDir:                    t.TempDir(),
		AccessName:               "test-client",
		LogLevel:                 "error",
		ShutdownTimeout:          time.Second,
		AllowNonLocalConnections: false,
		EnableCORS:               false,
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	_, err := New(context.Background(), cfg, logger)
	if err == nil {
		t.Fatal("New() error = nil, want error")
	}

	if !strings.Contains(err.Error(), "fresh-client bootstrap is disabled") {
		t.Fatalf("New() error = %v, want bootstrap guidance", err)
	}
}

func TestNew_PropagatesBootstrapPreparationFailure(t *testing.T) {
	dbDir := t.TempDir()

	originalEnsureFreshClientBundle := ensureFreshClientBundle
	ensureFreshClientBundle = func(context.Context, bootstrap.Options) (bootstrap.Result, error) {
		return bootstrap.Result{}, errors.New("forced bootstrap failure")
	}
	defer func() {
		ensureFreshClientBundle = originalEnsureFreshClientBundle
	}()

	cfg := config.Config{
		ListenAddr:                 "127.0.0.1:0",
		DBDir:                      dbDir,
		EnableFreshClientBootstrap: true,
		BootstrapTimeout:           time.Minute,
		AccessName:                 "test-client",
		LogLevel:                   "error",
		ShutdownTimeout:            time.Second,
		AllowNonLocalConnections:   false,
		EnableCORS:                 false,
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	_, err := New(context.Background(), cfg, logger)
	if err == nil {
		t.Fatal("New() error = nil, want error")
	}

	if !strings.Contains(err.Error(), "prepare hydrus DB bundle: forced bootstrap failure") {
		t.Fatalf("New() error = %v, want wrapped bootstrap failure", err)
	}
}

func TestNew_PropagatesReadBundleFailureAfterBootstrap(t *testing.T) {
	dbDir := t.TempDir()

	originalEnsureFreshClientBundle := ensureFreshClientBundle
	ensureFreshClientBundle = func(context.Context, bootstrap.Options) (bootstrap.Result, error) {
		return bootstrap.Result{Bootstrapped: true}, nil
	}
	defer func() {
		ensureFreshClientBundle = originalEnsureFreshClientBundle
	}()

	originalOpenReadBundle := openReadBundle
	openReadBundle = func(context.Context, string) (*hydrusdb.Bundle, error) {
		return nil, errors.New("forced read open failure")
	}
	defer func() {
		openReadBundle = originalOpenReadBundle
	}()

	cfg := config.Config{
		ListenAddr:                 "127.0.0.1:0",
		DBDir:                      dbDir,
		EnableFreshClientBootstrap: true,
		BootstrapTimeout:           time.Minute,
		AccessName:                 "test-client",
		LogLevel:                   "error",
		ShutdownTimeout:            time.Second,
		AllowNonLocalConnections:   false,
		EnableCORS:                 false,
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	_, err := New(context.Background(), cfg, logger)
	if err == nil {
		t.Fatal("New() error = nil, want error")
	}

	if !strings.Contains(err.Error(), "open hydrus DB bundle: forced read open failure") {
		t.Fatalf("New() error = %v, want wrapped read open failure", err)
	}
}

func TestNew_AllowsReadOnlyBundleWhenWritableOpenFails(t *testing.T) {
	dbDir := createThinClientBundle(t)

	originalOpenWriteBundle := openWriteBundle
	openWriteBundle = func(context.Context, string) (*hydrusdb.Bundle, error) {
		return nil, errors.New("forced write open failure")
	}
	defer func() {
		openWriteBundle = originalOpenWriteBundle
	}()

	cfg := config.Config{
		ListenAddr:               "127.0.0.1:0",
		DBDir:                    dbDir,
		AccessName:               "test-client",
		LogLevel:                 "error",
		ShutdownTimeout:          time.Second,
		AllowNonLocalConnections: false,
		EnableCORS:               false,
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	application, err := New(context.Background(), cfg, logger)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer application.closeResources()

	if application.readBundle == nil {
		t.Fatal("application.readBundle = nil, want opened read bundle")
	}

	if application.writeBundle != nil {
		t.Fatal("application.writeBundle != nil, want read-only degraded mode")
	}

	req := httptest.NewRequest(http.MethodGet, "/get_services", nil)
	req.Header.Set("Hydrus-Client-API-Access-Key", application.access.AccessKey())
	rr := httptest.NewRecorder()

	application.server.Handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("get_services status = %d, want %d", rr.Code, http.StatusOK)
	}

	importReq := httptest.NewRequest(
		http.MethodPost,
		"/v1/files/trash",
		strings.NewReader(`{"file_id":1}`),
	)
	importReq.Header.Set("Hydrus-Client-API-Access-Key", application.access.AccessKey())
	importRR := httptest.NewRecorder()

	application.server.Handler.ServeHTTP(importRR, importReq)

	if importRR.Code != http.StatusForbidden {
		t.Fatalf("trash status = %d, want %d", importRR.Code, http.StatusForbidden)
	}
}

func TestApp_DBBackedImportRoundTripEndpoints(t *testing.T) {
	dbDir := createThinClientBundle(t)
	sourcePath := writeAppPNGSourceFile(t, t.TempDir(), "app-import.png", 16, 24)

	cfg := config.Config{
		ListenAddr:               "127.0.0.1:0",
		DBDir:                    dbDir,
		AccessKey:                strings.Repeat("a", 64),
		AccessName:               "test-client",
		LogLevel:                 "error",
		ShutdownTimeout:          time.Second,
		AllowNonLocalConnections: false,
		EnableCORS:               false,
	}

	runImportRoundTripEndpointsTest(t, cfg, sourcePath, false)
}

func TestApp_NativeBootstrapImportRoundTripEndpoints(t *testing.T) {
	dbDir := filepath.Join(t.TempDir(), "fresh-bundle")
	sourcePath := writeAppPNGSourceFile(t, t.TempDir(), "app-native-bootstrap-import.png", 16, 24)

	cfg := config.Config{
		ListenAddr:                 "127.0.0.1:0",
		DBDir:                      dbDir,
		EnableFreshClientBootstrap: true,
		BootstrapTimeout:           time.Minute,
		AccessKey:                  strings.Repeat("b", 64),
		AccessName:                 "test-client",
		LogLevel:                   "error",
		ShutdownTimeout:            time.Second,
		AllowNonLocalConnections:   false,
		EnableCORS:                 false,
	}

	runImportRoundTripEndpointsTest(t, cfg, sourcePath, true)
}

func runImportRoundTripEndpointsTest(
	t *testing.T,
	cfg config.Config,
	sourcePath string,
	expectBootstrapDiscovery bool,
) {
	t.Helper()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	application, err := New(context.Background(), cfg, logger)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer application.closeResources()

	servicesReq := httptest.NewRequest(http.MethodGet, "/get_services", nil)
	servicesReq.Header.Set("Hydrus-Client-API-Access-Key", cfg.AccessKey)
	servicesRR := httptest.NewRecorder()

	application.server.Handler.ServeHTTP(servicesRR, servicesReq)

	if servicesRR.Code != http.StatusOK {
		t.Fatalf("get_services status = %d, want %d", servicesRR.Code, http.StatusOK)
	}

	assertAppServiceDiscovery(t, servicesRR.Body.Bytes(), expectBootstrapDiscovery)
	assertAppGetServiceLookup(
		t,
		application.server.Handler,
		cfg.AccessKey,
		"/get_service?service_name=My%20FiLeS",
		http.StatusOK,
		"my files",
	)
	assertAppGetServiceError(
		t,
		application.server.Handler,
		cfg.AccessKey,
		"/get_service?service_key=636c69656e7420617069",
		http.StatusBadRequest,
		"service exists but is not available through this endpoint",
	)
	assertAppGetServiceError(
		t,
		application.server.Handler,
		cfg.AccessKey,
		"/get_service?service_name=client%20api",
		http.StatusNotFound,
		"service not found",
	)

	recentReq := httptest.NewRequest(http.MethodGet, "/v1/library/recent?limit=10", nil)
	recentReq.Header.Set("Hydrus-Client-API-Access-Key", cfg.AccessKey)
	recentRR := httptest.NewRecorder()

	application.server.Handler.ServeHTTP(recentRR, recentReq)

	if recentRR.Code != http.StatusOK {
		t.Fatalf("initial recent status = %d, want %d", recentRR.Code, http.StatusOK)
	}

	var recentPayload map[string]any
	decodeAppJSON(t, recentRR.Body.Bytes(), &recentPayload)
	items := recentPayload["items"].([]any)
	if len(items) != 0 {
		t.Fatalf("len(initial items) = %d, want 0", len(items))
	}

	importReq := newAppMultipartUploadRequest(t, "/v1/import/upload", sourcePath)
	importReq.Header.Set("Hydrus-Client-API-Access-Key", cfg.AccessKey)
	importRR := httptest.NewRecorder()

	application.server.Handler.ServeHTTP(importRR, importReq)

	if importRR.Code != http.StatusOK {
		t.Fatalf("import status = %d, want %d", importRR.Code, http.StatusOK)
	}

	var importPayload map[string]any
	decodeAppJSON(t, importRR.Body.Bytes(), &importPayload)

	fileID := int64(importPayload["file_id"].(float64))
	if fileID <= 0 {
		t.Fatalf("file_id = %d, want > 0", fileID)
	}

	recentReq = httptest.NewRequest(http.MethodGet, "/v1/library/recent?limit=10", nil)
	recentReq.Header.Set("Hydrus-Client-API-Access-Key", cfg.AccessKey)
	recentRR = httptest.NewRecorder()

	application.server.Handler.ServeHTTP(recentRR, recentReq)

	if recentRR.Code != http.StatusOK {
		t.Fatalf("recent status = %d, want %d", recentRR.Code, http.StatusOK)
	}

	decodeAppJSON(t, recentRR.Body.Bytes(), &recentPayload)
	items = recentPayload["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}

	item := items[0].(map[string]any)
	if int64(item["file_id"].(float64)) != fileID {
		t.Fatalf("recent file_id = %v, want %d", item["file_id"], fileID)
	}

	if item["has_thumbnail"] != true {
		t.Fatalf("recent has_thumbnail = %v, want true", item["has_thumbnail"])
	}

	thumbnailReq := httptest.NewRequest(
		http.MethodGet,
		item["thumbnail_url"].(string),
		nil,
	)
	thumbnailReq.Header.Set("Hydrus-Client-API-Access-Key", cfg.AccessKey)
	thumbnailRR := httptest.NewRecorder()

	application.server.Handler.ServeHTTP(thumbnailRR, thumbnailReq)

	if thumbnailRR.Code != http.StatusOK {
		t.Fatalf("thumbnail status = %d, want %d", thumbnailRR.Code, http.StatusOK)
	}

	if thumbnailRR.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("thumbnail Content-Type = %q, want image/png", thumbnailRR.Header().Get("Content-Type"))
	}

	if len(thumbnailRR.Body.Bytes()) == 0 {
		t.Fatal("thumbnail body is empty, want managed preview bytes")
	}

	metadataReq := httptest.NewRequest(
		http.MethodGet,
		"/get_files/file_metadata?file_id="+strconv.FormatInt(fileID, 10)+"&only_return_basic_information=true",
		nil,
	)
	metadataReq.Header.Set("Hydrus-Client-API-Access-Key", cfg.AccessKey)
	metadataRR := httptest.NewRecorder()

	application.server.Handler.ServeHTTP(metadataRR, metadataReq)

	if metadataRR.Code != http.StatusOK {
		t.Fatalf("metadata status = %d, want %d", metadataRR.Code, http.StatusOK)
	}

	var metadataPayload map[string]any
	decodeAppJSON(t, metadataRR.Body.Bytes(), &metadataPayload)
	metadataRows := metadataPayload["metadata"].([]any)
	metadataRow := metadataRows[0].(map[string]any)
	if int64(metadataRow["file_id"].(float64)) != fileID {
		t.Fatalf("metadata file_id = %v, want %d", metadataRow["file_id"], fileID)
	}

	if metadataRow["mime"] != "image/png" {
		t.Fatalf("metadata mime = %v, want image/png", metadataRow["mime"])
	}

	contentReq := httptest.NewRequest(
		http.MethodGet,
		importPayload["content_url"].(string),
		nil,
	)
	contentReq.Header.Set("Hydrus-Client-API-Access-Key", cfg.AccessKey)
	contentRR := httptest.NewRecorder()

	application.server.Handler.ServeHTTP(contentRR, contentReq)

	if contentRR.Code != http.StatusOK {
		t.Fatalf("content status = %d, want %d", contentRR.Code, http.StatusOK)
	}

	if contentRR.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("Content-Type = %q, want image/png", contentRR.Header().Get("Content-Type"))
	}

	sourceBytes, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatalf("ReadFile(sourcePath) error = %v", err)
	}

	if string(contentRR.Body.Bytes()) != string(sourceBytes) {
		t.Fatal("content body does not match imported source bytes")
	}

	trashBody, err := json.Marshal(map[string]int64{"file_id": fileID})
	if err != nil {
		t.Fatalf("json.Marshal(trashBody) error = %v", err)
	}

	trashReq := httptest.NewRequest(
		http.MethodPost,
		"/v1/files/trash",
		strings.NewReader(string(trashBody)),
	)
	trashReq.Header.Set("Hydrus-Client-API-Access-Key", cfg.AccessKey)
	trashRR := httptest.NewRecorder()

	application.server.Handler.ServeHTTP(trashRR, trashReq)

	if trashRR.Code != http.StatusOK {
		t.Fatalf("trash status = %d, want %d", trashRR.Code, http.StatusOK)
	}

	recentReq = httptest.NewRequest(http.MethodGet, "/v1/library/recent?limit=10", nil)
	recentReq.Header.Set("Hydrus-Client-API-Access-Key", cfg.AccessKey)
	recentRR = httptest.NewRecorder()

	application.server.Handler.ServeHTTP(recentRR, recentReq)

	if recentRR.Code != http.StatusOK {
		t.Fatalf("recent after trash status = %d, want %d", recentRR.Code, http.StatusOK)
	}

	decodeAppJSON(t, recentRR.Body.Bytes(), &recentPayload)
	items = recentPayload["items"].([]any)
	if len(items) != 0 {
		t.Fatalf("len(items) after trash = %d, want 0", len(items))
	}

	metadataReq = httptest.NewRequest(
		http.MethodGet,
		"/get_files/file_metadata?file_id="+strconv.FormatInt(fileID, 10),
		nil,
	)
	metadataReq.Header.Set("Hydrus-Client-API-Access-Key", cfg.AccessKey)
	metadataRR = httptest.NewRecorder()

	application.server.Handler.ServeHTTP(metadataRR, metadataReq)

	if metadataRR.Code != http.StatusOK {
		t.Fatalf("metadata after trash status = %d, want %d", metadataRR.Code, http.StatusOK)
	}

	decodeAppJSON(t, metadataRR.Body.Bytes(), &metadataPayload)
	metadataRows = metadataPayload["metadata"].([]any)
	metadataRow = metadataRows[0].(map[string]any)
	if metadataRow["is_trashed"] != true {
		t.Fatalf("metadata is_trashed = %v, want true", metadataRow["is_trashed"])
	}
}

func createEmptySQLiteDB(t *testing.T, path string) {
	t.Helper()

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open(%q) error = %v", path, err)
	}
	defer db.Close()

	if _, err := db.Exec(`PRAGMA user_version = 0;`); err != nil {
		t.Fatalf("Exec(PRAGMA user_version) error = %v", err)
	}
}

func createThinClientBundle(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	mainPath := filepath.Join(dir, "client.db")
	masterPath := filepath.Join(dir, "client.master.db")
	cachesPath := filepath.Join(dir, "client.caches.db")
	mappingsPath := filepath.Join(dir, "client.mappings.db")

	mainDB, err := sql.Open("sqlite", mainPath)
	if err != nil {
		t.Fatalf("sql.Open(main) error = %v", err)
	}
	defer mainDB.Close()

	mustExecApp(t, mainDB, `
		CREATE TABLE services (
			service_id INTEGER PRIMARY KEY AUTOINCREMENT,
			service_key BLOB UNIQUE,
			service_type INTEGER,
			name TEXT,
			dictionary_string TEXT
		);
	`)
	mustExecApp(t, mainDB, `
		CREATE TABLE files_info (
			hash_id INTEGER PRIMARY KEY,
			size INTEGER,
			mime INTEGER,
			width INTEGER,
			height INTEGER,
			duration INTEGER,
			num_frames INTEGER,
			has_audio INTEGER,
			num_words INTEGER
		);
	`)
	mustExecApp(t, mainDB, `CREATE TABLE files_info_forced_filetypes (hash_id INTEGER PRIMARY KEY, forced_mime INTEGER);`)
	mustExecApp(t, mainDB, `CREATE TABLE file_inbox (hash_id INTEGER PRIMARY KEY);`)
	mustExecApp(t, mainDB, `CREATE TABLE archive_timestamps (hash_id INTEGER PRIMARY KEY, archived_timestamp_ms INTEGER);`)
	mustExecApp(t, mainDB, `CREATE TABLE file_modified_timestamps (hash_id INTEGER PRIMARY KEY, file_modified_timestamp_ms INTEGER);`)
	mustExecApp(t, mainDB, `CREATE TABLE file_domain_modified_timestamps (hash_id INTEGER, domain_id INTEGER, file_modified_timestamp_ms INTEGER, PRIMARY KEY (hash_id, domain_id));`)
	mustExecApp(t, mainDB, `CREATE TABLE url_map (hash_id INTEGER, url_id INTEGER, PRIMARY KEY (hash_id, url_id));`)
	mustExecApp(t, mainDB, `CREATE TABLE service_filenames (service_id INTEGER, hash_id INTEGER, filename TEXT, PRIMARY KEY (service_id, hash_id));`)
	mustExecApp(t, mainDB, `CREATE TABLE pixel_hash_map (hash_id INTEGER, pixel_hash_id INTEGER, PRIMARY KEY (hash_id, pixel_hash_id));`)
	mustExecApp(t, mainDB, `CREATE TABLE has_transparency (hash_id INTEGER PRIMARY KEY);`)
	mustExecApp(t, mainDB, `CREATE TABLE has_exif (hash_id INTEGER PRIMARY KEY);`)
	mustExecApp(t, mainDB, `CREATE TABLE has_human_readable_embedded_metadata (hash_id INTEGER PRIMARY KEY);`)
	mustExecApp(t, mainDB, `CREATE TABLE has_icc_profile (hash_id INTEGER PRIMARY KEY);`)
	mustExecApp(t, mainDB, `CREATE TABLE current_client_files_locations (location_id INTEGER PRIMARY KEY, location TEXT UNIQUE);`)
	mustExecApp(t, mainDB, `CREATE TABLE client_files_subfolders (prefix TEXT, location_id INTEGER, PRIMARY KEY (prefix, location_id));`)
	mustExecApp(t, mainDB, `CREATE TABLE ideal_client_files_locations (location_id INTEGER PRIMARY KEY, weight INTEGER, max_num_bytes INTEGER);`)
	mustExecApp(t, mainDB, `CREATE TABLE ideal_thumbnail_override_location (location_id INTEGER);`)
	mustExecApp(t, mainDB, `CREATE TABLE current_storage_granularity (granularity INTEGER);`)
	mustExecApp(t, mainDB, `CREATE TABLE current_files_2 (hash_id INTEGER PRIMARY KEY, timestamp_ms INTEGER);`)
	mustExecApp(t, mainDB, `CREATE TABLE current_files_3 (hash_id INTEGER PRIMARY KEY, timestamp_ms INTEGER);`)
	mustExecApp(t, mainDB, `CREATE TABLE current_files_4 (hash_id INTEGER PRIMARY KEY, timestamp_ms INTEGER);`)
	mustExecApp(t, mainDB, `CREATE TABLE current_files_5 (hash_id INTEGER PRIMARY KEY, timestamp_ms INTEGER);`)
	mustExecApp(t, mainDB, `CREATE TABLE deleted_files_4 (hash_id INTEGER PRIMARY KEY, timestamp_ms INTEGER, original_timestamp_ms INTEGER);`)
	mustExecApp(t, mainDB, `CREATE TABLE deleted_files_5 (hash_id INTEGER PRIMARY KEY, timestamp_ms INTEGER, original_timestamp_ms INTEGER);`)
	mustExecApp(t, mainDB, `CREATE TABLE current_files_6 (hash_id INTEGER PRIMARY KEY, timestamp_ms INTEGER);`)
	seedAppTestStorage(t, mainDB, dir)
	mustExecApp(
		t,
		mainDB,
		`INSERT INTO services (service_id, service_key, service_type, name, dictionary_string) VALUES (?, ?, ?, ?, ?), (?, ?, ?, ?, ?), (?, ?, ?, ?, ?), (?, ?, ?, ?, ?), (?, ?, ?, ?, ?), (?, ?, ?, ?, ?), (?, ?, ?, ?, ?), (?, ?, ?, ?, ?);`,
		2, []byte("local-files"), 2, "my files", "{}",
		3, []byte("all-local-files"), 15, "all local files", "{}",
		4, []byte("all-local-media"), 21, "all local media", "{}",
		5, []byte("combined-files"), 11, "all known files", "{}",
		6, []byte("trash"), 14, "trash", "{}",
		7, []byte("all deleted files"), 19, "deleted from anywhere", "{}",
		8, []byte("local notes"), 17, "local notes", "{}",
		9, []byte("client api"), 18, "client api", "{}",
	)

	masterDB, err := sql.Open("sqlite", masterPath)
	if err != nil {
		t.Fatalf("sql.Open(master) error = %v", err)
	}
	defer masterDB.Close()

	mustExecApp(t, masterDB, `CREATE TABLE hashes (hash_id INTEGER PRIMARY KEY, hash BLOB UNIQUE);`)
	mustExecApp(t, masterDB, `CREATE TABLE blurhashes (hash_id INTEGER PRIMARY KEY, blurhash TEXT);`)
	mustExecApp(t, masterDB, `CREATE TABLE url_domains (domain_id INTEGER PRIMARY KEY, domain TEXT UNIQUE);`)
	mustExecApp(t, masterDB, `CREATE TABLE urls (url_id INTEGER PRIMARY KEY, domain_id INTEGER, url TEXT UNIQUE);`)

	createEmptySQLiteDB(t, cachesPath)
	createEmptySQLiteDB(t, mappingsPath)
	return dir
}

func seedAppTestStorage(t *testing.T, db *sql.DB, dbDir string) {
	t.Helper()

	fileRoot := clientfiles.DefaultFileRoot(dbDir)
	thumbnailRoot := clientfiles.DefaultThumbnailRoot(dbDir)

	mustExecApp(t, db, `INSERT INTO current_storage_granularity (granularity) VALUES (?);`, clientfiles.DefaultPrefixLength)
	mustExecApp(t, db, `INSERT INTO current_client_files_locations (location_id, location) VALUES (?, ?), (?, ?);`, 1, fileRoot, 2, thumbnailRoot)
	mustExecApp(t, db, `INSERT INTO ideal_client_files_locations (location_id, weight, max_num_bytes) VALUES (?, ?, NULL);`, 1, 1)
	mustExecApp(t, db, `INSERT INTO ideal_thumbnail_override_location (location_id) VALUES (?);`, 2)

	for _, prefix := range appTestStoragePrefixes(clientfiles.KindFile, clientfiles.DefaultPrefixLength) {
		mustExecApp(t, db, `INSERT INTO client_files_subfolders (prefix, location_id) VALUES (?, ?);`, prefix, 1)
	}

	for _, prefix := range appTestStoragePrefixes(clientfiles.KindThumbnail, clientfiles.DefaultPrefixLength) {
		mustExecApp(t, db, `INSERT INTO client_files_subfolders (prefix, location_id) VALUES (?, ?);`, prefix, 2)
	}

	if err := os.MkdirAll(fileRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll(fileRoot) error = %v", err)
	}

	if err := os.MkdirAll(thumbnailRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll(thumbnailRoot) error = %v", err)
	}
}

func appTestStoragePrefixes(kind clientfiles.Kind, prefixLength int) []string {
	prefixes := []string{}
	var build func(string, int)
	build = func(prefix string, remaining int) {
		if remaining == 0 {
			prefixes = append(prefixes, string(kind)+prefix)
			return
		}

		for _, digit := range "0123456789abcdef" {
			build(prefix+string(digit), remaining-1)
		}
	}

	build("", prefixLength)
	return prefixes
}

func writeAppPNGSourceFile(
	t *testing.T,
	dir string,
	name string,
	width int,
	height int,
) string {
	t.Helper()

	path := filepath.Join(dir, name)
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	defer file.Close()

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: 100, G: 120, B: 140, A: 255})
		}
	}

	if err := png.Encode(file, img); err != nil {
		t.Fatalf("png.Encode() error = %v", err)
	}

	return path
}

func mustExecApp(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()

	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("Exec(%q) error = %v", query, err)
	}
}

func decodeAppJSON(t *testing.T, raw []byte, target any) {
	t.Helper()

	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
}

func newAppMultipartUploadRequest(t *testing.T, target string, sourcePath string) *http.Request {
	t.Helper()

	fileInfo, err := os.Stat(sourcePath)
	if err != nil {
		t.Fatalf("Stat(sourcePath) error = %v", err)
	}

	payload, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatalf("ReadFile(sourcePath) error = %v", err)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	modifiedAtMS := fileInfo.ModTime().UTC().UnixMilli()
	if modifiedAtMS > 0 {
		if err := writer.WriteField("file_modified_at_ms", strconv.FormatInt(modifiedAtMS, 10)); err != nil {
			t.Fatalf("WriteField(file_modified_at_ms) error = %v", err)
		}
	}

	part, err := writer.CreateFormFile("file", filepath.Base(sourcePath))
	if err != nil {
		t.Fatalf("CreateFormFile() error = %v", err)
	}

	if _, err := part.Write(payload); err != nil {
		t.Fatalf("multipart file write error = %v", err)
	}

	if err := writer.Close(); err != nil {
		t.Fatalf("multipart writer Close() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, target, bytes.NewReader(body.Bytes()))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

func assertAppGetServiceLookup(
	t *testing.T,
	handler http.Handler,
	accessKey string,
	path string,
	wantStatus int,
	wantName string,
) {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Hydrus-Client-API-Access-Key", accessKey)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != wantStatus {
		t.Fatalf("%s status = %d, want %d", path, rr.Code, wantStatus)
	}

	var payload map[string]any
	decodeAppJSON(t, rr.Body.Bytes(), &payload)

	service, ok := payload["service"].(map[string]any)
	if !ok {
		t.Fatalf("service type = %T, want map[string]any", payload["service"])
	}

	if service["name"] != wantName {
		t.Fatalf("service.name = %v, want %q", service["name"], wantName)
	}
}

func assertAppGetServiceError(
	t *testing.T,
	handler http.Handler,
	accessKey string,
	path string,
	wantStatus int,
	wantBody string,
) {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Hydrus-Client-API-Access-Key", accessKey)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != wantStatus {
		t.Fatalf("%s status = %d, want %d", path, rr.Code, wantStatus)
	}

	if got := strings.TrimSpace(rr.Body.String()); got != wantBody {
		t.Fatalf("%s body = %q, want %q", path, got, wantBody)
	}
}

func assertAppServiceDiscovery(t *testing.T, raw []byte, expectBootstrapDiscovery bool) {
	t.Helper()

	var payload map[string]any
	decodeAppJSON(t, raw, &payload)

	servicesValue, ok := payload["services_v2"].([]any)
	if !ok {
		t.Fatalf("services_v2 type = %T, want []any", payload["services_v2"])
	}

	hiddenNames := map[string]struct{}{
		"deleted from anywhere": {},
		"local notes":           {},
		"client api":            {},
	}

	serviceByName := map[string]map[string]any{}

	for _, item := range servicesValue {
		service, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("service item type = %T, want map[string]any", item)
		}

		name, ok := service["name"].(string)
		if !ok {
			t.Fatalf("service name type = %T, want string", service["name"])
		}

		if _, hidden := hiddenNames[name]; hidden {
			t.Fatalf("hidden bootstrap service %q unexpectedly appeared in discovery response", name)
		}

		serviceByName[name] = service
	}

	if !expectBootstrapDiscovery {
		return
	}

	localTagsValue, ok := payload["local_tags"].([]any)
	if !ok {
		t.Fatalf("local_tags type = %T, want []any", payload["local_tags"])
	}

	if len(localTagsValue) != 2 {
		t.Fatalf("len(local_tags) = %d, want 2", len(localTagsValue))
	}

	if _, ok := serviceByName["downloader tags"]; !ok {
		t.Fatal("downloader tags missing from discovery response")
	}

	favourites, ok := serviceByName["favourites"]
	if !ok {
		t.Fatal("favourites missing from discovery response")
	}

	if _, ok := payload["local_ratings"]; ok {
		t.Fatal("local_ratings unexpectedly present in discovery payload")
	}

	if got, _ := favourites["star_shape"].(string); got != "fat star" {
		t.Fatalf("favourites star_shape = %q, want %q", got, "fat star")
	}

	showInThumbnail, ok := favourites["show_in_thumbnail"].(bool)
	if !ok || showInThumbnail {
		t.Fatalf("favourites show_in_thumbnail = %v, want explicit false", favourites["show_in_thumbnail"])
	}

	showInThumbnailEvenWhenNull, ok := favourites["show_in_thumbnail_even_when_null"].(bool)
	if !ok || showInThumbnailEvenWhenNull {
		t.Fatalf(
			"favourites show_in_thumbnail_even_when_null = %v, want explicit false",
			favourites["show_in_thumbnail_even_when_null"],
		)
	}

	colours, ok := favourites["colours"].(map[string]any)
	if !ok {
		t.Fatalf("favourites colours type = %T, want map[string]any", favourites["colours"])
	}

	likeColour, ok := colours["like"].(map[string]any)
	if !ok {
		t.Fatalf("favourites like colour type = %T, want map[string]any", colours["like"])
	}

	if got, _ := likeColour["brush"].(string); got != "#F0F041" {
		t.Fatalf("favourites like brush = %q, want %q", got, "#F0F041")
	}

	dislikeColour, ok := colours["dislike"].(map[string]any)
	if !ok {
		t.Fatalf("favourites dislike colour type = %T, want map[string]any", colours["dislike"])
	}

	if got, _ := dislikeColour["brush"].(string); got != "#C85078" {
		t.Fatalf("favourites dislike brush = %q, want %q", got, "#C85078")
	}

	nullColour, ok := colours["null"].(map[string]any)
	if !ok {
		t.Fatalf("favourites null colour type = %T, want map[string]any", colours["null"])
	}

	if got, _ := nullColour["brush"].(string); got != "#BFBFBF" {
		t.Fatalf("favourites null brush = %q, want %q", got, "#BFBFBF")
	}

	mixedColour, ok := colours["mixed"].(map[string]any)
	if !ok {
		t.Fatalf("favourites mixed colour type = %T, want map[string]any", colours["mixed"])
	}

	if got, _ := mixedColour["brush"].(string); got != "#5F5F5F" {
		t.Fatalf("favourites mixed brush = %q, want %q", got, "#5F5F5F")
	}
}
