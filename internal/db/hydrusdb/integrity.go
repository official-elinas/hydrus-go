package hydrusdb

import (
	"context"
	"fmt"

	"github.com/official-elinas/hydrus-go/internal/core/filemetadata"
)

// CheckIntegrity runs SQLite PRAGMA integrity_check against the opened bundle.
func (b *Bundle) CheckIntegrity(
	ctx context.Context,
) (filemetadata.IntegrityCheckResult, error) {
	if b == nil || b.conn == nil {
		return filemetadata.IntegrityCheckResult{}, fmt.Errorf("hydrus bundle is unavailable")
	}

	rows, err := b.conn.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		return filemetadata.IntegrityCheckResult{}, fmt.Errorf("run sqlite integrity_check: %w", err)
	}
	defer rows.Close()

	results := []string{}
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			return filemetadata.IntegrityCheckResult{}, fmt.Errorf("scan sqlite integrity_check row: %w", err)
		}

		results = append(results, result)
	}

	if err := rows.Err(); err != nil {
		return filemetadata.IntegrityCheckResult{}, fmt.Errorf("iterate sqlite integrity_check rows: %w", err)
	}

	return filemetadata.IntegrityCheckResult{
		Passed:  len(results) == 1 && results[0] == "ok",
		Results: results,
	}, nil
}
