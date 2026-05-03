package ptrsync

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	coreptrsync "github.com/official-elinas/hydrus-go/internal/core/ptrsync"
	"github.com/official-elinas/hydrus-go/internal/db/hydrusdb"
	_ "modernc.org/sqlite"
)

func TestManagerSyncOnce(t *testing.T) {
	t.Run("fetches and persists one real remote snapshot pass", func(t *testing.T) {
		dir := createPTRManagerTestBundle(t)
		updateBody := hydrusNetworkBytes(t, []any{hydrusSerialisableTypeDefinitionsUpdate, 1, []any{}})
		updateHash := sha256Hex(updateBody)

		readBundle, err := hydrusdb.Open(context.Background(), dir)
		if err != nil {
			t.Fatalf("hydrusdb.Open() error = %v", err)
		}
		defer func() {
			if err := readBundle.Close(); err != nil {
				t.Fatalf("readBundle.Close() error = %v", err)
			}
		}()

		writeBundle, err := hydrusdb.OpenWritable(context.Background(), dir)
		if err != nil {
			t.Fatalf("hydrusdb.OpenWritable() error = %v", err)
		}
		defer func() {
			if err := writeBundle.Close(); err != nil {
				t.Fatalf("writeBundle.Close() error = %v", err)
			}
		}()

		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/session_key":
				http.SetCookie(w, &http.Cookie{Name: "session_key", Value: "manager-session", Path: "/"})
				w.WriteHeader(http.StatusOK)
			case "/account":
				if err := validateSessionCookie(r); err != nil {
					t.Error(err)
					http.Error(w, err.Error(), http.StatusUnauthorized)
					return
				}
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
				if err := validateSessionCookie(r); err != nil {
					t.Error(err)
					http.Error(w, err.Error(), http.StatusUnauthorized)
					return
				}
				_, _ = w.Write(hydrusArgsBytes(t, hydrusDictEntry{
					key:       "service_options",
					metaValue: metaHydrus(serialisableDictionary(hydrusDictEntry{key: "update_period", metaValue: metaJSON(int64(3600))}, hydrusDictEntry{key: "nullification_period", metaValue: metaJSON(int64(86400))})),
				}))
			case "/tag_filter":
				if err := validateSessionCookie(r); err != nil {
					t.Error(err)
					http.Error(w, err.Error(), http.StatusUnauthorized)
					return
				}
				_, _ = w.Write(hydrusArgsBytes(t, hydrusDictEntry{
					key:       "tag_filter",
					metaValue: metaHydrus(serialisableTagFilter(map[string]int{":": 1, "creator:": 0})),
				}))
			case "/metadata":
				if err := validateSessionCookie(r); err != nil {
					t.Error(err)
					http.Error(w, err.Error(), http.StatusUnauthorized)
					return
				}
				_, _ = w.Write(hydrusArgsBytes(t, hydrusDictEntry{
					key:       "metadata_slice",
					metaValue: metaHydrus(serialisableMetadata(1700000200, metadataRow{updateIndex: 0, updateHashes: []string{updateHash}, begin: 10, end: 20})),
				}))
			case "/update":
				if got := r.URL.Query().Get("update_hash"); got != updateHash {
					t.Fatalf("update_hash = %q, want %q", got, updateHash)
				}
				_, _ = w.Write(updateBody)
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()

		manager, err := NewManager(context.Background(), nil, testPTRConfigFromServer(t, server.URL, defaultManagerAccessKey()), readBundle, writeBundle)
		if err != nil {
			t.Fatalf("NewManager() error = %v", err)
		}

		status, err := manager.SyncOnce(context.Background())
		if err != nil {
			t.Fatalf("SyncOnce() error = %v", err)
		}

		if status.Phase != coreptrsync.PhaseIdle {
			t.Fatalf("status.Phase = %q, want %q", status.Phase, coreptrsync.PhaseIdle)
		}

		if status.IsRunning {
			t.Fatal("status.IsRunning = true, want false")
		}

		if status.MetadataSlice != 1 {
			t.Fatalf("status.MetadataSlice = %d, want 1", status.MetadataSlice)
		}

		persisted, err := manager.Status(context.Background())
		if err != nil {
			t.Fatalf("manager.Status() error = %v", err)
		}

		if persisted.MetadataSlice != 1 {
			t.Fatalf("persisted.MetadataSlice = %d, want 1", persisted.MetadataSlice)
		}
	})

	t.Run("records daemon-visible failure state when remote fetch fails", func(t *testing.T) {
		dir := createPTRManagerTestBundle(t)

		readBundle, err := hydrusdb.Open(context.Background(), dir)
		if err != nil {
			t.Fatalf("hydrusdb.Open() error = %v", err)
		}
		defer func() {
			if err := readBundle.Close(); err != nil {
				t.Fatalf("readBundle.Close() error = %v", err)
			}
		}()

		writeBundle, err := hydrusdb.OpenWritable(context.Background(), dir)
		if err != nil {
			t.Fatalf("hydrusdb.OpenWritable() error = %v", err)
		}
		defer func() {
			if err := writeBundle.Close(); err != nil {
				t.Fatalf("writeBundle.Close() error = %v", err)
			}
		}()

		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/session_key":
				http.SetCookie(w, &http.Cookie{Name: "session_key", Value: "manager-session", Path: "/"})
				w.WriteHeader(http.StatusOK)
			case "/account":
				http.Error(w, "boom", http.StatusInternalServerError)
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()

		manager, err := NewManager(context.Background(), nil, testPTRConfigFromServer(t, server.URL, defaultManagerAccessKey()), readBundle, writeBundle)
		if err != nil {
			t.Fatalf("NewManager() error = %v", err)
		}

		status, err := manager.SyncOnce(context.Background())
		if err == nil {
			t.Fatal("SyncOnce() error = nil, want fetch failure")
		}

		if status.Phase != coreptrsync.PhaseIdle {
			t.Fatalf("status.Phase = %q, want %q", status.Phase, coreptrsync.PhaseIdle)
		}

		if status.IsRunning {
			t.Fatal("status.IsRunning = true, want false")
		}

		if !strings.Contains(status.LastError, "/account") {
			t.Fatalf("status.LastError = %q, want account failure detail", status.LastError)
		}
	})

	t.Run("persists remote busy responses as retrying with a ten minute pause", func(t *testing.T) {
		dir := createPTRManagerTestBundle(t)

		readBundle, err := hydrusdb.Open(context.Background(), dir)
		if err != nil {
			t.Fatalf("hydrusdb.Open() error = %v", err)
		}
		defer func() {
			if err := readBundle.Close(); err != nil {
				t.Fatalf("readBundle.Close() error = %v", err)
			}
		}()

		writeBundle, err := hydrusdb.OpenWritable(context.Background(), dir)
		if err != nil {
			t.Fatalf("hydrusdb.OpenWritable() error = %v", err)
		}
		defer func() {
			if err := writeBundle.Close(); err != nil {
				t.Fatalf("writeBundle.Close() error = %v", err)
			}
		}()

		var accountAttempts atomic.Int32
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/session_key":
				http.SetCookie(w, &http.Cookie{Name: "session_key", Value: "manager-session", Path: "/"})
				w.WriteHeader(http.StatusOK)
			case "/account":
				accountAttempts.Add(1)
				w.Header().Set("Retry-After", "120")
				http.Error(
					w,
					`{"error":"This server is busy, please try again later.","exception_type":"ServerBusyException","status_code":503}`,
					http.StatusServiceUnavailable,
				)
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()

		manager, err := NewManager(context.Background(), nil, testPTRConfigFromServer(t, server.URL, defaultManagerAccessKey()), readBundle, writeBundle)
		if err != nil {
			t.Fatalf("NewManager() error = %v", err)
		}

		status, err := manager.SyncOnce(context.Background())
		if err != nil {
			t.Fatalf("SyncOnce() error = %v, want nil for retrying state", err)
		}

		if status.Phase != coreptrsync.PhaseRetrying {
			t.Fatalf("status.Phase = %q, want %q", status.Phase, coreptrsync.PhaseRetrying)
		}

		if status.IsRunning {
			t.Fatal("status.IsRunning = true, want false")
		}

		if status.RetryAttempt != 1 {
			t.Fatalf("status.RetryAttempt = %d, want 1", status.RetryAttempt)
		}

		minimumRetryAt := time.Now().UTC().Add(10*time.Minute - 5*time.Second).UnixMilli()
		if status.RetryAtMS < minimumRetryAt {
			t.Fatalf("status.RetryAtMS = %d, want at least %d", status.RetryAtMS, minimumRetryAt)
		}

		if got := accountAttempts.Load(); got != 1 {
			t.Fatalf("account attempts = %d, want 1", got)
		}

		triggered, err := manager.Trigger(context.Background())
		if err != nil {
			t.Fatalf("Trigger() error = %v", err)
		}

		if triggered.Phase != coreptrsync.PhaseRetrying {
			t.Fatalf("triggered.Phase = %q, want %q", triggered.Phase, coreptrsync.PhaseRetrying)
		}

		if got := accountAttempts.Load(); got != 1 {
			t.Fatalf("account attempts after Trigger = %d, want 1", got)
		}
	})

	t.Run("returns a server issue after too many busy retries", func(t *testing.T) {
		dir := createPTRManagerTestBundle(t)

		readBundle, err := hydrusdb.Open(context.Background(), dir)
		if err != nil {
			t.Fatalf("hydrusdb.Open() error = %v", err)
		}
		defer func() {
			if err := readBundle.Close(); err != nil {
				t.Fatalf("readBundle.Close() error = %v", err)
			}
		}()

		writeBundle, err := hydrusdb.OpenWritable(context.Background(), dir)
		if err != nil {
			t.Fatalf("hydrusdb.OpenWritable() error = %v", err)
		}
		defer func() {
			if err := writeBundle.Close(); err != nil {
				t.Fatalf("writeBundle.Close() error = %v", err)
			}
		}()

		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/session_key":
				http.SetCookie(w, &http.Cookie{Name: "session_key", Value: "manager-session", Path: "/"})
				w.WriteHeader(http.StatusOK)
			case "/account":
				http.Error(
					w,
					`{"error":"This server is busy, please try again later.","exception_type":"ServerBusyException","status_code":503}`,
					http.StatusServiceUnavailable,
				)
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()

		manager, err := NewManager(context.Background(), nil, testPTRConfigFromServer(t, server.URL, defaultManagerAccessKey()), readBundle, writeBundle)
		if err != nil {
			t.Fatalf("NewManager() error = %v", err)
		}

		mainDB := openSQLiteForPTRManagerTest(t, filepath.Join(dir, "client.db"))
		mustExecPTRManagerTest(
			t,
			mainDB,
			`UPDATE main.ptr_sync_state SET retry_attempt = ?, retry_at_ms = 0, phase = ? WHERE singleton = ?`,
			ptrSyncMaxBusyRetryAttempts,
			coreptrsync.PhaseIdle,
			1,
		)
		if err := mainDB.Close(); err != nil {
			t.Fatalf("mainDB.Close() error = %v", err)
		}

		status, err := manager.SyncOnce(context.Background())
		if err == nil || !strings.Contains(err.Error(), "PTR server issue") {
			t.Fatalf("SyncOnce() error = %v, want server issue error", err)
		}

		if status.Phase != coreptrsync.PhaseIdle {
			t.Fatalf("status.Phase = %q, want %q", status.Phase, coreptrsync.PhaseIdle)
		}

		if !strings.Contains(status.LastError, "PTR server issue") {
			t.Fatalf("status.LastError = %q, want server issue detail", status.LastError)
		}
	})

	t.Run("cleans up the runtime lease when success persistence fails", func(t *testing.T) {
		dir := createPTRManagerTestBundle(t)
		updateBody := hydrusNetworkBytes(t, []any{hydrusSerialisableTypeDefinitionsUpdate, 1, []any{}})
		updateHash := sha256Hex(updateBody)

		readBundle, err := hydrusdb.Open(context.Background(), dir)
		if err != nil {
			t.Fatalf("hydrusdb.Open() error = %v", err)
		}
		defer func() {
			if err := readBundle.Close(); err != nil {
				t.Fatalf("readBundle.Close() error = %v", err)
			}
		}()

		writeBundle, err := hydrusdb.OpenWritable(context.Background(), dir)
		if err != nil {
			t.Fatalf("hydrusdb.OpenWritable() error = %v", err)
		}
		defer func() {
			if err := writeBundle.Close(); err != nil {
				t.Fatalf("writeBundle.Close() error = %v", err)
			}
		}()

		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/session_key":
				http.SetCookie(w, &http.Cookie{Name: "session_key", Value: "manager-session", Path: "/"})
				w.WriteHeader(http.StatusOK)
			case "/account":
				if err := validateSessionCookie(r); err != nil {
					t.Error(err)
					http.Error(w, err.Error(), http.StatusUnauthorized)
					return
				}
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
				if err := validateSessionCookie(r); err != nil {
					t.Error(err)
					http.Error(w, err.Error(), http.StatusUnauthorized)
					return
				}
				_, _ = w.Write(hydrusArgsBytes(t, hydrusDictEntry{
					key:       "service_options",
					metaValue: metaHydrus(serialisableDictionary(hydrusDictEntry{key: "update_period", metaValue: metaJSON(int64(3600))}, hydrusDictEntry{key: "nullification_period", metaValue: metaJSON(int64(86400))})),
				}))
			case "/tag_filter":
				if err := validateSessionCookie(r); err != nil {
					t.Error(err)
					http.Error(w, err.Error(), http.StatusUnauthorized)
					return
				}
				_, _ = w.Write(hydrusArgsBytes(t, hydrusDictEntry{
					key:       "tag_filter",
					metaValue: metaHydrus(serialisableTagFilter(map[string]int{":": 1, "creator:": 0})),
				}))
			case "/metadata":
				if err := validateSessionCookie(r); err != nil {
					t.Error(err)
					http.Error(w, err.Error(), http.StatusUnauthorized)
					return
				}
				_, _ = w.Write(hydrusArgsBytes(t, hydrusDictEntry{
					key:       "metadata_slice",
					metaValue: metaHydrus(serialisableMetadata(1700000200, metadataRow{updateIndex: 0, updateHashes: []string{updateHash}, begin: 10, end: 20})),
				}))
			case "/update":
				if got := r.URL.Query().Get("update_hash"); got != updateHash {
					t.Fatalf("update_hash = %q, want %q", got, updateHash)
				}
				_, _ = w.Write(updateBody)
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()

		manager, err := NewManager(context.Background(), nil, testPTRConfigFromServer(t, server.URL, defaultManagerAccessKey()), readBundle, writeBundle)
		if err != nil {
			t.Fatalf("NewManager() error = %v", err)
		}

		masterDB := openSQLiteForPTRManagerTest(t, filepath.Join(dir, "client.master.db"))
		mustExecPTRManagerTest(t, masterDB, `DROP TABLE hashes;`)
		if err := masterDB.Close(); err != nil {
			t.Fatalf("masterDB.Close() error = %v", err)
		}

		status, err := manager.SyncOnce(context.Background())
		if err == nil {
			t.Fatal("first SyncOnce() error = nil, want persistence failure")
		}

		if status.Phase != coreptrsync.PhaseIdle {
			t.Fatalf("status.Phase = %q, want %q", status.Phase, coreptrsync.PhaseIdle)
		}

		if status.IsRunning {
			t.Fatal("status.IsRunning = true, want false")
		}

		masterDB = openSQLiteForPTRManagerTest(t, filepath.Join(dir, "client.master.db"))
		mustExecPTRManagerTest(t, masterDB, `CREATE TABLE hashes (hash_id INTEGER PRIMARY KEY AUTOINCREMENT, hash BLOB UNIQUE);`)
		if err := masterDB.Close(); err != nil {
			t.Fatalf("masterDB.Close() error = %v", err)
		}

		status, err = manager.SyncOnce(context.Background())
		if err != nil {
			t.Fatalf("second SyncOnce() error = %v", err)
		}

		if status.MetadataSlice != 1 {
			t.Fatalf("status.MetadataSlice = %d, want 1", status.MetadataSlice)
		}
	})

	t.Run("downloads and registers pending update files before finishing the run", func(t *testing.T) {
		dir := createPTRManagerTestBundle(t)

		readBundle, err := hydrusdb.Open(context.Background(), dir)
		if err != nil {
			t.Fatalf("hydrusdb.Open() error = %v", err)
		}
		defer func() {
			if err := readBundle.Close(); err != nil {
				t.Fatalf("readBundle.Close() error = %v", err)
			}
		}()

		writeBundle, err := hydrusdb.OpenWritable(context.Background(), dir)
		if err != nil {
			t.Fatalf("hydrusdb.OpenWritable() error = %v", err)
		}
		defer func() {
			if err := writeBundle.Close(); err != nil {
				t.Fatalf("writeBundle.Close() error = %v", err)
			}
		}()

		updateBody := hydrusNetworkBytes(t, []any{hydrusSerialisableTypeContentUpdate, 1, []any{}})
		updateHash := sha256Hex(updateBody)

		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/session_key":
				http.SetCookie(w, &http.Cookie{Name: "session_key", Value: "manager-session", Path: "/"})
				w.WriteHeader(http.StatusOK)
			case "/account":
				_, _ = w.Write(hydrusArgsBytes(t, hydrusDictEntry{key: "account", metaValue: metaJSON([]any{strings.Repeat("aa", 32), unsupportedSerialisable(102), int64(1699990000), nil, serialisableDictionaryString(t, hydrusDictEntry{key: "banned_info", metaValue: metaJSON(nil)}, hydrusDictEntry{key: "bandwidth_tracker", metaValue: metaHydrus(unsupportedSerialisable(39))}, hydrusDictEntry{key: "message", metaValue: metaJSON("shared read-only")}, hydrusDictEntry{key: "message_created", metaValue: metaJSON(int64(1699990100))})})}))
			case "/options":
				_, _ = w.Write(hydrusArgsBytes(t, hydrusDictEntry{key: "service_options", metaValue: metaHydrus(serialisableDictionary(hydrusDictEntry{key: "update_period", metaValue: metaJSON(int64(3600))}, hydrusDictEntry{key: "nullification_period", metaValue: metaJSON(int64(86400))}))}))
			case "/tag_filter":
				_, _ = w.Write(hydrusArgsBytes(t, hydrusDictEntry{key: "tag_filter", metaValue: metaHydrus(serialisableTagFilter(map[string]int{":": 1}))}))
			case "/metadata":
				_, _ = w.Write(hydrusArgsBytes(t, hydrusDictEntry{key: "metadata_slice", metaValue: metaHydrus(serialisableMetadata(1700000200, metadataRow{updateIndex: 0, updateHashes: []string{updateHash}, begin: 10, end: 20}))}))
			case "/update":
				if got := r.URL.Query().Get("update_hash"); got != updateHash {
					t.Fatalf("update_hash = %q, want %q", got, updateHash)
				}
				_, _ = w.Write(updateBody)
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()

		manager, err := NewManager(context.Background(), nil, testPTRConfigFromServer(t, server.URL, defaultManagerAccessKey()), readBundle, writeBundle)
		if err != nil {
			t.Fatalf("NewManager() error = %v", err)
		}

		status, err := manager.SyncOnce(context.Background())
		if err != nil {
			t.Fatalf("SyncOnce() error = %v", err)
		}

		if status.DownloadedUpdateCount != 1 {
			t.Fatalf("status.DownloadedUpdateCount = %d, want 1", status.DownloadedUpdateCount)
		}

		if !status.IsComplete {
			t.Fatal("status.IsComplete = false, want true")
		}

		managedLayout, err := writeBundle.ManagedLayout(context.Background())
		if err != nil {
			t.Fatalf("ManagedLayout() error = %v", err)
		}

		managedPath, err := managedLayout.ResolveFilePath(updateHash, "")
		if err != nil {
			t.Fatalf("ResolveFilePath() error = %v", err)
		}

		if _, err := os.Stat(managedPath); !os.IsNotExist(err) {
			t.Fatalf("managedPath stat err = %v, want not exists", err)
		}

		artifactPath, err := resolvePTRUpdateArtifactPath(writeBundle, updateHash)
		if err != nil {
			t.Fatalf("resolvePTRUpdateArtifactPath() error = %v", err)
		}

		artifactBytes, err := os.ReadFile(artifactPath)
		if err != nil {
			t.Fatalf("ReadFile(artifactPath) error = %v", err)
		}

		if string(artifactBytes) != string(updateBody) {
			t.Fatal("PTR update artifact bytes mismatch")
		}

		serviceKey := hex.EncodeToString([]byte("repository updates"))
		service, ok, err := writeBundle.ByKey(context.Background(), serviceKey)
		if err != nil {
			t.Fatalf("ByKey(repository updates) error = %v", err)
		}
		if !ok {
			t.Fatal("repository updates service missing")
		}

		if service.Type != 20 {
			t.Fatalf("repository updates service type = %d, want 20", service.Type)
		}
	})

	t.Run("reuses a stored pending update artifact without re-downloading it", func(t *testing.T) {
		dir := createPTRManagerTestBundle(t)

		readBundle, err := hydrusdb.Open(context.Background(), dir)
		if err != nil {
			t.Fatalf("hydrusdb.Open() error = %v", err)
		}
		defer func() {
			if err := readBundle.Close(); err != nil {
				t.Fatalf("readBundle.Close() error = %v", err)
			}
		}()

		writeBundle, err := hydrusdb.OpenWritable(context.Background(), dir)
		if err != nil {
			t.Fatalf("hydrusdb.OpenWritable() error = %v", err)
		}
		defer func() {
			if err := writeBundle.Close(); err != nil {
				t.Fatalf("writeBundle.Close() error = %v", err)
			}
		}()

		updateBody := hydrusNetworkBytes(t, []any{hydrusSerialisableTypeContentUpdate, 1, []any{}})
		updateHash := sha256Hex(updateBody)

		if _, _, err := storePTRUpdateArtifact(writeBundle, updateHash, updateBody); err != nil {
			t.Fatalf("storePTRUpdateArtifact() error = %v", err)
		}

		var updateCalls atomic.Int32
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/session_key":
				http.SetCookie(w, &http.Cookie{Name: "session_key", Value: "manager-session", Path: "/"})
				w.WriteHeader(http.StatusOK)
			case "/account":
				_, _ = w.Write(hydrusArgsBytes(t, hydrusDictEntry{key: "account", metaValue: metaJSON([]any{strings.Repeat("aa", 32), unsupportedSerialisable(102), int64(1699990000), nil, serialisableDictionaryString(t, hydrusDictEntry{key: "banned_info", metaValue: metaJSON(nil)}, hydrusDictEntry{key: "bandwidth_tracker", metaValue: metaHydrus(unsupportedSerialisable(39))}, hydrusDictEntry{key: "message", metaValue: metaJSON("shared read-only")}, hydrusDictEntry{key: "message_created", metaValue: metaJSON(int64(1699990100))})})}))
			case "/options":
				_, _ = w.Write(hydrusArgsBytes(t, hydrusDictEntry{key: "service_options", metaValue: metaHydrus(serialisableDictionary(hydrusDictEntry{key: "update_period", metaValue: metaJSON(int64(3600))}, hydrusDictEntry{key: "nullification_period", metaValue: metaJSON(int64(86400))}))}))
			case "/tag_filter":
				_, _ = w.Write(hydrusArgsBytes(t, hydrusDictEntry{key: "tag_filter", metaValue: metaHydrus(serialisableTagFilter(map[string]int{":": 1}))}))
			case "/metadata":
				_, _ = w.Write(hydrusArgsBytes(t, hydrusDictEntry{key: "metadata_slice", metaValue: metaHydrus(serialisableMetadata(1700000200, metadataRow{updateIndex: 0, updateHashes: []string{updateHash}, begin: 10, end: 20}))}))
			case "/update":
				updateCalls.Add(1)
				t.Fatalf("/update should not be called when the PTR artifact is already stored locally")
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()

		manager, err := NewManager(context.Background(), nil, testPTRConfigFromServer(t, server.URL, defaultManagerAccessKey()), readBundle, writeBundle)
		if err != nil {
			t.Fatalf("NewManager() error = %v", err)
		}

		status, err := manager.SyncOnce(context.Background())
		if err != nil {
			t.Fatalf("SyncOnce() error = %v", err)
		}

		if got := updateCalls.Load(); got != 0 {
			t.Fatalf("update call count = %d, want 0", got)
		}

		if status.DownloadedUpdateCount != 1 {
			t.Fatalf("status.DownloadedUpdateCount = %d, want 1", status.DownloadedUpdateCount)
		}

		if status.Phase != coreptrsync.PhaseIdle {
			t.Fatalf("status.Phase = %q, want %q", status.Phase, coreptrsync.PhaseIdle)
		}

		if status.IsRunning {
			t.Fatal("status.IsRunning = true, want false")
		}

		if !status.IsComplete {
			t.Fatal("status.IsComplete = false, want true")
		}
	})

	t.Run("registers multiple pending updates in one successful run", func(t *testing.T) {
		dir := createPTRManagerTestBundle(t)

		readBundle, err := hydrusdb.Open(context.Background(), dir)
		if err != nil {
			t.Fatalf("hydrusdb.Open() error = %v", err)
		}
		defer func() {
			if err := readBundle.Close(); err != nil {
				t.Fatalf("readBundle.Close() error = %v", err)
			}
		}()

		writeBundle, err := hydrusdb.OpenWritable(context.Background(), dir)
		if err != nil {
			t.Fatalf("hydrusdb.OpenWritable() error = %v", err)
		}
		defer func() {
			if err := writeBundle.Close(); err != nil {
				t.Fatalf("writeBundle.Close() error = %v", err)
			}
		}()

		updateBodies := [][]byte{
			hydrusNetworkBytes(t, []any{hydrusSerialisableTypeContentUpdate, 1, []any{}}),
			hydrusNetworkBytes(t, []any{hydrusSerialisableTypeContentUpdate, 2, []any{}}),
			hydrusNetworkBytes(t, []any{hydrusSerialisableTypeDefinitionsUpdate, 3, []any{}}),
		}
		updateHashes := make([]string, 0, len(updateBodies))
		updateBodiesByHash := make(map[string][]byte, len(updateBodies))
		for _, body := range updateBodies {
			hash := sha256Hex(body)
			updateHashes = append(updateHashes, hash)
			updateBodiesByHash[hash] = body
		}

		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/session_key":
				http.SetCookie(w, &http.Cookie{Name: "session_key", Value: "manager-session", Path: "/"})
				w.WriteHeader(http.StatusOK)
			case "/account":
				_, _ = w.Write(hydrusArgsBytes(t, hydrusDictEntry{key: "account", metaValue: metaJSON([]any{strings.Repeat("aa", 32), unsupportedSerialisable(102), int64(1699990000), nil, serialisableDictionaryString(t, hydrusDictEntry{key: "banned_info", metaValue: metaJSON(nil)}, hydrusDictEntry{key: "bandwidth_tracker", metaValue: metaHydrus(unsupportedSerialisable(39))}, hydrusDictEntry{key: "message", metaValue: metaJSON("shared read-only")}, hydrusDictEntry{key: "message_created", metaValue: metaJSON(int64(1699990100))})})}))
			case "/options":
				_, _ = w.Write(hydrusArgsBytes(t, hydrusDictEntry{key: "service_options", metaValue: metaHydrus(serialisableDictionary(hydrusDictEntry{key: "update_period", metaValue: metaJSON(int64(3600))}, hydrusDictEntry{key: "nullification_period", metaValue: metaJSON(int64(86400))}))}))
			case "/tag_filter":
				_, _ = w.Write(hydrusArgsBytes(t, hydrusDictEntry{key: "tag_filter", metaValue: metaHydrus(serialisableTagFilter(map[string]int{":": 1}))}))
			case "/metadata":
				_, _ = w.Write(hydrusArgsBytes(t, hydrusDictEntry{key: "metadata_slice", metaValue: metaHydrus(serialisableMetadata(1700000200, metadataRow{updateIndex: 0, updateHashes: updateHashes, begin: 10, end: 20}))}))
			case "/update":
				hash := r.URL.Query().Get("update_hash")
				body, ok := updateBodiesByHash[hash]
				if !ok {
					t.Fatalf("unexpected update_hash %q", hash)
				}
				_, _ = w.Write(body)
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()

		manager, err := NewManager(context.Background(), nil, testPTRConfigFromServer(t, server.URL, defaultManagerAccessKey()), readBundle, writeBundle)
		if err != nil {
			t.Fatalf("NewManager() error = %v", err)
		}

		status, err := manager.SyncOnce(context.Background())
		if err != nil {
			t.Fatalf("SyncOnce() error = %v", err)
		}

		if status.DownloadedUpdateCount != int64(len(updateHashes)) {
			t.Fatalf("status.DownloadedUpdateCount = %d, want %d", status.DownloadedUpdateCount, len(updateHashes))
		}

		persisted, err := manager.Status(context.Background())
		if err != nil {
			t.Fatalf("manager.Status() error = %v", err)
		}

		if persisted.DownloadedUpdateCount != int64(len(updateHashes)) {
			t.Fatalf("persisted.DownloadedUpdateCount = %d, want %d", persisted.DownloadedUpdateCount, len(updateHashes))
		}
	})

	t.Run("applies downloaded definitions and mappings into PTR mapping tables", func(t *testing.T) {
		dir := createPTRManagerTestBundle(t)

		readBundle, err := hydrusdb.Open(context.Background(), dir)
		if err != nil {
			t.Fatalf("hydrusdb.Open() error = %v", err)
		}
		defer func() {
			if err := readBundle.Close(); err != nil {
				t.Fatalf("readBundle.Close() error = %v", err)
			}
		}()

		writeBundle, err := hydrusdb.OpenWritable(context.Background(), dir)
		if err != nil {
			t.Fatalf("hydrusdb.OpenWritable() error = %v", err)
		}
		defer func() {
			if err := writeBundle.Close(); err != nil {
				t.Fatalf("writeBundle.Close() error = %v", err)
			}
		}()

		definitionsBody := hydrusNetworkBytes(t, []any{
			hydrusSerialisableTypeDefinitionsUpdate,
			1,
			[]any{
				[]any{hydrusDefinitionsTypeHashes, []any{[]any{int64(101), strings.Repeat("11", 32)}, []any{int64(102), strings.Repeat("22", 32)}}},
				[]any{hydrusDefinitionsTypeTags, []any{[]any{int64(201), "creator:alice"}, []any{int64(202), "old:tag"}}},
			},
		})
		mappingsBody := hydrusNetworkBytes(t, []any{
			hydrusSerialisableTypeContentUpdate,
			1,
			[]any{
				[]any{hydrusContentTypeMappings, []any{
					[]any{hydrusContentUpdateAdd, []any{[]any{int64(201), []any{int64(101), int64(102)}}}},
					[]any{hydrusContentUpdateDelete, []any{[]any{int64(202), []any{int64(101)}}}},
				}},
			},
		})

		definitionsHash := sha256Hex(definitionsBody)
		mappingsHash := sha256Hex(mappingsBody)

		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/session_key":
				http.SetCookie(w, &http.Cookie{Name: "session_key", Value: "manager-session", Path: "/"})
				w.WriteHeader(http.StatusOK)
			case "/account":
				_, _ = w.Write(hydrusArgsBytes(t, hydrusDictEntry{key: "account", metaValue: metaJSON([]any{strings.Repeat("aa", 32), unsupportedSerialisable(102), int64(1699990000), nil, serialisableDictionaryString(t, hydrusDictEntry{key: "banned_info", metaValue: metaJSON(nil)}, hydrusDictEntry{key: "bandwidth_tracker", metaValue: metaHydrus(unsupportedSerialisable(39))}, hydrusDictEntry{key: "message", metaValue: metaJSON("shared read-only")}, hydrusDictEntry{key: "message_created", metaValue: metaJSON(int64(1699990100))})})}))
			case "/options":
				_, _ = w.Write(hydrusArgsBytes(t, hydrusDictEntry{key: "service_options", metaValue: metaHydrus(serialisableDictionary(hydrusDictEntry{key: "update_period", metaValue: metaJSON(int64(3600))}, hydrusDictEntry{key: "nullification_period", metaValue: metaJSON(int64(86400))}))}))
			case "/tag_filter":
				_, _ = w.Write(hydrusArgsBytes(t, hydrusDictEntry{key: "tag_filter", metaValue: metaHydrus(serialisableTagFilter(map[string]int{":": 1}))}))
			case "/metadata":
				_, _ = w.Write(hydrusArgsBytes(t, hydrusDictEntry{key: "metadata_slice", metaValue: metaHydrus(serialisableMetadata(1700000200, metadataRow{updateIndex: 0, updateHashes: []string{definitionsHash}, begin: 10, end: 20}, metadataRow{updateIndex: 1, updateHashes: []string{mappingsHash}, begin: 21, end: 30}))}))
			case "/update":
				switch r.URL.Query().Get("update_hash") {
				case definitionsHash:
					_, _ = w.Write(definitionsBody)
				case mappingsHash:
					_, _ = w.Write(mappingsBody)
				default:
					t.Fatalf("unexpected update_hash %q", r.URL.Query().Get("update_hash"))
				}
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()

		manager, err := NewManager(context.Background(), nil, testPTRConfigFromServer(t, server.URL, defaultManagerAccessKey()), readBundle, writeBundle)
		if err != nil {
			t.Fatalf("NewManager() error = %v", err)
		}

		status, err := manager.SyncOnce(context.Background())
		if err != nil {
			t.Fatalf("SyncOnce() error = %v", err)
		}

		if status.ProcessedDefinitionCount != 1 {
			t.Fatalf("status.ProcessedDefinitionCount = %d, want 1", status.ProcessedDefinitionCount)
		}

		if status.ProcessedContentCount != 1 {
			t.Fatalf("status.ProcessedContentCount = %d, want 1", status.ProcessedContentCount)
		}

		serviceID := selectPTRManagerTestInt64(
			t,
			filepath.Join(dir, "client.db"),
			`SELECT service_id FROM services WHERE service_key = ?`,
			[]byte(coreptrsync.DaemonServiceKeyBytes()),
		)
		tagIDMapTableName := fmt.Sprintf("repository_tag_id_map_%d", serviceID)
		creatorTagID := selectPTRManagerTestInt64(
			t,
			filepath.Join(dir, "client.master.db"),
			fmt.Sprintf(`SELECT tag_id FROM %s WHERE service_tag_id = ?`, tagIDMapTableName),
			201,
		)
		oldTagID := selectPTRManagerTestInt64(
			t,
			filepath.Join(dir, "client.master.db"),
			fmt.Sprintf(`SELECT tag_id FROM %s WHERE service_tag_id = ?`, tagIDMapTableName),
			202,
		)

		if count := selectPTRManagerTestInt64(
			t,
			filepath.Join(dir, "client.mappings.db"),
			fmt.Sprintf(`SELECT COUNT(*) FROM current_mappings_%d WHERE tag_id = ?`, serviceID),
			creatorTagID,
		); count != 2 {
			t.Fatalf("current PTR mapping row count = %d, want 2", count)
		}

		if count := selectPTRManagerTestInt64(
			t,
			filepath.Join(dir, "client.mappings.db"),
			fmt.Sprintf(`SELECT COUNT(*) FROM deleted_mappings_%d WHERE tag_id = ?`, serviceID),
			oldTagID,
		); count != 1 {
			t.Fatalf("deleted PTR mapping row count = %d, want 1", count)
		}
	})
}

func TestManagerAutomaticallyResumesRetryingSyncAfterWakeup(t *testing.T) {
	dir := createPTRManagerTestBundle(t)
	updateBody := hydrusNetworkBytes(t, []any{hydrusSerialisableTypeDefinitionsUpdate, 1, []any{}})
	updateHash := sha256Hex(updateBody)

	bootstrapBundle, err := hydrusdb.OpenWritable(context.Background(), dir)
	if err != nil {
		t.Fatalf("hydrusdb.OpenWritable() bootstrap error = %v", err)
	}

	cfg := coreptrsync.DefaultConfig()
	cfg.Enabled = true
	if _, err := bootstrapBundle.RecoverPTRSyncFoundation(context.Background(), cfg); err != nil {
		t.Fatalf("RecoverPTRSyncFoundation() bootstrap error = %v", err)
	}
	if err := bootstrapBundle.Close(); err != nil {
		t.Fatalf("bootstrapBundle.Close() error = %v", err)
	}

	mainDB := openSQLiteForPTRManagerTest(t, filepath.Join(dir, "client.db"))
	mustExecPTRManagerTest(
		t,
		mainDB,
		`UPDATE main.ptr_sync_state SET phase = ?, is_running = 0, run_token = NULL, retry_at_ms = ?, retry_attempt = ? WHERE singleton = ?`,
		coreptrsync.PhaseRetrying,
		time.Now().Add(100*time.Millisecond).UnixMilli(),
		1,
		1,
	)
	if err := mainDB.Close(); err != nil {
		t.Fatalf("mainDB.Close() error = %v", err)
	}

	readBundle, err := hydrusdb.Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("hydrusdb.Open() error = %v", err)
	}
	defer func() {
		if err := readBundle.Close(); err != nil {
			t.Fatalf("readBundle.Close() error = %v", err)
		}
	}()

	writeBundle, err := hydrusdb.OpenWritable(context.Background(), dir)
	if err != nil {
		t.Fatalf("hydrusdb.OpenWritable() error = %v", err)
	}
	defer func() {
		if err := writeBundle.Close(); err != nil {
			t.Fatalf("writeBundle.Close() error = %v", err)
		}
	}()

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/session_key":
			http.SetCookie(w, &http.Cookie{Name: "session_key", Value: "manager-session", Path: "/"})
			w.WriteHeader(http.StatusOK)
		case "/account":
			_, _ = w.Write(hydrusArgsBytes(t, hydrusDictEntry{key: "account", metaValue: metaJSON([]any{strings.Repeat("aa", 32), unsupportedSerialisable(102), int64(1699990000), nil, serialisableDictionaryString(t, hydrusDictEntry{key: "banned_info", metaValue: metaJSON(nil)}, hydrusDictEntry{key: "bandwidth_tracker", metaValue: metaHydrus(unsupportedSerialisable(39))}, hydrusDictEntry{key: "message", metaValue: metaJSON("shared read-only")}, hydrusDictEntry{key: "message_created", metaValue: metaJSON(int64(1699990100))})})}))
		case "/options":
			_, _ = w.Write(hydrusArgsBytes(t, hydrusDictEntry{key: "service_options", metaValue: metaHydrus(serialisableDictionary(hydrusDictEntry{key: "update_period", metaValue: metaJSON(int64(3600))}, hydrusDictEntry{key: "nullification_period", metaValue: metaJSON(int64(86400))}))}))
		case "/tag_filter":
			_, _ = w.Write(hydrusArgsBytes(t, hydrusDictEntry{key: "tag_filter", metaValue: metaHydrus(serialisableTagFilter(map[string]int{":": 1}))}))
		case "/metadata":
			_, _ = w.Write(hydrusArgsBytes(t, hydrusDictEntry{key: "metadata_slice", metaValue: metaHydrus(serialisableMetadata(1700000200, metadataRow{updateIndex: 0, updateHashes: []string{updateHash}, begin: 10, end: 20}))}))
		case "/update":
			_, _ = w.Write(updateBody)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	manager, err := NewManager(context.Background(), nil, testPTRConfigFromServer(t, server.URL, defaultManagerAccessKey()), readBundle, writeBundle)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	status := waitForPTRStatus(t, manager, coreptrsync.PhaseIdle, false)
	if status.MetadataSlice != 1 {
		t.Fatalf("status.MetadataSlice = %d, want 1 after automatic retry wakeup", status.MetadataSlice)
	}
	if status.DownloadedUpdateCount != 1 {
		t.Fatalf("status.DownloadedUpdateCount = %d, want 1 after automatic retry wakeup", status.DownloadedUpdateCount)
	}
}

func TestManagerPendingMappings(t *testing.T) {
	t.Run("AddPendingMappings stages daemon-owned PTR mappings", func(t *testing.T) {
		dir := createPTRManagerTestBundle(t)

		masterDB := openSQLiteForPTRManagerTest(t, filepath.Join(dir, "client.master.db"))
		mustExecPTRManagerTest(
			t,
			masterDB,
			`INSERT INTO hashes (hash_id, hash) VALUES (?, ?), (?, ?);`,
			1,
			mustDecodeHexString(t, strings.Repeat("11", 32)),
			2,
			mustDecodeHexString(t, strings.Repeat("22", 32)),
		)
		masterDB.Close()

		readBundle, err := hydrusdb.Open(context.Background(), dir)
		if err != nil {
			t.Fatalf("hydrusdb.Open() error = %v", err)
		}
		defer func() {
			if err := readBundle.Close(); err != nil {
				t.Fatalf("readBundle.Close() error = %v", err)
			}
		}()

		writeBundle, err := hydrusdb.OpenWritable(context.Background(), dir)
		if err != nil {
			t.Fatalf("hydrusdb.OpenWritable() error = %v", err)
		}
		defer func() {
			if err := writeBundle.Close(); err != nil {
				t.Fatalf("writeBundle.Close() error = %v", err)
			}
		}()

		cfg := coreptrsync.DefaultConfig()
		cfg.Enabled = true

		manager, err := NewManager(context.Background(), nil, cfg, readBundle, writeBundle)
		if err != nil {
			t.Fatalf("NewManager() error = %v", err)
		}

		result, err := manager.AddPendingMappings(context.Background(), coreptrsync.PendingMappingsRequest{
			Hashes: []string{strings.Repeat("11", 32), strings.Repeat("22", 32)},
			Tags:   []string{"creator:alice", "series:zeta"},
		})
		if err != nil {
			t.Fatalf("AddPendingMappings() error = %v", err)
		}

		if result.ServiceKey != coreptrsync.DaemonServiceKeyHex() {
			t.Fatalf("result.ServiceKey = %q, want %q", result.ServiceKey, coreptrsync.DaemonServiceKeyHex())
		}

		if result.AddedMappings != 4 {
			t.Fatalf("result.AddedMappings = %d, want 4", result.AddedMappings)
		}

		groups, err := readBundle.ListPTRPendingMappingsForCommit(context.Background(), cfg, "")
		if err != nil {
			t.Fatalf("ListPTRPendingMappingsForCommit() error = %v", err)
		}

		if len(groups) != 2 {
			t.Fatalf("len(groups) = %d, want 2", len(groups))
		}
	})

	t.Run("CommitPending uploads grouped mappings and promotes local pending rows", func(t *testing.T) {
		dir := createPTRManagerTestBundle(t)

		masterDB := openSQLiteForPTRManagerTest(t, filepath.Join(dir, "client.master.db"))
		mustExecPTRManagerTest(
			t,
			masterDB,
			`INSERT INTO hashes (hash_id, hash) VALUES (?, ?), (?, ?);`,
			1,
			mustDecodeHexString(t, strings.Repeat("11", 32)),
			2,
			mustDecodeHexString(t, strings.Repeat("22", 32)),
		)
		masterDB.Close()

		readBundle, err := hydrusdb.Open(context.Background(), dir)
		if err != nil {
			t.Fatalf("hydrusdb.Open() error = %v", err)
		}
		defer func() {
			if err := readBundle.Close(); err != nil {
				t.Fatalf("readBundle.Close() error = %v", err)
			}
		}()

		writeBundle, err := hydrusdb.OpenWritable(context.Background(), dir)
		if err != nil {
			t.Fatalf("hydrusdb.OpenWritable() error = %v", err)
		}
		defer func() {
			if err := writeBundle.Close(); err != nil {
				t.Fatalf("writeBundle.Close() error = %v", err)
			}
		}()

		var updateCalls atomic.Int32
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/session_key":
				http.SetCookie(w, &http.Cookie{Name: "session_key", Value: "manager-session", Path: "/"})
				w.WriteHeader(http.StatusOK)
			case "/update":
				if err := validateSessionCookie(r); err != nil {
					t.Error(err)
					http.Error(w, err.Error(), http.StatusUnauthorized)
					return
				}

				updateCalls.Add(1)

				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatalf("io.ReadAll(update body) error = %v", err)
				}

				decoded, err := decodeHydrusNetworkBytes(body)
				if err != nil {
					t.Fatalf("decodeHydrusNetworkBytes(update body) error = %v", err)
				}

				serialisableDict := decoded.([]any)
				entries := serialisableDict[2].([]any)
				pair := entries[0].([]any)
				valueTuple := pair[1].([]any)
				clientUpdate := valueTuple[1].([]any)
				actions := clientUpdate[2].([]any)
				actionTuple := actions[0].([]any)
				if got, err := anyToInt(actionTuple[0]); err != nil || got != hydrusContentUpdatePend {
					t.Fatalf("action = %d, want %d", got, hydrusContentUpdatePend)
				}

				contentsAndReasons := actionTuple[1].([]any)
				if len(contentsAndReasons) != 1 {
					t.Fatalf("len(contentsAndReasons) = %d, want 1", len(contentsAndReasons))
				}

				w.WriteHeader(http.StatusOK)
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()

		manager, err := NewManager(context.Background(), nil, testPTRConfigFromServer(t, server.URL, defaultManagerAccessKey()), readBundle, writeBundle)
		if err != nil {
			t.Fatalf("NewManager() error = %v", err)
		}

		if _, err := manager.AddPendingMappings(context.Background(), coreptrsync.PendingMappingsRequest{
			Hashes: []string{strings.Repeat("11", 32), strings.Repeat("22", 32)},
			Tags:   []string{"creator:alice"},
		}); err != nil {
			t.Fatalf("AddPendingMappings() error = %v", err)
		}

		result, err := manager.CommitPending(context.Background(), coreptrsync.CommitPendingRequest{})
		if err != nil {
			t.Fatalf("CommitPending() error = %v", err)
		}

		if result.ServiceKey != coreptrsync.DaemonServiceKeyHex() {
			t.Fatalf("result.ServiceKey = %q, want %q", result.ServiceKey, coreptrsync.DaemonServiceKeyHex())
		}

		if result.CommittedMappings != 2 {
			t.Fatalf("result.CommittedMappings = %d, want 2", result.CommittedMappings)
		}

		if got := updateCalls.Load(); got != 1 {
			t.Fatalf("updateCalls = %d, want 1", got)
		}

		serviceID := selectPTRManagerTestInt64(
			t,
			filepath.Join(dir, "client.db"),
			`SELECT service_id FROM services WHERE service_key = ?`,
			mustDecodeHexString(t, coreptrsync.DaemonServiceKeyHex()),
		)

		if got := selectPTRManagerTestInt64(
			t,
			filepath.Join(dir, "client.mappings.db"),
			fmt.Sprintf(`SELECT COUNT(*) FROM pending_mappings_%d`, serviceID),
		); got != 0 {
			t.Fatalf("pending mapping row count = %d, want 0", got)
		}

		if got := selectPTRManagerTestInt64(
			t,
			filepath.Join(dir, "client.mappings.db"),
			fmt.Sprintf(`SELECT COUNT(*) FROM current_mappings_%d`, serviceID),
		); got != 2 {
			t.Fatalf("current mapping row count = %d, want 2", got)
		}
	})
}

func TestManagerTrigger(t *testing.T) {
	t.Run("starts one background run and deduplicates repeated triggers", func(t *testing.T) {
		dir := createPTRManagerTestBundle(t)
		updateBody := hydrusNetworkBytes(t, []any{hydrusSerialisableTypeDefinitionsUpdate, 1, []any{}})
		updateHash := sha256Hex(updateBody)

		readBundle, err := hydrusdb.Open(context.Background(), dir)
		if err != nil {
			t.Fatalf("hydrusdb.Open() error = %v", err)
		}
		defer func() {
			if err := readBundle.Close(); err != nil {
				t.Fatalf("readBundle.Close() error = %v", err)
			}
		}()

		writeBundle, err := hydrusdb.OpenWritable(context.Background(), dir)
		if err != nil {
			t.Fatalf("hydrusdb.OpenWritable() error = %v", err)
		}
		defer func() {
			if err := writeBundle.Close(); err != nil {
				t.Fatalf("writeBundle.Close() error = %v", err)
			}
		}()

		var accountCalls atomic.Int32
		accountStarted := make(chan struct{})
		accountRelease := make(chan struct{})

		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/session_key":
				http.SetCookie(w, &http.Cookie{Name: "session_key", Value: "manager-session", Path: "/"})
				w.WriteHeader(http.StatusOK)
			case "/account":
				if err := validateSessionCookie(r); err != nil {
					t.Error(err)
					http.Error(w, err.Error(), http.StatusUnauthorized)
					return
				}

				accountCalls.Add(1)
				select {
				case <-accountStarted:
				default:
					close(accountStarted)
				}

				<-accountRelease

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
				if err := validateSessionCookie(r); err != nil {
					t.Error(err)
					http.Error(w, err.Error(), http.StatusUnauthorized)
					return
				}

				_, _ = w.Write(hydrusArgsBytes(t, hydrusDictEntry{
					key: "service_options",
					metaValue: metaHydrus(serialisableDictionary(
						hydrusDictEntry{key: "update_period", metaValue: metaJSON(int64(3600))},
						hydrusDictEntry{key: "nullification_period", metaValue: metaJSON(int64(86400))},
					)),
				}))
			case "/tag_filter":
				if err := validateSessionCookie(r); err != nil {
					t.Error(err)
					http.Error(w, err.Error(), http.StatusUnauthorized)
					return
				}

				_, _ = w.Write(hydrusArgsBytes(t, hydrusDictEntry{
					key:       "tag_filter",
					metaValue: metaHydrus(serialisableTagFilter(map[string]int{":": 1, "creator:": 0})),
				}))
			case "/metadata":
				if err := validateSessionCookie(r); err != nil {
					t.Error(err)
					http.Error(w, err.Error(), http.StatusUnauthorized)
					return
				}

				_, _ = w.Write(hydrusArgsBytes(t, hydrusDictEntry{
					key: "metadata_slice",
					metaValue: metaHydrus(serialisableMetadata(
						1700000200,
						metadataRow{updateIndex: 0, updateHashes: []string{updateHash}, begin: 10, end: 20},
					)),
				}))
			case "/update":
				if got := r.URL.Query().Get("update_hash"); got != updateHash {
					t.Fatalf("update_hash = %q, want %q", got, updateHash)
				}
				<-accountRelease
				_, _ = w.Write(updateBody)
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()

		manager, err := NewManager(
			context.Background(),
			nil,
			testPTRConfigFromServer(t, server.URL, defaultManagerAccessKey()),
			readBundle,
			writeBundle,
		)
		if err != nil {
			t.Fatalf("NewManager() error = %v", err)
		}

		status, err := manager.Trigger(context.Background())
		if err != nil {
			t.Fatalf("Trigger() error = %v", err)
		}

		if status.Phase != coreptrsync.PhaseSyncing {
			t.Fatalf("status.Phase = %q, want %q", status.Phase, coreptrsync.PhaseSyncing)
		}

		if !status.IsRunning {
			t.Fatal("status.IsRunning = false, want true")
		}

		select {
		case <-accountStarted:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for background trigger to reach /account")
		}

		status, err = manager.Trigger(context.Background())
		if err != nil {
			t.Fatalf("second Trigger() error = %v", err)
		}

		if status.Phase != coreptrsync.PhaseSyncing {
			t.Fatalf("second status.Phase = %q, want %q", status.Phase, coreptrsync.PhaseSyncing)
		}

		if got := accountCalls.Load(); got != 1 {
			t.Fatalf("accountCalls = %d, want 1", got)
		}

		close(accountRelease)

		finalStatus := waitForPTRStatus(t, manager, coreptrsync.PhaseIdle, false)
		if finalStatus.MetadataSlice != 1 {
			t.Fatalf("finalStatus.MetadataSlice = %d, want 1", finalStatus.MetadataSlice)
		}
	})

	t.Run("shutdown cancels in-flight trigger and clears active lease", func(t *testing.T) {
		dir := createPTRManagerTestBundle(t)

		readBundle, err := hydrusdb.Open(context.Background(), dir)
		if err != nil {
			t.Fatalf("hydrusdb.Open() error = %v", err)
		}
		defer func() {
			if err := readBundle.Close(); err != nil {
				t.Fatalf("readBundle.Close() error = %v", err)
			}
		}()

		writeBundle, err := hydrusdb.OpenWritable(context.Background(), dir)
		if err != nil {
			t.Fatalf("hydrusdb.OpenWritable() error = %v", err)
		}
		defer func() {
			if err := writeBundle.Close(); err != nil {
				t.Fatalf("writeBundle.Close() error = %v", err)
			}
		}()

		accountStarted := make(chan struct{})

		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/session_key":
				http.SetCookie(w, &http.Cookie{Name: "session_key", Value: "manager-session", Path: "/"})
				w.WriteHeader(http.StatusOK)
			case "/account":
				if err := validateSessionCookie(r); err != nil {
					t.Error(err)
					http.Error(w, err.Error(), http.StatusUnauthorized)
					return
				}

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

		cfg := testPTRConfigFromServer(t, server.URL, defaultManagerAccessKey())
		manager, err := NewManager(context.Background(), nil, cfg, readBundle, writeBundle)
		if err != nil {
			t.Fatalf("NewManager() error = %v", err)
		}

		status, err := manager.Trigger(context.Background())
		if err != nil {
			t.Fatalf("Trigger() error = %v", err)
		}

		if status.Phase != coreptrsync.PhaseSyncing {
			t.Fatalf("status.Phase = %q, want %q", status.Phase, coreptrsync.PhaseSyncing)
		}

		select {
		case <-accountStarted:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for background trigger to reach /account")
		}

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := manager.Shutdown(shutdownCtx); err != nil {
			t.Fatalf("Shutdown() error = %v", err)
		}

		persisted, err := manager.Status(context.Background())
		if err != nil {
			t.Fatalf("Status() error = %v", err)
		}

		if persisted.IsRunning {
			t.Fatal("persisted.IsRunning = true, want false")
		}

		if persisted.LastError != "" {
			t.Fatalf("persisted.LastError = %q, want empty after shutdown cancellation", persisted.LastError)
		}

		lease, err := writeBundle.BeginPTRSync(context.Background(), cfg)
		if err != nil {
			t.Fatalf("BeginPTRSync() after shutdown error = %v", err)
		}

		if _, err := writeBundle.FinishPTRSyncFailure(context.Background(), cfg, lease.RunToken, "cleanup"); err != nil {
			t.Fatalf("FinishPTRSyncFailure() cleanup error = %v", err)
		}
	})
}

func createPTRManagerTestBundle(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	for _, name := range []string{"client.db", "client.master.db", "client.caches.db", "client.mappings.db"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte{}, 0o644); err != nil {
			t.Fatalf("os.WriteFile(%q) error = %v", name, err)
		}
	}

	mainDB := openSQLiteForPTRManagerTest(t, filepath.Join(dir, "client.db"))
	defer mainDB.Close()
	mustExecPTRManagerTest(t, mainDB, `CREATE TABLE services (service_id INTEGER PRIMARY KEY AUTOINCREMENT, service_key BLOB UNIQUE, service_type INTEGER, name TEXT, dictionary_string TEXT);`)
	mustExecPTRManagerTest(t, mainDB, `CREATE TABLE files_info (hash_id INTEGER PRIMARY KEY, size INTEGER, mime INTEGER, width INTEGER, height INTEGER, duration INTEGER, num_frames INTEGER, has_audio INTEGER, num_words INTEGER);`)
	mustExecPTRManagerTest(t, mainDB, `CREATE TABLE file_modified_timestamps (hash_id INTEGER PRIMARY KEY, file_modified_timestamp_ms INTEGER);`)
	mustExecPTRManagerTest(t, mainDB, `CREATE TABLE file_inbox (hash_id INTEGER PRIMARY KEY);`)
	mustExecPTRManagerTest(t, mainDB, `CREATE TABLE pixel_hash_map (hash_id INTEGER, pixel_hash_id INTEGER, PRIMARY KEY (hash_id, pixel_hash_id));`)
	mustExecPTRManagerTest(t, mainDB, `CREATE TABLE has_transparency (hash_id INTEGER PRIMARY KEY);`)
	mustExecPTRManagerTest(t, mainDB, `CREATE TABLE current_storage_granularity (granularity INTEGER);`)
	mustExecPTRManagerTest(t, mainDB, `CREATE TABLE current_client_files_locations (location_id INTEGER PRIMARY KEY, location TEXT UNIQUE);`)
	mustExecPTRManagerTest(t, mainDB, `CREATE TABLE client_files_subfolders (prefix TEXT, location_id INTEGER, PRIMARY KEY (prefix, location_id));`)
	mustExecPTRManagerTest(t, mainDB, `CREATE TABLE ideal_client_files_locations (location_id INTEGER PRIMARY KEY, weight INTEGER, max_num_bytes INTEGER);`)
	mustExecPTRManagerTest(t, mainDB, `CREATE TABLE ideal_thumbnail_override_location (location_id INTEGER);`)
	seedPTRManagerTestStorage(t, mainDB, dir)

	masterDB := openSQLiteForPTRManagerTest(t, filepath.Join(dir, "client.master.db"))
	defer masterDB.Close()
	mustExecPTRManagerTest(t, masterDB, `CREATE TABLE hashes (hash_id INTEGER PRIMARY KEY AUTOINCREMENT, hash BLOB UNIQUE);`)

	cachesDB := openSQLiteForPTRManagerTest(t, filepath.Join(dir, "client.caches.db"))
	defer cachesDB.Close()

	mappingsDB := openSQLiteForPTRManagerTest(t, filepath.Join(dir, "client.mappings.db"))
	defer mappingsDB.Close()

	return dir
}

func seedPTRManagerTestStorage(t *testing.T, db *sql.DB, dbDir string) {
	t.Helper()

	fileRoot := filepath.Join(dirClean(dbDir), "client_files")
	thumbnailRoot := filepath.Join(filepath.Dir(dirClean(dbDir)), "thumbnails")
	mustExecPTRManagerTest(t, db, `INSERT INTO current_storage_granularity (granularity) VALUES (2);`)
	mustExecPTRManagerTest(t, db, `INSERT INTO current_client_files_locations (location_id, location) VALUES (?, ?), (?, ?);`, 1, fileRoot, 2, thumbnailRoot)
	mustExecPTRManagerTest(t, db, `INSERT INTO ideal_client_files_locations (location_id, weight, max_num_bytes) VALUES (?, ?, NULL);`, 1, 1)
	mustExecPTRManagerTest(t, db, `INSERT INTO ideal_thumbnail_override_location (location_id) VALUES (?);`, 2)

	for _, prefix := range ptrManagerTestStoragePrefixes("f") {
		mustExecPTRManagerTest(t, db, `INSERT INTO client_files_subfolders (prefix, location_id) VALUES (?, ?);`, prefix, 1)
	}
	for _, prefix := range ptrManagerTestStoragePrefixes("t") {
		mustExecPTRManagerTest(t, db, `INSERT INTO client_files_subfolders (prefix, location_id) VALUES (?, ?);`, prefix, 2)
	}

	if err := os.MkdirAll(fileRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll(fileRoot) error = %v", err)
	}
	if err := os.MkdirAll(thumbnailRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll(thumbnailRoot) error = %v", err)
	}
}

func ptrManagerTestStoragePrefixes(kind string) []string {
	prefixes := make([]string, 0, 256)
	for _, first := range "0123456789abcdef" {
		for _, second := range "0123456789abcdef" {
			prefixes = append(prefixes, kind+string(first)+string(second))
		}
	}

	return prefixes
}

func dirClean(value string) string {
	return filepath.Clean(strings.TrimSpace(value))
}

func openSQLiteForPTRManagerTest(t *testing.T, path string) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open(%q) error = %v", path, err)
	}

	return db
}

func selectPTRManagerTestInt64(t *testing.T, path string, query string, args ...any) int64 {
	t.Helper()

	db := openSQLiteForPTRManagerTest(t, path)
	defer db.Close()

	var value int64
	if err := db.QueryRow(query, args...).Scan(&value); err != nil {
		t.Fatalf("db.QueryRow(%q) error = %v", query, err)
	}

	return value
}

func mustExecPTRManagerTest(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()

	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("db.Exec(%q) error = %v", query, err)
	}
}

func defaultManagerAccessKey() string {
	return "4a285629721ca442541ef2c15ea17d1f7f7578b0c3f4f5f2a05f8f0ab297786f"
}

func waitForPTRStatus(t *testing.T, manager *Manager, wantPhase string, wantRunning bool) coreptrsync.Status {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		status, err := manager.Status(context.Background())
		if err != nil {
			lastErr = err
			time.Sleep(10 * time.Millisecond)
			continue
		}

		if status.Phase == wantPhase && status.IsRunning == wantRunning {
			return status
		}

		time.Sleep(10 * time.Millisecond)
	}

	status, err := manager.Status(context.Background())
	if err != nil {
		if lastErr != nil {
			t.Fatalf("final Status() error = %v (last transient error: %v)", err, lastErr)
		}

		t.Fatalf("final Status() error = %v", err)
	}

	t.Fatalf(
		"timed out waiting for PTR status phase=%q running=%t; got phase=%q running=%t",
		wantPhase,
		wantRunning,
		status.Phase,
		status.IsRunning,
	)

	return coreptrsync.Status{}
}
