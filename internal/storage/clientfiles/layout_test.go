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
			if _, err := layout.ResolveFilePath(hash, ext); err == nil {
				t.Fatalf("ResolveFilePath(%q) error = nil, want error", ext)
			}
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
}

func repeatHex(pair string, count int) string {
	result := ""
	for range count {
		result += pair
	}

	return result
}
