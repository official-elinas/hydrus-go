// Package clientfiles resolves deterministic Hydrus managed-storage paths.
package clientfiles

import (
	"fmt"
	"path/filepath"
	"strings"
)

const (
	// DefaultPrefixLength matches Hydrus's current default client-files
	// granularity.
	DefaultPrefixLength = 2
	maxPrefixLength     = 64
)

// Kind identifies the managed storage namespace.
type Kind string

const (
	KindFile      Kind = "f"
	KindThumbnail Kind = "t"
)

// Layout describes a deterministic managed client_files layout rooted at a
// single base path.
type Layout struct {
	Root         string
	PrefixLength int
}

// NewLayout validates and normalizes a managed client_files layout.
func NewLayout(root string, prefixLength int) (Layout, error) {
	cleanRoot := filepath.Clean(strings.TrimSpace(root))
	if cleanRoot == "." || cleanRoot == "" {
		return Layout{}, fmt.Errorf("client_files root is required")
	}

	if prefixLength <= 0 {
		return Layout{}, fmt.Errorf("prefix length must be greater than zero")
	}

	if prefixLength > maxPrefixLength {
		return Layout{}, fmt.Errorf("prefix length must not exceed %d", maxPrefixLength)
	}

	return Layout{
		Root:         cleanRoot,
		PrefixLength: prefixLength,
	}, nil
}

// DefaultRoot derives the default managed client_files root for a Hydrus DB
// directory.
func DefaultRoot(dbDir string) string {
	return filepath.Join(filepath.Clean(strings.TrimSpace(dbDir)), "client_files")
}

// ResolveFilePath returns the managed file path rooted under the layout root
// for a SHA-256 and extension.
func (l Layout) ResolveFilePath(hashHex string, ext string) (string, error) {
	normalizedHash, err := NormalizeSHA256Hex(hashHex)
	if err != nil {
		return "", err
	}

	normalizedExt, err := normalizeExtension(ext)
	if err != nil {
		return "", err
	}

	relativeDir, err := RelativeDirectory(KindFile, normalizedHash, l.PrefixLength)
	if err != nil {
		return "", err
	}

	filename := normalizedHash + normalizedExt
	return filepath.Join(l.Root, relativeDir, filename), nil
}

// ResolveThumbnailPath returns the managed thumbnail path rooted under the
// layout root for a SHA-256.
func (l Layout) ResolveThumbnailPath(hashHex string) (string, error) {
	normalizedHash, err := NormalizeSHA256Hex(hashHex)
	if err != nil {
		return "", err
	}

	relativeDir, err := RelativeDirectory(
		KindThumbnail,
		normalizedHash,
		l.PrefixLength,
	)
	if err != nil {
		return "", err
	}

	filename := normalizedHash + ".thumbnail"
	return filepath.Join(l.Root, relativeDir, filename), nil
}

// NormalizeSHA256Hex validates and canonicalizes a SHA-256 hex digest.
func NormalizeSHA256Hex(hashHex string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(hashHex))
	if len(normalized) != 64 {
		return "", fmt.Errorf("sha256 hex must be 64 characters")
	}

	for _, r := range normalized {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') {
			continue
		}

		return "", fmt.Errorf("sha256 hex contains invalid character %q", r)
	}

	return normalized, nil
}

// Prefix returns the Hydrus managed-storage prefix for a hash and kind.
func Prefix(kind Kind, hashHex string, prefixLength int) (string, error) {
	normalizedHash, err := NormalizeSHA256Hex(hashHex)
	if err != nil {
		return "", err
	}

	if prefixLength <= 0 {
		return "", fmt.Errorf("prefix length must be greater than zero")
	}

	if prefixLength > maxPrefixLength {
		return "", fmt.Errorf("prefix length must not exceed %d", maxPrefixLength)
	}

	if kind != KindFile && kind != KindThumbnail {
		return "", fmt.Errorf("unknown managed storage kind %q", kind)
	}

	return string(kind) + normalizedHash[:prefixLength], nil
}

// RelativeDirectory converts a Hydrus managed prefix into a relative directory
// path.
func RelativeDirectory(kind Kind, hashHex string, prefixLength int) (string, error) {
	prefix, err := Prefix(kind, hashHex, prefixLength)
	if err != nil {
		return "", err
	}

	return relativeDirectoryFromPrefix(prefix)
}

func normalizeExtension(ext string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(ext))
	if normalized == "" {
		return "", fmt.Errorf("managed file extension is required")
	}

	normalized = strings.TrimPrefix(normalized, ".")
	if normalized == "" {
		return "", fmt.Errorf("managed file extension is required")
	}

	if strings.ContainsAny(normalized, `/\`) {
		return "", fmt.Errorf("managed file extension must not contain path separators")
	}

	return "." + normalized, nil
}

func relativeDirectoryFromPrefix(prefix string) (string, error) {
	if len(prefix) < 2 {
		return "", fmt.Errorf("managed prefix must include kind and hash prefix")
	}

	kind := prefix[:1]
	remainder := prefix[1:]
	if remainder == "" {
		return "", fmt.Errorf("managed prefix must include hash characters")
	}

	parts := []string{}
	for len(remainder) > 0 {
		chunkLen := 2
		if len(remainder) < chunkLen {
			chunkLen = len(remainder)
		}

		parts = append(parts, remainder[:chunkLen])
		remainder = remainder[chunkLen:]
	}

	parts[0] = kind + parts[0]
	return filepath.Join(parts...), nil
}
