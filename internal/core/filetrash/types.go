// Package filetrash defines the thin-client trash contract.
package filetrash

import "context"

// Request describes one thin-client trash request.
type Request struct {
	FileID int64 `json:"file_id"`
}

// Result is the daemon response for one trashed file.
type Result struct {
	FileID            int64 `json:"file_id"`
	Trashed           bool  `json:"trashed"`
	RemovedFromRecent bool  `json:"removed_from_recent"`
}

// Store trashes files in the daemon-managed library.
type Store interface {
	TrashFile(context.Context, Request) (Result, error)
}

// RequestError reports an invalid trash request.
type RequestError struct {
	Message string
}

func (e *RequestError) Error() string {
	return e.Message
}

// NotFoundError reports that a requested file could not be found.
type NotFoundError struct {
	Message string
}

func (e *NotFoundError) Error() string {
	return e.Message
}
