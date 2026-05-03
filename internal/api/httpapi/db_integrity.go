package httpapi

import "net/http"

func (s *Server) handlePostDatabaseIntegrityCheck(w http.ResponseWriter, r *http.Request) {
	_, statusCode, err := s.access.Authorize(
		r,
		PermissionManageDatabase,
	)
	if err != nil {
		writeError(w, statusCode, err.Error())
		return
	}

	if s.metadataStore == nil {
		s.logger.Warn("database integrity request rejected", "reason", "metadata store unavailable")
		writeError(w, http.StatusNotImplemented, "database integrity checks are unavailable")
		return
	}

	result, err := s.metadataStore.CheckIntegrity(r.Context())
	if err != nil {
		s.logger.Warn("database integrity request failed", "error", err)
		writeError(w, http.StatusInternalServerError, "could not run database integrity check")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"integrity": result})
}
