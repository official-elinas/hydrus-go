package httpapi

import (
	"errors"
	"net/http"
	"time"

	coreptrsync "github.com/official-elinas/hydrus-go/internal/core/ptrsync"
)

func (s *Server) handleGetPTRStatus(w http.ResponseWriter, r *http.Request) {
	startedAt := time.Now()
	_, statusCode, err := s.access.Authorize(
		r,
		PermissionSearchAndFetchFiles,
	)
	if err != nil {
		writeError(w, statusCode, err.Error())
		return
	}

	if s.ptrStore == nil {
		s.logger.Warn("PTR status request rejected", "reason", "PTR store unavailable")
		writeError(w, http.StatusNotImplemented, "PTR status is unavailable")
		return
	}

	s.logger.Debug("received PTR status request")

	status, err := s.ptrStore.Status(r.Context())
	if err != nil {
		s.logger.Warn(
			"PTR status request failed",
			"error",
			err,
			"duration_ms",
			time.Since(startedAt).Milliseconds(),
		)
		writeError(w, http.StatusInternalServerError, "could not load PTR status")
		return
	}

	s.logger.Debug(
		"served PTR status request",
		"phase",
		status.Phase,
		"running",
		status.IsRunning,
		"metadata_slice",
		status.MetadataSlice,
		"downloaded_updates",
		status.DownloadedUpdateCount,
		"processed_definitions",
		status.ProcessedDefinitionCount,
		"processed_content",
		status.ProcessedContentCount,
		"duration_ms",
		time.Since(startedAt).Milliseconds(),
	)

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
		s.logger.Warn("manual PTR sync request rejected", "reason", "PTR store unavailable")
		writeError(w, http.StatusNotImplemented, "PTR sync trigger is unavailable")
		return
	}

	s.logger.Info("received manual PTR sync request")

	status, err := s.ptrStore.Trigger(r.Context())
	if err != nil {
		switch {
		case errors.Is(err, coreptrsync.ErrSyncDisabled):
			s.logger.Warn("manual PTR sync request rejected", "reason", "PTR sync disabled")
			writeJSON(w, http.StatusBadRequest, map[string]any{"ptr": status})
			return
		case errors.Is(err, coreptrsync.ErrSyncUnavailable):
			s.logger.Warn("manual PTR sync request rejected", "reason", "PTR sync unavailable")
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ptr": status})
			return
		default:
			s.logger.Warn("manual PTR sync request failed", "error", err)
			writeError(w, http.StatusInternalServerError, "could not trigger PTR sync")
			return
		}
	}

	s.logger.Info(
		"accepted manual PTR sync request",
		"phase",
		status.Phase,
		"running",
		status.IsRunning,
		"metadata_slice",
		status.MetadataSlice,
	)

	writeJSON(w, http.StatusOK, map[string]any{"ptr": status})
}
