package app

import (
	"context"

	"github.com/official-elinas/hydrus-go/internal/core/filemetadata"
	"github.com/official-elinas/hydrus-go/internal/db/hydrusdb"
)

type metadataStoreRouter struct {
	readStore  *hydrusdb.Bundle
	writeStore *hydrusdb.Bundle
}

func newMetadataStoreRouter(
	readStore *hydrusdb.Bundle,
	writeStore *hydrusdb.Bundle,
) filemetadata.Store {
	if readStore == nil {
		return writeStore
	}

	if writeStore == nil {
		return readStore
	}

	return metadataStoreRouter{
		readStore:  readStore,
		writeStore: writeStore,
	}
}

func (s metadataStoreRouter) GetMetadata(
	ctx context.Context,
	request filemetadata.Request,
) ([]filemetadata.Row, error) {
	if request.CreateNewFileIDs && s.writeStore != nil && s.readStore != nil {
		if err := s.writeStore.EnsureHashIDs(ctx, request.Hashes); err != nil {
			return nil, err
		}

		request.CreateNewFileIDs = false
		return s.readStore.GetMetadata(ctx, request)
	}

	if request.CreateNewFileIDs {
		if s.writeStore == nil {
			return s.readStore.GetMetadata(ctx, request)
		}

		return s.writeStore.GetMetadata(ctx, request)
	}

	return s.readStore.GetMetadata(ctx, request)
}
