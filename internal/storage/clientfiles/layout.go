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
	Root          string
	ThumbnailRoot string
	PrefixLength  int
	prefixRoots   map[string]string
}

// NewLayout validates and normalizes a managed client_files layout.
func NewLayout(root string, prefixLength int) (Layout, error) {
	return NewSplitLayout(root, root, prefixLength)
}

// NewSplitLayout validates and normalizes a managed client_files layout with
// separate roots for originals and thumbnails.
func NewSplitLayout(
	fileRoot string,
	thumbnailRoot string,
	prefixLength int,
) (Layout, error) {
	cleanFileRoot, err := normalizeRoot("client_files root", fileRoot)
	if err != nil {
		return Layout{}, err
	}

	cleanThumbnailRoot, err := normalizeRoot("thumbnail root", thumbnailRoot)
	if err != nil {
		return Layout{}, err
	}

	if err := validatePrefixLength(prefixLength); err != nil {
		return Layout{}, err
	}

	return Layout{
		Root:          cleanFileRoot,
		ThumbnailRoot: cleanThumbnailRoot,
		PrefixLength:  prefixLength,
	}, nil
}

// NewPrefixLayout validates and normalizes a managed client_files layout whose
// roots are provided per prefix.
func NewPrefixLayout(prefixLength int, prefixRoots map[string]string) (Layout, error) {
	if err := validatePrefixLength(prefixLength); err != nil {
		return Layout{}, err
	}

	if len(prefixRoots) == 0 {
		return Layout{}, fmt.Errorf("managed prefix roots are required")
	}

	normalizedRoots := make(map[string]string, len(prefixRoots))
	for rawPrefix, rawRoot := range prefixRoots {
		prefix := strings.ToLower(strings.TrimSpace(rawPrefix))
		if err := validateManagedPrefix(prefix, prefixLength); err != nil {
			return Layout{}, err
		}

		root, err := normalizeRoot(fmt.Sprintf("root for prefix %q", prefix), rawRoot)
		if err != nil {
			return Layout{}, err
		}

		normalizedRoots[prefix] = root
	}

	return Layout{
		PrefixLength: prefixLength,
		prefixRoots:  normalizedRoots,
	}, nil
}

// DefaultRoot derives the default managed client_files root for a Hydrus DB
// directory.
func DefaultRoot(dbDir string) string {
	return DefaultFileRoot(dbDir)
}

// DefaultFileRoot derives the default managed originals root for a Hydrus DB
// directory.
func DefaultFileRoot(dbDir string) string {
	return filepath.Join(filepath.Clean(strings.TrimSpace(dbDir)), "client_files")
}

// DefaultThumbnailRoot derives the default managed thumbnail root for a Hydrus
// DB directory.
func DefaultThumbnailRoot(dbDir string) string {
	return filepath.Join(HydrusRoot(dbDir), "thumbnails")
}

// HydrusRoot derives the Hydrus application root from a DB directory.
func HydrusRoot(dbDir string) string {
	return filepath.Dir(filepath.Clean(strings.TrimSpace(dbDir)))
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

	root, err := l.rootForHash(KindFile, normalizedHash)
	if err != nil {
		return "", err
	}

	relativeDir, err := RelativeDirectory(KindFile, normalizedHash, l.PrefixLength)
	if err != nil {
		return "", err
	}

	filename := normalizedHash + normalizedExt
	return filepath.Join(root, relativeDir, filename), nil
}

// ResolveThumbnailPath returns the managed thumbnail path rooted under the
// layout root for a SHA-256.
func (l Layout) ResolveThumbnailPath(hashHex string) (string, error) {
	normalizedHash, err := NormalizeSHA256Hex(hashHex)
	if err != nil {
		return "", err
	}

	root, err := l.rootForHash(KindThumbnail, normalizedHash)
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
	return filepath.Join(root, relativeDir, filename), nil
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
		return "", nil
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

func (l Layout) rootForHash(kind Kind, hashHex string) (string, error) {
	prefix, err := Prefix(kind, hashHex, l.PrefixLength)
	if err != nil {
		return "", err
	}

	return l.rootForPrefix(prefix)
}

func (l Layout) rootForPrefix(prefix string) (string, error) {
	if len(l.prefixRoots) > 0 {
		if root, ok := l.prefixRoots[prefix]; ok {
			return root, nil
		}

		return "", fmt.Errorf("managed storage root for prefix %q is not configured", prefix)
	}

	if strings.HasPrefix(prefix, string(KindThumbnail)) {
		if strings.TrimSpace(l.ThumbnailRoot) != "" {
			return l.ThumbnailRoot, nil
		}
	}

	if strings.TrimSpace(l.Root) == "" {
		return "", fmt.Errorf("managed file root is required")
	}

	return l.Root, nil
}

func normalizeRoot(label string, root string) (string, error) {
	cleanRoot := filepath.Clean(strings.TrimSpace(root))
	if cleanRoot == "." || cleanRoot == "" {
		return "", fmt.Errorf("%s is required", label)
	}

	return cleanRoot, nil
}

func validatePrefixLength(prefixLength int) error {
	if prefixLength <= 0 {
		return fmt.Errorf("prefix length must be greater than zero")
	}

	if prefixLength > maxPrefixLength {
		return fmt.Errorf("prefix length must not exceed %d", maxPrefixLength)
	}

	return nil
}

func validateManagedPrefix(prefix string, prefixLength int) error {
	if len(prefix) != prefixLength+1 {
		return fmt.Errorf(
			"managed prefix %q must be %d characters long",
			prefix,
			prefixLength+1,
		)
	}

	if !strings.HasPrefix(prefix, string(KindFile)) && !strings.HasPrefix(prefix, string(KindThumbnail)) {
		return fmt.Errorf("managed prefix %q must start with %q or %q", prefix, KindFile, KindThumbnail)
	}

	for _, r := range prefix[1:] {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') {
			continue
		}

		return fmt.Errorf("managed prefix %q contains invalid character %q", prefix, r)
	}

	return nil
}
