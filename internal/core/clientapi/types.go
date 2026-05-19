// Package clientapi defines the narrow write-side Hydrus Client API
// compatibility contract used by downloader-facing integrations.
package clientapi

import "context"

const (
	TimestampTypeModifiedDomain = 0
	TimestampTypeModifiedFile   = 1
)

// URLAssociationRequest attaches known source URLs to existing files.
type URLAssociationRequest struct {
	Hashes    []string
	FileIDs   []int64
	URLsToAdd []string
}

// TagRequest records current local-tag mappings for existing files.
type TagRequest struct {
	Hashes     []string
	FileIDs    []int64
	ServiceKey string
	Tags       []string
}

// NotesRequest sets named notes on one existing file.
type NotesRequest struct {
	Hash   string
	FileID *int64
	Notes  map[string]string
}

// TimeRequest sets one supported timestamp type on existing files.
type TimeRequest struct {
	Hashes        []string
	FileIDs       []int64
	TimestampType int
	TimestampMS   int64
	Domain        string
}

// Store applies the write-side Hydrus Client API compatibility mutations that
// external downloaders such as hydownloader need after importing files.
type Store interface {
	AssociateURLs(context.Context, URLAssociationRequest) error
	AddTags(context.Context, TagRequest) error
	SetNotes(context.Context, NotesRequest) (map[string]string, error)
	SetTime(context.Context, TimeRequest) error
}

// RequestError reports an invalid compatibility mutation request.
type RequestError struct {
	Message string
}

func (e *RequestError) Error() string {
	return e.Message
}

// NotFoundError reports a missing file or service target.
type NotFoundError struct {
	Message string
}

func (e *NotFoundError) Error() string {
	return e.Message
}

// UnsupportedError reports a supported route shape that still maps to an
// unimplemented Hydrus behavior in this migration slice.
type UnsupportedError struct {
	Message string
}

func (e *UnsupportedError) Error() string {
	return e.Message
}
