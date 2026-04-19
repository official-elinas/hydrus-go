package hydrusdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"

	"github.com/official-elinas/hydrus-go/internal/core/fileassets"
	"github.com/official-elinas/hydrus-go/internal/core/mimes"
)

// ResolveFileContent resolves the managed original-file asset path for a file
// identifier.
func (b *Bundle) ResolveFileContent(
	ctx context.Context,
	fileID int64,
) (fileassets.Descriptor, error) {
	hash, mime, forcedMime, err := b.lookupManagedFileDescriptor(ctx, fileID)
	if err != nil {
		return fileassets.Descriptor{}, err
	}

	layout, err := b.ManagedLayout(ctx)
	if err != nil {
		return fileassets.Descriptor{}, err
	}

	candidateMimes := []int64{mime}
	if forcedMime.Valid && forcedMime.Int64 != mime {
		candidateMimes = append(candidateMimes, forcedMime.Int64)
	}

	for _, candidateMime := range candidateMimes {
		info := mimes.Lookup(int(candidateMime))
		if info.Ext == "" {
			continue
		}

		path, err := layout.ResolveFilePath(hash, info.Ext)
		if err != nil {
			return fileassets.Descriptor{}, fmt.Errorf("resolve managed file path: %w", err)
		}

		if _, err := os.Stat(path); err != nil {
			if os.IsNotExist(err) {
				continue
			}

			return fileassets.Descriptor{}, fmt.Errorf("stat managed file path: %w", err)
		}

		return fileassets.Descriptor{
			FileID:   fileID,
			Hash:     hash,
			Path:     path,
			Filename: hash + info.Ext,
			MIME:     info.Mimetype,
		}, nil
	}

	return fileassets.Descriptor{}, &fileassets.NotFoundError{
		Message: fmt.Sprintf("managed file content for file_id %d was not found", fileID),
	}
}

// ResolveThumbnail resolves the managed thumbnail asset path for a file
// identifier.
func (b *Bundle) ResolveThumbnail(
	ctx context.Context,
	fileID int64,
) (fileassets.Descriptor, error) {
	hash, _, _, err := b.lookupManagedFileDescriptor(ctx, fileID)
	if err != nil {
		return fileassets.Descriptor{}, err
	}

	layout, err := b.ManagedLayout(ctx)
	if err != nil {
		return fileassets.Descriptor{}, err
	}

	path, err := layout.ResolveThumbnailPath(hash)
	if err != nil {
		return fileassets.Descriptor{}, fmt.Errorf("resolve managed thumbnail path: %w", err)
	}

	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return fileassets.Descriptor{}, &fileassets.NotFoundError{
				Message: fmt.Sprintf("managed thumbnail for file_id %d was not found", fileID),
			}
		}

		return fileassets.Descriptor{}, fmt.Errorf("stat managed thumbnail path: %w", err)
	}

	return fileassets.Descriptor{
		FileID:   fileID,
		Hash:     hash,
		Path:     path,
		Filename: hash + ".thumbnail",
	}, nil
}

func (b *Bundle) lookupManagedFileDescriptor(
	ctx context.Context,
	fileID int64,
) (string, int64, sql.NullInt64, error) {
	row := b.conn.QueryRowContext(
		ctx,
		`SELECT lower(hex(h.hash)), fi.mime, fft.forced_mime
		FROM main.files_info fi
		JOIN external_master.hashes h USING (hash_id)
		LEFT JOIN main.files_info_forced_filetypes fft USING (hash_id)
		WHERE fi.hash_id = ?`,
		fileID,
	)

	var (
		hash       string
		mime       int64
		forcedMime sql.NullInt64
	)

	if err := row.Scan(&hash, &mime, &forcedMime); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", 0, sql.NullInt64{}, &fileassets.NotFoundError{
				Message: fmt.Sprintf("file_id %d was not found", fileID),
			}
		}

		return "", 0, sql.NullInt64{}, fmt.Errorf("query managed file descriptor: %w", err)
	}

	return hash, mime, forcedMime, nil
}
