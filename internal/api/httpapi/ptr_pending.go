package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	coreptrsync "github.com/official-elinas/hydrus-go/internal/core/ptrsync"
)

type addTagsRequest struct {
	Hash                       string                       `json:"hash"`
	Hashes                     []string                     `json:"hashes"`
	FileID                     *int64                       `json:"file_id"`
	FileIDs                    []int64                      `json:"file_ids"`
	ServiceKeysToActionsToTags map[string]map[string][]string `json:"service_keys_to_actions_to_tags"`
}

type commitPendingRequest struct {
	ServiceKey string `json:"service_key"`
}

func (s *Server) handlePostAddTags(w http.ResponseWriter, r *http.Request) {
	_, statusCode, err := s.access.Authorize(r, PermissionEditFileTags)
	if err != nil {
		writeError(w, statusCode, err.Error())
		return
	}

	if s.ptrStore == nil {
		writeError(w, http.StatusNotImplemented, "PTR pending tag staging is unavailable")
		return
	}

	request, err := parseAddTagsRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	result, err := s.ptrStore.AddPendingMappings(r.Context(), request)
	if err != nil {
		switch {
		case errors.Is(err, coreptrsync.ErrSyncDisabled), errors.Is(err, coreptrsync.ErrCommitPendingUnavailable):
			writeError(w, http.StatusServiceUnavailable, err.Error())
		default:
			writeError(w, http.StatusBadRequest, err.Error())
		}
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"service_key":    result.ServiceKey,
		"added_mappings": result.AddedMappings,
	})
}

func (s *Server) handlePostCommitPending(w http.ResponseWriter, r *http.Request) {
	_, statusCode, err := s.access.Authorize(r, PermissionCommitPending)
	if err != nil {
		writeError(w, statusCode, err.Error())
		return
	}

	if s.ptrStore == nil {
		writeError(w, http.StatusNotImplemented, "PTR pending commit is unavailable")
		return
	}

	request, err := parseCommitPendingRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	result, err := s.ptrStore.CommitPending(r.Context(), request)
	if err != nil {
		switch {
		case errors.Is(err, coreptrsync.ErrSyncDisabled), errors.Is(err, coreptrsync.ErrCommitPendingUnavailable):
			writeError(w, http.StatusServiceUnavailable, err.Error())
		default:
			writeError(w, http.StatusBadRequest, err.Error())
		}
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"service_key":        result.ServiceKey,
		"committed_mappings": result.CommittedMappings,
	})
}

func parseAddTagsRequest(r *http.Request) (coreptrsync.PendingMappingsRequest, error) {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	var raw addTagsRequest
	if err := decoder.Decode(&raw); err != nil {
		return coreptrsync.PendingMappingsRequest{}, err
	}

	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return coreptrsync.PendingMappingsRequest{}, errors.New("request body must contain a single JSON object")
	}

	request := coreptrsync.PendingMappingsRequest{}
	if trimmedHash := strings.ToLower(strings.TrimSpace(raw.Hash)); trimmedHash != "" {
		request.Hashes = append(request.Hashes, trimmedHash)
	}
	request.Hashes = append(request.Hashes, raw.Hashes...)
	if raw.FileID != nil {
		request.FileIDs = append(request.FileIDs, *raw.FileID)
	}
	request.FileIDs = append(request.FileIDs, raw.FileIDs...)

	if len(raw.ServiceKeysToActionsToTags) != 1 {
		return coreptrsync.PendingMappingsRequest{}, errors.New("service_keys_to_actions_to_tags must contain exactly one service")
	}

	for serviceKey, actionsToTags := range raw.ServiceKeysToActionsToTags {
		request.ServiceKey = strings.ToLower(strings.TrimSpace(serviceKey))
		for action, tags := range actionsToTags {
			actionCode, err := strconv.Atoi(strings.TrimSpace(action))
			if err != nil {
				return coreptrsync.PendingMappingsRequest{}, err
			}
			if actionCode != 2 {
				return coreptrsync.PendingMappingsRequest{}, errors.New("only CONTENT_UPDATE_PEND (2) is supported")
			}

			request.Tags = append(request.Tags, tags...)
		}
	}

	return request, nil
}

func (s *Server) handleGetPendingCounts(w http.ResponseWriter, r *http.Request) {
	_, statusCode, err := s.access.Authorize(r, PermissionEditFileTags)
	if err != nil {
		writeError(w, statusCode, err.Error())
		return
	}

	if s.ptrStore == nil {
		writeError(w, http.StatusNotImplemented, "PTR pending counts are unavailable")
		return
	}

	serviceKey := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("service_key")))

	info, err := s.ptrStore.PendingMappingCount(r.Context(), coreptrsync.PendingCountRequest{ServiceKey: serviceKey})
	if err != nil {
		switch {
		case errors.Is(err, coreptrsync.ErrSyncDisabled), errors.Is(err, coreptrsync.ErrCommitPendingUnavailable):
			writeError(w, http.StatusServiceUnavailable, err.Error())
		case errors.Is(err, coreptrsync.ErrPTRServiceNotFound):
			writeError(w, http.StatusNotFound, err.Error())
		default:
			writeError(w, http.StatusBadRequest, err.Error())
		}
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"service_key":   info.ServiceKey,
		"pending_count": info.PendingCount,
	})
}

func parseCommitPendingRequest(r *http.Request) (coreptrsync.CommitPendingRequest, error) {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	var request commitPendingRequest
	if err := decoder.Decode(&request); err != nil {
		if err == io.EOF {
			return coreptrsync.CommitPendingRequest{}, nil
		}
		return coreptrsync.CommitPendingRequest{}, err
	}

	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return coreptrsync.CommitPendingRequest{}, errors.New("request body must contain a single JSON object")
	}

	return coreptrsync.CommitPendingRequest{ServiceKey: strings.ToLower(strings.TrimSpace(request.ServiceKey))}, nil
}
