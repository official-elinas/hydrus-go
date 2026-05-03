// Package librarybrowse defines the thin-client browse contract for recent
// local files.
package librarybrowse

import "context"

// SortBy names the server-side sort orders supported by SearchByTags.
type SortBy string

const (
	// SortByImportNewest sorts by import timestamp descending (default).
	SortByImportNewest SortBy = "import_newest"
	// SortByImportOldest sorts by import timestamp ascending.
	SortByImportOldest SortBy = "import_oldest"
	// SortBySizeDesc sorts by file size descending (largest first).
	SortBySizeDesc SortBy = "size_desc"
	// SortBySizeAsc sorts by file size ascending (smallest first).
	SortBySizeAsc SortBy = "size_asc"
)

// PredicateOp is a comparison operator used in a SystemPredicate.
type PredicateOp string

const (
	PredicateOpLT  PredicateOp = "<"
	PredicateOpLTE PredicateOp = "<="
	PredicateOpEQ  PredicateOp = "="
	PredicateOpGTE PredicateOp = ">="
	PredicateOpGT  PredicateOp = ">"
)

// PredicateField names a files_info column that can be filtered server-side.
type PredicateField string

const (
	PredicateFieldSize   PredicateField = "size"
	PredicateFieldWidth  PredicateField = "width"
	PredicateFieldHeight PredicateField = "height"
)

// SystemPredicate is a single server-side numeric filter on a file attribute.
type SystemPredicate struct {
	Field PredicateField
	Op    PredicateOp
	Value int64
}

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

// SearchRequest describes a tag-search browse request.
type SearchRequest struct {
	Request
	Tags             []string
	SortBy           SortBy
	SystemPredicates []SystemPredicate
	// FavoriteFilter, when non-nil, restricts results to files that are (true)
	// or are not (false) marked as a favourite in any local like-rating service.
	FavoriteFilter *bool
}

// Store loads recent-local-file browse pages.
type Store interface {
	ListRecent(context.Context, Request) (Page, error)
	SearchByTags(context.Context, SearchRequest) (Page, error)
}

// UnsupportedError reports a browse mode that the current slice does not yet
// implement.
type UnsupportedError struct {
	Message string
}

func (e *UnsupportedError) Error() string {
	return e.Message
}
