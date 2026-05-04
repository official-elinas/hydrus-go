package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strings"

	"github.com/official-elinas/hydrus-go/internal/core/clientapi"
)

type hydrusAssociateURLRequest struct {
	Hash         string   `json:"hash"`
	Hashes       []string `json:"hashes"`
	FileID       *int64   `json:"file_id"`
	FileIDs      []int64  `json:"file_ids"`
	URLToAdd     string   `json:"url_to_add"`
	URLsToAdd    []string `json:"urls_to_add"`
	URLToDelete  string   `json:"url_to_delete"`
	URLsToDelete []string `json:"urls_to_delete"`
}

type hydrusSetNotesRequest struct {
	Hash   string            `json:"hash"`
	FileID *int64            `json:"file_id"`
	Notes  map[string]string `json:"notes"`
}

type hydrusSetTimeRequest struct {
	Hash          string   `json:"hash"`
	Hashes        []string `json:"hashes"`
	FileID        *int64   `json:"file_id"`
	FileIDs       []int64  `json:"file_ids"`
	TimestampType int      `json:"timestamp_type"`
	Timestamp     *float64 `json:"timestamp"`
	TimestampMS   *int64   `json:"timestamp_ms"`
	Domain        string   `json:"domain"`
}

func (s *Server) handleHydrusAssociateURL(w http.ResponseWriter, r *http.Request) {
	_, statusCode, err := s.access.Authorize(
		r,
		PermissionImportAndEditURLs,
		PermissionImportAndDeleteFiles,
	)
	if err != nil {
		writeError(w, statusCode, err.Error())
		return
	}

	if s.clientAPIStore == nil {
		writeError(w, http.StatusNotImplemented, "URL association is unavailable until HYDRUS_GO_DB_DIR is configured")
		return
	}

	request, err := parseHydrusAssociateURLRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := s.clientAPIStore.AssociateURLs(r.Context(), request); err != nil {
		if writeClientAPIStoreError(w, err) {
			return
		}
	}

	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleHydrusSetNotes(w http.ResponseWriter, r *http.Request) {
	_, statusCode, err := s.access.Authorize(
		r,
		PermissionEditFileNotes,
	)
	if err != nil {
		writeError(w, statusCode, err.Error())
		return
	}

	if s.clientAPIStore == nil {
		writeError(w, http.StatusNotImplemented, "note edits are unavailable until HYDRUS_GO_DB_DIR is configured")
		return
	}

	request, err := parseHydrusSetNotesRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	notes, err := s.clientAPIStore.SetNotes(r.Context(), request)
	if err != nil {
		if writeClientAPIStoreError(w, err) {
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"notes": notes})
}

func (s *Server) handleHydrusSetTime(w http.ResponseWriter, r *http.Request) {
	_, statusCode, err := s.access.Authorize(
		r,
		PermissionEditFileTimes,
	)
	if err != nil {
		writeError(w, statusCode, err.Error())
		return
	}

	if s.clientAPIStore == nil {
		writeError(w, http.StatusNotImplemented, "time edits are unavailable until HYDRUS_GO_DB_DIR is configured")
		return
	}

	request, err := parseHydrusSetTimeRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := s.clientAPIStore.SetTime(r.Context(), request); err != nil {
		if writeClientAPIStoreError(w, err) {
			return
		}
	}

	w.WriteHeader(http.StatusOK)
}

func parseHydrusAssociateURLRequest(r *http.Request) (clientapi.URLAssociationRequest, error) {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	var raw hydrusAssociateURLRequest
	if err := decoder.Decode(&raw); err != nil {
		return clientapi.URLAssociationRequest{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return clientapi.URLAssociationRequest{}, errors.New("request body must contain a single JSON object")
	}

	if strings.TrimSpace(raw.URLToDelete) != "" || len(raw.URLsToDelete) > 0 {
		return clientapi.URLAssociationRequest{}, errors.New("URL deletion is not supported yet")
	}

	request := clientapi.URLAssociationRequest{
		Hashes:  appendSingularHash(nil, raw.Hash, raw.Hashes),
		FileIDs: appendSingularFileID(nil, raw.FileID, raw.FileIDs),
	}
	if trimmed := strings.TrimSpace(raw.URLToAdd); trimmed != "" {
		request.URLsToAdd = append(request.URLsToAdd, trimmed)
	}
	request.URLsToAdd = append(request.URLsToAdd, raw.URLsToAdd...)

	return request, nil
}

func parseHydrusSetNotesRequest(r *http.Request) (clientapi.NotesRequest, error) {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	var raw hydrusSetNotesRequest
	if err := decoder.Decode(&raw); err != nil {
		return clientapi.NotesRequest{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return clientapi.NotesRequest{}, errors.New("request body must contain a single JSON object")
	}

	return clientapi.NotesRequest{
		Hash:   strings.TrimSpace(raw.Hash),
		FileID: raw.FileID,
		Notes:  raw.Notes,
	}, nil
}

func parseHydrusSetTimeRequest(r *http.Request) (clientapi.TimeRequest, error) {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	var raw hydrusSetTimeRequest
	if err := decoder.Decode(&raw); err != nil {
		return clientapi.TimeRequest{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return clientapi.TimeRequest{}, errors.New("request body must contain a single JSON object")
	}

	hasSeconds := raw.Timestamp != nil
	hasMilliseconds := raw.TimestampMS != nil
	if hasSeconds == hasMilliseconds {
		return clientapi.TimeRequest{}, errors.New("provide exactly one of timestamp or timestamp_ms")
	}

	var timestampMS int64
	if hasMilliseconds {
		timestampMS = *raw.TimestampMS
	} else {
		timestampMS = int64(math.Round(*raw.Timestamp * 1000.0))
	}

	return clientapi.TimeRequest{
		Hashes:        appendSingularHash(nil, raw.Hash, raw.Hashes),
		FileIDs:       appendSingularFileID(nil, raw.FileID, raw.FileIDs),
		TimestampType: raw.TimestampType,
		TimestampMS:   timestampMS,
		Domain:        strings.TrimSpace(raw.Domain),
	}, nil
}

func appendSingularHash(base []string, hash string, hashes []string) []string {
	if trimmed := strings.TrimSpace(hash); trimmed != "" {
		base = append(base, trimmed)
	}

	return append(base, hashes...)
}

func appendSingularFileID(base []int64, fileID *int64, fileIDs []int64) []int64 {
	if fileID != nil {
		base = append(base, *fileID)
	}

	return append(base, fileIDs...)
}

func writeClientAPIStoreError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}

	var requestError *clientapi.RequestError
	var notFoundError *clientapi.NotFoundError
	var unsupportedError *clientapi.UnsupportedError
	switch {
	case errors.As(err, &requestError):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.As(err, &notFoundError):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.As(err, &unsupportedError):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "could not apply client API mutation")
	}

	return true
}
