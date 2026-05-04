# thin desktop client prototype

## Goal

Build a simple multi-platform desktop prototype that validates `hydrusd`
recent-browse, selected-file preview, queued remote-safe import, and trash
workflows against a real Hydrus library alongside ongoing PTR backend work.

This is **not** a full Hydrus desktop parity effort yet.

The first client should stay closer to the shape of
`image-tests/comfyui-image-browser.png` than to the dense multi-pane Hydrus
workstation UI in `image-tests/hydrus.png`.

## Chosen stack

- **Fyne in Go**
- `hydrusd` remains the owner of SQLite, `client_files`, imports, trash, and PTR sync

Why this stack:

- avoids JS/TS and Python as the main UI runtime
- avoids introducing a C++/CMake toolchain for the first prototype
- keeps the thin client in the same language/tooling stack as the daemon
- is sufficient for a dark, native-feeling prototype that proves the daemon contract

## First prototype slice

The first prototype iteration is intentionally narrow:

- connect to the local or remote daemon
- browse recent local files in a dense thumbnail grid
  - load additional recent results as the user scrolls, backed by the daemon's existing offset/limit API
- queue imports from:
  - a single-file picker
  - a folder picker
  - drag-and-drop of files or folders into the window
- process queued imports sequentially through the daemon's remote-safe upload endpoint
- review queue state in a dedicated list with retry/remove/prune controls
- keep queue staging usable while disconnected and resume processing when a
  usable daemon connection is restored
- skip unsupported dropped items or unreadable paths with local feedback rather
  than failing the entire queue
- preview selected still images and video poster frames through the daemon's `/v1/files/content` endpoint, using direct decode where possible and local FFmpeg conversion otherwise
  - keep preview bounded for thin-client responsiveness (currently 16 MiB payload,
    8192px maximum dimension, 16,000,000 decoded pixels)
- show selected-file metadata in a left-side details pane, including daemon-served tag groups for the selected file
- show daemon-owned PTR sync status and a manual sync action in a dedicated popup launched from the top-level `Network` menu, including real-time pending-count visibility and commit support
- trash one selected file through the daemon's trash endpoint
- refresh the grid and metadata state after each mutation

This slice is about validating daemon/database behavior first, not building a
general-purpose media manager.

## Thin-client daemon contract

The prototype can already use the existing bootstrap/auth endpoints:

- `GET /healthz`
- `GET /api_version`
- `GET /verify_access_key`
- `GET /session_key`
- `GET /get_services`
- `GET /get_service`
- `GET /get_files/file_metadata`
- `GET /v1/tags/autocomplete`

The thin-client-specific daemon endpoints for this prototype are:

- `GET /v1/library/recent?offset=<n>&limit=<n>`
  - returns recent local files for the thumbnail grid
- `GET /v1/library/search?tags=<tag>&sort_by=<mode>&system_predicates[]=<predicate>`
  - returns daemon-backed local-library search results for explicit/exact tags, supported `system:` predicates, and supported server-side sort modes
- `GET /v1/files/content?file_id=<id>`
  - streams the managed original file
- `GET /v1/files/thumbnail?file_id=<id>`
  - streams the managed thumbnail when present
  - fresh imports now attempt to create a managed thumbnail immediately for the currently supported still-image and FFmpeg-backed media subset
- `POST /v1/import/local_file`
  - imports one daemon-local file path through the public thin-client contract
  - supported still-image/video imports now attempt best-effort thumbnail generation after placement through direct decode or local FFmpeg fallback depending on media type
- `POST /v1/import/url`
  - imports one direct file URL through daemon-owned download/fetch logic
- `POST /v1/import/upload`
  - imports one uploaded file through the public thin-client contract
  - this is the preferred path for the desktop client because it remains safe when `hydrusd` runs on another host or over LAN
- `POST /v1/files/trash`
  - moves one file into the local trash domain through the public thin-client contract
- `GET /service/ptr/status`
  - returns daemon-owned anonymous PTR sync state for polling
- `POST /service/ptr/sync`
  - starts one daemon-owned anonymous PTR sync pass and returns the immediate status payload
- `GET /manage_services/pending_counts`
  - returns locally staged pending PTR mapping counts for the daemon-owned repository service
- `POST /add_tags/add_tags`
  - stages PTR pending-add mappings through the narrow current Hydrus-compatible request shape
- `POST /manage_services/commit_pending`
  - commits currently staged PTR pending-add mappings to the remote repository
- `POST /manage_database/integrity_check`
  - runs a daemon-side SQLite integrity check and returns the current result summary

Important notes:

- the desktop client now prefers the upload endpoint so local file selection and
  drag-and-drop do **not** depend on the path being meaningful from the
  `hydrusd` host's point of view
- the daemon-local path endpoint remains available for daemon-hosted workflows
- the prototype can now be tested against either an existing Hydrus client
  bundle or an empty/missing `HYDRUS_GO_DB_DIR` that `hydrusd` bootstraps on
  first start
- the current first-start bootstrap on this branch is native Go rather than a
  Python checkout/runtime dependency
- immediate thumbnail availability should currently be expected only for the
  formats covered by the current direct-decode/FFmpeg-backed generation slice;
  unsupported or missing-FFmpeg media may still browse without a thumbnail
- the current PTR slice supports pending-add staging, commit upload, pending-count visibility, restart opt-in persistence via the bundle-local `ptrsync` marker, and daemon retry/defer handling for transient `/update` transport failures
- the current desktop PTR wording should be read literally: repository update **bundles** are not ordinary media files; the daemon separately reports pending download bundles, pending process bundles, local caught-up state, true up-to-date state, and raw-network vs effective-progress telemetry
- when the remote PTR is temporarily busy, the daemon now reports retrying state with countdown-friendly status fields instead of silently hiding all retries inside one client request

## Prototype UI shape

Current minimum window structure:

- compact top action bar: connect, refresh, add file, add folder, trash selected
- top menu bar with File / Pages / Database / Network / Services / Help entries
- left Fyne-managed split pane:
  - upper controls/import pane:
    - daemon connection state
    - import queue summary and queue contents
    - queue review controls: retry selected, remove selected, retry failed, clear finished, clear queue
    - last action/result text
  - lower details pane:
  - selected-file preview for supported still images and video poster frames
    - selected-file tag metadata
    - selected-file metadata details
- `Network > PTR Sync` opens a dedicated popup window for daemon-owned PTR status, refresh, and manual sync actions, including bundle progress ratios and up-to-date determination
- the current shell has had a first cleanup pass so the main split panes resize more sanely and the empty selected-preview state no longer collapses its text vertically
- dominant center thumbnail grid
- bottom status line

What this prototype intentionally does **not** include yet:

- a fuller Hydrus workstation-style search page beyond the current daemon-backed tag/predicate/sort slice already wired into the prototype shell
- dense multi-pane workstation layout
- permanent preview/export focus
- broader page/service management UI
- full bidirectional PTR sync (current slice is read-focused with narrow pending-add / commit support)

## Validation purpose

The prototype exists to validate:

- daemon/client contract clarity
- SQLite browse responsiveness after add/trash mutations
- managed `client_files` correctness under daemon ownership
- public import round trips from UI -> daemon -> DB -> browse
- public trash round trips from UI -> daemon -> DB -> browse
- daemon-owned PTR status/trigger behavior including downloaded definitions/content processing into local tag/query state, pending mapping staging, and commit upload foundation
