package app

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/official-elinas/hydrus-go/internal/api/httpapi"
	"github.com/official-elinas/hydrus-go/internal/bootstrap"
	"github.com/official-elinas/hydrus-go/internal/config"
	coreptrsync "github.com/official-elinas/hydrus-go/internal/core/ptrsync"
	"github.com/official-elinas/hydrus-go/internal/core/services"
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

func TestRun_ShutsDownActivePTRTriggerWithoutStuckLease(t *testing.T) {
	dbDir := createThinClientBundle(t)
	accountStarted := make(chan struct{})

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/session_key":
			http.SetCookie(w, &http.Cookie{Name: "session_key", Value: "app-session", Path: "/"})
			w.WriteHeader(http.StatusOK)
		case "/account":
			select {
			case <-accountStarted:
			default:
				close(accountStarted)
			}

			<-r.Context().Done()
			return
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	ptrConfig := testAppPTRConfigFromServer(t, server.URL, coreptrsync.DefaultSharedAccessKey)
	ptrConfig.Enabled = true

	cfg := config.Config{
		ListenAddr:               "127.0.0.1:0",
		DBDir:                    dbDir,
		PTR:                      ptrConfig,
		AccessKey:                strings.Repeat("a", 64),
		AccessName:               "test-client",
		LogLevel:                 "error",
		ShutdownTimeout:          5 * time.Second,
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

	req := httptest.NewRequest(http.MethodPost, "/service/ptr/sync", nil)
	req.Header.Set("Hydrus-Client-API-Access-Key", cfg.AccessKey)
	rr := httptest.NewRecorder()

	application.server.Handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("ptr trigger status = %d, want %d", rr.Code, http.StatusOK)
	}
	t.Logf("ptr trigger response: %s", strings.TrimSpace(rr.Body.String()))

	var payload map[string]any
	decodeAppJSON(t, rr.Body.Bytes(), &payload)
	ptr := payload["ptr"].(map[string]any)
	if ptr["phase"] != coreptrsync.PhaseSyncing {
		t.Fatalf("ptr.phase = %v, want %q", ptr["phase"], coreptrsync.PhaseSyncing)
	}

	if ptr["is_running"] != true {
		t.Fatalf("ptr.is_running = %v, want true", ptr["is_running"])
	}

	select {
	case <-accountStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for app-triggered PTR sync to reach /account")
	}

	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for App.Run to stop with active PTR sync")
	}

	readBundle, err := hydrusdb.Open(context.Background(), dbDir)
	if err != nil {
		t.Fatalf("hydrusdb.Open() error = %v", err)
	}
	defer readBundle.Close()

	status, err := readBundle.GetPTRSyncStatus(context.Background(), cfg.PTR)
	if err != nil {
		t.Fatalf("GetPTRSyncStatus() error = %v", err)
	}

	if status.IsRunning {
		t.Fatal("status.IsRunning = true, want false")
	}

	if status.LastError != "" {
		t.Fatalf("status.LastError = %q, want empty after app shutdown cancellation", status.LastError)
	}
	t.Logf("post-shutdown ptr status: phase=%s is_running=%t last_error=%q", status.Phase, status.IsRunning, status.LastError)

	writeBundle, err := hydrusdb.OpenWritable(context.Background(), dbDir)
	if err != nil {
		t.Fatalf("hydrusdb.OpenWritable() error = %v", err)
	}
	defer writeBundle.Close()

	lease, err := writeBundle.BeginPTRSync(context.Background(), cfg.PTR)
	if err != nil {
		t.Fatalf("BeginPTRSync() after app shutdown error = %v", err)
	}

	if _, err := writeBundle.FinishPTRSyncFailure(context.Background(), cfg.PTR, lease.RunToken, "cleanup"); err != nil {
		t.Fatalf("FinishPTRSyncFailure() cleanup error = %v", err)
	}
}

func TestRun_PersistsCompletePTRSyncAcrossAppRestart(t *testing.T) {
	dbDir := createThinClientBundle(t)

	var (
		sessionKeyRequests atomic.Int32
		accountRequests    atomic.Int32
		optionsRequests    atomic.Int32
		tagFilterRequests  atomic.Int32
		metadataRequests   atomic.Int32
		updateRequests     atomic.Int32
	)

	updateBody := hydrusNetworkBytes(t, []any{hydrusSerialisableTypeDefinitionsUpdate, 1, []any{}})
	updateHash := sha256Hex(updateBody)
	nextUpdateDue := time.Now().Add(time.Hour).Unix()

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/session_key":
			sessionKeyRequests.Add(1)
			http.SetCookie(w, &http.Cookie{Name: "session_key", Value: "app-session", Path: "/"})
			w.WriteHeader(http.StatusOK)
		case "/account":
			accountRequests.Add(1)
			_, _ = w.Write(hydrusArgsBytes(t, hydrusDictEntry{
				key: "account",
				metaValue: metaJSON([]any{
					strings.Repeat("aa", 32),
					unsupportedSerialisable(102),
					int64(1699990000),
					nil,
					serialisableDictionaryString(t,
						hydrusDictEntry{key: "banned_info", metaValue: metaJSON(nil)},
						hydrusDictEntry{key: "bandwidth_tracker", metaValue: metaHydrus(unsupportedSerialisable(39))},
						hydrusDictEntry{key: "message", metaValue: metaJSON("shared read-only")},
						hydrusDictEntry{key: "message_created", metaValue: metaJSON(int64(1699990100))},
					),
				}),
			}))
		case "/options":
			optionsRequests.Add(1)
			_, _ = w.Write(hydrusArgsBytes(t, hydrusDictEntry{
				key:       "service_options",
				metaValue: metaHydrus(serialisableDictionary(hydrusDictEntry{key: "update_period", metaValue: metaJSON(int64(3600))}, hydrusDictEntry{key: "nullification_period", metaValue: metaJSON(int64(86400))})),
			}))
		case "/tag_filter":
			tagFilterRequests.Add(1)
			_, _ = w.Write(hydrusArgsBytes(t, hydrusDictEntry{
				key:       "tag_filter",
				metaValue: metaHydrus(serialisableTagFilter(map[string]int{":": 1})),
			}))
		case "/metadata":
			metadataRequests.Add(1)
			_, _ = w.Write(hydrusArgsBytes(t, hydrusDictEntry{
				key:       "metadata_slice",
				metaValue: metaHydrus(serialisableMetadata(nextUpdateDue, metadataRow{updateIndex: 0, updateHashes: []string{updateHash}, begin: 10, end: 20})),
			}))
		case "/update":
			updateRequests.Add(1)
			if got := r.URL.Query().Get("update_hash"); got != updateHash {
				t.Fatalf("update_hash = %q, want %q", got, updateHash)
			}
			_, _ = w.Write(updateBody)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	ptrConfig := testAppPTRConfigFromServer(t, server.URL, coreptrsync.DefaultSharedAccessKey)
	ptrConfig.Enabled = true

	cfg := config.Config{
		ListenAddr:               "127.0.0.1:0",
		DBDir:                    dbDir,
		PTR:                      ptrConfig,
		AccessKey:                strings.Repeat("a", 64),
		AccessName:               "test-client",
		LogLevel:                 "error",
		ShutdownTimeout:          5 * time.Second,
		AllowNonLocalConnections: false,
		EnableCORS:               false,
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	app1, err := New(context.Background(), cfg, logger)
	if err != nil {
		t.Fatalf("New(first) error = %v", err)
	}

	ctx1, cancel1 := context.WithCancel(context.Background())
	errCh1 := make(chan error, 1)
	go func() {
		errCh1 <- app1.Run(ctx1)
	}()

	time.Sleep(100 * time.Millisecond)

	triggerReq := httptest.NewRequest(http.MethodPost, "/service/ptr/sync", nil)
	triggerReq.Header.Set("Hydrus-Client-API-Access-Key", cfg.AccessKey)
	triggerRR := httptest.NewRecorder()

	app1.server.Handler.ServeHTTP(triggerRR, triggerReq)

	if triggerRR.Code != http.StatusOK {
		t.Fatalf("ptr trigger status = %d, want %d", triggerRR.Code, http.StatusOK)
	}

	firstStatus := waitForAppPTRStatus(t, app1.server.Handler, cfg.AccessKey, func(status coreptrsync.Status) bool {
		return status.Phase == coreptrsync.PhaseIdle &&
			status.IsComplete &&
			!status.IsRunning &&
			status.MetadataSlice == 1 &&
			status.DownloadedUpdateCount == 1 &&
			status.ProcessedDefinitionCount == 1 &&
			status.ProcessedContentCount == 0
	})

	if firstStatus.LastError != "" {
		t.Fatalf("firstStatus.LastError = %q, want empty", firstStatus.LastError)
	}

	cancel1()
	if err := waitForAppRunStop(errCh1); err != nil {
		t.Fatal(err)
	}

	app2, err := New(context.Background(), cfg, logger)
	if err != nil {
		t.Fatalf("New(second) error = %v", err)
	}

	ctx2, cancel2 := context.WithCancel(context.Background())
	errCh2 := make(chan error, 1)
	go func() {
		errCh2 <- app2.Run(ctx2)
	}()

	time.Sleep(100 * time.Millisecond)

	secondStatus := mustAppPTRStatus(t, app2.server.Handler, cfg.AccessKey)
	if secondStatus.Phase != coreptrsync.PhaseIdle {
		t.Fatalf("secondStatus.Phase = %q, want %q", secondStatus.Phase, coreptrsync.PhaseIdle)
	}

	if !secondStatus.IsComplete {
		t.Fatal("secondStatus.IsComplete = false, want true")
	}

	if secondStatus.IsRunning {
		t.Fatal("secondStatus.IsRunning = true, want false")
	}

	if secondStatus.MetadataSlice != 1 {
		t.Fatalf("secondStatus.MetadataSlice = %d, want 1", secondStatus.MetadataSlice)
	}

	if secondStatus.DownloadedUpdateCount != 1 {
		t.Fatalf("secondStatus.DownloadedUpdateCount = %d, want 1", secondStatus.DownloadedUpdateCount)
	}

	if secondStatus.ProcessedDefinitionCount != 1 {
		t.Fatalf("secondStatus.ProcessedDefinitionCount = %d, want 1", secondStatus.ProcessedDefinitionCount)
	}

	if secondStatus.ProcessedContentCount != 0 {
		t.Fatalf("secondStatus.ProcessedContentCount = %d, want 0", secondStatus.ProcessedContentCount)
	}

	if secondStatus.LastError != "" {
		t.Fatalf("secondStatus.LastError = %q, want empty", secondStatus.LastError)
	}

	cancel2()
	if err := waitForAppRunStop(errCh2); err != nil {
		t.Fatal(err)
	}

	if got := sessionKeyRequests.Load(); got != 1 {
		t.Fatalf("session_key requests = %d, want 1", got)
	}

	if got := accountRequests.Load(); got != 1 {
		t.Fatalf("account requests = %d, want 1", got)
	}

	if got := optionsRequests.Load(); got != 1 {
		t.Fatalf("options requests = %d, want 1", got)
	}

	if got := tagFilterRequests.Load(); got != 1 {
		t.Fatalf("tag_filter requests = %d, want 1", got)
	}

	if got := metadataRequests.Load(); got != 1 {
		t.Fatalf("metadata requests = %d, want 1", got)
	}

	if got := updateRequests.Load(); got != 1 {
		t.Fatalf("update requests = %d, want 1", got)
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

func TestNew_EnablesAnonymousPTRFoundation(t *testing.T) {
	dbDir := createThinClientBundle(t)
	ptrConfig := coreptrsync.DefaultConfig()
	ptrConfig.Enabled = true

	cfg := config.Config{
		ListenAddr:               "127.0.0.1:0",
		DBDir:                    dbDir,
		PTR:                      ptrConfig,
		AccessKey:                strings.Repeat("a", 64),
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

	serviceReq := httptest.NewRequest(
		http.MethodGet,
		"/get_service?service_name="+url.QueryEscape(coreptrsync.DefaultServiceName),
		nil,
	)
	serviceReq.Header.Set("Hydrus-Client-API-Access-Key", cfg.AccessKey)
	serviceRR := httptest.NewRecorder()

	application.server.Handler.ServeHTTP(serviceRR, serviceReq)

	if serviceRR.Code != http.StatusOK {
		t.Fatalf("get_service status = %d, want %d", serviceRR.Code, http.StatusOK)
	}

	var servicePayload map[string]any
	decodeAppJSON(t, serviceRR.Body.Bytes(), &servicePayload)
	service := servicePayload["service"].(map[string]any)
	if service["name"] != coreptrsync.DefaultServiceName {
		t.Fatalf("service.name = %v, want %q", service["name"], coreptrsync.DefaultServiceName)
	}

	statusReq := httptest.NewRequest(http.MethodGet, "/service/ptr/status", nil)
	statusReq.Header.Set("Hydrus-Client-API-Access-Key", cfg.AccessKey)
	statusRR := httptest.NewRecorder()

	application.server.Handler.ServeHTTP(statusRR, statusReq)

	if statusRR.Code != http.StatusOK {
		t.Fatalf("ptr status = %d, want %d", statusRR.Code, http.StatusOK)
	}

	var statusPayload map[string]any
	decodeAppJSON(t, statusRR.Body.Bytes(), &statusPayload)
	ptr := statusPayload["ptr"].(map[string]any)
	if ptr["enabled"] != true {
		t.Fatalf("ptr.enabled = %v, want true", ptr["enabled"])
	}

	if ptr["configured"] != true {
		t.Fatalf("ptr.configured = %v, want true", ptr["configured"])
	}

	if ptr["phase"] != coreptrsync.PhaseIdle {
		t.Fatalf("ptr.phase = %v, want %q", ptr["phase"], coreptrsync.PhaseIdle)
	}

	if ptr["account_mode"] != coreptrsync.AccountModeSharedReadOnly {
		t.Fatalf("ptr.account_mode = %v, want %q", ptr["account_mode"], coreptrsync.AccountModeSharedReadOnly)
	}
}

func TestNew_PTRStatusIsDisabledWithoutDBWhenSyncDisabled(t *testing.T) {
	cfg := config.Config{
		ListenAddr:               "127.0.0.1:0",
		AccessKey:                strings.Repeat("a", 64),
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

	statusReq := httptest.NewRequest(http.MethodGet, "/service/ptr/status", nil)
	statusReq.Header.Set("Hydrus-Client-API-Access-Key", cfg.AccessKey)
	statusRR := httptest.NewRecorder()

	application.server.Handler.ServeHTTP(statusRR, statusReq)

	if statusRR.Code != http.StatusOK {
		t.Fatalf("ptr status = %d, want %d", statusRR.Code, http.StatusOK)
	}

	var statusPayload map[string]any
	decodeAppJSON(t, statusRR.Body.Bytes(), &statusPayload)
	ptr := statusPayload["ptr"].(map[string]any)

	if ptr["enabled"] != false {
		t.Fatalf("ptr.enabled = %v, want false", ptr["enabled"])
	}

	if ptr["configured"] != false {
		t.Fatalf("ptr.configured = %v, want false", ptr["configured"])
	}

	if ptr["phase"] != coreptrsync.PhaseDisabled {
		t.Fatalf("ptr.phase = %v, want %q", ptr["phase"], coreptrsync.PhaseDisabled)
	}
}

func TestNew_GrantsPTRMutationPermissionsThroughBootstrapAuth(t *testing.T) {
	cfg := config.Config{
		ListenAddr:               "127.0.0.1:0",
		AccessKey:                strings.Repeat("a", 64),
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

	verifyReq := httptest.NewRequest(http.MethodGet, "/verify_access_key", nil)
	verifyReq.Header.Set("Hydrus-Client-API-Access-Key", cfg.AccessKey)
	verifyRR := httptest.NewRecorder()

	application.server.Handler.ServeHTTP(verifyRR, verifyReq)

	if verifyRR.Code != http.StatusOK {
		t.Fatalf("verify_access_key status = %d, want %d", verifyRR.Code, http.StatusOK)
	}

	var verifyPayload struct {
		BasicPermissions []httpapi.Permission `json:"basic_permissions"`
	}
	decodeAppJSON(t, verifyRR.Body.Bytes(), &verifyPayload)

	if !containsAppPermission(verifyPayload.BasicPermissions, httpapi.PermissionEditFileTags) {
		t.Fatalf("basic_permissions = %v, want %d", verifyPayload.BasicPermissions, httpapi.PermissionEditFileTags)
	}

	if !containsAppPermission(verifyPayload.BasicPermissions, httpapi.PermissionCommitPending) {
		t.Fatalf("basic_permissions = %v, want %d", verifyPayload.BasicPermissions, httpapi.PermissionCommitPending)
	}

	tests := []struct {
		name       string
		method     string
		path       string
		body       string
		wantStatus int
	}{
		{
			name:       "pending counts falls through auth",
			method:     http.MethodGet,
			path:       "/manage_services/pending_counts",
			wantStatus: http.StatusOK,
		},
		{
			name:       "commit pending falls through auth",
			method:     http.MethodPost,
			path:       "/manage_services/commit_pending",
			body:       `{"service_key":"` + strings.Repeat("b", 64) + `"}`,
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name:       "add tags falls through auth",
			method:     http.MethodPost,
			path:       "/add_tags/add_tags",
			body:       `{"hash":"` + strings.Repeat("c", 64) + `","service_keys_to_actions_to_tags":{"` + strings.Repeat("d", 64) + `":{"2":["test tag"]}}}`,
			wantStatus: http.StatusServiceUnavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			req.Header.Set("Hydrus-Client-API-Access-Key", cfg.AccessKey)
			rr := httptest.NewRecorder()

			application.server.Handler.ServeHTTP(rr, req)

			if rr.Code != tt.wantStatus {
				t.Fatalf("%s status = %d, want %d", tt.path, rr.Code, tt.wantStatus)
			}

			if rr.Code == http.StatusForbidden {
				t.Fatalf("%s unexpectedly returned 403 forbidden", tt.path)
			}
		})
	}
}

type stubDownloaderController struct {
	activate func() error
	shutdown func() error
}

func (s stubDownloaderController) ActivateAutoimport(context.Context) error {
	if s.activate != nil {
		return s.activate()
	}
	return nil
}

func (s stubDownloaderController) CheckCallbackURLReachability() {
}

func (s stubDownloaderController) Shutdown(context.Context) error {
	if s.shutdown != nil {
		return s.shutdown()
	}
	return nil
}

func TestApp_ActivateDownloaderAutoimportAfterReady(t *testing.T) {
	originalWait := waitForDaemonReadyFn
	defer func() { waitForDaemonReadyFn = originalWait }()

	calledWait := false
	calledActivate := false
	waitForDaemonReadyFn = func(ctx context.Context, listenAddr string) error {
		calledWait = true
		if listenAddr != "127.0.0.1:45869" {
			t.Fatalf("listenAddr = %q, want 127.0.0.1:45869", listenAddr)
		}
		if ctx.Err() != nil {
			t.Fatalf("wait context already canceled: %v", ctx.Err())
		}
		return nil
	}

	application := &App{
		cfg: config.Config{
			ListenAddr:      "127.0.0.1:45869",
			ShutdownTimeout: 3 * time.Second,
		},
		downloaderManager: stubDownloaderController{activate: func() error {
			if !calledWait {
				t.Fatal("ActivateAutoimport called before waitForDaemonReady")
			}
			calledActivate = true
			return nil
		}},
	}

	if err := application.activateDownloaderAutoimportAfterReady(); err != nil {
		t.Fatalf("activateDownloaderAutoimportAfterReady() error = %v", err)
	}
	if !calledWait {
		t.Fatal("waitForDaemonReady was not called")
	}
	if !calledActivate {
		t.Fatal("ActivateAutoimport was not called")
	}
}

func TestNew_PTRStatusIgnoresPersistedFoundationWhenSyncDisabled(t *testing.T) {
	dbDir := createThinClientBundle(t)
	enabledPTR := coreptrsync.DefaultConfig()
	enabledPTR.Enabled = true

	enabledCfg := config.Config{
		ListenAddr:               "127.0.0.1:0",
		DBDir:                    dbDir,
		PTR:                      enabledPTR,
		AccessKey:                strings.Repeat("a", 64),
		AccessName:               "test-client",
		LogLevel:                 "error",
		ShutdownTimeout:          time.Second,
		AllowNonLocalConnections: false,
		EnableCORS:               false,
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	enabledApp, err := New(context.Background(), enabledCfg, logger)
	if err != nil {
		t.Fatalf("New(enabled) error = %v", err)
	}
	enabledApp.closeResources()

	disabledCfg := enabledCfg
	disabledCfg.PTR = coreptrsync.DefaultConfig()

	disabledApp, err := New(context.Background(), disabledCfg, logger)
	if err != nil {
		t.Fatalf("New(disabled) error = %v", err)
	}
	defer disabledApp.closeResources()

	statusReq := httptest.NewRequest(http.MethodGet, "/service/ptr/status", nil)
	statusReq.Header.Set("Hydrus-Client-API-Access-Key", disabledCfg.AccessKey)
	statusRR := httptest.NewRecorder()

	disabledApp.server.Handler.ServeHTTP(statusRR, statusReq)

	if statusRR.Code != http.StatusOK {
		t.Fatalf("ptr status = %d, want %d", statusRR.Code, http.StatusOK)
	}

	var statusPayload map[string]any
	decodeAppJSON(t, statusRR.Body.Bytes(), &statusPayload)
	ptr := statusPayload["ptr"].(map[string]any)

	if ptr["enabled"] != false {
		t.Fatalf("ptr.enabled = %v, want false", ptr["enabled"])
	}

	if ptr["configured"] != false {
		t.Fatalf("ptr.configured = %v, want false", ptr["configured"])
	}

	if ptr["phase"] != coreptrsync.PhaseDisabled {
		t.Fatalf("ptr.phase = %v, want %q", ptr["phase"], coreptrsync.PhaseDisabled)
	}
}

func TestNew_PTRNameCollisionDoesNotAbortStartup(t *testing.T) {
	dbDir := createThinClientBundle(t)
	mainDB := openSQLiteForAppTest(t, filepath.Join(dbDir, "client.db"))
	defer mainDB.Close()

	mustExecApp(
		t,
		mainDB,
		`INSERT INTO services (service_key, service_type, name, dictionary_string) VALUES (?, ?, ?, ?)`,
		[]byte("existing-public-ptr"),
		int(services.TypeTagRepository),
		coreptrsync.DefaultServiceName,
		"{}",
	)

	ptrConfig := coreptrsync.DefaultConfig()
	ptrConfig.Enabled = true

	cfg := config.Config{
		ListenAddr:               "127.0.0.1:0",
		DBDir:                    dbDir,
		PTR:                      ptrConfig,
		AccessKey:                strings.Repeat("a", 64),
		AccessName:               "test-client",
		LogLevel:                 "error",
		ShutdownTimeout:          time.Second,
		AllowNonLocalConnections: false,
		EnableCORS:               false,
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	application, err := New(context.Background(), cfg, logger)
	if err != nil {
		t.Fatalf("New() error = %v, want degraded PTR availability instead", err)
	}
	defer application.closeResources()

	statusReq := httptest.NewRequest(http.MethodGet, "/service/ptr/status", nil)
	statusReq.Header.Set("Hydrus-Client-API-Access-Key", cfg.AccessKey)
	statusRR := httptest.NewRecorder()

	application.server.Handler.ServeHTTP(statusRR, statusReq)

	if statusRR.Code != http.StatusOK {
		t.Fatalf("ptr status = %d, want %d", statusRR.Code, http.StatusOK)
	}

	var statusPayload map[string]any
	decodeAppJSON(t, statusRR.Body.Bytes(), &statusPayload)
	ptr := statusPayload["ptr"].(map[string]any)

	if ptr["phase"] != coreptrsync.PhaseUnavailable {
		t.Fatalf("ptr.phase = %v, want %q", ptr["phase"], coreptrsync.PhaseUnavailable)
	}

	reason, ok := ptr["unavailable_reason"].(string)
	if !ok || !strings.Contains(reason, "already in use") {
		t.Fatalf("ptr.unavailable_reason = %v, want collision guidance", ptr["unavailable_reason"])
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

	createReq := httptest.NewRequest(
		http.MethodGet,
		"/get_files/file_metadata?hashes=%5B%22"+strings.Repeat("d", 64)+"%22%5D&only_return_identifiers=true&create_new_file_ids=true&include_services_object=false",
		nil,
	)
	createReq.Header.Set("Hydrus-Client-API-Access-Key", application.access.AccessKey())
	createRR := httptest.NewRecorder()

	application.server.Handler.ServeHTTP(createRR, createReq)

	if createRR.Code != http.StatusNotImplemented {
		t.Fatalf("create_new_file_ids status = %d, want %d", createRR.Code, http.StatusNotImplemented)
	}
}

func TestApp_CreateNewFileIDsRoundTrip(t *testing.T) {
	dbDir := createThinClientBundle(t)
	unknownHash := strings.Repeat("d", 64)

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

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	application, err := New(context.Background(), cfg, logger)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer application.closeResources()

	createReq := httptest.NewRequest(
		http.MethodGet,
		"/get_files/file_metadata?hashes="+url.QueryEscape(`["`+unknownHash+`"]`)+"&only_return_identifiers=true&create_new_file_ids=true&include_services_object=false",
		nil,
	)
	createReq.Header.Set("Hydrus-Client-API-Access-Key", cfg.AccessKey)
	createRR := httptest.NewRecorder()

	application.server.Handler.ServeHTTP(createRR, createReq)

	if createRR.Code != http.StatusOK {
		t.Fatalf("create_new_file_ids status = %d, want %d", createRR.Code, http.StatusOK)
	}

	var createPayload map[string]any
	decodeAppJSON(t, createRR.Body.Bytes(), &createPayload)
	createRows := createPayload["metadata"].([]any)
	createRow := createRows[0].(map[string]any)
	createdFileID := int64(createRow["file_id"].(float64))
	if createdFileID <= 0 {
		t.Fatalf("created file_id = %d, want > 0", createdFileID)
	}

	if createRow["hash"] != unknownHash {
		t.Fatalf("created hash = %v, want %q", createRow["hash"], unknownHash)
	}

	repeatedReq := httptest.NewRequest(
		http.MethodGet,
		"/get_files/file_metadata?hashes="+url.QueryEscape(`["`+unknownHash+`"]`)+"&only_return_identifiers=true&include_services_object=false",
		nil,
	)
	repeatedReq.Header.Set("Hydrus-Client-API-Access-Key", cfg.AccessKey)
	repeatedRR := httptest.NewRecorder()

	application.server.Handler.ServeHTTP(repeatedRR, repeatedReq)

	if repeatedRR.Code != http.StatusOK {
		t.Fatalf("repeated identifier status = %d, want %d", repeatedRR.Code, http.StatusOK)
	}

	var repeatedPayload map[string]any
	decodeAppJSON(t, repeatedRR.Body.Bytes(), &repeatedPayload)
	repeatedRows := repeatedPayload["metadata"].([]any)
	repeatedRow := repeatedRows[0].(map[string]any)
	if got := int64(repeatedRow["file_id"].(float64)); got != createdFileID {
		t.Fatalf("repeated file_id = %d, want %d", got, createdFileID)
	}

	basicReq := httptest.NewRequest(
		http.MethodGet,
		"/get_files/file_metadata?hashes="+url.QueryEscape(`["`+unknownHash+`"]`)+"&only_return_basic_information=true&include_services_object=false",
		nil,
	)
	basicReq.Header.Set("Hydrus-Client-API-Access-Key", cfg.AccessKey)
	basicRR := httptest.NewRecorder()

	application.server.Handler.ServeHTTP(basicRR, basicReq)

	if basicRR.Code != http.StatusOK {
		t.Fatalf("basic metadata status = %d, want %d", basicRR.Code, http.StatusOK)
	}

	var basicPayload map[string]any
	decodeAppJSON(t, basicRR.Body.Bytes(), &basicPayload)
	basicRows := basicPayload["metadata"].([]any)
	basicRow := basicRows[0].(map[string]any)
	if got := basicRow["file_id"]; got != nil {
		t.Fatalf("basic metadata file_id = %v, want nil for unimported created hash", got)
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

func TestApp_DBBackedImportURLRoundTrip(t *testing.T) {
	dbDir := createThinClientBundle(t)
	sourcePath := writeAppPNGSourceFile(t, t.TempDir(), "app-url-import.png", 16, 24)
	sourceBytes, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatalf("ReadFile(sourcePath) error = %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, "/image.png", http.StatusFound)
		case "/image.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(sourceBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := config.Config{
		ListenAddr:               "127.0.0.1:0",
		DBDir:                    dbDir,
		AccessKey:                strings.Repeat("c", 64),
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

	requestBody := fmt.Sprintf(`{"url":%q}`, server.URL+"/redirect")
	importReq := httptest.NewRequest(http.MethodPost, "/v1/import/url", strings.NewReader(requestBody))
	importReq.Header.Set("Hydrus-Client-API-Access-Key", cfg.AccessKey)
	importRR := httptest.NewRecorder()

	application.server.Handler.ServeHTTP(importRR, importReq)

	if importRR.Code != http.StatusOK {
		t.Fatalf("import status = %d, want %d", importRR.Code, http.StatusOK)
	}

	var importPayload map[string]any
	decodeAppJSON(t, importRR.Body.Bytes(), &importPayload)
	fileID := int64(importPayload["file_id"].(float64))

	metadataReq := httptest.NewRequest(
		http.MethodGet,
		"/get_files/file_metadata?file_id="+strconv.FormatInt(fileID, 10)+"&detailed_url_information=true",
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
	knownURLs := metadataRow["known_urls"].([]any)
	if len(knownURLs) != 2 {
		t.Fatalf("len(known_urls) = %d, want 2", len(knownURLs))
	}
	if knownURLs[0] != server.URL+"/image.png" || knownURLs[1] != server.URL+"/redirect" {
		t.Fatalf("known_urls = %v, want redirected and requested URLs", knownURLs)
	}
}

func TestApp_DBBackedHydrusClientMutationRoundTrip(t *testing.T) {
	dbDir := filepath.Join(t.TempDir(), "fresh-bundle")
	sourcePath := writeAppPNGSourceFile(t, t.TempDir(), "app-hydrus-api-import.png", 16, 24)

	cfg := config.Config{
		ListenAddr:                 "127.0.0.1:0",
		DBDir:                      dbDir,
		EnableFreshClientBootstrap: true,
		BootstrapTimeout:           time.Minute,
		AccessKey:                  strings.Repeat("d", 64),
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

	addFileReq := httptest.NewRequest(
		http.MethodPost,
		"/add_files/add_file",
		strings.NewReader(fmt.Sprintf(`{"path":%q}`, sourcePath)),
	)
	addFileReq.Header.Set("Hydrus-Client-API-Access-Key", cfg.AccessKey)
	addFileReq.Header.Set("Content-Type", "application/json")
	addFileRR := httptest.NewRecorder()

	application.server.Handler.ServeHTTP(addFileRR, addFileReq)

	if addFileRR.Code != http.StatusOK {
		t.Fatalf("add_file status = %d, want %d", addFileRR.Code, http.StatusOK)
	}

	var addFilePayload map[string]any
	decodeAppJSON(t, addFileRR.Body.Bytes(), &addFilePayload)
	if got := int(addFilePayload["status"].(float64)); got != 1 {
		t.Fatalf("add_file status payload = %d, want 1", got)
	}
	hashHex := addFilePayload["hash"].(string)

	servicesReq := httptest.NewRequest(http.MethodGet, "/get_services", nil)
	servicesReq.Header.Set("Hydrus-Client-API-Access-Key", cfg.AccessKey)
	servicesRR := httptest.NewRecorder()

	application.server.Handler.ServeHTTP(servicesRR, servicesReq)

	if servicesRR.Code != http.StatusOK {
		t.Fatalf("get_services status = %d, want %d", servicesRR.Code, http.StatusOK)
	}

	var servicesPayload map[string]any
	decodeAppJSON(t, servicesRR.Body.Bytes(), &servicesPayload)
	localTags := servicesPayload["local_tags"].([]any)
	myTagsServiceKey := ""
	for _, rawService := range localTags {
		servicePayload := rawService.(map[string]any)
		if servicePayload["name"] == "my tags" {
			myTagsServiceKey = servicePayload["service_key"].(string)
			break
		}
	}
	if myTagsServiceKey == "" {
		t.Fatalf("local_tags = %v, want my tags service", localTags)
	}

	associateReq := httptest.NewRequest(
		http.MethodPost,
		"/add_urls/associate_url",
		strings.NewReader(fmt.Sprintf(`{"hash":%q,"urls_to_add":["https://example.com/post/1"]}`, hashHex)),
	)
	associateReq.Header.Set("Hydrus-Client-API-Access-Key", cfg.AccessKey)
	associateReq.Header.Set("Content-Type", "application/json")
	associateRR := httptest.NewRecorder()

	application.server.Handler.ServeHTTP(associateRR, associateReq)

	if associateRR.Code != http.StatusOK {
		t.Fatalf("associate_url status = %d, want %d", associateRR.Code, http.StatusOK)
	}

	addTagsReq := httptest.NewRequest(
		http.MethodPost,
		"/add_tags/add_tags",
		strings.NewReader(fmt.Sprintf(`{"hash":%q,"service_keys_to_tags":{%q:["creator:alice"]}}`, hashHex, myTagsServiceKey)),
	)
	addTagsReq.Header.Set("Hydrus-Client-API-Access-Key", cfg.AccessKey)
	addTagsReq.Header.Set("Content-Type", "application/json")
	addTagsRR := httptest.NewRecorder()

	application.server.Handler.ServeHTTP(addTagsRR, addTagsReq)

	if addTagsRR.Code != http.StatusOK {
		t.Fatalf("add_tags status = %d, want %d", addTagsRR.Code, http.StatusOK)
	}
	if addTagsRR.Body.Len() != 0 {
		t.Fatalf("add_tags body len = %d, want 0", addTagsRR.Body.Len())
	}

	setNotesReq := httptest.NewRequest(
		http.MethodPost,
		"/add_notes/set_notes",
		strings.NewReader(fmt.Sprintf(`{"hash":%q,"notes":{"artist commentary":"hello from hydrus-go"}}`, hashHex)),
	)
	setNotesReq.Header.Set("Hydrus-Client-API-Access-Key", cfg.AccessKey)
	setNotesReq.Header.Set("Content-Type", "application/json")
	setNotesRR := httptest.NewRecorder()

	application.server.Handler.ServeHTTP(setNotesRR, setNotesReq)

	if setNotesRR.Code != http.StatusOK {
		t.Fatalf("set_notes status = %d, want %d", setNotesRR.Code, http.StatusOK)
	}

	setFileTimeReq := httptest.NewRequest(
		http.MethodPost,
		"/edit_times/set_time",
		strings.NewReader(fmt.Sprintf(`{"hash":%q,"timestamp_type":1,"timestamp":20.123}`, hashHex)),
	)
	setFileTimeReq.Header.Set("Hydrus-Client-API-Access-Key", cfg.AccessKey)
	setFileTimeReq.Header.Set("Content-Type", "application/json")
	setFileTimeRR := httptest.NewRecorder()

	application.server.Handler.ServeHTTP(setFileTimeRR, setFileTimeReq)

	if setFileTimeRR.Code != http.StatusOK {
		t.Fatalf("set file time status = %d, want %d", setFileTimeRR.Code, http.StatusOK)
	}

	setDomainTimeReq := httptest.NewRequest(
		http.MethodPost,
		"/edit_times/set_time",
		strings.NewReader(fmt.Sprintf(`{"hash":%q,"timestamp_type":0,"timestamp":30.123,"domain":"example.com"}`, hashHex)),
	)
	setDomainTimeReq.Header.Set("Hydrus-Client-API-Access-Key", cfg.AccessKey)
	setDomainTimeReq.Header.Set("Content-Type", "application/json")
	setDomainTimeRR := httptest.NewRecorder()

	application.server.Handler.ServeHTTP(setDomainTimeRR, setDomainTimeReq)

	if setDomainTimeRR.Code != http.StatusOK {
		t.Fatalf("set domain time status = %d, want %d", setDomainTimeRR.Code, http.StatusOK)
	}

	metadataReq := httptest.NewRequest(
		http.MethodGet,
		"/get_files/file_metadata?hashes="+url.QueryEscape(`[`+"\""+hashHex+"\""+`]`)+"&detailed_url_information=true&include_notes=true&include_milliseconds=true",
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

	knownURLs := metadataRow["known_urls"].([]any)
	if len(knownURLs) != 1 || knownURLs[0] != "https://example.com/post/1" {
		t.Fatalf("known_urls = %v, want [https://example.com/post/1]", knownURLs)
	}

	notes := metadataRow["notes"].(map[string]any)
	if notes["artist commentary"] != "hello from hydrus-go" {
		t.Fatalf("notes[artist commentary] = %v, want hello from hydrus-go", notes["artist commentary"])
	}

	timeModifiedDetails := metadataRow["time_modified_details"].(map[string]any)
	if got := timeModifiedDetails["local"]; got != 20.123 {
		t.Fatalf("time_modified_details[local] = %v, want 20.123", got)
	}
	if got := timeModifiedDetails["example.com"]; got != 30.123 {
		t.Fatalf("time_modified_details[example.com] = %v, want 30.123", got)
	}

	tags := metadataRow["tags"].(map[string]any)
	foundCurrentTag := false
	for _, rawService := range tags {
		serviceTags := rawService.(map[string]any)
		if serviceTags["name"] != "my tags" {
			continue
		}
		storageTags := serviceTags["storage_tags"].(map[string]any)
		currentTags := storageTags["0"].([]any)
		if len(currentTags) == 1 && currentTags[0] == "creator:alice" {
			foundCurrentTag = true
		}
	}
	if !foundCurrentTag {
		t.Fatalf("tags payload = %v, want my tags current creator:alice", tags)
	}

	forceCommitReq := httptest.NewRequest(http.MethodPost, "/manage_database/force_commit", nil)
	forceCommitReq.Header.Set("Hydrus-Client-API-Access-Key", cfg.AccessKey)
	forceCommitRR := httptest.NewRecorder()

	application.server.Handler.ServeHTTP(forceCommitRR, forceCommitReq)

	if forceCommitRR.Code != http.StatusOK {
		t.Fatalf("force_commit status = %d, want %d", forceCommitRR.Code, http.StatusOK)
	}
	if forceCommitRR.Body.Len() != 0 {
		t.Fatalf("force_commit body len = %d, want 0", forceCommitRR.Body.Len())
	}
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
		"/get_files/file_metadata?file_id="+strconv.FormatInt(fileID, 10),
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

	pixelHash, ok := metadataRow["pixel_hash"].(string)
	if !ok {
		t.Fatalf("metadata pixel_hash type = %T, want string", metadataRow["pixel_hash"])
	}

	if len(pixelHash) != 64 {
		t.Fatalf("len(metadata pixel_hash) = %d, want 64", len(pixelHash))
	}

	if metadataRow["has_transparency"] != false {
		t.Fatalf("metadata has_transparency = %v, want false", metadataRow["has_transparency"])
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

	if !bytes.Equal(contentRR.Body.Bytes(), sourceBytes) {
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

	db := openSQLiteForAppTest(t, path)
	defer db.Close()

	if _, err := db.Exec(`PRAGMA user_version = 0;`); err != nil {
		t.Fatalf("Exec(PRAGMA user_version) error = %v", err)
	}
}

func openSQLiteForAppTest(t *testing.T, path string) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open(%q) error = %v", path, err)
	}

	if _, err := db.Exec(`PRAGMA synchronous = OFF;`); err != nil {
		_ = db.Close()
		t.Fatalf("Exec(PRAGMA synchronous = OFF) error = %v", err)
	}

	return db
}

func createThinClientBundle(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	mainPath := filepath.Join(dir, "client.db")
	masterPath := filepath.Join(dir, "client.master.db")
	cachesPath := filepath.Join(dir, "client.caches.db")
	mappingsPath := filepath.Join(dir, "client.mappings.db")

	mainDB := openSQLiteForAppTest(t, mainPath)
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

	masterDB := openSQLiteForAppTest(t, masterPath)
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
	for y := range height {
		for x := range width {
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

func containsAppPermission(permissions []httpapi.Permission, want httpapi.Permission) bool {
	for _, permission := range permissions {
		if permission == want {
			return true
		}
	}

	return false
}

func waitForAppPTRStatus(
	t *testing.T,
	handler http.Handler,
	accessKey string,
	want func(coreptrsync.Status) bool,
) coreptrsync.Status {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	var lastStatus coreptrsync.Status
	for {
		lastStatus = mustAppPTRStatus(t, handler, accessKey)
		if want(lastStatus) {
			return lastStatus
		}

		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for PTR status, last status = %+v", lastStatus)
		}

		time.Sleep(25 * time.Millisecond)
	}
}

func mustAppPTRStatus(t *testing.T, handler http.Handler, accessKey string) coreptrsync.Status {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/service/ptr/status", nil)
	req.Header.Set("Hydrus-Client-API-Access-Key", accessKey)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("/service/ptr/status status = %d, want %d", rr.Code, http.StatusOK)
	}

	var payload struct {
		PTR coreptrsync.Status `json:"ptr"`
	}
	decodeAppJSON(t, rr.Body.Bytes(), &payload)

	return payload.PTR
}

func waitForAppRunStop(errCh <-chan error) error {
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			return fmt.Errorf("Run() error = %v, want context.Canceled", err)
		}
		return nil
	case <-time.After(5 * time.Second):
		return errors.New("timed out waiting for App.Run to stop")
	}
}

type hydrusDictEntry struct {
	key       string
	metaValue any
}

type metadataRow struct {
	updateIndex  int64
	updateHashes []string
	begin        int64
	end          int64
}

func hydrusArgsBytes(t *testing.T, entries ...hydrusDictEntry) []byte {
	t.Helper()
	return hydrusNetworkBytes(t, serialisableDictionary(entries...))
}

func hydrusNetworkBytes(t *testing.T, serialisable any) []byte {
	t.Helper()

	payload, err := json.Marshal(serialisable)
	if err != nil {
		t.Fatalf("json.Marshal(serialisable) error = %v", err)
	}

	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	if _, err := writer.Write(payload); err != nil {
		t.Fatalf("writer.Write() error = %v", err)
	}

	if err := writer.Close(); err != nil {
		t.Fatalf("writer.Close() error = %v", err)
	}

	return compressed.Bytes()
}

func serialisableDictionary(entries ...hydrusDictEntry) any {
	pairs := make([]any, 0, len(entries))
	for _, entry := range entries {
		pairs = append(pairs, []any{metaJSON(entry.key), entry.metaValue})
	}

	return []any{hydrusSerialisableTypeDictionary, 2, pairs}
}

func serialisableDictionaryString(t *testing.T, entries ...hydrusDictEntry) string {
	t.Helper()

	payload, err := json.Marshal(serialisableDictionary(entries...))
	if err != nil {
		t.Fatalf("json.Marshal(serialisableDictionary) error = %v", err)
	}

	return string(payload)
}

func serialisableTagFilter(rules map[string]int) any {
	items := make([]any, 0, len(rules))
	for key, rule := range rules {
		items = append(items, []any{key, rule})
	}

	return []any{hydrusSerialisableTypeTagFilter, 1, items}
}

func serialisableMetadata(nextUpdateDue int64, rows ...metadataRow) any {
	serialisableRows := make([]any, 0, len(rows))
	for _, row := range rows {
		serialisableRows = append(serialisableRows, []any{row.updateIndex, row.updateHashes, row.begin, row.end})
	}

	return []any{hydrusSerialisableTypeMetadata, 1, []any{serialisableRows, nextUpdateDue}}
}

func unsupportedSerialisable(serialisableType int) any {
	return []any{serialisableType, 1, []any{}}
}

func metaJSON(value any) any {
	return []any{hydrusMetaTypeJSONOK, value}
}

func metaHydrus(value any) any {
	return []any{hydrusMetaTypeHydrusSerializable, value}
}

func sha256Hex(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func testAppPTRConfigFromServer(t *testing.T, rawURL string, accessKey string) coreptrsync.Config {
	t.Helper()

	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("url.Parse(%q) error = %v", rawURL, err)
	}

	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatalf("Atoi(port=%q) error = %v", parsed.Port(), err)
	}

	return coreptrsync.Config{
		Enabled:     true,
		Host:        parsed.Hostname(),
		Port:        port,
		AccessKey:   accessKey,
		ServiceName: coreptrsync.DefaultServiceName,
	}
}

const (
	hydrusMetaTypeJSONOK             = 0
	hydrusMetaTypeHydrusSerializable = 2

	hydrusSerialisableTypeDictionary        = 21
	hydrusSerialisableTypeDefinitionsUpdate = 36
	hydrusSerialisableTypeMetadata          = 37
	hydrusSerialisableTypeTagFilter         = 44
)

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
