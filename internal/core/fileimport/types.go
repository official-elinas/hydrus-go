// Package fileimport defines the thin-client local file import contract.
package fileimport

import "context"

// Request describes one thin-client local-path import request.
type Request struct {
	Path                string `json:"path"`
	LocalFileServiceKey string `json:"local_file_service_key,omitempty"`
}

// Result is the daemon response for one imported local file.
type Result struct {
	FileID                    int64  `json:"file_id"`
	Hash                      string `json:"hash"`
	AlreadyImported           bool   `json:"already_imported"`
	ManagedFileAlreadyPresent bool   `json:"managed_file_already_present"`
}

// Store imports local files into the daemon-managed library.
type Store interface {
	ImportLocalPath(context.Context, Request) (Result, error)
}

// RequestError reports an invalid import request.
type RequestError struct {
	Message string
}

func (e *RequestError) Error() string {
	return e.Message
}

// NotFoundError reports that a requested local file path could not be found.
type NotFoundError struct {
	Message string
}

func (e *NotFoundError) Error() string {
	return e.Message
}
