package hydrusdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/official-elinas/hydrus-go/internal/core/services"
	coretags "github.com/official-elinas/hydrus-go/internal/core/tags"
)

const (
	contentStatusCurrent = iota
	contentStatusPending
	contentStatusDeleted
	contentStatusPetitioned
)

var tagStatusOrder = []int{
	contentStatusCurrent,
	contentStatusPending,
	contentStatusDeleted,
	contentStatusPetitioned,
}

type metadataTagsPayload struct {
	tags          map[string]map[string]any
	legacyStorage map[string]map[string][]string
	legacyDisplay map[string]map[string][]string
}

type tagIDStatusSets map[int]map[int64]struct{}

func newTagIDStatusSets() tagIDStatusSets {
	return tagIDStatusSets{}
}

func (s tagIDStatusSets) add(status int, tagID int64) {
	if _, ok := s[status]; !ok {
		s[status] = map[int64]struct{}{}
	}

	s[status][tagID] = struct{}{}
}

func (s tagIDStatusSets) merge(other tagIDStatusSets) {
	for status, tagIDs := range other {
		for tagID := range tagIDs {
			s.add(status, tagID)
		}
	}
}

func (s tagIDStatusSets) copyStatusFrom(other tagIDStatusSets, status int) {
	for tagID := range other[status] {
		s.add(status, tagID)
	}
}

func (s tagIDStatusSets) allTagIDs() []int64 {
	tagIDs := []int64{}
	seen := map[int64]struct{}{}
	for _, tagIDsByStatus := range s {
		for tagID := range tagIDsByStatus {
			if _, ok := seen[tagID]; ok {
				continue
			}

			seen[tagID] = struct{}{}
			tagIDs = append(tagIDs, tagID)
		}
	}

	return tagIDs
}

func (s tagIDStatusSets) payload(tagsByID map[int64]string) map[string][]string {
	payload := map[string][]string{}
	for _, status := range tagStatusOrder {
		tagIDs := s[status]
		if len(tagIDs) == 0 {
			continue
		}

		sortedTags := make([]string, 0, len(tagIDs))
		for tagID := range tagIDs {
			tag, ok := tagsByID[tagID]
			if !ok {
				continue
			}

			sortedTags = append(sortedTags, tag)
		}

		if len(sortedTags) == 0 {
			continue
		}

		slices.Sort(sortedTags)
		payload[strconv.Itoa(status)] = sortedTags
	}

	return payload
}

func (b *Bundle) lookupFileTags(
	ctx context.Context,
	fileIDs []int64,
	currentFileServices map[int64][]currentFileServiceMembership,
) (map[int64]metadataTagsPayload, error) {
	tTotal := time.Now()
	payloads := map[int64]metadataTagsPayload{}
	if len(fileIDs) == 0 {
		return payloads, nil
	}

	servicesByID, err := b.lookupAllServiceDefinitions(ctx)
	if err != nil {
		return nil, err
	}

	realTagServices := []serviceDefinition{}
	combinedTagServices := []serviceDefinition{}
	combinedFileService, hasCombinedFileService := findServiceDefinitionByType(
		servicesByID,
		services.TypeCombinedFile,
	)
	for _, service := range servicesByID {
		switch service.serviceType {
		case services.TypeLocalTag, services.TypeTagRepository:
			realTagServices = append(realTagServices, service)
		case services.TypeCombinedTag:
			combinedTagServices = append(combinedTagServices, service)
		}
	}

	uniqueFileIDs := dedupeInt64s(fileIDs)
	for _, hashID := range uniqueFileIDs {
		payloads[hashID] = metadataTagsPayload{
			tags:          map[string]map[string]any{},
			legacyStorage: map[string]map[string][]string{},
			legacyDisplay: map[string]map[string][]string{},
		}
	}

	if len(realTagServices) == 0 && len(combinedTagServices) == 0 {
		return payloads, nil
	}

	t0 := time.Now()
	mappingTableNames, err := b.lookupSchemaTableNames(ctx, "external_mappings")
	if err != nil {
		return nil, err
	}
	cacheTableNames, err := b.lookupSchemaTableNames(ctx, "external_caches")
	if err != nil {
		return nil, err
	}
	slog.Debug("lookupFileTags: schema table name lookup", "elapsed", time.Since(t0))

	fileServiceGroups := groupHashIDsByTagCachedFileServiceID(
		uniqueFileIDs,
		currentFileServices,
		combinedFileService,
		hasCombinedFileService,
	)

	type pairResult struct {
		tagServiceKey   string
		storageByHashID map[int64]tagIDStatusSets
		displayByHashID map[int64]tagIDStatusSets
	}

	resultsCh := make(chan pairResult, len(realTagServices)*len(fileServiceGroups))

	slog.Debug("lookupFileTags: starting mapping queries",
		"file_ids", len(uniqueFileIDs),
		"real_tag_services", len(realTagServices),
		"file_service_groups", len(fileServiceGroups),
	)
	tMappings := time.Now()

	eg, egCtx := errgroup.WithContext(ctx)
	eg.SetLimit(readPoolSize)

	for _, tagService := range realTagServices {
		for fileServiceID, groupedHashIDs := range fileServiceGroups {
			tagService := tagService
			fileServiceID := fileServiceID
			groupedHashIDs := groupedHashIDs
			useCombinedFallback := !hasCombinedFileService || fileServiceID == combinedFileService.id

			eg.Go(func() error {
				tPair := time.Now()
				conn, err := b.acquireReadConn(egCtx)
				if err != nil {
					return err
				}
				defer b.releaseReadConn(conn)

				tStorage := time.Now()
				groupStorageByHashID, err := b.lookupStorageTagServiceMappingsConn(
					egCtx,
					conn,
					groupedHashIDs,
					fileServiceID,
					tagService,
					mappingTableNames,
					cacheTableNames,
					useCombinedFallback,
				)
				if err != nil {
					return err
				}
				slog.Debug("lookupFileTags: storage mappings",
					"tag_service_id", tagService.id,
					"file_service_id", fileServiceID,
					"elapsed", time.Since(tStorage),
				)

				tDisplay := time.Now()
				groupDisplayByHashID, err := b.lookupDisplayTagServiceMappingsConn(
					egCtx,
					conn,
					groupedHashIDs,
					fileServiceID,
					tagService,
					groupStorageByHashID,
					cacheTableNames,
					useCombinedFallback,
				)
				if err != nil {
					return err
				}
				slog.Debug("lookupFileTags: display mappings",
					"tag_service_id", tagService.id,
					"file_service_id", fileServiceID,
					"elapsed", time.Since(tDisplay),
					"pair_total", time.Since(tPair),
				)

				resultsCh <- pairResult{
					tagServiceKey:   tagService.serviceKey,
					storageByHashID: groupStorageByHashID,
					displayByHashID: groupDisplayByHashID,
				}
				return nil
			})
		}
	}

	if err := eg.Wait(); err != nil {
		return nil, err
	}
	close(resultsCh)
	slog.Debug("lookupFileTags: mapping queries done", "elapsed", time.Since(tMappings))

	storageByHashID := map[int64]map[string]tagIDStatusSets{}
	displayByHashID := map[int64]map[string]tagIDStatusSets{}
	for result := range resultsCh {
		for hashID, statuses := range result.storageByHashID {
			ensureHashServiceTagSet(storageByHashID, hashID, result.tagServiceKey).merge(statuses)
		}
		for hashID, statuses := range result.displayByHashID {
			ensureHashServiceTagSet(displayByHashID, hashID, result.tagServiceKey).merge(statuses)
		}
	}

	seenTagIDs := []int64{}
	for _, tagsByService := range storageByHashID {
		for _, statuses := range tagsByService {
			seenTagIDs = append(seenTagIDs, statuses.allTagIDs()...)
		}
	}

	for _, tagsByService := range displayByHashID {
		for _, statuses := range tagsByService {
			seenTagIDs = append(seenTagIDs, statuses.allTagIDs()...)
		}
	}

	deduped := dedupeInt64s(seenTagIDs)
	slog.Debug("lookupFileTags: resolving tag IDs to strings", "unique_tag_ids", len(deduped))
	tTagLookup := time.Now()

	tagConn, err := b.acquireReadConn(ctx)
	if err != nil {
		return nil, err
	}
	tagsByID, err := b.lookupTagsByIDConn(ctx, tagConn, deduped)
	b.releaseReadConn(tagConn)
	if err != nil {
		return nil, err
	}
	slog.Debug("lookupFileTags: tag ID resolution done", "elapsed", time.Since(tTagLookup))
	slog.Debug("lookupFileTags: total", "elapsed", time.Since(tTotal))

	for _, hashID := range uniqueFileIDs {
		payload := payloads[hashID]
		combinedStorage := newTagIDStatusSets()
		combinedDisplay := newTagIDStatusSets()

		for _, service := range realTagServices {
			storageStatuses := ensureExistingHashServiceTagSet(
				storageByHashID,
				hashID,
				service.serviceKey,
			)
			displayStatuses := ensureExistingHashServiceTagSet(
				displayByHashID,
				hashID,
				service.serviceKey,
			)

			combinedStorage.merge(storageStatuses)
			combinedDisplay.merge(displayStatuses)

			storageTags := storageStatuses.payload(tagsByID)
			displayTags := displayStatuses.payload(tagsByID)
			payload.tags[service.serviceKey] = buildTagServicePayload(
				service,
				storageTags,
				displayTags,
			)

			if len(storageTags) > 0 {
				payload.legacyStorage[service.serviceKey] = storageTags
			}

			if len(displayTags) > 0 {
				payload.legacyDisplay[service.serviceKey] = displayTags
			}
		}

		for _, service := range combinedTagServices {
			storageTags := combinedStorage.payload(tagsByID)
			displayTags := combinedDisplay.payload(tagsByID)
			payload.tags[service.serviceKey] = buildTagServicePayload(
				service,
				storageTags,
				displayTags,
			)

			if len(storageTags) > 0 {
				payload.legacyStorage[service.serviceKey] = storageTags
			}

			if len(displayTags) > 0 {
				payload.legacyDisplay[service.serviceKey] = displayTags
			}
		}

		payloads[hashID] = payload
	}

	return payloads, nil
}

func (b *Bundle) lookupFileTagsConn(
	ctx context.Context,
	_ *sql.Conn,
	fileIDs []int64,
	currentFileServices map[int64][]currentFileServiceMembership,
) (map[int64]metadataTagsPayload, error) {
	return b.lookupFileTags(ctx, fileIDs, currentFileServices)
}

func findServiceDefinitionByType(
	definitions []serviceDefinition,
	serviceType services.Type,
) (serviceDefinition, bool) {
	for _, definition := range definitions {
		if definition.serviceType == serviceType {
			return definition, true
		}
	}

	return serviceDefinition{}, false
}

func ensureHashServiceTagSet(
	byHashID map[int64]map[string]tagIDStatusSets,
	hashID int64,
	serviceKey string,
) tagIDStatusSets {
	if _, ok := byHashID[hashID]; !ok {
		byHashID[hashID] = map[string]tagIDStatusSets{}
	}

	if _, ok := byHashID[hashID][serviceKey]; !ok {
		byHashID[hashID][serviceKey] = newTagIDStatusSets()
	}

	return byHashID[hashID][serviceKey]
}

func ensureExistingHashServiceTagSet(
	byHashID map[int64]map[string]tagIDStatusSets,
	hashID int64,
	serviceKey string,
) tagIDStatusSets {
	servicesByKey, ok := byHashID[hashID]
	if !ok {
		return newTagIDStatusSets()
	}

	statuses, ok := servicesByKey[serviceKey]
	if !ok {
		return newTagIDStatusSets()
	}

	return statuses
}

func groupHashIDsByTagCachedFileServiceID(
	fileIDs []int64,
	currentFileServices map[int64][]currentFileServiceMembership,
	combinedFileService serviceDefinition,
	hasCombinedFileService bool,
) map[int64][]int64 {
	// Hydrus groups hashes by the most comprehensive specific-cache-backed file
	// service first and then makes the groups non-overlapping. That means the
	// same hash can legitimately use a different specific cache depending on the
	// wider request cohort, which we preserve for DB parity.
	byFileServiceID := map[int64]map[int64]struct{}{}
	for _, hashID := range fileIDs {
		for _, membership := range currentFileServices[hashID] {
			if !supportsSpecificMappingCache(membership.service.serviceType) {
				continue
			}

			if _, ok := byFileServiceID[membership.service.id]; !ok {
				byFileServiceID[membership.service.id] = map[int64]struct{}{}
			}

			byFileServiceID[membership.service.id][hashID] = struct{}{}
		}
	}

	type fileServiceGroup struct {
		fileServiceID int64
		hashIDs       map[int64]struct{}
	}

	groups := make([]fileServiceGroup, 0, len(byFileServiceID))
	for fileServiceID, hashIDs := range byFileServiceID {
		groups = append(groups, fileServiceGroup{fileServiceID: fileServiceID, hashIDs: hashIDs})
	}

	slices.SortFunc(groups, func(left, right fileServiceGroup) int {
		if len(left.hashIDs) > len(right.hashIDs) {
			return -1
		}

		if len(left.hashIDs) < len(right.hashIDs) {
			return 1
		}

		if left.fileServiceID < right.fileServiceID {
			return -1
		}

		if left.fileServiceID > right.fileServiceID {
			return 1
		}

		return 0
	})

	seenHashIDs := map[int64]struct{}{}
	result := map[int64][]int64{}
	for _, group := range groups {
		nonOverlapping := []int64{}
		for hashID := range group.hashIDs {
			if _, ok := seenHashIDs[hashID]; ok {
				continue
			}

			nonOverlapping = append(nonOverlapping, hashID)
		}

		if len(nonOverlapping) == 0 {
			continue
		}

		slices.Sort(nonOverlapping)
		result[group.fileServiceID] = nonOverlapping
		for _, hashID := range nonOverlapping {
			seenHashIDs[hashID] = struct{}{}
		}
	}

	unmappedHashIDs := []int64{}
	for _, hashID := range fileIDs {
		if _, ok := seenHashIDs[hashID]; ok {
			continue
		}

		unmappedHashIDs = append(unmappedHashIDs, hashID)
	}

	if len(unmappedHashIDs) > 0 {
		fallbackFileServiceID := int64(0)
		if hasCombinedFileService {
			fallbackFileServiceID = combinedFileService.id
		}

		result[fallbackFileServiceID] = unmappedHashIDs
	}

	return result
}

func supportsSpecificMappingCache(serviceType services.Type) bool {
	switch serviceType {
	case services.TypeLocalFileDomain,
		services.TypeLocalFileTrashDomain,
		services.TypeHydrusLocalFileStorage,
		services.TypeCombinedLocalFileDomains,
		services.TypeLocalFileUpdateDomain,
		services.TypeFileRepository,
		services.TypeIPFS,
		services.TypeCombinedDeletedFile:
		return true
	default:
		return false
	}
}

func (b *Bundle) lookupStorageTagServiceMappings(
	ctx context.Context,
	fileIDs []int64,
	fileServiceID int64,
	tagService serviceDefinition,
	mappingTableNames map[string]struct{},
	cacheTableNames map[string]struct{},
	useCombinedFallback bool,
) (map[int64]tagIDStatusSets, error) {
	mappings := map[int64]tagIDStatusSets{}
	if len(fileIDs) == 0 {
		return mappings, nil
	}

	queries := []struct {
		status    int
		schema    string
		tableName string
	}{
		{
			status:    contentStatusCurrent,
			schema:    "external_mappings",
			tableName: fmt.Sprintf("current_mappings_%d", tagService.id),
		},
		{
			status:    contentStatusDeleted,
			schema:    "external_mappings",
			tableName: fmt.Sprintf("deleted_mappings_%d", tagService.id),
		},
		{
			status:    contentStatusPending,
			schema:    "external_mappings",
			tableName: fmt.Sprintf("pending_mappings_%d", tagService.id),
		},
	}

	if !useCombinedFallback {
		currentTableName := specificStorageCurrentTableName(fileServiceID, tagService.id)
		if _, ok := cacheTableNames[currentTableName]; ok {
			queries[0].schema = "external_caches"
			queries[0].tableName = currentTableName
		}

		deletedTableName := specificStorageDeletedTableName(fileServiceID, tagService.id)
		if _, ok := cacheTableNames[deletedTableName]; ok {
			queries[1].schema = "external_caches"
			queries[1].tableName = deletedTableName
		}

		pendingTableName := specificStoragePendingTableName(fileServiceID, tagService.id)
		if _, ok := cacheTableNames[pendingTableName]; ok {
			queries[2].schema = "external_caches"
			queries[2].tableName = pendingTableName
		}
	}

	for _, query := range queries {
		if query.schema == "external_mappings" {
			if _, ok := mappingTableNames[query.tableName]; !ok {
				continue
			}
		} else {
			if _, ok := cacheTableNames[query.tableName]; !ok {
				continue
			}
		}

		if err := b.collectTagIDsFromTable(
			ctx,
			fileIDs,
			query.schema,
			query.tableName,
			query.status,
			mappings,
		); err != nil {
			return nil, err
		}
	}

	petitionedTableName := fmt.Sprintf("petitioned_mappings_%d", tagService.id)
	if _, ok := mappingTableNames[petitionedTableName]; ok {
		if err := b.collectTagIDsFromTable(
			ctx,
			fileIDs,
			"external_mappings",
			petitionedTableName,
			contentStatusPetitioned,
			mappings,
		); err != nil {
			return nil, err
		}
	}

	return mappings, nil
}

func (b *Bundle) lookupDisplayTagServiceMappings(
	ctx context.Context,
	fileIDs []int64,
	fileServiceID int64,
	tagService serviceDefinition,
	storageByHashID map[int64]tagIDStatusSets,
	cacheTableNames map[string]struct{},
	useCombinedFallback bool,
) (map[int64]tagIDStatusSets, error) {
	displayByHashID := map[int64]tagIDStatusSets{}
	if len(fileIDs) == 0 {
		return displayByHashID, nil
	}

	useFallbackCurrent := true
	useFallbackPending := true
	if !useCombinedFallback {
		currentDisplayTableName := specificDisplayCurrentTableName(fileServiceID, tagService.id)
		if _, ok := cacheTableNames[currentDisplayTableName]; ok {
			useFallbackCurrent = false
			if err := b.collectTagIDsFromTable(
				ctx,
				fileIDs,
				"external_caches",
				currentDisplayTableName,
				contentStatusCurrent,
				displayByHashID,
			); err != nil {
				return nil, err
			}
		}

		pendingDisplayTableName := specificDisplayPendingTableName(fileServiceID, tagService.id)
		if _, ok := cacheTableNames[pendingDisplayTableName]; ok {
			useFallbackPending = false
			if err := b.collectTagIDsFromTable(
				ctx,
				fileIDs,
				"external_caches",
				pendingDisplayTableName,
				contentStatusPending,
				displayByHashID,
			); err != nil {
				return nil, err
			}
		}
	}

	if useFallbackCurrent {
		if err := b.addDisplayImplicationsFromStorage(
			ctx,
			tagService,
			storageByHashID,
			contentStatusCurrent,
			cacheTableNames,
			displayByHashID,
		); err != nil {
			return nil, err
		}
	}

	if useFallbackPending {
		if err := b.addDisplayImplicationsFromStorage(
			ctx,
			tagService,
			storageByHashID,
			contentStatusPending,
			cacheTableNames,
			displayByHashID,
		); err != nil {
			return nil, err
		}
	}

	for hashID, storageStatuses := range storageByHashID {
		displayStatuses := ensureTagIDStatusSet(displayByHashID, hashID)
		displayStatuses.copyStatusFrom(storageStatuses, contentStatusDeleted)
		displayStatuses.copyStatusFrom(storageStatuses, contentStatusPetitioned)
	}

	return displayByHashID, nil
}

func ensureTagIDStatusSet(
	byHashID map[int64]tagIDStatusSets,
	hashID int64,
) tagIDStatusSets {
	if _, ok := byHashID[hashID]; !ok {
		byHashID[hashID] = newTagIDStatusSets()
	}

	return byHashID[hashID]
}

func (b *Bundle) addDisplayImplicationsFromStorage(
	ctx context.Context,
	tagService serviceDefinition,
	storageByHashID map[int64]tagIDStatusSets,
	status int,
	cacheTableNames map[string]struct{},
	displayByHashID map[int64]tagIDStatusSets,
) error {
	inputTagIDs := []int64{}
	for _, storageStatuses := range storageByHashID {
		for tagID := range storageStatuses[status] {
			inputTagIDs = append(inputTagIDs, tagID)
		}
	}

	implicationsByTagID, err := b.lookupDisplayImplicationTagIDs(
		ctx,
		tagService.id,
		dedupeInt64s(inputTagIDs),
		cacheTableNames,
	)
	if err != nil {
		return err
	}

	for hashID, storageStatuses := range storageByHashID {
		for tagID := range storageStatuses[status] {
			impliedTagIDs, ok := implicationsByTagID[tagID]
			if !ok || len(impliedTagIDs) == 0 {
				continue
			}

			displayStatuses := ensureTagIDStatusSet(displayByHashID, hashID)
			for _, impliedTagID := range impliedTagIDs {
				displayStatuses.add(status, impliedTagID)
			}
		}
	}

	return nil
}

func (b *Bundle) lookupDisplayImplicationTagIDs(
	ctx context.Context,
	tagServiceID int64,
	tagIDs []int64,
	cacheTableNames map[string]struct{},
) (map[int64][]int64, error) {
	implicationsByTagID := map[int64][]int64{}
	if len(tagIDs) == 0 {
		return implicationsByTagID, nil
	}

	idealsByTagID := map[int64]int64{}
	for _, tagID := range tagIDs {
		idealsByTagID[tagID] = tagID
	}

	siblingTableName := actualTagSiblingLookupTableName(tagServiceID)
	if _, ok := cacheTableNames[siblingTableName]; ok {
		query := fmt.Sprintf(
			`SELECT bad_tag_id, ideal_tag_id
			FROM external_caches.%s
			WHERE bad_tag_id IN (%s)`,
			siblingTableName,
			placeholders(len(tagIDs)),
		)

		rows, err := b.conn.QueryContext(ctx, query, int64Args(tagIDs)...)
		if err != nil {
			return nil, fmt.Errorf("query tag siblings lookup %s: %w", siblingTableName, err)
		}
		defer rows.Close()

		for rows.Next() {
			var (
				badTagID   int64
				idealTagID int64
			)

			if err := rows.Scan(&badTagID, &idealTagID); err != nil {
				return nil, fmt.Errorf("scan tag siblings lookup %s row: %w", siblingTableName, err)
			}

			idealsByTagID[badTagID] = idealTagID
		}

		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("iterate tag siblings lookup %s rows: %w", siblingTableName, err)
		}
	}

	idealTagIDs := dedupeInt64s(mapValuesInt64(idealsByTagID))
	ancestorsByChildTagID := map[int64][]int64{}
	parentTableName := actualTagParentLookupTableName(tagServiceID)
	if len(idealTagIDs) > 0 {
		if _, ok := cacheTableNames[parentTableName]; ok {
			query := fmt.Sprintf(
				`SELECT child_tag_id, ancestor_tag_id
				FROM external_caches.%s
				WHERE child_tag_id IN (%s)`,
				parentTableName,
				placeholders(len(idealTagIDs)),
			)

			rows, err := b.conn.QueryContext(ctx, query, int64Args(idealTagIDs)...)
			if err != nil {
				return nil, fmt.Errorf("query tag parents lookup %s: %w", parentTableName, err)
			}
			defer rows.Close()

			for rows.Next() {
				var (
					childTagID    int64
					ancestorTagID int64
				)

				if err := rows.Scan(&childTagID, &ancestorTagID); err != nil {
					return nil, fmt.Errorf("scan tag parents lookup %s row: %w", parentTableName, err)
				}

				ancestorsByChildTagID[childTagID] = append(
					ancestorsByChildTagID[childTagID],
					ancestorTagID,
				)
			}

			if err := rows.Err(); err != nil {
				return nil, fmt.Errorf("iterate tag parents lookup %s rows: %w", parentTableName, err)
			}
		}
	}

	for _, tagID := range tagIDs {
		idealTagID := idealsByTagID[tagID]
		impliedTagIDs := []int64{idealTagID}
		impliedTagIDs = append(impliedTagIDs, ancestorsByChildTagID[idealTagID]...)
		implicationsByTagID[tagID] = dedupeInt64s(impliedTagIDs)
	}

	return implicationsByTagID, nil
}

func (b *Bundle) collectTagIDsFromTable(
	ctx context.Context,
	fileIDs []int64,
	schemaName string,
	tableName string,
	status int,
	mappings map[int64]tagIDStatusSets,
) error {
	query := fmt.Sprintf(
		`SELECT hash_id, tag_id
		FROM %s.%s
		WHERE hash_id IN (%s)`,
		schemaName,
		tableName,
		placeholders(len(fileIDs)),
	)

	rows, err := b.conn.QueryContext(ctx, query, int64Args(fileIDs)...)
	if err != nil {
		return fmt.Errorf("query tag mappings %s.%s: %w", schemaName, tableName, err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			hashID int64
			tagID  int64
		)

		if err := rows.Scan(&hashID, &tagID); err != nil {
			return fmt.Errorf("scan tag mapping %s.%s row: %w", schemaName, tableName, err)
		}

		ensureTagIDStatusSet(mappings, hashID).add(status, tagID)
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate tag mapping %s.%s rows: %w", schemaName, tableName, err)
	}

	return nil
}

func (b *Bundle) lookupTagsByID(
	ctx context.Context,
	tagIDs []int64,
) (map[int64]string, error) {
	if len(tagIDs) == 0 {
		return map[int64]string{}, nil
	}

	schemaMode, err := b.lookupMasterTagSchemaMode(ctx)
	if err != nil {
		return nil, err
	}

	switch schemaMode {
	case masterTagSchemaSplit:
		return b.lookupSplitTagsByID(ctx, tagIDs)
	case masterTagSchemaLegacyFlat:
		return b.lookupFlatTagsByID(ctx, tagIDs)
	case masterTagSchemaEmpty:
		return nil, errors.New("external_master tag schema is missing the tags table")
	default:
		return nil, errors.New("unsupported external_master tag schema mode")
	}

}

func (b *Bundle) lookupSplitTagsByID(
	ctx context.Context,
	tagIDs []int64,
) (map[int64]string, error) {

	query := fmt.Sprintf(
		`SELECT t.tag_id, n.namespace, s.subtag
		FROM external_master.tags t
		JOIN external_master.namespaces n USING (namespace_id)
		JOIN external_master.subtags s USING (subtag_id)
		WHERE t.tag_id IN (%s)`,
		placeholders(len(tagIDs)),
	)

	rows, err := b.conn.QueryContext(ctx, query, int64Args(tagIDs)...)
	if err != nil {
		return nil, fmt.Errorf("query tags by ID: %w", err)
	}
	defer rows.Close()

	tagsByID := map[int64]string{}
	for rows.Next() {
		var (
			tagID     int64
			namespace string
			subtag    string
		)

		if err := rows.Scan(&tagID, &namespace, &subtag); err != nil {
			return nil, fmt.Errorf("scan tags by ID row: %w", err)
		}

		tagsByID[tagID] = coretags.Combine(namespace, subtag)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tags by ID rows: %w", err)
	}

	return tagsByID, nil
}

func (b *Bundle) lookupFlatTagsByID(
	ctx context.Context,
	tagIDs []int64,
) (map[int64]string, error) {
	query := fmt.Sprintf(
		`SELECT tag_id, tag
		FROM external_master.tags
		WHERE tag_id IN (%s)`,
		placeholders(len(tagIDs)),
	)

	rows, err := b.conn.QueryContext(ctx, query, int64Args(tagIDs)...)
	if err != nil {
		return nil, fmt.Errorf("query flat tags by ID: %w", err)
	}
	defer rows.Close()

	tagsByID := map[int64]string{}
	for rows.Next() {
		var (
			tagID int64
			tag   string
		)

		if err := rows.Scan(&tagID, &tag); err != nil {
			return nil, fmt.Errorf("scan flat tags by ID row: %w", err)
		}

		tagsByID[tagID] = tag
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate flat tags by ID rows: %w", err)
	}

	return tagsByID, nil
}

func specificStorageCurrentTableName(fileServiceID int64, tagServiceID int64) string {
	return fmt.Sprintf("specific_current_mappings_cache_%d_%d", fileServiceID, tagServiceID)
}

func specificStorageDeletedTableName(fileServiceID int64, tagServiceID int64) string {
	return fmt.Sprintf("specific_deleted_mappings_cache_%d_%d", fileServiceID, tagServiceID)
}

func specificStoragePendingTableName(fileServiceID int64, tagServiceID int64) string {
	return fmt.Sprintf("specific_pending_mappings_cache_%d_%d", fileServiceID, tagServiceID)
}

func specificDisplayCurrentTableName(fileServiceID int64, tagServiceID int64) string {
	return fmt.Sprintf("specific_display_current_mappings_cache_%d_%d", fileServiceID, tagServiceID)
}

func specificDisplayPendingTableName(fileServiceID int64, tagServiceID int64) string {
	return fmt.Sprintf("specific_display_pending_mappings_cache_%d_%d", fileServiceID, tagServiceID)
}

func actualTagSiblingLookupTableName(tagServiceID int64) string {
	return fmt.Sprintf("actual_tag_siblings_lookup_cache_%d", tagServiceID)
}

func actualTagParentLookupTableName(tagServiceID int64) string {
	return fmt.Sprintf("actual_tag_parents_lookup_cache_%d", tagServiceID)
}

func mapValuesInt64(values map[int64]int64) []int64 {
	result := make([]int64, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}

	return result
}

func buildTagServicePayload(
	service serviceDefinition,
	storageTags map[string][]string,
	displayTags map[string][]string,
) map[string]any {
	return map[string]any{
		"name":         service.name,
		"type":         service.serviceType,
		"type_pretty":  services.TypePretty(service.serviceType),
		"storage_tags": storageTags,
		"display_tags": displayTags,
	}
}

func collectTagIDsFromTableConn(
	ctx context.Context,
	conn *sql.Conn,
	fileIDs []int64,
	schemaName string,
	tableName string,
	status int,
	mappings map[int64]tagIDStatusSets,
) error {
	query := fmt.Sprintf(
		`WITH h(hash_id) AS (VALUES %s)
		SELECT m.hash_id, m.tag_id
		FROM h CROSS JOIN %s.%s m USING (hash_id)`,
		rowPlaceholders(len(fileIDs)),
		schemaName,
		tableName,
	)

	rows, err := conn.QueryContext(ctx, query, int64Args(fileIDs)...)
	if err != nil {
		return fmt.Errorf("query tag mappings %s.%s: %w", schemaName, tableName, err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			hashID int64
			tagID  int64
		)

		if err := rows.Scan(&hashID, &tagID); err != nil {
			return fmt.Errorf("scan tag mapping %s.%s row: %w", schemaName, tableName, err)
		}

		ensureTagIDStatusSet(mappings, hashID).add(status, tagID)
	}

	return rows.Err()
}

func (b *Bundle) lookupStorageTagServiceMappingsConn(
	ctx context.Context,
	conn *sql.Conn,
	fileIDs []int64,
	fileServiceID int64,
	tagService serviceDefinition,
	mappingTableNames map[string]struct{},
	cacheTableNames map[string]struct{},
	useCombinedFallback bool,
) (map[int64]tagIDStatusSets, error) {
	mappings := map[int64]tagIDStatusSets{}
	if len(fileIDs) == 0 {
		return mappings, nil
	}

	queries := []struct {
		status    int
		schema    string
		tableName string
	}{
		{status: contentStatusCurrent, schema: "external_mappings", tableName: fmt.Sprintf("current_mappings_%d", tagService.id)},
		{status: contentStatusDeleted, schema: "external_mappings", tableName: fmt.Sprintf("deleted_mappings_%d", tagService.id)},
		{status: contentStatusPending, schema: "external_mappings", tableName: fmt.Sprintf("pending_mappings_%d", tagService.id)},
	}

	if !useCombinedFallback {
		if n := specificStorageCurrentTableName(fileServiceID, tagService.id); tableExists(cacheTableNames, n) {
			queries[0].schema, queries[0].tableName = "external_caches", n
		}
		if n := specificStorageDeletedTableName(fileServiceID, tagService.id); tableExists(cacheTableNames, n) {
			queries[1].schema, queries[1].tableName = "external_caches", n
		}
		if n := specificStoragePendingTableName(fileServiceID, tagService.id); tableExists(cacheTableNames, n) {
			queries[2].schema, queries[2].tableName = "external_caches", n
		}
	}

	for _, q := range queries {
		tableSet := mappingTableNames
		if q.schema != "external_mappings" {
			tableSet = cacheTableNames
		}
		if _, ok := tableSet[q.tableName]; !ok {
			continue
		}
		if err := collectTagIDsFromTableConn(ctx, conn, fileIDs, q.schema, q.tableName, q.status, mappings); err != nil {
			return nil, err
		}
	}

	petitionedTableName := fmt.Sprintf("petitioned_mappings_%d", tagService.id)
	if _, ok := mappingTableNames[petitionedTableName]; ok {
		if err := collectTagIDsFromTableConn(ctx, conn, fileIDs, "external_mappings", petitionedTableName, contentStatusPetitioned, mappings); err != nil {
			return nil, err
		}
	}

	return mappings, nil
}

func (b *Bundle) lookupDisplayImplicationTagIDsConn(
	ctx context.Context,
	conn *sql.Conn,
	tagServiceID int64,
	tagIDs []int64,
	cacheTableNames map[string]struct{},
) (map[int64][]int64, error) {
	implicationsByTagID := map[int64][]int64{}
	if len(tagIDs) == 0 {
		return implicationsByTagID, nil
	}

	idealsByTagID := map[int64]int64{}
	for _, tagID := range tagIDs {
		idealsByTagID[tagID] = tagID
	}

	siblingTableName := actualTagSiblingLookupTableName(tagServiceID)
	if _, ok := cacheTableNames[siblingTableName]; ok {
		query := fmt.Sprintf(
			`SELECT bad_tag_id, ideal_tag_id
			FROM external_caches.%s
			WHERE bad_tag_id IN (%s)`,
			siblingTableName,
			placeholders(len(tagIDs)),
		)

		rows, err := conn.QueryContext(ctx, query, int64Args(tagIDs)...)
		if err != nil {
			return nil, fmt.Errorf("query tag siblings lookup %s: %w", siblingTableName, err)
		}
		defer rows.Close()

		for rows.Next() {
			var badTagID, idealTagID int64
			if err := rows.Scan(&badTagID, &idealTagID); err != nil {
				return nil, fmt.Errorf("scan tag siblings lookup %s row: %w", siblingTableName, err)
			}
			idealsByTagID[badTagID] = idealTagID
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("iterate tag siblings lookup %s rows: %w", siblingTableName, err)
		}
	}

	idealTagIDs := dedupeInt64s(mapValuesInt64(idealsByTagID))
	ancestorsByChildTagID := map[int64][]int64{}
	parentTableName := actualTagParentLookupTableName(tagServiceID)
	if len(idealTagIDs) > 0 {
		if _, ok := cacheTableNames[parentTableName]; ok {
			query := fmt.Sprintf(
				`SELECT child_tag_id, ancestor_tag_id
				FROM external_caches.%s
				WHERE child_tag_id IN (%s)`,
				parentTableName,
				placeholders(len(idealTagIDs)),
			)

			rows, err := conn.QueryContext(ctx, query, int64Args(idealTagIDs)...)
			if err != nil {
				return nil, fmt.Errorf("query tag parents lookup %s: %w", parentTableName, err)
			}
			defer rows.Close()

			for rows.Next() {
				var childTagID, ancestorTagID int64
				if err := rows.Scan(&childTagID, &ancestorTagID); err != nil {
					return nil, fmt.Errorf("scan tag parents lookup %s row: %w", parentTableName, err)
				}
				ancestorsByChildTagID[childTagID] = append(ancestorsByChildTagID[childTagID], ancestorTagID)
			}
			if err := rows.Err(); err != nil {
				return nil, fmt.Errorf("iterate tag parents lookup %s rows: %w", parentTableName, err)
			}
		}
	}

	for _, tagID := range tagIDs {
		idealTagID := idealsByTagID[tagID]
		impliedTagIDs := []int64{idealTagID}
		impliedTagIDs = append(impliedTagIDs, ancestorsByChildTagID[idealTagID]...)
		implicationsByTagID[tagID] = dedupeInt64s(impliedTagIDs)
	}

	return implicationsByTagID, nil
}

func (b *Bundle) addDisplayImplicationsFromStorageConn(
	ctx context.Context,
	conn *sql.Conn,
	tagService serviceDefinition,
	storageByHashID map[int64]tagIDStatusSets,
	status int,
	cacheTableNames map[string]struct{},
	displayByHashID map[int64]tagIDStatusSets,
) error {
	inputTagIDs := []int64{}
	for _, storageStatuses := range storageByHashID {
		for tagID := range storageStatuses[status] {
			inputTagIDs = append(inputTagIDs, tagID)
		}
	}

	implicationsByTagID, err := b.lookupDisplayImplicationTagIDsConn(
		ctx,
		conn,
		tagService.id,
		dedupeInt64s(inputTagIDs),
		cacheTableNames,
	)
	if err != nil {
		return err
	}

	for hashID, storageStatuses := range storageByHashID {
		for tagID := range storageStatuses[status] {
			impliedTagIDs, ok := implicationsByTagID[tagID]
			if !ok || len(impliedTagIDs) == 0 {
				continue
			}
			displayStatuses := ensureTagIDStatusSet(displayByHashID, hashID)
			for _, impliedTagID := range impliedTagIDs {
				displayStatuses.add(status, impliedTagID)
			}
		}
	}

	return nil
}

func (b *Bundle) lookupDisplayTagServiceMappingsConn(
	ctx context.Context,
	conn *sql.Conn,
	fileIDs []int64,
	fileServiceID int64,
	tagService serviceDefinition,
	storageByHashID map[int64]tagIDStatusSets,
	cacheTableNames map[string]struct{},
	useCombinedFallback bool,
) (map[int64]tagIDStatusSets, error) {
	displayByHashID := map[int64]tagIDStatusSets{}
	if len(fileIDs) == 0 {
		return displayByHashID, nil
	}

	useFallbackCurrent := true
	useFallbackPending := true
	if !useCombinedFallback {
		currentDisplayTableName := specificDisplayCurrentTableName(fileServiceID, tagService.id)
		if _, ok := cacheTableNames[currentDisplayTableName]; ok {
			useFallbackCurrent = false
			if err := collectTagIDsFromTableConn(ctx, conn, fileIDs, "external_caches", currentDisplayTableName, contentStatusCurrent, displayByHashID); err != nil {
				return nil, err
			}
		}

		pendingDisplayTableName := specificDisplayPendingTableName(fileServiceID, tagService.id)
		if _, ok := cacheTableNames[pendingDisplayTableName]; ok {
			useFallbackPending = false
			if err := collectTagIDsFromTableConn(ctx, conn, fileIDs, "external_caches", pendingDisplayTableName, contentStatusPending, displayByHashID); err != nil {
				return nil, err
			}
		}
	}

	if useFallbackCurrent {
		if err := b.addDisplayImplicationsFromStorageConn(ctx, conn, tagService, storageByHashID, contentStatusCurrent, cacheTableNames, displayByHashID); err != nil {
			return nil, err
		}
	}

	if useFallbackPending {
		if err := b.addDisplayImplicationsFromStorageConn(ctx, conn, tagService, storageByHashID, contentStatusPending, cacheTableNames, displayByHashID); err != nil {
			return nil, err
		}
	}

	for hashID, storageStatuses := range storageByHashID {
		displayStatuses := ensureTagIDStatusSet(displayByHashID, hashID)
		displayStatuses.copyStatusFrom(storageStatuses, contentStatusDeleted)
		displayStatuses.copyStatusFrom(storageStatuses, contentStatusPetitioned)
	}

	return displayByHashID, nil
}

func (b *Bundle) lookupTagsByIDConn(
	ctx context.Context,
	conn *sql.Conn,
	tagIDs []int64,
) (map[int64]string, error) {
	if len(tagIDs) == 0 {
		return map[int64]string{}, nil
	}

	schemaMode, err := b.lookupMasterTagSchemaMode(ctx)
	if err != nil {
		return nil, err
	}

	switch schemaMode {
	case masterTagSchemaSplit:
		return b.lookupSplitTagsByIDConn(ctx, conn, tagIDs)
	case masterTagSchemaLegacyFlat:
		return b.lookupFlatTagsByIDConn(ctx, conn, tagIDs)
	default:
		return nil, errors.New("unsupported external_master tag schema mode")
	}
}

func (b *Bundle) lookupSplitTagsByIDConn(
	ctx context.Context,
	conn *sql.Conn,
	tagIDs []int64,
) (map[int64]string, error) {
	query := fmt.Sprintf(
		`SELECT t.tag_id, n.namespace, s.subtag
		FROM external_master.tags t
		JOIN external_master.namespaces n USING (namespace_id)
		JOIN external_master.subtags s USING (subtag_id)
		WHERE t.tag_id IN (%s)`,
		placeholders(len(tagIDs)),
	)

	rows, err := conn.QueryContext(ctx, query, int64Args(tagIDs)...)
	if err != nil {
		return nil, fmt.Errorf("query tags by ID: %w", err)
	}
	defer rows.Close()

	tagsByID := map[int64]string{}
	for rows.Next() {
		var tagID int64
		var namespace, subtag string
		if err := rows.Scan(&tagID, &namespace, &subtag); err != nil {
			return nil, fmt.Errorf("scan tags by ID row: %w", err)
		}
		tagsByID[tagID] = coretags.Combine(namespace, subtag)
	}

	return tagsByID, rows.Err()
}

func (b *Bundle) lookupFlatTagsByIDConn(
	ctx context.Context,
	conn *sql.Conn,
	tagIDs []int64,
) (map[int64]string, error) {
	query := fmt.Sprintf(
		`SELECT tag_id, tag
		FROM external_master.tags
		WHERE tag_id IN (%s)`,
		placeholders(len(tagIDs)),
	)

	rows, err := conn.QueryContext(ctx, query, int64Args(tagIDs)...)
	if err != nil {
		return nil, fmt.Errorf("query flat tags by ID: %w", err)
	}
	defer rows.Close()

	tagsByID := map[int64]string{}
	for rows.Next() {
		var tagID int64
		var tag string
		if err := rows.Scan(&tagID, &tag); err != nil {
			return nil, fmt.Errorf("scan flat tags by ID row: %w", err)
		}
		tagsByID[tagID] = tag
	}

	return tagsByID, rows.Err()
}

func tableExists(tableSet map[string]struct{}, name string) bool {
	_, ok := tableSet[name]
	return ok
}
