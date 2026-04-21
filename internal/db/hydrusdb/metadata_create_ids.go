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
	resolved := map[string]int64{}
	err := b.WithImmediateTx(ctx, func(tx *ImmediateTx) error {
		var err error
		resolved, err = ensureHashIDsTx(ctx, tx, hashes)
		return err
	})
	if err != nil {
		return nil, err
	}

	return resolved, nil
}

// ensureHashIDsTx resolves or creates master hash IDs inside the caller's write
// transaction so higher-level workflows can keep hash creation atomic with their
// own guarded state changes.
func ensureHashIDsTx(
	ctx context.Context,
	tx *ImmediateTx,
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
	for _, hash := range normalizedHashes {
		hashID, exists, err := lookupHashIDByHash(ctx, tx, hashBytesByHash[hash])
		if err != nil {
			return nil, err
		}

		if !exists {
			if _, err := tx.ExecContext(
				ctx,
				`INSERT OR IGNORE INTO external_master.hashes (hash) VALUES (?)`,
				hashBytesByHash[hash],
			); err != nil {
				return nil, fmt.Errorf("insert external_master.hashes row: %w", err)
			}

			hashID, exists, err = lookupHashIDByHash(ctx, tx, hashBytesByHash[hash])
			if err != nil {
				return nil, err
			}

			if !exists {
				return nil, errors.New("inserted hash row was not readable inside transaction")
			}
		}

		resolved[hash] = hashID
	}

	return resolved, nil
}

// EnsureHashIDs creates master hash IDs for unknown hashes on a writable bundle.
func (b *Bundle) EnsureHashIDs(ctx context.Context, hashes []string) error {
	_, err := b.ensureHashIDs(ctx, hashes)
	return err
}
