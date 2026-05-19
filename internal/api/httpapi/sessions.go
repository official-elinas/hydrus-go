package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/official-elinas/hydrus-go/internal/core/clientsessions"
)

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	_, statusCode, err := s.access.Authorize(r, PermissionManagePages)
	if err != nil {
		writeError(w, statusCode, err.Error())
		return
	}

	sessions, err := s.sessionStore.ListSessions(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list sessions")
		return
	}

	if sessions == nil {
		sessions = []clientsessions.Session{}
	}

	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
}

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	_, statusCode, err := s.access.Authorize(r, PermissionManagePages)
	if err != nil {
		writeError(w, statusCode, err.Error())
		return
	}

	var req clientsessions.CreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %s", err))
		return
	}

	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	session, err := s.sessionStore.CreateSession(r.Context(), req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create session")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{"session": session})
}

func (s *Server) handleUpdateSession(w http.ResponseWriter, r *http.Request) {
	_, statusCode, err := s.access.Authorize(r, PermissionManagePages)
	if err != nil {
		writeError(w, statusCode, err.Error())
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid session id")
		return
	}

	var req clientsessions.UpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %s", err))
		return
	}

	session, err := s.sessionStore.UpdateSession(r.Context(), id, req)
	if err != nil {
		var notFound *clientsessions.NotFoundError
		if errors.As(err, &notFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "could not update session")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"session": session})
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	_, statusCode, err := s.access.Authorize(r, PermissionManagePages)
	if err != nil {
		writeError(w, statusCode, err.Error())
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid session id")
		return
	}

	if err := s.sessionStore.DeleteSession(r.Context(), id); err != nil {
		var notFound *clientsessions.NotFoundError
		if errors.As(err, &notFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "could not delete session")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
