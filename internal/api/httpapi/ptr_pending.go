package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/official-elinas/hydrus-go/internal/core/clientapi"
	coreptrsync "github.com/official-elinas/hydrus-go/internal/core/ptrsync"
	"github.com/official-elinas/hydrus-go/internal/core/services"
)

type addTagsRequest struct {
	Hash                       string                         `json:"hash"`
	Hashes                     []string                       `json:"hashes"`
	FileID                     *int64                         `json:"file_id"`
	FileIDs                    []int64                        `json:"file_ids"`
	ServiceKeysToTags          map[string][]string            `json:"service_keys_to_tags"`
	ServiceNamesToTags         map[string][]string            `json:"service_names_to_tags"`
	ServiceKeysToActionsToTags map[string]map[string][]string `json:"service_keys_to_actions_to_tags"`
}

type parsedAddTagsRequest struct {
	Hashes                     []string
	FileIDs                    []int64
	ServiceKeysToTags          map[string][]string
	ServiceNamesToTags         map[string][]string
	ServiceKeysToActionsToTags map[string]map[string][]string
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

	request, err := parseAddTagsRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if len(request.ServiceKeysToTags) > 0 || len(request.ServiceNamesToTags) > 0 {
		if s.clientAPIStore == nil {
			writeError(w, http.StatusNotImplemented, "local tag writes are unavailable until HYDRUS_GO_DB_DIR is configured")
			return
		}

		if err := s.applyCurrentTagWrites(r.Context(), request); err != nil {
			if writeClientAPIStoreError(w, err) {
				return
			}
		}

		w.WriteHeader(http.StatusOK)
		return
	}

	if len(request.ServiceKeysToActionsToTags) == 0 {
		writeError(w, http.StatusBadRequest, "unsupported add_tags payload")
		return
	}

	if allCurrent, err := request.currentActionTagRequests(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	} else if len(allCurrent) > 0 {
		if s.clientAPIStore == nil {
			writeError(w, http.StatusNotImplemented, "local tag writes are unavailable until HYDRUS_GO_DB_DIR is configured")
			return
		}

		for _, currentRequest := range allCurrent {
			if err := s.clientAPIStore.AddTags(r.Context(), currentRequest); err != nil {
				if writeClientAPIStoreError(w, err) {
					return
				}
			}
		}

		w.WriteHeader(http.StatusOK)
		return
	}

	if s.ptrStore == nil {
		writeError(w, http.StatusNotImplemented, "PTR pending tag staging is unavailable")
		return
	}

	ptrRequest, err := request.pendingMappingsRequest()
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	result, err := s.ptrStore.AddPendingMappings(r.Context(), ptrRequest)
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

func (s *Server) applyCurrentTagWrites(ctx context.Context, request parsedAddTagsRequest) error {
	for serviceKey, tags := range request.ServiceKeysToTags {
		if err := s.clientAPIStore.AddTags(ctx, clientapi.TagRequest{
			Hashes:     append([]string(nil), request.Hashes...),
			FileIDs:    append([]int64(nil), request.FileIDs...),
			ServiceKey: strings.ToLower(strings.TrimSpace(serviceKey)),
			Tags:       append([]string(nil), tags...),
		}); err != nil {
			return err
		}
	}

	for serviceName, tags := range request.ServiceNamesToTags {
		service, ok := s.resolveWritableTagServiceByName(ctx, serviceName)
		if !ok {
			return &clientapi.NotFoundError{Message: "service not found"}
		}
		if err := s.clientAPIStore.AddTags(ctx, clientapi.TagRequest{
			Hashes:     append([]string(nil), request.Hashes...),
			FileIDs:    append([]int64(nil), request.FileIDs...),
			ServiceKey: service.ServiceKey,
			Tags:       append([]string(nil), tags...),
		}); err != nil {
			return err
		}
	}

	return nil
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

func parseAddTagsRequest(r *http.Request) (parsedAddTagsRequest, error) {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	var raw addTagsRequest
	if err := decoder.Decode(&raw); err != nil {
		return parsedAddTagsRequest{}, err
	}

	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return parsedAddTagsRequest{}, errors.New("request body must contain a single JSON object")
	}

	request := parsedAddTagsRequest{
		ServiceKeysToTags:          raw.ServiceKeysToTags,
		ServiceNamesToTags:         raw.ServiceNamesToTags,
		ServiceKeysToActionsToTags: raw.ServiceKeysToActionsToTags,
	}
	if trimmedHash := strings.ToLower(strings.TrimSpace(raw.Hash)); trimmedHash != "" {
		request.Hashes = append(request.Hashes, trimmedHash)
	}
	request.Hashes = append(request.Hashes, raw.Hashes...)
	if raw.FileID != nil {
		request.FileIDs = append(request.FileIDs, *raw.FileID)
	}
	request.FileIDs = append(request.FileIDs, raw.FileIDs...)

	return request, nil
}

func (r parsedAddTagsRequest) currentActionTagRequests() ([]clientapi.TagRequest, error) {
	requests := []clientapi.TagRequest{}
	if len(r.ServiceKeysToActionsToTags) == 0 {
		return requests, nil
	}

	for serviceKey, actionsToTags := range r.ServiceKeysToActionsToTags {
		if len(actionsToTags) != 1 {
			return nil, errors.New("only one action per service is supported")
		}

		for action, tags := range actionsToTags {
			actionCode, err := strconv.Atoi(strings.TrimSpace(action))
			if err != nil {
				return nil, err
			}
			if actionCode != 0 {
				return nil, nil
			}

			requests = append(requests, clientapi.TagRequest{
				Hashes:     append([]string(nil), r.Hashes...),
				FileIDs:    append([]int64(nil), r.FileIDs...),
				ServiceKey: strings.ToLower(strings.TrimSpace(serviceKey)),
				Tags:       append([]string(nil), tags...),
			})
		}
	}

	return requests, nil
}

func (r parsedAddTagsRequest) pendingMappingsRequest() (coreptrsync.PendingMappingsRequest, error) {
	request := coreptrsync.PendingMappingsRequest{
		Hashes:  append([]string(nil), r.Hashes...),
		FileIDs: append([]int64(nil), r.FileIDs...),
	}

	if len(r.ServiceKeysToActionsToTags) != 1 {
		return coreptrsync.PendingMappingsRequest{}, errors.New("service_keys_to_actions_to_tags must contain exactly one service")
	}

	for serviceKey, actionsToTags := range r.ServiceKeysToActionsToTags {
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

func (s *Server) resolveWritableTagServiceByName(ctx context.Context, serviceName string) (services.Service, bool) {
	service, ok, err := s.services.ByName(ctx, strings.TrimSpace(serviceName))
	if err != nil || !ok {
		return services.Service{}, false
	}
	if service.Type != services.TypeLocalTag {
		return services.Service{}, false
	}
	return service, true
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
