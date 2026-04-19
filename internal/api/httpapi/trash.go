package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/official-elinas/hydrus-go/internal/core/filetrash"
)

func (s *Server) handleTrashFile(w http.ResponseWriter, r *http.Request) {
	_, statusCode, err := s.access.Authorize(
		r,
		PermissionImportAndDeleteFiles,
	)
	if err != nil {
		writeError(w, statusCode, err.Error())
		return
	}

	if s.trashStore == nil {
		writeError(
			w,
			http.StatusNotImplemented,
			"file trash is unavailable until HYDRUS_GO_DB_DIR is configured",
		)
		return
	}

	request, err := parseTrashFileRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	result, err := s.trashStore.TrashFile(r.Context(), request)
	if err != nil {
		var requestError *filetrash.RequestError
		var notFoundError *filetrash.NotFoundError

		switch {
		case errors.As(err, &requestError):
			writeError(w, http.StatusBadRequest, err.Error())
			return
		case errors.As(err, &notFoundError):
			writeError(w, http.StatusNotFound, err.Error())
			return
		default:
			writeError(w, http.StatusInternalServerError, "could not trash file")
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"file_id":             result.FileID,
		"trashed":             result.Trashed,
		"removed_from_recent": result.RemovedFromRecent,
		"state":               "trashed",
	})
}

func parseTrashFileRequest(r *http.Request) (filetrash.Request, error) {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	var request filetrash.Request
	if err := decoder.Decode(&request); err != nil {
		return filetrash.Request{}, err
	}

	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return filetrash.Request{}, errors.New("request body must contain a single JSON object")
	}

	return request, nil
}
