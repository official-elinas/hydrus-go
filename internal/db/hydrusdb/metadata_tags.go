package hydrusdb

import (
	"context"
	"fmt"
	"slices"
	"strconv"

	"github.com/official-elinas/hydrus-go/internal/core/services"
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

	mappingTableNames, err := b.lookupSchemaTableNames(ctx, "external_mappings")
	if err != nil {
		return nil, err
	}

	cacheTableNames, err := b.lookupSchemaTableNames(ctx, "external_caches")
	if err != nil {
		return nil, err
	}

	fileServiceGroups := groupHashIDsByTagCachedFileServiceID(
		uniqueFileIDs,
		currentFileServices,
		combinedFileService,
		hasCombinedFileService,
	)

	storageByHashID := map[int64]map[string]tagIDStatusSets{}
	displayByHashID := map[int64]map[string]tagIDStatusSets{}
	for _, tagService := range realTagServices {
		for fileServiceID, groupedHashIDs := range fileServiceGroups {
			useCombinedFallback := !hasCombinedFileService || fileServiceID == combinedFileService.id

			groupStorageByHashID, err := b.lookupStorageTagServiceMappings(
				ctx,
				groupedHashIDs,
				fileServiceID,
				tagService,
				mappingTableNames,
				cacheTableNames,
				useCombinedFallback,
			)
			if err != nil {
				return nil, err
			}

			for hashID, statuses := range groupStorageByHashID {
				ensureHashServiceTagSet(storageByHashID, hashID, tagService.serviceKey).merge(statuses)
			}

			groupDisplayByHashID, err := b.lookupDisplayTagServiceMappings(
				ctx,
				groupedHashIDs,
				fileServiceID,
				tagService,
				groupStorageByHashID,
				cacheTableNames,
				useCombinedFallback,
			)
			if err != nil {
				return nil, err
			}

			for hashID, statuses := range groupDisplayByHashID {
				ensureHashServiceTagSet(displayByHashID, hashID, tagService.serviceKey).merge(statuses)
			}
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

	tagsByID, err := b.lookupTagsByID(ctx, dedupeInt64s(seenTagIDs))
	if err != nil {
		return nil, err
	}

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

	query := fmt.Sprintf(
		`SELECT tag_id, tag
		FROM external_master.tags
		WHERE tag_id IN (%s)`,
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
			tagID int64
			tag   string
		)

		if err := rows.Scan(&tagID, &tag); err != nil {
			return nil, fmt.Errorf("scan tags by ID row: %w", err)
		}

		tagsByID[tagID] = tag
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tags by ID rows: %w", err)
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
