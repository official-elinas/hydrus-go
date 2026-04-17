package importing

import (
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/official-elinas/hydrus-go/internal/core/fileimport"
	"github.com/official-elinas/hydrus-go/internal/core/filemetadata"
	"github.com/official-elinas/hydrus-go/internal/db/hydrusdb"
)

func TestImporterImportLocalPath(t *testing.T) {
	t.Run("imports a local png and records image dimensions", func(t *testing.T) {
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

		sourcePath := writePNGSourceFile(t, t.TempDir(), "import.png", 12, 34)

		result, err := importer.ImportLocalPath(context.Background(), fileimport.Request{Path: sourcePath})
		if err != nil {
			t.Fatalf("ImportLocalPath() error = %v", err)
		}

		if result.FileID <= 0 {
			t.Fatalf("result.FileID = %d, want > 0", result.FileID)
		}

		if result.AlreadyImported {
			t.Fatal("result.AlreadyImported = true, want false")
		}

		if result.ManagedFileAlreadyPresent {
			t.Fatal("result.ManagedFileAlreadyPresent = true, want false")
		}

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
		if row["mime"] != "image/png" {
			t.Fatalf("row[mime] = %v, want image/png", row["mime"])
		}

		if row["width"] != int64(12) {
			t.Fatalf("row[width] = %v, want 12", row["width"])
		}

		if row["height"] != int64(34) {
			t.Fatalf("row[height] = %v, want 34", row["height"])
		}
	})

	t.Run("exact retry reports already imported", func(t *testing.T) {
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

		sourcePath := writePNGSourceFile(t, t.TempDir(), "retry.png", 10, 10)

		first, err := importer.ImportLocalPath(context.Background(), fileimport.Request{Path: sourcePath})
		if err != nil {
			t.Fatalf("first ImportLocalPath() error = %v", err)
		}

		second, err := importer.ImportLocalPath(context.Background(), fileimport.Request{Path: sourcePath})
		if err != nil {
			t.Fatalf("second ImportLocalPath() error = %v", err)
		}

		if !second.AlreadyImported {
			t.Fatal("second.AlreadyImported = false, want true")
		}

		if second.FileID != first.FileID {
			t.Fatalf("second.FileID = %d, want %d", second.FileID, first.FileID)
		}
	})

	t.Run("imports a video-like path through extension fallback", func(t *testing.T) {
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

		sourcePath, _, _ := writePreparedSourceFile(
			t,
			t.TempDir(),
			"clip.mp4",
			[]byte("fake mp4 bytes for extension fallback"),
		)

		result, err := importer.ImportLocalPath(context.Background(), fileimport.Request{Path: sourcePath})
		if err != nil {
			t.Fatalf("ImportLocalPath() error = %v", err)
		}

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

		if row["width"] != nil || row["height"] != nil {
			t.Fatalf("row width/height = %v/%v, want nil/nil", row["width"], row["height"])
		}
	})

	t.Run("rejects relative and missing paths", func(t *testing.T) {
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

		_, err = importer.ImportLocalPath(context.Background(), fileimport.Request{Path: "relative/file.png"})
		var requestError *fileimport.RequestError
		if !errorAs(t, err, &requestError) {
			t.Fatalf("ImportLocalPath(relative) error = %T, want *fileimport.RequestError", err)
		}

		_, err = importer.ImportLocalPath(
			context.Background(),
			fileimport.Request{Path: filepath.Join(t.TempDir(), "missing.png")},
		)
		var notFoundError *fileimport.NotFoundError
		if !errorAs(t, err, &notFoundError) {
			t.Fatalf("ImportLocalPath(missing) error = %T, want *fileimport.NotFoundError", err)
		}
	})
}

func writePNGSourceFile(
	t *testing.T,
	dir string,
	name string,
	width int,
	height int,
) string {
	t.Helper()

	path := filepath.Join(dir, name)
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	defer file.Close()

	imageData := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			imageData.Set(x, y, color.RGBA{R: 25, G: 50, B: 75, A: 255})
		}
	}

	if err := png.Encode(file, imageData); err != nil {
		t.Fatalf("png.Encode() error = %v", err)
	}

	return path
}

func errorAs[T error](t *testing.T, err error, target *T) bool {
	t.Helper()

	return errors.As(err, target)
}
