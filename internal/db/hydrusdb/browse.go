package hydrusdb

import (
	"context"
	"database/sql"
	"fmt"
	"os"

	"github.com/official-elinas/hydrus-go/internal/core/librarybrowse"
	"github.com/official-elinas/hydrus-go/internal/core/mimes"
	"github.com/official-elinas/hydrus-go/internal/core/services"
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

	rows, err := b.conn.QueryContext(ctx, query, request.Limit+1, request.Offset)
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
			return tableName, nil
		}
	}

	return "", &librarybrowse.UnsupportedError{
		Message: "recent local browse is unavailable for this Hydrus bundle",
	}
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
