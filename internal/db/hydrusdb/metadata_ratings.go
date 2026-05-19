package hydrusdb

import (
	"context"
	"database/sql"
	"fmt"
	"math"

	"github.com/official-elinas/hydrus-go/internal/core/services"
)

type ratingServiceDefinition struct {
	id      int64
	service services.Service
}

func (b *Bundle) lookupFileRatings(
	ctx context.Context,
	fileIDs []int64,
) (map[int64]map[string]any, error) {
	payloads := map[int64]map[string]any{}
	if len(fileIDs) == 0 {
		return payloads, nil
	}

	uniqueFileIDs := dedupeInt64s(fileIDs)
	ratingServices, err := b.lookupRatingServiceDefinitions(ctx)
	if err != nil {
		return nil, err
	}

	serviceByID := map[int64]ratingServiceDefinition{}
	serviceIDs := make([]int64, 0, len(ratingServices))
	for _, definition := range ratingServices {
		serviceByID[definition.id] = definition
		serviceIDs = append(serviceIDs, definition.id)
	}

	for _, hashID := range uniqueFileIDs {
		payloads[hashID] = buildDefaultRatingsPayload(ratingServices)
	}

	if len(ratingServices) == 0 {
		return payloads, nil
	}

	mainTableNames, err := b.lookupMainTableNames(ctx)
	if err != nil {
		return nil, err
	}

	if _, ok := mainTableNames["local_ratings"]; ok {
		if err := b.collectLocalRatings(
			ctx,
			uniqueFileIDs,
			serviceIDs,
			serviceByID,
			payloads,
		); err != nil {
			return nil, err
		}
	}

	if _, ok := mainTableNames["local_incdec_ratings"]; ok {
		if err := b.collectLocalIncDecRatings(
			ctx,
			uniqueFileIDs,
			serviceIDs,
			serviceByID,
			payloads,
		); err != nil {
			return nil, err
		}
	}

	return payloads, nil
}

func buildDefaultRatingsPayload(
	ratingServices []ratingServiceDefinition,
) map[string]any {
	payload := map[string]any{}
	for _, definition := range ratingServices {
		payload[definition.service.ServiceKey] = defaultRatingValue(
			definition.service.Type,
		)
	}

	return payload
}

func defaultRatingValue(serviceType services.Type) any {
	if serviceType == services.TypeLocalRatingIncDec {
		return 0
	}

	return nil
}

func (b *Bundle) lookupRatingServiceDefinitions(
	ctx context.Context,
) ([]ratingServiceDefinition, error) {
	ratingTypes := []services.Type{
		services.TypeLocalRatingNumerical,
		services.TypeLocalRatingLike,
		services.TypeRatingNumericalRepository,
		services.TypeRatingLikeRepository,
		services.TypeLocalRatingIncDec,
	}
	args := make([]any, 0, len(ratingTypes))
	for _, ratingType := range ratingTypes {
		args = append(args, int(ratingType))
	}

	query := fmt.Sprintf(
		`SELECT service_id, lower(hex(service_key)), service_type, name, dictionary_string
		FROM main.services
		WHERE service_type IN (%s)
		ORDER BY service_id ASC`,
		placeholders(len(args)),
	)

	rows, err := b.conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query rating service definitions: %w", err)
	}
	defer rows.Close()

	definitions := []ratingServiceDefinition{}
	for rows.Next() {
		var (
			serviceID        int64
			serviceKey       string
			serviceType      int
			name             string
			dictionaryString sql.NullString
		)

		if err := rows.Scan(
			&serviceID,
			&serviceKey,
			&serviceType,
			&name,
			&dictionaryString,
		); err != nil {
			return nil, fmt.Errorf("scan rating service definition row: %w", err)
		}

		service := services.Service{
			Name:       name,
			ServiceKey: serviceKey,
			Type:       services.Type(serviceType),
			TypePretty: services.TypePretty(services.Type(serviceType)),
		}

		if dictionaryString.Valid {
			if err := applyServiceExtras(
				service.Type,
				dictionaryString.String,
				&service,
			); err != nil {
				return nil, fmt.Errorf(
					"parse rating service %q extras: %w",
					serviceKey,
					err,
				)
			}
		}

		definitions = append(definitions, ratingServiceDefinition{
			id:      serviceID,
			service: service,
		})
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rating service definition rows: %w", err)
	}

	return definitions, nil
}

func (b *Bundle) collectLocalRatings(
	ctx context.Context,
	fileIDs []int64,
	serviceIDs []int64,
	serviceByID map[int64]ratingServiceDefinition,
	payloads map[int64]map[string]any,
) error {
	if len(fileIDs) == 0 || len(serviceIDs) == 0 {
		return nil
	}

	args := int64Args(fileIDs)
	args = append(args, int64Args(serviceIDs)...)

	query := fmt.Sprintf(
		`SELECT service_id, hash_id, rating
		FROM main.local_ratings
		WHERE hash_id IN (%s)
		AND service_id IN (%s)`,
		placeholders(len(fileIDs)),
		placeholders(len(serviceIDs)),
	)

	rows, err := b.conn.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("query local ratings: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			serviceID int64
			hashID    int64
			rating    float64
		)

		if err := rows.Scan(&serviceID, &hashID, &rating); err != nil {
			return fmt.Errorf("scan local rating row: %w", err)
		}

		definition, ok := serviceByID[serviceID]
		if !ok {
			continue
		}

		value, ok := localRatingValueForAPI(definition.service, rating)
		if !ok {
			continue
		}

		payloads[hashID][definition.service.ServiceKey] = value
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate local rating rows: %w", err)
	}

	return nil
}

func localRatingValueForAPI(
	service services.Service,
	rating float64,
) (any, bool) {
	switch service.Type {
	case services.TypeLocalRatingLike, services.TypeRatingLikeRepository:
		return rating >= 0.5, true
	case services.TypeLocalRatingNumerical, services.TypeRatingNumericalRepository:
		stars, ok := convertRatingToStars(service, rating)
		if !ok {
			return nil, true
		}

		return stars, true
	default:
		return nil, false
	}
}

func convertRatingToStars(
	service services.Service,
	rating float64,
) (int, bool) {
	if service.MaxStars == nil || *service.MaxStars <= 0 {
		return 0, false
	}

	maxStars := *service.MaxStars
	allowZero := service.AllowsZero != nil && *service.AllowsZero
	if allowZero {
		return int(math.Round(rating * float64(maxStars))), true
	}

	if maxStars == 1 {
		return 1, true
	}

	return int(math.Round(rating*float64(maxStars-1))) + 1, true
}

func (b *Bundle) lookupFileRatingsConn(
	ctx context.Context,
	_ *sql.Conn,
	fileIDs []int64,
) (map[int64]map[string]any, error) {
	return b.lookupFileRatings(ctx, fileIDs)
}

func (b *Bundle) collectLocalIncDecRatings(
	ctx context.Context,
	fileIDs []int64,
	serviceIDs []int64,
	serviceByID map[int64]ratingServiceDefinition,
	payloads map[int64]map[string]any,
) error {
	if len(fileIDs) == 0 || len(serviceIDs) == 0 {
		return nil
	}

	args := int64Args(fileIDs)
	args = append(args, int64Args(serviceIDs)...)

	query := fmt.Sprintf(
		`SELECT service_id, hash_id, rating
		FROM main.local_incdec_ratings
		WHERE hash_id IN (%s)
		AND service_id IN (%s)`,
		placeholders(len(fileIDs)),
		placeholders(len(serviceIDs)),
	)

	rows, err := b.conn.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("query local inc/dec ratings: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			serviceID int64
			hashID    int64
			rating    int
		)

		if err := rows.Scan(&serviceID, &hashID, &rating); err != nil {
			return fmt.Errorf("scan local inc/dec rating row: %w", err)
		}

		definition, ok := serviceByID[serviceID]
		if !ok || definition.service.Type != services.TypeLocalRatingIncDec {
			continue
		}

		payloads[hashID][definition.service.ServiceKey] = rating
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate local inc/dec rating rows: %w", err)
	}

	return nil
}
