// Package importing composes prepared local file placement with minimal Hydrus
// DB import writes.
package importing

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/official-elinas/hydrus-go/internal/core/mimes"
	"github.com/official-elinas/hydrus-go/internal/db/hydrusdb"
	"github.com/official-elinas/hydrus-go/internal/storage/clientfiles"
)

// PreparedFile describes a caller-prepared local import. The caller is expected
// to supply the source path and already-known file metadata.
type PreparedFile struct {
	SourcePath          string
	HashHex             string
	Size                int64
	Mime                int
	Width               *int64
	Height              *int64
	Duration            *int64
	NumFrames           *int64
	HasAudio            *bool
	NumWords            *int64
	ImportedAtMS        int64
	FileModifiedAtMS    *int64
	LocalFileServiceKey string
}

// Result describes the composed storage-plus-DB outcome for one prepared
// import.
type Result struct {
	FileID                    int64
	ManagedPath               string
	ManagedFileAlreadyPresent bool
	AlreadyImported           bool
}

// Importer composes managed file placement with the minimal Hydrus DB write
// path.
type Importer struct {
	bundle *hydrusdb.Bundle
	layout clientfiles.Layout
}

// NewImporter constructs an internal prepared-file importer.
func NewImporter(bundle *hydrusdb.Bundle, layout clientfiles.Layout) (*Importer, error) {
	if bundle == nil {
		return nil, fmt.Errorf("hydrus bundle is required")
	}

	if strings.TrimSpace(layout.Root) == "" {
		return nil, fmt.Errorf("managed client_files layout root is required")
	}

	if layout.PrefixLength <= 0 {
		return nil, fmt.Errorf("managed client_files prefix length must be greater than zero")
	}

	return &Importer{bundle: bundle, layout: layout}, nil
}

// NewDefaultImporter constructs an importer with the default Hydrus
// `client_files` root derived from the bundle DB directory.
func NewDefaultImporter(bundle *hydrusdb.Bundle, dbDir string) (*Importer, error) {
	layout, err := clientfiles.NewLayout(
		clientfiles.DefaultRoot(dbDir),
		clientfiles.DefaultPrefixLength,
	)
	if err != nil {
		return nil, err
	}

	return NewImporter(bundle, layout)
}

// ImportPreparedFile performs the first internal import checkpoint: place a
// caller-prepared file in managed storage, then record the minimal Hydrus DB
// state needed for metadata round-trips.
func (i *Importer) ImportPreparedFile(
	ctx context.Context,
	prepared PreparedFile,
) (Result, error) {
	if i == nil {
		return Result{}, fmt.Errorf("importer is nil")
	}

	if strings.TrimSpace(prepared.SourcePath) == "" {
		return Result{}, fmt.Errorf("prepared source path is required")
	}

	mimeInfo := mimes.Lookup(prepared.Mime)
	if mimeInfo.Ext == "" {
		return Result{}, fmt.Errorf(
			"prepared MIME %d does not resolve to a managed file extension",
			prepared.Mime,
		)
	}

	placement, err := i.layout.PlaceFileFromPath(
		prepared.SourcePath,
		prepared.HashHex,
		mimeInfo.Ext,
	)
	if err != nil {
		return Result{}, err
	}

	dbResult, err := i.bundle.RecordPreparedLocalImport(
		ctx,
		hydrusdb.PreparedLocalImport{
			HashHex:             prepared.HashHex,
			Size:                prepared.Size,
			Mime:                prepared.Mime,
			Width:               prepared.Width,
			Height:              prepared.Height,
			Duration:            prepared.Duration,
			NumFrames:           prepared.NumFrames,
			HasAudio:            prepared.HasAudio,
			NumWords:            prepared.NumWords,
			ImportedAtMS:        prepared.ImportedAtMS,
			FileModifiedAtMS:    prepared.FileModifiedAtMS,
			LocalFileServiceKey: prepared.LocalFileServiceKey,
		},
	)
	if err != nil {
		if !placement.AlreadyPresent {
			if cleanupErr := i.cleanupFailedPlacement(
				context.WithoutCancel(ctx),
				prepared.HashHex,
				placement.Path,
			); cleanupErr != nil {
				return Result{}, errors.Join(err, cleanupErr)
			}
		}

		return Result{}, err
	}

	return Result{
		FileID:                    dbResult.FileID,
		ManagedPath:               placement.Path,
		ManagedFileAlreadyPresent: placement.AlreadyPresent,
		AlreadyImported:           dbResult.AlreadyImported,
	}, nil
}

func (i *Importer) cleanupFailedPlacement(
	ctx context.Context,
	hashHex string,
	managedPath string,
) error {
	_, exists, err := i.bundle.LookupImportedFileIDByHash(ctx, hashHex)
	if err != nil {
		return fmt.Errorf("check import state before managed cleanup: %w", err)
	}

	if exists {
		return nil
	}

	if err := os.Remove(managedPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove managed file after failed DB import: %w", err)
	}

	return nil
}
