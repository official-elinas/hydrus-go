package hydrusdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/official-elinas/hydrus-go/internal/core/clientapi"
	"github.com/official-elinas/hydrus-go/internal/core/services"
)

func (b *Bundle) AssociateURLs(ctx context.Context, request clientapi.URLAssociationRequest) error {
	normalizedURLs, err := normalizeKnownURLs(request.URLsToAdd)
	if err != nil {
		return &clientapi.RequestError{Message: err.Error()}
	}
	if len(normalizedURLs) == 0 {
		return &clientapi.RequestError{Message: "urls_to_add must contain at least one full http or https URL"}
	}

	return b.WithImmediateTx(ctx, func(tx *ImmediateTx) error {
		hashIDs, err := resolveExistingClientAPIHashIDsTx(ctx, tx, request.Hashes, request.FileIDs)
		if err != nil {
			return err
		}

		if err := ensureKnownURLTables(ctx, tx); err != nil {
			return err
		}

		for _, knownURL := range normalizedURLs {
			urlID, err := ensureKnownURLID(ctx, tx, knownURL)
			if err != nil {
				return err
			}

			for _, hashID := range hashIDs {
				if _, err := tx.ExecContext(
					ctx,
					`INSERT OR IGNORE INTO main.url_map (hash_id, url_id) VALUES (?, ?)`,
					hashID,
					urlID,
				); err != nil {
					return fmt.Errorf("insert url_map row: %w", err)
				}
			}
		}

		return nil
	})
}

func (b *Bundle) AddTags(ctx context.Context, request clientapi.TagRequest) error {
	serviceKey := strings.ToLower(strings.TrimSpace(request.ServiceKey))
	if serviceKey == "" {
		return &clientapi.RequestError{Message: "service key is required"}
	}
	if len(request.Tags) == 0 {
		return &clientapi.RequestError{Message: "at least one tag is required"}
	}

	return b.WithImmediateTx(ctx, func(tx *ImmediateTx) error {
		hashIDs, err := resolveExistingClientAPIHashIDsTx(ctx, tx, request.Hashes, request.FileIDs)
		if err != nil {
			return err
		}

		definitions, err := lookupAllServiceDefinitionsQuerier(ctx, tx)
		if err != nil {
			return err
		}

		tagService, ok := findServiceDefinitionByKey(definitions, serviceKey)
		if !ok {
			return &clientapi.NotFoundError{Message: "service not found"}
		}
		if tagService.serviceType != services.TypeLocalTag {
			return &clientapi.UnsupportedError{Message: "only local tag services are supported by this add_tags path"}
		}

		if err := ensurePTRMappingsTables(ctx, tx, tagService.id); err != nil {
			return err
		}

		mainTableNames, err := lookupSchemaTableNamesTx(ctx, tx, "main")
		if err != nil {
			return err
		}
		cacheTableNames, err := lookupSchemaTableNamesTx(ctx, tx, "external_caches")
		if err != nil {
			return err
		}

		tagIDs := make([]int64, 0, len(request.Tags))
		for _, rawTag := range request.Tags {
			tagID, err := ensureTagIDTx(ctx, tx, rawTag)
			if err != nil {
				return &clientapi.RequestError{Message: err.Error()}
			}
			tagIDs = append(tagIDs, tagID)
		}
		tagIDs = dedupeInt64s(tagIDs)

		impliedTagIDsByTagID, err := b.lookupDisplayImplicationTagIDs(ctx, tagService.id, tagIDs, cacheTableNames)
		if err != nil {
			return err
		}

		fileServiceIDsByHashID, err := lookupSpecificCacheFileServiceIDsTx(ctx, tx, hashIDs, definitions, mainTableNames)
		if err != nil {
			return err
		}

		for _, tagID := range tagIDs {
			impliedTagIDs := impliedTagIDsByTagID[tagID]
			if len(impliedTagIDs) == 0 {
				impliedTagIDs = []int64{tagID}
			}

			for _, hashID := range hashIDs {
				if err := setCurrentTagMappingTx(ctx, tx, tagService.id, tagID, hashID); err != nil {
					return err
				}

				for _, fileServiceID := range fileServiceIDsByHashID[hashID] {
					if err := setSpecificCurrentTagCachesTx(ctx, tx, cacheTableNames, fileServiceID, tagService.id, tagID, impliedTagIDs, hashID); err != nil {
						return err
					}
				}
			}
		}

		return nil
	})
}

func (b *Bundle) SetNotes(ctx context.Context, request clientapi.NotesRequest) (map[string]string, error) {
	if len(request.Notes) == 0 {
		return nil, &clientapi.RequestError{Message: "notes must contain at least one entry"}
	}

	fileIDs := []int64{}
	if request.FileID != nil {
		fileIDs = append(fileIDs, *request.FileID)
	}
	hashes := []string{}
	if strings.TrimSpace(request.Hash) != "" {
		hashes = append(hashes, request.Hash)
	}

	result := map[string]string{}
	for name, note := range request.Notes {
		result[name] = note
	}

	err := b.WithImmediateTx(ctx, func(tx *ImmediateTx) error {
		hashIDs, err := resolveExistingClientAPIHashIDsTx(ctx, tx, hashes, fileIDs)
		if err != nil {
			return err
		}
		if len(hashIDs) != 1 {
			return &clientapi.RequestError{Message: "set_notes expects exactly one target file"}
		}

		if err := ensureNotesTables(ctx, tx); err != nil {
			return err
		}

		for rawName, note := range result {
			name := strings.TrimSpace(rawName)
			if name == "" {
				return &clientapi.RequestError{Message: "note names must not be blank"}
			}

			labelID, err := ensureLabelIDTx(ctx, tx, name)
			if err != nil {
				return err
			}
			noteID, err := ensureNoteIDTx(ctx, tx, note)
			if err != nil {
				return err
			}

			if _, err := tx.ExecContext(
				ctx,
				`INSERT OR REPLACE INTO main.file_notes (hash_id, name_id, note_id) VALUES (?, ?, ?)`,
				hashIDs[0],
				labelID,
				noteID,
			); err != nil {
				return fmt.Errorf("upsert file note row: %w", err)
			}
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return result, nil
}

func (b *Bundle) SetTime(ctx context.Context, request clientapi.TimeRequest) error {
	if request.TimestampMS <= 0 {
		return &clientapi.RequestError{Message: "timestamp_ms must be greater than zero"}
	}

	return b.WithImmediateTx(ctx, func(tx *ImmediateTx) error {
		hashIDs, err := resolveExistingClientAPIHashIDsTx(ctx, tx, request.Hashes, request.FileIDs)
		if err != nil {
			return err
		}

		switch request.TimestampType {
		case clientapi.TimestampTypeModifiedFile:
			if err := ensureFileModifiedTimestampTable(ctx, tx); err != nil {
				return err
			}
			for _, hashID := range hashIDs {
				if _, err := tx.ExecContext(
					ctx,
					`INSERT INTO main.file_modified_timestamps (hash_id, file_modified_timestamp_ms)
					VALUES (?, ?)
					ON CONFLICT(hash_id) DO UPDATE SET file_modified_timestamp_ms = excluded.file_modified_timestamp_ms`,
					hashID,
					request.TimestampMS,
				); err != nil {
					return fmt.Errorf("upsert file modified timestamp row: %w", err)
				}
			}
			return nil

		case clientapi.TimestampTypeModifiedDomain:
			domain := strings.TrimSpace(request.Domain)
			if domain == "" {
				return &clientapi.RequestError{Message: "domain is required for modified domain timestamps"}
			}
			if strings.EqualFold(domain, "local") {
				if err := ensureFileModifiedTimestampTable(ctx, tx); err != nil {
					return err
				}
				for _, hashID := range hashIDs {
					if _, err := tx.ExecContext(
						ctx,
						`INSERT INTO main.file_modified_timestamps (hash_id, file_modified_timestamp_ms)
						VALUES (?, ?)
						ON CONFLICT(hash_id) DO UPDATE SET file_modified_timestamp_ms = excluded.file_modified_timestamp_ms`,
						hashID,
						request.TimestampMS,
					); err != nil {
						return fmt.Errorf("upsert local modified timestamp row: %w", err)
					}
				}
				return nil
			}

			if err := ensureKnownURLTables(ctx, tx); err != nil {
				return err
			}
			if err := ensureDomainModifiedTimestampTable(ctx, tx); err != nil {
				return err
			}

			domainID, err := ensureKnownURLDomainID(ctx, tx, domain)
			if err != nil {
				return err
			}

			for _, hashID := range hashIDs {
				if _, err := tx.ExecContext(
					ctx,
					`INSERT INTO main.file_domain_modified_timestamps (hash_id, domain_id, file_modified_timestamp_ms)
					VALUES (?, ?, ?)
					ON CONFLICT(hash_id, domain_id) DO UPDATE SET file_modified_timestamp_ms = excluded.file_modified_timestamp_ms`,
					hashID,
					domainID,
					request.TimestampMS,
				); err != nil {
					return fmt.Errorf("upsert domain modified timestamp row: %w", err)
				}
			}
			return nil
		default:
			return &clientapi.UnsupportedError{Message: "only modified file and modified domain timestamps are supported"}
		}
	})
}

func resolveExistingClientAPIHashIDsTx(ctx context.Context, tx *ImmediateTx, hashes []string, fileIDs []int64) ([]int64, error) {
	normalizedFileIDs := []int64{}
	for _, fileID := range fileIDs {
		if fileID <= 0 {
			return nil, &clientapi.RequestError{Message: "file ids must be greater than zero"}
		}
		normalizedFileIDs = append(normalizedFileIDs, fileID)
	}

	normalizedHashes := []string{}
	for _, rawHash := range hashes {
		trimmed := strings.TrimSpace(rawHash)
		if trimmed == "" {
			continue
		}
		normalizedHashes = append(normalizedHashes, trimmed)
	}

	if len(normalizedFileIDs) == 0 && len(normalizedHashes) == 0 {
		return nil, &clientapi.RequestError{Message: "at least one file identifier is required"}
	}

	targetIDs := dedupeInt64s(normalizedFileIDs)
	if len(targetIDs) > 0 {
		existingFileIDs, err := lookupExistingFileIDsTx(ctx, tx, targetIDs)
		if err != nil {
			return nil, err
		}
		for _, fileID := range targetIDs {
			if _, ok := existingFileIDs[fileID]; !ok {
				return nil, &clientapi.NotFoundError{Message: fmt.Sprintf("file_id %d was not found", fileID)}
			}
		}
	}

	for _, rawHash := range normalizedHashes {
		_, hashBytes, err := normalizePreparedHash(rawHash)
		if err != nil {
			return nil, &clientapi.RequestError{Message: err.Error()}
		}

		hashID, ok, err := lookupImportedFileIDByHashTx(ctx, tx, hashBytes)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, &clientapi.NotFoundError{Message: fmt.Sprintf("hash %s was not found", strings.ToLower(strings.TrimSpace(rawHash)))}
		}

		targetIDs = append(targetIDs, hashID)
	}

	targetIDs = dedupeInt64s(targetIDs)
	sort.Slice(targetIDs, func(i, j int) bool { return targetIDs[i] < targetIDs[j] })
	return targetIDs, nil
}

func lookupExistingFileIDsTx(ctx context.Context, tx *ImmediateTx, fileIDs []int64) (map[int64]struct{}, error) {
	results := map[int64]struct{}{}
	if len(fileIDs) == 0 {
		return results, nil
	}

	query := fmt.Sprintf(
		`SELECT hash_id FROM main.files_info WHERE hash_id IN (%s)`,
		placeholders(len(fileIDs)),
	)
	rows, err := tx.QueryContext(ctx, query, int64Args(fileIDs)...)
	if err != nil {
		return nil, fmt.Errorf("query existing file ids: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var hashID int64
		if err := rows.Scan(&hashID); err != nil {
			return nil, fmt.Errorf("scan existing file id row: %w", err)
		}
		results[hashID] = struct{}{}
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate existing file ids: %w", err)
	}

	return results, nil
}

func lookupImportedFileIDByHashTx(ctx context.Context, tx *ImmediateTx, hashBytes []byte) (int64, bool, error) {
	row := tx.QueryRowContext(
		ctx,
		`SELECT fi.hash_id
		FROM main.files_info fi
		JOIN external_master.hashes h USING (hash_id)
		WHERE h.hash = ?`,
		hashBytes,
	)

	var hashID int64
	if err := row.Scan(&hashID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("query imported file by hash: %w", err)
	}

	return hashID, true, nil
}

func ensureKnownURLTables(ctx context.Context, tx *ImmediateTx) error {
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS main.url_map (
			hash_id INTEGER,
			url_id INTEGER,
			PRIMARY KEY (hash_id, url_id)
		)`,
		`CREATE TABLE IF NOT EXISTS external_master.url_domains (domain_id INTEGER PRIMARY KEY, domain TEXT UNIQUE)`,
		`CREATE TABLE IF NOT EXISTS external_master.urls (url_id INTEGER PRIMARY KEY, domain_id INTEGER, url TEXT UNIQUE)`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("ensure known URL tables: %w", err)
		}
	}

	return nil
}

func ensureNotesTables(ctx context.Context, tx *ImmediateTx) error {
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS main.file_notes (
			hash_id INTEGER,
			name_id INTEGER,
			note_id INTEGER,
			PRIMARY KEY (hash_id, name_id)
		)`,
		`CREATE TABLE IF NOT EXISTS external_master.labels (label_id INTEGER PRIMARY KEY, label TEXT UNIQUE)`,
		`CREATE TABLE IF NOT EXISTS external_master.notes (note_id INTEGER PRIMARY KEY, note TEXT UNIQUE)`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("ensure note tables: %w", err)
		}
	}

	return nil
}

func ensureFileModifiedTimestampTable(ctx context.Context, tx *ImmediateTx) error {
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS main.file_modified_timestamps (
		hash_id INTEGER PRIMARY KEY,
		file_modified_timestamp_ms INTEGER
	)`); err != nil {
		return fmt.Errorf("ensure file_modified_timestamps table: %w", err)
	}

	return nil
}

func ensureDomainModifiedTimestampTable(ctx context.Context, tx *ImmediateTx) error {
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS main.file_domain_modified_timestamps (
		hash_id INTEGER,
		domain_id INTEGER,
		file_modified_timestamp_ms INTEGER,
		PRIMARY KEY (hash_id, domain_id)
	)`); err != nil {
		return fmt.Errorf("ensure file_domain_modified_timestamps table: %w", err)
	}

	return nil
}

func ensureLabelIDTx(ctx context.Context, tx *ImmediateTx, label string) (int64, error) {
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO external_master.labels (label) VALUES (?)`, label); err != nil {
		return 0, fmt.Errorf("insert label row: %w", err)
	}

	row := tx.QueryRowContext(ctx, `SELECT label_id FROM external_master.labels WHERE label = ?`, label)
	var labelID int64
	if err := row.Scan(&labelID); err != nil {
		return 0, fmt.Errorf("query label row: %w", err)
	}

	return labelID, nil
}

func ensureNoteIDTx(ctx context.Context, tx *ImmediateTx, note string) (int64, error) {
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO external_master.notes (note) VALUES (?)`, note); err != nil {
		return 0, fmt.Errorf("insert note row: %w", err)
	}

	row := tx.QueryRowContext(ctx, `SELECT note_id FROM external_master.notes WHERE note = ?`, note)
	var noteID int64
	if err := row.Scan(&noteID); err != nil {
		return 0, fmt.Errorf("query note row: %w", err)
	}

	return noteID, nil
}

func setCurrentTagMappingTx(ctx context.Context, tx *ImmediateTx, serviceID int64, tagID int64, hashID int64) error {
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{query: fmt.Sprintf(`DELETE FROM external_mappings.deleted_mappings_%d WHERE tag_id = ? AND hash_id = ?`, serviceID), args: []any{tagID, hashID}},
		{query: fmt.Sprintf(`DELETE FROM external_mappings.pending_mappings_%d WHERE tag_id = ? AND hash_id = ?`, serviceID), args: []any{tagID, hashID}},
		{query: fmt.Sprintf(`DELETE FROM external_mappings.petitioned_mappings_%d WHERE tag_id = ? AND hash_id = ?`, serviceID), args: []any{tagID, hashID}},
		{query: fmt.Sprintf(`INSERT OR IGNORE INTO external_mappings.current_mappings_%d (tag_id, hash_id) VALUES (?, ?)`, serviceID), args: []any{tagID, hashID}},
	} {
		if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			return fmt.Errorf("update current tag mapping: %w", err)
		}
	}

	return nil
}

func setSpecificCurrentTagCachesTx(ctx context.Context, tx *ImmediateTx, cacheTableNames map[string]struct{}, fileServiceID int64, tagServiceID int64, tagID int64, impliedTagIDs []int64, hashID int64) error {
	currentTableName := specificStorageCurrentTableName(fileServiceID, tagServiceID)
	if _, ok := cacheTableNames[currentTableName]; ok {
		if _, err := tx.ExecContext(
			ctx,
			fmt.Sprintf(`INSERT OR IGNORE INTO external_caches.%s (tag_id, hash_id) VALUES (?, ?)`, currentTableName),
			tagID,
			hashID,
		); err != nil {
			return fmt.Errorf("insert specific current mapping cache row: %w", err)
		}
	}

	pendingTableName := specificStoragePendingTableName(fileServiceID, tagServiceID)
	if _, ok := cacheTableNames[pendingTableName]; ok {
		if _, err := tx.ExecContext(
			ctx,
			fmt.Sprintf(`DELETE FROM external_caches.%s WHERE tag_id = ? AND hash_id = ?`, pendingTableName),
			tagID,
			hashID,
		); err != nil {
			return fmt.Errorf("delete specific pending mapping cache row: %w", err)
		}
	}

	displayCurrentTableName := specificDisplayCurrentTableName(fileServiceID, tagServiceID)
	if _, ok := cacheTableNames[displayCurrentTableName]; ok {
		for _, impliedTagID := range impliedTagIDs {
			if _, err := tx.ExecContext(
				ctx,
				fmt.Sprintf(`INSERT OR IGNORE INTO external_caches.%s (tag_id, hash_id) VALUES (?, ?)`, displayCurrentTableName),
				impliedTagID,
				hashID,
			); err != nil {
				return fmt.Errorf("insert specific display current mapping cache row: %w", err)
			}
		}
	}

	displayPendingTableName := specificDisplayPendingTableName(fileServiceID, tagServiceID)
	if _, ok := cacheTableNames[displayPendingTableName]; ok {
		for _, impliedTagID := range impliedTagIDs {
			if _, err := tx.ExecContext(
				ctx,
				fmt.Sprintf(`DELETE FROM external_caches.%s WHERE tag_id = ? AND hash_id = ?`, displayPendingTableName),
				impliedTagID,
				hashID,
			); err != nil {
				return fmt.Errorf("delete specific display pending mapping cache row: %w", err)
			}
		}
	}

	return nil
}

func lookupSpecificCacheFileServiceIDsTx(ctx context.Context, tx *ImmediateTx, hashIDs []int64, definitions []serviceDefinition, mainTableNames map[string]struct{}) (map[int64][]int64, error) {
	results := map[int64][]int64{}
	if len(hashIDs) == 0 {
		return results, nil
	}

	for _, service := range definitions {
		if !supportsSpecificMappingCache(service.serviceType) {
			continue
		}

		tableName := fmt.Sprintf("current_files_%d", service.id)
		if _, ok := mainTableNames[tableName]; !ok {
			continue
		}

		query := fmt.Sprintf(`SELECT hash_id FROM main.%s WHERE hash_id IN (%s)`, tableName, placeholders(len(hashIDs)))
		rows, err := tx.QueryContext(ctx, query, int64Args(hashIDs)...)
		if err != nil {
			return nil, fmt.Errorf("query current file memberships for %s: %w", tableName, err)
		}

		for rows.Next() {
			var hashID int64
			if err := rows.Scan(&hashID); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan current file membership for %s: %w", tableName, err)
			}
			results[hashID] = append(results[hashID], service.id)
		}

		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("iterate current file memberships for %s: %w", tableName, err)
		}
		rows.Close()
	}

	for hashID, serviceIDs := range results {
		results[hashID] = dedupeInt64s(serviceIDs)
	}

	return results, nil
}

func findServiceDefinitionByKey(definitions []serviceDefinition, serviceKey string) (serviceDefinition, bool) {
	normalized := strings.ToLower(strings.TrimSpace(serviceKey))
	for _, definition := range definitions {
		if definition.serviceKey == normalized {
			return definition, true
		}
	}

	return serviceDefinition{}, false
}
