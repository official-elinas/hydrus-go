package hydrusdb

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	coreptrsync "github.com/official-elinas/hydrus-go/internal/core/ptrsync"
	"github.com/official-elinas/hydrus-go/internal/core/services"
)

const ptrSyncStateSingleton = 1

// ErrPTRServiceNameCollision reports that the configured PTR display name is
// already taken by another Hydrus service, so the daemon cannot safely
// provision its stable daemon-owned PTR service key.
var ErrPTRServiceNameCollision = errors.New("ptr service name collision")

// EnsurePTRSyncFoundation guarantees that the public tag repository service,
// repository mapping tables, and persisted daemon sync state exist for the
// configured anonymous PTR connection.
func (b *Bundle) EnsurePTRSyncFoundation(
	ctx context.Context,
	cfg coreptrsync.Config,
) (coreptrsync.Status, error) {
	status := coreptrsync.Status{}

	err := b.WithImmediateTx(ctx, func(tx *ImmediateTx) error {
		service, serviceID, err := ensurePTRTagRepositoryService(ctx, tx, cfg)
		if err != nil {
			return err
		}

		if err := ensurePTRMappingsTables(ctx, tx, serviceID); err != nil {
			return err
		}

		if err := ensurePTRSyncStateTable(ctx, tx); err != nil {
			return err
		}

		if err := upsertPTRSyncState(ctx, tx, serviceID); err != nil {
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

func ensurePTRSyncStateTable(ctx context.Context, tx *ImmediateTx) error {
	if _, err := tx.ExecContext(
		ctx,
		`CREATE TABLE IF NOT EXISTS main.ptr_sync_state (
			singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
			service_id INTEGER NOT NULL,
			account_mode TEXT NOT NULL,
			phase TEXT NOT NULL,
			is_running INTEGER NOT NULL,
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

	return nil
}

func upsertPTRSyncState(ctx context.Context, tx *ImmediateTx, serviceID int64) error {
	row := tx.QueryRowContext(
		ctx,
		`SELECT service_id, account_mode, phase, is_running, last_error
		FROM main.ptr_sync_state
		WHERE singleton = ?`,
		ptrSyncStateSingleton,
	)

	var (
		existingServiceID   int64
		existingAccountMode string
		existingPhase       string
		existingIsRunning   int64
		existingLastError   sql.NullString
	)

	err := row.Scan(
		&existingServiceID,
		&existingAccountMode,
		&existingPhase,
		&existingIsRunning,
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
					metadata_slice,
					downloaded_update_count,
					processed_definition_count,
					processed_content_count,
					last_error,
					updated_at_ms
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				ptrSyncStateSingleton,
				serviceID,
				coreptrsync.AccountModeSharedReadOnly,
				coreptrsync.PhaseIdle,
				0,
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

	if existingServiceID == serviceID &&
		existingAccountMode == coreptrsync.AccountModeSharedReadOnly &&
		existingPhase == coreptrsync.PhaseIdle &&
		existingIsRunning == 0 &&
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
			last_error = ?,
			updated_at_ms = ?
		WHERE singleton = ?`,
		serviceID,
		coreptrsync.AccountModeSharedReadOnly,
		coreptrsync.PhaseIdle,
		0,
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
