package importing

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/official-elinas/hydrus-go/internal/core/fileimport"
)

const urlImportResponseLimitBytes int64 = 4 << 30

// ImportURL downloads one direct file URL through the daemon and imports the
// result through the existing staged upload pipeline.
func (i *Importer) ImportURL(
	ctx context.Context,
	request fileimport.URLRequest,
) (fileimport.Result, error) {
	if i == nil {
		return fileimport.Result{}, &fileimport.RequestError{Message: "importer is nil"}
	}

	importURL, err := normalizeImportURL(request.URL)
	if err != nil {
		return fileimport.Result{}, err
	}

	referralURL, err := normalizeOptionalImportURL(request.ReferralURL)
	if err != nil {
		return fileimport.Result{}, err
	}

	stagedPath, filename, finalURL, cleanup, err := stageImportedURL(ctx, importURL, referralURL)
	if err != nil {
		return fileimport.Result{}, err
	}
	defer cleanup()

	knownURLs := []string{importURL}
	if finalURL != "" && finalURL != importURL {
		knownURLs = append(knownURLs, finalURL)
	}

	hashHex, err := hashUploadedFile(ctx, stagedPath)
	if err != nil {
		return fileimport.Result{}, err
	}

	alreadyImportedFileID, alreadyImported, err := i.bundle.LookupImportedFileIDByHash(ctx, hashHex)
	if err != nil {
		return fileimport.Result{}, err
	}

	if alreadyImported {
		if err := i.bundle.AddKnownURLsByHash(ctx, hashHex, knownURLs); err != nil {
			return fileimport.Result{}, err
		}

		return fileimport.Result{
			FileID:                    alreadyImportedFileID,
			Hash:                      hashHex,
			AlreadyImported:           true,
			ManagedFileAlreadyPresent: true,
		}, nil
	}

	return i.ImportUpload(ctx, fileimport.UploadRequest{
		StagedPath:          stagedPath,
		Filename:            filename,
		LocalFileServiceKey: strings.TrimSpace(request.LocalFileServiceKey),
		KnownURLs:           knownURLs,
	})
}

func normalizeImportURL(rawURL string) (string, error) {
	parsedURL, ok := parseImportURL(strings.TrimSpace(rawURL))
	if !ok {
		return "", &fileimport.RequestError{Message: "url must be a full http or https URL"}
	}

	return parsedURL.String(), nil
}

func normalizeOptionalImportURL(rawURL string) (string, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return "", nil
	}

	return normalizeImportURL(trimmed)
}

func parseImportURL(rawURL string) (*url.URL, bool) {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return nil, false
	}

	scheme := strings.ToLower(strings.TrimSpace(parsedURL.Scheme))
	if scheme != "http" && scheme != "https" {
		return nil, false
	}

	host := strings.TrimSpace(parsedURL.Hostname())
	if host == "" {
		return nil, false
	}

	clone := *parsedURL
	clone.Scheme = scheme
	clone.Fragment = ""
	clone.RawFragment = ""
	return &clone, true
}

func stageImportedURL(
	ctx context.Context,
	importURL string,
	referralURL string,
) (string, string, string, func(), error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, importURL, nil)
	if err != nil {
		return "", "", "", func() {}, &fileimport.RequestError{Message: fmt.Sprintf("build URL import request: %v", err)}
	}
	if referralURL != "" {
		req.Header.Set("Referer", referralURL)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", "", func() {}, &fileimport.RequestError{Message: fmt.Sprintf("request URL import: %v", err)}
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", "", func() {}, &fileimport.RequestError{Message: fmt.Sprintf("URL import returned HTTP %d", resp.StatusCode)}
	}

	tempFile, err := os.CreateTemp("", "hydrus-go-url-import-*")
	if err != nil {
		return "", "", "", func() {}, fmt.Errorf("create URL import temp file: %w", err)
	}

	tempPath := tempFile.Name()
	cleanup := func() {
		_ = os.Remove(tempPath)
	}

	limited := io.LimitReader(resp.Body, urlImportResponseLimitBytes+1)
	written, err := io.Copy(tempFile, limited)
	if err != nil {
		_ = tempFile.Close()
		cleanup()
		return "", "", "", func() {}, fmt.Errorf("write URL import temp file: %w", err)
	}
	if written > urlImportResponseLimitBytes {
		_ = tempFile.Close()
		cleanup()
		return "", "", "", func() {}, &fileimport.RequestError{Message: fmt.Sprintf("URL import exceeds %d bytes", urlImportResponseLimitBytes)}
	}
	if err := tempFile.Close(); err != nil {
		cleanup()
		return "", "", "", func() {}, fmt.Errorf("close URL import temp file: %w", err)
	}

	if modifiedAt := resp.Header.Get("Last-Modified"); strings.TrimSpace(modifiedAt) != "" {
		if parsedModifiedAt, err := http.ParseTime(modifiedAt); err == nil {
			_ = os.Chtimes(tempPath, parsedModifiedAt, parsedModifiedAt)
		}
	}

	finalURL := ""
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}

	filename := downloadFilename(importURL, finalURL)
	return tempPath, filename, finalURL, cleanup, nil
}

func downloadFilename(importURL string, finalURL string) string {
	for _, rawURL := range []string{finalURL, importURL} {
		trimmed := strings.TrimSpace(rawURL)
		if trimmed == "" {
			continue
		}

		parsed, err := url.Parse(trimmed)
		if err != nil {
			continue
		}

		name := strings.TrimSpace(filepath.Base(parsed.Path))
		if name != "" && name != "/" && name != "." {
			return name
		}
	}

	return "downloaded-file"
}
