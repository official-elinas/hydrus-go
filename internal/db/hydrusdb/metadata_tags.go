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

type tagStatusSets map[int]map[string]struct{}

func newTagStatusSets() tagStatusSets {
	return tagStatusSets{}
}

func (s tagStatusSets) add(status int, tag string) {
	if _, ok := s[status]; !ok {
		s[status] = map[string]struct{}{}
	}

	s[status][tag] = struct{}{}
}

func (s tagStatusSets) merge(other tagStatusSets) {
	for status, tags := range other {
		for tag := range tags {
			s.add(status, tag)
		}
	}
}

func (s tagStatusSets) payload() map[string][]string {
	payload := map[string][]string{}
	for _, status := range tagStatusOrder {
		tags := s[status]
		if len(tags) == 0 {
			continue
		}

		sortedTags := make([]string, 0, len(tags))
		for tag := range tags {
			sortedTags = append(sortedTags, tag)
		}

		slices.Sort(sortedTags)
		payload[strconv.Itoa(status)] = sortedTags
	}

	return payload
}

func (b *Bundle) lookupFileTags(
	ctx context.Context,
	fileIDs []int64,
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

	tagsByHashID := map[int64]map[string]tagStatusSets{}
	for _, service := range realTagServices {
		serviceTagsByHashID, err := b.lookupTagServiceMappings(
			ctx,
			uniqueFileIDs,
			service,
			mappingTableNames,
		)
		if err != nil {
			return nil, err
		}

		for hashID, statuses := range serviceTagsByHashID {
			if _, ok := tagsByHashID[hashID]; !ok {
				tagsByHashID[hashID] = map[string]tagStatusSets{}
			}

			tagsByHashID[hashID][service.serviceKey] = statuses
		}
	}

	for _, hashID := range uniqueFileIDs {
		payload := payloads[hashID]
		serviceTags := tagsByHashID[hashID]
		combinedTags := newTagStatusSets()

		for _, service := range realTagServices {
			statuses, ok := serviceTags[service.serviceKey]
			if !ok {
				statuses = newTagStatusSets()
			}

			combinedTags.merge(statuses)

			storageTags := statuses.payload()
			// Hydrus display-tag parity ultimately needs display-cache reads so
			// sibling/parent processing can diverge from raw storage mappings.
			// This slice keeps the daemon-first boundary intact by surfacing the
			// storage view for both fields until that cache-backed path lands.
			displayTags := statuses.payload()
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
			storageTags := combinedTags.payload()
			// Combined display tags intentionally mirror the current storage
			// approximation for the same reason as the per-service payload above.
			displayTags := combinedTags.payload()
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

func (b *Bundle) lookupTagServiceMappings(
	ctx context.Context,
	fileIDs []int64,
	service serviceDefinition,
	tableNames map[string]struct{},
) (map[int64]tagStatusSets, error) {
	mappings := map[int64]tagStatusSets{}
	for _, query := range []struct {
		status    int
		tableName string
	}{
		{status: contentStatusCurrent, tableName: fmt.Sprintf("current_mappings_%d", service.id)},
		{status: contentStatusPending, tableName: fmt.Sprintf("pending_mappings_%d", service.id)},
		{status: contentStatusDeleted, tableName: fmt.Sprintf("deleted_mappings_%d", service.id)},
		{status: contentStatusPetitioned, tableName: fmt.Sprintf("petitioned_mappings_%d", service.id)},
	} {
		if _, ok := tableNames[query.tableName]; !ok {
			continue
		}

		if err := b.collectTagMappingsFromTable(
			ctx,
			fileIDs,
			query.tableName,
			query.status,
			mappings,
		); err != nil {
			return nil, err
		}
	}

	return mappings, nil
}

func (b *Bundle) collectTagMappingsFromTable(
	ctx context.Context,
	fileIDs []int64,
	tableName string,
	status int,
	mappings map[int64]tagStatusSets,
) error {
	query := fmt.Sprintf(
		`SELECT m.hash_id, t.tag
		FROM external_mappings.%s m
		JOIN external_master.tags t USING (tag_id)
		WHERE m.hash_id IN (%s)`,
		tableName,
		placeholders(len(fileIDs)),
	)

	rows, err := b.conn.QueryContext(ctx, query, int64Args(fileIDs)...)
	if err != nil {
		return fmt.Errorf("query tag mappings %s: %w", tableName, err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			hashID int64
			tag    string
		)

		if err := rows.Scan(&hashID, &tag); err != nil {
			return fmt.Errorf("scan tag mapping %s row: %w", tableName, err)
		}

		if _, ok := mappings[hashID]; !ok {
			mappings[hashID] = newTagStatusSets()
		}

		mappings[hashID].add(status, tag)
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate tag mapping %s rows: %w", tableName, err)
	}

	return nil
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
