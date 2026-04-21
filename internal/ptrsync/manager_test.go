package ptrsync

import (
	"context"
	"database/sql"
	"encoding/hex"
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

		managedLayout, err := writeBundle.ManagedLayout(context.Background())
		if err != nil {
			t.Fatalf("ManagedLayout() error = %v", err)
		}

		managedPath, err := managedLayout.ResolveFilePath(updateHash, "")
		if err != nil {
			t.Fatalf("ResolveFilePath() error = %v", err)
		}

		managedBytes, err := os.ReadFile(managedPath)
		if err != nil {
			t.Fatalf("ReadFile(managedPath) error = %v", err)
		}

		if string(managedBytes) != string(updateBody) {
			t.Fatal("managed update bytes mismatch")
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
