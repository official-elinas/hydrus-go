// Package librarybrowse defines the thin-client browse contract for recent
// local files.
package librarybrowse

import "context"

// Request describes a recent-local-files browse request.
type Request struct {
	Offset int
	Limit  int
}

// Item is a single recent local file returned to a thin client browse view.
type Item struct {
	FileID       int64  `json:"file_id"`
	Hash         string `json:"hash"`
	MIME         string `json:"mime"`
	Width        *int64 `json:"width,omitempty"`
	Height       *int64 `json:"height,omitempty"`
	ImportedAtMS *int64 `json:"imported_at_ms,omitempty"`
	HasThumbnail bool   `json:"has_thumbnail"`
}

// Page is a single browse page.
type Page struct {
	Items   []Item
	HasMore bool
}

// Store loads recent-local-file browse pages.
type Store interface {
	ListRecent(context.Context, Request) (Page, error)
}

// UnsupportedError reports a browse mode that the current slice does not yet
// implement.
type UnsupportedError struct {
	Message string
}

func (e *UnsupportedError) Error() string {
	return e.Message
}
