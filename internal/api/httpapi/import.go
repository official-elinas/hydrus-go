package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/official-elinas/hydrus-go/internal/core/fileimport"
)

func (s *Server) handleImportLocalFile(w http.ResponseWriter, r *http.Request) {
	_, statusCode, err := s.access.Authorize(
		r,
		PermissionImportAndDeleteFiles,
	)
	if err != nil {
		writeError(w, statusCode, err.Error())
		return
	}

	if s.importStore == nil {
		writeError(
			w,
			http.StatusNotImplemented,
			"local file import is unavailable until HYDRUS_GO_DB_DIR is configured",
		)
		return
	}

	request, err := parseLocalFileImportRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	result, err := s.importStore.ImportLocalPath(r.Context(), request)
	if err != nil {
		var requestError *fileimport.RequestError
		var notFoundError *fileimport.NotFoundError
		switch {
		case errors.As(err, &requestError):
			writeError(w, http.StatusBadRequest, err.Error())
			return
		case errors.As(err, &notFoundError):
			writeError(w, http.StatusNotFound, err.Error())
			return
		default:
			writeError(w, http.StatusInternalServerError, "could not import local file")
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"file_id":                      result.FileID,
		"hash":                         result.Hash,
		"already_imported":             result.AlreadyImported,
		"managed_file_already_present": result.ManagedFileAlreadyPresent,
		"content_url":                  "/v1/files/content?file_id=" + strconv.FormatInt(result.FileID, 10),
		"thumbnail_url":                "/v1/files/thumbnail?file_id=" + strconv.FormatInt(result.FileID, 10),
		"metadata_url":                 "/get_files/file_metadata?file_id=" + strconv.FormatInt(result.FileID, 10),
	})
}

func parseLocalFileImportRequest(r *http.Request) (fileimport.Request, error) {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	var request fileimport.Request
	if err := decoder.Decode(&request); err != nil {
		return fileimport.Request{}, err
	}

	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fileimport.Request{}, errors.New("request body must contain a single JSON object")
	}

	return request, nil
}
