// Package fileassets defines thin-client file and thumbnail asset resolution
// contracts.
package fileassets

import "context"

// Descriptor identifies a managed file asset that can be streamed to a client.
type Descriptor struct {
	FileID   int64
	Hash     string
	Path     string
	Filename string
	MIME     string
}

// Store resolves managed file assets for streaming.
type Store interface {
	ResolveFileContent(context.Context, int64) (Descriptor, error)
	ResolveThumbnail(context.Context, int64) (Descriptor, error)
}

// NotFoundError reports that an asset could not be resolved.
type NotFoundError struct {
	Message string
}

func (e *NotFoundError) Error() string {
	return e.Message
}
