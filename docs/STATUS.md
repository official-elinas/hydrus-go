# hydrus-go status

Last updated: 2026-05-03

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
- expanded full/default DB-backed `file_metadata` with a first daemon-served `tags` slice:
  - service-keyed `tags` payloads with per-service `storage_tags` and Hydrus-like `display_tags`
  - compatibility `service_keys_to_statuses_to_tags` and `service_keys_to_statuses_to_display_tags` behind `hide_service_keys_tags=false`, with legacy display values matching `tags[*].display_tags`
  - `display_tags` now prefer specific display cache tables when available, otherwise fall back to sibling/parent-expanded storage tags, with deleted/petitioned display copied from storage
- expanded full/default DB-backed `file_metadata` with daemon-served `ratings` keyed by rating service key:
  - Hydrus-like API values for local like/dislike, numerical, and inc/dec services
  - unrated service defaults that stay aligned with Hydrus (`null` for like/numerical, `0` for inc/dec)
- expanded full/default DB-backed `file_metadata` with daemon-served `file_viewing_statistics`:
  - always emits media-viewer, preview-viewer, and client-api-viewer entries in Hydrus canvas order
  - uses float-second `viewtime` and `last_viewed_timestamp` values independent of `include_milliseconds`
- expanded full/default DB-backed `file_metadata` with optional daemon-served `notes`:
  - emitted only when `include_notes=true`
  - uses a Hydrus-like note-name → note-text object and returns `{}` for files with no stored notes
- expanded full/default DB-backed `file_metadata` with optional daemon-served `detailed_known_urls`:
  - emitted only when `detailed_url_information=true`
  - preserves `known_urls` while adding Hydrus-like normalized/classified URL rows for the currently implemented URL-detail layer
- added writable-bundle `create_new_file_ids=true` support for DB-backed `file_metadata`:
  - unknown hashes now allocate master `hash_id` rows when a writable bundle is available
  - identifier mode returns the new `file_id` immediately, while basic/full modes still return missing rows until a real `files_info` record exists
  - read-only/degraded daemon mode still rejects this write-semantics flag
- added an internal writable Hydrus bundle mode with a serialized `BEGIN IMMEDIATE` transaction runner
- added a pure managed `client_files` layout package for deterministic file and thumbnail path resolution
- added an internal managed `client_files` placement layer with lazy directory creation and no-overwrite publication
- added an internal prepared-file local import checkpoint that composes managed placement with serialized DB writes
- added round-trip tests proving imported files become visible through the existing metadata read paths
- added thin-client-focused browse and asset endpoints for recent local files, originals, and thumbnails
- added public local-path import and trash endpoints built on top of separate read and write bundle connections
- added best-effort JPEG/PNG import-time enrichment for `pixel_hash` and `has_transparency` so those full-metadata fields round-trip immediately for daemon-imported still images
- added best-effort managed thumbnail generation for imported JPEG/PNG/GIF files, including stale-thumbnail repair on exact re-import
- added a first Fyne desktop prototype scaffold for `hydrusd`
- added focused daemonclient contract tests for the desktop client's auth, browse, mutation, thumbnail, and content-fetch HTTP paths
- added a real `hydrusd --listen host:port` runtime override for temporary LAN testing without extra environment setup
- added explicit Linux and Windows desktop build targets, plus a Windows GUI-subsystem build so Explorer launches do not spawn an extra terminal window
- added selected-file original preview and in-memory caching in the Fyne client for JPEG/PNG/GIF files through `/v1/files/content`
- bounded selected-file preview work to keep the thin client responsive (16 MiB payload, 8192px maximum dimension, 16,000,000 decoded pixels)
- added a native-Go fresh client bundle bootstrap for empty or missing `HYDRUS_GO_DB_DIR` targets; plain `hydrusd` now defaults to seeding `./db` unless bootstrap is explicitly disabled
- added runtime bootstrap timeout and bundle-state safety checks for first-start initialization
- removed the obsolete Python-interpreter / Hydrus-root bootstrap knobs from the active config and CLI surface
- expanded the native first-start seed with `client.temp.db`, default managed-storage metadata/root, a seeded `version` row, and hidden built-in services needed for closer Hydrus bootstrap shape
- expanded the native first-start service-list seed with `downloader tags` and `favourites`, including a Hydrus-style favourites rating dictionary
- added DB and app-wiring tests using a copied minimal SQLite fixture bundle
- added tests for config validation, HTTP/auth behavior, and shutdown lifecycle
- added daemon-owned anonymous PTR status persistence, remote snapshot fetch/decode/persist, and repository metadata durability
- added `POST /service/ptr/sync` for daemon-owned single-flight background PTR sync triggering
- added PTR manager/app shutdown handling so active anonymous PTR sync leases are cleared before daemon teardown
- added real anonymous PTR `/update` download with expected-hash verification, extensionless managed-file placement, and local `repository updates` registration
- added daemon-owned PTR download bookkeeping so persisted status reflects real locally registered update counts
- added daemonclient PTR status/trigger support plus a Fyne-side PTR status/manual-sync popup under the desktop Network menu that stays daemon-first
- added Fyne recent-grid incremental loading on top of the daemon's existing recent offset/limit API
- added PTR-side remote-busy retry handling with a short retry ladder, capped exponential backoff, and an explicit server-issue failure after repeated busy responses
- added batched local PTR downloaded-update registration/finalization so one sync pass does less repeated serialized DB bookkeeping per downloaded blob
- improved the Fyne shell layout so the main client window resizes more sanely and the selected-preview empty state no longer collapses into vertical placeholder text
- added a dedicated parity roadmap document covering the next backend, PTR, performance, and UI priorities
- documented the daemon-first migration direction and current bootstrap limits
- added daemon-backed tag autocomplete, manual DB integrity checks, and narrow PTR pending-add / commit flows across the daemon + desktop prototype
- added PTR definition/content apply into local mappings/tag state with processed counters and durable completion semantics
- added `GET /v1/library/search` plus matching daemonclient support for daemon-backed AND-tag local-file search over existing current-file and current-mapping tables, including PTR-applied tags
- added an app-level PTR restart smoke test for completed-sync persistence across daemon restart
- wired the Fyne search bar/grid to daemon-backed local-library tag search for explicit/exact tag queries and later removed the loaded-grid fallback so unsupported terms no longer apply client-local filtering
- added a direct PTR smoke test that proves PTR-applied mappings remain searchable through the library search path after reopen/restart
- verified the resizable still-image watcher path for arrow-key and mouse-wheel previous/next navigation while keeping the selected gallery tile visibly highlighted, and added regression coverage for watcher navigation and tile-selection styling
- added daemon-side search support for `system:` predicates (`size`, `width`, `height`, `favorite`, `resolution`) and server-side sort controls (`import_oldest`, `size_desc`, `size_asc`)
- wired the desktop search path to daemon-backed predicates (`size`, `width`, `height`, `favorite`, `resolution`) and sort modes; unsupported desktop search terms are now ignored rather than applied as UI-local fallback filtering
- added end-to-end PTR pending-state visibility including DB counts, manager/store/API layers, daemonclient, and Fyne pending-count labeling
- added a minimal PTR completion-and-effect proof surface by persisting verified current mapping counts and exposing them through DB status, API payloads, daemonclient decoding, and desktop PTR status/completion text
- updated the live daemon bootstrap auth principal to include permissions for local PTR pending count, staging, and commit flows
- verified fresh first-start bootstrap end-to-end against a real fresh DB directory through live smoke testing
- expanded the `GET /manage_services/pending_counts` endpoint for real-time repository pending-state visibility

## In Progress

- iterating on the Fyne prototype's preview caching, reconnect, metadata, and broader visual polish after landing the latest search and PTR slices
- preparing thin-client-driven performance validation for SQLite and managed `client_files` behavior alongside the first PTR daemon work
- converting the latest parity reconnaissance into a concrete implementation order in `docs/PARITY_ROADMAP.md`
- broadening daemon-side search beyond the current predicate slice into more complex Hydrus-like search terms and union/negation logic

### Active reconnaissance notes

- the Hydrus client DB is an attached SQLite bundle, not a single file database
- the Python client uses a dedicated DB worker model with one long-lived connection
- transaction behavior is centered around `BEGIN IMMEDIATE` and savepoints
- the current daemon runtime now splits reads and writes across separate bundle connections so public local-path imports do not share a connection with browse/read handlers
- the packaged Linux Hydrus release exposes `hydrus_client -d/--db_dir`, and a headless first-start probe created the canonical client DB bundle plus `client_files` in a fresh directory
- the daemon now has a fully native first-start bundle path, but upstream bootstrap parity remains intentionally partial
- on this machine, the broad `go test ./internal/db/hydrusdb` suite still exceeds Go's default 10-minute package timeout even when the new search/PTR reopen tests are skipped; targeted changed-path tests are passing and remain the current reliable verification gate for this slice

## Next

### Roadmap checklist

See also: [`docs/PARITY_ROADMAP.md`](./PARITY_ROADMAP.md) for the prioritized parity order and must-have UI list.

- [x] Phase 1: headless bootstrap daemon
- [x] Phase 2: read-only DB-backed service discovery and basic metadata
- [x] Phase 3: first default/full metadata slices

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
- [x] add public trash behavior for imported local files
- [x] add minimal browse/list APIs so clients can load local files without hash-by-hash probing
- [x] add thumbnail and original-file serving APIs for client preview flows
- [ ] expand the public import surface beyond the current single-file local-path/staged-upload APIs into richer batch workflows
- [x] add thumbnail generation for supported JPEG/PNG/GIF imports after placement
- [x] capture richer still-image import metadata after placement
- [ ] continue enriching imported-file metadata beyond the initial JPEG/PNG pixel-hash/transparency slice

### Phase 5: Thin Desktop Client MVP

- [x] connect a desktop client to the local daemon with the existing auth/bootstrap flow
- [x] support basic import, browse, add, and trash workflows against daemon APIs
- [x] preview selected JPEG/PNG/GIF originals through daemon APIs with bounded client-side safety limits
- [x] add recent-page navigation / incremental loading to the prototype's browse grid
- [x] surface daemon-owned PTR status and a manual sync trigger in the prototype
- [x] fix the selected-preview empty state and first-pass shell resizing/layout problems in the prototype
- [ ] validate the daemon/client contract with a simple multi-platform desktop UI before attempting Hydrus UI parity
- [x] keep the first client closer to `comfyui-image-browser` scope than full Hydrus parity

### Phase 6: Performance Validation

- [ ] validate import/browse/trash latency against real libraries through the thin client
- [ ] measure SQLite read/write behavior under realistic daemon workflows
- [ ] validate managed `client_files` correctness and throughput as PTR work expands
- [ ] address bottlenecks discovered during end-to-end client/daemon testing

### Phase 7: PTR Integration (Sync-in logic for definitions/content now completed)

- [x] define PTR service configuration defaults, shared read-only anonymous auth, and local daemon status/state requirements
- [x] add daemon-side PTR service/mapping-table/state foundations and a pollable status endpoint
- [x] add real anonymous PTR session/account/options/tag-filter/metadata fetch and durable snapshot persistence
- [x] add a daemon-owned background trigger/lifecycle slice for anonymous PTR sync
- [x] implement anonymous PTR `/update` download and local repository-updates registration
- [x] add daemon-side busy-response retry/backoff handling and batch the local downloaded-update finalization hot path
- [x] process downloaded PTR definitions/content into local mappings and tag/query state
- [x] prove PTR-applied mappings remain queryable through the daemon search path after restart/reopen
- [ ] verify end-to-end value from local import through PTR sync and tag acquisition
- [x] add PTR pending-add staging and commit foundations for eventual sync-out parity
- [ ] broaden PTR sync-out/upload beyond the current pending-add commit slice into fuller mapping/petition parity
- [x] add real-time pending-count visibility to the daemon and thin client

### Phase 8: Read/Query Expansion After Import + PTR

- [ ] continue `GET /get_files/file_metadata` toward broader parity for detailed URLs, exact thumbnail sizing, and remaining edge-case payload semantics
- [x] begin DB-backed tag-search read paths on top of imported and PTR-synced data
- [x] add a first daemon-side local tag-search endpoint for AND-tag browse over local files plus current mappings
- [x] wire the desktop search bar to the daemon-backed tag-search slice for explicit/exact tag queries without applying local fallback for unsupported non-tag/system filtering
- [x] broaden daemon-side search/query into Hydrus-like search terms, system predicates, and server-side sorting that can drive a real search page
- [ ] add daemon-side tag mutation and pending-state APIs as a prerequisite for PTR sync-out and UI parity
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

### 2026-04-28 — Milestone: search slice expansion, pending-count visibility, and bootstrap verification

- verified fresh hydrusd bootstrap end-to-end against a real fresh DB directory
- expanded daemon-side search to support `system:` predicates (`size`, `width`, `height`, `favorite`, `resolution`) and server-side sort modes
- wired the Fyne search grid to use daemon-backed predicates (`size`, `width`, `height`, `favorite`, `resolution`) and sorting; unsupported terms now leave the daemon-backed result set unchanged instead of applying UI-local fallback filtering
- added end-to-end PTR pending-state visibility including DB counts, manager/store/API, daemonclient, and Fyne UI labels
- added persisted verified-current-mapping PTR status reporting across the DB, API, daemonclient, and Fyne desktop status text
- updated the live daemon bootstrap auth principal to include required permissions for local PTR pending count, staging, and commit flows
- verified with live smoke testing and `make check-desktop`

### 2026-05-03 — Milestone: PTR continuity, bundle semantics, and observability hardening

- added bundle-local PTR opt-in persistence through a simple `ptrsync` marker beside `client.db`, so a user-triggered sync survives daemon restart on the same Hydrus bundle
- changed transient PTR `/update` transport failures such as EOF / connection reset to persist `phase=retrying` and auto-resume instead of falling back to terminal idle failure
- taught the existing-DB continuation path to reuse SQLite-stored repository update bodies before attempting `/update` again, so partial bundles with large backlogs no longer act like fresh queues after a retry or restart
- matched upstream Hydrus repository compatibility for invalid definition tags that clean to empty by mapping those repository-local tag ids to the sentinel `invalid repository tag` instead of aborting the whole definitions update
- clarified PTR status semantics to separate:
  - local backlog completion (`is_complete`)
  - future-remote-schedule freshness (`is_up_to_date`)
  - pending download bundles
  - pending process bundles
  - effective end-to-end progress pacing versus raw network fetch timing
- updated the desktop PTR popup to present repository update **bundles** and done/total bundle progress rather than implying ordinary imported media-file downloads
- refreshed README/docs to match the live API surface, current PTR behavior, and added a machine-readable `openapi.json` for Swagger/OpenAPI tooling

### 2026-04-26 — Milestone: still-image watcher navigation and highlight verification

- confirmed the in-app still-image watcher remains a resizable Fyne window rather than a fullscreen/OS handoff
- verified watcher navigation now follows gallery order through arrow keys and mouse wheel while preserving selected-tile highlight state in the grid
- fixed a missing `//go:build fyne` guard on `internal/desktop/fyneapp/watcher.go`
- verified with `go test -tags fyne ./internal/desktop/fyneapp -count=1` and `make check-desktop`


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

### 2026-04-17 — Milestone 11: managed still-image thumbnails

- added best-effort daemon-side thumbnail generation after successful local-path imports for JPEG, PNG, and GIF files
- made exact re-import capable of repairing missing or stale managed thumbnails without overwriting unrelated placement failures
- added import/storage/app tests covering happy-path thumbnail availability, corruption repair, and bounded downscaling
- verified with targeted package tests, `go test ./...`, and `make check-desktop`

### 2026-04-17 — Milestone 12: desktop contract and build ergonomics

- added dedicated daemonclient contract tests for auth bootstrap, browse, mutation, thumbnail fetch, and original content fetch
- added `hydrusd --listen host:port` for one-off LAN test binds without requiring separate non-local environment toggles
- added explicit `build-desktop-linux` and `build-desktop-windows` targets alongside the existing host-platform desktop build
- switched the Windows desktop build to the GUI subsystem so Explorer launches do not open an extra terminal window
- verified with `go test ./...`, `make build-desktop`, `make build-desktop-windows`, and `make check-desktop`

### 2026-04-17 — Milestone 13: bounded selected-file original preview

- added larger selected-file preview in the Fyne client for JPEG/PNG/GIF originals using `GET /v1/files/content`
- added request cancellation, reconnect invalidation, and stale-result suppression for preview loads
- bounded preview payload and decoded image size to keep the thin client responsive during Windows/LAN testing
- verified with `go test ./...`, `make build-desktop`, `make build-desktop-windows`, and `make check-desktop`

### 2026-04-18 — Milestone 14: native-Go fresh bundle bootstrap

- started with an opt-in daemon startup bootstrap for empty or missing `HYDRUS_GO_DB_DIR` targets
- shifted the implementation on `feat/go-bootstrap-branch` to a native-Go empty-bundle initializer aligned to current hydrus-go runtime expectations
- preserved fail-fast bundle-state handling for `ready`, `empty`, `partial`, and `non-empty without bundle` directory states
- verified native first-start behavior with targeted bootstrap/config/app tests plus `go test ./...`

### 2026-04-19 — Milestone 15: DB-backed metadata tags slice

- expanded full/default `GET /get_files/file_metadata` rows with Hydrus-like `tags` objects keyed by service key
- added DB-backed storage-tag reads from `client.mappings.db` for local/tag-repository services, combined-tag unioning when the virtual combined tag service is present, and matching display-tag payloads
- restored the deprecated `service_keys_to_statuses_to_tags` and `service_keys_to_statuses_to_display_tags` maps behind `hide_service_keys_tags=false`, with the legacy display map matching `tags[*].display_tags`
- kept the daemon-first boundary intact; desktop clients still receive tags only through daemon-served metadata
- added display-tag parity that prefers specific display cache tables when available and otherwise derives display tags through sibling/parent fallback
- verified with targeted DB/API/app/daemonclient package tests

### 2026-04-19 — Milestone 16: DB-backed metadata ratings slice

- expanded full/default `GET /get_files/file_metadata` rows with a `ratings` object keyed by rating service key
- added DB-backed reads from `main.local_ratings` and `main.local_incdec_ratings` for local rating services
- mirrored Hydrus API rating semantics for like/dislike booleans, numerical star conversion, and inc/dec integer counts
- preserved Hydrus-like unrated defaults in the payload (`null` for like/numerical services, `0` for inc/dec services)
- kept the daemon-first boundary intact; desktop clients still receive ratings only through daemon-served metadata
- verified with targeted DB/API/app/hydrusdb package tests

### 2026-04-19 — Milestone 17: DB-backed metadata viewing statistics slice

- expanded full/default `GET /get_files/file_metadata` rows with `file_viewing_statistics`
- added DB-backed reads from `main.file_viewing_stats` for the Hydrus API canvas types exposed in metadata
- mirrored Hydrus canvas ordering and labels for media viewer, preview viewer, and client api viewer
- preserved Hydrus-style float-second `viewtime` and `last_viewed_timestamp` values without tying them to `include_milliseconds`
- synthesized zeroed default entries when a file has no stored viewing stats for one or more exposed canvas types

### 2026-04-19 — Milestone 18: DB-backed metadata notes slice

- expanded full/default `GET /get_files/file_metadata` rows with optional `notes` when `include_notes=true`
- added DB-backed reads from `main.file_notes`, `external_master.labels`, and `external_master.notes`
- mirrored Hydrus API note payload shape as a note-name → note-text object
- preserved a safe migration fallback by returning empty notes objects when a file has no notes or when note tables are absent in a partial bundle
- verified with targeted DB/API/app/hydrusdb package tests

### 2026-04-19 — Milestone 19: DB-backed metadata detailed URL slice

- expanded full/default `GET /get_files/file_metadata` rows with optional `detailed_known_urls` when `detailed_url_information=true`
- preserved the existing sorted `known_urls` field while adding Hydrus-like normalized/classified URL rows alongside it
- mirrored Hydrus-style unknown-url payloads for valid unrecognised full URLs and current parser-missing semantics for the seeded `otherbooru` post URL fixture
- verified with targeted DB/API/app/hydrusdb package tests

### 2026-04-19 — Milestone 20: writable metadata file-ID allocation slice

- expanded DB-backed `GET /get_files/file_metadata` to honor `create_new_file_ids=true` when a writable bundle is available
- added master-hash allocation through `external_master.hashes` so identifier-mode lookups can return newly created `file_id` values for unknown hashes
- preserved Hydrus-like missing-row behavior for basic/full metadata until a corresponding `main.files_info` row exists
- kept read-only/degraded daemon mode safe by continuing to reject `create_new_file_ids=true` there
- verified with targeted DB/API/app/hydrusdb package tests

### 2026-04-19 — Milestone 21: import-time still-image metadata enrichment

- enriched daemon-local and staged-upload JPEG/PNG imports so newly imported files immediately round-trip through full metadata with `pixel_hash` and `has_transparency`
- kept the core import transaction durable by treating the auxiliary rows as best-effort optional metadata that can also be backfilled on exact retry
- tightened duplicate handling so conflicting auxiliary metadata is rejected instead of silently widening `pixel_hash_map` state
- intentionally left animated-media and blurhash import-time enrichment for later parity work
- verified with targeted DB/import/app tests plus a full `go test ./...` pass

### 2026-04-20 — Milestone 22: daemon-owned PTR trigger and lifecycle slice

- recovered and normalized daemon-owned PTR runtime state safely at manager startup without stealing active leases during normal ensure paths
- added durable PTR sync runtime persistence with guarded `run_token` ownership, remote snapshot storage, and repository metadata append/replace behavior
- added real anonymous PTR client/wire handling for `/session_key`, `/account`, `/options`, `/tag_filter`, and `/metadata`
- added a daemon-owned single-flight background trigger path so `POST /service/ptr/sync` starts one real PTR sync pass and repeated triggers do not start a second run
- added shutdown-safe PTR manager/app lifecycle handling so daemon teardown cancels in-flight work and clears active leases before bundle close
- kept actual PTR `/update` download/content processing out of scope for this slice; status/triggering now exist, but repository definitions/content are still not applied locally
- verified with targeted `internal/ptrsync`, `internal/api/httpapi`, and `internal/app` package tests plus app-level trigger/shutdown manual QA

### 2026-04-20 — Milestone 23: daemon-owned PTR update download slice

- added real anonymous PTR `GET /update?update_hash=...` fetch support with expected SHA-256 body verification
- classified downloaded update payloads as Hydrus definitions/content update mimes and stored them with Hydrus-style extensionless managed filenames
- registered downloaded update blobs in the local `repository updates` file domain and cleared pending unregistered-update rows after successful local import
- updated PTR status persistence so `downloaded_update_count` reflects real daemon-owned local registration state rather than declared remote metadata only
- intentionally left definitions/content processing and tag-application behavior for the next PTR slice
- verified with targeted `./internal/ptrsync ./internal/db/hydrusdb ./internal/importing ./internal/storage/clientfiles` package tests plus live daemon + mock PTR server manual QA

### 2026-04-22 — Milestone 24: PTR retrying UI, batch finalization, and shell cleanup

- replaced the earlier short hidden PTR busy loop with a daemon-visible retry model using a `2s/3s/4s/5s/5s` ladder, capped follow-up backoff, and an explicit server-issue failure after repeated busy responses
- batched local PTR downloaded-update registration/finalization work so one sync pass does less repeated serialized DB bookkeeping per downloaded blob
- clarified the desktop PTR wording so downloaded counts refer to downloaded update files rather than implying applied mappings
- fixed the desktop PTR polling loop so it no longer stops after about one minute and require a manual refresh to continue updating
- cleaned up the first Fyne shell layout so the main split panes resize more sanely and the selected-preview empty state no longer renders vertical placeholder text
- verified with targeted PTR/backend package tests, targeted `hydrusdb` PTR tests, Fyne tests, and rebuilt daemon/desktop binaries

### 2026-04-26 — Milestone 25: current branch checkpoint for near-parity search + PTR

- the live branch now applies PTR definitions and mappings into local tag/query state rather than stopping at downloaded update blobs
- the live branch now exposes `GET /v1/library/search` and matching daemonclient support for AND-tag local-file search over existing browse + mapping tables, so PTR-applied site tags have a daemon search surface
- the app test suite now includes `TestRun_PersistsCompletePTRSyncAcrossAppRestart`; the next direct hardening step after this checkpoint was a restart/reopen assertion that PTR-applied mappings stay query-visible

### 2026-04-26 — Milestone 26: desktop daemon-backed tag search + PTR reopen searchability proof

- wired the Fyne search bar/grid to the daemon-backed local-library search path for explicit/exact tag queries while keeping loaded-grid fallback for non-tag text and `system:` predicates
- added direct `hydrusdb` coverage for AND-tag paging semantics in the new local-library search path
- added direct PTR reopen coverage that applies PTR mappings, completes sync, reopens the bundle, and proves the PTR-applied tag remains searchable through `SearchByTags`
- verified with targeted `hydrusdb`, `httpapi`, `daemonclient`, `app`, and tagged Fyne desktop tests, plus `make check-desktop`

### 2026-05-02 — Milestone 27: PTR SQLite throughput cleanup and preview placeholder fix

- traced the PTR slowdown after raw update-body storage moved into SQLite and found the main new stall on the flush/apply side rather than the raw network fetch side
- fixed the worst per-block regression by making the processable-update query return only `processed = 0` rows, so each 25-update flush no longer drags already-processed SQLite BLOB rows back through the apply pass
- removed the redundant per-small-chunk PTR download metrics write transaction inside the fetch loop and left the flush-driven metrics/status updates in place
- trimmed unnecessary in-memory body copies in the PTR fetch/flush path while preserving the same daemon-owned retry/apply behavior
- cautiously bumped PTR update fetch concurrency from `2` to `3` after the DB-side hot path cleanup, keeping the public-PTR safety posture well below the earlier `8`-goroutine experiment
- fixed the Fyne selected-preview empty-state layout so the top-right placeholder/help text stays horizontal instead of collapsing into a vertical column in a narrow centered label container
- confirmed that SQLite `*.db-wal` and `*.db-shm` files are now expected runtime sidecars for writable bundle opens because `hydrusd` enables WAL on the writable main and attached DBs; they are not extra logical Hydrus databases
- verified with targeted `internal/db/hydrusdb`, `internal/ptrsync`, `internal/app`, and tagged Fyne desktop tests, plus rebuilt daemon/desktop binaries and manual QA that showed a seeded `600 processed / 25 unprocessed` PTR dataset now returns only the 25 remaining processable rows
