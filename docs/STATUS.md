# hydrus-go status

Last updated: 2026-04-17

## Completed

- created `hydrus-go` as a standalone Go module inside the existing Hydrus repo
- initialized an independent git repository and pushed it to GitHub
- added the first headless daemon bootstrap (`hydrusd`)
- added environment-based config, structured logging, and graceful shutdown
- implemented initial HTTP compatibility endpoints:
  - `GET /`
  - `GET /healthz`
  - `GET /api_version`
  - `GET /verify_access_key`
  - `GET /session_key`
  - `GET /get_services`
  - `GET /get_service`
- implemented `GET /get_files/file_metadata` for identifier/basic read-only modes
- added bootstrap access-key and in-memory session-key auth flow
- added a fixed Hydrus-compatible bootstrap service catalog
- added optional read-only Hydrus DB bundle opening through `HYDRUS_GO_DB_DIR`
- attached the Hydrus SQLite bundle (`client.db`, `client.master.db`, `client.caches.db`, `client.mappings.db`) on a single query-only connection
- switched `/get_services` and `/get_service` to DB-backed behavior when a Hydrus DB bundle is configured
- added DB-backed `file_metadata` identifier/basic mode support, including forced MIME and blurhash reads
- added the first default/full DB-backed `file_metadata` non-tag subset with:
  - file service membership and import/delete timestamps
  - aggregate/local/domain modified timestamps
  - archived/inbox/local/trash/deleted state
  - known URLs, pixel hash, and IPFS multihashes
  - transparency/EXIF/human-readable/ICC metadata booleans
  - `include_milliseconds=true` support for the implemented full-mode timestamps
- explicitly rejects `include_notes=true` and `detailed_url_information=true` in the current full-mode slice
- added an internal writable Hydrus bundle mode with a serialized `BEGIN IMMEDIATE` transaction runner
- added a pure managed `client_files` layout package for deterministic file and thumbnail path resolution
- added an internal managed `client_files` placement layer with lazy directory creation and no-overwrite publication
- added an internal prepared-file local import checkpoint that composes managed placement with serialized DB writes
- added round-trip tests proving imported files become visible through the existing metadata read paths
- added thin-client-focused browse and asset endpoints for recent local files, originals, and thumbnails
- added public local-path import and trash endpoints built on top of separate read and write bundle connections
- added a first Fyne desktop prototype scaffold for `hydrusd`
- added DB and app-wiring tests using a copied minimal SQLite fixture bundle
- added tests for config validation, HTTP/auth behavior, and shutdown lifecycle
- documented the daemon-first migration direction and current bootstrap limits

## In Progress

- iterating on the Fyne prototype's grid, reconnect, and metadata UX after landing the first connect/browse/import/trash loop
- preparing thin-client-driven performance validation for SQLite and managed `client_files` behavior before PTR work begins

### Active reconnaissance notes

- the Hydrus client DB is an attached SQLite bundle, not a single file database
- the Python client uses a dedicated DB worker model with one long-lived connection
- transaction behavior is centered around `BEGIN IMMEDIATE` and savepoints
- the current daemon runtime now splits reads and writes across separate bundle connections so public local-path imports do not share a connection with browse/read handlers

## Next

### Roadmap checklist

- [x] Phase 1: headless bootstrap daemon
- [x] Phase 2: read-only DB-backed service discovery and basic metadata
- [x] Phase 3: first default/full non-tag metadata slice

### Phase 4: Writable Import Foundation

- [x] characterize the Python Hydrus write-set for a single local file import
- [x] design the first Hydrus-compatible writable import transaction flow
- [x] implement a serialized write model for the daemon's SQLite bundle access
- [x] implement managed-store path resolution in `client_files`
- [x] implement directory creation and file placement in `client_files`
- [x] add the first internal DB-backed local file import path
- [x] verify round-trip behavior from import to `GET /get_files/file_metadata`
- [x] document live-DB write constraints and operational expectations
- [x] extend the internal prepared-file checkpoint into a public hashing/sniffing import flow
- [x] add public delete/trash behavior for imported local files
- [x] add minimal browse/list APIs so clients can load local files without hash-by-hash probing
- [x] add thumbnail and original-file serving APIs for client preview flows
- [ ] expand the public import surface beyond single local-path imports into richer batch/upload workflows
- [ ] add thumbnail generation and richer import metadata capture after placement

### Phase 5: Thin Desktop Client MVP

- [x] connect a desktop client to the local daemon with the existing auth/bootstrap flow
- [x] support basic import, browse, add, and delete/trash workflows against daemon APIs
- [ ] validate the daemon/client contract with a simple multi-platform desktop UI before attempting Hydrus UI parity
- [x] keep the first client closer to `comfyui-image-browser` scope than full Hydrus parity

### Phase 6: Performance Validation

- [ ] validate import/browse/delete latency against real libraries through the thin client
- [ ] measure SQLite read/write behavior under realistic daemon workflows
- [ ] validate managed `client_files` correctness and throughput before PTR work begins
- [ ] address bottlenecks discovered during end-to-end client/daemon testing

### Phase 7: PTR Integration

- [ ] implement PTR repository sync foundations for imported files
- [ ] define PTR service configuration, auth, and local daemon state requirements
- [ ] make imported files eligible for PTR-driven tag/update retrieval
- [ ] verify end-to-end value from local import through PTR sync and tag acquisition

### Phase 8: Read/Query Expansion After Import + PTR

- [ ] continue `GET /get_files/file_metadata` toward broader parity for tags, ratings, notes, and viewing stats
- [ ] begin DB-backed search and tagging read paths on top of imported and PTR-synced data
- [ ] refine service/media-result behavior for common client workflows

## Later / Out of Scope for Now

- direct Hydrus desktop UI parity in the first client milestone
- full downloader/parser/subscription parity in the first phase
- legacy Hydrus Server parity
- multi-process/distributed architecture

## Open Risks / Unknowns

- SQLite locking behavior under Go concurrency may differ materially from Python behavior
- Hydrus search/tag semantics are much richer than simple SQL lookups
- downloader/parsing parity is large and should not derail the client-core migration
- the managed file-store contract must stay compatible with Hydrus expectations
- LAN support needs deliberate auth/security controls as the daemon surface grows

## Milestone Log

### 2026-04-16 — Milestone 1: headless bootstrap

- completed daemon bootstrap and first compatibility-oriented API slice
- verified with `go test ./...` and `go build ./cmd/hydrusd`
- committed as `feat: bootstrap headless hydrus-go daemon`

### 2026-04-16 — architecture clarified

- confirmed the Go backend should be the long-lived source of truth
- backend is intended to own SQLite, `client_files`, and background processing
- real clients are expected to connect locally first, with LAN support as the next network scope

### 2026-04-16 — DB reconnaissance started

- identified the Python client DB as a multi-file attached SQLite topology
- confirmed that Hydrus relies on a serialized single-connection transaction model
- narrowed the first DB-backed parity target to read-only service, metadata, and search-oriented behavior

### 2026-04-16 — Milestone 2: read-only DB-backed service and metadata slice

- added `HYDRUS_GO_DB_DIR` for optional read-only Hydrus DB bundle mode
- selected `modernc.org/sqlite` for the first DB-backed slice on Go 1.22
- opened the Hydrus bundle on a single connection with attached databases and `PRAGMA query_only = ON`
- switched service discovery to DB-backed reads when bundle mode is enabled
- added `GET /get_files/file_metadata` for:
  - `only_return_identifiers=true`
  - `only_return_basic_information=true`
  - optional blurhash and services object controls
- added rating-service extras in `services` / `services_v2` by decoding Hydrus `dictionary_string`
- verified with `go test ./...`

### 2026-04-16 — Milestone 3: first default/full non-tag metadata slice

- replaced the full-mode `501` with a DB-backed non-tag metadata response slice
- added `file_services`, modified/archive timestamps, local/trash/deleted state, URLs, pixel hash, IPFS multihashes, and basic embedded-metadata flags
- added `include_milliseconds=true` support for the implemented full-mode timestamps
- kept unsupported full-mode behaviors explicit by rejecting `include_notes=true` and `detailed_url_information=true`
- verified with `go test ./...`

### 2026-04-16 — Milestone 4: serialized writable transaction foundation

- added a separate internal writable bundle open path alongside the existing read-only daemon path
- added a serialized `BEGIN IMMEDIATE` transaction runner for future Hydrus-compatible writes
- verified commit, rollback, read-only rejection, context-aware write queuing, and post-failure reuse with fixture-backed tests
- kept the daemon runtime itself read-only while import and `client_files` write behavior are still being designed

### 2026-04-16 — Milestone 5: managed `client_files` path resolution foundation

- added a dedicated `internal/storage/clientfiles` package for deterministic managed file and thumbnail paths
- mirrored the current Hydrus prefix/layout rules for default granularity `2` and odd granularity cases like `3`
- added tests for hash normalization, path derivation, invalid inputs, and default root calculation from the DB directory
- kept the slice pure and read-only: no directory creation, no file copy/move orchestration, and no public import API yet

### 2026-04-16 — Milestone 6: managed `client_files` placement foundation

- added managed file and thumbnail placement helpers on top of the new layout package
- placement now creates parent directories lazily, writes to a temp file in the destination directory, preserves source `mtime`, and publishes without overwriting conflicting destinations
- added tests for successful placement, idempotent re-placement, coarse timestamp tolerance, conflict handling, and thumbnail placement
- kept the slice internal-only: no DB mutation integration and no public import API yet

### 2026-04-16 — Milestone 7: internal prepared-file import checkpoint

- added `Bundle.RecordPreparedLocalImport(...)` for minimal serialized DB import writes using dynamic service resolution
- added `internal/importing` to compose managed placement with the new DB write-set and best-effort cleanup on failure
- added fixture-backed tests for exact retry, duplicate conflicts, rollback, managed-file cleanup, and import-to-metadata round trips
- kept the daemon runtime and public HTTP surface read-only; this checkpoint is internal-only and caller-prepared for now

### 2026-04-16 — Milestone 8: thin-client browse and asset foundation

- added `GET /v1/library/recent` for paged recent-local-file browsing
- added `GET /v1/files/content` and `GET /v1/files/thumbnail` for thin-client preview/export flows
- added DB/storage-backed tests for recent browse, managed original-file resolution, and thumbnail resolution
- added HTTP tests for recent browse and managed asset streaming


### 2026-04-16 — Milestone 9: public local-path import foundation

- split the daemon into separate read and write Hydrus bundle connections when `HYDRUS_GO_DB_DIR` is configured
- added `POST /v1/import/local_file` for single-file daemon-local imports using hashing, MIME detection, and best-effort image dimensions
- added tests for local-path import retries, extension-based video fallback, and request/not-found HTTP error mapping
- added an app-level import -> browse -> metadata -> content round-trip test for the thin-client contract
- added a connection-isolation test proving read handlers do not observe uncommitted writes from the writable bundle

### 2026-04-17 — Milestone 10: trash-first prototype pivot

- added `POST /v1/files/trash` for single-file trash moves through the daemon's public thin-client contract
- kept the daemon startup safe by degrading to read-only mode when the writable bundle cannot be opened
- pivoted the desktop client direction from Qt to a Fyne-based `hydrusd` prototype
- started a Fyne UI shell shaped by `image-tests/comfyui-image-browser.png` and `image-tests/hydrus.png` for add/trash validation
