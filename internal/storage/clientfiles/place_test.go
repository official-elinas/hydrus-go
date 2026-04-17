package clientfiles

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLayoutPlaceFileFromPath(t *testing.T) {
	root := t.TempDir()
	layout, err := NewLayout(root, DefaultPrefixLength)
	if err != nil {
		t.Fatalf("NewLayout() error = %v", err)
	}

	hash := repeatHex("ab", 32)
	sourceTime := time.Unix(1_700_000_000, 456_000_000)
	sourcePath := writeTestFile(
		t,
		filepath.Join(t.TempDir(), "source.png"),
		[]byte("image-bytes"),
		sourceTime,
	)

	t.Run("places file and creates parent directories", func(t *testing.T) {
		result, err := layout.PlaceFileFromPath(sourcePath, hash, ".png")
		if err != nil {
			t.Fatalf("PlaceFileFromPath() error = %v", err)
		}

		if result.AlreadyPresent {
			t.Fatal("AlreadyPresent = true, want false")
		}

		expectedPath := filepath.Join(root, "fab", hash+".png")
		if result.Path != expectedPath {
			t.Fatalf("result.Path = %q, want %q", result.Path, expectedPath)
		}

		assertFileContents(t, result.Path, []byte("image-bytes"))

		info, err := os.Stat(result.Path)
		if err != nil {
			t.Fatalf("Stat() error = %v", err)
		}

		if normalizeFileTime(info.ModTime()) != sourceTime.UTC() {
			t.Fatalf("dest modtime = %v, want source modtime", info.ModTime())
		}
	})

	t.Run("replacing the same source is idempotent", func(t *testing.T) {
		result, err := layout.PlaceFileFromPath(sourcePath, hash, ".png")
		if err != nil {
			t.Fatalf("PlaceFileFromPath() error = %v", err)
		}

		if !result.AlreadyPresent {
			t.Fatal("AlreadyPresent = false, want true")
		}

		expectedPath := filepath.Join(root, "fab", hash+".png")
		if result.Path != expectedPath {
			t.Fatalf("result.Path = %q, want %q", result.Path, expectedPath)
		}

		assertFileContents(t, result.Path, []byte("image-bytes"))
	})

	t.Run("same contents with coarse timestamp drift stay idempotent", func(t *testing.T) {
		destinationPath := filepath.Join(root, "fab", hash+".png")
		updatedTime := time.Unix(1_700_000_000, 0)
		if err := os.Chtimes(destinationPath, updatedTime, updatedTime); err != nil {
			t.Fatalf("Chtimes() error = %v", err)
		}

		result, err := layout.PlaceFileFromPath(sourcePath, hash, ".png")
		if err != nil {
			t.Fatalf("PlaceFileFromPath() error = %v", err)
		}

		if !result.AlreadyPresent {
			t.Fatal("AlreadyPresent = false, want true")
		}

		assertFileContents(t, destinationPath, []byte("image-bytes"))
	})

	t.Run("same contents with unrelated timestamp drift are treated as conflict", func(t *testing.T) {
		destinationPath := filepath.Join(root, "fab", hash+".png")
		updatedTime := time.Unix(1_800_000_000, 0)
		if err := os.Chtimes(destinationPath, updatedTime, updatedTime); err != nil {
			t.Fatalf("Chtimes() error = %v", err)
		}

		_, err := layout.PlaceFileFromPath(sourcePath, hash, ".png")
		if err == nil {
			t.Fatal("PlaceFileFromPath() error = nil, want error")
		}

		if !strings.Contains(err.Error(), "managed destination already exists") {
			t.Fatalf("error = %v, want managed destination conflict", err)
		}

		assertFileContents(t, destinationPath, []byte("image-bytes"))
	})

	t.Run("preserves source file", func(t *testing.T) {
		assertFileContents(t, sourcePath, []byte("image-bytes"))
	})

	t.Run("rejects conflicting existing destination", func(t *testing.T) {
		otherHash := repeatHex("cd", 32)
		conflictingPath, err := layout.ResolveFilePath(otherHash, ".png")
		if err != nil {
			t.Fatalf("ResolveFilePath() error = %v", err)
		}

		if err := os.MkdirAll(filepath.Dir(conflictingPath), 0o755); err != nil {
			t.Fatalf("MkdirAll() error = %v", err)
		}

		writeTestFile(
			t,
			conflictingPath,
			[]byte("other-bytes"),
			time.Unix(1_600_000_000, 0),
		)

		_, err = layout.PlaceFileFromPath(sourcePath, otherHash, ".png")
		if err == nil {
			t.Fatal("PlaceFileFromPath() error = nil, want error")
		}

		if !strings.Contains(err.Error(), "managed destination already exists") {
			t.Fatalf("error = %v, want managed destination conflict", err)
		}

		assertFileContents(t, conflictingPath, []byte("other-bytes"))
	})

	t.Run("rejects missing source path", func(t *testing.T) {
		_, err := layout.PlaceFileFromPath(
			filepath.Join(t.TempDir(), "missing.png"),
			hash,
			".png",
		)
		if err == nil {
			t.Fatal("PlaceFileFromPath() error = nil, want error")
		}

		if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("error = %v, want not-exist error", err)
		}
	})
}

func TestLayoutPlaceThumbnailFromPath(t *testing.T) {
	root := t.TempDir()
	layout, err := NewLayout(root, 3)
	if err != nil {
		t.Fatalf("NewLayout() error = %v", err)
	}

	hash := repeatHex("ab", 32)
	sourcePath := writeTestFile(
		t,
		filepath.Join(t.TempDir(), "source.thumbnail"),
		[]byte("thumbnail-bytes"),
		time.Unix(1_710_000_000, 0),
	)

	result, err := layout.PlaceThumbnailFromPath(sourcePath, hash)
	if err != nil {
		t.Fatalf("PlaceThumbnailFromPath() error = %v", err)
	}

	expectedPath := filepath.Join(root, "tab", "a", hash+".thumbnail")
	if result.Path != expectedPath {
		t.Fatalf("result.Path = %q, want %q", result.Path, expectedPath)
	}

	assertFileContents(t, result.Path, []byte("thumbnail-bytes"))
}

func TestLinkTempFileIntoPlace(t *testing.T) {
	t.Run("reports existing destination without overwriting", func(t *testing.T) {
		dir := t.TempDir()
		tempPath := writeTestFile(
			t,
			filepath.Join(dir, "temp-file"),
			[]byte("temp-bytes"),
			time.Unix(1_720_000_000, 0),
		)
		destinationPath := writeTestFile(
			t,
			filepath.Join(dir, "destination-file"),
			[]byte("dest-bytes"),
			time.Unix(1_720_000_100, 0),
		)

		conflictAppeared, err := linkTempFileIntoPlace(tempPath, destinationPath)
		if err != nil {
			t.Fatalf("linkTempFileIntoPlace() error = %v", err)
		}

		if !conflictAppeared {
			t.Fatal("conflictAppeared = false, want true")
		}

		assertFileContents(t, destinationPath, []byte("dest-bytes"))
		assertFileContents(t, tempPath, []byte("temp-bytes"))
	})
}

func writeTestFile(
	t *testing.T,
	path string,
	contents []byte,
	modTime time.Time,
) string {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	if err := os.WriteFile(path, contents, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}

	return path
}

func assertFileContents(t *testing.T, path string, expected []byte) {
	t.Helper()

	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	if string(contents) != string(expected) {
		t.Fatalf("contents = %q, want %q", contents, expected)
	}
}
