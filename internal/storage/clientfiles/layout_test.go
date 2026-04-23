package clientfiles

import (
	"path/filepath"
	"testing"
)

func TestNormalizeSHA256Hex(t *testing.T) {
	t.Run("normalizes uppercase hex", func(t *testing.T) {
		hash, err := NormalizeSHA256Hex(repeatHex("AB", 32))
		if err != nil {
			t.Fatalf("NormalizeSHA256Hex() error = %v", err)
		}

		if hash != repeatHex("ab", 32) {
			t.Fatalf("normalized hash = %q, want lowercase canonical hash", hash)
		}
	})

	t.Run("trims surrounding whitespace", func(t *testing.T) {
		hash, err := NormalizeSHA256Hex("  " + repeatHex("ab", 32) + "\n")
		if err != nil {
			t.Fatalf("NormalizeSHA256Hex() error = %v", err)
		}

		if hash != repeatHex("ab", 32) {
			t.Fatalf("normalized hash = %q, want trimmed canonical hash", hash)
		}
	})

	t.Run("rejects invalid length", func(t *testing.T) {
		if _, err := NormalizeSHA256Hex("abcd"); err == nil {
			t.Fatal("NormalizeSHA256Hex() error = nil, want error")
		}
	})

	t.Run("rejects invalid characters", func(t *testing.T) {
		if _, err := NormalizeSHA256Hex("zz" + repeatHex("ab", 31)); err == nil {
			t.Fatal("NormalizeSHA256Hex() error = nil, want error")
		}
	})
}

func TestPrefix(t *testing.T) {
	hash := repeatHex("ab", 32)

	t.Run("builds file prefix", func(t *testing.T) {
		prefix, err := Prefix(KindFile, hash, 2)
		if err != nil {
			t.Fatalf("Prefix() error = %v", err)
		}

		if prefix != "fab" {
			t.Fatalf("prefix = %q, want fab", prefix)
		}
	})

	t.Run("builds thumbnail prefix", func(t *testing.T) {
		prefix, err := Prefix(KindThumbnail, hash, 3)
		if err != nil {
			t.Fatalf("Prefix() error = %v", err)
		}

		if prefix != "taba" {
			t.Fatalf("prefix = %q, want taba", prefix)
		}
	})

	t.Run("rejects unknown kind", func(t *testing.T) {
		if _, err := Prefix(Kind("x"), hash, 2); err == nil {
			t.Fatal("Prefix() error = nil, want error")
		}
	})

	t.Run("rejects oversized granularity", func(t *testing.T) {
		if _, err := Prefix(KindFile, hash, 65); err == nil {
			t.Fatal("Prefix() error = nil, want error")
		}
	})
}

func TestLayoutResolvePaths(t *testing.T) {
	hash := repeatHex("ab", 32)
	layout, err := NewLayout("/library/client_files", DefaultPrefixLength)
	if err != nil {
		t.Fatalf("NewLayout() error = %v", err)
	}

	t.Run("resolves file path at default granularity", func(t *testing.T) {
		path, err := layout.ResolveFilePath(hash, ".PNG")
		if err != nil {
			t.Fatalf("ResolveFilePath() error = %v", err)
		}

		expected := filepath.Join("/library/client_files", "fab", hash+".png")
		if path != expected {
			t.Fatalf("path = %q, want %q", path, expected)
		}
	})

	t.Run("resolves split thumbnail root independently", func(t *testing.T) {
		splitLayout, err := NewSplitLayout(
			"/library/client_files",
			"/library/thumbnails",
			DefaultPrefixLength,
		)
		if err != nil {
			t.Fatalf("NewSplitLayout() error = %v", err)
		}

		path, err := splitLayout.ResolveThumbnailPath(hash)
		if err != nil {
			t.Fatalf("ResolveThumbnailPath() error = %v", err)
		}

		expected := filepath.Join("/library/thumbnails", "tab", hash+".thumbnail")
		if path != expected {
			t.Fatalf("path = %q, want %q", path, expected)
		}
	})

	t.Run("resolves DB-driven prefix roots", func(t *testing.T) {
		prefixLayout, err := NewPrefixLayout(DefaultPrefixLength, map[string]string{
			"fab": "/remote/client_files",
			"tab": "/local/thumbnails",
		})
		if err != nil {
			t.Fatalf("NewPrefixLayout() error = %v", err)
		}

		filePath, err := prefixLayout.ResolveFilePath(hash, ".png")
		if err != nil {
			t.Fatalf("ResolveFilePath() error = %v", err)
		}

		wantFilePath := filepath.Join("/remote/client_files", "fab", hash+".png")
		if filePath != wantFilePath {
			t.Fatalf("filePath = %q, want %q", filePath, wantFilePath)
		}

		thumbnailPath, err := prefixLayout.ResolveThumbnailPath(hash)
		if err != nil {
			t.Fatalf("ResolveThumbnailPath() error = %v", err)
		}

		wantThumbnailPath := filepath.Join("/local/thumbnails", "tab", hash+".thumbnail")
		if thumbnailPath != wantThumbnailPath {
			t.Fatalf("thumbnailPath = %q, want %q", thumbnailPath, wantThumbnailPath)
		}
	})

	t.Run("resolves thumbnail path at default granularity", func(t *testing.T) {
		path, err := layout.ResolveThumbnailPath(hash)
		if err != nil {
			t.Fatalf("ResolveThumbnailPath() error = %v", err)
		}

		expected := filepath.Join("/library/client_files", "tab", hash+".thumbnail")
		if path != expected {
			t.Fatalf("path = %q, want %q", path, expected)
		}
	})

	t.Run("resolves odd granularity subdirectories", func(t *testing.T) {
		threeCharLayout, err := NewLayout("/library/client_files", 3)
		if err != nil {
			t.Fatalf("NewLayout() error = %v", err)
		}

		path, err := threeCharLayout.ResolveFilePath(hash, "jpg")
		if err != nil {
			t.Fatalf("ResolveFilePath() error = %v", err)
		}

		expected := filepath.Join("/library/client_files", "fab", "a", hash+".jpg")
		if path != expected {
			t.Fatalf("path = %q, want %q", path, expected)
		}
	})

	t.Run("rejects invalid hash", func(t *testing.T) {
		if _, err := layout.ResolveThumbnailPath("deadbeef"); err == nil {
			t.Fatal("ResolveThumbnailPath() error = nil, want error")
		}
	})

	t.Run("rejects invalid extension input", func(t *testing.T) {
		invalidExtensions := []string{"", ".", "png/../x", "png\\..\\x"}
		for _, ext := range invalidExtensions {
			if ext == "" {
				continue
			}
			if _, err := layout.ResolveFilePath(hash, ext); err == nil {
				t.Fatalf("ResolveFilePath(%q) error = nil, want error", ext)
			}
		}
	})

	t.Run("resolves extensionless managed file paths", func(t *testing.T) {
		path, err := layout.ResolveFilePath(hash, "")
		if err != nil {
			t.Fatalf("ResolveFilePath() error = %v", err)
		}

		expected := filepath.Join("/library/client_files", "fab", hash)
		if path != expected {
			t.Fatalf("path = %q, want %q", path, expected)
		}
	})

	t.Run("resolves odd granularity thumbnail paths", func(t *testing.T) {
		threeCharLayout, err := NewLayout("/library/client_files", 3)
		if err != nil {
			t.Fatalf("NewLayout() error = %v", err)
		}

		path, err := threeCharLayout.ResolveThumbnailPath(hash)
		if err != nil {
			t.Fatalf("ResolveThumbnailPath() error = %v", err)
		}

		expected := filepath.Join("/library/client_files", "tab", "a", hash+".thumbnail")
		if path != expected {
			t.Fatalf("path = %q, want %q", path, expected)
		}
	})
}

func TestDefaultRoot(t *testing.T) {
	root := DefaultRoot(" /hydrus/db/../db ")
	expected := filepath.Join(filepath.Clean("/hydrus/db/../db"), "client_files")
	if root != expected {
		t.Fatalf("root = %q, want %q", root, expected)
	}
}

func TestDefaultThumbnailRoot(t *testing.T) {
	root := DefaultThumbnailRoot(" /hydrus/db/../db ")
	expected := filepath.Join(filepath.Dir(filepath.Clean("/hydrus/db/../db")), "thumbnails")
	if root != expected {
		t.Fatalf("root = %q, want %q", root, expected)
	}
}

func TestNewLayout(t *testing.T) {
	t.Run("cleans root path", func(t *testing.T) {
		layout, err := NewLayout("/hydrus/db/../db/client_files/", 2)
		if err != nil {
			t.Fatalf("NewLayout() error = %v", err)
		}

		expected := filepath.Clean("/hydrus/db/../db/client_files/")
		if layout.Root != expected {
			t.Fatalf("layout.Root = %q, want %q", layout.Root, expected)
		}
	})

	t.Run("rejects empty root", func(t *testing.T) {
		if _, err := NewLayout("", 2); err == nil {
			t.Fatal("NewLayout() error = nil, want error")
		}
	})

	t.Run("rejects invalid prefix length", func(t *testing.T) {
		if _, err := NewLayout("/hydrus/db/client_files", 0); err == nil {
			t.Fatal("NewLayout() error = nil, want error")
		}
	})

	t.Run("rejects oversized prefix length", func(t *testing.T) {
		if _, err := NewLayout("/hydrus/db/client_files", 65); err == nil {
			t.Fatal("NewLayout() error = nil, want error")
		}
	})

	t.Run("rejects empty thumbnail root in split layout", func(t *testing.T) {
		if _, err := NewSplitLayout("/hydrus/db/client_files", "", 2); err == nil {
			t.Fatal("NewSplitLayout() error = nil, want error")
		}
	})

	t.Run("rejects invalid DB prefix layouts", func(t *testing.T) {
		if _, err := NewPrefixLayout(2, map[string]string{"zzz": "/hydrus/db/client_files"}); err == nil {
			t.Fatal("NewPrefixLayout() error = nil, want error")
		}

		if _, err := NewPrefixLayout(2, map[string]string{}); err == nil {
			t.Fatal("NewPrefixLayout(empty) error = nil, want error")
		}
	})
}

func repeatHex(pair string, count int) string {
	result := ""
	for range count {
		result += pair
	}

	return result
}
