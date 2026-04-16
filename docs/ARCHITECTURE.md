# hydrus-go architecture

## Current architectural direction

`hydrus-go` is being built as a **headless daemon/backend**.

The daemon is the source of truth for Hydrus state. It owns:

- the SQLite database
- the managed `client_files` storage layout
- background processing and maintenance jobs
- API surfaces used by external clients

UI clients should connect to the daemon over stable APIs rather than directly
touching the database or file store.

## Network model

- local-only is the default operating mode
- LAN-connected clients are an intended near-term target
- broader remote exposure is explicitly later and should only happen with
  deliberate security hardening

## Migration stance

- preserve the original Python project as the reference implementation
- build new Go code only under `hydrus-go/`
- avoid line-by-line translation
- prefer backend/core extraction over UI replication
- preserve Hydrus's managed-library model rather than turning it into a loose file indexer

## What the daemon will eventually own

Over time, the daemon is expected to absorb:

- database access and migrations
- file import and hashing
- managed file storage
- thumbnails and media metadata extraction
- search and tagging behavior
- duplicates and file relationships
- local API compatibility
- downloader/import automation where it makes sense

## What is not a primary target

- a direct port of the legacy Hydrus desktop UI
- a reimplementation of the old Hydrus Server as a separate product

If anything server-like returns, it should come from the same daemon-first
architecture so that real clients can connect to a single backend that owns the
library state.

## Current implementation reality

Today, only the bootstrap daemon and a small compatibility-oriented API slice
exist. The current service catalog is fixed in memory and not yet backed by a
real Hydrus database.
