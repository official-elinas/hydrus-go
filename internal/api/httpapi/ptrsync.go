package httpapi

import "net/http"

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
