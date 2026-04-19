package hydrusdb

import (
	"context"
	"database/sql"
	"fmt"
	"sort"

	"github.com/official-elinas/hydrus-go/internal/core/filemetadata"
	"github.com/official-elinas/hydrus-go/internal/core/services"
)

type serviceDefinition struct {
	id          int64
	serviceKey  string
	serviceType services.Type
	name        string
}

type currentFileServiceMembership struct {
	service             serviceDefinition
	importedTimestampMS sql.NullInt64
}

type deletedFileServiceMembership struct {
	service                   serviceDefinition
	deletedTimestampMS        sql.NullInt64
	originalImportedTimestamp sql.NullInt64
}

type domainModifiedTimestamp struct {
	domain      string
	timestampMS int64
}

func (b *Bundle) fullMetadataRows(
	ctx context.Context,
	orderedHashes []string,
	hashesToFileIDs map[string]int64,
	includeLegacyServiceKeysTags bool,
	includeMilliseconds bool,
) ([]filemetadata.Row, error) {
	knownFileIDs := dedupeInt64s(mapValues(hashesToFileIDs))
	basicRecords, err := b.lookupBasicRecords(ctx, knownFileIDs)
	if err != nil {
		return nil, err
	}

	pixelHashes, err := b.lookupPixelHashes(ctx, knownFileIDs)
	if err != nil {
		return nil, err
	}

	transparencyHashIDs, err := b.lookupHashIDSet(
		ctx,
		knownFileIDs,
		"main.has_transparency",
		"has_transparency",
	)
	if err != nil {
		return nil, err
	}

	exifHashIDs, err := b.lookupHashIDSet(
		ctx,
		knownFileIDs,
		"main.has_exif",
		"has_exif",
	)
	if err != nil {
		return nil, err
	}

	humanReadableHashIDs, err := b.lookupHashIDSet(
		ctx,
		knownFileIDs,
		"main.has_human_readable_embedded_metadata",
		"has_human_readable_embedded_metadata",
	)
	if err != nil {
		return nil, err
	}

	iccProfileHashIDs, err := b.lookupHashIDSet(
		ctx,
		knownFileIDs,
		"main.has_icc_profile",
		"has_icc_profile",
	)
	if err != nil {
		return nil, err
	}

	inboxHashIDs, err := b.lookupHashIDSet(ctx, knownFileIDs, "main.file_inbox", "file_inbox")
	if err != nil {
		return nil, err
	}

	archivedTimestamps, err := b.lookupInt64ByHashID(
		ctx,
		knownFileIDs,
		"main.archive_timestamps",
		"archived_timestamp_ms",
		"archive_timestamps",
	)
	if err != nil {
		return nil, err
	}

	fileModifiedTimestamps, err := b.lookupInt64ByHashID(
		ctx,
		knownFileIDs,
		"main.file_modified_timestamps",
		"file_modified_timestamp_ms",
		"file_modified_timestamps",
	)
	if err != nil {
		return nil, err
	}

	domainModifiedTimestamps, err := b.lookupDomainModifiedTimestamps(ctx, knownFileIDs)
	if err != nil {
		return nil, err
	}

	knownURLs, err := b.lookupKnownURLs(ctx, knownFileIDs)
	if err != nil {
		return nil, err
	}

	currentFileServices, deletedFileServices, err := b.lookupFileServiceMemberships(ctx, knownFileIDs)
	if err != nil {
		return nil, err
	}

	ipfsMultihashes, err := b.lookupIPFSMultihashes(ctx, knownFileIDs)
	if err != nil {
		return nil, err
	}

	tagsByHashID, err := b.lookupFileTags(ctx, knownFileIDs, currentFileServices)
	if err != nil {
		return nil, err
	}

	rows := make([]filemetadata.Row, 0, len(orderedHashes))
	for _, hash := range orderedHashes {
		record, ok := basicRecords[hash]
		if !ok {
			rows = append(rows, filemetadata.MissingHashRow(hash))
			continue
		}

		rows = append(rows, buildFullMetadataRow(
			record,
			includeMilliseconds,
			pixelHashes,
			transparencyHashIDs,
			exifHashIDs,
			humanReadableHashIDs,
			iccProfileHashIDs,
			inboxHashIDs,
			archivedTimestamps,
			fileModifiedTimestamps,
			domainModifiedTimestamps,
			knownURLs,
			currentFileServices,
			deletedFileServices,
			ipfsMultihashes,
			tagsByHashID,
			includeLegacyServiceKeysTags,
		))
	}

	return rows, nil
}

func buildFullMetadataRow(
	record basicRecord,
	includeMilliseconds bool,
	pixelHashes map[int64]string,
	transparencyHashIDs map[int64]struct{},
	exifHashIDs map[int64]struct{},
	humanReadableHashIDs map[int64]struct{},
	iccProfileHashIDs map[int64]struct{},
	inboxHashIDs map[int64]struct{},
	archivedTimestamps map[int64]int64,
	fileModifiedTimestamps map[int64]int64,
	domainModifiedTimestamps map[int64][]domainModifiedTimestamp,
	knownURLs map[int64][]string,
	currentFileServices map[int64][]currentFileServiceMembership,
	deletedFileServices map[int64][]deletedFileServiceMembership,
	ipfsMultihashes map[int64]map[string]string,
	tagsByHashID map[int64]metadataTagsPayload,
	includeLegacyServiceKeysTags bool,
) filemetadata.Row {
	row := buildBasicRow(record, true)
	hashID := record.hashID

	pixelHash, ok := pixelHashes[hashID]
	if ok {
		row["pixel_hash"] = pixelHash
	} else {
		row["pixel_hash"] = nil
	}

	current := currentFileServices[hashID]
	deleted := deletedFileServices[hashID]
	isInbox := containsHashID(inboxHashIDs, hashID)
	isTrashed := hasCurrentServiceType(current, services.TypeLocalFileTrashDomain)

	row["file_services"] = map[string]any{
		"current": buildCurrentFileServicesPayload(current, includeMilliseconds),
		"deleted": buildDeletedFileServicesPayload(deleted, includeMilliseconds),
	}
	row["time_modified"] = aggregateModifiedTimestamp(
		fileModifiedTimestamps,
		domainModifiedTimestamps,
		hashID,
		includeMilliseconds,
	)
	row["time_modified_details"] = buildTimeModifiedDetails(
		fileModifiedTimestamps,
		domainModifiedTimestamps,
		hashID,
		includeMilliseconds,
	)
	row["is_inbox"] = isInbox
	row["is_local"] = hasCurrentServiceType(current, services.TypeHydrusLocalFileStorage)
	row["is_trashed"] = isTrashed
	row["is_deleted"] = isTrashed || hasDeletedServiceType(deleted, services.TypeCombinedLocalFileDomains)
	row["has_transparency"] = containsHashID(transparencyHashIDs, hashID)
	row["has_exif"] = containsHashID(exifHashIDs, hashID)
	row["has_human_readable_embedded_metadata"] = containsHashID(humanReadableHashIDs, hashID)
	row["has_icc_profile"] = containsHashID(iccProfileHashIDs, hashID)
	row["known_urls"] = cloneStringSlice(knownURLs[hashID])
	row["ipfs_multihashes"] = cloneStringMap(ipfsMultihashes[hashID])

	tagsPayload, ok := tagsByHashID[hashID]
	if !ok {
		tagsPayload = metadataTagsPayload{
			tags:          map[string]map[string]any{},
			legacyStorage: map[string]map[string][]string{},
			legacyDisplay: map[string]map[string][]string{},
		}
	}

	row["tags"] = tagsPayload.tags
	if includeLegacyServiceKeysTags {
		row["service_keys_to_statuses_to_tags"] = tagsPayload.legacyStorage
		row["service_keys_to_statuses_to_display_tags"] = tagsPayload.legacyDisplay
	}

	if !isInbox {
		if archivedTimestampMS, ok := archivedTimestamps[hashID]; ok {
			row["time_archived"] = hydrusTimestampMS(archivedTimestampMS, includeMilliseconds)
		}
	}

	return row
}

func (b *Bundle) lookupPixelHashes(
	ctx context.Context,
	fileIDs []int64,
) (map[int64]string, error) {
	if len(fileIDs) == 0 {
		return map[int64]string{}, nil
	}

	query := fmt.Sprintf(
		`SELECT phm.hash_id, lower(hex(h.hash))
		FROM main.pixel_hash_map phm
		JOIN external_master.hashes h ON phm.pixel_hash_id = h.hash_id
		WHERE phm.hash_id IN (%s)`,
		placeholders(len(fileIDs)),
	)

	rows, err := b.conn.QueryContext(ctx, query, int64Args(fileIDs)...)
	if err != nil {
		return nil, fmt.Errorf("query pixel hashes: %w", err)
	}
	defer rows.Close()

	pixelHashes := map[int64]string{}
	for rows.Next() {
		var (
			hashID    int64
			pixelHash string
		)

		if err := rows.Scan(&hashID, &pixelHash); err != nil {
			return nil, fmt.Errorf("scan pixel hash row: %w", err)
		}

		if _, ok := pixelHashes[hashID]; !ok {
			pixelHashes[hashID] = pixelHash
		}
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pixel hash rows: %w", err)
	}

	return pixelHashes, nil
}

func (b *Bundle) lookupHashIDSet(
	ctx context.Context,
	fileIDs []int64,
	tableName string,
	label string,
) (map[int64]struct{}, error) {
	if len(fileIDs) == 0 {
		return map[int64]struct{}{}, nil
	}

	query := fmt.Sprintf(
		`SELECT hash_id FROM %s WHERE hash_id IN (%s)`,
		tableName,
		placeholders(len(fileIDs)),
	)

	rows, err := b.conn.QueryContext(ctx, query, int64Args(fileIDs)...)
	if err != nil {
		return nil, fmt.Errorf("query %s rows: %w", label, err)
	}
	defer rows.Close()

	hashIDs := map[int64]struct{}{}
	for rows.Next() {
		var hashID int64
		if err := rows.Scan(&hashID); err != nil {
			return nil, fmt.Errorf("scan %s row: %w", label, err)
		}

		hashIDs[hashID] = struct{}{}
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s rows: %w", label, err)
	}

	return hashIDs, nil
}

func (b *Bundle) lookupInt64ByHashID(
	ctx context.Context,
	fileIDs []int64,
	tableName string,
	columnName string,
	label string,
) (map[int64]int64, error) {
	if len(fileIDs) == 0 {
		return map[int64]int64{}, nil
	}

	query := fmt.Sprintf(
		`SELECT hash_id, %s FROM %s WHERE hash_id IN (%s)`,
		columnName,
		tableName,
		placeholders(len(fileIDs)),
	)

	rows, err := b.conn.QueryContext(ctx, query, int64Args(fileIDs)...)
	if err != nil {
		return nil, fmt.Errorf("query %s rows: %w", label, err)
	}
	defer rows.Close()

	values := map[int64]int64{}
	for rows.Next() {
		var (
			hashID int64
			value  int64
		)

		if err := rows.Scan(&hashID, &value); err != nil {
			return nil, fmt.Errorf("scan %s row: %w", label, err)
		}

		values[hashID] = value
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s rows: %w", label, err)
	}

	return values, nil
}

func (b *Bundle) lookupDomainModifiedTimestamps(
	ctx context.Context,
	fileIDs []int64,
) (map[int64][]domainModifiedTimestamp, error) {
	if len(fileIDs) == 0 {
		return map[int64][]domainModifiedTimestamp{}, nil
	}

	query := fmt.Sprintf(
		`SELECT fdm.hash_id, ud.domain, fdm.file_modified_timestamp_ms
		FROM main.file_domain_modified_timestamps fdm
		JOIN external_master.url_domains ud USING (domain_id)
		WHERE fdm.hash_id IN (%s)`,
		placeholders(len(fileIDs)),
	)

	rows, err := b.conn.QueryContext(ctx, query, int64Args(fileIDs)...)
	if err != nil {
		return nil, fmt.Errorf("query domain modified timestamps: %w", err)
	}
	defer rows.Close()

	results := map[int64][]domainModifiedTimestamp{}
	for rows.Next() {
		var (
			hashID      int64
			domain      string
			timestampMS int64
		)

		if err := rows.Scan(&hashID, &domain, &timestampMS); err != nil {
			return nil, fmt.Errorf("scan domain modified timestamp row: %w", err)
		}

		results[hashID] = append(results[hashID], domainModifiedTimestamp{
			domain:      domain,
			timestampMS: timestampMS,
		})
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate domain modified timestamp rows: %w", err)
	}

	return results, nil
}

func (b *Bundle) lookupKnownURLs(
	ctx context.Context,
	fileIDs []int64,
) (map[int64][]string, error) {
	if len(fileIDs) == 0 {
		return map[int64][]string{}, nil
	}

	query := fmt.Sprintf(
		`SELECT um.hash_id, u.url
		FROM main.url_map um
		JOIN external_master.urls u USING (url_id)
		WHERE um.hash_id IN (%s)`,
		placeholders(len(fileIDs)),
	)

	rows, err := b.conn.QueryContext(ctx, query, int64Args(fileIDs)...)
	if err != nil {
		return nil, fmt.Errorf("query known URLs: %w", err)
	}
	defer rows.Close()

	urlsByHashID := map[int64][]string{}
	for rows.Next() {
		var (
			hashID int64
			url    string
		)

		if err := rows.Scan(&hashID, &url); err != nil {
			return nil, fmt.Errorf("scan known URL row: %w", err)
		}

		urlsByHashID[hashID] = append(urlsByHashID[hashID], url)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate known URL rows: %w", err)
	}

	for hashID := range urlsByHashID {
		sort.Strings(urlsByHashID[hashID])
	}

	return urlsByHashID, nil
}

func (b *Bundle) lookupFileServiceMemberships(
	ctx context.Context,
	fileIDs []int64,
) (
	map[int64][]currentFileServiceMembership,
	map[int64][]deletedFileServiceMembership,
	error,
) {
	currentByHashID := map[int64][]currentFileServiceMembership{}
	deletedByHashID := map[int64][]deletedFileServiceMembership{}
	if len(fileIDs) == 0 {
		return currentByHashID, deletedByHashID, nil
	}

	servicesByID, err := b.lookupAllServiceDefinitions(ctx)
	if err != nil {
		return nil, nil, err
	}

	tableNames, err := b.lookupMainTableNames(ctx)
	if err != nil {
		return nil, nil, err
	}

	for _, service := range servicesByID {
		currentTableName := fmt.Sprintf("current_files_%d", service.id)
		if _, ok := tableNames[currentTableName]; ok {
			memberships, err := b.lookupCurrentFileServiceMemberships(
				ctx,
				fileIDs,
				currentTableName,
				service,
			)
			if err != nil {
				return nil, nil, err
			}

			for hashID, rows := range memberships {
				currentByHashID[hashID] = append(currentByHashID[hashID], rows...)
			}
		}

		deletedTableName := fmt.Sprintf("deleted_files_%d", service.id)
		if _, ok := tableNames[deletedTableName]; ok {
			memberships, err := b.lookupDeletedFileServiceMemberships(
				ctx,
				fileIDs,
				deletedTableName,
				service,
			)
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

func (b *Bundle) lookupCurrentFileServiceMemberships(
	ctx context.Context,
	fileIDs []int64,
	tableName string,
	service serviceDefinition,
) (map[int64][]currentFileServiceMembership, error) {
	query := fmt.Sprintf(
		`SELECT hash_id, timestamp_ms FROM main.%s WHERE hash_id IN (%s)`,
		tableName,
		placeholders(len(fileIDs)),
	)

	rows, err := b.conn.QueryContext(ctx, query, int64Args(fileIDs)...)
	if err != nil {
		return nil, fmt.Errorf("query current file service %s: %w", tableName, err)
	}
	defer rows.Close()

	memberships := map[int64][]currentFileServiceMembership{}
	for rows.Next() {
		var (
			hashID              int64
			importedTimestampMS sql.NullInt64
		)

		if err := rows.Scan(&hashID, &importedTimestampMS); err != nil {
			return nil, fmt.Errorf("scan current file service %s row: %w", tableName, err)
		}

		memberships[hashID] = append(memberships[hashID], currentFileServiceMembership{
			service:             service,
			importedTimestampMS: importedTimestampMS,
		})
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate current file service %s rows: %w", tableName, err)
	}

	return memberships, nil
}

func (b *Bundle) lookupDeletedFileServiceMemberships(
	ctx context.Context,
	fileIDs []int64,
	tableName string,
	service serviceDefinition,
) (map[int64][]deletedFileServiceMembership, error) {
	query := fmt.Sprintf(
		`SELECT hash_id, timestamp_ms, original_timestamp_ms
		FROM main.%s
		WHERE hash_id IN (%s)`,
		tableName,
		placeholders(len(fileIDs)),
	)

	rows, err := b.conn.QueryContext(ctx, query, int64Args(fileIDs)...)
	if err != nil {
		return nil, fmt.Errorf("query deleted file service %s: %w", tableName, err)
	}
	defer rows.Close()

	memberships := map[int64][]deletedFileServiceMembership{}
	for rows.Next() {
		var (
			hashID                    int64
			deletedTimestampMS        sql.NullInt64
			originalImportedTimestamp sql.NullInt64
		)

		if err := rows.Scan(
			&hashID,
			&deletedTimestampMS,
			&originalImportedTimestamp,
		); err != nil {
			return nil, fmt.Errorf("scan deleted file service %s row: %w", tableName, err)
		}

		memberships[hashID] = append(memberships[hashID], deletedFileServiceMembership{
			service:                   service,
			deletedTimestampMS:        deletedTimestampMS,
			originalImportedTimestamp: originalImportedTimestamp,
		})
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate deleted file service %s rows: %w", tableName, err)
	}

	return memberships, nil
}

func (b *Bundle) lookupAllServiceDefinitions(
	ctx context.Context,
) ([]serviceDefinition, error) {
	rows, err := b.conn.QueryContext(
		ctx,
		`SELECT service_id, lower(hex(service_key)), service_type, name
		FROM main.services
		ORDER BY service_id ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("query service definitions: %w", err)
	}
	defer rows.Close()

	definitions := []serviceDefinition{}
	for rows.Next() {
		var (
			serviceID   int64
			serviceKey  string
			serviceType int
			name        string
		)

		if err := rows.Scan(&serviceID, &serviceKey, &serviceType, &name); err != nil {
			return nil, fmt.Errorf("scan service definition row: %w", err)
		}

		definitions = append(definitions, serviceDefinition{
			id:          serviceID,
			serviceKey:  serviceKey,
			serviceType: services.Type(serviceType),
			name:        name,
		})
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate service definition rows: %w", err)
	}

	return definitions, nil
}

func (b *Bundle) lookupMainTableNames(ctx context.Context) (map[string]struct{}, error) {
	return b.lookupSchemaTableNames(ctx, "main")
}

func (b *Bundle) lookupSchemaTableNames(
	ctx context.Context,
	schemaName string,
) (map[string]struct{}, error) {
	switch schemaName {
	case "main", "external_master", "external_caches", "external_mappings":
	default:
		return nil, fmt.Errorf("unsupported sqlite schema name %q", schemaName)
	}

	rows, err := b.conn.QueryContext(
		ctx,
		fmt.Sprintf(`SELECT name FROM %s.sqlite_master WHERE type = 'table'`, schemaName),
	)
	if err != nil {
		return nil, fmt.Errorf("query sqlite table names for schema %s: %w", schemaName, err)
	}
	defer rows.Close()

	tableNames := map[string]struct{}{}
	for rows.Next() {
		var tableName string
		if err := rows.Scan(&tableName); err != nil {
			return nil, fmt.Errorf("scan sqlite table name for schema %s: %w", schemaName, err)
		}

		tableNames[tableName] = struct{}{}
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sqlite table names for schema %s: %w", schemaName, err)
	}

	return tableNames, nil
}

func (b *Bundle) lookupIPFSMultihashes(
	ctx context.Context,
	fileIDs []int64,
) (map[int64]map[string]string, error) {
	if len(fileIDs) == 0 {
		return map[int64]map[string]string{}, nil
	}

	args := int64Args(fileIDs)
	args = append(args, int(services.TypeIPFS))

	query := fmt.Sprintf(
		`SELECT sf.hash_id, lower(hex(s.service_key)), sf.filename
		FROM main.service_filenames sf
		JOIN main.services s USING (service_id)
		WHERE sf.hash_id IN (%s)
		AND s.service_type = ?`,
		placeholders(len(fileIDs)),
	)

	rows, err := b.conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query ipfs multihashes: %w", err)
	}
	defer rows.Close()

	multihashesByHashID := map[int64]map[string]string{}
	for rows.Next() {
		var (
			hashID     int64
			serviceKey string
			multihash  string
		)

		if err := rows.Scan(&hashID, &serviceKey, &multihash); err != nil {
			return nil, fmt.Errorf("scan ipfs multihash row: %w", err)
		}

		if _, ok := multihashesByHashID[hashID]; !ok {
			multihashesByHashID[hashID] = map[string]string{}
		}

		multihashesByHashID[hashID][serviceKey] = multihash
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate ipfs multihash rows: %w", err)
	}

	return multihashesByHashID, nil
}

func buildCurrentFileServicesPayload(
	memberships []currentFileServiceMembership,
	includeMilliseconds bool,
) map[string]map[string]any {
	payload := map[string]map[string]any{}
	for _, membership := range memberships {
		payload[membership.service.serviceKey] = map[string]any{
			"name":        membership.service.name,
			"type":        membership.service.serviceType,
			"type_pretty": services.TypePretty(membership.service.serviceType),
			"time_imported": hydrusNullableTimestampMS(
				membership.importedTimestampMS,
				includeMilliseconds,
			),
		}
	}

	return payload
}

func buildDeletedFileServicesPayload(
	memberships []deletedFileServiceMembership,
	includeMilliseconds bool,
) map[string]map[string]any {
	payload := map[string]map[string]any{}
	for _, membership := range memberships {
		payload[membership.service.serviceKey] = map[string]any{
			"name":        membership.service.name,
			"type":        membership.service.serviceType,
			"type_pretty": services.TypePretty(membership.service.serviceType),
			"time_deleted": hydrusNullableTimestampMS(
				membership.deletedTimestampMS,
				includeMilliseconds,
			),
			"time_imported": hydrusNullableTimestampMS(
				membership.originalImportedTimestamp,
				includeMilliseconds,
			),
		}
	}

	return payload
}

func hasCurrentServiceType(
	memberships []currentFileServiceMembership,
	serviceType services.Type,
) bool {
	for _, membership := range memberships {
		if membership.service.serviceType == serviceType {
			return true
		}
	}

	return false
}

func hasDeletedServiceType(
	memberships []deletedFileServiceMembership,
	serviceType services.Type,
) bool {
	for _, membership := range memberships {
		if membership.service.serviceType == serviceType {
			return true
		}
	}

	return false
}

func aggregateModifiedTimestamp(
	fileModifiedTimestamps map[int64]int64,
	domainModifiedTimestamps map[int64][]domainModifiedTimestamp,
	hashID int64,
	includeMilliseconds bool,
) any {
	timestampsMS := []int64{}
	if timestampMS, ok := fileModifiedTimestamps[hashID]; ok {
		timestampsMS = append(timestampsMS, timestampMS)
	}

	for _, domainTimestamp := range domainModifiedTimestamps[hashID] {
		timestampsMS = append(timestampsMS, domainTimestamp.timestampMS)
	}

	if len(timestampsMS) == 0 {
		return nil
	}

	aggregateTimestampMS := timestampsMS[0]
	for _, timestampMS := range timestampsMS[1:] {
		if timestampMS < aggregateTimestampMS {
			aggregateTimestampMS = timestampMS
		}
	}

	return hydrusTimestampMS(aggregateTimestampMS, includeMilliseconds)
}

func buildTimeModifiedDetails(
	fileModifiedTimestamps map[int64]int64,
	domainModifiedTimestamps map[int64][]domainModifiedTimestamp,
	hashID int64,
	includeMilliseconds bool,
) map[string]any {
	details := map[string]any{}
	if timestampMS, ok := fileModifiedTimestamps[hashID]; ok {
		details["local"] = hydrusTimestampMS(timestampMS, includeMilliseconds)
	}

	for _, domainTimestamp := range domainModifiedTimestamps[hashID] {
		details[domainTimestamp.domain] = hydrusTimestampMS(
			domainTimestamp.timestampMS,
			includeMilliseconds,
		)
	}

	return details
}

func hydrusTimestampMS(timestampMS int64, includeMilliseconds bool) any {
	if includeMilliseconds {
		return float64(timestampMS) / 1000.0
	}

	return timestampMS / 1000
}

func hydrusNullableTimestampMS(
	timestamp sql.NullInt64,
	includeMilliseconds bool,
) any {
	if !timestamp.Valid {
		return nil
	}

	return hydrusTimestampMS(timestamp.Int64, includeMilliseconds)
}

func containsHashID(hashIDs map[int64]struct{}, hashID int64) bool {
	_, ok := hashIDs[hashID]
	return ok
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return map[string]string{}
	}

	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}

	return cloned
}

func cloneStringSlice(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}

	cloned := make([]string, len(values))
	copy(cloned, values)
	return cloned
}

func int64Args(values []int64) []any {
	args := make([]any, 0, len(values))
	for _, value := range values {
		args = append(args, value)
	}

	return args
}
