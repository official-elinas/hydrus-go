package httpapi

import (
	"net/http"
	"strconv"
	"strings"
)

const defaultTagAutocompleteLimit = 20

func (s *Server) handleGetTagAutocomplete(w http.ResponseWriter, r *http.Request) {
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
			"tag autocomplete is unavailable until HYDRUS_GO_DB_DIR is configured",
		)
		return
	}

	prefix := strings.TrimSpace(r.URL.Query().Get("q"))
	if prefix == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"query":       "",
			"suggestions": []string{},
		})
		return
	}

	limit := defaultTagAutocompleteLimit
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
		parsedLimit, err := strconv.Atoi(rawLimit)
		if err != nil {
			writeError(w, http.StatusBadRequest, "parse limit: "+err.Error())
			return
		}

		if parsedLimit <= 0 {
			writeError(w, http.StatusBadRequest, "limit must be greater than zero")
			return
		}

		limit = parsedLimit
	}

	suggestions, err := s.metadataStore.SuggestTags(r.Context(), prefix, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load tag suggestions")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"query":       prefix,
		"suggestions": suggestions,
	})
}
