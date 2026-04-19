package hydrusdb

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"

	"github.com/official-elinas/hydrus-go/internal/core/filetrash"
	"github.com/official-elinas/hydrus-go/internal/core/services"
)

type trashFilePlan struct {
	currentRemovals []string
	deletedWrites   []trashDeletedWrite
	trashTableName  string
	alreadyTrashed  bool
	trashedAtMS     int64
}

type trashDeletedWrite struct {
	tableName                 string
	deletedTimestampMS        sql.NullInt64
	originalImportedTimestamp sql.NullInt64
}

// TrashFile moves one current local file into the Hydrus trash domain.
func (b *Bundle) TrashFile(
	ctx context.Context,
	request filetrash.Request,
) (filetrash.Result, error) {
	if request.FileID <= 0 {
		return filetrash.Result{}, &filetrash.RequestError{Message: "file_id must be greater than zero"}
	}

	result := filetrash.Result{FileID: request.FileID, Trashed: true}

	err := b.WithImmediateTx(ctx, func(tx *ImmediateTx) error {
		exists, err := fileExistsByID(ctx, tx, request.FileID)
		if err != nil {
			return err
		}

		if !exists {
			return &filetrash.NotFoundError{
				Message: fmt.Sprintf("file_id %d was not found", request.FileID),
			}
		}

		plan, err := b.resolveTrashFilePlan(ctx, request.FileID)
		if err != nil {
			return err
		}

		if plan.alreadyTrashed {
			result.RemovedFromRecent = false
			return nil
		}

		result.RemovedFromRecent = len(plan.currentRemovals) > 0

		for _, tableName := range plan.currentRemovals {
			query := fmt.Sprintf(`DELETE FROM main.%s WHERE hash_id = ?`, tableName)
			if _, err := tx.ExecContext(ctx, query, request.FileID); err != nil {
				return fmt.Errorf("delete current membership from %s: %w", tableName, err)
			}
		}

		if err := upsertCurrentMembership(
			ctx,
			tx,
			plan.trashTableName,
			request.FileID,
			plan.trashedAtMS,
		); err != nil {
			return err
		}

		for _, write := range plan.deletedWrites {
			if err := upsertDeletedMembership(
				ctx,
				tx,
				write.tableName,
				request.FileID,
				write.deletedTimestampMS,
				write.originalImportedTimestamp,
			); err != nil {
				return err
			}
		}

		return nil
	})
	if err != nil {
		return filetrash.Result{}, err
	}

	return result, nil
}

func (b *Bundle) resolveTrashFilePlan(
	ctx context.Context,
	fileID int64,
) (trashFilePlan, error) {
	definitions, err := b.lookupAllServiceDefinitions(ctx)
	if err != nil {
		return trashFilePlan{}, err
	}

	tableNames, err := b.lookupMainTableNames(ctx)
	if err != nil {
		return trashFilePlan{}, err
	}

	currentMemberships, _, err := b.lookupFileServiceMemberships(ctx, []int64{fileID})
	if err != nil {
		return trashFilePlan{}, err
	}

	current := currentMemberships[fileID]
	if hasCurrentServiceType(current, services.TypeLocalFileTrashDomain) {
		return trashFilePlan{alreadyTrashed: true}, nil
	}

	trashService, ok, err := findUniqueServiceByType(
		definitions,
		services.TypeLocalFileTrashDomain,
	)
	if err != nil {
		return trashFilePlan{}, err
	}
	if !ok {
		return trashFilePlan{}, fmt.Errorf("required trash service is missing")
	}

	trashTableName := fmt.Sprintf("current_files_%d", trashService.id)
	if _, ok := tableNames[trashTableName]; !ok {
		return trashFilePlan{}, fmt.Errorf(
			"required trash membership table %q is missing",
			trashTableName,
		)
	}

	removals := collectTrashCurrentRemovals(current)
	if len(removals) == 0 {
		return trashFilePlan{}, &filetrash.RequestError{
			Message: fmt.Sprintf("file_id %d is not a current local file", fileID),
		}
	}

	sort.Strings(removals)

	deletedWrites := []trashDeletedWrite{}
	trashedAtMS := time.Now().UTC().UnixMilli()
	originalImportedAtMS := selectTrashOriginalImportedTimestamp(current)

	if combinedLocal, ok, err := findUniqueServiceByType(
		definitions,
		services.TypeCombinedLocalFileDomains,
	); err != nil {
		return trashFilePlan{}, err
	} else if ok {
		deletedTableName := fmt.Sprintf("deleted_files_%d", combinedLocal.id)
		if _, exists := tableNames[deletedTableName]; exists {
			deletedWrites = append(deletedWrites, trashDeletedWrite{
				tableName:                 deletedTableName,
				deletedTimestampMS:        sql.NullInt64{Int64: trashedAtMS, Valid: true},
				originalImportedTimestamp: originalImportedAtMS,
			})
		}
	}

	if combinedFile, ok, err := findUniqueServiceByType(
		definitions,
		services.TypeCombinedFile,
	); err != nil {
		return trashFilePlan{}, err
	} else if ok {
		deletedTableName := fmt.Sprintf("deleted_files_%d", combinedFile.id)
		if _, exists := tableNames[deletedTableName]; exists {
			deletedWrites = append(deletedWrites, trashDeletedWrite{tableName: deletedTableName})
		}
	}

	return trashFilePlan{
		currentRemovals: removals,
		deletedWrites:   deletedWrites,
		trashTableName:  trashTableName,
		trashedAtMS:     trashedAtMS,
	}, nil
}

func fileExistsByID(ctx context.Context, q rowQuerier, fileID int64) (bool, error) {
	row := q.QueryRowContext(
		ctx,
		`SELECT 1 FROM main.files_info WHERE hash_id = ?`,
		fileID,
	)

	var exists int
	if err := row.Scan(&exists); err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}

		return false, fmt.Errorf("query file existence by id: %w", err)
	}

	return true, nil
}

func collectTrashCurrentRemovals(current []currentFileServiceMembership) []string {
	tableSet := map[string]struct{}{}
	for _, membership := range current {
		switch membership.service.serviceType {
		case services.TypeLocalFileDomain,
			services.TypeHydrusLocalFileStorage,
			services.TypeCombinedLocalFileDomains,
			services.TypeCombinedFile:
			tableName := fmt.Sprintf("current_files_%d", membership.service.id)
			tableSet[tableName] = struct{}{}
		}
	}

	tables := make([]string, 0, len(tableSet))
	for tableName := range tableSet {
		tables = append(tables, tableName)
	}

	return tables
}

func selectTrashOriginalImportedTimestamp(
	current []currentFileServiceMembership,
) sql.NullInt64 {
	preferredTypes := []services.Type{
		services.TypeCombinedLocalFileDomains,
		services.TypeLocalFileDomain,
		services.TypeHydrusLocalFileStorage,
	}

	for _, serviceType := range preferredTypes {
		for _, membership := range current {
			if membership.service.serviceType != serviceType {
				continue
			}

			if membership.importedTimestampMS.Valid {
				return membership.importedTimestampMS
			}
		}
	}

	return sql.NullInt64{}
}

func upsertCurrentMembership(
	ctx context.Context,
	tx *ImmediateTx,
	tableName string,
	fileID int64,
	timestampMS int64,
) error {
	query := fmt.Sprintf(
		`INSERT INTO main.%s (hash_id, timestamp_ms)
		VALUES (?, ?)
		ON CONFLICT(hash_id) DO UPDATE SET timestamp_ms = excluded.timestamp_ms`,
		tableName,
	)

	if _, err := tx.ExecContext(ctx, query, fileID, timestampMS); err != nil {
		return fmt.Errorf("upsert current membership %s: %w", tableName, err)
	}

	return nil
}

func upsertDeletedMembership(
	ctx context.Context,
	tx *ImmediateTx,
	tableName string,
	fileID int64,
	deletedTimestampMS sql.NullInt64,
	originalImportedTimestamp sql.NullInt64,
) error {
	query := fmt.Sprintf(
		`INSERT INTO main.%s (hash_id, timestamp_ms, original_timestamp_ms)
		VALUES (?, ?, ?)
		ON CONFLICT(hash_id) DO UPDATE SET
			timestamp_ms = excluded.timestamp_ms,
			original_timestamp_ms = COALESCE(main.%s.original_timestamp_ms, excluded.original_timestamp_ms)`,
		tableName,
		tableName,
	)

	if _, err := tx.ExecContext(
		ctx,
		query,
		fileID,
		nullableInt64Value(deletedTimestampMS),
		nullableInt64Value(originalImportedTimestamp),
	); err != nil {
		return fmt.Errorf("upsert deleted membership %s: %w", tableName, err)
	}

	return nil
}
