package hydrusdb

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
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
			SET phase = ?, is_running = 1, last_error = ?, service_id = ?
			WHERE singleton = ?`,
			coreptrsync.PhaseSyncing,
			"stale worker crash",
			serviceID,
			ptrSyncStateSingleton,
		); err != nil {
			t.Fatalf("UPDATE ptr_sync_state error = %v", err)
		}

		normalized, err := bundle.EnsurePTRSyncFoundation(context.Background(), cfg)
		if err != nil {
			t.Fatalf("EnsurePTRSyncFoundation(normalize) error = %v", err)
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
