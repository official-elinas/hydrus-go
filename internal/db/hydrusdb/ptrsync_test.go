package hydrusdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	coreptrsync "github.com/official-elinas/hydrus-go/internal/core/ptrsync"
	"github.com/official-elinas/hydrus-go/internal/core/services"
)

func TestBundleEnsurePTRSyncFoundation(t *testing.T) {
	t.Run("creates public tag repository service, mapping tables, and sync state", func(t *testing.T) {
		dir, _ := createTestBundle(t)

		bundle, err := OpenWritable(context.Background(), dir)
		if err != nil {
			t.Fatalf("OpenWritable() error = %v", err)
		}
		defer func() {
			if err := bundle.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
		}()

		cfg := coreptrsync.DefaultConfig()
		cfg.Enabled = true

		status, err := bundle.EnsurePTRSyncFoundation(context.Background(), cfg)
		if err != nil {
			t.Fatalf("EnsurePTRSyncFoundation() error = %v", err)
		}

		if !status.Enabled {
			t.Fatal("status.Enabled = false, want true")
		}

		if !status.Configured {
			t.Fatal("status.Configured = false, want true")
		}

		if status.ServiceName != coreptrsync.DefaultServiceName {
			t.Fatalf("status.ServiceName = %q, want %q", status.ServiceName, coreptrsync.DefaultServiceName)
		}

		if status.ServiceKey != coreptrsync.DaemonServiceKeyHex() {
			t.Fatalf("status.ServiceKey = %q, want %q", status.ServiceKey, coreptrsync.DaemonServiceKeyHex())
		}

		if status.AccountMode != coreptrsync.AccountModeSharedReadOnly {
			t.Fatalf("status.AccountMode = %q, want %q", status.AccountMode, coreptrsync.AccountModeSharedReadOnly)
		}

		if status.Phase != coreptrsync.PhaseIdle {
			t.Fatalf("status.Phase = %q, want %q", status.Phase, coreptrsync.PhaseIdle)
		}

		service, ok, err := bundle.ByName(context.Background(), coreptrsync.DefaultServiceName)
		if err != nil {
			t.Fatalf("ByName() error = %v", err)
		}
		if !ok {
			t.Fatal("ByName() ok = false, want true")
		}

		if service.Type != services.TypeTagRepository {
			t.Fatalf("service.Type = %d, want %d", service.Type, services.TypeTagRepository)
		}

		serviceID := selectInt64(
			t,
			bundle.conn,
			`SELECT service_id FROM main.services WHERE name = ?`,
			coreptrsync.DefaultServiceName,
		)

		mappingsDB := openSQLiteForTest(t, filepath.Join(dir, "client.mappings.db"))
		defer mappingsDB.Close()
		mappingsConn, err := mappingsDB.Conn(context.Background())
		if err != nil {
			t.Fatalf("mappingsDB.Conn() error = %v", err)
		}
		defer mappingsConn.Close()

		for _, tableName := range []string{
			"current_mappings_" + strconv.FormatInt(serviceID, 10),
			"deleted_mappings_" + strconv.FormatInt(serviceID, 10),
			"pending_mappings_" + strconv.FormatInt(serviceID, 10),
			"petitioned_mappings_" + strconv.FormatInt(serviceID, 10),
		} {
			if !rowExistsInDB(
				t,
				mappingsConn,
				`SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?`,
				tableName,
			) {
				t.Fatalf("expected mappings table %q to exist", tableName)
			}
		}

		repositoryUpdatesTableName, repositoryUnregisteredTableName, repositoryProcessedTableName :=
			generatePTRRepositoryTableNames(serviceID)
		for _, tableName := range []string{
			repositoryUpdatesTableName,
			repositoryUnregisteredTableName,
			repositoryProcessedTableName,
			"ptr_sync_remote_state",
		} {
			if !rowExistsInDB(
				t,
				bundle.conn,
				`SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?`,
				tableName,
			) {
				t.Fatalf("expected main table %q to exist", tableName)
			}
		}

		for _, indexName := range []string{
			repositoryUpdatesTableName + "_hash_id_idx",
			repositoryProcessedTableName + "_content_type_idx",
		} {
			if !rowExistsInDB(
				t,
				bundle.conn,
				`SELECT 1 FROM sqlite_master WHERE type = 'index' AND name = ?`,
				indexName,
			) {
				t.Fatalf("expected main index %q to exist", indexName)
			}
		}

		if !rowExistsInDB(
			t,
			bundle.conn,
			`SELECT 1 FROM main.ptr_sync_state WHERE singleton = ? AND service_id = ?`,
			ptrSyncStateSingleton,
			serviceID,
		) {
			t.Fatal("ptr_sync_state row missing")
		}
	})

	t.Run("is idempotent across repeated ensures", func(t *testing.T) {
		dir, _ := createTestBundle(t)

		bundle, err := OpenWritable(context.Background(), dir)
		if err != nil {
			t.Fatalf("OpenWritable() error = %v", err)
		}
		defer func() {
			if err := bundle.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
		}()

		cfg := coreptrsync.DefaultConfig()
		cfg.Enabled = true

		first, err := bundle.EnsurePTRSyncFoundation(context.Background(), cfg)
		if err != nil {
			t.Fatalf("first EnsurePTRSyncFoundation() error = %v", err)
		}

		second, err := bundle.EnsurePTRSyncFoundation(context.Background(), cfg)
		if err != nil {
			t.Fatalf("second EnsurePTRSyncFoundation() error = %v", err)
		}

		if first.ServiceKey != second.ServiceKey {
			t.Fatalf("second.ServiceKey = %q, want %q", second.ServiceKey, first.ServiceKey)
		}

		count := selectInt64(
			t,
			bundle.conn,
			`SELECT COUNT(*) FROM main.services WHERE name = ?`,
			coreptrsync.DefaultServiceName,
		)
		if count != 1 {
			t.Fatalf("PTR service row count = %d, want 1", count)
		}
	})

	t.Run("reuses stable service key when display name changes", func(t *testing.T) {
		dir, _ := createTestBundle(t)

		bundle, err := OpenWritable(context.Background(), dir)
		if err != nil {
			t.Fatalf("OpenWritable() error = %v", err)
		}
		defer func() {
			if err := bundle.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
		}()

		cfg := coreptrsync.DefaultConfig()
		cfg.Enabled = true

		first, err := bundle.EnsurePTRSyncFoundation(context.Background(), cfg)
		if err != nil {
			t.Fatalf("first EnsurePTRSyncFoundation() error = %v", err)
		}

		cfg.ServiceName = "community ptr"
		second, err := bundle.EnsurePTRSyncFoundation(context.Background(), cfg)
		if err != nil {
			t.Fatalf("second EnsurePTRSyncFoundation() error = %v", err)
		}

		if second.ServiceKey != first.ServiceKey {
			t.Fatalf("second.ServiceKey = %q, want %q", second.ServiceKey, first.ServiceKey)
		}

		service, ok, err := bundle.ByKey(context.Background(), coreptrsync.DaemonServiceKeyHex())
		if err != nil {
			t.Fatalf("ByKey() error = %v", err)
		}
		if !ok {
			t.Fatal("ByKey() ok = false, want true")
		}

		if service.Name != "community ptr" {
			t.Fatalf("service.Name = %q, want %q", service.Name, "community ptr")
		}
	})

	t.Run("rejects renaming daemon-owned PTR service onto an existing name", func(t *testing.T) {
		dir, _ := createTestBundle(t)

		bundle, err := OpenWritable(context.Background(), dir)
		if err != nil {
			t.Fatalf("OpenWritable() error = %v", err)
		}
		defer func() {
			if err := bundle.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
		}()

		cfg := coreptrsync.DefaultConfig()
		cfg.Enabled = true

		if _, err := bundle.EnsurePTRSyncFoundation(context.Background(), cfg); err != nil {
			t.Fatalf("first EnsurePTRSyncFoundation() error = %v", err)
		}

		if _, err := bundle.conn.ExecContext(
			context.Background(),
			`INSERT INTO main.services (service_key, service_type, name, dictionary_string) VALUES (?, ?, ?, ?)`,
			[]byte("existing-community-ptr"),
			int(services.TypeTagRepository),
			"community ptr",
			"{}",
		); err != nil {
			t.Fatalf("INSERT conflicting service error = %v", err)
		}

		cfg.ServiceName = "community ptr"

		_, err = bundle.EnsurePTRSyncFoundation(context.Background(), cfg)
		if !errors.Is(err, ErrPTRServiceNameCollision) {
			t.Fatalf("EnsurePTRSyncFoundation() error = %v, want ErrPTRServiceNameCollision", err)
		}
	})

	t.Run("rejects renaming daemon-owned PTR service onto a case-variant existing name", func(t *testing.T) {
		dir, _ := createTestBundle(t)

		bundle, err := OpenWritable(context.Background(), dir)
		if err != nil {
			t.Fatalf("OpenWritable() error = %v", err)
		}
		defer func() {
			if err := bundle.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
		}()

		cfg := coreptrsync.DefaultConfig()
		cfg.Enabled = true

		if _, err := bundle.EnsurePTRSyncFoundation(context.Background(), cfg); err != nil {
			t.Fatalf("first EnsurePTRSyncFoundation() error = %v", err)
		}

		if _, err := bundle.conn.ExecContext(
			context.Background(),
			`INSERT INTO main.services (service_key, service_type, name, dictionary_string) VALUES (?, ?, ?, ?)`,
			[]byte("existing-community-ptr-case-variant"),
			int(services.TypeTagRepository),
			"Community PTR",
			"{}",
		); err != nil {
			t.Fatalf("INSERT case-variant conflicting service error = %v", err)
		}

		cfg.ServiceName = "community ptr"

		_, err = bundle.EnsurePTRSyncFoundation(context.Background(), cfg)
		if !errors.Is(err, ErrPTRServiceNameCollision) {
			t.Fatalf("EnsurePTRSyncFoundation() error = %v, want ErrPTRServiceNameCollision", err)
		}
	})

	t.Run("rejects renaming when the daemon service is the first folded match", func(t *testing.T) {
		dir, _ := createTestBundle(t)

		bundle, err := OpenWritable(context.Background(), dir)
		if err != nil {
			t.Fatalf("OpenWritable() error = %v", err)
		}
		defer func() {
			if err := bundle.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
		}()

		cfg := coreptrsync.DefaultConfig()
		cfg.Enabled = true
		cfg.ServiceName = "Community PTR"

		if _, err := bundle.EnsurePTRSyncFoundation(context.Background(), cfg); err != nil {
			t.Fatalf("first EnsurePTRSyncFoundation() error = %v", err)
		}

		if _, err := bundle.conn.ExecContext(
			context.Background(),
			`INSERT INTO main.services (service_key, service_type, name, dictionary_string) VALUES (?, ?, ?, ?)`,
			[]byte("existing-community-ptr-later"),
			int(services.TypeTagRepository),
			"community ptr",
			"{}",
		); err != nil {
			t.Fatalf("INSERT later folded-collision service error = %v", err)
		}

		cfg.ServiceName = "COMMUNITY PTR"

		_, err = bundle.EnsurePTRSyncFoundation(context.Background(), cfg)
		if !errors.Is(err, ErrPTRServiceNameCollision) {
			t.Fatalf("EnsurePTRSyncFoundation() error = %v, want ErrPTRServiceNameCollision", err)
		}
	})

	t.Run("normalizes stale runtime sync state back to idle", func(t *testing.T) {
		dir, _ := createTestBundle(t)

		bundle, err := OpenWritable(context.Background(), dir)
		if err != nil {
			t.Fatalf("OpenWritable() error = %v", err)
		}
		defer func() {
			if err := bundle.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
		}()

		cfg := coreptrsync.DefaultConfig()
		cfg.Enabled = true

		status, err := bundle.EnsurePTRSyncFoundation(context.Background(), cfg)
		if err != nil {
			t.Fatalf("EnsurePTRSyncFoundation() error = %v", err)
		}

		serviceID := selectInt64(
			t,
			bundle.conn,
			`SELECT service_id FROM main.services WHERE service_key = ?`,
			mustDecodeHex(t, status.ServiceKey),
		)

		if _, err := bundle.conn.ExecContext(
			context.Background(),
			`UPDATE main.ptr_sync_state
			SET phase = ?, is_running = 1, run_token = ?, last_error = ?, service_id = ?
			WHERE singleton = ?`,
			coreptrsync.PhaseSyncing,
			"stale-run-token",
			"stale worker crash",
			serviceID,
			ptrSyncStateSingleton,
		); err != nil {
			t.Fatalf("UPDATE ptr_sync_state error = %v", err)
		}

		normalized, err := bundle.RecoverPTRSyncFoundation(context.Background(), cfg)
		if err != nil {
			t.Fatalf("RecoverPTRSyncFoundation() error = %v", err)
		}

		if normalized.Phase != coreptrsync.PhaseIdle {
			t.Fatalf("normalized.Phase = %q, want %q", normalized.Phase, coreptrsync.PhaseIdle)
		}

		if normalized.IsRunning {
			t.Fatal("normalized.IsRunning = true, want false")
		}

		if normalized.LastError != "" {
			t.Fatalf("normalized.LastError = %q, want empty", normalized.LastError)
		}

		var runToken sql.NullString
		row := bundle.conn.QueryRowContext(
			context.Background(),
			`SELECT run_token FROM main.ptr_sync_state WHERE singleton = ?`,
			ptrSyncStateSingleton,
		)
		if err := row.Scan(&runToken); err != nil {
			t.Fatalf("SELECT ptr_sync_state.run_token error = %v", err)
		}

		if runToken.Valid {
			t.Fatalf("run_token = %q, want NULL", runToken.String)
		}
	})

	t.Run("ensure foundation does not steal an active sync lease", func(t *testing.T) {
		dir, _ := createTestBundle(t)

		bundle, err := OpenWritable(context.Background(), dir)
		if err != nil {
			t.Fatalf("OpenWritable() error = %v", err)
		}
		defer func() {
			if err := bundle.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
		}()

		cfg := coreptrsync.DefaultConfig()
		cfg.Enabled = true

		lease, err := bundle.BeginPTRSync(context.Background(), cfg)
		if err != nil {
			t.Fatalf("BeginPTRSync() error = %v", err)
		}

		status, err := bundle.EnsurePTRSyncFoundation(context.Background(), cfg)
		if err != nil {
			t.Fatalf("EnsurePTRSyncFoundation() error = %v", err)
		}

		if status.Phase != coreptrsync.PhaseSyncing {
			t.Fatalf("status.Phase = %q, want %q", status.Phase, coreptrsync.PhaseSyncing)
		}

		if !status.IsRunning {
			t.Fatal("status.IsRunning = false, want true")
		}

		if _, err := bundle.BeginPTRSync(context.Background(), cfg); !errors.Is(err, coreptrsync.ErrSyncAlreadyRunning) {
			t.Fatalf("second BeginPTRSync() error = %v, want ErrSyncAlreadyRunning", err)
		}

		if _, err := bundle.FinishPTRSyncFailure(context.Background(), cfg, lease.RunToken, "cleanup"); err != nil {
			t.Fatalf("FinishPTRSyncFailure(cleanup) error = %v", err)
		}
	})

	t.Run("preserves updated_at_ms on no-op ensure", func(t *testing.T) {
		dir, _ := createTestBundle(t)

		bundle, err := OpenWritable(context.Background(), dir)
		if err != nil {
			t.Fatalf("OpenWritable() error = %v", err)
		}
		defer func() {
			if err := bundle.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
		}()

		cfg := coreptrsync.DefaultConfig()
		cfg.Enabled = true

		if _, err := bundle.EnsurePTRSyncFoundation(context.Background(), cfg); err != nil {
			t.Fatalf("first EnsurePTRSyncFoundation() error = %v", err)
		}

		const sentinelUpdatedAtMS int64 = 123456789
		if _, err := bundle.conn.ExecContext(
			context.Background(),
			`UPDATE main.ptr_sync_state SET updated_at_ms = ? WHERE singleton = ?`,
			sentinelUpdatedAtMS,
			ptrSyncStateSingleton,
		); err != nil {
			t.Fatalf("UPDATE ptr_sync_state updated_at_ms error = %v", err)
		}

		status, err := bundle.EnsurePTRSyncFoundation(context.Background(), cfg)
		if err != nil {
			t.Fatalf("second EnsurePTRSyncFoundation() error = %v", err)
		}

		if status.UpdatedAtMS != sentinelUpdatedAtMS {
			t.Fatalf("status.UpdatedAtMS = %d, want %d", status.UpdatedAtMS, sentinelUpdatedAtMS)
		}
	})

	t.Run("returns a typed collision error when PTR name is already taken", func(t *testing.T) {
		dir, _ := createTestBundle(t)

		bundle, err := OpenWritable(context.Background(), dir)
		if err != nil {
			t.Fatalf("OpenWritable() error = %v", err)
		}
		defer func() {
			if err := bundle.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
		}()

		cfg := coreptrsync.DefaultConfig()
		cfg.Enabled = true

		if _, err := bundle.conn.ExecContext(
			context.Background(),
			`INSERT INTO main.services (service_key, service_type, name, dictionary_string) VALUES (?, ?, ?, ?)`,
			[]byte("existing-public-ptr"),
			int(services.TypeTagRepository),
			cfg.ServiceName,
			"{}",
		); err != nil {
			t.Fatalf("INSERT existing PTR service error = %v", err)
		}

		_, err = bundle.EnsurePTRSyncFoundation(context.Background(), cfg)
		if !errors.Is(err, ErrPTRServiceNameCollision) {
			t.Fatalf("EnsurePTRSyncFoundation() error = %v, want ErrPTRServiceNameCollision", err)
		}
	})

	t.Run("treats case-variant PTR names as collisions on create", func(t *testing.T) {
		dir, _ := createTestBundle(t)

		bundle, err := OpenWritable(context.Background(), dir)
		if err != nil {
			t.Fatalf("OpenWritable() error = %v", err)
		}
		defer func() {
			if err := bundle.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
		}()

		cfg := coreptrsync.DefaultConfig()
		cfg.Enabled = true

		if _, err := bundle.conn.ExecContext(
			context.Background(),
			`INSERT INTO main.services (service_key, service_type, name, dictionary_string) VALUES (?, ?, ?, ?)`,
			[]byte("existing-public-ptr-case-variant"),
			int(services.TypeTagRepository),
			"Public Tag Repository",
			"{}",
		); err != nil {
			t.Fatalf("INSERT case-variant PTR service error = %v", err)
		}

		_, err = bundle.EnsurePTRSyncFoundation(context.Background(), cfg)
		if !errors.Is(err, ErrPTRServiceNameCollision) {
			t.Fatalf("EnsurePTRSyncFoundation() error = %v, want ErrPTRServiceNameCollision", err)
		}
	})
}

func TestBundleGetPTRSyncStatus(t *testing.T) {
	t.Run("returns default unconfigured state before PTR is enabled locally", func(t *testing.T) {
		dir, _ := createTestBundle(t)

		bundle, err := Open(context.Background(), dir)
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		defer func() {
			if err := bundle.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
		}()

		cfg := coreptrsync.DefaultConfig()
		cfg.Enabled = true

		status, err := bundle.GetPTRSyncStatus(context.Background(), cfg)
		if err != nil {
			t.Fatalf("GetPTRSyncStatus() error = %v", err)
		}

		if !status.Enabled {
			t.Fatal("status.Enabled = false, want true")
		}

		if status.Configured {
			t.Fatal("status.Configured = true, want false")
		}

		if status.Phase != coreptrsync.PhaseIdle {
			t.Fatalf("status.Phase = %q, want %q", status.Phase, coreptrsync.PhaseIdle)
		}
	})

	t.Run("returns disabled state when PTR is not enabled locally", func(t *testing.T) {
		dir, _ := createTestBundle(t)

		bundle, err := OpenWritable(context.Background(), dir)
		if err != nil {
			t.Fatalf("OpenWritable() error = %v", err)
		}
		defer func() {
			if err := bundle.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
		}()

		enabledCfg := coreptrsync.DefaultConfig()
		enabledCfg.Enabled = true

		if _, err := bundle.EnsurePTRSyncFoundation(context.Background(), enabledCfg); err != nil {
			t.Fatalf("EnsurePTRSyncFoundation() error = %v", err)
		}

		disabledCfg := coreptrsync.DefaultConfig()
		status, err := bundle.GetPTRSyncStatus(context.Background(), disabledCfg)
		if err != nil {
			t.Fatalf("GetPTRSyncStatus() error = %v", err)
		}

		if status.Enabled {
			t.Fatal("status.Enabled = true, want false")
		}

		if status.Configured {
			t.Fatal("status.Configured = true, want false")
		}

		if status.Phase != coreptrsync.PhaseDisabled {
			t.Fatalf("status.Phase = %q, want %q", status.Phase, coreptrsync.PhaseDisabled)
		}
	})
}

func TestBundlePTRSyncRuntime(t *testing.T) {
	t.Run("begin and fail sync updates runtime state", func(t *testing.T) {
		dir, _ := createTestBundle(t)

		bundle, err := OpenWritable(context.Background(), dir)
		if err != nil {
			t.Fatalf("OpenWritable() error = %v", err)
		}
		defer func() {
			if err := bundle.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
		}()

		cfg := coreptrsync.DefaultConfig()
		cfg.Enabled = true

		started, err := bundle.BeginPTRSync(context.Background(), cfg)
		if err != nil {
			t.Fatalf("BeginPTRSync() error = %v", err)
		}

		if started.Status.Phase != coreptrsync.PhaseSyncing {
			t.Fatalf("started.Status.Phase = %q, want %q", started.Status.Phase, coreptrsync.PhaseSyncing)
		}

		if !started.Status.IsRunning {
			t.Fatal("started.Status.IsRunning = false, want true")
		}

		if _, err := bundle.BeginPTRSync(context.Background(), cfg); !errors.Is(err, coreptrsync.ErrSyncAlreadyRunning) {
			t.Fatalf("second BeginPTRSync() error = %v, want ErrSyncAlreadyRunning", err)
		}

		failed, err := bundle.FinishPTRSyncFailure(context.Background(), cfg, started.RunToken, "network boom")
		if err != nil {
			t.Fatalf("FinishPTRSyncFailure() error = %v", err)
		}

		if failed.Phase != coreptrsync.PhaseIdle {
			t.Fatalf("failed.Phase = %q, want %q", failed.Phase, coreptrsync.PhaseIdle)
		}

		if failed.IsRunning {
			t.Fatal("failed.IsRunning = true, want false")
		}

		if failed.LastError != "network boom" {
			t.Fatalf("failed.LastError = %q, want %q", failed.LastError, "network boom")
		}
	})

	t.Run("rejects wrong success token without mutating metadata tables", func(t *testing.T) {
		dir, _ := createTestBundle(t)

		bundle, err := OpenWritable(context.Background(), dir)
		if err != nil {
			t.Fatalf("OpenWritable() error = %v", err)
		}
		defer func() {
			if err := bundle.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
		}()

		cfg := coreptrsync.DefaultConfig()
		cfg.Enabled = true

		lease, err := bundle.BeginPTRSync(context.Background(), cfg)
		if err != nil {
			t.Fatalf("BeginPTRSync() error = %v", err)
		}

		remoteState := testPTRRemoteState(
			t,
			1700000200,
			testPTRMetadataUpdate(t, 0, 10, 20, strings.Repeat("11", 32)),
		)

		_, err = bundle.FinishPTRSyncSuccess(context.Background(), cfg, "wrong-token", remoteState, true)
		if !errors.Is(err, errPTRSyncRunNotActive) {
			t.Fatalf("FinishPTRSyncSuccess() error = %v, want errPTRSyncRunNotActive", err)
		}

		serviceID := selectInt64(
			t,
			bundle.conn,
			`SELECT service_id FROM main.services WHERE service_key = ?`,
			mustDecodeHex(t, coreptrsync.DaemonServiceKeyHex()),
		)
		repositoryUpdatesTableName, repositoryUnregisteredTableName, _ :=
			generatePTRRepositoryTableNames(serviceID)

		if count := selectInt64(
			t,
			bundle.conn,
			fmt.Sprintf("SELECT COUNT(*) FROM %s", repositoryUpdatesTableName),
		); count != 0 {
			t.Fatalf("repository update row count = %d, want 0", count)
		}

		if count := selectInt64(
			t,
			bundle.conn,
			fmt.Sprintf("SELECT COUNT(*) FROM %s", repositoryUnregisteredTableName),
		); count != 0 {
			t.Fatalf("repository unregistered row count = %d, want 0", count)
		}

		if count := selectInt64(
			t,
			bundle.conn,
			`SELECT COUNT(*) FROM main.ptr_sync_remote_state WHERE service_id = ?`,
			serviceID,
		); count != 0 {
			t.Fatalf("ptr_sync_remote_state row count = %d, want 0", count)
		}

		if count := selectInt64(
			t,
			bundle.conn,
			`SELECT COUNT(*) FROM external_master.hashes WHERE hash = ?`,
			mustDecodeHex(t, strings.Repeat("11", 32)),
		); count != 0 {
			t.Fatalf("external_master hash row count = %d, want 0", count)
		}

		status, err := bundle.GetPTRSyncStatus(context.Background(), cfg)
		if err != nil {
			t.Fatalf("GetPTRSyncStatus() error = %v", err)
		}

		if status.Phase != coreptrsync.PhaseSyncing {
			t.Fatalf("status.Phase = %q, want %q", status.Phase, coreptrsync.PhaseSyncing)
		}

		if !status.IsRunning {
			t.Fatal("status.IsRunning = false, want true")
		}

		if _, err := bundle.FinishPTRSyncFailure(context.Background(), cfg, lease.RunToken, "cleanup"); err != nil {
			t.Fatalf("FinishPTRSyncFailure(cleanup) error = %v", err)
		}
	})

	t.Run("stores remote snapshot and metadata on first successful sync", func(t *testing.T) {
		dir, _ := createTestBundle(t)

		bundle, err := OpenWritable(context.Background(), dir)
		if err != nil {
			t.Fatalf("OpenWritable() error = %v", err)
		}
		defer func() {
			if err := bundle.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
		}()

		cfg := coreptrsync.DefaultConfig()
		cfg.Enabled = true

		lease, err := bundle.BeginPTRSync(context.Background(), cfg)
		if err != nil {
			t.Fatalf("BeginPTRSync() error = %v", err)
		}

		remoteState := testPTRRemoteState(
			t,
			1700000200,
			testPTRMetadataUpdate(t, 0, 10, 20, strings.Repeat("11", 32), strings.Repeat("22", 32)),
			testPTRMetadataUpdate(t, 1, 21, 30, strings.Repeat("33", 32)),
		)

		status, err := bundle.FinishPTRSyncSuccess(context.Background(), cfg, lease.RunToken, remoteState, true)
		if err != nil {
			t.Fatalf("FinishPTRSyncSuccess() error = %v", err)
		}

		if status.Phase != coreptrsync.PhaseIdle {
			t.Fatalf("status.Phase = %q, want %q", status.Phase, coreptrsync.PhaseIdle)
		}

		if status.IsRunning {
			t.Fatal("status.IsRunning = true, want false")
		}

		if status.MetadataSlice != 2 {
			t.Fatalf("status.MetadataSlice = %d, want 2", status.MetadataSlice)
		}

		serviceID := selectInt64(
			t,
			bundle.conn,
			`SELECT service_id FROM main.services WHERE service_key = ?`,
			mustDecodeHex(t, coreptrsync.DaemonServiceKeyHex()),
		)
		repositoryUpdatesTableName, repositoryUnregisteredTableName, _ :=
			generatePTRRepositoryTableNames(serviceID)

		if count := selectInt64(
			t,
			bundle.conn,
			fmt.Sprintf("SELECT COUNT(*) FROM %s", repositoryUpdatesTableName),
		); count != 3 {
			t.Fatalf("repository update row count = %d, want 3", count)
		}

		if count := selectInt64(
			t,
			bundle.conn,
			fmt.Sprintf("SELECT COUNT(*) FROM %s", repositoryUnregisteredTableName),
		); count != 3 {
			t.Fatalf("repository unregistered row count = %d, want 3", count)
		}

		if count := selectInt64(
			t,
			bundle.conn,
			`SELECT COUNT(*) FROM external_master.hashes WHERE hash IN (?, ?, ?)`,
			mustDecodeHex(t, strings.Repeat("11", 32)),
			mustDecodeHex(t, strings.Repeat("22", 32)),
			mustDecodeHex(t, strings.Repeat("33", 32)),
		); count != 3 {
			t.Fatalf("external_master hash row count = %d, want 3", count)
		}

		if got := selectInt64(
			t,
			bundle.conn,
			`SELECT next_update_due FROM main.ptr_sync_remote_state WHERE service_id = ?`,
			serviceID,
		); got != 1700000200 {
			t.Fatalf("next_update_due = %d, want 1700000200", got)
		}

		if got := selectInt64(
			t,
			bundle.conn,
			`SELECT update_period FROM main.ptr_sync_remote_state WHERE service_id = ?`,
			serviceID,
		); got != 3600 {
			t.Fatalf("update_period = %d, want 3600", got)
		}

		if got := selectInt64(
			t,
			bundle.conn,
			`SELECT nullification_period FROM main.ptr_sync_remote_state WHERE service_id = ?`,
			serviceID,
		); got != 86400 {
			t.Fatalf("nullification_period = %d, want 86400", got)
		}

		tagFilterJSON := selectString(
			t,
			bundle.conn,
			`SELECT tag_filter_json FROM main.ptr_sync_remote_state WHERE service_id = ?`,
			serviceID,
		)
		var tagFilterRules map[string]int
		if err := json.Unmarshal([]byte(tagFilterJSON), &tagFilterRules); err != nil {
			t.Fatalf("json.Unmarshal(tag_filter_json) error = %v", err)
		}

		if tagFilterRules[":"] != 1 || tagFilterRules["creator:"] != 0 {
			t.Fatalf("tag filter rules = %#v, want : => 1 and creator: => 0", tagFilterRules)
		}
	})

	t.Run("appends metadata on incremental sync", func(t *testing.T) {
		dir, _ := createTestBundle(t)

		bundle, err := OpenWritable(context.Background(), dir)
		if err != nil {
			t.Fatalf("OpenWritable() error = %v", err)
		}
		defer func() {
			if err := bundle.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
		}()

		cfg := coreptrsync.DefaultConfig()
		cfg.Enabled = true

		firstState := testPTRRemoteState(
			t,
			1700000200,
			testPTRMetadataUpdate(t, 0, 10, 20, strings.Repeat("11", 32)),
		)
		firstLease, err := bundle.BeginPTRSync(context.Background(), cfg)
		if err != nil {
			t.Fatalf("first BeginPTRSync() error = %v", err)
		}

		if _, err := bundle.FinishPTRSyncSuccess(context.Background(), cfg, firstLease.RunToken, firstState, true); err != nil {
			t.Fatalf("first FinishPTRSyncSuccess() error = %v", err)
		}

		secondState := testPTRRemoteState(
			t,
			1700000300,
			testPTRMetadataUpdate(t, 1, 21, 30, strings.Repeat("22", 32)),
		)
		secondLease, err := bundle.BeginPTRSync(context.Background(), cfg)
		if err != nil {
			t.Fatalf("second BeginPTRSync() error = %v", err)
		}

		status, err := bundle.FinishPTRSyncSuccess(context.Background(), cfg, secondLease.RunToken, secondState, false)
		if err != nil {
			t.Fatalf("second FinishPTRSyncSuccess() error = %v", err)
		}

		if status.MetadataSlice != 2 {
			t.Fatalf("status.MetadataSlice = %d, want 2", status.MetadataSlice)
		}

		serviceID := selectInt64(
			t,
			bundle.conn,
			`SELECT service_id FROM main.services WHERE service_key = ?`,
			mustDecodeHex(t, coreptrsync.DaemonServiceKeyHex()),
		)
		repositoryUpdatesTableName, repositoryUnregisteredTableName, _ :=
			generatePTRRepositoryTableNames(serviceID)

		if count := selectInt64(
			t,
			bundle.conn,
			fmt.Sprintf("SELECT COUNT(*) FROM %s", repositoryUpdatesTableName),
		); count != 2 {
			t.Fatalf("repository update row count = %d, want 2", count)
		}

		if count := selectInt64(
			t,
			bundle.conn,
			fmt.Sprintf("SELECT COUNT(*) FROM %s", repositoryUnregisteredTableName),
		); count != 2 {
			t.Fatalf("repository unregistered row count = %d, want 2", count)
		}

		if got := selectInt64(
			t,
			bundle.conn,
			`SELECT next_update_due FROM main.ptr_sync_remote_state WHERE service_id = ?`,
			serviceID,
		); got != 1700000300 {
			t.Fatalf("next_update_due = %d, want 1700000300", got)
		}
	})

	t.Run("replaces metadata on full resync and prunes stale processed rows", func(t *testing.T) {
		dir, _ := createTestBundle(t)

		bundle, err := OpenWritable(context.Background(), dir)
		if err != nil {
			t.Fatalf("OpenWritable() error = %v", err)
		}
		defer func() {
			if err := bundle.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
		}()

		cfg := coreptrsync.DefaultConfig()
		cfg.Enabled = true

		initialState := testPTRRemoteState(
			t,
			1700000200,
			testPTRMetadataUpdate(t, 0, 10, 20, strings.Repeat("11", 32)),
			testPTRMetadataUpdate(t, 1, 21, 30, strings.Repeat("22", 32)),
		)
		initialLease, err := bundle.BeginPTRSync(context.Background(), cfg)
		if err != nil {
			t.Fatalf("initial BeginPTRSync() error = %v", err)
		}

		if _, err := bundle.FinishPTRSyncSuccess(context.Background(), cfg, initialLease.RunToken, initialState, true); err != nil {
			t.Fatalf("initial FinishPTRSyncSuccess() error = %v", err)
		}

		serviceID := selectInt64(
			t,
			bundle.conn,
			`SELECT service_id FROM main.services WHERE service_key = ?`,
			mustDecodeHex(t, coreptrsync.DaemonServiceKeyHex()),
		)
		_, _, repositoryProcessedTableName := generatePTRRepositoryTableNames(serviceID)

		hashOneID, ok, err := lookupHashIDByHash(
			context.Background(),
			bundle.conn,
			mustDecodeHex(t, strings.Repeat("11", 32)),
		)
		if err != nil || !ok {
			t.Fatalf("lookupHashIDByHash(hash11) = (%d, %v, %v), want existing hash id", hashOneID, ok, err)
		}

		hashTwoID, ok, err := lookupHashIDByHash(
			context.Background(),
			bundle.conn,
			mustDecodeHex(t, strings.Repeat("22", 32)),
		)
		if err != nil || !ok {
			t.Fatalf("lookupHashIDByHash(hash22) = (%d, %v, %v), want existing hash id", hashTwoID, ok, err)
		}

		if _, err := bundle.conn.ExecContext(
			context.Background(),
			fmt.Sprintf(
				"INSERT INTO %s (hash_id, content_type, processed) VALUES (?, ?, ?), (?, ?, ?)",
				repositoryProcessedTableName,
			),
			hashOneID,
			0,
			1,
			hashTwoID,
			0,
			1,
		); err != nil {
			t.Fatalf("INSERT repository processed rows error = %v", err)
		}

		replacementState := testPTRRemoteState(
			t,
			1700000400,
			testPTRMetadataUpdate(t, 0, 10, 20, strings.Repeat("11", 32)),
		)
		replacementLease, err := bundle.BeginPTRSync(context.Background(), cfg)
		if err != nil {
			t.Fatalf("replacement BeginPTRSync() error = %v", err)
		}

		status, err := bundle.FinishPTRSyncSuccess(context.Background(), cfg, replacementLease.RunToken, replacementState, true)
		if err != nil {
			t.Fatalf("replacement FinishPTRSyncSuccess() error = %v", err)
		}

		if status.MetadataSlice != 1 {
			t.Fatalf("status.MetadataSlice = %d, want 1", status.MetadataSlice)
		}

		repositoryUpdatesTableName, repositoryUnregisteredTableName, _ :=
			generatePTRRepositoryTableNames(serviceID)
		if count := selectInt64(
			t,
			bundle.conn,
			fmt.Sprintf("SELECT COUNT(*) FROM %s", repositoryUpdatesTableName),
		); count != 1 {
			t.Fatalf("repository update row count = %d, want 1", count)
		}

		if count := selectInt64(
			t,
			bundle.conn,
			fmt.Sprintf("SELECT COUNT(*) FROM %s", repositoryUnregisteredTableName),
		); count != 1 {
			t.Fatalf("repository unregistered row count = %d, want 1", count)
		}

		if count := selectInt64(
			t,
			bundle.conn,
			fmt.Sprintf("SELECT COUNT(*) FROM %s", repositoryProcessedTableName),
		); count != 1 {
			t.Fatalf("repository processed row count = %d, want 1", count)
		}

		if count := selectInt64(
			t,
			bundle.conn,
			fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE hash_id = ?", repositoryProcessedTableName),
			hashTwoID,
		); count != 0 {
			t.Fatalf("stale processed row count = %d, want 0", count)
		}
	})
}

func testPTRRemoteState(
	t *testing.T,
	nextUpdateDue int64,
	updates ...coreptrsync.MetadataUpdate,
) coreptrsync.RemoteState {
	t.Helper()

	expires := int64(1700000100)

	return coreptrsync.RemoteState{
		Account: coreptrsync.AccountSnapshot{
			AccountKey:     mustDecodeHex(t, strings.Repeat("aa", 32)),
			Created:        1699990000,
			Expires:        &expires,
			Message:        "shared read-only",
			MessageCreated: 1699990100,
		},
		ServiceOptions: coreptrsync.ServiceOptions{
			UpdatePeriod:        3600,
			NullificationPeriod: 86400,
		},
		TagFilter: coreptrsync.TagFilterSnapshot{Rules: map[string]int{":": 1, "creator:": 0}},
		Metadata: coreptrsync.MetadataSlice{
			Updates:       updates,
			NextUpdateDue: nextUpdateDue,
		},
	}
}

func testPTRMetadataUpdate(
	t *testing.T,
	updateIndex int64,
	begin int64,
	end int64,
	hashHexes ...string,
) coreptrsync.MetadataUpdate {
	t.Helper()

	hashes := make([][]byte, 0, len(hashHexes))
	for _, hashHex := range hashHexes {
		hashes = append(hashes, mustDecodeHex(t, hashHex))
	}

	return coreptrsync.MetadataUpdate{
		UpdateIndex:  updateIndex,
		UpdateHashes: hashes,
		Begin:        begin,
		End:          end,
	}
}
