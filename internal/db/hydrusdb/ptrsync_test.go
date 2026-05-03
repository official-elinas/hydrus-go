package hydrusdb

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/official-elinas/hydrus-go/internal/core/librarybrowse"
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

		masterDB := openSQLiteForTest(t, filepath.Join(dir, "client.master.db"))
		defer masterDB.Close()
		masterConn, err := masterDB.Conn(context.Background())
		if err != nil {
			t.Fatalf("masterDB.Conn() error = %v", err)
		}
		defer masterConn.Close()
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
		repositoryHashIDMapTableName, repositoryTagIDMapTableName :=
			generatePTRRepositoryDefinitionTableNames(serviceID)
		repositoryUpdatesServiceID := selectInt64(
			t,
			bundle.conn,
			`SELECT service_id FROM main.services WHERE service_key = ?`,
			[]byte("repository updates"),
		)
		for _, tableName := range []string{
			repositoryUpdatesTableName,
			repositoryUnregisteredTableName,
			repositoryProcessedTableName,
			fmt.Sprintf("current_files_%d", repositoryUpdatesServiceID),
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

		for _, tableName := range []string{
			strings.TrimPrefix(repositoryHashIDMapTableName, "external_master."),
			strings.TrimPrefix(repositoryTagIDMapTableName, "external_master."),
		} {
			if !rowExistsInDB(
				t,
				masterConn,
				`SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?`,
				tableName,
			) {
				t.Fatalf("expected master table %q to exist", tableName)
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

	t.Run("preserves persisted retrying state during recovery", func(t *testing.T) {
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

		const (
			retryAtMS    int64 = 987654321
			retryAttempt int64 = 3
		)

		if _, err := bundle.conn.ExecContext(
			context.Background(),
			`UPDATE main.ptr_sync_state
			SET phase = ?, is_running = 0, run_token = NULL, retry_at_ms = ?, retry_attempt = ?, service_id = ?
			WHERE singleton = ?`,
			coreptrsync.PhaseRetrying,
			retryAtMS,
			retryAttempt,
			serviceID,
			ptrSyncStateSingleton,
		); err != nil {
			t.Fatalf("UPDATE ptr_sync_state retrying error = %v", err)
		}

		recovered, err := bundle.RecoverPTRSyncFoundation(context.Background(), cfg)
		if err != nil {
			t.Fatalf("RecoverPTRSyncFoundation() error = %v", err)
		}

		if recovered.Phase != coreptrsync.PhaseRetrying {
			t.Fatalf("recovered.Phase = %q, want %q", recovered.Phase, coreptrsync.PhaseRetrying)
		}

		if recovered.RetryAtMS != retryAtMS {
			t.Fatalf("recovered.RetryAtMS = %d, want %d", recovered.RetryAtMS, retryAtMS)
		}

		if recovered.RetryAttempt != retryAttempt {
			t.Fatalf("recovered.RetryAttempt = %d, want %d", recovered.RetryAttempt, retryAttempt)
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

	t.Run("finalize downloaded update removes unregistered row and updates counters from table state", func(t *testing.T) {
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

		remoteState := testPTRRemoteState(
			t,
			1700000200,
			testPTRMetadataUpdate(t, 0, 10, 20, strings.Repeat("11", 32)),
		)
		lease, err := bundle.BeginPTRSync(context.Background(), cfg)
		if err != nil {
			t.Fatalf("BeginPTRSync() error = %v", err)
		}

		status, err := bundle.PersistPTRSyncMetadata(context.Background(), cfg, lease.RunToken, remoteState, true)
		if err != nil {
			t.Fatalf("PersistPTRSyncMetadata() error = %v", err)
		}

		if !status.IsRunning {
			t.Fatal("status.IsRunning = false, want true")
		}

		if status.DownloadedUpdateCount != 0 {
			t.Fatalf("status.DownloadedUpdateCount = %d, want 0", status.DownloadedUpdateCount)
		}

		if status.IsComplete {
			t.Fatal("status.IsComplete = true, want false while updates are still pending")
		}

		status, err = bundle.FinalizePTRDownloadedUpdatesBatch(context.Background(), cfg, lease.RunToken, []PTRDownloadedUpdateBatchItem{{
			HashHex: strings.Repeat("11", 32),
			Body:    []byte("ptr-update-body-01"),
			PreparedImport: PreparedLocalImport{
				HashHex:             strings.Repeat("11", 32),
				Size:                18,
				Mime:                29,
				ImportedAtMS:        1111,
				LocalFileServiceKey: hex.EncodeToString([]byte("repository updates")),
			},
		}})
		if err != nil {
			t.Fatalf("FinalizePTRDownloadedUpdatesBatch() error = %v", err)
		}

		if status.DownloadedUpdateCount != 1 {
			t.Fatalf("status.DownloadedUpdateCount = %d, want 1", status.DownloadedUpdateCount)
		}

		serviceID := selectInt64(
			t,
			bundle.conn,
			`SELECT service_id FROM main.services WHERE service_key = ?`,
			mustDecodeHex(t, coreptrsync.DaemonServiceKeyHex()),
		)
		_, repositoryUnregisteredTableName, _ := generatePTRRepositoryTableNames(serviceID)

		if count := selectInt64(
			t,
			bundle.conn,
			fmt.Sprintf("SELECT COUNT(*) FROM %s", repositoryUnregisteredTableName),
		); count != 0 {
			t.Fatalf("repository unregistered row count = %d, want 0", count)
		}

		status, err = bundle.CompletePTRSyncSuccess(context.Background(), cfg, lease.RunToken)
		if err != nil {
			t.Fatalf("CompletePTRSyncSuccess() error = %v", err)
		}

		if status.Phase != coreptrsync.PhaseIdle {
			t.Fatalf("status.Phase = %q, want %q", status.Phase, coreptrsync.PhaseIdle)
		}

		if status.IsComplete {
			t.Fatal("status.IsComplete = true, want false while finalized PTR work is still unapplied")
		}
	})

	t.Run("accepts non-64-char repository definition hashes", func(t *testing.T) {
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

		definitionsHash := strings.Repeat("ee", 32)

		lease, err := bundle.BeginPTRSync(context.Background(), cfg)
		if err != nil {
			t.Fatalf("BeginPTRSync() error = %v", err)
		}

		remoteState := testPTRRemoteState(
			t,
			1700000400,
			testPTRMetadataUpdate(t, 0, 10, 20, definitionsHash),
		)
		if _, err := bundle.PersistPTRSyncMetadata(context.Background(), cfg, lease.RunToken, remoteState, true); err != nil {
			t.Fatalf("PersistPTRSyncMetadata() error = %v", err)
		}

		if _, err := bundle.FinalizePTRDownloadedUpdatesBatch(context.Background(), cfg, lease.RunToken, []PTRDownloadedUpdateBatchItem{{
			HashHex: definitionsHash,
			Body:    []byte("definitions-40-hex"),
			PreparedImport: PreparedLocalImport{
				HashHex:             definitionsHash,
				Size:                18,
				Mime:                28,
				ImportedAtMS:        1111,
				LocalFileServiceKey: hex.EncodeToString([]byte("repository updates")),
			},
		}}); err != nil {
			t.Fatalf("FinalizePTRDownloadedUpdatesBatch() error = %v", err)
		}

		definitionHashHex := strings.Repeat("11", 20)
		definitions := PTRDefinitionsUpdate{
			ServiceHashIDsToHashes: map[int64]string{101: definitionHashHex},
			ServiceTagIDsToTags:    map[int64]string{201: "creator:alice"},
		}
		if err := bundle.ApplyPTRDefinitions(context.Background(), cfg, lease.RunToken, definitionsHash, definitions); err != nil {
			t.Fatalf("ApplyPTRDefinitions() error = %v", err)
		}

		serviceID := selectInt64(
			t,
			bundle.conn,
			`SELECT service_id FROM main.services WHERE service_key = ?`,
			mustDecodeHex(t, coreptrsync.DaemonServiceKeyHex()),
		)
		hashIDMapTableName, _ := generatePTRRepositoryDefinitionTableNames(serviceID)

		hashID := selectInt64(
			t,
			bundle.conn,
			fmt.Sprintf("SELECT hash_id FROM %s WHERE service_hash_id = ?", hashIDMapTableName),
			101,
		)
		if got := selectInt64(
			t,
			bundle.conn,
			`SELECT length(hash) FROM external_master.hashes WHERE hash_id = ?`,
			hashID,
		); got != 20 {
			t.Fatalf("length(external_master.hashes.hash) = %d, want 20", got)
		}

		status, err := bundle.GetPTRSyncStatus(context.Background(), cfg)
		if err != nil {
			t.Fatalf("GetPTRSyncStatus() error = %v", err)
		}
		if status.ProcessedDefinitionCount != 1 {
			t.Fatalf("status.ProcessedDefinitionCount = %d, want 1", status.ProcessedDefinitionCount)
		}
	})

	t.Run("falls back to invalid repository tag for empty cleaned definition tags", func(t *testing.T) {
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

		definitionsHash := strings.Repeat("ef", 32)

		lease, err := bundle.BeginPTRSync(context.Background(), cfg)
		if err != nil {
			t.Fatalf("BeginPTRSync() error = %v", err)
		}

		remoteState := testPTRRemoteState(
			t,
			1700000500,
			testPTRMetadataUpdate(t, 0, 10, 20, definitionsHash),
		)
		if _, err := bundle.PersistPTRSyncMetadata(context.Background(), cfg, lease.RunToken, remoteState, true); err != nil {
			t.Fatalf("PersistPTRSyncMetadata() error = %v", err)
		}

		if _, err := bundle.FinalizePTRDownloadedUpdatesBatch(context.Background(), cfg, lease.RunToken, []PTRDownloadedUpdateBatchItem{{
			HashHex: definitionsHash,
			Body:    []byte("definitions-invalid-tag"),
			PreparedImport: PreparedLocalImport{
				HashHex:             definitionsHash,
				Size:                23,
				Mime:                28,
				ImportedAtMS:        1111,
				LocalFileServiceKey: hex.EncodeToString([]byte("repository updates")),
			},
		}}); err != nil {
			t.Fatalf("FinalizePTRDownloadedUpdatesBatch() error = %v", err)
		}

		definitions := PTRDefinitionsUpdate{
			ServiceTagIDsToTags: map[int64]string{201: "\x7f"},
		}
		if err := bundle.ApplyPTRDefinitions(context.Background(), cfg, lease.RunToken, definitionsHash, definitions); err != nil {
			t.Fatalf("ApplyPTRDefinitions() error = %v", err)
		}

		serviceID := selectInt64(
			t,
			bundle.conn,
			`SELECT service_id FROM main.services WHERE service_key = ?`,
			mustDecodeHex(t, coreptrsync.DaemonServiceKeyHex()),
		)
		_, tagIDMapTableName := generatePTRRepositoryDefinitionTableNames(serviceID)
		mappedTagID := selectInt64(
			t,
			bundle.conn,
			fmt.Sprintf("SELECT tag_id FROM %s WHERE service_tag_id = ?", tagIDMapTableName),
			201,
		)

		expectedTagID, err := bundle.EnsureTagID(context.Background(), ptrInvalidRepositoryTag)
		if err != nil {
			t.Fatalf("EnsureTagID(%q) error = %v", ptrInvalidRepositoryTag, err)
		}
		if mappedTagID != expectedTagID {
			t.Fatalf("mappedTagID = %d, want invalid repository tag id %d", mappedTagID, expectedTagID)
		}

		status, err := bundle.GetPTRSyncStatus(context.Background(), cfg)
		if err != nil {
			t.Fatalf("GetPTRSyncStatus() error = %v", err)
		}
		if status.ProcessedDefinitionCount != 1 {
			t.Fatalf("status.ProcessedDefinitionCount = %d, want 1", status.ProcessedDefinitionCount)
		}
	})
}

func TestBundlePTRPendingMappings(t *testing.T) {
	t.Run("stages lists and commits pending mappings", func(t *testing.T) {
		dir, fixture := createTestBundle(t)

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

		stageResult, err := bundle.StagePTRPendingMappings(context.Background(), cfg, coreptrsync.PendingMappingsRequest{
			Hashes: []string{fixture.hash1Hex, fixture.hash2Hex},
			Tags:   []string{"creator:alice", "series:zeta"},
		})
		if err != nil {
			t.Fatalf("StagePTRPendingMappings() error = %v", err)
		}

		if stageResult.ServiceKey != coreptrsync.DaemonServiceKeyHex() {
			t.Fatalf("stageResult.ServiceKey = %q, want %q", stageResult.ServiceKey, coreptrsync.DaemonServiceKeyHex())
		}

		if stageResult.AddedMappings != 4 {
			t.Fatalf("stageResult.AddedMappings = %d, want 4", stageResult.AddedMappings)
		}

		groups, err := bundle.ListPTRPendingMappingsForCommit(context.Background(), cfg, "")
		if err != nil {
			t.Fatalf("ListPTRPendingMappingsForCommit() error = %v", err)
		}

		if len(groups) != 2 {
			t.Fatalf("len(groups) = %d, want 2", len(groups))
		}

		if groups[0].Tag != "creator:alice" || strings.Join(groups[0].Hashes, "|") != fixture.hash1Hex+"|"+fixture.hash2Hex {
			t.Fatalf("groups[0] = %+v, want creator:alice with both hashes", groups[0])
		}

		if groups[1].Tag != "series:zeta" || strings.Join(groups[1].Hashes, "|") != fixture.hash1Hex+"|"+fixture.hash2Hex {
			t.Fatalf("groups[1] = %+v, want series:zeta with both hashes", groups[1])
		}

		commitResult, err := bundle.CommitPTRPendingMappingsSuccess(context.Background(), cfg, "")
		if err != nil {
			t.Fatalf("CommitPTRPendingMappingsSuccess() error = %v", err)
		}

		if commitResult.ServiceKey != coreptrsync.DaemonServiceKeyHex() {
			t.Fatalf("commitResult.ServiceKey = %q, want %q", commitResult.ServiceKey, coreptrsync.DaemonServiceKeyHex())
		}

		if commitResult.CommittedMappings != 4 {
			t.Fatalf("commitResult.CommittedMappings = %d, want 4", commitResult.CommittedMappings)
		}

		serviceID := selectInt64(
			t,
			bundle.conn,
			`SELECT service_id FROM main.services WHERE service_key = ?`,
			mustDecodeHex(t, coreptrsync.DaemonServiceKeyHex()),
		)

		if got := selectInt64(
			t,
			bundle.conn,
			fmt.Sprintf(`SELECT COUNT(*) FROM external_mappings.pending_mappings_%d`, serviceID),
		); got != 0 {
			t.Fatalf("pending mapping row count = %d, want 0", got)
		}

		if got := selectInt64(
			t,
			bundle.conn,
			fmt.Sprintf(`SELECT COUNT(*) FROM external_mappings.current_mappings_%d`, serviceID),
		); got != 4 {
			t.Fatalf("current mapping row count = %d, want 4", got)
		}

		restageResult, err := bundle.StagePTRPendingMappings(context.Background(), cfg, coreptrsync.PendingMappingsRequest{
			Hashes: []string{fixture.hash1Hex, fixture.hash2Hex},
			Tags:   []string{"creator:alice", "series:zeta"},
		})
		if err != nil {
			t.Fatalf("restage StagePTRPendingMappings() error = %v", err)
		}

		if restageResult.AddedMappings != 0 {
			t.Fatalf("restageResult.AddedMappings = %d, want 0", restageResult.AddedMappings)
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

		if status.IsComplete {
			t.Fatal("status.IsComplete = true, want false before any successful sync")
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

		if failed.IsComplete {
			t.Fatal("failed.IsComplete = true, want false after failure")
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

func TestBundlePTRApplyPipeline(t *testing.T) {
	t.Run("finalizes downloaded updates that are already imported outside the repository-updates domain", func(t *testing.T) {
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

		hashHex := strings.Repeat("cc", 32)
		lease, err := bundle.BeginPTRSync(context.Background(), cfg)
		if err != nil {
			t.Fatalf("BeginPTRSync() error = %v", err)
		}

		remoteState := testPTRRemoteState(
			t,
			1700000300,
			testPTRMetadataUpdate(t, 0, 10, 20, hashHex),
		)
		if _, err := bundle.PersistPTRSyncMetadata(context.Background(), cfg, lease.RunToken, remoteState, true); err != nil {
			t.Fatalf("PersistPTRSyncMetadata() error = %v", err)
		}

		localFileService := mustUniqueServiceDefinitionByType(t, bundle, services.TypeLocalFileDomain)
		localImport, err := bundle.RecordPreparedLocalImport(context.Background(), PreparedLocalImport{
			HashHex:             hashHex,
			Size:                21,
			Mime:                29,
			ImportedAtMS:        3333,
			LocalFileServiceKey: localFileService.serviceKey,
		})
		if err != nil {
			t.Fatalf("RecordPreparedLocalImport() error = %v", err)
		}

		if err := bundle.Close(); err != nil {
			t.Fatalf("Close() before restart error = %v", err)
		}

		bundle, err = OpenWritable(context.Background(), dir)
		if err != nil {
			t.Fatalf("OpenWritable() after restart error = %v", err)
		}

		status, err := bundle.FinalizePTRDownloadedUpdatesBatch(
			context.Background(),
			cfg,
			lease.RunToken,
			[]PTRDownloadedUpdateBatchItem{{
				HashHex: hashHex,
				Body:    []byte("ptr-update-body-batch1"),
				PreparedImport: PreparedLocalImport{
					HashHex:             hashHex,
					Size:                22,
					Mime:                29,
					ImportedAtMS:        4444,
					LocalFileServiceKey: hex.EncodeToString([]byte("repository updates")),
				},
			}},
		)
		if err != nil {
			t.Fatalf("FinalizePTRDownloadedUpdatesBatch() error = %v", err)
		}

		if status.DownloadedUpdateCount != 1 {
			t.Fatalf("status.DownloadedUpdateCount = %d, want 1", status.DownloadedUpdateCount)
		}

		serviceID := selectInt64(
			t,
			bundle.conn,
			`SELECT service_id FROM main.services WHERE service_key = ?`,
			mustDecodeHex(t, coreptrsync.DaemonServiceKeyHex()),
		)
		_, repositoryUnregisteredTableName, _ := generatePTRRepositoryTableNames(serviceID)
		if count := selectInt64(
			t,
			bundle.conn,
			fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE hash_id = ?", repositoryUnregisteredTableName),
			localImport.FileID,
		); count != 0 {
			t.Fatalf("repository unregistered row count = %d, want 0", count)
		}

		_, _, repositoryProcessedTableName := generatePTRRepositoryTableNames(serviceID)
		if count := selectInt64(
			t,
			bundle.conn,
			fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE hash_id = ? AND content_type = ? AND length(body) = ?`, repositoryProcessedTableName),
			localImport.FileID,
			PTRContentTypeMappings,
			22,
		); count != 1 {
			t.Fatalf("repository processed row count = %d, want 1", count)
		}
	})

	t.Run("reconciles local updates, orders definitions first, and applies mappings", func(t *testing.T) {
		dir, fixture := createTestBundle(t)

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

		definitionsHash := strings.Repeat("aa", 32)
		mappingsHash := strings.Repeat("bb", 32)

		lease, err := bundle.BeginPTRSync(context.Background(), cfg)
		if err != nil {
			t.Fatalf("BeginPTRSync() error = %v", err)
		}

		remoteState := testPTRRemoteState(
			t,
			1700000200,
			testPTRMetadataUpdate(t, 0, 10, 20, definitionsHash),
			testPTRMetadataUpdate(t, 1, 21, 30, mappingsHash),
		)
		if _, err := bundle.PersistPTRSyncMetadata(context.Background(), cfg, lease.RunToken, remoteState, true); err != nil {
			t.Fatalf("PersistPTRSyncMetadata() error = %v", err)
		}

		batch := []PTRDownloadedUpdateBatchItem{
			{
				HashHex: definitionsHash,
				Body:    []byte("definitions"),
				PreparedImport: PreparedLocalImport{
					HashHex:             definitionsHash,
					Size:                11,
					Mime:                28,
					ImportedAtMS:        1111,
					LocalFileServiceKey: hex.EncodeToString([]byte("repository updates")),
				},
			},
			{
				HashHex: mappingsHash,
				Body:    []byte("mappings-123"),
				PreparedImport: PreparedLocalImport{
					HashHex:             mappingsHash,
					Size:                12,
					Mime:                29,
					ImportedAtMS:        2222,
					LocalFileServiceKey: hex.EncodeToString([]byte("repository updates")),
				},
			},
		}
		if _, err := bundle.FinalizePTRDownloadedUpdatesBatch(context.Background(), cfg, lease.RunToken, batch); err != nil {
			t.Fatalf("FinalizePTRDownloadedUpdatesBatch() error = %v", err)
		}

		processable, err := bundle.ListPTRProcessableUpdates(context.Background(), cfg, lease.RunToken)
		if err != nil {
			t.Fatalf("ListPTRProcessableUpdates() error = %v", err)
		}

		if len(processable) != 2 {
			t.Fatalf("len(processable) = %d, want 2", len(processable))
		}

		if processable[0].ContentType != PTRContentTypeDefinitions || processable[1].ContentType != PTRContentTypeMappings {
			t.Fatalf("processable content types = [%d %d], want [%d %d]", processable[0].ContentType, processable[1].ContentType, PTRContentTypeDefinitions, PTRContentTypeMappings)
		}

		definitions := PTRDefinitionsUpdate{
			ServiceHashIDsToHashes: map[int64]string{101: fixture.hash1Hex, 102: fixture.hash2Hex},
			ServiceTagIDsToTags:    map[int64]string{201: "creator:alice", 202: "old:tag"},
		}
		if err := bundle.ApplyPTRDefinitions(context.Background(), cfg, lease.RunToken, definitionsHash, definitions); err != nil {
			t.Fatalf("ApplyPTRDefinitions() error = %v", err)
		}

		serviceID := selectInt64(
			t,
			bundle.conn,
			`SELECT service_id FROM main.services WHERE service_key = ?`,
			mustDecodeHex(t, coreptrsync.DaemonServiceKeyHex()),
		)
		hashIDMapTableName, tagIDMapTableName := generatePTRRepositoryDefinitionTableNames(serviceID)

		if got := selectInt64(
			t,
			bundle.conn,
			fmt.Sprintf("SELECT hash_id FROM %s WHERE service_hash_id = ?", hashIDMapTableName),
			101,
		); got != 1 {
			t.Fatalf("service_hash_id 101 mapped hash_id = %d, want 1", got)
		}

		if got := selectInt64(
			t,
			bundle.conn,
			fmt.Sprintf("SELECT tag_id FROM %s WHERE service_tag_id = ?", tagIDMapTableName),
			202,
		); got != 3 {
			t.Fatalf("service_tag_id 202 mapped tag_id = %d, want 3", got)
		}

		if _, err := bundle.conn.ExecContext(
			context.Background(),
			fmt.Sprintf(`INSERT INTO external_mappings.deleted_mappings_%d (tag_id, hash_id) VALUES (?, ?);`, serviceID),
			1,
			1,
		); err != nil {
			t.Fatalf("insert deleted_mappings row error = %v", err)
		}
		if _, err := bundle.conn.ExecContext(
			context.Background(),
			fmt.Sprintf(`INSERT INTO external_mappings.pending_mappings_%d (tag_id, hash_id) VALUES (?, ?);`, serviceID),
			1,
			2,
		); err != nil {
			t.Fatalf("insert pending_mappings row error = %v", err)
		}
		if _, err := bundle.conn.ExecContext(
			context.Background(),
			fmt.Sprintf(`INSERT INTO external_mappings.current_mappings_%d (tag_id, hash_id) VALUES (?, ?);`, serviceID),
			3,
			1,
		); err != nil {
			t.Fatalf("insert current_mappings row error = %v", err)
		}
		if _, err := bundle.conn.ExecContext(
			context.Background(),
			fmt.Sprintf(`INSERT INTO external_mappings.petitioned_mappings_%d (tag_id, hash_id, reason_id) VALUES (?, ?, ?);`, serviceID),
			3,
			1,
			1,
		); err != nil {
			t.Fatalf("insert petitioned_mappings row error = %v", err)
		}

		mappings := PTRMappingsUpdate{
			Adds:    []PTRMappingUpdateRow{{ServiceTagID: 201, ServiceHashIDs: []int64{101, 102}}},
			Deletes: []PTRMappingUpdateRow{{ServiceTagID: 202, ServiceHashIDs: []int64{101}}},
		}
		if err := bundle.ApplyPTRMappings(context.Background(), cfg, lease.RunToken, mappingsHash, mappings); err != nil {
			t.Fatalf("ApplyPTRMappings() error = %v", err)
		}

		if count := selectInt64(
			t,
			bundle.conn,
			fmt.Sprintf(`SELECT COUNT(*) FROM external_mappings.current_mappings_%d WHERE tag_id = ? AND hash_id IN (?, ?)`, serviceID),
			1,
			1,
			2,
		); count != 2 {
			t.Fatalf("current mapping add row count = %d, want 2", count)
		}

		if count := selectInt64(
			t,
			bundle.conn,
			fmt.Sprintf(`SELECT COUNT(*) FROM external_mappings.deleted_mappings_%d WHERE tag_id = ? AND hash_id = ?`, serviceID),
			1,
			1,
		); count != 0 {
			t.Fatalf("deleted mapping stale add row count = %d, want 0", count)
		}

		if count := selectInt64(
			t,
			bundle.conn,
			fmt.Sprintf(`SELECT COUNT(*) FROM external_mappings.pending_mappings_%d WHERE tag_id = ? AND hash_id = ?`, serviceID),
			1,
			2,
		); count != 0 {
			t.Fatalf("pending mapping stale add row count = %d, want 0", count)
		}

		if count := selectInt64(
			t,
			bundle.conn,
			fmt.Sprintf(`SELECT COUNT(*) FROM external_mappings.current_mappings_%d WHERE tag_id = ? AND hash_id = ?`, serviceID),
			3,
			1,
		); count != 0 {
			t.Fatalf("current mapping delete row count = %d, want 0", count)
		}

		if count := selectInt64(
			t,
			bundle.conn,
			fmt.Sprintf(`SELECT COUNT(*) FROM external_mappings.petitioned_mappings_%d WHERE tag_id = ? AND hash_id = ?`, serviceID),
			3,
			1,
		); count != 0 {
			t.Fatalf("petitioned mapping delete row count = %d, want 0", count)
		}

		if count := selectInt64(
			t,
			bundle.conn,
			fmt.Sprintf(`SELECT COUNT(*) FROM external_mappings.deleted_mappings_%d WHERE tag_id = ? AND hash_id = ?`, serviceID),
			3,
			1,
		); count != 1 {
			t.Fatalf("deleted mapping delete row count = %d, want 1", count)
		}

		status, err := bundle.GetPTRSyncStatus(context.Background(), cfg)
		if err != nil {
			t.Fatalf("GetPTRSyncStatus() error = %v", err)
		}

		if status.ProcessedDefinitionCount != 1 {
			t.Fatalf("status.ProcessedDefinitionCount = %d, want 1", status.ProcessedDefinitionCount)
		}

		if status.ProcessedContentCount != 1 {
			t.Fatalf("status.ProcessedContentCount = %d, want 1", status.ProcessedContentCount)
		}

		processable, err = bundle.ListPTRProcessableUpdates(context.Background(), cfg, lease.RunToken)
		if err != nil {
			t.Fatalf("ListPTRProcessableUpdates() after apply error = %v", err)
		}

		if len(processable) != 0 {
			t.Fatalf("len(processable) after apply = %d, want 0", len(processable))
		}
	})
}

func TestBundlePTRAppliedMappingsRemainSearchableAfterRestart(t *testing.T) {
	dir, fixture := createTestBundle(t)

	bundle, err := OpenWritable(context.Background(), dir)
	if err != nil {
		t.Fatalf("OpenWritable() error = %v", err)
	}

	cfg := coreptrsync.DefaultConfig()
	cfg.Enabled = true

	definitionsHash := strings.Repeat("cc", 32)
	mappingsHash := strings.Repeat("dd", 32)

	lease, err := bundle.BeginPTRSync(context.Background(), cfg)
	if err != nil {
		t.Fatalf("BeginPTRSync() error = %v", err)
	}

	remoteState := testPTRRemoteState(
		t,
		1700000300,
		testPTRMetadataUpdate(t, 0, 10, 20, definitionsHash),
		testPTRMetadataUpdate(t, 1, 21, 30, mappingsHash),
	)
	if _, err := bundle.PersistPTRSyncMetadata(context.Background(), cfg, lease.RunToken, remoteState, true); err != nil {
		t.Fatalf("PersistPTRSyncMetadata() error = %v", err)
	}

	batch := []PTRDownloadedUpdateBatchItem{
		{
			HashHex: definitionsHash,
			Body:    []byte("definitions-1"),
			PreparedImport: PreparedLocalImport{
				HashHex:             definitionsHash,
				Size:                13,
				Mime:                28,
				ImportedAtMS:        3333,
				LocalFileServiceKey: hex.EncodeToString([]byte("repository updates")),
			},
		},
		{
			HashHex: mappingsHash,
			Body:    []byte("mappings-12345"),
			PreparedImport: PreparedLocalImport{
				HashHex:             mappingsHash,
				Size:                14,
				Mime:                29,
				ImportedAtMS:        4444,
				LocalFileServiceKey: hex.EncodeToString([]byte("repository updates")),
			},
		},
	}
	if _, err := bundle.FinalizePTRDownloadedUpdatesBatch(context.Background(), cfg, lease.RunToken, batch); err != nil {
		t.Fatalf("FinalizePTRDownloadedUpdatesBatch() error = %v", err)
	}

	definitions := PTRDefinitionsUpdate{
		ServiceHashIDsToHashes: map[int64]string{101: fixture.hash1Hex},
		ServiceTagIDsToTags:    map[int64]string{201: "ptr:applied"},
	}
	if err := bundle.ApplyPTRDefinitions(context.Background(), cfg, lease.RunToken, definitionsHash, definitions); err != nil {
		t.Fatalf("ApplyPTRDefinitions() error = %v", err)
	}

	mappings := PTRMappingsUpdate{
		Adds: []PTRMappingUpdateRow{{ServiceTagID: 201, ServiceHashIDs: []int64{101}}},
	}
	if err := bundle.ApplyPTRMappings(context.Background(), cfg, lease.RunToken, mappingsHash, mappings); err != nil {
		t.Fatalf("ApplyPTRMappings() error = %v", err)
	}

	status, err := bundle.CompletePTRSyncSuccess(context.Background(), cfg, lease.RunToken)
	if err != nil {
		t.Fatalf("CompletePTRSyncSuccess() error = %v", err)
	}
	if !status.IsComplete {
		t.Fatal("status.IsComplete = false, want true")
	}
	if status.IsUpToDate {
		t.Fatal("status.IsUpToDate = true, want false when next update due is already in the past")
	}
	if status.LastSyncMappingCount == nil || *status.LastSyncMappingCount != 1 {
		t.Fatalf("status.LastSyncMappingCount = %v, want 1", status.LastSyncMappingCount)
	}

	serviceID := selectInt64(
		t,
		bundle.conn,
		`SELECT service_id FROM main.services WHERE service_key = ?`,
		mustDecodeHex(t, coreptrsync.DaemonServiceKeyHex()),
	)
	if _, err := bundle.conn.ExecContext(
		context.Background(),
		`UPDATE main.ptr_sync_remote_state SET next_update_due = ? WHERE service_id = ?`,
		time.Now().Add(1*time.Hour).Unix(),
		serviceID,
	); err != nil {
		t.Fatalf("update ptr_sync_remote_state next_update_due error = %v", err)
	}

	status, err = bundle.GetPTRSyncStatus(context.Background(), cfg)
	if err != nil {
		t.Fatalf("GetPTRSyncStatus() after next_update_due bump error = %v", err)
	}
	if !status.IsUpToDate {
		t.Fatal("status.IsUpToDate = false, want true with no local backlog and a future next_update_due")
	}

	if err := bundle.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	readBundle, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("Open() after PTR apply error = %v", err)
	}
	defer func() {
		if err := readBundle.Close(); err != nil {
			t.Fatalf("readBundle.Close() error = %v", err)
		}
	}()

	reopenedStatus, err := readBundle.GetPTRSyncStatus(context.Background(), cfg)
	if err != nil {
		t.Fatalf("GetPTRSyncStatus() after reopen error = %v", err)
	}
	if !reopenedStatus.IsComplete {
		t.Fatal("reopenedStatus.IsComplete = false, want true")
	}
	if reopenedStatus.LastSyncMappingCount == nil || *reopenedStatus.LastSyncMappingCount != 1 {
		t.Fatalf("reopenedStatus.LastSyncMappingCount = %v, want 1", reopenedStatus.LastSyncMappingCount)
	}

	page, err := readBundle.SearchByTags(context.Background(), librarybrowse.SearchRequest{
		Request: librarybrowse.Request{Offset: 0, Limit: 10},
		Tags:    []string{"ptr:applied"},
	})
	if err != nil {
		t.Fatalf("SearchByTags(ptr:applied) after reopen error = %v", err)
	}

	if len(page.Items) != 1 {
		t.Fatalf("len(page.Items) = %d, want 1", len(page.Items))
	}

	if page.Items[0].FileID != 1 {
		t.Fatalf("page.Items[0].FileID = %d, want 1", page.Items[0].FileID)
	}

	if page.HasMore {
		t.Fatal("page.HasMore = true, want false")
	}
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

func TestBundleCountPTRPendingMappings(t *testing.T) {
	t.Run("returns zero for provisioned service with no pending rows", func(t *testing.T) {
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
			t.Fatalf("EnsurePTRSyncFoundation() error = %v", err)
		}

		info, err := bundle.CountPTRPendingMappings(context.Background(), "")
		if err != nil {
			t.Fatalf("CountPTRPendingMappings() error = %v", err)
		}

		if info.ServiceKey != coreptrsync.DaemonServiceKeyHex() {
			t.Fatalf("info.ServiceKey = %q, want %q", info.ServiceKey, coreptrsync.DaemonServiceKeyHex())
		}

		if info.PendingCount != 0 {
			t.Fatalf("info.PendingCount = %d, want 0", info.PendingCount)
		}
	})

	t.Run("returns correct count after staging pending mappings", func(t *testing.T) {
		dir, fixture := createTestBundle(t)

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

		stageResult, err := bundle.StagePTRPendingMappings(context.Background(), cfg, coreptrsync.PendingMappingsRequest{
			Hashes: []string{fixture.hash1Hex, fixture.hash2Hex},
			Tags:   []string{"creator:alice", "series:zeta"},
		})
		if err != nil {
			t.Fatalf("StagePTRPendingMappings() error = %v", err)
		}

		info, err := bundle.CountPTRPendingMappings(context.Background(), "")
		if err != nil {
			t.Fatalf("CountPTRPendingMappings() error = %v", err)
		}

		if info.PendingCount != stageResult.AddedMappings {
			t.Fatalf("info.PendingCount = %d, want %d", info.PendingCount, stageResult.AddedMappings)
		}
	})

	t.Run("returns ErrPTRServiceNotFound for unprovisioned service key", func(t *testing.T) {
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

		_, err = bundle.CountPTRPendingMappings(context.Background(), "")
		if !errors.Is(err, coreptrsync.ErrPTRServiceNotFound) {
			t.Fatalf("CountPTRPendingMappings() error = %v, want ErrPTRServiceNotFound", err)
		}
	})
}
