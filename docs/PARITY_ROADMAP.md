# hydrus-go parity roadmap

Last updated: 2026-05-04

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
- public thin-client recent browse, search, content, thumbnail, import, trash, downloader queue/status, and tag-mutation APIs
- Fyne desktop prototype for connect/browse/search/import/trash plus broader still-image preview, video poster previews, and FFmpeg-backed in-app video playback
- desktop search and sorting using daemon-backed predicates and sort modes
- daemon-owned anonymous PTR status, manual sync trigger, and definition/content application
- real anonymous PTR sync-out/upload flow for pending mappings, with real-time pending-count visibility
- daemon-owned external hydownloader supervision with URL/subscription queueing and Hydrus Client API autoimport compatibility

The following are explicitly **not** at parity yet:

- no complex daemon-side search logic (unions, negations, complex groupings)
- no broader repository mutation flows (petitions, review-services, advanced metadata)
- no native-Go downloader/parser/subscription parity beyond the current external hydownloader bridge
- no audio-capable full media-player parity beyond the current FFmpeg-backed in-app frame-stream watcher

## Highest-priority parity gaps

### 1. PTR definition/content application (DONE)

- the daemon now parses downloaded definitions/content updates and applies them into the local DB
- imported/local files correctly acquire PTR-derived tags during metadata and search reads
- sync status accurately reflects applied vs registered update counts

### 2. Complex search/query expansion

Current state:

- there is a daemon-side search foundation supporting AND-tags, `system:` predicates (`size`, `width`, `height`, `favorite`, `resolution`), and server-side sorting

Missing parity:

- complex union and negation logic in queries
- broader Hydrus-style search terms and groupings
- service-aware tag filters beyond the current current-mapping slice

Why this is next:

- Hydrus is fundamentally a local search/tag workstation
- full UI parity is blocked until the daemon can answer more complex search questions

### 3. Broadened tag mutation and repository workflows

Current state:

- tag mutation, pending staging, and commit upload exist and are wired end-to-end

Missing parity:

- petition flow for repository-backed removals
- review-services pages for managing broader repository state
- advanced metadata mutation (notes, ratings, etc.) beyond current read slices

Why this matters:

- true PTR parity is broad; the current slice covers the most frequent add-mapping workflow
- workstation users need a full set of tag actions

### 4. PTR sync-out/upload parity

Current state:

- daemon supports anonymous read/sync-in, pending-add staging, commit upload, and real-time pending-count visibility

Missing parity:

- discover which local files and mappings are eligible for PTR upload beyond the current manual pending-add workflow
- broader sync-out parity for mapping/petition updates

Why this matters:

- true PTR parity is bidirectional for normal tag-repository use
- the user explicitly called this out as a must-have parity target

### 5. Downloader and media hardening

Current state:

- hydrusd can now supervise an external hydownloader instance, queue URL/subscription work, and receive autoimported files back through the Hydrus Client API compatibility bridge
- the desktop watcher can now render FFmpeg-backed in-app video playback and broader still-image previews

Missing parity:

- broader hydownloader management UX beyond the current queue/status/API slice
- stronger end-to-end downloader/parser/gallery coverage against real sites
- richer in-app media playback features such as audio, seek/pause controls, and packaging of runtime dependencies

Why this matters:

- the downloader system now exists in practice, but it still needs hardening before it feels workstation-complete
- media viewing is no longer blocked outright, but it is not yet equal to a full native player experience

### 6. File lifecycle and metadata parity gaps

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
- downloader/subscription workflow polish beyond the new external hydownloader bridge

## Recommended implementation order

1. **Broaden daemon-side search logic**
   - add support for union, negation, and complex groupings
2. **Add repository petition flows**
   - support for removing/petitioning tags through the PTR
3. **Harden the new hydownloader and media slices**
	- verify external downloader supervision, importer behavior, and FFmpeg-backed watcher UX under real workloads
4. **Iterate on UI density and workstation workflows**
   - moving the desktop closer to full Hydrus workstation parity

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
  - currently confirms there are public mutation endpoints for `add_tags`, `pending_counts`, and `commit_pending`, but broader petition and review-services management are still missing

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

- complex search semantics (unions, negations, groupings) and `search_files` parity
- delete/petition tags
- review-services-style repository management pages (pending counts/commit exists; broader management missing)
- forget pending
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

- the daemon supports complex search expansion (unions, negations, groupings)
- the daemon supports the full set of `system:` predicates and server-side sort modes
- broader repository petition and review-services management land in the daemon and desktop
- the roadmap for workstation workflow parity (archive/inbox/undelete) is reduced to concrete implementation slices

## Next meaningful coding slice

The next implementation pass should focus on **Complex Search Expansion** and **Broader Repository Management**.

### Atomic deliverables

1. add support for union, negation, and complex groupings in daemon search
2. add support for remaining `system:` predicates (`local`, `trashed`, `deleted`) and name-based sorting
3. add repository petition staging and commit foundations
4. add basic review-services pages for managing broader repository state
5. add tests proving complex search queries return correct results across local and PTR tags

### Files most likely to change first

- `internal/db/hydrusdb/browse.go`
- `internal/api/httpapi/thin_client.go`
- `internal/db/hydrusdb/ptrsync.go`
- `internal/core/ptrsync/types.go`
- `internal/api/httpapi/ptr_pending.go`
- `internal/db/hydrusdb/metadata_tags.go`

### Explicit non-goals for that slice

- no desktop redesign
- no replacement of the existing `/v1/library/search` endpoint shape yet
- no PTR sync-out/upload yet
- no broad tag mutation API yet

That keeps the next step small enough to verify while still unlocking real user-visible PTR value.
