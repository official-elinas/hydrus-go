# hydrus-go parity roadmap

Last updated: 2026-04-21

## Goal

Move `hydrus-go` from early daemon + thin-client slices toward practical Hydrus
parity without breaking the daemon-first architecture.

That means:

- the daemon remains the only owner of SQLite, `client_files`, and PTR work
- desktop/UI work must stay on top of daemon APIs rather than touching the DB directly
- parity work should prioritize features that unlock real daily Hydrus behavior rather than broad but shallow surface-area growth

## Current confirmed state

The following are now confirmed working or intentionally present:

- DB-backed service discovery
- DB-backed `GET /get_files/file_metadata` with a meaningful first full/default slice
- serialized writable DB transactions via `BEGIN IMMEDIATE`
- managed `client_files` placement for daemon-owned imports
- public thin-client recent browse, content, thumbnail, import, and trash APIs
- Fyne desktop prototype for connect/browse/import/trash/selected-image preview
- desktop recent-page navigation using daemon paging
- daemon-owned anonymous PTR status and manual sync trigger
- real anonymous PTR snapshot fetch and `/update` download with local `repository updates` registration

The following are explicitly **not** at parity yet:

- downloaded PTR definitions/content are not applied into local mappings/query-visible tag state
- no daemon-side search engine comparable to Hydrus `search_files`
- no daemon-side tag mutation / pending / petition / commit flow
- no PTR sync-out/upload flow for pending mappings
- desktop still lacks Hydrus-style search/tagging/review-services workflows

## Highest-priority parity gaps

### 1. PTR definition/content application

Current state:

- PTR status, metadata fetch, and update-file download exist
- update blobs are verified, stored extensionlessly, and registered in `repository updates`

Missing parity:

- parse downloaded definitions/content updates
- write parsed mappings into the local DB in a Hydrus-compatible shape
- make imported/local files benefit from downloaded PTR tags during metadata and future search reads
- track what has been applied vs merely downloaded

Why this is first:

- the current PTR slice proves transport, but not value
- until downloaded updates affect local tag/query state, PTR sync does not change the user's real browse/search experience

### 2. Search/query foundation

Current state:

- there is recent-file browsing, but no real query engine

Missing parity:

- daemon-side equivalent of Hydrus file search primitives
- service-aware tag filters
- inbox/archive/local/trash constraints in queries
- basic sort and pagination semantics for search-driven browsing

Why this is essential:

- Hydrus is fundamentally a local search/tag workstation
- UI parity is mostly blocked until the daemon can answer real search questions

### 3. Tag mutation and pending repository flow

Current state:

- metadata can be read, but tags cannot yet be changed through daemon APIs

Missing parity:

- local tag add/delete flows
- PTR pending mapping creation
- petition flow for repository-backed removals
- pending-count and review state endpoints
- commit/forget pending operations

Why this matters:

- PTR sync-out cannot exist without pending-tag state
- a Hydrus-like UI needs tag actions long before full workstation polish

### 4. PTR sync-out/upload parity

Current state:

- daemon only supports anonymous read/sync-in behavior

Missing parity:

- discover which local files and mappings are eligible for PTR upload
- serialize pending mappings into Hydrus-compatible upload payloads
- upload pending content/metadata to the PTR
- reflect out-of-sync / upload-blocked conditions in daemon status
- surface pending counts and last upload results to clients

Why this matters:

- true PTR parity is bidirectional for normal tag-repository use
- the user explicitly called this out as a must-have parity target

### 5. File lifecycle and metadata parity gaps

Important remaining gaps after the four items above:

- archive/unarchive/inbox actions through daemon APIs
- undelete / clear deletion record / permanent delete decisions and semantics
- exact thumbnail semantics and broader metadata edge cases
- file relationships
- broader notes/ratings/edit behavior beyond current read slices

These matter, but they should not outrank PTR application, search, and pending flows.

## Must-have UI features for practical Hydrus use

These are ranked by how much real Hydrus usage they unlock.

### Tier 1: essential after daemon support exists

- search page with a left-side tag/search pane
- center results grid/list driven by daemon search APIs rather than recent-only browsing
- selected-file metadata pane with clear service grouping
- tag add/remove/pending controls for the selected file(s)
- file-state actions: archive/inbox/trash and refresh/requery
- review-services style PTR panel showing sync state, pending counts, and last errors

### Tier 2: important shortly after

- multi-select actions on search results
- saved/repeatable query presets
- better service visibility (my tags vs PTR vs repository updates)
- richer preview handling for more media types

### Tier 3: later parity polish

- more Hydrus-like dense multi-pane layout refinements
- advanced review/service-management pages
- downloader/subscription workflows

## Recommended implementation order

1. **Apply downloaded PTR definitions/content into local mappings/tag state**
   - unlocks actual value from the current PTR transport work
2. **Add a first daemon-side search API foundation**
   - enough for tag + file-domain querying and paged result IDs
3. **Add daemon-side tag mutation + pending-state APIs**
   - local tag actions and PTR-bound pending creation
4. **Add PTR sync-out/upload flow**
   - push pending mappings to the repository and expose progress/status
5. **Move the desktop from recent-browse shell toward a real search workspace**
   - only after the daemon can support it cleanly

## Concrete module map for the next phase

This is the practical starting map for implementation, based on the current codebase.

### PTR apply / sync-out work

- `internal/ptrsync/manager.go`
  - current daemon-owned coordinator for sync trigger, background lifetime, and terminal status
  - likely home for expanding one sync pass from "fetch metadata + download updates" to
    "fetch metadata + download updates + apply content + later upload pending content"
- `internal/db/hydrusdb/ptrsync.go`
  - already owns PTR foundation tables, persisted status, metadata snapshot, processed/download bookkeeping, and repository-updates registration
  - natural place for:
    - marking downloaded updates as applied
    - tracking processed definition/content ranges
    - persisting pending upload state and sync-out bookkeeping
- `internal/core/ptrsync/types.go`
  - current daemon/UI contract for PTR status
  - will need new fields once sync-out exists, likely around pending/upload counts and last upload result
- `internal/api/httpapi/ptrsync.go`
  - current status/trigger surface
  - can remain thin while the store contract grows underneath it

### Search/query foundation

- `internal/core/librarybrowse/types.go`
  - currently models recent-only browse requests
  - likely either needs expansion or a sibling search package for true query-driven results
- `internal/db/hydrusdb/browse.go`
  - current paged recent browse implementation
  - useful reference for result paging, but not sufficient for Hydrus-style search
- `internal/api/httpapi/router.go`
  - confirms the current public surface has no `search_files`-style endpoint yet
  - new search endpoints will need to be added here once a query store exists
- `internal/db/hydrusdb/metadata_tags.go`
  - already knows how to read service-aware current/pending/deleted/petitioned tag state for metadata payloads
  - likely the best read-path reference for search semantics and status-aware tag visibility

### Tag mutation / pending-state work

- `internal/core/tags/tags.go`
  - current tag normalization/validation base
  - should stay the common entry point before any mutation writes
- `internal/db/hydrusdb/tag_ids.go`
  - likely existing place to extend when mutation paths need durable tag ID creation/lookup
- `internal/db/hydrusdb/tx.go`
  - serialized `BEGIN IMMEDIATE` write gate for all future mutation flows
  - must remain the only mutation path for tag writes, pending writes, petitions, and sync-out bookkeeping
- `internal/api/httpapi/router.go`
  - currently confirms there are no public mutation endpoints for tags, pending counts, petitions, or commits

### File lifecycle parity follow-ups

- `internal/db/hydrusdb/trash.go`
  - current example of a Hydrus-style file state mutation through the serialized write gate
  - best mutation reference for later archive/inbox/undelete/permanent-delete work
- `internal/app/app.go`
  - current wiring point for stores exposed by the daemon
  - new search or tag-mutation stores will need to be threaded through here before the HTTP layer can expose them

## Missing public API surface confirmed today

The current router confirms the daemon exposes:

- metadata reads
- recent browse
- file content / thumbnail reads
- local import / upload import
- trash
- PTR status / manual sync trigger

It does **not** yet expose public endpoints for:

- search / `search_files`
- add/delete/petition tags
- pending counts / review-services-style repository state
- commit pending / forget pending
- archive / inbox / undelete / permanent delete
- file relationships

That means UI parity should continue to follow backend capability, not leap ahead of it.

## Transaction-speed validation plan

Performance work should be measured, not guessed.

### Required measurements

- single-file import latency (hashing + DB write + managed placement + thumbnail path)
- recent browse latency on realistic libraries
- metadata fetch latency for one file and small result batches
- writer contention impact while concurrent browse/read traffic is active
- PTR apply throughput once definition/content processing exists

### Suggested benchmark scenarios

- fresh native bundle with small library
- real copied Hydrus bundle with representative size
- import burst of still images
- browse/search requests while serialized writes are active

### Minimum output expected from the benchmark pass

- p50/p95 timings
- library size/context used for the run
- whether the bottleneck was hashing, SQLite, thumbnail generation, or filesystem placement
- follow-up actions only after evidence shows a bottleneck

### Best code entry points for the benchmark pass

- `internal/db/hydrusdb/tx.go`
  - measure serialized write wait time and transaction duration under contention
- `internal/db/hydrusdb/import.go`
  - measure import write-set cost directly once benchmark harnesses are added
- `internal/importing/importer.go`
  - measure end-to-end import latency including file placement and write-set composition
- `internal/db/hydrusdb/browse.go`
  - measure read latency for paged browse/search style requests
- `internal/db/hydrusdb/ptrsync.go`
  - measure update registration today and definition/content apply throughput once implemented

## Guardrails

- do not move SQLite access into the desktop client
- do not treat UI parity as layout-only work; real daemon capabilities must land first
- do not broaden the API surface with placeholder endpoints that lack Hydrus-like semantics
- do not optimize transaction paths before benchmark evidence exists

## Definition of the next meaningful milestone

The next milestone should be considered successful when all of the following are true:

- downloaded PTR update content is applied into local tag/mapping state
- imported files can acquire PTR-derived tags after sync
- the daemon exposes a first real search/query API
- the desktop can browse results through search rather than recent-only pagination
- the roadmap for pending-tag creation and PTR sync-out is reduced from planning to concrete implementation slices

## First concrete coding slice to execute next

The next implementation pass should stay narrow and focus on **PTR apply foundation**, not full sync-out yet.

### Atomic deliverables

1. add durable bookkeeping for which downloaded PTR update files have been applied vs merely registered
2. add a parser/loader path for downloaded PTR definitions/content files
3. write parsed definitions/content into local tag/mapping tables through `WithImmediateTx(...)`
4. update PTR status counters so `processed_definition_count` and `processed_content_count` reflect real applied work
5. add tests proving one imported/local file can acquire PTR-derived tag visibility after apply

### Files most likely to change first

- `internal/ptrsync/manager.go`
- `internal/db/hydrusdb/ptrsync.go`
- `internal/core/ptrsync/types.go`
- `internal/db/hydrusdb/metadata_tags.go`
- `internal/db/hydrusdb/ptrsync_test.go`
- `internal/ptrsync/manager_test.go`

### Explicit non-goals for that slice

- no desktop redesign
- no public search endpoint yet
- no PTR sync-out/upload yet
- no broad tag mutation API yet

That keeps the next step small enough to verify while still unlocking real user-visible PTR value.
