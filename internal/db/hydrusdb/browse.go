package hydrusdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/official-elinas/hydrus-go/internal/core/librarybrowse"
	"github.com/official-elinas/hydrus-go/internal/core/mimes"
	"github.com/official-elinas/hydrus-go/internal/core/services"
	coretags "github.com/official-elinas/hydrus-go/internal/core/tags"
	"github.com/official-elinas/hydrus-go/internal/storage/clientfiles"
)

// ListRecent returns a paged browse view of recent local files for the thin
// desktop client MVP.
func (b *Bundle) ListRecent(
	ctx context.Context,
	request librarybrowse.Request,
) (librarybrowse.Page, error) {
	if request.Offset < 0 {
		return librarybrowse.Page{}, fmt.Errorf("browse offset must be non-negative")
	}

	if request.Limit <= 0 {
		return librarybrowse.Page{}, fmt.Errorf("browse limit must be greater than zero")
	}

	conn, err := b.acquireReadConn(ctx)
	if err != nil {
		return librarybrowse.Page{}, err
	}
	defer b.releaseReadConn(conn)

	return b.listRecentWithConn(ctx, conn, request)
}

func (b *Bundle) listRecentWithConn(
	ctx context.Context,
	conn *sql.Conn,
	request librarybrowse.Request,
) (librarybrowse.Page, error) {
	tableName, err := b.resolveRecentBrowseTable(ctx)
	if err != nil {
		return librarybrowse.Page{}, err
	}

	layout, err := b.ManagedLayout(ctx)
	if err != nil {
		return librarybrowse.Page{}, err
	}

	query := fmt.Sprintf(
		`SELECT cf.hash_id,
			lower(hex(h.hash)),
			fi.mime,
			fi.width,
			fi.height,
			cf.timestamp_ms
		FROM main.%s cf
		JOIN external_master.hashes h USING (hash_id)
		JOIN main.files_info fi USING (hash_id)
		ORDER BY
			CASE WHEN cf.timestamp_ms IS NULL THEN 1 ELSE 0 END ASC,
			cf.timestamp_ms DESC,
			cf.hash_id DESC
		LIMIT ? OFFSET ?`,
		tableName,
	)

	rows, err := conn.QueryContext(ctx, query, request.Limit+1, request.Offset)
	if err != nil {
		return librarybrowse.Page{}, fmt.Errorf("query recent local files: %w", err)
	}
	defer rows.Close()

	page := librarybrowse.Page{Items: []librarybrowse.Item{}}
	for rows.Next() {
		var (
			hashID       int64
			hash         string
			mime         int64
			width        sql.NullInt64
			height       sql.NullInt64
			importedAtMS sql.NullInt64
		)

		if err := rows.Scan(
			&hashID,
			&hash,
			&mime,
			&width,
			&height,
			&importedAtMS,
		); err != nil {
			return librarybrowse.Page{}, fmt.Errorf("scan recent local file row: %w", err)
		}

		if len(page.Items) == request.Limit {
			page.HasMore = true
			break
		}

		hasThumbnail, err := managedThumbnailExists(layout, hash)
		if err != nil {
			return librarybrowse.Page{}, err
		}

		page.Items = append(page.Items, librarybrowse.Item{
			FileID:       hashID,
			Hash:         hash,
			MIME:         mimes.Lookup(int(mime)).Mimetype,
			Width:        nullableInt64Pointer(width),
			Height:       nullableInt64Pointer(height),
			ImportedAtMS: nullableInt64Pointer(importedAtMS),
			HasThumbnail: hasThumbnail,
		})
	}

	if err := rows.Err(); err != nil {
		return librarybrowse.Page{}, fmt.Errorf("iterate recent local file rows: %w", err)
	}

	return page, nil
}

func (b *Bundle) resolveRecentBrowseTable(ctx context.Context) (string, error) {
	b.recentBrowseTableMu.RLock()
	if b.hasRecentBrowseTable {
		tableName := b.recentBrowseTable
		b.recentBrowseTableMu.RUnlock()
		return tableName, nil
	}
	b.recentBrowseTableMu.RUnlock()

	tableNames, err := b.lookupMainTableNames(ctx)
	if err != nil {
		return "", err
	}

	definitions, err := b.lookupAllServiceDefinitions(ctx)
	if err != nil {
		return "", err
	}

	if service, ok, err := findUniqueServiceByType(
		definitions,
		services.TypeCombinedLocalFileDomains,
	); err != nil {
		return "", err
	} else if ok {
		tableName := fmt.Sprintf("current_files_%d", service.id)
		if _, exists := tableNames[tableName]; exists {
			b.cacheRecentBrowseTable(tableName)
			return tableName, nil
		}
	}

	if service, ok, err := findUniqueServiceByType(
		definitions,
		services.TypeLocalFileDomain,
	); err != nil {
		return "", err
	} else if ok {
		tableName := fmt.Sprintf("current_files_%d", service.id)
		if _, exists := tableNames[tableName]; exists {
			b.cacheRecentBrowseTable(tableName)
			return tableName, nil
		}
	}

	if service, ok, err := findUniqueServiceByType(
		definitions,
		services.TypeHydrusLocalFileStorage,
	); err != nil {
		return "", err
	} else if ok {
		tableName := fmt.Sprintf("current_files_%d", service.id)
		if _, exists := tableNames[tableName]; exists {
			b.cacheRecentBrowseTable(tableName)
			return tableName, nil
		}
	}

	return "", &librarybrowse.UnsupportedError{
		Message: "recent local browse is unavailable for this Hydrus bundle",
	}
}

func (b *Bundle) cacheRecentBrowseTable(tableName string) {
	if strings.TrimSpace(tableName) == "" {
		return
	}

	b.recentBrowseTableMu.Lock()
	b.recentBrowseTable = tableName
	b.hasRecentBrowseTable = true
	b.recentBrowseTableMu.Unlock()
}

func managedThumbnailExists(layout clientfiles.Layout, hash string) (bool, error) {
	thumbnailPath, err := layout.ResolveThumbnailPath(hash)
	if err != nil {
		return false, fmt.Errorf("resolve managed thumbnail path: %w", err)
	}

	if _, err := os.Stat(thumbnailPath); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}

		return false, fmt.Errorf("stat managed thumbnail path: %w", err)
	}

	return true, nil
}

func nullableInt64Pointer(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}

	result := value.Int64
	return &result
}

// SearchByTags returns a paged browse view of local files that have all of the
// requested tags applied across any local tag service.
func (b *Bundle) SearchByTags(
	ctx context.Context,
	request librarybrowse.SearchRequest,
) (librarybrowse.Page, error) {
	if request.Offset < 0 {
		return librarybrowse.Page{}, fmt.Errorf("browse offset must be non-negative")
	}

	if request.Limit <= 0 {
		return librarybrowse.Page{}, fmt.Errorf("browse limit must be greater than zero")
	}

	if len(request.Tags) == 0 && len(request.SystemPredicates) == 0 && request.SortBy == "" && request.FavoriteFilter == nil {
		return b.ListRecent(ctx, request.Request)
	}

	conn, err := b.acquireReadConn(ctx)
	if err != nil {
		return librarybrowse.Page{}, err
	}
	defer b.releaseReadConn(conn)

	return b.searchByTagsWithConn(ctx, conn, request)
}

func (b *Bundle) searchByTagsWithConn(
	ctx context.Context,
	conn *sql.Conn,
	request librarybrowse.SearchRequest,
) (librarybrowse.Page, error) {
	currentFilesTable, err := b.resolveRecentBrowseTable(ctx)
	if err != nil {
		return librarybrowse.Page{}, err
	}

	layout, err := b.ManagedLayout(ctx)
	if err != nil {
		return librarybrowse.Page{}, err
	}

	mappingTableNames, err := b.lookupSchemaTableNames(ctx, "external_mappings")
	if err != nil {
		return librarybrowse.Page{}, fmt.Errorf("lookup mapping table names: %w", err)
	}

	var mappingTables []string
	for name := range mappingTableNames {
		if strings.HasPrefix(name, "current_mappings_") {
			mappingTables = append(mappingTables, name)
		}
	}

	if len(mappingTables) == 0 {
		return librarybrowse.Page{Items: []librarybrowse.Item{}, HasMore: false}, nil
	}

	schemaMode, err := b.lookupMasterTagSchemaMode(ctx)
	if err != nil {
		return librarybrowse.Page{}, fmt.Errorf("lookup tag schema mode: %w", err)
	}

	tagIDs := make([]int64, 0, len(request.Tags))
	for _, rawTag := range request.Tags {
		tagID, found, err := b.lookupTagIDReadOnlyConn(ctx, conn, schemaMode, rawTag)
		if err != nil {
			return librarybrowse.Page{}, fmt.Errorf("lookup tag id for %q: %w", rawTag, err)
		}

		if !found {
			return librarybrowse.Page{Items: []librarybrowse.Item{}, HasMore: false}, nil
		}

		tagIDs = append(tagIDs, tagID)
	}

	existsClauses := make([]string, 0, len(tagIDs))
	args := make([]any, 0, len(tagIDs)+2)
	for _, tagID := range tagIDs {
		orParts := make([]string, 0, len(mappingTables))
		for _, tbl := range mappingTables {
			orParts = append(orParts,
				fmt.Sprintf(
					"EXISTS (SELECT 1 FROM external_mappings.%s m WHERE m.hash_id = cf.hash_id AND m.tag_id = ?)",
					tbl,
				),
			)
			args = append(args, tagID)
		}

		existsClauses = append(existsClauses, "("+strings.Join(orParts, " OR ")+")")
	}

	for _, pred := range request.SystemPredicates {
		col, ok := allowedPredicateColumn(pred.Field)
		if !ok {
			continue
		}

		op, ok := allowedPredicateOp(pred.Op)
		if !ok {
			continue
		}

		existsClauses = append(existsClauses, fmt.Sprintf("fi.%s %s ?", col, op))
		args = append(args, pred.Value)
	}

	if request.FavoriteFilter != nil {
		likeTypeLocal := int(services.TypeLocalRatingLike)
		likeTypeRepo := int(services.TypeRatingLikeRepository)
		favoriteExistsClause := fmt.Sprintf(
			`EXISTS (
				SELECT 1 FROM main.local_ratings lr
				JOIN main.services svc ON svc.service_id = lr.service_id
				WHERE lr.hash_id = cf.hash_id
				AND svc.service_type IN (?, ?)
				AND lr.rating >= 0.5
			)`,
		)
		if *request.FavoriteFilter {
			existsClauses = append(existsClauses, favoriteExistsClause)
		} else {
			existsClauses = append(existsClauses, "NOT "+favoriteExistsClause)
		}
		args = append(args, likeTypeLocal, likeTypeRepo)
	}

	whereClause := strings.Join(existsClauses, " AND ")

	orderClause := buildOrderClause(request.SortBy)

	var query string
	if whereClause == "" {
		query = fmt.Sprintf(
			`SELECT cf.hash_id,
			lower(hex(h.hash)),
			fi.mime,
			fi.width,
			fi.height,
			cf.timestamp_ms
		FROM main.%s cf
		JOIN external_master.hashes h USING (hash_id)
		JOIN main.files_info fi USING (hash_id)
		ORDER BY %s
		LIMIT ? OFFSET ?`,
			currentFilesTable,
			orderClause,
		)
	} else {
		query = fmt.Sprintf(
			`SELECT cf.hash_id,
			lower(hex(h.hash)),
			fi.mime,
			fi.width,
			fi.height,
			cf.timestamp_ms
		FROM main.%s cf
		JOIN external_master.hashes h USING (hash_id)
		JOIN main.files_info fi USING (hash_id)
		WHERE %s
		ORDER BY %s
		LIMIT ? OFFSET ?`,
			currentFilesTable,
			whereClause,
			orderClause,
		)
	}

	args = append(args, request.Limit+1, request.Offset)

	rows, err := conn.QueryContext(ctx, query, args...)
	if err != nil {
		return librarybrowse.Page{}, fmt.Errorf("query tag-search local files: %w", err)
	}
	defer rows.Close()

	page := librarybrowse.Page{Items: []librarybrowse.Item{}}
	for rows.Next() {
		var (
			hashID       int64
			hash         string
			mime         int64
			width        sql.NullInt64
			height       sql.NullInt64
			importedAtMS sql.NullInt64
		)

		if err := rows.Scan(
			&hashID,
			&hash,
			&mime,
			&width,
			&height,
			&importedAtMS,
		); err != nil {
			return librarybrowse.Page{}, fmt.Errorf("scan tag-search local file row: %w", err)
		}

		if len(page.Items) == request.Limit {
			page.HasMore = true
			break
		}

		hasThumbnail, err := managedThumbnailExists(layout, hash)
		if err != nil {
			return librarybrowse.Page{}, err
		}

		page.Items = append(page.Items, librarybrowse.Item{
			FileID:       hashID,
			Hash:         hash,
			MIME:         mimes.Lookup(int(mime)).Mimetype,
			Width:        nullableInt64Pointer(width),
			Height:       nullableInt64Pointer(height),
			ImportedAtMS: nullableInt64Pointer(importedAtMS),
			HasThumbnail: hasThumbnail,
		})
	}

	if err := rows.Err(); err != nil {
		return librarybrowse.Page{}, fmt.Errorf("iterate tag-search local file rows: %w", err)
	}

	return page, nil
}

func (b *Bundle) lookupTagIDReadOnlyConn(
	ctx context.Context,
	conn *sql.Conn,
	schemaMode masterTagSchemaMode,
	tag string,
) (int64, bool, error) {
	cleanTag := coretags.Clean(tag)
	if err := coretags.CheckNotEmpty(cleanTag); err != nil {
		return 0, false, nil
	}

	switch schemaMode {
	case masterTagSchemaLegacyFlat:
		row := conn.QueryRowContext(
			ctx,
			`SELECT tag_id FROM external_master.tags WHERE tag = ?`,
			cleanTag,
		)

		var tagID int64
		if err := row.Scan(&tagID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return 0, false, nil
			}

			return 0, false, fmt.Errorf("query legacy flat tag id: %w", err)
		}

		return tagID, true, nil

	case masterTagSchemaSplit:
		namespace, subtag := coretags.Split(cleanTag)

		row := conn.QueryRowContext(
			ctx,
			`SELECT t.tag_id
			FROM external_master.tags t
			JOIN external_master.namespaces n ON n.namespace_id = t.namespace_id
			JOIN external_master.subtags s ON s.subtag_id = t.subtag_id
			WHERE n.namespace = ? AND s.subtag = ?`,
			namespace,
			subtag,
		)

		var tagID int64
		if err := row.Scan(&tagID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return 0, false, nil
			}

			return 0, false, fmt.Errorf("query split tag id: %w", err)
		}

		return tagID, true, nil

	default:
		return 0, false, nil
	}
}

func allowedPredicateColumn(field librarybrowse.PredicateField) (string, bool) {
	switch field {
	case librarybrowse.PredicateFieldSize:
		return "size", true
	case librarybrowse.PredicateFieldWidth:
		return "width", true
	case librarybrowse.PredicateFieldHeight:
		return "height", true
	default:
		return "", false
	}
}

func allowedPredicateOp(op librarybrowse.PredicateOp) (string, bool) {
	switch op {
	case librarybrowse.PredicateOpLT:
		return "<", true
	case librarybrowse.PredicateOpLTE:
		return "<=", true
	case librarybrowse.PredicateOpEQ:
		return "=", true
	case librarybrowse.PredicateOpGTE:
		return ">=", true
	case librarybrowse.PredicateOpGT:
		return ">", true
	default:
		return "", false
	}
}

func buildOrderClause(sortBy librarybrowse.SortBy) string {
	switch sortBy {
	case librarybrowse.SortBySizeDesc:
		return "CASE WHEN fi.size IS NULL THEN 1 ELSE 0 END ASC, fi.size DESC, cf.hash_id DESC"
	case librarybrowse.SortBySizeAsc:
		return "CASE WHEN fi.size IS NULL THEN 1 ELSE 0 END ASC, fi.size ASC, cf.hash_id DESC"
	case librarybrowse.SortByImportOldest:
		return "CASE WHEN cf.timestamp_ms IS NULL THEN 1 ELSE 0 END ASC, cf.timestamp_ms ASC, cf.hash_id ASC"
	default:
		return "CASE WHEN cf.timestamp_ms IS NULL THEN 1 ELSE 0 END ASC, cf.timestamp_ms DESC, cf.hash_id DESC"
	}
}
