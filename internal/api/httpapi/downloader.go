package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	coredownloader "github.com/official-elinas/hydrus-go/internal/core/downloader"
)

func (s *Server) handleGetDownloaderStatus(w http.ResponseWriter, r *http.Request) {
	_, statusCode, err := s.access.Authorize(r, PermissionImportAndEditURLs)
	if err != nil {
		writeError(w, statusCode, err.Error())
		return
	}
	if s.downloaderStore == nil {
		writeError(w, http.StatusNotImplemented, "hydownloader integration is disabled")
		return
	}

	status, err := s.downloaderStore.Status(r.Context())
	if err != nil {
		if writeDownloaderStoreError(w, err) {
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"downloader": status})
}

func (s *Server) handleGetDownloaderDownloaders(w http.ResponseWriter, r *http.Request) {
	_, statusCode, err := s.access.Authorize(r, PermissionImportAndEditURLs)
	if err != nil {
		writeError(w, statusCode, err.Error())
		return
	}
	if s.downloaderStore == nil {
		writeError(w, http.StatusNotImplemented, "hydownloader integration is disabled")
		return
	}

	downloaders, err := s.downloaderStore.Downloaders(r.Context())
	if err != nil {
		if writeDownloaderStoreError(w, err) {
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"downloaders": downloaders})
}

func (s *Server) handlePostDownloaderURL(w http.ResponseWriter, r *http.Request) {
	_, statusCode, err := s.access.Authorize(r, PermissionImportAndEditURLs)
	if err != nil {
		writeError(w, statusCode, err.Error())
		return
	}
	if s.downloaderStore == nil {
		writeError(w, http.StatusNotImplemented, "hydownloader integration is disabled")
		return
	}

	request, err := parseDownloaderURLRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.downloaderStore.QueueURL(r.Context(), request); err != nil {
		if writeDownloaderStoreError(w, err) {
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"queued": true})
}

func (s *Server) handlePostDownloaderSubscription(w http.ResponseWriter, r *http.Request) {
	_, statusCode, err := s.access.Authorize(r, PermissionImportAndEditURLs)
	if err != nil {
		writeError(w, statusCode, err.Error())
		return
	}
	if s.downloaderStore == nil {
		writeError(w, http.StatusNotImplemented, "hydownloader integration is disabled")
		return
	}

	request, err := parseDownloaderSubscriptionRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.downloaderStore.QueueSubscription(r.Context(), request); err != nil {
		if writeDownloaderStoreError(w, err) {
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"queued": true})
}

func parseDownloaderURLRequest(r *http.Request) (coredownloader.URLRequest, error) {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	var request coredownloader.URLRequest
	if err := decoder.Decode(&request); err != nil {
		return coredownloader.URLRequest{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return coredownloader.URLRequest{}, errors.New("request body must contain a single JSON object")
	}

	return request, nil
}

func parseDownloaderSubscriptionRequest(r *http.Request) (coredownloader.SubscriptionRequest, error) {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	var request coredownloader.SubscriptionRequest
	if err := decoder.Decode(&request); err != nil {
		return coredownloader.SubscriptionRequest{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return coredownloader.SubscriptionRequest{}, errors.New("request body must contain a single JSON object")
	}

	return request, nil
}

func writeDownloaderStoreError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}

	var requestError *coredownloader.RequestError
	var notConfiguredError *coredownloader.NotConfiguredError
	switch {
	case errors.As(err, &requestError):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.As(err, &notConfiguredError):
		writeError(w, http.StatusNotImplemented, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "could not manage hydownloader")
	}

	return true
}
