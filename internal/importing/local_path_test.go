package importing

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/official-elinas/hydrus-go/internal/core/fileimport"
	"github.com/official-elinas/hydrus-go/internal/core/filemetadata"
	"github.com/official-elinas/hydrus-go/internal/db/hydrusdb"
	"github.com/official-elinas/hydrus-go/internal/storage/clientfiles"
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
		expectedMetadata := detectStillImageImportMetadata(sourcePath, 2)

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

		layout := mustImportTestLayout(t, dir)
		thumbnailPath := mustResolveManagedThumbnailPath(t, layout, result.Hash)
		assertManagedThumbnailImage(t, thumbnailPath, 12, 34)

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

		fullRows, err := readBundle.GetMetadata(context.Background(), filemetadata.Request{
			FileIDs: []int64{result.FileID},
		})
		if err != nil {
			t.Fatalf("GetMetadata(full) error = %v", err)
		}

		full := fullRows[0]
		if got := full["pixel_hash"]; got != expectedMetadata.PixelHashHex {
			t.Fatalf("full[pixel_hash] = %v, want %q", got, expectedMetadata.PixelHashHex)
		}

		if got := full["has_transparency"]; got != false {
			t.Fatalf("full[has_transparency] = %v, want false", got)
		}
	})

	t.Run("imports a transparent png and records useful transparency", func(t *testing.T) {
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

		sourcePath := writeTransparentPNGSourceFile(t, t.TempDir(), "transparent.png", 10, 8)
		expectedMetadata := detectStillImageImportMetadata(sourcePath, 2)

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

		fullRows, err := readBundle.GetMetadata(context.Background(), filemetadata.Request{
			FileIDs: []int64{result.FileID},
		})
		if err != nil {
			t.Fatalf("GetMetadata(full) error = %v", err)
		}

		full := fullRows[0]
		if got := full["has_transparency"]; got != true {
			t.Fatalf("full[has_transparency] = %v, want true", got)
		}

		if got := full["pixel_hash"]; got != expectedMetadata.PixelHashHex {
			t.Fatalf("full[pixel_hash] = %v, want %q", got, expectedMetadata.PixelHashHex)
		}
	})

	t.Run("imports supported jpeg and gif files with thumbnails", func(t *testing.T) {
		tests := []struct {
			name      string
			fileName  string
			wantMIME  string
			width     int
			height    int
			writeFile func(*testing.T, string, string, int, int) string
		}{
			{
				name:      "jpeg",
				fileName:  "import.jpg",
				wantMIME:  "image/jpeg",
				width:     18,
				height:    12,
				writeFile: writeJPEGSourceFile,
			},
			{
				name:      "gif",
				fileName:  "import.gif",
				wantMIME:  "image/gif",
				width:     14,
				height:    11,
				writeFile: writeGIFSourceFile,
			},
			{
				name:      "bmp",
				fileName:  "import.bmp",
				wantMIME:  "image/bmp",
				width:     16,
				height:    9,
				writeFile: writeFFmpegStillImageSourceFile,
			},
			{
				name:      "tiff",
				fileName:  "import.tiff",
				wantMIME:  "image/tiff",
				width:     17,
				height:    10,
				writeFile: writeFFmpegStillImageSourceFile,
			},
			{
				name:      "webp",
				fileName:  "import.webp",
				wantMIME:  "image/webp",
				width:     19,
				height:    13,
				writeFile: writeFFmpegStillImageSourceFile,
			},
			{
				name:      "avif",
				fileName:  "import.avif",
				wantMIME:  "image/avif",
				width:     20,
				height:    14,
				writeFile: writeFFmpegStillImageSourceFile,
			},
		}

		for _, tt := range tests {
			caseData := tt
			t.Run(caseData.name, func(t *testing.T) {
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

				sourcePath := caseData.writeFile(t, t.TempDir(), caseData.fileName, caseData.width, caseData.height)

				result, err := importer.ImportLocalPath(context.Background(), fileimport.Request{Path: sourcePath})
				if err != nil {
					t.Fatalf("ImportLocalPath() error = %v", err)
				}

				layout := mustImportTestLayout(t, dir)
				thumbnailPath := mustResolveManagedThumbnailPath(t, layout, result.Hash)
				assertManagedThumbnailImage(t, thumbnailPath, caseData.width, caseData.height)

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
				if row["mime"] != caseData.wantMIME {
					t.Fatalf("row[mime] = %v, want %s", row["mime"], caseData.wantMIME)
				}
			})
		}
	})

	t.Run("uses DB-configured split storage roots", func(t *testing.T) {
		dir, _ := createImportTestBundle(t)

		mainDB := openSQLiteForTest(t, filepath.Join(dir, "client.db"))
		defer mainDB.Close()

		portableFileRoot := "custom_files"
		thumbnailRoot := filepath.Join(filepath.Dir(dir), "custom-thumbnails")
		if err := os.MkdirAll(filepath.Join(dir, portableFileRoot), 0o755); err != nil {
			t.Fatalf("MkdirAll(portableFileRoot) error = %v", err)
		}
		if err := os.MkdirAll(thumbnailRoot, 0o755); err != nil {
			t.Fatalf("MkdirAll(thumbnailRoot) error = %v", err)
		}

		mustExec(
			t,
			mainDB,
			`UPDATE current_client_files_locations SET location = CASE location_id WHEN 1 THEN ? WHEN 2 THEN ? ELSE location END`,
			portableFileRoot,
			thumbnailRoot,
		)

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

		sourcePath := writePNGSourceFile(t, t.TempDir(), "custom-roots.png", 20, 10)
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

		layout, err := readBundle.ManagedLayout(context.Background())
		if err != nil {
			t.Fatalf("ManagedLayout() error = %v", err)
		}

		managedFilePath, err := layout.ResolveFilePath(result.Hash, ".png")
		if err != nil {
			t.Fatalf("ResolveFilePath() error = %v", err)
		}

		wantFilePrefix := filepath.Join(dir, portableFileRoot, "f")
		if !strings.HasPrefix(managedFilePath, wantFilePrefix) {
			t.Fatalf("managedFilePath = %q, want prefix %q", managedFilePath, wantFilePrefix)
		}

		if _, err := os.Stat(managedFilePath); err != nil {
			t.Fatalf("Stat(managedFilePath) error = %v", err)
		}

		thumbnailPath, err := layout.ResolveThumbnailPath(result.Hash)
		if err != nil {
			t.Fatalf("ResolveThumbnailPath() error = %v", err)
		}

		wantThumbnailPrefix := filepath.Join(thumbnailRoot, "t")
		if !strings.HasPrefix(thumbnailPath, wantThumbnailPrefix) {
			t.Fatalf("thumbnailPath = %q, want prefix %q", thumbnailPath, wantThumbnailPrefix)
		}

		assertManagedThumbnailImage(t, thumbnailPath, 20, 10)

		defaultThumbnailPath := filepath.Join(
			clientfiles.DefaultThumbnailRoot(dir),
			"t"+result.Hash[:2],
			result.Hash+".thumbnail",
		)
		if defaultThumbnailPath != thumbnailPath {
			if _, err := os.Stat(defaultThumbnailPath); !os.IsNotExist(err) {
				t.Fatalf("default thumbnail path stat err = %v, want not exists", err)
			}
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

		layout := mustImportTestLayout(t, dir)
		thumbnailPath := mustResolveManagedThumbnailPath(t, layout, first.Hash)
		if err := os.Remove(thumbnailPath); err != nil {
			t.Fatalf("Remove(thumbnailPath) error = %v", err)
		}

		third, err := importer.ImportLocalPath(context.Background(), fileimport.Request{Path: sourcePath})
		if err != nil {
			t.Fatalf("third ImportLocalPath() error = %v", err)
		}

		if !third.AlreadyImported {
			t.Fatal("third.AlreadyImported = false, want true")
		}

		assertManagedThumbnailImage(t, thumbnailPath, 10, 10)
	})

	t.Run("exact retry repairs a stale managed thumbnail", func(t *testing.T) {
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

		sourcePath := writePNGSourceFile(t, t.TempDir(), "repair.png", 32, 20)

		first, err := importer.ImportLocalPath(context.Background(), fileimport.Request{Path: sourcePath})
		if err != nil {
			t.Fatalf("first ImportLocalPath() error = %v", err)
		}

		layout := mustImportTestLayout(t, dir)
		thumbnailPath := mustResolveManagedThumbnailPath(t, layout, first.Hash)
		if err := os.WriteFile(thumbnailPath, []byte("not-a-valid-thumbnail"), 0o644); err != nil {
			t.Fatalf("WriteFile(thumbnailPath) error = %v", err)
		}

		second, err := importer.ImportLocalPath(context.Background(), fileimport.Request{Path: sourcePath})
		if err != nil {
			t.Fatalf("second ImportLocalPath() error = %v", err)
		}

		if !second.AlreadyImported {
			t.Fatal("second.AlreadyImported = false, want true")
		}

		assertManagedThumbnailImage(t, thumbnailPath, 32, 20)
	})

	t.Run("rejects fake video bytes that only match by extension", func(t *testing.T) {
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

		_, err = importer.ImportLocalPath(context.Background(), fileimport.Request{Path: sourcePath})
		var requestError *fileimport.RequestError
		if !errorAs(t, err, &requestError) {
			t.Fatalf("ImportLocalPath() error = %T, want *fileimport.RequestError", err)
		}
		if !strings.Contains(err.Error(), "unsupported or unverified media payload") {
			t.Fatalf("ImportLocalPath() error = %v, want unsupported or unverified media payload", err)
		}
	})

	t.Run("imports actual common video containers with dimensions and thumbnails", func(t *testing.T) {
		if _, err := exec.LookPath("ffmpeg"); err != nil {
			t.Skip("ffmpeg is required for common video container import tests")
		}

		tests := []struct {
			name     string
			fileName string
			wantMIME string
		}{
			{name: "mp4", fileName: "clip.mp4", wantMIME: "video/mp4"},
			{name: "flv", fileName: "clip.flv", wantMIME: "video/x-flv"},
			{name: "wmv", fileName: "clip.wmv", wantMIME: "video/x-ms-wmv"},
		}

		for _, tt := range tests {
			tt := tt
			t.Run(tt.name, func(t *testing.T) {
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

				sourcePath := writeFFmpegVideoSourceFile(t, t.TempDir(), tt.fileName, 8, 6)
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

				rows, err := readBundle.GetMetadata(context.Background(), filemetadata.Request{FileIDs: []int64{result.FileID}, OnlyReturnBasicInformation: true})
				if err != nil {
					t.Fatalf("GetMetadata() error = %v", err)
				}
				row := rows[0]
				if row["mime"] != tt.wantMIME {
					t.Fatalf("row[mime] = %v, want %s", row["mime"], tt.wantMIME)
				}
				if row["width"] != int64(8) || row["height"] != int64(6) {
					t.Fatalf("row width/height = %v/%v, want 8/6", row["width"], row["height"])
				}
				if got := row["has_audio"]; got != false {
					t.Fatalf("row[has_audio] = %v, want false", got)
				}
				duration, ok := row["duration"].(int64)
				if !ok || duration <= 0 {
					t.Fatalf("row[duration] = %v, want positive int64", row["duration"])
				}
				frames, ok := row["num_frames"].(int64)
				if !ok || frames <= 0 {
					t.Fatalf("row[num_frames] = %v, want positive int64", row["num_frames"])
				}

				layout := mustImportTestLayout(t, dir)
				thumbnailPath := mustResolveManagedThumbnailPath(t, layout, result.Hash)
				assertManagedThumbnailImage(t, thumbnailPath, 8, 6)
			})
		}
	})

	t.Run("downscales large images into bounded thumbnails", func(t *testing.T) {
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

		sourcePath := writePNGSourceFile(t, t.TempDir(), "large.png", 512, 384)

		result, err := importer.ImportLocalPath(context.Background(), fileimport.Request{Path: sourcePath})
		if err != nil {
			t.Fatalf("ImportLocalPath() error = %v", err)
		}

		layout := mustImportTestLayout(t, dir)
		thumbnailPath := mustResolveManagedThumbnailPath(t, layout, result.Hash)
		assertManagedThumbnailImage(t, thumbnailPath, 256, 192)
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

func writeTransparentPNGSourceFile(
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

	imageData := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			alpha := uint8(255)
			if x == 0 && y == 0 {
				alpha = 64
			}
			imageData.SetNRGBA(x, y, color.NRGBA{R: 25, G: 50, B: 75, A: alpha})
		}
	}

	if err := png.Encode(file, imageData); err != nil {
		t.Fatalf("png.Encode() error = %v", err)
	}

	return path
}

func writeJPEGSourceFile(
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

	if err := jpeg.Encode(file, writeSolidImage(width, height), &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("jpeg.Encode() error = %v", err)
	}

	return path
}

func writeGIFSourceFile(
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

	if err := gif.Encode(file, writeSolidImage(width, height), nil); err != nil {
		t.Fatalf("gif.Encode() error = %v", err)
	}

	return path
}

func writeFFmpegStillImageSourceFile(
	t *testing.T,
	dir string,
	name string,
	width int,
	height int,
) string {
	t.Helper()

	inputPath := writePNGSourceFile(t, dir, name+".png", width, height)
	outputPath := filepath.Join(dir, name)
	cmd := exec.Command(
		"ffmpeg",
		"-nostdin",
		"-v", "error",
		"-y",
		"-i", inputPath,
		outputPath,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg convert %q error = %v\n%s", name, err, string(output))
	}

	return outputPath
}

func writeFFmpegVideoSourceFile(
	t *testing.T,
	dir string,
	name string,
	width int,
	height int,
) string {
	t.Helper()

	outputPath := filepath.Join(dir, name)
	args := []string{
		"ffmpeg",
		"-nostdin",
		"-v", "error",
		"-y",
		"-f", "lavfi",
		"-i", fmt.Sprintf("color=c=#336699:s=%dx%d:d=1", width, height),
	}
	switch filepath.Ext(name) {
	case ".webm":
		args = append(args, "-c:v", "libvpx-vp9", "-pix_fmt", "yuv420p")
	case ".flv":
		args = append(args, "-c:v", "flv", "-pix_fmt", "yuv420p")
	case ".wmv":
		args = append(args, "-c:v", "wmv2", "-pix_fmt", "yuv420p")
	default:
		args = append(args, "-c:v", "libx264", "-pix_fmt", "yuv420p")
	}
	args = append(args, outputPath)

	cmd := exec.Command(args[0], args[1:]...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg video convert %q error = %v\n%s", name, err, string(output))
	}

	return outputPath
}

func writeSolidImage(width int, height int) *image.RGBA {
	imageData := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			imageData.Set(x, y, color.RGBA{R: 25, G: 50, B: 75, A: 255})
		}
	}

	return imageData
}

func mustImportTestLayout(t *testing.T, dir string) clientfiles.Layout {
	t.Helper()

	layout, err := clientfiles.NewSplitLayout(
		clientfiles.DefaultFileRoot(dir),
		clientfiles.DefaultThumbnailRoot(dir),
		clientfiles.DefaultPrefixLength,
	)
	if err != nil {
		t.Fatalf("NewSplitLayout() error = %v", err)
	}

	return layout
}

func mustResolveManagedThumbnailPath(t *testing.T, layout clientfiles.Layout, hash string) string {
	t.Helper()

	path, err := layout.ResolveThumbnailPath(hash)
	if err != nil {
		t.Fatalf("ResolveThumbnailPath() error = %v", err)
	}

	return path
}

func assertManagedThumbnailImage(t *testing.T, path string, wantWidth int, wantHeight int) {
	t.Helper()

	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("Open(thumbnail) error = %v", err)
	}
	defer file.Close()

	config, format, err := image.DecodeConfig(file)
	if err != nil {
		t.Fatalf("DecodeConfig(thumbnail) error = %v", err)
	}

	if format != "png" {
		t.Fatalf("thumbnail format = %q, want png", format)
	}

	if config.Width != wantWidth {
		t.Fatalf("thumbnail width = %d, want %d", config.Width, wantWidth)
	}

	if config.Height != wantHeight {
		t.Fatalf("thumbnail height = %d, want %d", config.Height, wantHeight)
	}

	if config.Width > managedThumbnailMaxDimension || config.Height > managedThumbnailMaxDimension {
		t.Fatalf(
			"thumbnail dimensions = %dx%d, want max %d",
			config.Width,
			config.Height,
			managedThumbnailMaxDimension,
		)
	}
}

func errorAs[T error](t *testing.T, err error, target *T) bool {
	t.Helper()

	return errors.As(err, target)
}
