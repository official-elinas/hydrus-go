package hydrusdb

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	coreptrsync "github.com/official-elinas/hydrus-go/internal/core/ptrsync"
	"github.com/official-elinas/hydrus-go/internal/core/services"
	coretags "github.com/official-elinas/hydrus-go/internal/core/tags"
)

const (
	ptrSyncStateSingleton                = 1
	ptrRepositoryHashIDMapTablePrefix    = "repository_hash_id_map_"
	ptrRepositoryTagIDMapTablePrefix     = "repository_tag_id_map_"
	ptrRepositoryUpdatesTablePrefix      = "repository_updates_"
	ptrRepositoryUnregisteredTablePrefix = "repository_unregistered_updates_"
	ptrRepositoryProcessedTablePrefix    = "repository_updates_processed_"
	ptrRepositoryUpdatesServiceName      = "repository updates"
	ptrInvalidRepositoryTag              = "invalid repository tag"

	PTRContentTypeMappings    int64 = 0
	PTRContentTypeDefinitions int64 = 21

	ptrHydrusUpdateDefinitionsMime = 28
	ptrHydrusUpdateContentMime     = 29
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

// PTRDownloadedUpdateBatchItem is one downloaded PTR update artifact that
// should be durably persisted inside SQLite in a single DB transaction.
type PTRDownloadedUpdateBatchItem struct {
	HashHex        string
	Body           []byte
	PreparedImport PreparedLocalImport
}

// PTRProcessableUpdate describes one SQLite-backed repository update artifact
// that is eligible for ordered processing in the current sync pass.
type PTRProcessableUpdate struct {
	UpdateIndex int64
	HashID      int64
	HashHex     string
	ContentType int64
	Body        []byte
	Processed   bool
}

// PTRApplyUpdateBatchItem is one decoded PTR update that is ready to be
// applied in update order inside a single DB transaction.
type PTRApplyUpdateBatchItem struct {
	HashHex     string
	ContentType int64
	Definitions PTRDefinitionsUpdate
	Mappings    PTRMappingsUpdate
}

// PTRDefinitionsUpdate is the minimal decoded repository definitions payload
// needed to normalize future mapping updates.
type PTRDefinitionsUpdate struct {
	ServiceHashIDsToHashes map[int64]string
	ServiceTagIDsToTags    map[int64]string
}

// PTRMappingUpdateRow is one repository-local mappings row.
type PTRMappingUpdateRow struct {
	ServiceTagID   int64
	ServiceHashIDs []int64
}

// PTRMappingsUpdate is the minimal decoded mappings payload needed to write the
// external_mappings current/deleted tables.
type PTRMappingsUpdate struct {
	Adds    []PTRMappingUpdateRow
	Deletes []PTRMappingUpdateRow
}

// PTRPendingMappingGroup is one outbound pending mappings group in the exact
// logical shape Hydrus uploads to tag repositories: one tag plus many hashes.
type PTRPendingMappingGroup struct {
	Tag    string
	Hashes []string
}

type processedRow struct {
	hashID      int64
	contentType int64
	processed   bool
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

// StagePTRPendingMappings records add-only pending mappings for the daemon-owned
// PTR service using either file IDs or hash hex strings as file identifiers.
func (b *Bundle) StagePTRPendingMappings(
	ctx context.Context,
	cfg coreptrsync.Config,
	request coreptrsync.PendingMappingsRequest,
) (coreptrsync.PendingMappingsResult, error) {
	if !cfg.Enabled {
		return coreptrsync.PendingMappingsResult{}, coreptrsync.ErrSyncDisabled
	}

	targetServiceKey, err := normalizePTRTargetServiceKey(request.ServiceKey)
	if err != nil {
		return coreptrsync.PendingMappingsResult{}, err
	}

	tags := dedupeStrings(request.Tags)
	if len(tags) == 0 {
		return coreptrsync.PendingMappingsResult{}, errors.New("at least one tag is required")
	}

	hashes, err := b.resolvePTRPendingRequestHashes(ctx, request)
	if err != nil {
		return coreptrsync.PendingMappingsResult{}, err
	}
	if len(hashes) == 0 {
		return coreptrsync.PendingMappingsResult{}, errors.New("at least one file identifier is required")
	}

	hashIDsByHash, err := b.lookupKnownHashIDs(ctx, hashes)
	if err != nil {
		return coreptrsync.PendingMappingsResult{}, err
	}

	missingHashes := []string{}
	hashIDs := make([]int64, 0, len(hashes))
	for _, hash := range hashes {
		hashID, ok := hashIDsByHash[hash]
		if !ok {
			missingHashes = append(missingHashes, hash)
			continue
		}

		hashIDs = append(hashIDs, hashID)
	}
	if len(missingHashes) > 0 {
		return coreptrsync.PendingMappingsResult{}, fmt.Errorf("hashes not found: %v", missingHashes)
	}

	result := coreptrsync.PendingMappingsResult{ServiceKey: targetServiceKey}
	err = b.WithImmediateTx(ctx, func(tx *ImmediateTx) error {
		service, serviceID, err := preparePTRSyncFoundationTx(ctx, tx, cfg)
		if err != nil {
			return err
		}

		if service.ServiceKey != targetServiceKey {
			return fmt.Errorf("PTR service key %q is unavailable", targetServiceKey)
		}

		addedMappings, err := stagePTRPendingMappingsTx(ctx, tx, serviceID, tags, hashIDs)
		if err != nil {
			return err
		}

		result.AddedMappings = addedMappings
		return nil
	})
	if err != nil {
		return coreptrsync.PendingMappingsResult{}, err
	}

	return result, nil
}

// ListPTRPendingMappingsForCommit returns grouped pending add mappings in the
// exact logical shape needed for Hydrus client-to-server repository updates.
func (b *Bundle) ListPTRPendingMappingsForCommit(
	ctx context.Context,
	cfg coreptrsync.Config,
	serviceKey string,
) ([]PTRPendingMappingGroup, error) {
	if !cfg.Enabled {
		return nil, coreptrsync.ErrSyncDisabled
	}

	targetServiceKey, err := normalizePTRTargetServiceKey(serviceKey)
	if err != nil {
		return nil, err
	}

	service, ok, err := lookupServiceByKeyTx(ctx, b.conn, targetServiceKey)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("PTR service key %q is unavailable", targetServiceKey)
	}

	serviceID, err := lookupServiceIDByKeyTx(ctx, b.conn, targetServiceKey)
	if err != nil {
		return nil, err
	}

	if service.Type != services.TypeTagRepository {
		return nil, fmt.Errorf("service %q is not a tag repository", targetServiceKey)
	}

	query := fmt.Sprintf(
		`SELECT pm.tag_id, lower(hex(h.hash))
		FROM external_mappings.pending_mappings_%d pm
		JOIN external_master.hashes h USING (hash_id)
		ORDER BY pm.tag_id ASC, pm.hash_id ASC`,
		serviceID,
	)

	rows, err := b.conn.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query PTR pending mappings for commit: %w", err)
	}
	defer rows.Close()

	tagIDsInOrder := []int64{}
	hashesByTagID := map[int64][]string{}
	seenTagIDs := map[int64]struct{}{}
	for rows.Next() {
		var (
			tagID   int64
			hashHex string
		)

		if err := rows.Scan(&tagID, &hashHex); err != nil {
			return nil, fmt.Errorf("scan PTR pending mapping row: %w", err)
		}

		if _, ok := seenTagIDs[tagID]; !ok {
			seenTagIDs[tagID] = struct{}{}
			tagIDsInOrder = append(tagIDsInOrder, tagID)
		}

		hashesByTagID[tagID] = append(hashesByTagID[tagID], hashHex)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate PTR pending mapping rows: %w", err)
	}

	tagsByID, err := b.lookupTagsByID(ctx, tagIDsInOrder)
	if err != nil {
		return nil, err
	}

	groups := make([]PTRPendingMappingGroup, 0, len(tagIDsInOrder))
	for _, tagID := range tagIDsInOrder {
		tag, ok := tagsByID[tagID]
		if !ok {
			return nil, fmt.Errorf("PTR pending mapping tag_id %d could not be resolved", tagID)
		}

		groups = append(groups, PTRPendingMappingGroup{
			Tag:    tag,
			Hashes: hashesByTagID[tagID],
		})
	}

	return groups, nil
}

// CountPTRPendingMappings returns the number of locally staged pending add
// mappings for the given PTR service key.
func (b *Bundle) CountPTRPendingMappings(
	ctx context.Context,
	serviceKey string,
) (coreptrsync.PendingInfo, error) {
	targetServiceKey, err := normalizePTRTargetServiceKey(serviceKey)
	if err != nil {
		return coreptrsync.PendingInfo{}, err
	}

	serviceID, err := lookupServiceIDByKeyTx(ctx, b.conn, targetServiceKey)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return coreptrsync.PendingInfo{}, fmt.Errorf("%w: %s", coreptrsync.ErrPTRServiceNotFound, targetServiceKey)
		}
		return coreptrsync.PendingInfo{}, err
	}

	var count int64
	row := b.conn.QueryRowContext(
		ctx,
		fmt.Sprintf(`SELECT COUNT(*) FROM external_mappings.pending_mappings_%d`, serviceID),
	)
	if err := row.Scan(&count); err != nil {
		return coreptrsync.PendingInfo{}, fmt.Errorf("count PTR pending mappings: %w", err)
	}

	return coreptrsync.PendingInfo{ServiceKey: targetServiceKey, PendingCount: count}, nil
}

// CommitPTRPendingMappingsSuccess applies the local successful-commit state
// transition for all currently pending add mappings on the daemon-owned PTR.
func (b *Bundle) CommitPTRPendingMappingsSuccess(
	ctx context.Context,
	cfg coreptrsync.Config,
	serviceKey string,
) (coreptrsync.CommitPendingResult, error) {
	if !cfg.Enabled {
		return coreptrsync.CommitPendingResult{}, coreptrsync.ErrSyncDisabled
	}

	targetServiceKey, err := normalizePTRTargetServiceKey(serviceKey)
	if err != nil {
		return coreptrsync.CommitPendingResult{}, err
	}

	result := coreptrsync.CommitPendingResult{ServiceKey: targetServiceKey}
	err = b.WithImmediateTx(ctx, func(tx *ImmediateTx) error {
		service, serviceID, err := preparePTRSyncFoundationTx(ctx, tx, cfg)
		if err != nil {
			return err
		}

		if service.ServiceKey != targetServiceKey {
			return fmt.Errorf("PTR service key %q is unavailable", targetServiceKey)
		}

		committedMappings, err := commitPTRPendingMappingsSuccessTx(ctx, tx, serviceID)
		if err != nil {
			return err
		}

		result.CommittedMappings = committedMappings
		return nil
	})
	if err != nil {
		return coreptrsync.CommitPendingResult{}, err
	}

	return result, nil
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

// PersistPTRSyncMetadata persists the fetched remote PTR snapshot and metadata
// slice while keeping the sync lease active for follow-up update downloads.
func (b *Bundle) PersistPTRSyncMetadata(
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

		if err := ensurePTRRepositoryDefinitionTables(ctx, tx, serviceID); err != nil {
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

		_, err = applyPTRRepositoryMetadata(
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

		downloadedCount, err := queryPTRDownloadedUpdateCount(ctx, tx, serviceID)
		if err != nil {
			return err
		}

		downloadedBytes, err := queryPTRDownloadedUpdateBytes(ctx, tx, serviceID)
		if err != nil {
			return err
		}

		if err := updatePTRSyncDownloadedUpdateCount(ctx, tx, runToken, downloadedCount, downloadedBytes); err != nil {
			return err
		}

		if err := recomputePTRProcessedCounts(ctx, tx, serviceID, runToken); err != nil {
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

// CompletePTRSyncSuccess ends an active PTR sync lease after metadata
// persistence and update downloads are finished.
func (b *Bundle) CompletePTRSyncSuccess(
	ctx context.Context,
	cfg coreptrsync.Config,
	runToken string,
) (coreptrsync.Status, error) {
	return b.finishPTRSyncIdle(ctx, cfg, runToken, "", true)
}

// CancelPTRSync clears an active PTR sync lease without recording a failure.
// This is used for interrupted runs such as daemon shutdown where the next run
// should continue from persisted state rather than surface a terminal error.
func (b *Bundle) CancelPTRSync(
	ctx context.Context,
	cfg coreptrsync.Config,
	runToken string,
) (coreptrsync.Status, error) {
	return b.finishPTRSyncIdle(ctx, cfg, runToken, "", false)
}

func (b *Bundle) finishPTRSyncIdle(
	ctx context.Context,
	cfg coreptrsync.Config,
	runToken string,
	lastError string,
	updateMappingCount bool,
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

		repositoryUpdatesTableName, _, _ := generatePTRRepositoryTableNames(serviceID)
		nextUpdateIndex, err := queryPTRNextUpdateIndex(ctx, tx, repositoryUpdatesTableName)
		if err != nil {
			return err
		}

		var lastSyncMappingCount *int64
		if updateMappingCount {
			mappingCount, err := queryPTRCurrentMappingCount(ctx, tx, serviceID)
			if err != nil {
				return err
			}
			lastSyncMappingCount = &mappingCount
		}

		if err := setPTRSyncIdle(ctx, tx, serviceID, runToken, nextUpdateIndex, lastError, lastSyncMappingCount); err != nil {
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

// FinishPTRSyncSuccess preserves the original wrapper behavior for callers that
// complete PTR sync in one step.
func (b *Bundle) FinishPTRSyncSuccess(
	ctx context.Context,
	cfg coreptrsync.Config,
	runToken string,
	remoteState coreptrsync.RemoteState,
	replaceMetadata bool,
) (coreptrsync.Status, error) {
	if _, err := b.PersistPTRSyncMetadata(ctx, cfg, runToken, remoteState, replaceMetadata); err != nil {
		return coreptrsync.Status{}, err
	}

	return b.CompletePTRSyncSuccess(ctx, cfg, runToken)
}

// ListPTRPendingUpdateHashes returns the currently unregistered PTR update
// hashes in deterministic hash_id order for an active run.
func (b *Bundle) ListPTRPendingUpdateHashes(
	ctx context.Context,
	cfg coreptrsync.Config,
	runToken string,
) ([][]byte, error) {
	if !cfg.Enabled {
		return nil, coreptrsync.ErrSyncDisabled
	}

	var hashes [][]byte
	err := b.WithImmediateTx(ctx, func(tx *ImmediateTx) error {
		_, serviceID, err := lookupPTRServiceTx(ctx, tx)
		if err != nil {
			return err
		}

		if err := ensurePTRSyncRunActive(ctx, tx, serviceID, runToken); err != nil {
			return err
		}

		_, repositoryUnregisteredTableName, _ := generatePTRRepositoryTableNames(serviceID)
		rows, err := tx.QueryContext(
			ctx,
			fmt.Sprintf(
				`SELECT h.hash
				FROM %s ru
				JOIN external_master.hashes h USING (hash_id)
				ORDER BY ru.hash_id ASC`,
				repositoryUnregisteredTableName,
			),
		)
		if err != nil {
			return fmt.Errorf("query PTR pending update hashes: %w", err)
		}
		defer rows.Close()

		hashes = nil
		for rows.Next() {
			var hash []byte
			if err := rows.Scan(&hash); err != nil {
				return fmt.Errorf("scan PTR pending update hash: %w", err)
			}

			hashes = append(hashes, append([]byte(nil), hash...))
		}

		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate PTR pending update hashes: %w", err)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return hashes, nil
}

// FinalizePTRDownloadedUpdate marks one successfully downloaded PTR update as
// locally registered while keeping the sync lease active.
func (b *Bundle) FinalizePTRDownloadedUpdate(
	ctx context.Context,
	cfg coreptrsync.Config,
	runToken string,
	hashHex string,
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

		hashBytes, decodeErr := hex.DecodeString(strings.ToLower(strings.TrimSpace(hashHex)))
		if decodeErr != nil {
			return fmt.Errorf("decode PTR downloaded update hash: %w", decodeErr)
		}

		hashID, ok, err := lookupHashIDByHash(ctx, tx, hashBytes)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("PTR downloaded update hash %s is not registered locally", hashHex)
		}

		_, repositoryUnregisteredTableName, _ := generatePTRRepositoryTableNames(serviceID)
		if _, err := tx.ExecContext(
			ctx,
			fmt.Sprintf("DELETE FROM %s WHERE hash_id = ?", repositoryUnregisteredTableName),
			hashID,
		); err != nil {
			return fmt.Errorf("delete PTR repository unregistered update row: %w", err)
		}

		downloadedCount, err := queryPTRDownloadedUpdateCount(ctx, tx, serviceID)
		if err != nil {
			return err
		}

		downloadedBytes, err := queryPTRDownloadedUpdateBytes(ctx, tx, serviceID)
		if err != nil {
			return err
		}

		if err := updatePTRSyncDownloadedUpdateCount(ctx, tx, runToken, downloadedCount, downloadedBytes); err != nil {
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

// FinalizePTRDownloadedUpdatesBatch marks multiple successfully downloaded PTR
// updates as locally registered while keeping the sync lease active.
func (b *Bundle) FinalizePTRDownloadedUpdatesBatch(
	ctx context.Context,
	cfg coreptrsync.Config,
	runToken string,
	items []PTRDownloadedUpdateBatchItem,
) (coreptrsync.Status, error) {
	if !cfg.Enabled {
		return coreptrsync.Status{}, coreptrsync.ErrSyncDisabled
	}

	if len(items) == 0 {
		return b.GetPTRSyncStatus(ctx, cfg)
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

		_, repositoryUnregisteredTableName, _ := generatePTRRepositoryTableNames(serviceID)
		_, _, repositoryProcessedTableName := generatePTRRepositoryTableNames(serviceID)
		for _, item := range items {
			hashHex := strings.ToLower(strings.TrimSpace(item.HashHex))
			if hashHex == "" {
				return fmt.Errorf("PTR downloaded update hash is required")
			}
			if len(item.Body) == 0 {
				return fmt.Errorf("PTR downloaded update body is required for %s", hashHex)
			}

			if strings.TrimSpace(item.PreparedImport.HashHex) != "" {
				if !strings.EqualFold(item.PreparedImport.HashHex, hashHex) {
					return fmt.Errorf(
						"prepared PTR import hash %q did not match batch hash %q",
						item.PreparedImport.HashHex,
						hashHex,
					)
				}
			}

			contentType, ok := ptrContentTypeForUpdateMime(int64(item.PreparedImport.Mime))
			if !ok {
				return fmt.Errorf("PTR downloaded update %s had unsupported mime %d", hashHex, item.PreparedImport.Mime)
			}

			sizeBytes := int64(len(item.Body))
			if item.PreparedImport.Size > 0 && item.PreparedImport.Size != sizeBytes {
				return fmt.Errorf(
					"PTR downloaded update %s had size %d but body length %d",
					hashHex,
					item.PreparedImport.Size,
					sizeBytes,
				)
			}

			hashID, err := lookupRequiredHashIDByHex(ctx, tx, hashHex)
			if err != nil {
				return err
			}

			if _, err := tx.ExecContext(
				ctx,
				fmt.Sprintf(
					`INSERT INTO %s (hash_id, content_type, processed, mime, size_bytes, body)
					VALUES (?, ?, 0, ?, ?, ?)
					ON CONFLICT(hash_id, content_type) DO UPDATE SET
						mime = excluded.mime,
						size_bytes = excluded.size_bytes,
						body = excluded.body`,
					repositoryProcessedTableName,
				),
				hashID,
				contentType,
				item.PreparedImport.Mime,
				sizeBytes,
				item.Body,
			); err != nil {
				return fmt.Errorf("upsert PTR repository processed row: %w", err)
			}

			if _, err := tx.ExecContext(
				ctx,
				fmt.Sprintf("DELETE FROM %s WHERE hash_id = ?", repositoryUnregisteredTableName),
				hashID,
			); err != nil {
				return fmt.Errorf("delete PTR repository unregistered update row: %w", err)
			}
		}

		downloadedCount, err := queryPTRDownloadedUpdateCount(ctx, tx, serviceID)
		if err != nil {
			return err
		}

		downloadedBytes, err := queryPTRDownloadedUpdateBytes(ctx, tx, serviceID)
		if err != nil {
			return err
		}

		if err := updatePTRSyncDownloadedUpdateCount(ctx, tx, runToken, downloadedCount, downloadedBytes); err != nil {
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

func finalizePTRPreparedImportTx(
	ctx context.Context,
	tx *ImmediateTx,
	prepared PreparedLocalImport,
) (PreparedLocalImportResult, error) {
	normalized, err := normalizePreparedLocalImport(prepared)
	if err != nil {
		return PreparedLocalImportResult{}, err
	}

	plan, err := resolvePreparedLocalImportPlanTx(ctx, tx, normalized.localFileServiceKey)
	if err != nil {
		return PreparedLocalImportResult{}, err
	}

	hashID, hashExists, err := lookupHashIDByHash(ctx, tx, normalized.hashBytes)
	if err != nil {
		return PreparedLocalImportResult{}, err
	}

	if hashExists {
		record, ok, err := lookupBasicRecordByHashID(ctx, tx, hashID)
		if err != nil {
			return PreparedLocalImportResult{}, err
		}

		if ok {
			if !basicRecordMatchesPreparedImport(record, normalized) {
				return PreparedLocalImportResult{}, fmt.Errorf(
					"prepared import conflicts with existing file_id %d",
					hashID,
				)
			}

			if err := ensurePreparedAuxiliaryMetadata(ctx, tx, hashID, normalized, plan); err != nil {
				return PreparedLocalImportResult{}, err
			}

			if err := ensurePreparedCurrentMemberships(ctx, tx, hashID, plan, normalized.importedAtMS); err != nil {
				return PreparedLocalImportResult{}, err
			}

			return PreparedLocalImportResult{FileID: hashID, AlreadyImported: true}, nil
		}
	}

	return recordPreparedLocalImportTx(ctx, tx, prepared)
}

// SetPTRSyncThrottled ends an active PTR sync lease in a retrying state after
// the remote requested a temporary backoff.
func (b *Bundle) SetPTRSyncThrottled(
	ctx context.Context,
	cfg coreptrsync.Config,
	runToken string,
	retryAtMS int64,
	retryAttempt int64,
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

		if err := setPTRSyncThrottled(ctx, tx, serviceID, runToken, retryAtMS, retryAttempt); err != nil {
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

	if err := ensurePTRRepositoryDefinitionTables(ctx, tx, serviceID); err != nil {
		return services.Service{}, 0, err
	}

	if _, _, err := ensurePTRRepositoryUpdatesService(ctx, tx); err != nil {
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
		fmt.Sprintf(
			`CREATE INDEX IF NOT EXISTS external_mappings.current_mappings_%d_hash_id_idx ON current_mappings_%d (hash_id)`,
			serviceID,
			serviceID,
		),
		fmt.Sprintf(
			`CREATE INDEX IF NOT EXISTS external_mappings.deleted_mappings_%d_hash_id_idx ON deleted_mappings_%d (hash_id)`,
			serviceID,
			serviceID,
		),
		fmt.Sprintf(
			`CREATE INDEX IF NOT EXISTS external_mappings.pending_mappings_%d_hash_id_idx ON pending_mappings_%d (hash_id)`,
			serviceID,
			serviceID,
		),
		fmt.Sprintf(
			`CREATE INDEX IF NOT EXISTS external_mappings.petitioned_mappings_%d_hash_id_idx ON petitioned_mappings_%d (hash_id)`,
			serviceID,
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

func ensurePTRRepositoryDefinitionTables(
	ctx context.Context,
	tx *ImmediateTx,
	serviceID int64,
) error {
	hashIDMapTableName, tagIDMapTableName := generatePTRRepositoryDefinitionTableNames(serviceID)

	statements := []string{
		fmt.Sprintf(
			`CREATE TABLE IF NOT EXISTS %s (
				service_hash_id INTEGER PRIMARY KEY,
				hash_id INTEGER
			)`,
			hashIDMapTableName,
		),
		fmt.Sprintf(
			`CREATE TABLE IF NOT EXISTS %s (
				service_tag_id INTEGER PRIMARY KEY,
				tag_id INTEGER
			)`,
			tagIDMapTableName,
		),
	}

	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("ensure PTR repository definition table: %w", err)
		}
	}

	return nil
}

func ensurePTRRepositoryUpdatesService(
	ctx context.Context,
	tx *ImmediateTx,
) (services.Service, int64, error) {
	serviceKeyHex := hex.EncodeToString([]byte(ptrRepositoryUpdatesServiceName))
	service, ok, err := lookupServiceByKeyTx(ctx, tx, serviceKeyHex)
	if err != nil {
		return services.Service{}, 0, err
	}

	if ok {
		if service.Type != services.TypeLocalFileUpdateDomain {
			return services.Service{}, 0, fmt.Errorf(
				"existing repository updates service key belongs to service type %d, want %d",
				service.Type,
				services.TypeLocalFileUpdateDomain,
			)
		}

		serviceID, err := lookupServiceIDByKeyTx(ctx, tx, service.ServiceKey)
		if err != nil {
			return services.Service{}, 0, err
		}

		if err := ensureCurrentFilesTable(ctx, tx, serviceID); err != nil {
			return services.Service{}, 0, err
		}

		return service, serviceID, nil
	}

	result, err := tx.ExecContext(
		ctx,
		`INSERT INTO main.services (service_key, service_type, name, dictionary_string)
		VALUES (?, ?, ?, ?)`,
		[]byte(ptrRepositoryUpdatesServiceName),
		int(services.TypeLocalFileUpdateDomain),
		ptrRepositoryUpdatesServiceName,
		"{}",
	)
	if err != nil {
		return services.Service{}, 0, fmt.Errorf("insert repository updates service: %w", err)
	}

	serviceID, err := result.LastInsertId()
	if err != nil {
		return services.Service{}, 0, fmt.Errorf("read repository updates service id: %w", err)
	}

	if err := ensureCurrentFilesTable(ctx, tx, serviceID); err != nil {
		return services.Service{}, 0, err
	}

	return services.Service{
		Name:       ptrRepositoryUpdatesServiceName,
		ServiceKey: serviceKeyHex,
		Type:       services.TypeLocalFileUpdateDomain,
		TypePretty: services.TypePretty(services.TypeLocalFileUpdateDomain),
	}, serviceID, nil
}

func ensureCurrentFilesTable(ctx context.Context, tx *ImmediateTx, serviceID int64) error {
	if _, err := tx.ExecContext(
		ctx,
		fmt.Sprintf(
			`CREATE TABLE IF NOT EXISTS main.current_files_%d (
				hash_id INTEGER PRIMARY KEY,
				timestamp_ms INTEGER
			)`,
			serviceID,
		),
	); err != nil {
		return fmt.Errorf("ensure current_files_%d table: %w", serviceID, err)
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
				mime INTEGER NOT NULL DEFAULT 0,
				size_bytes INTEGER NOT NULL DEFAULT 0,
				body BLOB,
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

	if err := ensurePTRProcessedUpdateTableColumns(ctx, tx, repositoryProcessedTableName); err != nil {
		return err
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
			downloaded_update_bytes INTEGER NOT NULL DEFAULT 0,
			current_run_downloaded_bytes INTEGER NOT NULL DEFAULT 0,
			current_run_download_ms INTEGER NOT NULL DEFAULT 0,
			processed_definition_count INTEGER NOT NULL,
			processed_content_count INTEGER NOT NULL,
			last_sync_mapping_count INTEGER NOT NULL DEFAULT -1,
			retry_at_ms INTEGER NOT NULL DEFAULT 0,
			retry_attempt INTEGER NOT NULL DEFAULT 0,
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

	if _, err := tx.ExecContext(
		ctx,
		`ALTER TABLE main.ptr_sync_state ADD COLUMN retry_at_ms INTEGER NOT NULL DEFAULT 0`,
	); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column name: retry_at_ms") {
		return fmt.Errorf("ensure ptr_sync_state.retry_at_ms column: %w", err)
	}

	if _, err := tx.ExecContext(
		ctx,
		`ALTER TABLE main.ptr_sync_state ADD COLUMN retry_attempt INTEGER NOT NULL DEFAULT 0`,
	); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column name: retry_attempt") {
		return fmt.Errorf("ensure ptr_sync_state.retry_attempt column: %w", err)
	}

	if _, err := tx.ExecContext(
		ctx,
		`ALTER TABLE main.ptr_sync_state ADD COLUMN last_sync_mapping_count INTEGER NOT NULL DEFAULT -1`,
	); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column name: last_sync_mapping_count") {
		return fmt.Errorf("ensure ptr_sync_state.last_sync_mapping_count column: %w", err)
	}

	if _, err := tx.ExecContext(
		ctx,
		`ALTER TABLE main.ptr_sync_state ADD COLUMN downloaded_update_bytes INTEGER NOT NULL DEFAULT 0`,
	); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column name: downloaded_update_bytes") {
		return fmt.Errorf("ensure ptr_sync_state.downloaded_update_bytes column: %w", err)
	}

	if _, err := tx.ExecContext(
		ctx,
		`ALTER TABLE main.ptr_sync_state ADD COLUMN current_run_downloaded_bytes INTEGER NOT NULL DEFAULT 0`,
	); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column name: current_run_downloaded_bytes") {
		return fmt.Errorf("ensure ptr_sync_state.current_run_downloaded_bytes column: %w", err)
	}

	if _, err := tx.ExecContext(
		ctx,
		`ALTER TABLE main.ptr_sync_state ADD COLUMN current_run_download_ms INTEGER NOT NULL DEFAULT 0`,
	); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column name: current_run_download_ms") {
		return fmt.Errorf("ensure ptr_sync_state.current_run_download_ms column: %w", err)
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
		`SELECT service_id, account_mode, phase, is_running, run_token, retry_at_ms, retry_attempt, last_sync_mapping_count, last_error
		FROM main.ptr_sync_state
		WHERE singleton = ?`,
		ptrSyncStateSingleton,
	)

	var (
		existingServiceID            int64
		existingAccountMode          string
		existingPhase                string
		existingIsRunning            int64
		existingRunToken             sql.NullString
		existingRetryAtMS            int64
		existingRetryAttempt         int64
		existingLastSyncMappingCount int64
		existingLastError            sql.NullString
	)

	err := row.Scan(
		&existingServiceID,
		&existingAccountMode,
		&existingPhase,
		&existingIsRunning,
		&existingRunToken,
		&existingRetryAtMS,
		&existingRetryAttempt,
		&existingLastSyncMappingCount,
		&existingLastError,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			seedDownloadedCount, seedErr := queryPTRDownloadedUpdateCount(ctx, tx, serviceID)
			if seedErr != nil {
				return fmt.Errorf("seed ptr_sync_state downloaded count: %w", seedErr)
			}

			seedDownloadedBytes, seedErr := queryPTRDownloadedUpdateBytes(ctx, tx, serviceID)
			if seedErr != nil {
				return fmt.Errorf("seed ptr_sync_state downloaded bytes: %w", seedErr)
			}

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
					downloaded_update_bytes,
					current_run_downloaded_bytes,
					current_run_download_ms,
					processed_definition_count,
					processed_content_count,
					retry_at_ms,
					retry_attempt,
					last_sync_mapping_count,
					last_error,
					updated_at_ms
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				ptrSyncStateSingleton,
				serviceID,
				coreptrsync.AccountModeSharedReadOnly,
				coreptrsync.PhaseIdle,
				0,
				nil,
				0,
				seedDownloadedCount,
				seedDownloadedBytes,
				0,
				0,
				0,
				0,
				0,
				0,
				-1,
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

	_ = existingLastSyncMappingCount

	if existingServiceID == serviceID &&
		existingAccountMode == coreptrsync.AccountModeSharedReadOnly &&
		existingPhase == coreptrsync.PhaseThrottling &&
		existingIsRunning == 0 &&
		!existingRunToken.Valid {
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
			retry_at_ms = ?,
			retry_attempt = ?,
			last_error = ?,
			updated_at_ms = ?
		WHERE singleton = ?`,
		serviceID,
		coreptrsync.AccountModeSharedReadOnly,
		coreptrsync.PhaseIdle,
		0,
		nil,
		0,
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
	serviceID, err := lookupServiceIDByKeyTx(ctx, q, service.ServiceKey)
	if err != nil {
		return coreptrsync.Status{}, err
	}

	row := q.QueryRowContext(
		ctx,
		`SELECT
			account_mode,
			phase,
			is_running,
			metadata_slice,
			downloaded_update_count,
			downloaded_update_bytes,
			current_run_downloaded_bytes,
			current_run_download_ms,
			processed_definition_count,
			processed_content_count,
			last_sync_mapping_count,
			retry_at_ms,
			retry_attempt,
			last_error,
			updated_at_ms
		FROM main.ptr_sync_state
		WHERE singleton = ?`,
		ptrSyncStateSingleton,
	)

	var (
		accountMode               string
		phase                     string
		isRunning                 int64
		metadataSlice             int64
		downloadedUpdateCount     int64
		downloadedUpdateBytes     int64
		currentRunDownloadedBytes int64
		currentRunDownloadMS      int64
		processedDefinitionCount  int64
		processedContentCount     int64
		lastSyncMappingCount      int64
		retryAtMS                 int64
		retryAttempt              int64
		lastError                 sql.NullString
		updatedAtMS               int64
	)

	if err := row.Scan(
		&accountMode,
		&phase,
		&isRunning,
		&metadataSlice,
		&downloadedUpdateCount,
		&downloadedUpdateBytes,
		&currentRunDownloadedBytes,
		&currentRunDownloadMS,
		&processedDefinitionCount,
		&processedContentCount,
		&lastSyncMappingCount,
		&retryAtMS,
		&retryAttempt,
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

	pendingDownloadCount, err := queryPTRPendingDownloadCount(ctx, q, serviceID)
	if err != nil {
		return coreptrsync.Status{}, err
	}

	pendingProcessCount, err := queryPTRPendingProcessCount(ctx, q, serviceID)
	if err != nil {
		return coreptrsync.Status{}, err
	}

	nextUpdateDue, err := queryPTRNextUpdateDueByServiceID(ctx, q, serviceID)
	if err != nil {
		return coreptrsync.Status{}, err
	}

	hasObservedSyncProgress := metadataSlice > 0 || downloadedUpdateCount > 0 || processedDefinitionCount > 0 || processedContentCount > 0
	isComplete :=
		phase == coreptrsync.PhaseIdle &&
			isRunning == 0 &&
			!lastError.Valid &&
			pendingDownloadCount == 0 &&
			pendingProcessCount == 0 &&
			hasObservedSyncProgress
	isUpToDate :=
		isComplete &&
			nextUpdateDue > 0 &&
			time.Now().UTC().Unix() < nextUpdateDue

	status := coreptrsync.Status{
		Enabled:                   cfg.Enabled,
		Configured:                true,
		ServiceName:               service.Name,
		ServiceKey:                service.ServiceKey,
		Host:                      cfg.Host,
		Port:                      cfg.Port,
		AccountMode:               accountMode,
		Phase:                     phase,
		IsRunning:                 isRunning != 0,
		IsComplete:                isComplete,
		IsUpToDate:                isUpToDate,
		MetadataSlice:             metadataSlice,
		DownloadedUpdateCount:     downloadedUpdateCount,
		DownloadedUpdateBytes:     downloadedUpdateBytes,
		CurrentRunDownloadedBytes: currentRunDownloadedBytes,
		CurrentRunDownloadMS:      currentRunDownloadMS,
		ProcessedDefinitionCount:  processedDefinitionCount,
		ProcessedContentCount:     processedContentCount,
		PendingDownloadCount:      pendingDownloadCount,
		PendingProcessCount:       pendingProcessCount,
		NextUpdateDue:             nextUpdateDue,
		RetryAtMS:                 retryAtMS,
		RetryAttempt:              retryAttempt,
		UpdatedAtMS:               updatedAtMS,
	}
	if lastSyncMappingCount >= 0 {
		value := lastSyncMappingCount
		status.LastSyncMappingCount = &value
	}
	if currentRunDownloadedBytes > 0 && currentRunDownloadMS > 0 {
		status.CurrentRunBytesPerSecond = (currentRunDownloadedBytes * 1000) / currentRunDownloadMS
	}
	if lastError.Valid {
		status.LastError = lastError.String
	}

	return status, nil
}

func updatePTRSyncDownloadedUpdateCount(
	ctx context.Context,
	tx *ImmediateTx,
	runToken string,
	downloadedCount int64,
	downloadedBytes int64,
) error {
	nowMS := time.Now().UTC().UnixMilli()
	result, err := tx.ExecContext(
		ctx,
		`UPDATE main.ptr_sync_state
		SET downloaded_update_count = ?,
			downloaded_update_bytes = ?,
			updated_at_ms = ?
		WHERE singleton = ? AND is_running = 1 AND run_token = ?`,
		downloadedCount,
		downloadedBytes,
		nowMS,
		ptrSyncStateSingleton,
		runToken,
	)
	if err != nil {
		return fmt.Errorf("update ptr_sync_state downloaded update count: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read ptr_sync_state downloaded update count rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return errPTRSyncRunNotActive
	}

	return nil
}

// UpdatePTRSyncDownloadMetrics persists exact current-run download metrics while
// keeping the active sync lease alive.
func (b *Bundle) UpdatePTRSyncDownloadMetrics(
	ctx context.Context,
	cfg coreptrsync.Config,
	runToken string,
	currentRunDownloadedBytes int64,
	currentRunDownloadMS int64,
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

		if err := ensurePTRSyncRunActive(ctx, tx, serviceID, runToken); err != nil {
			return err
		}

		downloadedCount, err := queryPTRDownloadedUpdateCount(ctx, tx, serviceID)
		if err != nil {
			return err
		}

		downloadedBytes, err := queryPTRDownloadedUpdateBytes(ctx, tx, serviceID)
		if err != nil {
			return err
		}

		nowMS := time.Now().UTC().UnixMilli()
		result, err := tx.ExecContext(
			ctx,
			`UPDATE main.ptr_sync_state
			SET downloaded_update_count = ?,
				downloaded_update_bytes = ?,
				current_run_downloaded_bytes = ?,
				current_run_download_ms = ?,
				updated_at_ms = ?
			WHERE singleton = ? AND is_running = 1 AND run_token = ?`,
			downloadedCount,
			downloadedBytes,
			currentRunDownloadedBytes,
			currentRunDownloadMS,
			nowMS,
			ptrSyncStateSingleton,
			runToken,
		)
		if err != nil {
			return fmt.Errorf("update ptr_sync_state download metrics: %w", err)
		}

		rowsAffected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("read ptr_sync_state download metrics rows affected: %w", err)
		}

		if rowsAffected == 0 {
			return errPTRSyncRunNotActive
		}

		status, err = lookupPTRSyncStatus(ctx, tx, cfg, service)
		return err
	})
	if err != nil {
		return coreptrsync.Status{}, err
	}

	return status, nil
}

// GetPTRNextUpdateDue returns the persisted remote next-update timestamp in unix
// seconds for the daemon PTR service.
func (b *Bundle) GetPTRNextUpdateDue(
	ctx context.Context,
	cfg coreptrsync.Config,
) (int64, error) {
	if !cfg.Enabled {
		return 0, nil
	}

	serviceID, err := lookupServiceIDByKeyTx(ctx, b.conn, coreptrsync.DaemonServiceKeyHex())
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}

		return 0, err
	}

	row := b.conn.QueryRowContext(
		ctx,
		`SELECT next_update_due FROM main.ptr_sync_remote_state WHERE service_id = ?`,
		serviceID,
	)

	var nextUpdateDue int64
	if err := row.Scan(&nextUpdateDue); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}

		return 0, fmt.Errorf("query PTR next update due: %w", err)
	}

	return nextUpdateDue, nil
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

func generatePTRRepositoryDefinitionTableNames(serviceID int64) (string, string) {
	return fmt.Sprintf("external_master.%s%d", ptrRepositoryHashIDMapTablePrefix, serviceID),
		fmt.Sprintf("external_master.%s%d", ptrRepositoryTagIDMapTablePrefix, serviceID)
}

func generatePTRRepositoryTableNames(serviceID int64) (string, string, string) {
	return fmt.Sprintf("%s%d", ptrRepositoryUpdatesTablePrefix, serviceID),
		fmt.Sprintf("%s%d", ptrRepositoryUnregisteredTablePrefix, serviceID),
		fmt.Sprintf("%s%d", ptrRepositoryProcessedTablePrefix, serviceID)
}

// ListPTRProcessableUpdates reconciles already-local repository update blobs
// into the processed table and returns the currently processable work in update
// order with definitions before mappings.
func (b *Bundle) ListPTRProcessableUpdates(
	ctx context.Context,
	cfg coreptrsync.Config,
	runToken string,
) ([]PTRProcessableUpdate, error) {
	if !cfg.Enabled {
		return nil, coreptrsync.ErrSyncDisabled
	}

	processable := []PTRProcessableUpdate{}
	err := b.WithImmediateTx(ctx, func(tx *ImmediateTx) error {
		_, serviceID, err := lookupPTRServiceTx(ctx, tx)
		if err != nil {
			return err
		}

		if err := ensurePTRSyncRunActive(ctx, tx, serviceID, runToken); err != nil {
			return err
		}

		if err := ensurePTRRepositoryDefinitionTables(ctx, tx, serviceID); err != nil {
			return err
		}

		processable, err = queryPTRProcessableUpdates(ctx, tx, serviceID)
		return err
	})
	if err != nil {
		return nil, err
	}

	return processable, nil
}

// ApplyPTRDefinitions persists repository-local definitions into repository id
// map tables before marking the source update processed.
func (b *Bundle) ApplyPTRDefinitions(
	ctx context.Context,
	cfg coreptrsync.Config,
	runToken string,
	updateHashHex string,
	update PTRDefinitionsUpdate,
) error {
	if !cfg.Enabled {
		return coreptrsync.ErrSyncDisabled
	}

	return b.WithImmediateTx(ctx, func(tx *ImmediateTx) error {
		_, serviceID, err := lookupPTRServiceTx(ctx, tx)
		if err != nil {
			return err
		}

		if err := ensurePTRSyncRunActive(ctx, tx, serviceID, runToken); err != nil {
			return err
		}

		if err := ensurePTRRepositoryDefinitionTables(ctx, tx, serviceID); err != nil {
			return err
		}

		if err := applyPTRDefinitionsUpdateTx(ctx, tx, serviceID, updateHashHex, update); err != nil {
			return err
		}

		return recomputePTRProcessedCounts(ctx, tx, serviceID, runToken)
	})
}

// ApplyPTRMappings normalizes repository-local ids through repository map
// tables, writes mapping state, and marks the source update processed.
func (b *Bundle) ApplyPTRMappings(
	ctx context.Context,
	cfg coreptrsync.Config,
	runToken string,
	updateHashHex string,
	update PTRMappingsUpdate,
) error {
	if !cfg.Enabled {
		return coreptrsync.ErrSyncDisabled
	}

	return b.WithImmediateTx(ctx, func(tx *ImmediateTx) error {
		_, serviceID, err := lookupPTRServiceTx(ctx, tx)
		if err != nil {
			return err
		}

		if err := ensurePTRSyncRunActive(ctx, tx, serviceID, runToken); err != nil {
			return err
		}

		if err := ensurePTRMappingsTables(ctx, tx, serviceID); err != nil {
			return err
		}

		if err := ensurePTRRepositoryDefinitionTables(ctx, tx, serviceID); err != nil {
			return err
		}

		if err := applyPTRMappingsUpdateTx(ctx, tx, serviceID, updateHashHex, update); err != nil {
			return err
		}

		return recomputePTRProcessedCounts(ctx, tx, serviceID, runToken)
	})
}

// ApplyPTRProcessableUpdatesBatch applies already-decoded PTR updates in update
// order inside one DB transaction.
func (b *Bundle) ApplyPTRProcessableUpdatesBatch(
	ctx context.Context,
	cfg coreptrsync.Config,
	runToken string,
	items []PTRApplyUpdateBatchItem,
) error {
	if !cfg.Enabled {
		return coreptrsync.ErrSyncDisabled
	}

	if len(items) == 0 {
		return nil
	}

	return b.WithImmediateTx(ctx, func(tx *ImmediateTx) error {
		_, serviceID, err := lookupPTRServiceTx(ctx, tx)
		if err != nil {
			return err
		}

		if err := ensurePTRSyncRunActive(ctx, tx, serviceID, runToken); err != nil {
			return err
		}

		hasDefinitions := false
		hasMappings := false
		for _, item := range items {
			switch item.ContentType {
			case PTRContentTypeDefinitions:
				hasDefinitions = true
			case PTRContentTypeMappings:
				hasMappings = true
			default:
				return fmt.Errorf("unsupported PTR update content type %d", item.ContentType)
			}
		}

		if hasDefinitions || hasMappings {
			if err := ensurePTRRepositoryDefinitionTables(ctx, tx, serviceID); err != nil {
				return err
			}
		}

		if hasMappings {
			if err := ensurePTRMappingsTables(ctx, tx, serviceID); err != nil {
				return err
			}
		}

		for _, item := range items {
			hashHex := strings.ToLower(strings.TrimSpace(item.HashHex))
			if hashHex == "" {
				return fmt.Errorf("PTR processable update hash is required")
			}

			switch item.ContentType {
			case PTRContentTypeDefinitions:
				if err := applyPTRDefinitionsUpdateTx(ctx, tx, serviceID, hashHex, item.Definitions); err != nil {
					return fmt.Errorf("apply PTR definitions update %s: %w", hashHex, err)
				}
			case PTRContentTypeMappings:
				if err := applyPTRMappingsUpdateTx(ctx, tx, serviceID, hashHex, item.Mappings); err != nil {
					return fmt.Errorf("apply PTR mappings update %s: %w", hashHex, err)
				}
			}
		}

		return recomputePTRProcessedCounts(ctx, tx, serviceID, runToken)
	})
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
			current_run_downloaded_bytes = ?,
			current_run_download_ms = ?,
			retry_at_ms = ?,
			last_error = ?,
			updated_at_ms = ?
		WHERE singleton = ? AND is_running = 0`,
		serviceID,
		coreptrsync.AccountModeSharedReadOnly,
		coreptrsync.PhaseSyncing,
		1,
		runToken,
		0,
		0,
		0,
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
	lastSyncMappingCount *int64,
) error {
	nowMS := time.Now().UTC().UnixMilli()
	var lastErrorValue any
	if trimmed := strings.TrimSpace(lastError); trimmed != "" {
		lastErrorValue = trimmed
	}

	query := `UPDATE main.ptr_sync_state
		SET service_id = ?,
			account_mode = ?,
			phase = ?,
			is_running = ?,
			run_token = ?,
			metadata_slice = ?,
			retry_at_ms = ?,
			retry_attempt = ?,
			last_error = ?,
			updated_at_ms = ?`
	args := []any{
		serviceID,
		coreptrsync.AccountModeSharedReadOnly,
		coreptrsync.PhaseIdle,
		0,
		nil,
		nextUpdateIndex,
		0,
		0,
		lastErrorValue,
		nowMS,
	}
	if lastSyncMappingCount != nil {
		query += `,
			last_sync_mapping_count = ?`
		args = append(args, *lastSyncMappingCount)
	}
	query += `
		WHERE singleton = ? AND is_running = 1 AND run_token = ?`
	args = append(args, ptrSyncStateSingleton, runToken)

	result, err := tx.ExecContext(ctx, query, args...)
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

func queryPTRCurrentMappingCount(
	ctx context.Context,
	q queryRowContextQuerier,
	serviceID int64,
) (int64, error) {
	row := q.QueryRowContext(
		ctx,
		fmt.Sprintf(`SELECT COUNT(*) FROM external_mappings.current_mappings_%d`, serviceID),
	)

	var count int64
	if err := row.Scan(&count); err != nil {
		return 0, fmt.Errorf("query PTR current mapping count: %w", err)
	}

	return count, nil
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
			retry_at_ms = ?,
			retry_attempt = ?,
			last_error = ?,
			updated_at_ms = ?
		WHERE singleton = ? AND is_running = 1 AND run_token = ?`,
		serviceID,
		coreptrsync.AccountModeSharedReadOnly,
		coreptrsync.PhaseIdle,
		0,
		nil,
		0,
		0,
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

func setPTRSyncThrottled(
	ctx context.Context,
	tx *ImmediateTx,
	serviceID int64,
	runToken string,
	retryAtMS int64,
	retryAttempt int64,
) error {
	if retryAttempt < 1 {
		retryAttempt = 1
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
			retry_at_ms = ?,
			retry_attempt = ?,
			last_error = ?,
			updated_at_ms = ?
		WHERE singleton = ? AND is_running = 1 AND run_token = ?`,
		serviceID,
		coreptrsync.AccountModeSharedReadOnly,
		coreptrsync.PhaseThrottling,
		0,
		nil,
		retryAtMS,
		retryAttempt,
		nil,
		nowMS,
		ptrSyncStateSingleton,
		runToken,
	)
	if err != nil {
		return fmt.Errorf("set ptr_sync_state retrying: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read ptr_sync_state retrying rows affected: %w", err)
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

func resolvePTRUpdateDomainService(definitions []serviceDefinition) (serviceDefinition, error) {
	servicesByType := []serviceDefinition{}
	for _, definition := range definitions {
		if definition.serviceType != services.TypeLocalFileUpdateDomain {
			continue
		}

		servicesByType = append(servicesByType, definition)
	}

	if len(servicesByType) == 0 {
		return serviceDefinition{}, errors.New("required repository updates local file domain is missing")
	}

	if len(servicesByType) > 1 {
		return serviceDefinition{}, errors.New("multiple repository updates local file domains are configured")
	}

	return servicesByType[0], nil
}

func queryPTRDownloadedUpdateCount(
	ctx context.Context,
	q queryRowContextQuerier,
	serviceID int64,
) (int64, error) {
	_, _, repositoryProcessedTableName := generatePTRRepositoryTableNames(serviceID)

	row := q.QueryRowContext(
		ctx,
		fmt.Sprintf("SELECT COUNT(*) FROM %s", repositoryProcessedTableName),
	)

	var count int64
	if err := row.Scan(&count); err != nil {
		return 0, fmt.Errorf("query PTR downloaded update count: %w", err)
	}

	return count, nil
}

func queryPTRDownloadedUpdateBytes(
	ctx context.Context,
	q queryRowContextQuerier,
	serviceID int64,
) (int64, error) {
	_, _, repositoryProcessedTableName := generatePTRRepositoryTableNames(serviceID)

	row := q.QueryRowContext(
		ctx,
		fmt.Sprintf(`SELECT COALESCE(SUM(size_bytes), 0) FROM %s`, repositoryProcessedTableName),
	)

	var totalBytes int64
	if err := row.Scan(&totalBytes); err != nil {
		return 0, fmt.Errorf("query PTR downloaded update bytes: %w", err)
	}

	return totalBytes, nil
}

func queryPTRPendingDownloadCount(
	ctx context.Context,
	q queryRowContextQuerier,
	serviceID int64,
) (int64, error) {
	_, repositoryUnregisteredTableName, _ := generatePTRRepositoryTableNames(serviceID)

	row := q.QueryRowContext(
		ctx,
		fmt.Sprintf("SELECT COUNT(*) FROM %s", repositoryUnregisteredTableName),
	)

	var count int64
	if err := row.Scan(&count); err != nil {
		return 0, fmt.Errorf("query PTR pending download count: %w", err)
	}

	return count, nil
}

func queryPTRPendingProcessCount(
	ctx context.Context,
	q queryRowContextQuerier,
	serviceID int64,
) (int64, error) {
	_, _, repositoryProcessedTableName := generatePTRRepositoryTableNames(serviceID)

	row := q.QueryRowContext(
		ctx,
		fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE processed = ?", repositoryProcessedTableName),
		0,
	)

	var count int64
	if err := row.Scan(&count); err != nil {
		return 0, fmt.Errorf("query PTR pending process count: %w", err)
	}

	return count, nil
}

func queryPTRNextUpdateDueByServiceID(
	ctx context.Context,
	q queryRowContextQuerier,
	serviceID int64,
) (int64, error) {
	row := q.QueryRowContext(
		ctx,
		`SELECT next_update_due FROM main.ptr_sync_remote_state WHERE service_id = ?`,
		serviceID,
	)

	var nextUpdateDue int64
	if err := row.Scan(&nextUpdateDue); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}

		return 0, fmt.Errorf("query PTR next update due: %w", err)
	}

	return nextUpdateDue, nil
}

func reconcilePTRProcessedUpdatesForLocalBlobs(
	ctx context.Context,
	tx *ImmediateTx,
	serviceID int64,
	updateServiceID int64,
) error {
	repositoryUpdatesTableName, repositoryUnregisteredTableName, repositoryProcessedTableName := generatePTRRepositoryTableNames(serviceID)

	rows, err := tx.QueryContext(
		ctx,
		fmt.Sprintf(
			`SELECT ru.hash_id, fi.mime
			FROM %s ru
			JOIN main.current_files_%d cf USING (hash_id)
			JOIN main.files_info fi USING (hash_id)`,
			repositoryUpdatesTableName,
			updateServiceID,
		),
	)
	if err != nil {
		return fmt.Errorf("query locally registered PTR updates: %w", err)
	}
	defer rows.Close()

	correctRows := map[[2]int64]struct{}{}
	localHashIDs := map[int64]struct{}{}
	for rows.Next() {
		var (
			hashID int64
			mime   int64
		)

		if err := rows.Scan(&hashID, &mime); err != nil {
			return fmt.Errorf("scan locally registered PTR update row: %w", err)
		}

		contentType, ok := ptrContentTypeForUpdateMime(mime)
		if !ok {
			continue
		}

		correctRows[[2]int64{hashID, contentType}] = struct{}{}
		localHashIDs[hashID] = struct{}{}
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate locally registered PTR update rows: %w", err)
	}

	currentRows, err := queryPTRProcessedRows(ctx, tx, repositoryProcessedTableName)
	if err != nil {
		return err
	}

	for _, row := range currentRows {
		key := [2]int64{row.hashID, row.contentType}
		if _, ok := localHashIDs[row.hashID]; !ok {
			continue
		}

		if _, ok := correctRows[key]; ok {
			delete(correctRows, key)
			continue
		}

		if _, err := tx.ExecContext(
			ctx,
			fmt.Sprintf("DELETE FROM %s WHERE hash_id = ? AND content_type = ?", repositoryProcessedTableName),
			row.hashID,
			row.contentType,
		); err != nil {
			return fmt.Errorf("delete stale PTR processed row: %w", err)
		}
	}

	for key := range correctRows {
		if _, err := tx.ExecContext(
			ctx,
			fmt.Sprintf(
				"INSERT OR IGNORE INTO %s (hash_id, content_type, processed) VALUES (?, ?, ?)",
				repositoryProcessedTableName,
			),
			key[0],
			key[1],
			0,
		); err != nil {
			return fmt.Errorf("insert PTR processed row: %w", err)
		}
	}

	if len(localHashIDs) > 0 {
		for hashID := range localHashIDs {
			if _, err := tx.ExecContext(
				ctx,
				fmt.Sprintf("DELETE FROM %s WHERE hash_id = ?", repositoryUnregisteredTableName),
				hashID,
			); err != nil {
				return fmt.Errorf("delete reconciled PTR unregistered row: %w", err)
			}
		}
	}

	return nil
}

func queryPTRProcessedRows(
	ctx context.Context,
	tx *ImmediateTx,
	processedTableName string,
) ([]processedRow, error) {
	rows, err := tx.QueryContext(
		ctx,
		fmt.Sprintf("SELECT hash_id, content_type, processed FROM %s", processedTableName),
	)
	if err != nil {
		return nil, fmt.Errorf("query PTR processed rows: %w", err)
	}
	defer rows.Close()

	processedRows := []processedRow{}
	for rows.Next() {
		var (
			row       processedRow
			processed int64
		)

		if err := rows.Scan(&row.hashID, &row.contentType, &processed); err != nil {
			return nil, fmt.Errorf("scan PTR processed row: %w", err)
		}

		row.processed = processed != 0
		processedRows = append(processedRows, row)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate PTR processed rows: %w", err)
	}

	return processedRows, nil
}

func queryPTRProcessableUpdates(
	ctx context.Context,
	tx *ImmediateTx,
	serviceID int64,
) ([]PTRProcessableUpdate, error) {
	repositoryUpdatesTableName, repositoryUnregisteredTableName, repositoryProcessedTableName :=
		generatePTRRepositoryTableNames(serviceID)

	var minBlockedUpdateIndex sql.NullInt64
	if err := tx.QueryRowContext(
		ctx,
		fmt.Sprintf(
			`SELECT MIN(update_index)
			FROM %s ru
			JOIN %s pending USING (hash_id)`,
			repositoryUpdatesTableName,
			repositoryUnregisteredTableName,
		),
	).Scan(&minBlockedUpdateIndex); err != nil {
		return nil, fmt.Errorf("query PTR earliest blocked update index: %w", err)
	}

	predicate := "rp.processed = ? AND rp.content_type IN (?, ?)"
	args := []any{0, PTRContentTypeDefinitions, PTRContentTypeMappings}
	if minBlockedUpdateIndex.Valid {
		predicate += " AND ru.update_index < ?"
		args = append(args, minBlockedUpdateIndex.Int64)
	}

	rows, err := tx.QueryContext(
		ctx,
		fmt.Sprintf(
			`SELECT ru.update_index, rp.hash_id, lower(hex(h.hash)), rp.content_type, rp.body, rp.processed
			FROM %s rp
			JOIN %s ru USING (hash_id)
			JOIN external_master.hashes h USING (hash_id)
			WHERE %s
			ORDER BY ru.update_index ASC,
				CASE WHEN rp.content_type = %d THEN 0 ELSE 1 END ASC,
				rp.hash_id ASC`,
			repositoryProcessedTableName,
			repositoryUpdatesTableName,
			predicate,
			PTRContentTypeDefinitions,
		),
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("query PTR processable updates: %w", err)
	}
	defer rows.Close()

	processable := []PTRProcessableUpdate{}
	for rows.Next() {
		var (
			item      PTRProcessableUpdate
			processed int64
		)

		if err := rows.Scan(
			&item.UpdateIndex,
			&item.HashID,
			&item.HashHex,
			&item.ContentType,
			&item.Body,
			&processed,
		); err != nil {
			return nil, fmt.Errorf("scan PTR processable update row: %w", err)
		}

		item.Processed = processed != 0
		processable = append(processable, item)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate PTR processable update rows: %w", err)
	}

	return processable, nil
}

func (b *Bundle) LoadPTRStoredUpdateBody(
	ctx context.Context,
	hashHex string,
) ([]byte, int, bool, error) {
	if b == nil {
		return nil, 0, false, fmt.Errorf("hydrus bundle is nil")
	}

	_, hashBytes, err := normalizePreparedHash(hashHex)
	if err != nil {
		return nil, 0, false, fmt.Errorf("normalize PTR update hash %q: %w", hashHex, err)
	}

	serviceID, err := lookupServiceIDByKeyTx(ctx, b.conn, coreptrsync.DaemonServiceKeyHex())
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, 0, false, nil
		}

		return nil, 0, false, err
	}

	_, _, repositoryProcessedTableName := generatePTRRepositoryTableNames(serviceID)
	row := b.conn.QueryRowContext(
		ctx,
		fmt.Sprintf(`SELECT mime, body FROM %s rp JOIN external_master.hashes h USING (hash_id) WHERE h.hash = ? AND body IS NOT NULL LIMIT 1`, repositoryProcessedTableName),
		hashBytes,
	)

	var (
		mime int
		body []byte
	)
	if err := row.Scan(&mime, &body); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, 0, false, nil
		}

		return nil, 0, false, fmt.Errorf("query stored PTR update body: %w", err)
	}

	return body, mime, true, nil
}

func ptrContentTypeForUpdateMime(mime int64) (int64, bool) {
	switch mime {
	case ptrHydrusUpdateDefinitionsMime:
		return PTRContentTypeDefinitions, true
	case ptrHydrusUpdateContentMime:
		return PTRContentTypeMappings, true
	default:
		return 0, false
	}
}

func lookupRequiredHashIDByHex(ctx context.Context, tx *ImmediateTx, hashHex string) (int64, error) {
	_, hashBytes, err := normalizePreparedHash(hashHex)
	if err != nil {
		return 0, fmt.Errorf("normalize PTR update hash %q: %w", hashHex, err)
	}

	hashID, ok, err := lookupHashIDByHash(ctx, tx, hashBytes)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, fmt.Errorf("PTR update hash %s is not registered locally", hashHex)
	}

	return hashID, nil
}

func normalizePTRDefinitionHashHex(hashHex string) (string, []byte, error) {
	normalized := strings.ToLower(strings.TrimSpace(hashHex))
	if normalized == "" {
		return "", nil, fmt.Errorf("PTR definition hash must not be empty")
	}
	if len(normalized)%2 != 0 {
		return "", nil, fmt.Errorf("PTR definition hash must have an even number of hex characters")
	}

	hashBytes, err := hex.DecodeString(normalized)
	if err != nil {
		return "", nil, fmt.Errorf("decode PTR definition hash: %w", err)
	}
	if len(hashBytes) == 0 {
		return "", nil, fmt.Errorf("PTR definition hash must decode to at least one byte")
	}

	return normalized, hashBytes, nil
}

func ensurePTRDefinitionHashIDsTx(
	ctx context.Context,
	tx *ImmediateTx,
	hashes []string,
) (map[string]int64, error) {
	if len(hashes) == 0 {
		return map[string]int64{}, nil
	}

	normalizedHashes := make([]string, 0, len(hashes))
	hashBytesByHash := make(map[string][]byte, len(hashes))
	seen := map[string]struct{}{}
	for _, hash := range hashes {
		normalized, hashBytes, err := normalizePTRDefinitionHashHex(hash)
		if err != nil {
			return nil, fmt.Errorf("normalize PTR definition hash %q: %w", hash, err)
		}

		if _, ok := seen[normalized]; ok {
			continue
		}

		seen[normalized] = struct{}{}
		normalizedHashes = append(normalizedHashes, normalized)
		hashBytesByHash[normalized] = hashBytes
	}

	resolved := make(map[string]int64, len(normalizedHashes))
	for _, hash := range normalizedHashes {
		hashID, exists, err := lookupHashIDByHash(ctx, tx, hashBytesByHash[hash])
		if err != nil {
			return nil, err
		}

		if !exists {
			if _, err := tx.ExecContext(
				ctx,
				`INSERT OR IGNORE INTO external_master.hashes (hash) VALUES (?)`,
				hashBytesByHash[hash],
			); err != nil {
				return nil, fmt.Errorf("insert external_master.hashes row: %w", err)
			}

			hashID, exists, err = lookupHashIDByHash(ctx, tx, hashBytesByHash[hash])
			if err != nil {
				return nil, err
			}
			if !exists {
				return nil, errors.New("inserted PTR definition hash row was not readable inside transaction")
			}
		}

		resolved[hash] = hashID
	}

	return resolved, nil
}

func ensurePTRProcessedUpdateTableColumns(
	ctx context.Context,
	tx *ImmediateTx,
	tableName string,
) error {
	columns, err := lookupSQLiteTableColumns(ctx, tx, tableName)
	if err != nil {
		return err
	}

	type columnSpec struct {
		name       string
		definition string
	}

	required := []columnSpec{
		{name: "mime", definition: "INTEGER NOT NULL DEFAULT 0"},
		{name: "size_bytes", definition: "INTEGER NOT NULL DEFAULT 0"},
		{name: "body", definition: "BLOB"},
	}
	for _, column := range required {
		if _, ok := columns[column.name]; ok {
			continue
		}

		if _, err := tx.ExecContext(
			ctx,
			fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", tableName, column.name, column.definition),
		); err != nil {
			return fmt.Errorf("add PTR processed column %s: %w", column.name, err)
		}
	}

	return nil
}

func lookupSQLiteTableColumns(
	ctx context.Context,
	q queryRowContextQuerier,
	tableName string,
) (map[string]struct{}, error) {
	rows, err := q.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", tableName))
	if err != nil {
		return nil, fmt.Errorf("query sqlite table columns for %s: %w", tableName, err)
	}
	defer rows.Close()

	columns := map[string]struct{}{}
	for rows.Next() {
		var (
			cid        int64
			name       string
			columnType string
			notNull    int64
			defaultVal any
			pk         int64
		)

		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultVal, &pk); err != nil {
			return nil, fmt.Errorf("scan sqlite table column for %s: %w", tableName, err)
		}

		columns[strings.ToLower(strings.TrimSpace(name))] = struct{}{}
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sqlite table columns for %s: %w", tableName, err)
	}

	return columns, nil
}

func applyPTRDefinitionsUpdateTx(
	ctx context.Context,
	tx *ImmediateTx,
	serviceID int64,
	updateHashHex string,
	update PTRDefinitionsUpdate,
) error {
	updateHashID, err := lookupRequiredHashIDByHex(ctx, tx, updateHashHex)
	if err != nil {
		return err
	}

	if err := applyPTRDefinitionsTx(ctx, tx, serviceID, update); err != nil {
		return err
	}

	if err := markPTRUpdateProcessed(ctx, tx, serviceID, updateHashID, PTRContentTypeDefinitions); err != nil {
		return err
	}

	return nil
}

func applyPTRMappingsUpdateTx(
	ctx context.Context,
	tx *ImmediateTx,
	serviceID int64,
	updateHashHex string,
	update PTRMappingsUpdate,
) error {
	updateHashID, err := lookupRequiredHashIDByHex(ctx, tx, updateHashHex)
	if err != nil {
		return err
	}

	if err := applyPTRMappingsTx(ctx, tx, serviceID, update); err != nil {
		return err
	}

	if err := markPTRUpdateProcessed(ctx, tx, serviceID, updateHashID, PTRContentTypeMappings); err != nil {
		return err
	}

	return nil
}

func applyPTRDefinitionsTx(
	ctx context.Context,
	tx *ImmediateTx,
	serviceID int64,
	update PTRDefinitionsUpdate,
) error {
	hashIDMapTableName, tagIDMapTableName := generatePTRRepositoryDefinitionTableNames(serviceID)

	if len(update.ServiceHashIDsToHashes) > 0 {
		hashHexes := make([]string, 0, len(update.ServiceHashIDsToHashes))
		for _, hashHex := range update.ServiceHashIDsToHashes {
			hashHexes = append(hashHexes, hashHex)
		}

		hashIDsByHash, err := ensurePTRDefinitionHashIDsTx(ctx, tx, hashHexes)
		if err != nil {
			return fmt.Errorf("ensure PTR definition hash ids: %w", err)
		}

		for serviceHashID, hashHex := range update.ServiceHashIDsToHashes {
			hashID, ok := hashIDsByHash[hashHex]
			if !ok {
				return fmt.Errorf("missing PTR definition hash id for %q", hashHex)
			}

			if _, err := tx.ExecContext(
				ctx,
				fmt.Sprintf(
					"REPLACE INTO %s (service_hash_id, hash_id) VALUES (?, ?)",
					hashIDMapTableName,
				),
				serviceHashID,
				hashID,
			); err != nil {
				return fmt.Errorf("upsert PTR repository hash id map row: %w", err)
			}
		}
	}

	for serviceTagID, tag := range update.ServiceTagIDsToTags {
		tagID, err := ensureTagIDTx(ctx, tx, tag)
		if err != nil {
			if errors.Is(err, coretags.ErrEmptyTag) {
				tagID, err = ensureTagIDTx(ctx, tx, ptrInvalidRepositoryTag)
				if err != nil {
					return fmt.Errorf("ensure PTR invalid definition tag fallback id: %w", err)
				}
			} else {
				return fmt.Errorf("ensure PTR definition tag id for %q: %w", tag, err)
			}
		}

		if _, err := tx.ExecContext(
			ctx,
			fmt.Sprintf(
				"REPLACE INTO %s (service_tag_id, tag_id) VALUES (?, ?)",
				tagIDMapTableName,
			),
			serviceTagID,
			tagID,
		); err != nil {
			return fmt.Errorf("upsert PTR repository tag id map row: %w", err)
		}
	}

	return nil
}

func applyPTRMappingsTx(
	ctx context.Context,
	tx *ImmediateTx,
	serviceID int64,
	update PTRMappingsUpdate,
) error {
	for _, row := range update.Adds {
		tagID, hashIDs, err := normalizePTRMappingRow(ctx, tx, serviceID, row)
		if err != nil {
			return err
		}

		for _, hashID := range hashIDs {
			if _, err := tx.ExecContext(
				ctx,
				fmt.Sprintf("DELETE FROM external_mappings.deleted_mappings_%d WHERE tag_id = ? AND hash_id = ?", serviceID),
				tagID,
				hashID,
			); err != nil {
				return fmt.Errorf("delete PTR deleted mapping row before add: %w", err)
			}

			if _, err := tx.ExecContext(
				ctx,
				fmt.Sprintf("DELETE FROM external_mappings.pending_mappings_%d WHERE tag_id = ? AND hash_id = ?", serviceID),
				tagID,
				hashID,
			); err != nil {
				return fmt.Errorf("delete PTR pending mapping row before add: %w", err)
			}

			if _, err := tx.ExecContext(
				ctx,
				fmt.Sprintf("INSERT OR IGNORE INTO external_mappings.current_mappings_%d (tag_id, hash_id) VALUES (?, ?)", serviceID),
				tagID,
				hashID,
			); err != nil {
				return fmt.Errorf("insert PTR current mapping row: %w", err)
			}
		}
	}

	for _, row := range update.Deletes {
		tagID, hashIDs, err := normalizePTRMappingRow(ctx, tx, serviceID, row)
		if err != nil {
			return err
		}

		for _, hashID := range hashIDs {
			if _, err := tx.ExecContext(
				ctx,
				fmt.Sprintf("DELETE FROM external_mappings.current_mappings_%d WHERE tag_id = ? AND hash_id = ?", serviceID),
				tagID,
				hashID,
			); err != nil {
				return fmt.Errorf("delete PTR current mapping row before delete: %w", err)
			}

			if _, err := tx.ExecContext(
				ctx,
				fmt.Sprintf("DELETE FROM external_mappings.petitioned_mappings_%d WHERE tag_id = ? AND hash_id = ?", serviceID),
				tagID,
				hashID,
			); err != nil {
				return fmt.Errorf("delete PTR petitioned mapping row before delete: %w", err)
			}

			if _, err := tx.ExecContext(
				ctx,
				fmt.Sprintf("INSERT OR IGNORE INTO external_mappings.deleted_mappings_%d (tag_id, hash_id) VALUES (?, ?)", serviceID),
				tagID,
				hashID,
			); err != nil {
				return fmt.Errorf("insert PTR deleted mapping row: %w", err)
			}
		}
	}

	return nil
}

func normalizePTRMappingRow(
	ctx context.Context,
	tx *ImmediateTx,
	serviceID int64,
	row PTRMappingUpdateRow,
) (int64, []int64, error) {
	hashIDMapTableName, tagIDMapTableName := generatePTRRepositoryDefinitionTableNames(serviceID)

	var tagID int64
	if err := tx.QueryRowContext(
		ctx,
		fmt.Sprintf("SELECT tag_id FROM %s WHERE service_tag_id = ?", tagIDMapTableName),
		row.ServiceTagID,
	).Scan(&tagID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil, fmt.Errorf("PTR repository tag definition missing for service_tag_id %d", row.ServiceTagID)
		}

		return 0, nil, fmt.Errorf("query PTR repository tag definition row: %w", err)
	}

	hashIDs := make([]int64, 0, len(row.ServiceHashIDs))
	for _, serviceHashID := range row.ServiceHashIDs {
		var hashID int64
		if err := tx.QueryRowContext(
			ctx,
			fmt.Sprintf("SELECT hash_id FROM %s WHERE service_hash_id = ?", hashIDMapTableName),
			serviceHashID,
		).Scan(&hashID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return 0, nil, fmt.Errorf("PTR repository hash definition missing for service_hash_id %d", serviceHashID)
			}

			return 0, nil, fmt.Errorf("query PTR repository hash definition row: %w", err)
		}

		hashIDs = append(hashIDs, hashID)
	}

	return tagID, hashIDs, nil
}

func markPTRUpdateProcessed(
	ctx context.Context,
	tx *ImmediateTx,
	serviceID int64,
	hashID int64,
	contentType int64,
) error {
	_, _, repositoryProcessedTableName := generatePTRRepositoryTableNames(serviceID)

	if _, err := tx.ExecContext(
		ctx,
		fmt.Sprintf("UPDATE %s SET processed = ? WHERE hash_id = ? AND content_type = ?", repositoryProcessedTableName),
		1,
		hashID,
		contentType,
	); err != nil {
		return fmt.Errorf("mark PTR update processed: %w", err)
	}

	return nil
}

func recomputePTRProcessedCounts(
	ctx context.Context,
	tx *ImmediateTx,
	serviceID int64,
	runToken string,
) error {
	_, _, repositoryProcessedTableName := generatePTRRepositoryTableNames(serviceID)

	var processedDefinitionCount int64
	if err := tx.QueryRowContext(
		ctx,
		fmt.Sprintf(
			"SELECT COUNT(*) FROM %s WHERE content_type = ? AND processed = ?",
			repositoryProcessedTableName,
		),
		PTRContentTypeDefinitions,
		1,
	).Scan(&processedDefinitionCount); err != nil {
		return fmt.Errorf("query PTR processed definition count: %w", err)
	}

	var processedContentCount int64
	if err := tx.QueryRowContext(
		ctx,
		fmt.Sprintf(
			"SELECT COUNT(*) FROM %s WHERE content_type = ? AND processed = ?",
			repositoryProcessedTableName,
		),
		PTRContentTypeMappings,
		1,
	).Scan(&processedContentCount); err != nil {
		return fmt.Errorf("query PTR processed content count: %w", err)
	}

	nowMS := time.Now().UTC().UnixMilli()
	result, err := tx.ExecContext(
		ctx,
		`UPDATE main.ptr_sync_state
		SET processed_definition_count = ?,
			processed_content_count = ?,
			updated_at_ms = ?
		WHERE singleton = ? AND is_running = 1 AND run_token = ?`,
		processedDefinitionCount,
		processedContentCount,
		nowMS,
		ptrSyncStateSingleton,
		runToken,
	)
	if err != nil {
		return fmt.Errorf("update ptr_sync_state processed counts: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read ptr_sync_state processed counts rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return errPTRSyncRunNotActive
	}

	return nil
}

func stagePTRPendingMappingsTx(
	ctx context.Context,
	tx *ImmediateTx,
	serviceID int64,
	tags []string,
	hashIDs []int64,
) (int64, error) {
	var addedMappings int64
	for _, tag := range tags {
		tagID, err := ensureTagIDTx(ctx, tx, tag)
		if err != nil {
			return 0, fmt.Errorf("ensure PTR pending tag id for %q: %w", tag, err)
		}

		for _, hashID := range hashIDs {
			if _, err := tx.ExecContext(
				ctx,
				fmt.Sprintf("DELETE FROM external_mappings.deleted_mappings_%d WHERE tag_id = ? AND hash_id = ?", serviceID),
				tagID,
				hashID,
			); err != nil {
				return 0, fmt.Errorf("delete PTR deleted mapping row before pending add: %w", err)
			}

			if _, err := tx.ExecContext(
				ctx,
				fmt.Sprintf("DELETE FROM external_mappings.petitioned_mappings_%d WHERE tag_id = ? AND hash_id = ?", serviceID),
				tagID,
				hashID,
			); err != nil {
				return 0, fmt.Errorf("delete PTR petitioned mapping row before pending add: %w", err)
			}

			exists, err := ptrMappingExistsTx(
				ctx,
				tx,
				fmt.Sprintf("external_mappings.current_mappings_%d", serviceID),
				tagID,
				hashID,
			)
			if err != nil {
				return 0, err
			}
			if exists {
				continue
			}

			result, err := tx.ExecContext(
				ctx,
				fmt.Sprintf("INSERT OR IGNORE INTO external_mappings.pending_mappings_%d (tag_id, hash_id) VALUES (?, ?)", serviceID),
				tagID,
				hashID,
			)
			if err != nil {
				return 0, fmt.Errorf("insert PTR pending mapping row: %w", err)
			}

			rowsAffected, err := result.RowsAffected()
			if err != nil {
				return 0, fmt.Errorf("read PTR pending mapping insert rows affected: %w", err)
			}

			addedMappings += rowsAffected
		}
	}

	return addedMappings, nil
}

func commitPTRPendingMappingsSuccessTx(
	ctx context.Context,
	tx *ImmediateTx,
	serviceID int64,
) (int64, error) {
	pendingTableName := fmt.Sprintf("external_mappings.pending_mappings_%d", serviceID)
	deletedTableName := fmt.Sprintf("external_mappings.deleted_mappings_%d", serviceID)
	petitionedTableName := fmt.Sprintf("external_mappings.petitioned_mappings_%d", serviceID)
	currentTableName := fmt.Sprintf("external_mappings.current_mappings_%d", serviceID)

	var committedMappings int64
	if err := tx.QueryRowContext(
		ctx,
		fmt.Sprintf("SELECT COUNT(*) FROM %s", pendingTableName),
	).Scan(&committedMappings); err != nil {
		return 0, fmt.Errorf("count PTR pending mappings before commit: %w", err)
	}

	if committedMappings == 0 {
		return 0, nil
	}

	for _, statement := range []string{
		fmt.Sprintf(
			`DELETE FROM %s
			WHERE EXISTS (
				SELECT 1 FROM %s pending
				WHERE pending.tag_id = %s.tag_id AND pending.hash_id = %s.hash_id
			)`,
			deletedTableName,
			pendingTableName,
			deletedTableName,
			deletedTableName,
		),
		fmt.Sprintf(
			`DELETE FROM %s
			WHERE EXISTS (
				SELECT 1 FROM %s pending
				WHERE pending.tag_id = %s.tag_id AND pending.hash_id = %s.hash_id
			)`,
			petitionedTableName,
			pendingTableName,
			petitionedTableName,
			petitionedTableName,
		),
		fmt.Sprintf(
			`INSERT OR IGNORE INTO %s (tag_id, hash_id)
			SELECT tag_id, hash_id FROM %s`,
			currentTableName,
			pendingTableName,
		),
		fmt.Sprintf(`DELETE FROM %s`, pendingTableName),
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return 0, fmt.Errorf("apply PTR pending commit success transition: %w", err)
		}
	}

	return committedMappings, nil
}

func ptrMappingExistsTx(
	ctx context.Context,
	tx *ImmediateTx,
	tableName string,
	tagID int64,
	hashID int64,
) (bool, error) {
	row := tx.QueryRowContext(
		ctx,
		fmt.Sprintf("SELECT 1 FROM %s WHERE tag_id = ? AND hash_id = ? LIMIT 1", tableName),
		tagID,
		hashID,
	)

	var sentinel int64
	if err := row.Scan(&sentinel); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}

		return false, fmt.Errorf("query PTR mapping existence in %s: %w", tableName, err)
	}

	return true, nil
}

func (b *Bundle) resolvePTRPendingRequestHashes(
	ctx context.Context,
	request coreptrsync.PendingMappingsRequest,
) ([]string, error) {
	hashes := dedupeStrings(request.Hashes)

	if len(request.FileIDs) == 0 {
		return hashes, nil
	}

	resolvedByFileID, err := b.lookupHashesByFileIDs(ctx, dedupeInt64s(request.FileIDs))
	if err != nil {
		return nil, err
	}

	for _, fileID := range request.FileIDs {
		hash, ok := resolvedByFileID[fileID]
		if !ok {
			return nil, fmt.Errorf("file_id %d could not be resolved to a hash", fileID)
		}

		hashes = append(hashes, hash)
	}

	return dedupeStrings(hashes), nil
}

func normalizePTRTargetServiceKey(serviceKey string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(serviceKey))
	if normalized == "" {
		return coreptrsync.DaemonServiceKeyHex(), nil
	}

	if normalized != coreptrsync.DaemonServiceKeyHex() {
		return "", fmt.Errorf("unsupported PTR service key %q", serviceKey)
	}

	return normalized, nil
}

func recordPreparedLocalImportTx(
	ctx context.Context,
	tx *ImmediateTx,
	prepared PreparedLocalImport,
) (PreparedLocalImportResult, error) {
	normalized, err := normalizePreparedLocalImport(prepared)
	if err != nil {
		return PreparedLocalImportResult{}, err
	}

	plan, err := resolvePreparedLocalImportPlanTx(ctx, tx, normalized.localFileServiceKey)
	if err != nil {
		return PreparedLocalImportResult{}, err
	}

	result := PreparedLocalImportResult{}

	hashID, hashExists, err := lookupHashIDByHash(ctx, tx, normalized.hashBytes)
	if err != nil {
		return PreparedLocalImportResult{}, err
	}

	if hashExists {
		record, ok, err := lookupBasicRecordByHashID(ctx, tx, hashID)
		if err != nil {
			return PreparedLocalImportResult{}, err
		}

		if ok {
			matches, err := preparedLocalImportMatchesExistingTx(ctx, tx, hashID, normalized, plan, record)
			if err != nil {
				return PreparedLocalImportResult{}, err
			}

			if !matches {
				return PreparedLocalImportResult{}, fmt.Errorf(
					"prepared import conflicts with existing file_id %d",
					hashID,
				)
			}

			result = PreparedLocalImportResult{FileID: hashID, AlreadyImported: true}
			if err := ensurePreparedAuxiliaryMetadata(ctx, tx, hashID, normalized, plan); err != nil {
				return PreparedLocalImportResult{}, err
			}

			return result, nil
		}
	} else {
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO external_master.hashes (hash) VALUES (?)`,
			normalized.hashBytes,
		); err != nil {
			return PreparedLocalImportResult{}, fmt.Errorf("insert external_master.hashes row: %w", err)
		}

		hashID, hashExists, err = lookupHashIDByHash(ctx, tx, normalized.hashBytes)
		if err != nil {
			return PreparedLocalImportResult{}, err
		}

		if !hashExists {
			return PreparedLocalImportResult{}, errors.New("inserted hash row was not readable inside transaction")
		}
	}

	if err := insertPreparedFilesInfo(ctx, tx, hashID, normalized); err != nil {
		return PreparedLocalImportResult{}, err
	}

	if err := ensurePreparedAuxiliaryMetadata(ctx, tx, hashID, normalized, plan); err != nil {
		return PreparedLocalImportResult{}, err
	}

	if plan.createInbox {
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO main.file_inbox (hash_id) VALUES (?)`,
			hashID,
		); err != nil {
			return PreparedLocalImportResult{}, fmt.Errorf("insert file_inbox row: %w", err)
		}
	}

	if normalized.fileModifiedAtMS.Valid {
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO main.file_modified_timestamps (hash_id, file_modified_timestamp_ms) VALUES (?, ?)`,
			hashID,
			normalized.fileModifiedAtMS.Int64,
		); err != nil {
			return PreparedLocalImportResult{}, fmt.Errorf("insert file_modified_timestamps row: %w", err)
		}
	}

	for _, membership := range plan.currentMemberships {
		if err := insertPreparedCurrentMembership(ctx, tx, hashID, membership, normalized.importedAtMS); err != nil {
			return PreparedLocalImportResult{}, err
		}
	}

	result = PreparedLocalImportResult{FileID: hashID}
	return result, nil
}

func resolvePreparedLocalImportPlanTx(
	ctx context.Context,
	tx *ImmediateTx,
	localFileServiceKey string,
) (preparedLocalImportPlan, error) {
	definitions, err := lookupAllServiceDefinitionsQuerier(ctx, tx)
	if err != nil {
		return preparedLocalImportPlan{}, err
	}

	tableNames, err := lookupSchemaTableNamesTx(ctx, tx, "main")
	if err != nil {
		return preparedLocalImportPlan{}, err
	}

	localFileService, err := resolveTargetLocalFileService(definitions, localFileServiceKey)
	if err != nil {
		return preparedLocalImportPlan{}, err
	}

	hydrusLocalStorage, ok, err := findUniqueServiceByType(definitions, services.TypeHydrusLocalFileStorage)
	if err != nil {
		return preparedLocalImportPlan{}, err
	}
	if !ok && localFileService.serviceType != services.TypeLocalFileUpdateDomain {
		return preparedLocalImportPlan{}, errors.New("required hydrus local file storage service is missing")
	}

	plan := preparedLocalImportPlan{}
	_, plan.hasPixelHashMap = tableNames["pixel_hash_map"]
	_, plan.hasTransparency = tableNames["has_transparency"]
	plan.createInbox = localFileService.serviceType == services.TypeLocalFileDomain

	if localFileService.serviceType == services.TypeLocalFileUpdateDomain {
		if err := appendPreparedCurrentMembership(&plan, tableNames, localFileService, true, true); err != nil {
			return preparedLocalImportPlan{}, err
		}

		return plan, nil
	}

	if err := appendPreparedCurrentMembership(&plan, tableNames, localFileService, true, true); err != nil {
		return preparedLocalImportPlan{}, err
	}

	if err := appendPreparedCurrentMembership(&plan, tableNames, hydrusLocalStorage, true, true); err != nil {
		return preparedLocalImportPlan{}, err
	}

	if combinedLocal, ok, err := findUniqueServiceByType(definitions, services.TypeCombinedLocalFileDomains); err != nil {
		return preparedLocalImportPlan{}, err
	} else if ok {
		if err := appendPreparedCurrentMembership(&plan, tableNames, combinedLocal, true, false); err != nil {
			return preparedLocalImportPlan{}, err
		}
	}

	if combinedFile, ok, err := findUniqueServiceByType(definitions, services.TypeCombinedFile); err != nil {
		return preparedLocalImportPlan{}, err
	} else if ok {
		if err := appendPreparedCurrentMembership(&plan, tableNames, combinedFile, false, false); err != nil {
			return preparedLocalImportPlan{}, err
		}
	}

	return plan, nil
}

func lookupSchemaTableNamesTx(
	ctx context.Context,
	q queryRowContextQuerier,
	schemaName string,
) (map[string]struct{}, error) {
	switch schemaName {
	case "main", "external_master", "external_caches", "external_mappings":
	default:
		return nil, fmt.Errorf("unsupported sqlite schema name %q", schemaName)
	}

	rows, err := q.QueryContext(
		ctx,
		fmt.Sprintf(`SELECT name FROM %s.sqlite_master WHERE type = 'table'`, schemaName),
	)
	if err != nil {
		return nil, fmt.Errorf("query sqlite table names for schema %s: %w", schemaName, err)
	}
	defer rows.Close()

	tableNames := map[string]struct{}{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan sqlite table name for schema %s: %w", schemaName, err)
		}

		tableNames[name] = struct{}{}
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sqlite table names for schema %s: %w", schemaName, err)
	}

	return tableNames, nil
}

func preparedLocalImportMatchesExistingTx(
	ctx context.Context,
	q queryRowContextQuerier,
	hashID int64,
	prepared normalizedPreparedLocalImport,
	plan preparedLocalImportPlan,
	record basicRecord,
) (bool, error) {
	if !basicRecordMatchesPreparedImport(record, prepared) {
		return false, nil
	}

	fileModifiedTimestamp, fileModifiedExists, err := lookupNullableInt64(
		ctx,
		q,
		`SELECT file_modified_timestamp_ms
		FROM main.file_modified_timestamps
		WHERE hash_id = ?`,
		hashID,
	)
	if err != nil {
		return false, err
	}
	if !fileModifiedRowMatches(fileModifiedTimestamp, fileModifiedExists, prepared.fileModifiedAtMS) {
		return false, nil
	}

	currentByHashID, _, err := lookupFileServiceMembershipsTx(ctx, q, []int64{hashID})
	if err != nil {
		return false, err
	}

	if !currentMembershipsMatchPlan(currentByHashID[hashID], plan) {
		return false, nil
	}

	if plan.hasPixelHashMap {
		pixelHashHex, pixelHashExists, err := lookupPreparedPixelHashHexByHashID(ctx, q, hashID)
		if err != nil {
			return false, err
		}

		if !optionalHashHexMatches(pixelHashHex, pixelHashExists, prepared.pixelHashHex) {
			return false, nil
		}
	}

	if plan.hasTransparency {
		hasTransparency, err := lookupPreparedHasTransparencyByHashID(ctx, q, hashID)
		if err != nil {
			return false, err
		}

		if !optionalTransparencyMatches(hasTransparency, prepared.hasTransparency) {
			return false, nil
		}
	}

	return true, nil
}

func lookupFileServiceMembershipsTx(
	ctx context.Context,
	q queryRowContextQuerier,
	fileIDs []int64,
) (map[int64][]currentFileServiceMembership, map[int64][]deletedFileServiceMembership, error) {
	if len(fileIDs) == 0 {
		return map[int64][]currentFileServiceMembership{}, map[int64][]deletedFileServiceMembership{}, nil
	}

	definitions, err := lookupAllServiceDefinitionsQuerier(ctx, q)
	if err != nil {
		return nil, nil, err
	}

	tableNames, err := lookupSchemaTableNamesTx(ctx, q, "main")
	if err != nil {
		return nil, nil, err
	}

	currentByHashID := make(map[int64][]currentFileServiceMembership, len(fileIDs))
	deletedByHashID := make(map[int64][]deletedFileServiceMembership, len(fileIDs))
	for _, fileID := range fileIDs {
		currentByHashID[fileID] = []currentFileServiceMembership{}
		deletedByHashID[fileID] = []deletedFileServiceMembership{}
	}

	sortedFileIDs := append([]int64(nil), fileIDs...)
	sort.Slice(sortedFileIDs, func(i, j int) bool { return sortedFileIDs[i] < sortedFileIDs[j] })

	for _, service := range definitions {
		currentTableName := fmt.Sprintf("current_files_%d", service.id)
		if _, ok := tableNames[currentTableName]; ok {
			memberships, err := lookupCurrentMembershipsTx(ctx, q, currentTableName, service, sortedFileIDs)
			if err != nil {
				return nil, nil, err
			}

			for hashID, rows := range memberships {
				currentByHashID[hashID] = append(currentByHashID[hashID], rows...)
			}
		}

		deletedTableName := fmt.Sprintf("deleted_files_%d", service.id)
		if _, ok := tableNames[deletedTableName]; ok {
			memberships, err := lookupDeletedMembershipsTx(ctx, q, deletedTableName, service, sortedFileIDs)
			if err != nil {
				return nil, nil, err
			}

			for hashID, rows := range memberships {
				deletedByHashID[hashID] = append(deletedByHashID[hashID], rows...)
			}
		}
	}

	return currentByHashID, deletedByHashID, nil
}

func lookupCurrentMembershipsTx(
	ctx context.Context,
	q queryRowContextQuerier,
	tableName string,
	service serviceDefinition,
	fileIDs []int64,
) (map[int64][]currentFileServiceMembership, error) {
	rows, err := q.QueryContext(
		ctx,
		fmt.Sprintf(`SELECT hash_id, timestamp_ms FROM main.%s`, tableName),
	)
	if err != nil {
		return nil, fmt.Errorf("query current file service %s rows: %w", tableName, err)
	}
	defer rows.Close()

	allowed := make(map[int64]struct{}, len(fileIDs))
	for _, fileID := range fileIDs {
		allowed[fileID] = struct{}{}
	}

	memberships := map[int64][]currentFileServiceMembership{}
	for rows.Next() {
		var (
			hashID            int64
			importedTimestamp sql.NullInt64
		)

		if err := rows.Scan(&hashID, &importedTimestamp); err != nil {
			return nil, fmt.Errorf("scan current file service %s row: %w", tableName, err)
		}

		if _, ok := allowed[hashID]; !ok {
			continue
		}

		memberships[hashID] = append(memberships[hashID], currentFileServiceMembership{
			service:             service,
			importedTimestampMS: importedTimestamp,
		})
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate current file service %s rows: %w", tableName, err)
	}

	return memberships, nil
}

func lookupDeletedMembershipsTx(
	ctx context.Context,
	q queryRowContextQuerier,
	tableName string,
	service serviceDefinition,
	fileIDs []int64,
) (map[int64][]deletedFileServiceMembership, error) {
	rows, err := q.QueryContext(
		ctx,
		fmt.Sprintf(`SELECT hash_id, timestamp_ms, original_timestamp_ms FROM main.%s`, tableName),
	)
	if err != nil {
		return nil, fmt.Errorf("query deleted file service %s rows: %w", tableName, err)
	}
	defer rows.Close()

	allowed := make(map[int64]struct{}, len(fileIDs))
	for _, fileID := range fileIDs {
		allowed[fileID] = struct{}{}
	}

	memberships := map[int64][]deletedFileServiceMembership{}
	for rows.Next() {
		var (
			hashID            int64
			deletedTimestamp  sql.NullInt64
			originalTimestamp sql.NullInt64
		)

		if err := rows.Scan(&hashID, &deletedTimestamp, &originalTimestamp); err != nil {
			return nil, fmt.Errorf("scan deleted file service %s row: %w", tableName, err)
		}

		if _, ok := allowed[hashID]; !ok {
			continue
		}

		memberships[hashID] = append(memberships[hashID], deletedFileServiceMembership{
			service:                   service,
			deletedTimestampMS:        deletedTimestamp,
			originalImportedTimestamp: originalTimestamp,
		})
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate deleted file service %s rows: %w", tableName, err)
	}

	return memberships, nil
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
