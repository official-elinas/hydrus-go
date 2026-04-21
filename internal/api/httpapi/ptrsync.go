package httpapi

import (
	"errors"
	"net/http"

	coreptrsync "github.com/official-elinas/hydrus-go/internal/core/ptrsync"
)

func (s *Server) handleGetPTRStatus(w http.ResponseWriter, r *http.Request) {
	_, statusCode, err := s.access.Authorize(
		r,
		PermissionSearchAndFetchFiles,
	)
	if err != nil {
		writeError(w, statusCode, err.Error())
		return
	}

	if s.ptrStore == nil {
		writeError(w, http.StatusNotImplemented, "PTR status is unavailable")
		return
	}

	status, err := s.ptrStore.Status(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load PTR status")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ptr": status})
}

func (s *Server) handlePostPTRSync(w http.ResponseWriter, r *http.Request) {
	_, statusCode, err := s.access.Authorize(
		r,
		PermissionImportAndDeleteFiles,
	)
	if err != nil {
		writeError(w, statusCode, err.Error())
		return
	}

	if s.ptrStore == nil {
		writeError(w, http.StatusNotImplemented, "PTR sync trigger is unavailable")
		return
	}

	status, err := s.ptrStore.Trigger(r.Context())
	if err != nil {
		switch {
		case errors.Is(err, coreptrsync.ErrSyncDisabled):
			writeJSON(w, http.StatusBadRequest, map[string]any{"ptr": status})
			return
		case errors.Is(err, coreptrsync.ErrSyncUnavailable):
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ptr": status})
			return
		default:
			writeError(w, http.StatusInternalServerError, "could not trigger PTR sync")
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"ptr": status})
}
