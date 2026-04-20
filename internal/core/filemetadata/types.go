// Package filemetadata defines the first read-only Hydrus file metadata query
// contract used by the HTTP API and DB-backed implementations.
package filemetadata

import "context"

// Request describes a file metadata lookup request.
type Request struct {
	Hashes                       []string
	FileIDs                      []int64
	OnlyReturnIdentifiers        bool
	OnlyReturnBasicInformation   bool
	IncludeServicesObject        bool
	IncludeLegacyServiceKeysTags bool
	IncludeBlurhash              bool
	IncludeMilliseconds          bool
	DetailedURLInformation       bool
	IncludeNotes                 bool
	CreateNewFileIDs             bool
}

// Row is a single file metadata response row.
type Row map[string]any

// Store loads file metadata rows.
type Store interface {
	GetMetadata(context.Context, Request) ([]Row, error)
}

// MissingHashRow builds the Hydrus-compatible row shape for unknown hashes.
func MissingHashRow(hash string) Row {
	return Row{
		"file_id": nil,
		"hash":    hash,
	}
}

// NotFoundError reports that one or more file identifiers could not be found.
type NotFoundError struct {
	Message string
}

func (e *NotFoundError) Error() string {
	return e.Message
}

// UnsupportedError reports a request mode that this migration slice does not
// implement yet.
type UnsupportedError struct {
	Message string
}

func (e *UnsupportedError) Error() string {
	return e.Message
}
