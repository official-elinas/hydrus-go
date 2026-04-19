package hydrusdb

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/official-elinas/hydrus-go/internal/storage/clientfiles"
)

// ManagedLayout resolves the bundle's current managed storage layout from the
// Hydrus storage tables.
func (b *Bundle) ManagedLayout(ctx context.Context) (clientfiles.Layout, error) {
	b.managedLayoutMu.RLock()
	if b.hasManagedLayout {
		layout := b.managedLayout
		b.managedLayoutMu.RUnlock()
		return layout, nil
	}
	b.managedLayoutMu.RUnlock()

	granularity, err := b.lookupStorageGranularity(ctx)
	if err != nil {
		return clientfiles.Layout{}, err
	}

	prefixRoots, err := b.lookupManagedPrefixRoots(ctx)
	if err != nil {
		return clientfiles.Layout{}, err
	}

	layout, err := clientfiles.NewPrefixLayout(granularity, prefixRoots)
	if err != nil {
		return clientfiles.Layout{}, fmt.Errorf("build managed storage layout: %w", err)
	}

	b.managedLayoutMu.Lock()
	b.managedLayout = layout
	b.hasManagedLayout = true
	b.managedLayoutMu.Unlock()

	return layout, nil
}

func (b *Bundle) lookupStorageGranularity(ctx context.Context) (int, error) {
	row := b.conn.QueryRowContext(
		ctx,
		`SELECT granularity FROM main.current_storage_granularity LIMIT 1`,
	)

	var granularity int
	if err := row.Scan(&granularity); err != nil {
		if err == sql.ErrNoRows {
			return 0, fmt.Errorf("managed storage granularity is missing")
		}

		return 0, fmt.Errorf("query managed storage granularity: %w", err)
	}

	return granularity, nil
}

func (b *Bundle) lookupManagedPrefixRoots(ctx context.Context) (map[string]string, error) {
	rows, err := b.conn.QueryContext(
		ctx,
		`SELECT cfs.prefix, cfl.location
		FROM main.client_files_subfolders cfs
		JOIN main.current_client_files_locations cfl USING (location_id)
		ORDER BY cfs.rowid ASC, cfl.location_id ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("query managed storage prefixes: %w", err)
	}
	defer rows.Close()

	baseDir := filepath.Dir(b.paths.main)
	prefixRoots := map[string]string{}
	for rows.Next() {
		var (
			prefix   string
			location string
		)

		if err := rows.Scan(&prefix, &location); err != nil {
			return nil, fmt.Errorf("scan managed storage prefix: %w", err)
		}

		prefix = strings.ToLower(strings.TrimSpace(prefix))
		if _, exists := prefixRoots[prefix]; exists {
			continue
		}

		resolved, err := resolveManagedLocation(baseDir, location)
		if err != nil {
			return nil, fmt.Errorf("resolve managed storage location for prefix %q: %w", prefix, err)
		}

		prefixRoots[prefix] = resolved
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate managed storage prefixes: %w", err)
	}

	return prefixRoots, nil
}

func resolveManagedLocation(baseDir string, location string) (string, error) {
	trimmed := strings.TrimSpace(location)
	if trimmed == "" {
		return "", fmt.Errorf("managed storage location is required")
	}

	normalized := filepath.Clean(trimmed)
	if filepath.IsAbs(normalized) {
		return normalized, nil
	}

	absPath := filepath.Clean(filepath.Join(baseDir, normalized))
	if runtime.GOOS != "windows" {
		if _, err := os.Stat(absPath); err != nil && os.IsNotExist(err) {
			return filepath.Clean(strings.ReplaceAll(absPath, `\`, `/`)), nil
		}
	}

	return absPath, nil
}
