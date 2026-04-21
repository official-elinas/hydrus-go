package ptrsync

import (
	"context"
	"database/sql"
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
					metaValue: metaHydrus(serialisableMetadata(1700000200, metadataRow{updateIndex: 0, updateHashes: []string{strings.Repeat("11", 32)}, begin: 10, end: 20})),
				}))
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
					metaValue: metaHydrus(serialisableMetadata(1700000200, metadataRow{updateIndex: 0, updateHashes: []string{strings.Repeat("11", 32)}, begin: 10, end: 20})),
				}))
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
}

func TestManagerTrigger(t *testing.T) {
	t.Run("starts one background run and deduplicates repeated triggers", func(t *testing.T) {
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
						metadataRow{updateIndex: 0, updateHashes: []string{strings.Repeat("11", 32)}, begin: 10, end: 20},
					)),
				}))
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

	masterDB := openSQLiteForPTRManagerTest(t, filepath.Join(dir, "client.master.db"))
	defer masterDB.Close()
	mustExecPTRManagerTest(t, masterDB, `CREATE TABLE hashes (hash_id INTEGER PRIMARY KEY AUTOINCREMENT, hash BLOB UNIQUE);`)

	cachesDB := openSQLiteForPTRManagerTest(t, filepath.Join(dir, "client.caches.db"))
	defer cachesDB.Close()

	mappingsDB := openSQLiteForPTRManagerTest(t, filepath.Join(dir, "client.mappings.db"))
	defer mappingsDB.Close()

	return dir
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
