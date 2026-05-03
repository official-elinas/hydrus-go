package httpapi

import (
	"errors"
	"fmt"
	stdmime "mime"
	"net/http"
	"os"
	"strconv"
	"strings"

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

func (s *Server) handleSearchFiles(w http.ResponseWriter, r *http.Request) {
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
			"tag search is unavailable until HYDRUS_GO_DB_DIR is configured",
		)
		return
	}

	request, err := parseSearchFilesRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	page, err := browseStore.SearchByTags(r.Context(), request)
	if err != nil {
		var unsupportedError *librarybrowse.UnsupportedError
		if errors.As(err, &unsupportedError) {
			writeError(w, http.StatusNotImplemented, err.Error())
			return
		}

		writeError(w, http.StatusInternalServerError, "could not search files")
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
		"offset":            request.Offset,
		"limit":             request.Limit,
		"tags":              request.Tags,
		"sort_by":           string(request.SortBy),
		"system_predicates": request.SystemPredicates,
		"has_more":          page.HasMore,
		"items":             items,
	})
}

func parseSearchFilesRequest(r *http.Request) (librarybrowse.SearchRequest, error) {
	base, err := parseRecentFilesRequest(r)
	if err != nil {
		return librarybrowse.SearchRequest{}, err
	}

	rawTags := r.URL.Query()["tags"]
	tags := make([]string, 0, len(rawTags))
	for _, t := range rawTags {
		trimmed := strings.TrimSpace(t)
		if trimmed != "" {
			tags = append(tags, trimmed)
		}
	}

	sortBy, err := parseSortBy(r.URL.Query().Get("sort_by"))
	if err != nil {
		return librarybrowse.SearchRequest{}, err
	}

	predicates, favoriteFilter, err := parseSystemPredicatesAndFavorite(r.URL.Query()["system_predicates[]"])
	if err != nil {
		return librarybrowse.SearchRequest{}, err
	}

	return librarybrowse.SearchRequest{
		Request:          base,
		Tags:             tags,
		SortBy:           sortBy,
		SystemPredicates: predicates,
		FavoriteFilter:   favoriteFilter,
	}, nil
}

func parseSortBy(raw string) (librarybrowse.SortBy, error) {
	switch librarybrowse.SortBy(raw) {
	case "":
		return "", nil
	case librarybrowse.SortByImportNewest,
		librarybrowse.SortByImportOldest,
		librarybrowse.SortBySizeDesc,
		librarybrowse.SortBySizeAsc:
		return librarybrowse.SortBy(raw), nil
	default:
		return "", fmt.Errorf("unsupported sort_by value %q", raw)
	}
}

func parseSystemPredicatesAndFavorite(raws []string) ([]librarybrowse.SystemPredicate, *bool, error) {
	predicates := make([]librarybrowse.SystemPredicate, 0, len(raws))
	var favoriteFilter *bool
	for _, raw := range raws {
		trimmed := strings.TrimSpace(raw)
		if isFavoritePredicate(trimmed) {
			v, err := parseFavoriteValue(trimmed)
			if err != nil {
				return nil, nil, err
			}
			favoriteFilter = &v
			continue
		}
		if strings.HasPrefix(strings.ToLower(trimmed), "resolution") {
			extra, err := parseResolutionPredicate(trimmed)
			if err != nil {
				return nil, nil, err
			}
			predicates = append(predicates, extra...)
			continue
		}
		pred, err := parseOneSystemPredicate(trimmed)
		if err != nil {
			return nil, nil, err
		}
		predicates = append(predicates, pred)
	}
	return predicates, favoriteFilter, nil
}

func isFavoritePredicate(s string) bool {
	lower := strings.ToLower(s)
	return lower == "favorite" || lower == "favourite" ||
		strings.HasPrefix(lower, "favorite=") || strings.HasPrefix(lower, "favourite=")
}

func parseFavoriteValue(s string) (bool, error) {
	lower := strings.ToLower(s)
	if lower == "favorite" || lower == "favourite" {
		return true, nil
	}
	var valStr string
	if strings.HasPrefix(lower, "favorite=") {
		valStr = s[len("favorite="):]
	} else {
		valStr = s[len("favourite="):]
	}
	switch strings.ToLower(valStr) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("invalid favorite value %q: want true or false", valStr)
	}
}

func parseResolutionPredicate(raw string) ([]librarybrowse.SystemPredicate, error) {
	lower := strings.ToLower(raw)
	ops := []string{"<=", ">=", "<", ">", "="}
	for _, op := range ops {
		idx := strings.Index(lower, op)
		if idx < 0 {
			continue
		}
		valueStr := strings.TrimSpace(raw[idx+len(op):])
		xIdx := strings.IndexByte(valueStr, 'x')
		if xIdx < 0 {
			xIdx = strings.IndexByte(valueStr, 'X')
		}
		if xIdx < 0 {
			return nil, fmt.Errorf("resolution value %q must be in WxH format", valueStr)
		}
		wStr := valueStr[:xIdx]
		hStr := valueStr[xIdx+1:]
		w, err := strconv.ParseInt(strings.TrimSpace(wStr), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse resolution width %q: %w", wStr, err)
		}
		h, err := strconv.ParseInt(strings.TrimSpace(hStr), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse resolution height %q: %w", hStr, err)
		}
		predOp := librarybrowse.PredicateOp(op)
		return []librarybrowse.SystemPredicate{
			{Field: librarybrowse.PredicateFieldWidth, Op: predOp, Value: w},
			{Field: librarybrowse.PredicateFieldHeight, Op: predOp, Value: h},
		}, nil
	}
	return nil, fmt.Errorf("cannot parse resolution predicate %q", raw)
}

func parseOneSystemPredicate(raw string) (librarybrowse.SystemPredicate, error) {
	raw = strings.TrimSpace(raw)

	ops := []string{"<=", ">=", "<", ">", "="}
	for _, op := range ops {
		idx := strings.Index(raw, op)
		if idx < 0 {
			continue
		}

		fieldStr := strings.TrimSpace(raw[:idx])
		valueStr := strings.TrimSpace(raw[idx+len(op):])

		field, ok := parsePredicateField(fieldStr)
		if !ok {
			return librarybrowse.SystemPredicate{}, fmt.Errorf("unsupported predicate field %q", fieldStr)
		}

		value, err := strconv.ParseInt(valueStr, 10, 64)
		if err != nil {
			return librarybrowse.SystemPredicate{}, fmt.Errorf("parse predicate value %q: %w", valueStr, err)
		}

		return librarybrowse.SystemPredicate{
			Field: field,
			Op:    librarybrowse.PredicateOp(op),
			Value: value,
		}, nil
	}

	return librarybrowse.SystemPredicate{}, fmt.Errorf("cannot parse system predicate %q", raw)
}

func parsePredicateField(s string) (librarybrowse.PredicateField, bool) {
	switch librarybrowse.PredicateField(s) {
	case librarybrowse.PredicateFieldSize,
		librarybrowse.PredicateFieldWidth,
		librarybrowse.PredicateFieldHeight:
		return librarybrowse.PredicateField(s), true
	default:
		return "", false
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
