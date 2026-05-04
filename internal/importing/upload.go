package importing

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/official-elinas/hydrus-go/internal/core/fileimport"
	"github.com/official-elinas/hydrus-go/internal/core/mimes"
)

var stableUploadSourceModTime = time.UnixMilli(0).UTC()

// ImportUpload imports one daemon-staged upload through the existing
// prepared-file import pipeline.
func (i *Importer) ImportUpload(
	ctx context.Context,
	request fileimport.UploadRequest,
) (fileimport.Result, error) {
	if i == nil {
		return fileimport.Result{}, &fileimport.RequestError{Message: "importer is nil"}
	}

	stagedPath, err := normalizeUploadStagedPath(request.StagedPath)
	if err != nil {
		return fileimport.Result{}, err
	}

	info, err := os.Stat(stagedPath)
	if err != nil {
		return fileimport.Result{}, fmt.Errorf("stat staged upload path %q: %w", stagedPath, err)
	}

	if !info.Mode().IsRegular() {
		return fileimport.Result{}, &fileimport.RequestError{
			Message: "staged upload path must point to a regular file",
		}
	}

	fileModifiedAtMS, err := normalizeUploadFileModifiedAtMS(request.FileModifiedAtMS)
	if err != nil {
		return fileimport.Result{}, err
	}

	if err := applyUploadSourceModTime(stagedPath, fileModifiedAtMS); err != nil {
		return fileimport.Result{}, fmt.Errorf("preserve uploaded file timestamp: %w", err)
	}

	hashHex, err := hashUploadedFile(ctx, stagedPath)
	if err != nil {
		return fileimport.Result{}, err
	}

	mimeEnum, err := detectUploadImportMIME(stagedPath, request.Filename)
	if err != nil {
		return fileimport.Result{}, err
	}

	stillImageMetadata := detectStillImageImportMetadata(stagedPath, mimeEnum)
	videoMetadata := detectVideoImportMetadata(stagedPath, mimeEnum)
	importedAtMS := time.Now().UTC().UnixMilli()

	result, err := i.ImportPreparedFile(ctx, PreparedFile{
		SourcePath:          stagedPath,
		HashHex:             hashHex,
		KnownURLs:           append([]string(nil), request.KnownURLs...),
		Size:                info.Size(),
		Mime:                mimeEnum,
		Width:               stillImageMetadata.Width,
		Height:              stillImageMetadata.Height,
		PixelHashHex:        stillImageMetadata.PixelHashHex,
		HasTransparency:     stillImageMetadata.HasTransparency,
		Duration:            videoMetadata.Duration,
		NumFrames:           videoMetadata.NumFrames,
		HasAudio:            videoMetadata.HasAudio,
		ImportedAtMS:        importedAtMS,
		FileModifiedAtMS:    fileModifiedAtMS,
		LocalFileServiceKey: strings.TrimSpace(request.LocalFileServiceKey),
	})
	if err != nil {
		return fileimport.Result{}, classifyImportError(err)
	}

	return i.finalizeImportedFile(ctx, result, hashHex, mimeEnum), nil
}

func normalizeUploadStagedPath(path string) (string, error) {
	normalized := filepath.Clean(strings.TrimSpace(path))
	if normalized == "." || normalized == "" {
		return "", &fileimport.RequestError{Message: "staged upload path is required"}
	}

	if !filepath.IsAbs(normalized) {
		return "", &fileimport.RequestError{Message: "staged upload path must be absolute"}
	}

	return normalized, nil
}

func normalizeUploadFileModifiedAtMS(value *int64) (*int64, error) {
	if value == nil {
		return nil, nil
	}

	normalized := *value
	if normalized <= 0 {
		return nil, &fileimport.RequestError{Message: "file_modified_at_ms must be greater than zero"}
	}

	return &normalized, nil
}

func applyUploadSourceModTime(path string, fileModifiedAtMS *int64) error {
	modifiedAt := stableUploadSourceModTime
	if fileModifiedAtMS != nil {
		modifiedAt = time.UnixMilli(*fileModifiedAtMS).UTC()
	}

	return os.Chtimes(path, modifiedAt, modifiedAt)
}

func hashUploadedFile(ctx context.Context, path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open staged upload path %q: %w", path, err)
	}
	defer file.Close()

	hasher := sha256.New()
	buffer := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			return "", fmt.Errorf("hash uploaded file: %w", err)
		}

		n, readErr := file.Read(buffer)
		if n > 0 {
			if _, err := hasher.Write(buffer[:n]); err != nil {
				return "", fmt.Errorf("hash uploaded file bytes: %w", err)
			}
		}

		if readErr == io.EOF {
			break
		}

		if readErr != nil {
			return "", fmt.Errorf("read uploaded file bytes: %w", readErr)
		}
	}

	return fmt.Sprintf("%x", hasher.Sum(nil)), nil
}

func detectUploadImportMIME(stagedPath string, filename string) (int, error) {
	file, err := os.Open(stagedPath)
	if err != nil {
		return 0, fmt.Errorf("open staged upload path %q for MIME detection: %w", stagedPath, err)
	}
	defer file.Close()

	buffer := make([]byte, 512)
	n, err := file.Read(buffer)
	if err != nil && err != io.EOF {
		return 0, fmt.Errorf("read uploaded file header: %w", err)
	}

	if mimeEnum, ok := mimes.FromMIMEType(http.DetectContentType(buffer[:n])); ok {
		return mimeEnum, nil
	}

	if mimeEnum, ok := mimes.FromExtension(filepath.Ext(sanitizeUploadFilename(filename))); ok {
		return mimeEnum, nil
	}

	if mimeEnum, ok := detectImportMIMEWithFFmpeg(stagedPath); ok {
		return mimeEnum, nil
	}

	return 0, &fileimport.RequestError{
		Message: fmt.Sprintf("%s has an unsupported file type", describeUploadFilename(filename)),
	}
}

func sanitizeUploadFilename(value string) string {
	sanitized := strings.TrimSpace(value)
	if sanitized == "" {
		return ""
	}

	separatorIndex := strings.LastIndexAny(sanitized, `/\\`)
	if separatorIndex >= 0 {
		sanitized = sanitized[separatorIndex+1:]
	}

	return strings.TrimSpace(sanitized)
}

func describeUploadFilename(value string) string {
	filename := sanitizeUploadFilename(value)
	if filename == "" {
		return "uploaded file"
	}

	return fmt.Sprintf("uploaded file %q", filename)
}
