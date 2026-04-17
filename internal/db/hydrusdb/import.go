package hydrusdb

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/official-elinas/hydrus-go/internal/core/services"
)

// PreparedLocalImport is the minimal DB write-set for a caller-prepared local
// file import. This slice assumes the caller already knows the file hash,
// MIME, and other basic media metadata.
type PreparedLocalImport struct {
	HashHex             string
	Size                int64
	Mime                int
	Width               *int64
	Height              *int64
	Duration            *int64
	NumFrames           *int64
	HasAudio            *bool
	NumWords            *int64
	ImportedAtMS        int64
	FileModifiedAtMS    *int64
	LocalFileServiceKey string
}

// PreparedLocalImportResult describes the DB-side result of recording one
// prepared local import.
type PreparedLocalImportResult struct {
	FileID          int64
	AlreadyImported bool
}

type normalizedPreparedLocalImport struct {
	hashHex             string
	hashBytes           []byte
	size                int64
	mime                int64
	width               sql.NullInt64
	height              sql.NullInt64
	duration            sql.NullInt64
	numFrames           sql.NullInt64
	hasAudio            sql.NullInt64
	numWords            sql.NullInt64
	importedAtMS        int64
	fileModifiedAtMS    sql.NullInt64
	localFileServiceKey string
}

type preparedLocalImportPlan struct {
	currentMemberships []preparedCurrentMembershipPlan
}

type preparedCurrentMembershipPlan struct {
	service             serviceDefinition
	tableName           string
	includeImportedAtMS bool
}

type rowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// RecordPreparedLocalImport writes the minimal Hydrus DB state needed for one
// prepared local file import. It intentionally stays internal-only and does not
// place files in managed storage; callers should compose storage placement
// separately.
func (b *Bundle) RecordPreparedLocalImport(
	ctx context.Context,
	prepared PreparedLocalImport,
) (PreparedLocalImportResult, error) {
	normalized, err := normalizePreparedLocalImport(prepared)
	if err != nil {
		return PreparedLocalImportResult{}, err
	}

	plan, err := b.resolvePreparedLocalImportPlan(ctx, normalized.localFileServiceKey)
	if err != nil {
		return PreparedLocalImportResult{}, err
	}

	result := PreparedLocalImportResult{}
	err = b.WithImmediateTx(ctx, func(tx *ImmediateTx) error {
		hashID, hashExists, err := lookupHashIDByHash(ctx, tx, normalized.hashBytes)
		if err != nil {
			return err
		}

		if hashExists {
			record, ok, err := lookupBasicRecordByHashID(ctx, tx, hashID)
			if err != nil {
				return err
			}

			if ok {
				matches, err := b.preparedLocalImportMatchesExisting(
					ctx,
					hashID,
					normalized,
					plan,
					record,
				)
				if err != nil {
					return err
				}

				if !matches {
					return fmt.Errorf(
						"prepared import conflicts with existing file_id %d",
						hashID,
					)
				}

				result = PreparedLocalImportResult{
					FileID:          hashID,
					AlreadyImported: true,
				}
				return nil
			}
		} else {
			if _, err := tx.ExecContext(
				ctx,
				`INSERT INTO external_master.hashes (hash) VALUES (?)`,
				normalized.hashBytes,
			); err != nil {
				return fmt.Errorf("insert external_master.hashes row: %w", err)
			}

			hashID, hashExists, err = lookupHashIDByHash(ctx, tx, normalized.hashBytes)
			if err != nil {
				return err
			}

			if !hashExists {
				return errors.New("inserted hash row was not readable inside transaction")
			}
		}

		if err := insertPreparedFilesInfo(ctx, tx, hashID, normalized); err != nil {
			return err
		}

		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO main.file_inbox (hash_id) VALUES (?)`,
			hashID,
		); err != nil {
			return fmt.Errorf("insert file_inbox row: %w", err)
		}

		if normalized.fileModifiedAtMS.Valid {
			if _, err := tx.ExecContext(
				ctx,
				`INSERT INTO main.file_modified_timestamps (hash_id, file_modified_timestamp_ms) VALUES (?, ?)`,
				hashID,
				normalized.fileModifiedAtMS.Int64,
			); err != nil {
				return fmt.Errorf("insert file_modified_timestamps row: %w", err)
			}
		}

		for _, membership := range plan.currentMemberships {
			if err := insertPreparedCurrentMembership(
				ctx,
				tx,
				hashID,
				membership,
				normalized.importedAtMS,
			); err != nil {
				return err
			}
		}

		result = PreparedLocalImportResult{FileID: hashID}
		return nil
	})
	if err != nil {
		return PreparedLocalImportResult{}, err
	}

	return result, nil
}

// LookupImportedFileIDByHash reports whether a hash already has a concrete file
// row in main.files_info. This is narrower than checking external_master.hashes
// alone because the master hash table also stores non-file hash rows.
func (b *Bundle) LookupImportedFileIDByHash(
	ctx context.Context,
	hashHex string,
) (int64, bool, error) {
	_, hashBytes, err := normalizePreparedHash(hashHex)
	if err != nil {
		return 0, false, err
	}

	row := b.conn.QueryRowContext(
		ctx,
		`SELECT fi.hash_id
		FROM main.files_info fi
		JOIN external_master.hashes h USING (hash_id)
		WHERE h.hash = ?`,
		hashBytes,
	)

	var fileID int64
	if err := row.Scan(&fileID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}

		return 0, false, fmt.Errorf("query imported file by hash: %w", err)
	}

	return fileID, true, nil
}

func normalizePreparedLocalImport(
	prepared PreparedLocalImport,
) (normalizedPreparedLocalImport, error) {
	hashHex, hashBytes, err := normalizePreparedHash(prepared.HashHex)
	if err != nil {
		return normalizedPreparedLocalImport{}, err
	}

	if prepared.Size < 0 {
		return normalizedPreparedLocalImport{}, fmt.Errorf("prepared file size must be non-negative")
	}

	if prepared.ImportedAtMS <= 0 {
		return normalizedPreparedLocalImport{}, fmt.Errorf("prepared import timestamp must be greater than zero")
	}

	width, err := normalizeOptionalNonNegativeInt64("width", prepared.Width)
	if err != nil {
		return normalizedPreparedLocalImport{}, err
	}

	height, err := normalizeOptionalNonNegativeInt64("height", prepared.Height)
	if err != nil {
		return normalizedPreparedLocalImport{}, err
	}

	duration, err := normalizeOptionalNonNegativeInt64("duration", prepared.Duration)
	if err != nil {
		return normalizedPreparedLocalImport{}, err
	}

	numFrames, err := normalizeOptionalNonNegativeInt64("num_frames", prepared.NumFrames)
	if err != nil {
		return normalizedPreparedLocalImport{}, err
	}

	numWords, err := normalizeOptionalNonNegativeInt64("num_words", prepared.NumWords)
	if err != nil {
		return normalizedPreparedLocalImport{}, err
	}

	fileModifiedAtMS, err := normalizeOptionalPositiveInt64(
		"file modified timestamp",
		prepared.FileModifiedAtMS,
	)
	if err != nil {
		return normalizedPreparedLocalImport{}, err
	}

	localFileServiceKey, err := normalizeOptionalHexKey(
		"local file service key",
		prepared.LocalFileServiceKey,
	)
	if err != nil {
		return normalizedPreparedLocalImport{}, err
	}

	return normalizedPreparedLocalImport{
		hashHex:             hashHex,
		hashBytes:           hashBytes,
		size:                prepared.Size,
		mime:                int64(prepared.Mime),
		width:               width,
		height:              height,
		duration:            duration,
		numFrames:           numFrames,
		hasAudio:            nullableBoolToInt64(prepared.HasAudio),
		numWords:            numWords,
		importedAtMS:        prepared.ImportedAtMS,
		fileModifiedAtMS:    fileModifiedAtMS,
		localFileServiceKey: localFileServiceKey,
	}, nil
}

func normalizePreparedHash(hashHex string) (string, []byte, error) {
	normalized := strings.ToLower(strings.TrimSpace(hashHex))
	if len(normalized) != 64 {
		return "", nil, fmt.Errorf("prepared file hash must be 64 hex characters")
	}

	decoded, err := hex.DecodeString(normalized)
	if err != nil {
		return "", nil, fmt.Errorf("decode prepared file hash: %w", err)
	}

	return normalized, decoded, nil
}

func normalizeOptionalHexKey(label string, value string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "" {
		return "", nil
	}

	if _, err := hex.DecodeString(normalized); err != nil {
		return "", fmt.Errorf("decode %s: %w", label, err)
	}

	return normalized, nil
}

func normalizeOptionalNonNegativeInt64(
	label string,
	value *int64,
) (sql.NullInt64, error) {
	if value == nil {
		return sql.NullInt64{}, nil
	}

	if *value < 0 {
		return sql.NullInt64{}, fmt.Errorf("prepared %s must be non-negative", label)
	}

	return sql.NullInt64{Int64: *value, Valid: true}, nil
}

func normalizeOptionalPositiveInt64(
	label string,
	value *int64,
) (sql.NullInt64, error) {
	if value == nil {
		return sql.NullInt64{}, nil
	}

	if *value <= 0 {
		return sql.NullInt64{}, fmt.Errorf("prepared %s must be greater than zero", label)
	}

	return sql.NullInt64{Int64: *value, Valid: true}, nil
}

func nullableBoolToInt64(value *bool) sql.NullInt64 {
	if value == nil {
		return sql.NullInt64{}
	}

	if *value {
		return sql.NullInt64{Int64: 1, Valid: true}
	}

	return sql.NullInt64{Int64: 0, Valid: true}
}

func (b *Bundle) resolvePreparedLocalImportPlan(
	ctx context.Context,
	localFileServiceKey string,
) (preparedLocalImportPlan, error) {
	definitions, err := b.lookupAllServiceDefinitions(ctx)
	if err != nil {
		return preparedLocalImportPlan{}, err
	}

	tableNames, err := b.lookupMainTableNames(ctx)
	if err != nil {
		return preparedLocalImportPlan{}, err
	}

	localFileService, err := resolveTargetLocalFileService(
		definitions,
		localFileServiceKey,
	)
	if err != nil {
		return preparedLocalImportPlan{}, err
	}

	hydrusLocalStorage, ok, err := findUniqueServiceByType(
		definitions,
		services.TypeHydrusLocalFileStorage,
	)
	if err != nil {
		return preparedLocalImportPlan{}, err
	}
	if !ok {
		return preparedLocalImportPlan{}, errors.New(
			"required hydrus local file storage service is missing",
		)
	}

	plan := preparedLocalImportPlan{}
	if err := appendPreparedCurrentMembership(
		&plan,
		tableNames,
		localFileService,
		true,
		true,
	); err != nil {
		return preparedLocalImportPlan{}, err
	}

	if err := appendPreparedCurrentMembership(
		&plan,
		tableNames,
		hydrusLocalStorage,
		true,
		true,
	); err != nil {
		return preparedLocalImportPlan{}, err
	}

	if combinedLocal, ok, err := findUniqueServiceByType(
		definitions,
		services.TypeCombinedLocalFileDomains,
	); err != nil {
		return preparedLocalImportPlan{}, err
	} else if ok {
		if err := appendPreparedCurrentMembership(
			&plan,
			tableNames,
			combinedLocal,
			true,
			false,
		); err != nil {
			return preparedLocalImportPlan{}, err
		}
	}

	if combinedFile, ok, err := findUniqueServiceByType(
		definitions,
		services.TypeCombinedFile,
	); err != nil {
		return preparedLocalImportPlan{}, err
	} else if ok {
		if err := appendPreparedCurrentMembership(
			&plan,
			tableNames,
			combinedFile,
			false,
			false,
		); err != nil {
			return preparedLocalImportPlan{}, err
		}
	}

	return plan, nil
}

func resolveTargetLocalFileService(
	definitions []serviceDefinition,
	localFileServiceKey string,
) (serviceDefinition, error) {
	if localFileServiceKey != "" {
		for _, definition := range definitions {
			if definition.serviceKey != localFileServiceKey {
				continue
			}

			if definition.serviceType != services.TypeLocalFileDomain {
				return serviceDefinition{}, fmt.Errorf(
					"service %q is not a local file domain",
					localFileServiceKey,
				)
			}

			return definition, nil
		}

		return serviceDefinition{}, fmt.Errorf(
			"local file service key %q was not found",
			localFileServiceKey,
		)
	}

	localFileServices := []serviceDefinition{}
	for _, definition := range definitions {
		if definition.serviceType == services.TypeLocalFileDomain {
			localFileServices = append(localFileServices, definition)
		}
	}

	if len(localFileServices) == 0 {
		return serviceDefinition{}, errors.New("no local file domain service is available")
	}

	if len(localFileServices) > 1 {
		return serviceDefinition{}, errors.New(
			"multiple local file domain services are available; an explicit local file service key is required",
		)
	}

	return localFileServices[0], nil
}

func findUniqueServiceByType(
	definitions []serviceDefinition,
	serviceType services.Type,
) (serviceDefinition, bool, error) {
	var matched serviceDefinition
	seen := 0

	for _, definition := range definitions {
		if definition.serviceType != serviceType {
			continue
		}

		matched = definition
		seen++
	}

	if seen == 0 {
		return serviceDefinition{}, false, nil
	}

	if seen > 1 {
		return serviceDefinition{}, false, fmt.Errorf(
			"multiple services of type %d are available",
			serviceType,
		)
	}

	return matched, true, nil
}

func appendPreparedCurrentMembership(
	plan *preparedLocalImportPlan,
	tableNames map[string]struct{},
	service serviceDefinition,
	includeImportedAtMS bool,
	required bool,
) error {
	tableName := fmt.Sprintf("current_files_%d", service.id)
	if _, ok := tableNames[tableName]; !ok {
		if required {
			return fmt.Errorf(
				"required current membership table %q is missing",
				tableName,
			)
		}

		return nil
	}

	plan.currentMemberships = append(
		plan.currentMemberships,
		preparedCurrentMembershipPlan{
			service:             service,
			tableName:           tableName,
			includeImportedAtMS: includeImportedAtMS,
		},
	)

	return nil
}

func lookupHashIDByHash(
	ctx context.Context,
	q rowQuerier,
	hashBytes []byte,
) (int64, bool, error) {
	row := q.QueryRowContext(
		ctx,
		`SELECT hash_id FROM external_master.hashes WHERE hash = ?`,
		hashBytes,
	)

	var hashID int64
	if err := row.Scan(&hashID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}

		return 0, false, fmt.Errorf("query hash row: %w", err)
	}

	return hashID, true, nil
}

func lookupBasicRecordByHashID(
	ctx context.Context,
	q rowQuerier,
	hashID int64,
) (basicRecord, bool, error) {
	row := q.QueryRowContext(
		ctx,
		`SELECT fi.hash_id,
			lower(hex(h.hash)),
			fi.size,
			fi.mime,
			fi.width,
			fi.height,
			fi.duration,
			fi.num_frames,
			fi.has_audio,
			fi.num_words
		FROM main.files_info fi
		JOIN external_master.hashes h USING (hash_id)
		WHERE fi.hash_id = ?`,
		hashID,
	)

	var record basicRecord
	if err := row.Scan(
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
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return basicRecord{}, false, nil
		}

		return basicRecord{}, false, fmt.Errorf("query files_info row: %w", err)
	}

	return record, true, nil
}

func insertPreparedFilesInfo(
	ctx context.Context,
	tx *ImmediateTx,
	hashID int64,
	prepared normalizedPreparedLocalImport,
) error {
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO main.files_info (
			hash_id,
			size,
			mime,
			width,
			height,
			duration,
			num_frames,
			has_audio,
			num_words
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		hashID,
		prepared.size,
		prepared.mime,
		nullableInt64Arg(prepared.width),
		nullableInt64Arg(prepared.height),
		nullableInt64Arg(prepared.duration),
		nullableInt64Arg(prepared.numFrames),
		nullableInt64Arg(prepared.hasAudio),
		nullableInt64Arg(prepared.numWords),
	); err != nil {
		return fmt.Errorf("insert files_info row: %w", err)
	}

	return nil
}

func insertPreparedCurrentMembership(
	ctx context.Context,
	tx *ImmediateTx,
	hashID int64,
	membership preparedCurrentMembershipPlan,
	importedAtMS int64,
) error {
	var timestamp any
	if membership.includeImportedAtMS {
		timestamp = importedAtMS
	}

	query := fmt.Sprintf(
		`INSERT INTO main.%s (hash_id, timestamp_ms) VALUES (?, ?)`,
		membership.tableName,
	)
	if _, err := tx.ExecContext(ctx, query, hashID, timestamp); err != nil {
		return fmt.Errorf("insert %s row: %w", membership.tableName, err)
	}

	return nil
}

func (b *Bundle) preparedLocalImportMatchesExisting(
	ctx context.Context,
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
		b.conn,
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

	currentByHashID, _, err := b.lookupFileServiceMemberships(
		ctx,
		[]int64{hashID},
	)
	if err != nil {
		return false, err
	}

	if !currentMembershipsMatchPlan(
		currentByHashID[hashID],
		plan,
	) {
		return false, nil
	}

	return true, nil
}

func basicRecordMatchesPreparedImport(
	record basicRecord,
	prepared normalizedPreparedLocalImport,
) bool {
	if record.hash != prepared.hashHex {
		return false
	}

	if record.size != prepared.size || record.mime != prepared.mime {
		return false
	}

	return nullInt64sEqual(record.width, prepared.width) &&
		nullInt64sEqual(record.height, prepared.height) &&
		nullInt64sEqual(record.duration, prepared.duration) &&
		nullInt64sEqual(record.numFrames, prepared.numFrames) &&
		nullInt64sEqual(record.hasAudio, prepared.hasAudio) &&
		nullInt64sEqual(record.numWords, prepared.numWords)
}

func lookupNullableInt64(
	ctx context.Context,
	q rowQuerier,
	query string,
	args ...any,
) (sql.NullInt64, bool, error) {
	row := q.QueryRowContext(ctx, query, args...)

	var value sql.NullInt64
	if err := row.Scan(&value); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return sql.NullInt64{}, false, nil
		}

		return sql.NullInt64{}, false, fmt.Errorf("query nullable int64 row: %w", err)
	}

	return value, true, nil
}

func fileModifiedRowMatches(
	actual sql.NullInt64,
	exists bool,
	expected sql.NullInt64,
) bool {
	if !expected.Valid {
		return !exists
	}

	if !exists {
		return false
	}

	return nullInt64sEqual(actual, expected)
}

func currentMembershipsMatchPlan(
	actual []currentFileServiceMembership,
	plan preparedLocalImportPlan,
) bool {
	expectedByServiceID := map[int64]preparedCurrentMembershipPlan{}
	for _, membership := range plan.currentMemberships {
		expectedByServiceID[membership.service.id] = membership
	}
	matchedServiceIDs := map[int64]struct{}{}

	for _, membership := range actual {
		expectedMembership, ok := expectedByServiceID[membership.service.id]
		if !ok {
			continue
		}

		if !expectedMembership.matchesImportedTimestamp(membership.importedTimestampMS) {
			return false
		}

		matchedServiceIDs[membership.service.id] = struct{}{}
	}

	return len(matchedServiceIDs) == len(expectedByServiceID)
}

func (m preparedCurrentMembershipPlan) matchesImportedTimestamp(actual sql.NullInt64) bool {
	if m.includeImportedAtMS {
		return actual.Valid
	}

	return !actual.Valid
}

func nullInt64sEqual(left sql.NullInt64, right sql.NullInt64) bool {
	if left.Valid != right.Valid {
		return false
	}

	if !left.Valid {
		return true
	}

	return left.Int64 == right.Int64
}

func nullableInt64Arg(value sql.NullInt64) any {
	if !value.Valid {
		return nil
	}

	return value.Int64
}
