package importing

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/official-elinas/hydrus-go/internal/core/fileimport"
	"github.com/official-elinas/hydrus-go/internal/core/filemetadata"
	"github.com/official-elinas/hydrus-go/internal/db/hydrusdb"
)

func TestImporterImportUpload(t *testing.T) {
	t.Run("imports uploaded png and records image metadata", func(t *testing.T) {
		dir, _ := createImportTestBundle(t)

		bundle, err := hydrusdb.OpenWritable(context.Background(), dir)
		if err != nil {
			t.Fatalf("OpenWritable() error = %v", err)
		}
		defer func() {
			if bundle != nil {
				if err := bundle.Close(); err != nil {
					t.Fatalf("Close() error = %v", err)
				}
			}
		}()

		importer, err := NewDefaultImporter(bundle, dir)
		if err != nil {
			t.Fatalf("NewDefaultImporter() error = %v", err)
		}

		stagedPath := writePNGSourceFile(t, t.TempDir(), "staged-upload.png", 16, 24)
		expectedMetadata := detectStillImageImportMetadata(stagedPath, 2)
		fileModifiedAtMS := int64(1_700_000_123_000)

		result, err := importer.ImportUpload(context.Background(), fileimport.UploadRequest{
			StagedPath:       stagedPath,
			Filename:         "original-name.png",
			FileModifiedAtMS: &fileModifiedAtMS,
		})
		if err != nil {
			t.Fatalf("ImportUpload() error = %v", err)
		}

		layout := mustImportTestLayout(t, dir)
		thumbnailPath := mustResolveManagedThumbnailPath(t, layout, result.Hash)
		assertManagedThumbnailImage(t, thumbnailPath, 16, 24)

		if err := bundle.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
		bundle = nil

		readBundle, err := hydrusdb.Open(context.Background(), dir)
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		defer func() {
			if err := readBundle.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
		}()

		rows, err := readBundle.GetMetadata(context.Background(), filemetadata.Request{
			FileIDs:               []int64{result.FileID},
			IncludeMilliseconds:   true,
			IncludeServicesObject: true,
		})
		if err != nil {
			t.Fatalf("GetMetadata() error = %v", err)
		}

		row := rows[0]
		if row["mime"] != "image/png" {
			t.Fatalf("row[mime] = %v, want image/png", row["mime"])
		}

		if row["width"] != int64(16) {
			t.Fatalf("row[width] = %v, want 16", row["width"])
		}

		if row["height"] != int64(24) {
			t.Fatalf("row[height] = %v, want 24", row["height"])
		}

		if row["time_modified"] != 1700000123.0 {
			t.Fatalf("row[time_modified] = %v, want 1700000123", row["time_modified"])
		}

		if got := row["pixel_hash"]; got != expectedMetadata.PixelHashHex {
			t.Fatalf("row[pixel_hash] = %v, want %q", got, expectedMetadata.PixelHashHex)
		}

		if got := row["has_transparency"]; got != false {
			t.Fatalf("row[has_transparency] = %v, want false", got)
		}
	})

	t.Run("imports uploaded file through original filename extension fallback", func(t *testing.T) {
		dir, _ := createImportTestBundle(t)

		bundle, err := hydrusdb.OpenWritable(context.Background(), dir)
		if err != nil {
			t.Fatalf("OpenWritable() error = %v", err)
		}
		defer func() {
			if bundle != nil {
				if err := bundle.Close(); err != nil {
					t.Fatalf("Close() error = %v", err)
				}
			}
		}()

		importer, err := NewDefaultImporter(bundle, dir)
		if err != nil {
			t.Fatalf("NewDefaultImporter() error = %v", err)
		}

		stagedPath, _, _ := writePreparedSourceFile(
			t,
			t.TempDir(),
			"staged-upload",
			[]byte("fake mp4 bytes for upload extension fallback"),
		)

		result, err := importer.ImportUpload(context.Background(), fileimport.UploadRequest{
			StagedPath: stagedPath,
			Filename:   "clip.mp4",
		})
		if err != nil {
			t.Fatalf("ImportUpload() error = %v", err)
		}

		if err := bundle.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
		bundle = nil

		readBundle, err := hydrusdb.Open(context.Background(), dir)
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		defer func() {
			if err := readBundle.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
		}()

		rows, err := readBundle.GetMetadata(context.Background(), filemetadata.Request{
			FileIDs:                    []int64{result.FileID},
			OnlyReturnBasicInformation: true,
		})
		if err != nil {
			t.Fatalf("GetMetadata() error = %v", err)
		}

		row := rows[0]
		if row["mime"] != "video/mp4" {
			t.Fatalf("row[mime] = %v, want video/mp4", row["mime"])
		}
	})

	t.Run("imports uploaded avif without filename extension via ffprobe fallback", func(t *testing.T) {
		if _, err := exec.LookPath("ffmpeg"); err != nil {
			t.Skip("ffmpeg is required for AVIF upload detection fallback")
		}

		dir, _ := createImportTestBundle(t)

		bundle, err := hydrusdb.OpenWritable(context.Background(), dir)
		if err != nil {
			t.Fatalf("OpenWritable() error = %v", err)
		}
		defer func() {
			if bundle != nil {
				if err := bundle.Close(); err != nil {
					t.Fatalf("Close() error = %v", err)
				}
			}
		}()

		importer, err := NewDefaultImporter(bundle, dir)
		if err != nil {
			t.Fatalf("NewDefaultImporter() error = %v", err)
		}

		stagedPath := writeFFmpegStillImageSourceFile(t, t.TempDir(), "staged-upload.avif", 18, 12)
		result, err := importer.ImportUpload(context.Background(), fileimport.UploadRequest{
			StagedPath: stagedPath,
			Filename:   "hydrus-api-upload",
		})
		if err != nil {
			t.Fatalf("ImportUpload() error = %v", err)
		}

		if err := bundle.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
		bundle = nil

		readBundle, err := hydrusdb.Open(context.Background(), dir)
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		defer func() {
			if err := readBundle.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
		}()

		rows, err := readBundle.GetMetadata(context.Background(), filemetadata.Request{FileIDs: []int64{result.FileID}, OnlyReturnBasicInformation: true})
		if err != nil {
			t.Fatalf("GetMetadata() error = %v", err)
		}

		if got := rows[0]["mime"]; got != "image/avif" {
			t.Fatalf("row[mime] = %v, want image/avif", got)
		}
	})

	t.Run("exact retry without file modified time reports already imported", func(t *testing.T) {
		dir, _ := createImportTestBundle(t)

		bundle, err := hydrusdb.OpenWritable(context.Background(), dir)
		if err != nil {
			t.Fatalf("OpenWritable() error = %v", err)
		}
		defer func() {
			if err := bundle.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
		}()

		importer, err := NewDefaultImporter(bundle, dir)
		if err != nil {
			t.Fatalf("NewDefaultImporter() error = %v", err)
		}

		firstStagedPath := writePNGSourceFile(t, t.TempDir(), "first-upload.png", 10, 10)
		secondBytes, err := os.ReadFile(firstStagedPath)
		if err != nil {
			t.Fatalf("ReadFile(firstStagedPath) error = %v", err)
		}

		secondStagedPath := filepath.Join(t.TempDir(), "second-upload.png")
		if err := os.WriteFile(secondStagedPath, secondBytes, 0o644); err != nil {
			t.Fatalf("WriteFile(secondStagedPath) error = %v", err)
		}

		first, err := importer.ImportUpload(context.Background(), fileimport.UploadRequest{
			StagedPath: firstStagedPath,
			Filename:   "retry.png",
		})
		if err != nil {
			t.Fatalf("first ImportUpload() error = %v", err)
		}

		second, err := importer.ImportUpload(context.Background(), fileimport.UploadRequest{
			StagedPath: secondStagedPath,
			Filename:   "retry.png",
		})
		if err != nil {
			t.Fatalf("second ImportUpload() error = %v", err)
		}

		if !second.AlreadyImported {
			t.Fatal("second.AlreadyImported = false, want true")
		}

		if second.FileID != first.FileID {
			t.Fatalf("second.FileID = %d, want %d", second.FileID, first.FileID)
		}
	})
}
