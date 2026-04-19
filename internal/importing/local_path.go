package importing

import (
	"context"
	"crypto/sha256"
	"fmt"
	"image"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/official-elinas/hydrus-go/internal/core/fileimport"
	"github.com/official-elinas/hydrus-go/internal/core/mimes"

	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
)

// ImportLocalPath imports one daemon-local file path through the existing
// prepared-file import pipeline.
func (i *Importer) ImportLocalPath(
	ctx context.Context,
	request fileimport.Request,
) (fileimport.Result, error) {
	if i == nil {
		return fileimport.Result{}, &fileimport.RequestError{Message: "importer is nil"}
	}

	sourcePath, err := normalizeLocalImportPath(request.Path)
	if err != nil {
		return fileimport.Result{}, err
	}

	info, err := os.Stat(sourcePath)
	if err != nil {
		if os.IsNotExist(err) {
			return fileimport.Result{}, &fileimport.NotFoundError{
				Message: fmt.Sprintf("local file path %q was not found", sourcePath),
			}
		}

		return fileimport.Result{}, &fileimport.RequestError{
			Message: fmt.Sprintf("stat local file path %q: %v", sourcePath, err),
		}
	}

	if !info.Mode().IsRegular() {
		return fileimport.Result{}, &fileimport.RequestError{
			Message: "local file path must point to a regular file",
		}
	}

	hashHex, err := hashLocalFile(ctx, sourcePath)
	if err != nil {
		return fileimport.Result{}, err
	}

	mimeEnum, err := detectLocalImportMIME(sourcePath)
	if err != nil {
		return fileimport.Result{}, err
	}

	width, height := detectImageDimensions(sourcePath, mimeEnum)

	importedAtMS := time.Now().UTC().UnixMilli()
	var fileModifiedAtMS *int64
	if modifiedAtMS := info.ModTime().UTC().UnixMilli(); modifiedAtMS > 0 {
		fileModifiedAtMS = &modifiedAtMS
	}

	result, err := i.ImportPreparedFile(ctx, PreparedFile{
		SourcePath:          sourcePath,
		HashHex:             hashHex,
		Size:                info.Size(),
		Mime:                mimeEnum,
		Width:               width,
		Height:              height,
		ImportedAtMS:        importedAtMS,
		FileModifiedAtMS:    fileModifiedAtMS,
		LocalFileServiceKey: strings.TrimSpace(request.LocalFileServiceKey),
	})
	if err != nil {
		return fileimport.Result{}, classifyImportError(err)
	}

	return i.finalizeImportedFile(ctx, result, hashHex, mimeEnum), nil
}

func normalizeLocalImportPath(path string) (string, error) {
	normalized := filepath.Clean(strings.TrimSpace(path))
	if normalized == "." || normalized == "" {
		return "", &fileimport.RequestError{Message: "local file path is required"}
	}

	if !filepath.IsAbs(normalized) {
		return "", &fileimport.RequestError{Message: "local file path must be absolute"}
	}

	return normalized, nil
}

func hashLocalFile(ctx context.Context, path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", &fileimport.RequestError{
			Message: fmt.Sprintf("open local file path %q: %v", path, err),
		}
	}
	defer file.Close()

	hasher := sha256.New()
	buffer := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			return "", fmt.Errorf("hash local file: %w", err)
		}

		n, readErr := file.Read(buffer)
		if n > 0 {
			if _, err := hasher.Write(buffer[:n]); err != nil {
				return "", fmt.Errorf("hash local file bytes: %w", err)
			}
		}

		if readErr == io.EOF {
			break
		}

		if readErr != nil {
			return "", fmt.Errorf("read local file bytes: %w", readErr)
		}
	}

	return fmt.Sprintf("%x", hasher.Sum(nil)), nil
}

func detectLocalImportMIME(path string) (int, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, &fileimport.RequestError{
			Message: fmt.Sprintf("open local file path %q for MIME detection: %v", path, err),
		}
	}
	defer file.Close()

	buffer := make([]byte, 512)
	n, err := file.Read(buffer)
	if err != nil && err != io.EOF {
		return 0, fmt.Errorf("read local file header: %w", err)
	}

	if mimeEnum, ok := mimes.FromMIMEType(http.DetectContentType(buffer[:n])); ok {
		return mimeEnum, nil
	}

	if mimeEnum, ok := mimes.FromExtension(filepath.Ext(path)); ok {
		return mimeEnum, nil
	}

	return 0, &fileimport.RequestError{
		Message: fmt.Sprintf("local file path %q has an unsupported file type", path),
	}
}

func detectImageDimensions(path string, mimeEnum int) (*int64, *int64) {
	if !supportsDecodeConfigDimensions(mimeEnum) {
		return nil, nil
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, nil
	}
	defer file.Close()

	config, _, err := image.DecodeConfig(file)
	if err != nil {
		return nil, nil
	}

	width := int64(config.Width)
	height := int64(config.Height)
	return &width, &height
}

func supportsDecodeConfigDimensions(mimeEnum int) bool {
	switch mimeEnum {
	case 1, 2, 3:
		return true
	default:
		return false
	}
}

func classifyImportError(err error) error {
	message := err.Error()
	switch {
	case strings.Contains(message, "local file service key"):
		return &fileimport.RequestError{Message: message}
	case strings.Contains(message, "multiple local file domain services"):
		return &fileimport.RequestError{Message: message}
	case strings.Contains(message, "is not a local file domain"):
		return &fileimport.RequestError{Message: message}
	default:
		return err
	}
}
