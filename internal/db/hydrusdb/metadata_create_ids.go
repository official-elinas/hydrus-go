package hydrusdb

import (
	"context"
	"errors"
	"fmt"
)

func (b *Bundle) ensureHashIDs(
	ctx context.Context,
	hashes []string,
) (map[string]int64, error) {
	if len(hashes) == 0 {
		return map[string]int64{}, nil
	}

	normalizedHashes := make([]string, 0, len(hashes))
	hashBytesByHash := make(map[string][]byte, len(hashes))
	seen := map[string]struct{}{}
	for _, hash := range hashes {
		normalized, hashBytes, err := normalizePreparedHash(hash)
		if err != nil {
			return nil, fmt.Errorf("normalize metadata hash %q: %w", hash, err)
		}

		if _, ok := seen[normalized]; ok {
			continue
		}

		seen[normalized] = struct{}{}
		normalizedHashes = append(normalizedHashes, normalized)
		hashBytesByHash[normalized] = hashBytes
	}

	resolved := make(map[string]int64, len(normalizedHashes))
	err := b.WithImmediateTx(ctx, func(tx *ImmediateTx) error {
		for _, hash := range normalizedHashes {
			hashID, exists, err := lookupHashIDByHash(ctx, tx, hashBytesByHash[hash])
			if err != nil {
				return err
			}

			if !exists {
				if _, err := tx.ExecContext(
					ctx,
					`INSERT OR IGNORE INTO external_master.hashes (hash) VALUES (?)`,
					hashBytesByHash[hash],
				); err != nil {
					return fmt.Errorf("insert external_master.hashes row: %w", err)
				}

				hashID, exists, err = lookupHashIDByHash(ctx, tx, hashBytesByHash[hash])
				if err != nil {
					return err
				}

				if !exists {
					return errors.New("inserted hash row was not readable inside transaction")
				}
			}

			resolved[hash] = hashID
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return resolved, nil
}

// EnsureHashIDs creates master hash IDs for unknown hashes on a writable bundle.
func (b *Bundle) EnsureHashIDs(ctx context.Context, hashes []string) error {
	_, err := b.ensureHashIDs(ctx, hashes)
	return err
}
