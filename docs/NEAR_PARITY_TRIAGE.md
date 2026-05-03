# NEAR PARITY TRIAGE

**Last Updated**: 2026-04-28  
**Context**: This document is a code-grounded triage snapshot for getting `hydrus-go` to **near practical parity** under severe time pressure. `hydrus-go` is a **conversion**, not a 1:1 PyQt translation. When this document disagrees with older docs, **current code wins**.

## What is definitely working now

- The daemon already handles DB-backed metadata reads, local import, upload import, trash, thumbnails, original-file serving, and access/session auth.
- The current API surface includes `/v1/library/search`, `/v1/tags/autocomplete`, `/v1/import/url`, `/v1/import/upload`, `/add_tags/add_tags`, `/manage_database/integrity_check`, `/manage_services/pending_counts`, and `/manage_services/commit_pending`.
- PTR sync-in is fully operational: the daemon downloads repository update bundles, persists them locally, applies definition updates, applies mapping updates, maintains processed counters, and now distinguishes local completion from true up-to-date state.
- The Fyne desktop already has a resizable split layout, left-side selected-file tags, a popup tag editor with autocomplete, daemon-backed search/predicate filtering, sorting, colored tag rendering, artist-first gallery labels, and an in-app watcher window for still images.
- The still-image watcher path is now re-verified: it stays an in-app resizable window, supports arrow-key and mouse-wheel previous/next navigation, and keeps the selected gallery tile highlight tied to the current preview selection.
- The daemon now has a local-library search foundation that supports AND-tag queries, `system:` predicates (`size`, `width`, `height`, `favorite`, `resolution`), and server-side sort modes (`import_oldest`, `size_desc`, `size_asc`). Unsupported desktop search terms are currently ignored rather than applied as UI-local fallback filters.
- PTR pending-state visibility exists end-to-end: DB counts, manager/store/API layers, daemonclient, and Fyne pending-count labeling are all wired.
- The biggest remaining practical gaps are **complex daemon-side search logic** (unions/negations), **broader search/repository mutation parity**, and **native in-app video playback**.

## 12-item parity checklist

| # | Requested parity item | Status | Current reality | Primary evidence |
|---|---|---|---|---|
| 1 | Verify full PTR sync + persists / does not re-download / how to know when done | **Done** | Anonymous PTR sync-in persists state, reuses already-stored update artifacts when present, tracks processed definition/content counts, and sets `IsComplete` only when idle with no pending download/process work and no last error. The branch now also has direct test coverage for completed-sync persistence across restart, artifact reuse, retry wakeup/backoff, shutdown lease cleanup, and PTR-applied mapping searchability after reopen. | `internal/ptrsync/manager.go`, `internal/db/hydrusdb/ptrsync.go`, `internal/app/app_test.go`, `internal/ptrsync/manager_test.go`, `internal/db/hydrusdb/ptrsync_test.go` |
| 2 | PTR commit tag features for imported files | **Partial** | Pending mapping staging and commit upload are real. The current path supports narrow PTR pending-add workflows and remote commit of those pending mappings. It is not yet broad Hydrus repository parity: no full petition/review-services flow, no broader mutation model, no richer pending-state UI. | `internal/api/httpapi/ptr_pending.go`, `internal/ptrsync/client.go`, `internal/db/hydrusdb/ptrsync.go`, `internal/core/ptrsync/types.go` |
| 3 | Full image and video watcher on double click, native to Fyne | **Partial** | Double-click opens a resizable in-app watcher window for still images. The watcher now supports arrow-key and mouse-wheel navigation across the current gallery ordering and keeps the selected tile visibly highlighted in the grid. Video is still explicitly not implemented; the app shows a fallback message for video MIME types instead of playing them. | `internal/desktop/fyneapp/tile.go`, `internal/desktop/fyneapp/watcher.go`, `internal/desktop/fyneapp/app.go`, `internal/desktop/fyneapp/tile_test.go`, `internal/desktop/fyneapp/watcher_test.go`, `internal/desktop/fyneapp/app_metadata_test.go` |
| 4 | Popup tag editor with autocomplete and tag metadata display | **Done** | The popup loads selected-file metadata, renders current tags, offers local + daemon-backed autocomplete suggestions, stages pending mappings, and commits pending mappings. | `internal/desktop/fyneapp/app.go`, `internal/api/httpapi/tags.go`, `internal/db/hydrusdb/tag_suggestions.go` |
| 5 | Hashes match image tags and artist-first gallery label | **Done** | The desktop metadata/tag lookups are keyed by `file_id`, and gallery labels prioritize `creator:` / `artist:` / `person:` / `studio:` before series/character, then hash fallback. No mismatch bug is evident in the current code path. | `internal/desktop/fyneapp/app.go`, `internal/desktop/fyneapp/app_metadata_test.go` |
| 6 | Hydrus-like tags on the left when an image is selected | **Done** | The left pane contains a dedicated selected-file tags section populated from daemon metadata and rendered as rich text. | `internal/desktop/fyneapp/app.go` |
| 7 | Search bar on left / autocomplete for tags | **Done** | The left search bar exists, daemon-backed autocomplete exists, and the Fyne grid now uses the daemon-backed `/v1/library/search` path for explicit/exact tag queries across local files + PTR-applied mappings. Unsupported non-tag text and unsupported `system:` terms are currently ignored rather than applied as loaded-grid fallback filters. | `internal/desktop/fyneapp/app.go`, `internal/api/httpapi/thin_client.go`, `internal/desktop/daemonclient/client.go`, `internal/api/httpapi/tags.go`, `internal/db/hydrusdb/tag_suggestions.go` |
| 8 | Image preview correctly resizable | **Done** | The Fyne shell uses split panes, min-size guards, and a scrollable preview region; the selected preview resizes sanely. | `internal/desktop/fyneapp/app.go`, `internal/desktop/fyneapp/minsize_box.go` |
| 9 | System predicate support (`favorite`, `size`, `resolution`, etc.) | **Partial** | `system:` predicates for `size`, `width`, `height`, `favorite`, and `resolution` are daemon-backed. Others like `local`, `trashed`, and `deleted` are still unsupported, but they are now ignored rather than applied as UI-local filters over the loaded result set. | `internal/db/hydrusdb/browse.go`, `internal/api/httpapi/thin_client.go`, `internal/desktop/fyneapp/app.go` |
| 10 | Sorting by date, name, size, etc. | **Partial** | Newest, oldest, and size-based sorting are daemon-backed and use server-side query sorting. Name/label sorting is currently handled as local-grid behavior. | `internal/api/httpapi/thin_client.go`, `internal/desktop/fyneapp/app.go` |
| 11 | Colored tags based on type | **Done** | Hydrus-like namespace colors are implemented for creator, series, character, unnamespaced tags, and fallback namespaced tags. | `internal/desktop/fyneapp/theme.go`, `internal/desktop/fyneapp/app.go`, `internal/desktop/fyneapp/app_metadata_test.go` |
| 12 | Manual DB integrity checks | **Done** | There is a daemon endpoint and a desktop menu action for SQLite integrity checks. | `internal/api/httpapi/db_integrity.go`, `internal/app/metadata_store.go`, `internal/desktop/fyneapp/app.go` |

## What the older docs get wrong

- `docs/STATUS.md`, `docs/README.md`, and `docs/PARITY_ROADMAP.md` underreport the current API surface. The router now includes endpoints that those files do not fully reflect:  
  - `GET /v1/tags/autocomplete`  
  - `POST /v1/import/url`  
  - `POST /v1/import/upload`  
  - `POST /add_tags/add_tags`  
  - `POST /manage_database/integrity_check`  
  - `POST /manage_services/commit_pending`
- Older docs that say **PTR apply is still missing** are stale relative to current code. The live PTR path now downloads, persists, and applies definition and mapping updates.
- Older docs that talk about PTR update "files" like ordinary imported media are stale or misleading relative to current code. The daemon now tracks repository update bundles, pending download/apply bundle counts, and separate local caught-up vs up-to-date state.
- Older roadmap text that says there are **no public mutation endpoints for tags/commits** is now stale. The more accurate statement is: mutation endpoints exist, but they are still **narrow PTR pending-add / commit-only** surfaces.
- `docs/CURATION_COCKPIT_BASELINE.md` is closer to the current desktop state than older `STATUS.md`, but even that file should still be treated as a branch snapshot rather than the final source of truth.

## Immediate written TODOs on this branch

- Broaden daemon-side search from the current tag-first local-file slice into fuller Hydrus-like server-side search semantics: complex union/negation/grouping logic, remaining `system:` predicates (`favorite`, `resolution`, `local`, `trashed`, `deleted`), and name-based sorting.
- Broaden the desktop search path from the current explicit/exact-tag and supported-system daemon slice into fuller daemon-first search semantics as the backend grows; unsupported terms are currently ignored rather than handled locally.
- Keep the now-verified still-image watcher path stable, and treat native in-app video playback as the remaining media-viewer gap rather than reopening still-image QA without a concrete regression.

## Ruthless 1.5-week critical path

### Day 0 decision: choose the video strategy immediately

This is the highest-risk item on the list.

- **If native in-app video playback is a hard requirement for near parity**, decide now on the integration path and accept the dependency cost. Fyne core does not give video playback for free.
- **If near parity can ship with still-image watcher + explicit video limitation**, do that deliberately and spend the week on search and repository workflows instead.

If this decision slips, item **#3** will slip.

### Phase A — Days 1–4: daemon-side search foundation

**Goal**: eliminate the “recent-only” ceiling.

- Expand daemon-side search beyond the current tag/size/resolution/favorite foundation into the remaining predicate and sort set: `local`, `trashed`, `deleted`, and name/label sorting.
- Support complex union and negation logic plus term groupings.
- Build on the new desktop daemon-search wiring so the current explicit/exact-tag path grows into fuller daemon-first search instead of the current tag-search + local-fallback hybrid.

**Why this is first**: it unlocks items **#7, #9, and #10** in one slice and moves the desktop from “recent browser” to “real Hydrus workspace”.

### Phase B — Days 4–7: broaden repository/tag workflow parity

**Goal**: turn the current narrow PTR pending-add path into a workflow that feels real.

- Keep the existing pending staging + commit path, but widen it with better visibility and verification.
- Ensure imported files can reliably stage PTR-bound mappings through both `file_id` and hash-driven paths.
- Surface pending counts / commit results clearly in the PTR UI so the operator can see what is queued and what was pushed.
- Decide whether petitions are required for “near parity” in this 1.5-week window. If yes, add them here. If no, explicitly defer them and focus only on add-mapping parity.

**Why this is second**: it directly advances item **#2** and reduces the biggest remaining mismatch between “I can tag locally” and “I can contribute through the PTR”.

### Phase C — Days 7–9: media watcher parity decision execution

**Goal**: close item **#3** as far as the chosen strategy allows.

- If the decision was **native video inside Fyne**, implement the chosen integration path and prove one supported video format end-to-end.
- If the decision was **defer native video**, keep the still-image watcher solid, polish zoom/fit/resizing behavior if needed, and make the explicit limitation text intentional rather than accidental.

**Why this is third**: still-image watcher parity is already present; video is the only missing part of the media-viewer slice.

### Phase D — Days 9–11: end-to-end hardening and doc freeze

**Goal**: verify that the near-parity slices actually hold together.

- Run manual PTR validation against a real remote and a real local library.
- Verify that search + sort + predicate flows behave correctly against daemon-backed results, not just loaded recent items.
- Re-test tag staging / commit from the desktop against imported files.
- Re-test still-image watcher and the chosen video behavior on the actual target environment.
- After code freeze, update `STATUS.md`, `PARITY_ROADMAP.md`, and `README.md` to remove stale false-gap claims.

## What not to spend the next 1.5 weeks on

- No downloader/parser/subscription parity.
- No broad UI redesign just to look more like PyQt Hydrus.
- No large architecture refactors.
- No advanced review-services pages beyond what is needed to validate the near-parity workflow.

## Bottom line

If the goal is **near parity in 1.5 weeks**, the shortest path is:

1. **broaden the current daemon-side search slice beyond tag-first desktop wiring**,
2. **broaden the PTR contribution workflow**,
3. **make the video-watcher decision explicit**.

Everything else on the user’s original list is already done or close enough to done that it should not outrank those three slices.
