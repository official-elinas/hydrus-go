package httpapi

import (
	"errors"
	"fmt"
	stdmime "mime"
	"net/http"
	"os"
	"strconv"

	"github.com/official-elinas/hydrus-go/internal/core/fileassets"
	"github.com/official-elinas/hydrus-go/internal/core/librarybrowse"
)

const (
	defaultRecentFilesLimit = 100
	maxRecentFilesLimit     = 250
)

func (s *Server) handleListRecentFiles(w http.ResponseWriter, r *http.Request) {
	_, statusCode, err := s.access.Authorize(
		r,
		PermissionSearchAndFetchFiles,
	)
	if err != nil {
		writeError(w, statusCode, err.Error())
		return
	}

	browseStore, ok := s.browseStore, s.browseStore != nil
	if !ok || browseStore == nil {
		writeError(
			w,
			http.StatusNotImplemented,
			"recent local browse is unavailable until HYDRUS_GO_DB_DIR is configured",
		)
		return
	}

	request, err := parseRecentFilesRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	page, err := browseStore.ListRecent(r.Context(), request)
	if err != nil {
		var unsupportedError *librarybrowse.UnsupportedError
		if errors.As(err, &unsupportedError) {
			writeError(w, http.StatusNotImplemented, err.Error())
			return
		}

		writeError(w, http.StatusInternalServerError, "could not load recent files")
		return
	}

	items := make([]map[string]any, 0, len(page.Items))
	for _, item := range page.Items {
		entry := map[string]any{
			"file_id":        item.FileID,
			"hash":           item.Hash,
			"mime":           item.MIME,
			"has_thumbnail":  item.HasThumbnail,
			"content_url":    fmt.Sprintf("/v1/files/content?file_id=%d", item.FileID),
			"thumbnail_url":  fmt.Sprintf("/v1/files/thumbnail?file_id=%d", item.FileID),
			"metadata_url":   fmt.Sprintf("/get_files/file_metadata?file_id=%d", item.FileID),
			"imported_at_ms": item.ImportedAtMS,
			"width":          item.Width,
			"height":         item.Height,
		}

		items = append(items, entry)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"offset":   request.Offset,
		"limit":    request.Limit,
		"has_more": page.HasMore,
		"items":    items,
	})
}

func (s *Server) handleGetFileContent(w http.ResponseWriter, r *http.Request) {
	s.handleResolvedFileAsset(w, r, func(store fileassets.Store, fileID int64) (fileassets.Descriptor, error) {
		return store.ResolveFileContent(r.Context(), fileID)
	})
}

func (s *Server) handleGetFileThumbnail(w http.ResponseWriter, r *http.Request) {
	s.handleResolvedFileAsset(w, r, func(store fileassets.Store, fileID int64) (fileassets.Descriptor, error) {
		return store.ResolveThumbnail(r.Context(), fileID)
	})
}

func (s *Server) handleResolvedFileAsset(
	w http.ResponseWriter,
	r *http.Request,
	resolve func(fileassets.Store, int64) (fileassets.Descriptor, error),
) {
	_, statusCode, err := s.access.Authorize(
		r,
		PermissionSearchAndFetchFiles,
	)
	if err != nil {
		writeError(w, statusCode, err.Error())
		return
	}

	assetStore, ok := s.assetStore, s.assetStore != nil
	if !ok || assetStore == nil {
		writeError(
			w,
			http.StatusNotImplemented,
			"file asset serving is unavailable until HYDRUS_GO_DB_DIR is configured",
		)
		return
	}

	fileID, err := parseAssetFileID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	descriptor, err := resolve(assetStore, fileID)
	if err != nil {
		var notFoundError *fileassets.NotFoundError
		if errors.As(err, &notFoundError) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}

		writeError(w, http.StatusInternalServerError, "could not resolve file asset")
		return
	}

	if err := serveManagedAsset(w, r, descriptor); err != nil {
		writeError(w, http.StatusInternalServerError, "could not stream file asset")
		return
	}
}

func parseRecentFilesRequest(r *http.Request) (librarybrowse.Request, error) {
	query := r.URL.Query()

	offset := 0
	if rawOffset := query.Get("offset"); rawOffset != "" {
		parsedOffset, err := strconv.Atoi(rawOffset)
		if err != nil {
			return librarybrowse.Request{}, fmt.Errorf("parse offset: %w", err)
		}

		offset = parsedOffset
	}

	limit := defaultRecentFilesLimit
	if rawLimit := query.Get("limit"); rawLimit != "" {
		parsedLimit, err := strconv.Atoi(rawLimit)
		if err != nil {
			return librarybrowse.Request{}, fmt.Errorf("parse limit: %w", err)
		}

		limit = parsedLimit
	}

	if offset < 0 {
		return librarybrowse.Request{}, fmt.Errorf("offset must be non-negative")
	}

	if limit <= 0 {
		return librarybrowse.Request{}, fmt.Errorf("limit must be greater than zero")
	}

	if limit > maxRecentFilesLimit {
		return librarybrowse.Request{}, fmt.Errorf(
			"limit must be less than or equal to %d",
			maxRecentFilesLimit,
		)
	}

	return librarybrowse.Request{Offset: offset, Limit: limit}, nil
}

func parseAssetFileID(r *http.Request) (int64, error) {
	rawFileID := r.URL.Query().Get("file_id")
	if rawFileID == "" {
		return 0, fmt.Errorf("please include a file_id in your request")
	}

	return parseFileID(rawFileID)
}

func serveManagedAsset(
	w http.ResponseWriter,
	r *http.Request,
	descriptor fileassets.Descriptor,
) error {
	file, err := os.Open(descriptor.Path)
	if err != nil {
		return fmt.Errorf("open managed asset: %w", err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat managed asset: %w", err)
	}

	contentType := descriptor.MIME
	if contentType == "" {
		contentType, err = sniffContentType(file)
		if err != nil {
			return err
		}
	}

	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}

	if descriptor.Filename != "" {
		disposition := stdmime.FormatMediaType(
			"inline",
			map[string]string{"filename": descriptor.Filename},
		)
		w.Header().Set("Content-Disposition", disposition)
	}

	http.ServeContent(w, r, descriptor.Filename, info.ModTime(), file)
	return nil
}

func sniffContentType(file *os.File) (string, error) {
	buffer := make([]byte, 512)
	n, err := file.Read(buffer)
	if err != nil {
		return "", fmt.Errorf("read managed asset header: %w", err)
	}

	if _, err := file.Seek(0, 0); err != nil {
		return "", fmt.Errorf("rewind managed asset: %w", err)
	}

	if n == 0 {
		return "application/octet-stream", nil
	}

	return http.DetectContentType(buffer[:n]), nil
}
