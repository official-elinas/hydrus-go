# hydrus-go status

Last updated: 2026-04-16

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
- added DB and app-wiring tests using a copied minimal SQLite fixture bundle
- added tests for config validation, HTTP/auth behavior, and shutdown lifecycle
- documented the daemon-first migration direction and current bootstrap limits

## In Progress

- characterizing the Python write-set for the first Hydrus-compatible local file import
- defining managed-store placement and the first minimal import API slice on top of the serialized write foundation

### Active reconnaissance notes

- the Hydrus client DB is an attached SQLite bundle, not a single file database
- the Python client uses a dedicated DB worker model with one long-lived connection
- transaction behavior is centered around `BEGIN IMMEDIATE` and savepoints
- the current daemon runtime still stays read-only while the internal write foundation is proven with fixtures

## Next

### Roadmap checklist

- [x] Phase 1: headless bootstrap daemon
- [x] Phase 2: read-only DB-backed service discovery and basic metadata
- [x] Phase 3: first default/full non-tag metadata slice

### Phase 4: Writable Import Foundation

- [ ] characterize the Python Hydrus write-set for a single local file import
- [ ] design the first Hydrus-compatible writable import transaction flow
- [x] implement a serialized write model for the daemon's SQLite bundle access
- [ ] implement managed-store path resolution and file placement in `client_files`
- [ ] add the first DB-backed local file import path/API
- [ ] verify round-trip behavior from import to `GET /get_files/file_metadata`
- [ ] document live-DB write constraints and operational expectations

### Phase 5: PTR Integration

- [ ] implement PTR repository sync foundations for imported files
- [ ] define PTR service configuration, auth, and local daemon state requirements
- [ ] make imported files eligible for PTR-driven tag/update retrieval
- [ ] verify end-to-end value from local import through PTR sync and tag acquisition

### Phase 6: Read/Query Expansion After Import + PTR

- [ ] continue `GET /get_files/file_metadata` toward broader parity for tags, ratings, notes, and viewing stats
- [ ] begin DB-backed search and tagging read paths on top of imported and PTR-synced data
- [ ] refine service/media-result behavior for common client workflows

## Later / Out of Scope for Now

- native Go GUI
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
