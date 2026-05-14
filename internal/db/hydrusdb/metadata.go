package hydrusdb

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"

	"github.com/official-elinas/hydrus-go/internal/core/filemetadata"
	"github.com/official-elinas/hydrus-go/internal/core/mimes"
)

// GetMetadata returns the currently implemented metadata modes, including
// writable-bundle create_new_file_ids allocation when available.
func (b *Bundle) GetMetadata(
	ctx context.Context,
	request filemetadata.Request,
) ([]filemetadata.Row, error) {
	if request.CreateNewFileIDs && b.mode == modeReadOnly {
		return nil, &filemetadata.UnsupportedError{
			Message: "create_new_file_ids is not supported in read-only mode",
		}
	}

	conn, err := b.acquireReadConn(ctx)
	if err != nil {
		return nil, err
	}
	defer b.releaseReadConn(conn)

	orderedHashes, hashesToFileIDs, err := b.resolveHashesConn(
		ctx,
		conn,
		request.Hashes,
		request.FileIDs,
		request.CreateNewFileIDs,
	)
	if err != nil {
		return nil, err
	}

	if request.OnlyReturnIdentifiers {
		return identifierRows(orderedHashes, hashesToFileIDs), nil
	}

	if request.OnlyReturnBasicInformation {
		return b.basicMetadataRowsConn(
			ctx,
			conn,
			orderedHashes,
			hashesToFileIDs,
			request.IncludeBlurhash,
		)
	}

	return b.fullMetadataRowsConn(
		ctx,
		conn,
		orderedHashes,
		hashesToFileIDs,
		request.DetailedURLInformation,
		request.IncludeNotes,
		request.IncludeLegacyServiceKeysTags,
		request.IncludeMilliseconds,
	)
}

func (b *Bundle) resolveHashesConn(
	ctx context.Context,
	conn *sql.Conn,
	explicitHashes []string,
	fileIDs []int64,
	createNewFileIDs bool,
) ([]string, map[string]int64, error) {
	return b.resolveHashesWithConn(ctx, conn, explicitHashes, fileIDs, createNewFileIDs)
}

func (b *Bundle) basicMetadataRowsConn(
	ctx context.Context,
	conn *sql.Conn,
	orderedHashes []string,
	hashesToFileIDs map[string]int64,
	includeBlurhash bool,
) ([]filemetadata.Row, error) {
	knownFileIDs := dedupeInt64s(mapValues(hashesToFileIDs))
	recordsByHash, err := b.lookupBasicRecordsConn(ctx, conn, knownFileIDs)
	if err != nil {
		return nil, err
	}

	rows := make([]filemetadata.Row, 0, len(orderedHashes))
	for _, hash := range orderedHashes {
		record, ok := recordsByHash[hash]
		if !ok {
			rows = append(rows, filemetadata.MissingHashRow(hash))
			continue
		}

		rows = append(rows, buildBasicRow(record, includeBlurhash))
	}

	return rows, nil
}

func (b *Bundle) fullMetadataRowsConn(
	ctx context.Context,
	conn *sql.Conn,
	orderedHashes []string,
	hashesToFileIDs map[string]int64,
	includeDetailedURLInformation bool,
	includeNotes bool,
	includeLegacyServiceKeysTags bool,
	includeMilliseconds bool,
) ([]filemetadata.Row, error) {
	return b.fullMetadataRowsWithConn(
		ctx,
		conn,
		orderedHashes,
		hashesToFileIDs,
		includeDetailedURLInformation,
		includeNotes,
		includeLegacyServiceKeysTags,
		includeMilliseconds,
	)
}

func (b *Bundle) resolveHashesWithConn(
	ctx context.Context,
	conn *sql.Conn,
	explicitHashes []string,
	fileIDs []int64,
	createNewFileIDs bool,
) ([]string, map[string]int64, error) {
	orderedHashes := make([]string, 0, len(explicitHashes)+len(fileIDs))
	for _, hash := range explicitHashes {
		normalized, _, err := normalizePreparedHash(hash)
		if err != nil {
			return nil, nil, fmt.Errorf("normalize metadata hash %q: %w", hash, err)
		}

		orderedHashes = append(orderedHashes, normalized)
	}

	if len(fileIDs) > 0 {
		resolvedHashes, err := b.lookupHashesByFileIDsConn(ctx, conn, fileIDs)
		if err != nil {
			return nil, nil, err
		}

		for _, fileID := range fileIDs {
			orderedHashes = append(orderedHashes, resolvedHashes[fileID])
		}
	}

	orderedHashes = dedupeStrings(orderedHashes)

	var hashesToFileIDs map[string]int64
	var err error
	if createNewFileIDs && len(explicitHashes) > 0 {
		hashesToFileIDs, err = b.ensureHashIDs(ctx, orderedHashes)
	} else {
		hashesToFileIDs, err = b.lookupKnownHashIDsConn(ctx, conn, orderedHashes)
	}
	if err != nil {
		return nil, nil, err
	}

	return orderedHashes, hashesToFileIDs, nil
}

func (b *Bundle) lookupHashesByFileIDsConn(
	ctx context.Context,
	conn *sql.Conn,
	fileIDs []int64,
) (map[int64]string, error) {
	args := make([]any, 0, len(fileIDs))
	for _, fileID := range fileIDs {
		args = append(args, fileID)
	}

	query := fmt.Sprintf(
		`SELECT hash_id, lower(hex(hash))
		FROM external_master.hashes
		WHERE hash_id IN (%s)`,
		placeholders(len(fileIDs)),
	)

	rows, err := conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query file IDs to hashes: %w", err)
	}
	defer rows.Close()

	resolved := map[int64]string{}
	for rows.Next() {
		var (
			fileID int64
			hash   string
		)

		if err := rows.Scan(&fileID, &hash); err != nil {
			return nil, fmt.Errorf("scan file ID hash row: %w", err)
		}

		resolved[fileID] = hash
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate file ID hash rows: %w", err)
	}

	missing := []int64{}
	for _, fileID := range fileIDs {
		if _, ok := resolved[fileID]; !ok {
			missing = append(missing, fileID)
		}
	}

	if len(missing) > 0 {
		return nil, &filemetadata.NotFoundError{
			Message: fmt.Sprintf("file_id not found: %v", missing),
		}
	}

	return resolved, nil
}

func (b *Bundle) lookupKnownHashIDsConn(
	ctx context.Context,
	conn *sql.Conn,
	hashes []string,
) (map[string]int64, error) {
	if len(hashes) == 0 {
		return map[string]int64{}, nil
	}

	args := make([]any, 0, len(hashes))
	for _, hash := range hashes {
		decoded, err := hex.DecodeString(hash)
		if err != nil {
			return nil, fmt.Errorf("decode hash %q: %w", hash, err)
		}

		args = append(args, decoded)
	}

	query := fmt.Sprintf(
		`SELECT hash_id, lower(hex(hash))
		FROM external_master.hashes
		WHERE hash IN (%s)`,
		placeholders(len(hashes)),
	)

	rows, err := conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query hashes to file IDs: %w", err)
	}
	defer rows.Close()

	resolved := map[string]int64{}
	for rows.Next() {
		var (
			fileID int64
			hash   string
		)

		if err := rows.Scan(&fileID, &hash); err != nil {
			return nil, fmt.Errorf("scan hash file ID row: %w", err)
		}

		resolved[hash] = fileID
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate hash file ID rows: %w", err)
	}

	return resolved, nil
}

func (b *Bundle) lookupBasicRecordsConn(
	ctx context.Context,
	conn *sql.Conn,
	fileIDs []int64,
) (map[string]basicRecord, error) {
	if len(fileIDs) == 0 {
		return map[string]basicRecord{}, nil
	}

	args := make([]any, 0, len(fileIDs))
	for _, fileID := range fileIDs {
		args = append(args, fileID)
	}

	query := fmt.Sprintf(
		`SELECT fi.hash_id,
			lower(hex(h.hash)),
			fi.size,
			fi.mime,
			fi.width,
			fi.height,
			fi.duration,
			fi.num_frames,
			fi.has_audio,
			fi.num_words,
			fft.forced_mime,
			bh.blurhash
		FROM main.files_info fi
		JOIN external_master.hashes h USING (hash_id)
		LEFT JOIN main.files_info_forced_filetypes fft USING (hash_id)
		LEFT JOIN external_master.blurhashes bh USING (hash_id)
		WHERE fi.hash_id IN (%s)`,
		placeholders(len(fileIDs)),
	)

	rows, err := conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query basic file metadata: %w", err)
	}
	defer rows.Close()

	records := map[string]basicRecord{}
	for rows.Next() {
		var record basicRecord
		if err := rows.Scan(
			&record.hashID,
			&record.hash,
			&record.size,
			&record.mime,
			&record.width,
			&record.height,
			&record.duration,
			&record.numFrames,
			&record.hasAudio,
			&record.numWords,
			&record.forcedMime,
			&record.blurhash,
		); err != nil {
			return nil, fmt.Errorf("scan basic file metadata: %w", err)
		}

		records[record.hash] = record
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate basic file metadata: %w", err)
	}

	return records, nil
}

func (b *Bundle) lookupHashesByFileIDs(
	ctx context.Context,
	fileIDs []int64,
) (map[int64]string, error) {
	args := make([]any, 0, len(fileIDs))
	for _, fileID := range fileIDs {
		args = append(args, fileID)
	}

	query := fmt.Sprintf(
		`SELECT hash_id, lower(hex(hash))
		FROM external_master.hashes
		WHERE hash_id IN (%s)`,
		placeholders(len(fileIDs)),
	)

	rows, err := b.conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query file IDs to hashes: %w", err)
	}
	defer rows.Close()

	resolved := map[int64]string{}
	for rows.Next() {
		var (
			fileID int64
			hash   string
		)

		if err := rows.Scan(&fileID, &hash); err != nil {
			return nil, fmt.Errorf("scan file ID hash row: %w", err)
		}

		resolved[fileID] = hash
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate file ID hash rows: %w", err)
	}

	missing := []int64{}
	for _, fileID := range fileIDs {
		if _, ok := resolved[fileID]; !ok {
			missing = append(missing, fileID)
		}
	}

	if len(missing) > 0 {
		return nil, &filemetadata.NotFoundError{
			Message: fmt.Sprintf("file_id not found: %v", missing),
		}
	}

	return resolved, nil
}

func (b *Bundle) lookupKnownHashIDs(
	ctx context.Context,
	hashes []string,
) (map[string]int64, error) {
	if len(hashes) == 0 {
		return map[string]int64{}, nil
	}

	args := make([]any, 0, len(hashes))
	for _, hash := range hashes {
		decoded, err := hex.DecodeString(hash)
		if err != nil {
			return nil, fmt.Errorf("decode hash %q: %w", hash, err)
		}

		args = append(args, decoded)
	}

	query := fmt.Sprintf(
		`SELECT hash_id, lower(hex(hash))
		FROM external_master.hashes
		WHERE hash IN (%s)`,
		placeholders(len(hashes)),
	)

	rows, err := b.conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query hashes to file IDs: %w", err)
	}
	defer rows.Close()

	resolved := map[string]int64{}
	for rows.Next() {
		var (
			fileID int64
			hash   string
		)

		if err := rows.Scan(&fileID, &hash); err != nil {
			return nil, fmt.Errorf("scan hash file ID row: %w", err)
		}

		resolved[hash] = fileID
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate hash file ID rows: %w", err)
	}

	return resolved, nil
}

func identifierRows(
	orderedHashes []string,
	hashesToFileIDs map[string]int64,
) []filemetadata.Row {
	rows := make([]filemetadata.Row, 0, len(orderedHashes))

	for _, hash := range orderedHashes {
		if fileID, ok := hashesToFileIDs[hash]; ok {
			rows = append(rows, filemetadata.Row{
				"file_id": fileID,
				"hash":    hash,
			})
			continue
		}

		rows = append(rows, filemetadata.MissingHashRow(hash))
	}

	return rows
}

type basicRecord struct {
	hashID     int64
	hash       string
	size       int64
	mime       int64
	width      sql.NullInt64
	height     sql.NullInt64
	duration   sql.NullInt64
	numFrames  sql.NullInt64
	hasAudio   sql.NullInt64
	numWords   sql.NullInt64
	forcedMime sql.NullInt64
	blurhash   sql.NullString
}

func (b *Bundle) basicMetadataRows(
	ctx context.Context,
	orderedHashes []string,
	hashesToFileIDs map[string]int64,
	includeBlurhash bool,
) ([]filemetadata.Row, error) {
	knownFileIDs := dedupeInt64s(mapValues(hashesToFileIDs))
	recordsByHash, err := b.lookupBasicRecords(ctx, knownFileIDs)
	if err != nil {
		return nil, err
	}

	rows := make([]filemetadata.Row, 0, len(orderedHashes))
	for _, hash := range orderedHashes {
		record, ok := recordsByHash[hash]
		if !ok {
			rows = append(rows, filemetadata.MissingHashRow(hash))
			continue
		}

		rows = append(rows, buildBasicRow(record, includeBlurhash))
	}

	return rows, nil
}

func (b *Bundle) lookupBasicRecords(
	ctx context.Context,
	fileIDs []int64,
) (map[string]basicRecord, error) {
	if len(fileIDs) == 0 {
		return map[string]basicRecord{}, nil
	}

	args := make([]any, 0, len(fileIDs))
	for _, fileID := range fileIDs {
		args = append(args, fileID)
	}

	query := fmt.Sprintf(
		`SELECT fi.hash_id,
			lower(hex(h.hash)),
			fi.size,
			fi.mime,
			fi.width,
			fi.height,
			fi.duration,
			fi.num_frames,
			fi.has_audio,
			fi.num_words,
			fft.forced_mime,
			bh.blurhash
		FROM main.files_info fi
		JOIN external_master.hashes h USING (hash_id)
		LEFT JOIN main.files_info_forced_filetypes fft USING (hash_id)
		LEFT JOIN external_master.blurhashes bh USING (hash_id)
		WHERE fi.hash_id IN (%s)`,
		placeholders(len(fileIDs)),
	)

	rows, err := b.conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query basic file metadata: %w", err)
	}
	defer rows.Close()

	records := map[string]basicRecord{}
	for rows.Next() {
		var record basicRecord
		if err := rows.Scan(
			&record.hashID,
			&record.hash,
			&record.size,
			&record.mime,
			&record.width,
			&record.height,
			&record.duration,
			&record.numFrames,
			&record.hasAudio,
			&record.numWords,
			&record.forcedMime,
			&record.blurhash,
		); err != nil {
			return nil, fmt.Errorf("scan basic file metadata: %w", err)
		}

		records[record.hash] = record
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate basic file metadata: %w", err)
	}

	return records, nil
}

func buildBasicRow(record basicRecord, includeBlurhash bool) filemetadata.Row {
	effectiveMime := int(record.mime)
	filetypeForced := false
	if record.forcedMime.Valid {
		effectiveMime = int(record.forcedMime.Int64)
		filetypeForced = true
	}

	info := mimes.Lookup(effectiveMime)
	row := filemetadata.Row{
		"file_id":         record.hashID,
		"hash":            record.hash,
		"size":            record.size,
		"mime":            info.Mimetype,
		"filetype_human":  info.Human,
		"filetype_enum":   effectiveMime,
		"ext":             info.Ext,
		"width":           nullableInt64Value(record.width),
		"height":          nullableInt64Value(record.height),
		"duration":        nullableInt64Value(record.duration),
		"num_frames":      nullableInt64Value(record.numFrames),
		"num_words":       nullableInt64Value(record.numWords),
		"has_audio":       nullableBoolValue(record.hasAudio),
		"filetype_forced": filetypeForced,
	}

	if filetypeForced {
		row["original_mime"] = mimes.Lookup(int(record.mime)).Mimetype
	}

	if includeBlurhash {
		row["blurhash"] = nullableStringValue(record.blurhash)
	}

	return row
}

func nullableInt64Value(value sql.NullInt64) any {
	if !value.Valid {
		return nil
	}

	return value.Int64
}

func nullableBoolValue(value sql.NullInt64) any {
	if !value.Valid {
		return nil
	}

	return value.Int64 != 0
}

func nullableStringValue(value sql.NullString) any {
	if !value.Valid {
		return nil
	}

	return value.String
}

func dedupeStrings(values []string) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(values))

	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}

		seen[value] = struct{}{}
		result = append(result, value)
	}

	return result
}

func dedupeInt64s(values []int64) []int64 {
	seen := map[int64]struct{}{}
	result := make([]int64, 0, len(values))

	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}

		seen[value] = struct{}{}
		result = append(result, value)
	}

	return result
}

func mapValues(values map[string]int64) []int64 {
	result := make([]int64, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}

	return result
}
