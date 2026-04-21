package hydrusdb

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	coreptrsync "github.com/official-elinas/hydrus-go/internal/core/ptrsync"
	"github.com/official-elinas/hydrus-go/internal/core/services"
)

const (
	ptrSyncStateSingleton                = 1
	ptrRepositoryUpdatesTablePrefix      = "repository_updates_"
	ptrRepositoryUnregisteredTablePrefix = "repository_unregistered_updates_"
	ptrRepositoryProcessedTablePrefix    = "repository_updates_processed_"
)

// ErrPTRServiceNameCollision reports that the configured PTR display name is
// already taken by another Hydrus service, so the daemon cannot safely
// provision its stable daemon-owned PTR service key.
var ErrPTRServiceNameCollision = errors.New("ptr service name collision")

var errPTRSyncRunNotActive = errors.New("PTR sync run token is not active")

// PTRSyncLease represents durable ownership of one active PTR sync pass.
type PTRSyncLease struct {
	RunToken string
	Status   coreptrsync.Status
}

// EnsurePTRSyncFoundation guarantees that the public tag repository service,
// repository mapping tables, and persisted daemon sync state exist for the
// configured anonymous PTR connection without altering any active runtime lease.
func (b *Bundle) EnsurePTRSyncFoundation(
	ctx context.Context,
	cfg coreptrsync.Config,
) (coreptrsync.Status, error) {
	status := coreptrsync.Status{}

	err := b.WithImmediateTx(ctx, func(tx *ImmediateTx) error {
		service, _, err := preparePTRSyncFoundationTx(ctx, tx, cfg)
		if err != nil {
			return err
		}

		loadedStatus, err := lookupPTRSyncStatus(ctx, tx, cfg, service)
		if err != nil {
			return err
		}

		status = loadedStatus
		return nil
	})
	if err != nil {
		return coreptrsync.Status{}, err
	}

	return status, nil
}

// RecoverPTRSyncFoundation prepares the daemon-owned PTR foundation and resets
// stale runtime sync state for startup recovery. Callers should only use it at
// times when no live sync worker should still exist.
func (b *Bundle) RecoverPTRSyncFoundation(
	ctx context.Context,
	cfg coreptrsync.Config,
) (coreptrsync.Status, error) {
	status := coreptrsync.Status{}

	err := b.WithImmediateTx(ctx, func(tx *ImmediateTx) error {
		service, _, err := preparePTRSyncFoundationTxWithMode(ctx, tx, cfg, true)
		if err != nil {
			return err
		}

		loadedStatus, err := lookupPTRSyncStatus(ctx, tx, cfg, service)
		if err != nil {
			return err
		}

		status = loadedStatus
		return nil
	})
	if err != nil {
		return coreptrsync.Status{}, err
	}

	return status, nil
}

// BeginPTRSync marks the PTR sync state as actively syncing. Callers are
// expected to serialize actual sync passes above this layer.
func (b *Bundle) BeginPTRSync(
	ctx context.Context,
	cfg coreptrsync.Config,
) (PTRSyncLease, error) {
	if !cfg.Enabled {
		return PTRSyncLease{}, coreptrsync.ErrSyncDisabled
	}

	lease := PTRSyncLease{}

	err := b.WithImmediateTx(ctx, func(tx *ImmediateTx) error {
		service, serviceID, err := preparePTRSyncFoundationTxWithMode(ctx, tx, cfg, false)
		if err != nil {
			return err
		}

		runToken, err := setPTRSyncRunning(ctx, tx, serviceID)
		if err != nil {
			return err
		}

		status, err := lookupPTRSyncStatus(ctx, tx, cfg, service)
		if err != nil {
			return err
		}

		lease = PTRSyncLease{RunToken: runToken, Status: status}
		return nil
	})
	if err != nil {
		return PTRSyncLease{}, err
	}

	return lease, nil
}

// FinishPTRSyncSuccess persists the first real remote PTR snapshot and metadata
// slice, then returns the daemon-visible idle status.
func (b *Bundle) FinishPTRSyncSuccess(
	ctx context.Context,
	cfg coreptrsync.Config,
	runToken string,
	remoteState coreptrsync.RemoteState,
	replaceMetadata bool,
) (coreptrsync.Status, error) {
	if !cfg.Enabled {
		return coreptrsync.Status{}, coreptrsync.ErrSyncDisabled
	}

	status := coreptrsync.Status{}
	err := b.WithImmediateTx(ctx, func(tx *ImmediateTx) error {
		service, serviceID, err := lookupPTRServiceTx(ctx, tx)
		if err != nil {
			return err
		}

		if err := ensurePTRSyncPersistenceTables(ctx, tx, serviceID); err != nil {
			return err
		}

		if err := ensurePTRSyncRunActive(ctx, tx, serviceID, runToken); err != nil {
			return err
		}

		hashIDsByHash, err := ensureHashIDsTx(ctx, tx, ptrUpdateHashesHex(remoteState.Metadata))
		if err != nil {
			return fmt.Errorf("ensure PTR update hash ids: %w", err)
		}

		if err := upsertPTRRemoteState(ctx, tx, serviceID, remoteState); err != nil {
			return err
		}

		nextUpdateIndex, err := applyPTRRepositoryMetadata(
			ctx,
			tx,
			serviceID,
			remoteState.Metadata,
			hashIDsByHash,
			replaceMetadata,
		)
		if err != nil {
			return err
		}

		if err := setPTRSyncIdle(ctx, tx, serviceID, runToken, nextUpdateIndex, ""); err != nil {
			return err
		}

		status, err = lookupPTRSyncStatus(ctx, tx, cfg, service)
		return err
	})
	if err != nil {
		return coreptrsync.Status{}, err
	}

	return status, nil
}

// FinishPTRSyncFailure records the latest daemon-visible PTR sync failure while
// returning the persisted idle status.
func (b *Bundle) FinishPTRSyncFailure(
	ctx context.Context,
	cfg coreptrsync.Config,
	runToken string,
	lastError string,
) (coreptrsync.Status, error) {
	if !cfg.Enabled {
		return coreptrsync.Status{}, coreptrsync.ErrSyncDisabled
	}

	status := coreptrsync.Status{}

	err := b.WithImmediateTx(ctx, func(tx *ImmediateTx) error {
		service, serviceID, err := lookupPTRServiceTx(ctx, tx)
		if err != nil {
			return err
		}

		if err := setPTRSyncFailed(ctx, tx, serviceID, runToken, lastError); err != nil {
			return err
		}

		status, err = lookupPTRSyncStatus(ctx, tx, cfg, service)
		return err
	})
	if err != nil {
		return coreptrsync.Status{}, err
	}

	return status, nil
}

// GetPTRSyncStatus returns the daemon-visible PTR sync status. It is safe to
// call on read-only or writable bundles.
func (b *Bundle) GetPTRSyncStatus(
	ctx context.Context,
	cfg coreptrsync.Config,
) (coreptrsync.Status, error) {
	if !cfg.Enabled {
		return defaultPTRSyncStatus(cfg), nil
	}

	service, ok, err := b.ByKey(ctx, coreptrsync.DaemonServiceKeyHex())
	if err != nil {
		return coreptrsync.Status{}, err
	}

	if !ok {
		return defaultPTRSyncStatus(cfg), nil
	}

	status, err := lookupPTRSyncStatus(ctx, b.conn, cfg, service)
	if err != nil {
		if isMissingPTRSyncStateTableError(err) {
			defaultStatus := defaultPTRSyncStatus(cfg)
			defaultStatus.ServiceKey = service.ServiceKey
			defaultStatus.ServiceName = service.Name
			return defaultStatus, nil
		}

		return coreptrsync.Status{}, err
	}

	return status, nil
}

type queryRowContextQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func preparePTRSyncFoundationTx(
	ctx context.Context,
	tx *ImmediateTx,
	cfg coreptrsync.Config,
) (services.Service, int64, error) {
	return preparePTRSyncFoundationTxWithMode(ctx, tx, cfg, false)
}

func preparePTRSyncFoundationTxWithMode(
	ctx context.Context,
	tx *ImmediateTx,
	cfg coreptrsync.Config,
	normalizeRuntimeState bool,
) (services.Service, int64, error) {
	service, serviceID, err := ensurePTRTagRepositoryService(ctx, tx, cfg)
	if err != nil {
		return services.Service{}, 0, err
	}

	if err := ensurePTRMappingsTables(ctx, tx, serviceID); err != nil {
		return services.Service{}, 0, err
	}

	if err := ensurePTRSyncPersistenceTables(ctx, tx, serviceID); err != nil {
		return services.Service{}, 0, err
	}

	if err := upsertPTRSyncState(ctx, tx, serviceID, normalizeRuntimeState); err != nil {
		return services.Service{}, 0, err
	}

	return service, serviceID, nil
}

func ensurePTRSyncPersistenceTables(
	ctx context.Context,
	tx *ImmediateTx,
	serviceID int64,
) error {
	if err := ensurePTRRepositoryUpdateTables(ctx, tx, serviceID); err != nil {
		return err
	}

	if err := ensurePTRSyncStateTable(ctx, tx); err != nil {
		return err
	}

	if err := ensurePTRSyncRemoteStateTable(ctx, tx); err != nil {
		return err
	}

	return nil
}

func ensurePTRTagRepositoryService(
	ctx context.Context,
	tx *ImmediateTx,
	cfg coreptrsync.Config,
) (services.Service, int64, error) {
	service, ok, err := lookupServiceByKeyTx(ctx, tx, coreptrsync.DaemonServiceKeyHex())
	if err != nil {
		return services.Service{}, 0, err
	}

	if ok {
		if service.Type != services.TypeTagRepository {
			return services.Service{}, 0, fmt.Errorf(
				"existing PTR daemon service key belongs to service type %d, want %d",
				service.Type,
				services.TypeTagRepository,
			)
		}

		if service.Name != cfg.ServiceName {
			nameCollision, collisionExists, err := lookupConflictingServiceByNameTx(
				ctx,
				tx,
				cfg.ServiceName,
				service.ServiceKey,
			)
			if err != nil {
				return services.Service{}, 0, err
			}

			if collisionExists {
				return services.Service{}, 0, fmt.Errorf(
					"%w: PTR service name %q is already in use by service key %s",
					ErrPTRServiceNameCollision,
					cfg.ServiceName,
					nameCollision.ServiceKey,
				)
			}

			if _, err := tx.ExecContext(
				ctx,
				`UPDATE main.services SET name = ? WHERE service_key = ?`,
				cfg.ServiceName,
				coreptrsync.DaemonServiceKeyBytes(),
			); err != nil {
				return services.Service{}, 0, fmt.Errorf("rename PTR tag repository service: %w", err)
			}

			service.Name = cfg.ServiceName
		}

		serviceID, err := lookupServiceIDByKeyTx(ctx, tx, service.ServiceKey)
		if err != nil {
			return services.Service{}, 0, err
		}

		return service, serviceID, nil
	}

	nameCollision, collisionExists, err := lookupConflictingServiceByNameTx(
		ctx,
		tx,
		cfg.ServiceName,
		"",
	)
	if err != nil {
		return services.Service{}, 0, err
	}

	if collisionExists {
		return services.Service{}, 0, fmt.Errorf(
			"%w: PTR service name %q is already in use by service key %s",
			ErrPTRServiceNameCollision,
			cfg.ServiceName,
			nameCollision.ServiceKey,
		)
	}

	serviceKeyBytes := coreptrsync.DaemonServiceKeyBytes()
	result, err := tx.ExecContext(
		ctx,
		`INSERT INTO main.services (service_key, service_type, name, dictionary_string)
		VALUES (?, ?, ?, ?)`,
		serviceKeyBytes,
		int(services.TypeTagRepository),
		cfg.ServiceName,
		"{}",
	)
	if err != nil {
		return services.Service{}, 0, fmt.Errorf("insert PTR tag repository service: %w", err)
	}

	serviceID, err := result.LastInsertId()
	if err != nil {
		return services.Service{}, 0, fmt.Errorf("read PTR tag repository service id: %w", err)
	}

	service = services.Service{
		Name:       cfg.ServiceName,
		ServiceKey: hex.EncodeToString(serviceKeyBytes),
		Type:       services.TypeTagRepository,
		TypePretty: services.TypePretty(services.TypeTagRepository),
	}

	return service, serviceID, nil
}

func ensurePTRMappingsTables(ctx context.Context, tx *ImmediateTx, serviceID int64) error {
	tables := []string{
		fmt.Sprintf(
			`CREATE TABLE IF NOT EXISTS external_mappings.current_mappings_%d (
				tag_id INTEGER,
				hash_id INTEGER,
				PRIMARY KEY (tag_id, hash_id)
			)`,
			serviceID,
		),
		fmt.Sprintf(
			`CREATE TABLE IF NOT EXISTS external_mappings.deleted_mappings_%d (
				tag_id INTEGER,
				hash_id INTEGER,
				PRIMARY KEY (tag_id, hash_id)
			)`,
			serviceID,
		),
		fmt.Sprintf(
			`CREATE TABLE IF NOT EXISTS external_mappings.pending_mappings_%d (
				tag_id INTEGER,
				hash_id INTEGER,
				PRIMARY KEY (tag_id, hash_id)
			)`,
			serviceID,
		),
		fmt.Sprintf(
			`CREATE TABLE IF NOT EXISTS external_mappings.petitioned_mappings_%d (
				tag_id INTEGER,
				hash_id INTEGER,
				reason_id INTEGER,
				PRIMARY KEY (tag_id, hash_id)
			)`,
			serviceID,
		),
	}

	for _, statement := range tables {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("ensure PTR mappings table: %w", err)
		}
	}

	return nil
}

func ensurePTRRepositoryUpdateTables(ctx context.Context, tx *ImmediateTx, serviceID int64) error {
	repositoryUpdatesTableName, repositoryUnregisteredTableName, repositoryProcessedTableName :=
		generatePTRRepositoryTableNames(serviceID)

	statements := []string{
		fmt.Sprintf(
			`CREATE TABLE IF NOT EXISTS %s (
				update_index INTEGER,
				hash_id INTEGER,
				PRIMARY KEY (update_index, hash_id)
			)`,
			repositoryUpdatesTableName,
		),
		fmt.Sprintf(
			`CREATE TABLE IF NOT EXISTS %s (
				hash_id INTEGER PRIMARY KEY
			)`,
			repositoryUnregisteredTableName,
		),
		fmt.Sprintf(
			`CREATE TABLE IF NOT EXISTS %s (
				hash_id INTEGER,
				content_type INTEGER,
				processed INTEGER NOT NULL DEFAULT 0,
				PRIMARY KEY (hash_id, content_type)
			)`,
			repositoryProcessedTableName,
		),
		fmt.Sprintf(
			`CREATE UNIQUE INDEX IF NOT EXISTS %s_hash_id_idx ON %s (hash_id)`,
			repositoryUpdatesTableName,
			repositoryUpdatesTableName,
		),
		fmt.Sprintf(
			`CREATE INDEX IF NOT EXISTS %s_content_type_idx ON %s (content_type)`,
			repositoryProcessedTableName,
			repositoryProcessedTableName,
		),
	}

	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("ensure PTR repository table: %w", err)
		}
	}

	return nil
}

func ensurePTRSyncStateTable(ctx context.Context, tx *ImmediateTx) error {
	if _, err := tx.ExecContext(
		ctx,
		`CREATE TABLE IF NOT EXISTS main.ptr_sync_state (
			singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
			service_id INTEGER NOT NULL,
			account_mode TEXT NOT NULL,
			phase TEXT NOT NULL,
			is_running INTEGER NOT NULL,
			run_token TEXT,
			metadata_slice INTEGER NOT NULL,
			downloaded_update_count INTEGER NOT NULL,
			processed_definition_count INTEGER NOT NULL,
			processed_content_count INTEGER NOT NULL,
			last_error TEXT,
			updated_at_ms INTEGER NOT NULL
		)`,
	); err != nil {
		return fmt.Errorf("ensure ptr_sync_state table: %w", err)
	}

	if _, err := tx.ExecContext(
		ctx,
		`ALTER TABLE main.ptr_sync_state ADD COLUMN run_token TEXT`,
	); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column name: run_token") {
		return fmt.Errorf("ensure ptr_sync_state.run_token column: %w", err)
	}

	return nil
}

func ensurePTRSyncRemoteStateTable(ctx context.Context, tx *ImmediateTx) error {
	if _, err := tx.ExecContext(
		ctx,
		`CREATE TABLE IF NOT EXISTS main.ptr_sync_remote_state (
			service_id INTEGER PRIMARY KEY,
			account_key BLOB,
			account_created INTEGER NOT NULL DEFAULT 0,
			account_expires INTEGER,
			account_message TEXT NOT NULL DEFAULT '',
			account_message_created INTEGER NOT NULL DEFAULT 0,
			account_banned_reason TEXT,
			account_banned_created INTEGER,
			account_banned_expires INTEGER,
			update_period INTEGER NOT NULL DEFAULT 0,
			nullification_period INTEGER NOT NULL DEFAULT 0,
			tag_filter_json TEXT NOT NULL DEFAULT '{}',
			next_update_due INTEGER NOT NULL DEFAULT 0,
			updated_at_ms INTEGER NOT NULL
		)`,
	); err != nil {
		return fmt.Errorf("ensure ptr_sync_remote_state table: %w", err)
	}

	return nil
}

func upsertPTRSyncState(
	ctx context.Context,
	tx *ImmediateTx,
	serviceID int64,
	normalizeRuntimeState bool,
) error {
	row := tx.QueryRowContext(
		ctx,
		`SELECT service_id, account_mode, phase, is_running, run_token, last_error
		FROM main.ptr_sync_state
		WHERE singleton = ?`,
		ptrSyncStateSingleton,
	)

	var (
		existingServiceID   int64
		existingAccountMode string
		existingPhase       string
		existingIsRunning   int64
		existingRunToken    sql.NullString
		existingLastError   sql.NullString
	)

	err := row.Scan(
		&existingServiceID,
		&existingAccountMode,
		&existingPhase,
		&existingIsRunning,
		&existingRunToken,
		&existingLastError,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			nowMS := time.Now().UTC().UnixMilli()
			if _, insertErr := tx.ExecContext(
				ctx,
				`INSERT INTO main.ptr_sync_state (
					singleton,
					service_id,
					account_mode,
					phase,
					is_running,
					run_token,
					metadata_slice,
					downloaded_update_count,
					processed_definition_count,
					processed_content_count,
					last_error,
					updated_at_ms
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				ptrSyncStateSingleton,
				serviceID,
				coreptrsync.AccountModeSharedReadOnly,
				coreptrsync.PhaseIdle,
				0,
				nil,
				0,
				0,
				0,
				0,
				nil,
				nowMS,
			); insertErr != nil {
				return fmt.Errorf("insert ptr_sync_state row: %w", insertErr)
			}

			return nil
		}

		return fmt.Errorf("query ptr_sync_state row: %w", err)
	}

	if !normalizeRuntimeState {
		return nil
	}

	if existingServiceID == serviceID &&
		existingAccountMode == coreptrsync.AccountModeSharedReadOnly &&
		existingPhase == coreptrsync.PhaseIdle &&
		existingIsRunning == 0 &&
		!existingRunToken.Valid &&
		!existingLastError.Valid {
		return nil
	}

	nowMS := time.Now().UTC().UnixMilli()
	if _, err := tx.ExecContext(
		ctx,
		`UPDATE main.ptr_sync_state
		SET service_id = ?,
			account_mode = ?,
			phase = ?,
			is_running = ?,
			run_token = ?,
			last_error = ?,
			updated_at_ms = ?
		WHERE singleton = ?`,
		serviceID,
		coreptrsync.AccountModeSharedReadOnly,
		coreptrsync.PhaseIdle,
		0,
		nil,
		nil,
		nowMS,
		ptrSyncStateSingleton,
	); err != nil {
		return fmt.Errorf("update ptr_sync_state row: %w", err)
	}

	return nil
}

func lookupPTRSyncStatus(
	ctx context.Context,
	q queryRowContextQuerier,
	cfg coreptrsync.Config,
	service services.Service,
) (coreptrsync.Status, error) {
	row := q.QueryRowContext(
		ctx,
		`SELECT
			account_mode,
			phase,
			is_running,
			metadata_slice,
			downloaded_update_count,
			processed_definition_count,
			processed_content_count,
			last_error,
			updated_at_ms
		FROM main.ptr_sync_state
		WHERE singleton = ?`,
		ptrSyncStateSingleton,
	)

	var (
		accountMode              string
		phase                    string
		isRunning                int64
		metadataSlice            int64
		downloadedUpdateCount    int64
		processedDefinitionCount int64
		processedContentCount    int64
		lastError                sql.NullString
		updatedAtMS              int64
	)

	if err := row.Scan(
		&accountMode,
		&phase,
		&isRunning,
		&metadataSlice,
		&downloadedUpdateCount,
		&processedDefinitionCount,
		&processedContentCount,
		&lastError,
		&updatedAtMS,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			defaultStatus := defaultPTRSyncStatus(cfg)
			defaultStatus.ServiceName = service.Name
			defaultStatus.ServiceKey = service.ServiceKey
			return defaultStatus, nil
		}

		return coreptrsync.Status{}, fmt.Errorf("query PTR sync state: %w", err)
	}

	status := coreptrsync.Status{
		Enabled:                  cfg.Enabled,
		Configured:               true,
		ServiceName:              service.Name,
		ServiceKey:               service.ServiceKey,
		Host:                     cfg.Host,
		Port:                     cfg.Port,
		AccountMode:              accountMode,
		Phase:                    phase,
		IsRunning:                isRunning != 0,
		MetadataSlice:            metadataSlice,
		DownloadedUpdateCount:    downloadedUpdateCount,
		ProcessedDefinitionCount: processedDefinitionCount,
		ProcessedContentCount:    processedContentCount,
		UpdatedAtMS:              updatedAtMS,
	}
	if lastError.Valid {
		status.LastError = lastError.String
	}

	return status, nil
}

func defaultPTRSyncStatus(cfg coreptrsync.Config) coreptrsync.Status {
	phase := coreptrsync.PhaseDisabled
	if cfg.Enabled {
		phase = coreptrsync.PhaseIdle
	}

	return coreptrsync.Status{
		Enabled:     cfg.Enabled,
		Configured:  false,
		ServiceName: cfg.ServiceName,
		Host:        cfg.Host,
		Port:        cfg.Port,
		AccountMode: coreptrsync.AccountModeSharedReadOnly,
		Phase:       phase,
		IsRunning:   false,
	}
}

func generatePTRRepositoryTableNames(serviceID int64) (string, string, string) {
	return fmt.Sprintf("%s%d", ptrRepositoryUpdatesTablePrefix, serviceID),
		fmt.Sprintf("%s%d", ptrRepositoryUnregisteredTablePrefix, serviceID),
		fmt.Sprintf("%s%d", ptrRepositoryProcessedTablePrefix, serviceID)
}

func lookupPTRServiceTx(
	ctx context.Context,
	q queryRowContextQuerier,
) (services.Service, int64, error) {
	service, ok, err := lookupServiceByKeyTx(ctx, q, coreptrsync.DaemonServiceKeyHex())
	if err != nil {
		return services.Service{}, 0, err
	}

	if !ok {
		return services.Service{}, 0, errors.New("PTR daemon service does not exist")
	}

	serviceID, err := lookupServiceIDByKeyTx(ctx, q, service.ServiceKey)
	if err != nil {
		return services.Service{}, 0, err
	}

	return service, serviceID, nil
}

func setPTRSyncRunning(ctx context.Context, tx *ImmediateTx, serviceID int64) (string, error) {
	runToken, err := newPTRSyncRunToken()
	if err != nil {
		return "", err
	}

	nowMS := time.Now().UTC().UnixMilli()
	result, err := tx.ExecContext(
		ctx,
		`UPDATE main.ptr_sync_state
		SET service_id = ?,
			account_mode = ?,
			phase = ?,
			is_running = ?,
			run_token = ?,
			last_error = ?,
			updated_at_ms = ?
		WHERE singleton = ? AND is_running = 0`,
		serviceID,
		coreptrsync.AccountModeSharedReadOnly,
		coreptrsync.PhaseSyncing,
		1,
		runToken,
		nil,
		nowMS,
		ptrSyncStateSingleton,
	)
	if err != nil {
		return "", fmt.Errorf("set ptr_sync_state running: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return "", fmt.Errorf("read ptr_sync_state running rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return "", coreptrsync.ErrSyncAlreadyRunning
	}

	return runToken, nil
}

func ensurePTRSyncRunActive(
	ctx context.Context,
	q queryRowContextQuerier,
	serviceID int64,
	runToken string,
) error {
	row := q.QueryRowContext(
		ctx,
		`SELECT 1 FROM main.ptr_sync_state
		WHERE singleton = ? AND service_id = ? AND is_running = 1 AND run_token = ?`,
		ptrSyncStateSingleton,
		serviceID,
		runToken,
	)

	var sentinel int64
	if err := row.Scan(&sentinel); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errPTRSyncRunNotActive
		}

		return fmt.Errorf("query active PTR sync run: %w", err)
	}

	return nil
}

func setPTRSyncIdle(
	ctx context.Context,
	tx *ImmediateTx,
	serviceID int64,
	runToken string,
	nextUpdateIndex int64,
	lastError string,
) error {
	nowMS := time.Now().UTC().UnixMilli()
	var lastErrorValue any
	if trimmed := strings.TrimSpace(lastError); trimmed != "" {
		lastErrorValue = trimmed
	}

	result, err := tx.ExecContext(
		ctx,
		`UPDATE main.ptr_sync_state
		SET service_id = ?,
			account_mode = ?,
			phase = ?,
			is_running = ?,
			run_token = ?,
			metadata_slice = ?,
			last_error = ?,
			updated_at_ms = ?
		WHERE singleton = ? AND is_running = 1 AND run_token = ?`,
		serviceID,
		coreptrsync.AccountModeSharedReadOnly,
		coreptrsync.PhaseIdle,
		0,
		nil,
		nextUpdateIndex,
		lastErrorValue,
		nowMS,
		ptrSyncStateSingleton,
		runToken,
	)
	if err != nil {
		return fmt.Errorf("set ptr_sync_state idle: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read ptr_sync_state idle rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return errPTRSyncRunNotActive
	}

	return nil
}

func setPTRSyncFailed(
	ctx context.Context,
	tx *ImmediateTx,
	serviceID int64,
	runToken string,
	lastError string,
) error {
	trimmedLastError := strings.TrimSpace(lastError)
	if trimmedLastError == "" {
		trimmedLastError = "PTR sync failed"
	}

	nowMS := time.Now().UTC().UnixMilli()
	result, err := tx.ExecContext(
		ctx,
		`UPDATE main.ptr_sync_state
		SET service_id = ?,
			account_mode = ?,
			phase = ?,
			is_running = ?,
			run_token = ?,
			last_error = ?,
			updated_at_ms = ?
		WHERE singleton = ? AND is_running = 1 AND run_token = ?`,
		serviceID,
		coreptrsync.AccountModeSharedReadOnly,
		coreptrsync.PhaseIdle,
		0,
		nil,
		trimmedLastError,
		nowMS,
		ptrSyncStateSingleton,
		runToken,
	)
	if err != nil {
		return fmt.Errorf("set ptr_sync_state failed: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read ptr_sync_state failed rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return errPTRSyncRunNotActive
	}

	return nil
}

func upsertPTRRemoteState(
	ctx context.Context,
	tx *ImmediateTx,
	serviceID int64,
	remoteState coreptrsync.RemoteState,
) error {
	tagFilterRules := remoteState.TagFilter.Rules
	if tagFilterRules == nil {
		tagFilterRules = map[string]int{}
	}

	tagFilterJSON, err := json.Marshal(tagFilterRules)
	if err != nil {
		return fmt.Errorf("marshal PTR tag filter: %w", err)
	}

	nowMS := time.Now().UTC().UnixMilli()
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO main.ptr_sync_remote_state (
			service_id,
			account_key,
			account_created,
			account_expires,
			account_message,
			account_message_created,
			account_banned_reason,
			account_banned_created,
			account_banned_expires,
			update_period,
			nullification_period,
			tag_filter_json,
			next_update_due,
			updated_at_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(service_id) DO UPDATE SET
			account_key = excluded.account_key,
			account_created = excluded.account_created,
			account_expires = excluded.account_expires,
			account_message = excluded.account_message,
			account_message_created = excluded.account_message_created,
			account_banned_reason = excluded.account_banned_reason,
			account_banned_created = excluded.account_banned_created,
			account_banned_expires = excluded.account_banned_expires,
			update_period = excluded.update_period,
			nullification_period = excluded.nullification_period,
			tag_filter_json = excluded.tag_filter_json,
			next_update_due = excluded.next_update_due,
			updated_at_ms = excluded.updated_at_ms`,
		serviceID,
		nullableBytes(remoteState.Account.AccountKey),
		remoteState.Account.Created,
		nullableInt64(remoteState.Account.Expires),
		remoteState.Account.Message,
		remoteState.Account.MessageCreated,
		nullableString(remoteState.Account.BannedReason),
		nullableInt64(remoteState.Account.BannedCreated),
		nullableInt64(remoteState.Account.BannedExpires),
		remoteState.ServiceOptions.UpdatePeriod,
		remoteState.ServiceOptions.NullificationPeriod,
		string(tagFilterJSON),
		remoteState.Metadata.NextUpdateDue,
		nowMS,
	); err != nil {
		return fmt.Errorf("upsert ptr_sync_remote_state row: %w", err)
	}

	return nil
}

func applyPTRRepositoryMetadata(
	ctx context.Context,
	tx *ImmediateTx,
	serviceID int64,
	metadata coreptrsync.MetadataSlice,
	hashIDsByHash map[string]int64,
	replaceMetadata bool,
) (int64, error) {
	repositoryUpdatesTableName, repositoryUnregisteredTableName, repositoryProcessedTableName :=
		generatePTRRepositoryTableNames(serviceID)

	type repositoryUpdateRow struct {
		updateIndex int64
		hashID      int64
	}

	updateRows := make([]repositoryUpdateRow, 0)
	allFutureHashIDs := map[int64]struct{}{}
	for _, update := range metadata.Updates {
		for _, updateHash := range update.UpdateHashes {
			hashID, ok := hashIDsByHash[hex.EncodeToString(updateHash)]
			if !ok {
				return 0, fmt.Errorf("missing PTR update hash id for %x", updateHash)
			}

			updateRows = append(updateRows, repositoryUpdateRow{updateIndex: update.UpdateIndex, hashID: hashID})
			allFutureHashIDs[hashID] = struct{}{}
		}
	}

	if replaceMetadata {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s", repositoryUpdatesTableName)); err != nil {
			return 0, fmt.Errorf("clear PTR repository updates: %w", err)
		}

		if _, err := tx.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s", repositoryUnregisteredTableName)); err != nil {
			return 0, fmt.Errorf("clear PTR repository unregistered updates: %w", err)
		}

		if err := prunePTRProcessedUpdates(ctx, tx, repositoryProcessedTableName, allFutureHashIDs); err != nil {
			return 0, err
		}
	}

	for _, row := range updateRows {
		if _, err := tx.ExecContext(
			ctx,
			fmt.Sprintf(
				"INSERT OR IGNORE INTO %s (update_index, hash_id) VALUES (?, ?)",
				repositoryUpdatesTableName,
			),
			row.updateIndex,
			row.hashID,
		); err != nil {
			return 0, fmt.Errorf("insert PTR repository update row: %w", err)
		}
	}

	for hashID := range allFutureHashIDs {
		if _, err := tx.ExecContext(
			ctx,
			fmt.Sprintf(
				"INSERT OR IGNORE INTO %s (hash_id) VALUES (?)",
				repositoryUnregisteredTableName,
			),
			hashID,
		); err != nil {
			return 0, fmt.Errorf("insert PTR repository unregistered update row: %w", err)
		}
	}

	nextUpdateIndex, err := queryPTRNextUpdateIndex(ctx, tx, repositoryUpdatesTableName)
	if err != nil {
		return 0, err
	}

	return nextUpdateIndex, nil
}

func prunePTRProcessedUpdates(
	ctx context.Context,
	tx *ImmediateTx,
	processedTableName string,
	keepHashIDs map[int64]struct{},
) error {
	if len(keepHashIDs) == 0 {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s", processedTableName)); err != nil {
			return fmt.Errorf("clear PTR processed updates: %w", err)
		}

		return nil
	}

	rows, err := tx.QueryContext(
		ctx,
		fmt.Sprintf("SELECT DISTINCT hash_id FROM %s", processedTableName),
	)
	if err != nil {
		return fmt.Errorf("query PTR processed update hashes: %w", err)
	}
	defer rows.Close()

	deleteHashIDs := make([]int64, 0)
	for rows.Next() {
		var hashID int64
		if err := rows.Scan(&hashID); err != nil {
			return fmt.Errorf("scan PTR processed update hash: %w", err)
		}

		if _, ok := keepHashIDs[hashID]; ok {
			continue
		}

		deleteHashIDs = append(deleteHashIDs, hashID)
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate PTR processed update hashes: %w", err)
	}

	for _, hashID := range deleteHashIDs {
		if _, err := tx.ExecContext(
			ctx,
			fmt.Sprintf("DELETE FROM %s WHERE hash_id = ?", processedTableName),
			hashID,
		); err != nil {
			return fmt.Errorf("delete stale PTR processed update row: %w", err)
		}
	}

	return nil
}

func queryPTRNextUpdateIndex(
	ctx context.Context,
	q queryRowContextQuerier,
	repositoryUpdatesTableName string,
) (int64, error) {
	row := q.QueryRowContext(
		ctx,
		fmt.Sprintf("SELECT COALESCE(MAX(update_index) + 1, 0) FROM %s", repositoryUpdatesTableName),
	)

	var nextUpdateIndex int64
	if err := row.Scan(&nextUpdateIndex); err != nil {
		return 0, fmt.Errorf("query PTR next update index: %w", err)
	}

	return nextUpdateIndex, nil
}

func ptrUpdateHashesHex(metadata coreptrsync.MetadataSlice) []string {
	hashes := make([]string, 0)
	seen := map[string]struct{}{}
	for _, update := range metadata.Updates {
		for _, updateHash := range update.UpdateHashes {
			hashHex := hex.EncodeToString(updateHash)
			if _, ok := seen[hashHex]; ok {
				continue
			}

			seen[hashHex] = struct{}{}
			hashes = append(hashes, hashHex)
		}
	}

	return hashes
}

func newPTRSyncRunToken() (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate PTR sync run token: %w", err)
	}

	return hex.EncodeToString(buffer), nil
}

func nullableInt64(value *int64) any {
	if value == nil {
		return nil
	}

	return *value
}

func nullableBytes(value []byte) any {
	if len(value) == 0 {
		return nil
	}

	return value
}

func nullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}

	return value
}

func lookupConflictingServiceByNameTx(
	ctx context.Context,
	q queryRowContextQuerier,
	name string,
	excludeServiceKey string,
) (services.Service, bool, error) {
	rows, err := q.QueryContext(
		ctx,
		`SELECT lower(hex(service_key)), service_type, name, dictionary_string
		FROM main.services
		ORDER BY service_id ASC`,
	)
	if err != nil {
		return services.Service{}, false, fmt.Errorf("query services by name: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		service, scanErr := scanCatalogService(rows)
		if scanErr != nil {
			return services.Service{}, false, scanErr
		}

		if !strings.EqualFold(service.Name, name) {
			continue
		}

		if excludeServiceKey != "" && strings.EqualFold(service.ServiceKey, excludeServiceKey) {
			continue
		}

		return service, true, nil
	}

	if err := rows.Err(); err != nil {
		return services.Service{}, false, fmt.Errorf("iterate services by name: %w", err)
	}

	return services.Service{}, false, nil
}

func lookupServiceByKeyTx(
	ctx context.Context,
	q queryRowContextQuerier,
	serviceKey string,
) (services.Service, bool, error) {
	decodedKey, err := hex.DecodeString(strings.ToLower(strings.TrimSpace(serviceKey)))
	if err != nil {
		return services.Service{}, false, fmt.Errorf("decode PTR service key: %w", err)
	}

	row := q.QueryRowContext(
		ctx,
		`SELECT lower(hex(service_key)), service_type, name, dictionary_string
		FROM main.services
		WHERE service_key = ?
		LIMIT 1`,
		decodedKey,
	)

	service, err := scanCatalogService(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return services.Service{}, false, nil
		}

		return services.Service{}, false, err
	}

	return service, true, nil
}

func lookupServiceIDByKeyTx(
	ctx context.Context,
	q queryRowContextQuerier,
	serviceKey string,
) (int64, error) {
	decodedKey, err := hex.DecodeString(strings.ToLower(strings.TrimSpace(serviceKey)))
	if err != nil {
		return 0, fmt.Errorf("decode PTR service key: %w", err)
	}

	row := q.QueryRowContext(
		ctx,
		`SELECT service_id FROM main.services WHERE service_key = ?`,
		decodedKey,
	)

	var serviceID int64
	if err := row.Scan(&serviceID); err != nil {
		return 0, fmt.Errorf("query PTR service id: %w", err)
	}

	return serviceID, nil
}

func isMissingPTRSyncStateTableError(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "no such table: main.ptr_sync_state") ||
		strings.Contains(strings.ToLower(err.Error()), "no such table: ptr_sync_state")
}
