package httpapi

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/official-elinas/hydrus-go/internal/core/filemetadata"
	"github.com/official-elinas/hydrus-go/internal/core/services"
)

const maxReasonableFileID = 1024 * 1024 * 1024 * 1024 * 1024

func (s *Server) handleGetFileMetadata(w http.ResponseWriter, r *http.Request) {
	_, statusCode, err := s.access.Authorize(
		r,
		PermissionSearchAndFetchFiles,
	)
	if err != nil {
		writeError(w, statusCode, err.Error())
		return
	}

	if s.metadataStore == nil {
		writeError(
			w,
			http.StatusNotImplemented,
			"file metadata is unavailable until HYDRUS_GO_DB_DIR is configured",
		)
		return
	}

	request, err := parseFileMetadataRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	metadata, err := s.metadataStore.GetMetadata(r.Context(), request)
	if err != nil {
		var notFoundError *filemetadata.NotFoundError
		var unsupportedError *filemetadata.UnsupportedError

		switch {
		case errors.As(err, &notFoundError):
			writeError(w, http.StatusNotFound, err.Error())
			return
		case errors.As(err, &unsupportedError):
			writeError(w, http.StatusNotImplemented, err.Error())
			return
		default:
			writeError(w, http.StatusInternalServerError, "could not load file metadata")
			return
		}
	}

	body := map[string]any{"metadata": metadata}

	if request.IncludeServicesObject {
		catalog, err := s.services.List(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not load services")
			return
		}

		catalog, err = s.augmentCatalogWithMetadataServices(
			r.Context(),
			catalog,
			metadata,
		)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not load services")
			return
		}

		body["services"] = catalog.LegacyMap()
		body["services_v2"] = catalog
	}

	writeJSON(w, http.StatusOK, body)
}

func parseFileMetadataRequest(r *http.Request) (filemetadata.Request, error) {
	query := r.URL.Query()

	onlyReturnIdentifiers, err := queryBool(
		query,
		"only_return_identifiers",
		false,
	)
	if err != nil {
		return filemetadata.Request{}, err
	}

	onlyReturnBasicInformation, err := queryBool(
		query,
		"only_return_basic_information",
		false,
	)
	if err != nil {
		return filemetadata.Request{}, err
	}

	includeServicesObject, err := queryBool(
		query,
		"include_services_object",
		true,
	)
	if err != nil {
		return filemetadata.Request{}, err
	}

	hideServiceKeysTags, err := queryBool(
		query,
		"hide_service_keys_tags",
		true,
	)
	if err != nil {
		return filemetadata.Request{}, err
	}

	includeBlurhash, err := queryBool(query, "include_blurhash", false)
	if err != nil {
		return filemetadata.Request{}, err
	}

	includeMilliseconds, err := queryBool(
		query,
		"include_milliseconds",
		false,
	)
	if err != nil {
		return filemetadata.Request{}, err
	}

	detailedURLInformation, err := queryBool(
		query,
		"detailed_url_information",
		false,
	)
	if err != nil {
		return filemetadata.Request{}, err
	}

	includeNotes, err := queryBool(query, "include_notes", false)
	if err != nil {
		return filemetadata.Request{}, err
	}

	createNewFileIDs, err := queryBool(query, "create_new_file_ids", false)
	if err != nil {
		return filemetadata.Request{}, err
	}

	hashes, err := parseHashes(query)
	if err != nil {
		return filemetadata.Request{}, err
	}

	fileIDs, err := parseFileIDs(query)
	if err != nil {
		return filemetadata.Request{}, err
	}

	if len(hashes) == 0 && len(fileIDs) == 0 {
		return filemetadata.Request{}, fmt.Errorf(
			"please include some files in your request--file_id or hash based",
		)
	}

	return filemetadata.Request{
		Hashes:                       hashes,
		FileIDs:                      fileIDs,
		OnlyReturnIdentifiers:        onlyReturnIdentifiers,
		OnlyReturnBasicInformation:   onlyReturnBasicInformation,
		IncludeServicesObject:        includeServicesObject,
		IncludeLegacyServiceKeysTags: !hideServiceKeysTags,
		IncludeBlurhash:              includeBlurhash,
		IncludeMilliseconds:          includeMilliseconds,
		DetailedURLInformation:       detailedURLInformation,
		IncludeNotes:                 includeNotes,
		CreateNewFileIDs:             createNewFileIDs,
	}, nil
}

func (s *Server) augmentCatalogWithMetadataServices(
	ctx context.Context,
	catalog services.Catalog,
	metadata []filemetadata.Row,
) (services.Catalog, error) {
	augmented := append(services.Catalog{}, catalog...)
	seen := map[string]struct{}{}
	for _, service := range augmented {
		seen[service.ServiceKey] = struct{}{}
	}

	for _, serviceKey := range metadataReferencedServiceKeys(metadata) {
		if _, ok := seen[serviceKey]; ok {
			continue
		}

		service, ok, err := s.services.ByKey(ctx, serviceKey)
		if err != nil {
			return nil, err
		}

		if !ok {
			continue
		}

		augmented = append(augmented, service)
		seen[serviceKey] = struct{}{}
	}

	return augmented, nil
}

func metadataReferencedServiceKeys(metadata []filemetadata.Row) []string {
	serviceKeys := []string{}
	seen := map[string]struct{}{}
	add := func(serviceKey string) {
		serviceKey = strings.TrimSpace(serviceKey)
		if serviceKey == "" {
			return
		}

		if _, ok := seen[serviceKey]; ok {
			return
		}

		seen[serviceKey] = struct{}{}
		serviceKeys = append(serviceKeys, serviceKey)
	}

	for _, row := range metadata {
		collectServiceKeysFromAnyMap(row["ratings"], add)
		collectServiceKeysFromTags(row["tags"], add)
		collectServiceKeysFromLegacyTags(
			row["service_keys_to_statuses_to_tags"],
			add,
		)
		collectServiceKeysFromLegacyTags(
			row["service_keys_to_statuses_to_display_tags"],
			add,
		)
		collectServiceKeysFromStringMap(row["ipfs_multihashes"], add)
		collectServiceKeysFromFileServices(row["file_services"], add)
	}

	slices.Sort(serviceKeys)

	return serviceKeys
}

func collectServiceKeysFromAnyMap(
	value any,
	add func(string),
) {
	entries, ok := value.(map[string]any)
	if !ok {
		return
	}

	for serviceKey := range entries {
		add(serviceKey)
	}
}

func collectServiceKeysFromTags(
	value any,
	add func(string),
) {
	entries, ok := value.(map[string]map[string]any)
	if !ok {
		return
	}

	for serviceKey := range entries {
		add(serviceKey)
	}
}

func collectServiceKeysFromLegacyTags(
	value any,
	add func(string),
) {
	entries, ok := value.(map[string]map[string][]string)
	if !ok {
		return
	}

	for serviceKey := range entries {
		add(serviceKey)
	}
}

func collectServiceKeysFromStringMap(
	value any,
	add func(string),
) {
	entries, ok := value.(map[string]string)
	if !ok {
		return
	}

	for serviceKey := range entries {
		add(serviceKey)
	}
}

func collectServiceKeysFromFileServices(
	value any,
	add func(string),
) {
	sections, ok := value.(map[string]any)
	if !ok {
		return
	}

	for _, sectionName := range []string{"current", "deleted"} {
		entries, ok := sections[sectionName].(map[string]map[string]any)
		if !ok {
			continue
		}

		for serviceKey := range entries {
			add(serviceKey)
		}
	}
}

func parseHashes(query map[string][]string) ([]string, error) {
	hashes := []string{}

	if rawHash := strings.TrimSpace(firstQueryValue(query, "hash")); rawHash != "" {
		normalized, err := normalizeHash(rawHash)
		if err != nil {
			return nil, err
		}

		hashes = append(hashes, normalized)
	}

	rawHashes := strings.TrimSpace(firstQueryValue(query, "hashes"))
	if rawHashes == "" {
		return hashes, nil
	}

	var encoded []string
	if err := json.Unmarshal([]byte(rawHashes), &encoded); err != nil {
		return nil, fmt.Errorf("decode hashes: %w", err)
	}

	for _, hash := range encoded {
		normalized, err := normalizeHash(hash)
		if err != nil {
			return nil, err
		}

		hashes = append(hashes, normalized)
	}

	return hashes, nil
}

func parseFileIDs(query map[string][]string) ([]int64, error) {
	fileIDs := []int64{}

	if rawFileID := strings.TrimSpace(firstQueryValue(query, "file_id")); rawFileID != "" {
		fileID, err := parseFileID(rawFileID)
		if err != nil {
			return nil, err
		}

		fileIDs = append(fileIDs, fileID)
	}

	rawFileIDs := strings.TrimSpace(firstQueryValue(query, "file_ids"))
	if rawFileIDs == "" {
		return fileIDs, nil
	}

	var moreFileIDs []int64
	if err := json.Unmarshal([]byte(rawFileIDs), &moreFileIDs); err != nil {
		return nil, fmt.Errorf("decode file_ids: %w", err)
	}

	for _, fileID := range moreFileIDs {
		if err := validateFileID(fileID); err != nil {
			return nil, err
		}

		fileIDs = append(fileIDs, fileID)
	}

	return fileIDs, nil
}

func parseFileID(raw string) (int64, error) {
	fileID, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse file_id: %w", err)
	}

	if err := validateFileID(fileID); err != nil {
		return 0, err
	}

	return fileID, nil
}

func validateFileID(fileID int64) error {
	if fileID < 0 {
		return fmt.Errorf("was asked about a negative hash_id")
	}

	if fileID > maxReasonableFileID {
		return fmt.Errorf("was asked about a hash_id that was way too big")
	}

	return nil
}

func normalizeHash(raw string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	if normalized == "" {
		return "", fmt.Errorf("hash must not be empty")
	}

	decoded, err := hex.DecodeString(normalized)
	if err != nil {
		return "", fmt.Errorf("invalid hash %q: %w", raw, err)
	}

	if len(decoded) != 32 {
		return "", fmt.Errorf(
			"invalid hash %q: expected 32 bytes, got %d",
			raw,
			len(decoded),
		)
	}

	return normalized, nil
}

func queryBool(
	query map[string][]string,
	key string,
	fallback bool,
) (bool, error) {
	raw := strings.TrimSpace(firstQueryValue(query, key))
	if raw == "" {
		return fallback, nil
	}

	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("parse %s: %w", key, err)
	}

	return value, nil
}

func firstQueryValue(query map[string][]string, key string) string {
	values := query[key]
	if len(values) == 0 {
		return ""
	}

	return values[0]
}
