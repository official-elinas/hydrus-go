package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/official-elinas/hydrus-go/internal/core/fileimport"
)

const (
	uploadFormFileField             = "file"
	uploadFormLocalServiceKeyField  = "local_file_service_key"
	uploadFormFileModifiedAtMSField = "file_modified_at_ms"
	uploadFormFieldLimitBytes       = 8 << 10
)

var uploadRequestBodyLimitBytes int64 = 4 << 30

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
	if writeImportStoreError(w, err) {
		return
	}

	writeImportResponse(w, result)
	return
}

func (s *Server) handleImportUpload(w http.ResponseWriter, r *http.Request) {
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
			"file upload is unavailable until HYDRUS_GO_DB_DIR is configured",
		)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, uploadRequestBodyLimitBytes)

	request, cleanup, err := parseUploadImportRequest(r)
	if err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeError(
				w,
				http.StatusRequestEntityTooLarge,
				fmt.Sprintf("upload exceeds %d bytes", uploadRequestBodyLimitBytes),
			)
			return
		}

		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	defer cleanup()

	result, err := s.importStore.ImportUpload(r.Context(), request)
	if writeImportStoreError(w, err) {
		return
	}

	writeImportResponse(w, result)
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

func parseUploadImportRequest(r *http.Request) (fileimport.UploadRequest, func(), error) {
	multipartReader, err := r.MultipartReader()
	if err != nil {
		return fileimport.UploadRequest{}, func() {}, errors.New(
			"request must be multipart/form-data",
		)
	}

	request := fileimport.UploadRequest{}
	cleanup := func() {}
	fileSeen := false
	serviceKeySeen := false
	modifiedAtSeen := false

	for {
		part, err := multipartReader.NextPart()
		if err == io.EOF {
			break
		}

		if err != nil {
			cleanup()
			return fileimport.UploadRequest{}, func() {}, fmt.Errorf(
				"read multipart upload body: %w",
				err,
			)
		}

		fieldName := strings.TrimSpace(part.FormName())
		filename := part.FileName()
		if filename != "" {
			if fieldName != uploadFormFileField {
				_ = part.Close()
				cleanup()
				return fileimport.UploadRequest{}, func() {}, fmt.Errorf(
					"unexpected upload file field %q",
					fieldName,
				)
			}

			if fileSeen {
				_ = part.Close()
				cleanup()
				return fileimport.UploadRequest{}, func() {}, errors.New(
					"request must contain exactly one file field named \"file\"",
				)
			}

			stagedPath, stagedCleanup, err := stageUploadPart(r.Context(), part)
			_ = part.Close()
			if err != nil {
				stagedCleanup()
				cleanup()
				return fileimport.UploadRequest{}, func() {}, err
			}

			cleanup = chainCleanup(cleanup, stagedCleanup)
			request.StagedPath = stagedPath
			request.Filename = filename
			fileSeen = true
			continue
		}

		value, err := readMultipartField(part, uploadFormFieldLimitBytes)
		_ = part.Close()
		if err != nil {
			cleanup()
			return fileimport.UploadRequest{}, func() {}, err
		}

		switch fieldName {
		case uploadFormLocalServiceKeyField:
			if serviceKeySeen {
				cleanup()
				return fileimport.UploadRequest{}, func() {}, errors.New(
					"local_file_service_key may only be provided once",
				)
			}

			request.LocalFileServiceKey = strings.TrimSpace(value)
			serviceKeySeen = true
		case uploadFormFileModifiedAtMSField:
			if modifiedAtSeen {
				cleanup()
				return fileimport.UploadRequest{}, func() {}, errors.New(
					"file_modified_at_ms may only be provided once",
				)
			}

			trimmed := strings.TrimSpace(value)
			if trimmed == "" {
				cleanup()
				return fileimport.UploadRequest{}, func() {}, errors.New(
					"file_modified_at_ms must not be empty",
				)
			}

			parsed, parseErr := strconv.ParseInt(trimmed, 10, 64)
			if parseErr != nil {
				cleanup()
				return fileimport.UploadRequest{}, func() {}, fmt.Errorf(
					"parse file_modified_at_ms: %w",
					parseErr,
				)
			}

			request.FileModifiedAtMS = &parsed
			modifiedAtSeen = true
		case "":
			cleanup()
			return fileimport.UploadRequest{}, func() {}, errors.New(
				"multipart fields must be named",
			)
		default:
			cleanup()
			return fileimport.UploadRequest{}, func() {}, fmt.Errorf(
				"unexpected upload form field %q",
				fieldName,
			)
		}
	}

	if !fileSeen {
		cleanup()
		return fileimport.UploadRequest{}, func() {}, errors.New(
			"request must contain one file field named \"file\"",
		)
	}

	return request, cleanup, nil
}

func stageUploadPart(
	ctx context.Context,
	part *multipart.Part,
) (string, func(), error) {
	tempFile, err := os.CreateTemp("", "hydrus-go-upload-*")
	if err != nil {
		return "", func() {}, fmt.Errorf("create staged upload file: %w", err)
	}

	tempPath := tempFile.Name()
	cleanup := func() {
		_ = os.Remove(tempPath)
	}

	if err := copyMultipartPart(ctx, tempFile, part); err != nil {
		_ = tempFile.Close()
		cleanup()
		return "", func() {}, fmt.Errorf("stage uploaded file: %w", err)
	}

	if err := tempFile.Sync(); err != nil {
		_ = tempFile.Close()
		cleanup()
		return "", func() {}, fmt.Errorf("sync staged upload file: %w", err)
	}

	if err := tempFile.Close(); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("close staged upload file: %w", err)
	}

	return tempPath, cleanup, nil
}

func copyMultipartPart(
	ctx context.Context,
	dst io.Writer,
	src io.Reader,
) error {
	buffer := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		n, readErr := src.Read(buffer)
		if n > 0 {
			if _, err := dst.Write(buffer[:n]); err != nil {
				return err
			}
		}

		if readErr == io.EOF {
			return nil
		}

		if readErr != nil {
			return readErr
		}
	}
}

func readMultipartField(part *multipart.Part, maxBytes int64) (string, error) {
	payload, err := io.ReadAll(io.LimitReader(part, maxBytes+1))
	if err != nil {
		return "", fmt.Errorf("read multipart field %q: %w", part.FormName(), err)
	}

	if int64(len(payload)) > maxBytes {
		return "", fmt.Errorf("multipart field %q exceeds %d bytes", part.FormName(), maxBytes)
	}

	return string(payload), nil
}

func chainCleanup(first func(), second func()) func() {
	return func() {
		second()
		first()
	}
}

func writeImportStoreError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}

	var requestError *fileimport.RequestError
	var notFoundError *fileimport.NotFoundError
	switch {
	case errors.As(err, &requestError):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.As(err, &notFoundError):
		writeError(w, http.StatusNotFound, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "could not import file")
	}

	return true
}

func writeImportResponse(w http.ResponseWriter, result fileimport.Result) {
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
