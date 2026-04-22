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
- preview selected JPEG/PNG/GIF originals through the daemon's `/v1/files/content` endpoint
  - keep preview bounded for thin-client responsiveness (currently 16 MiB payload,
    8192px maximum dimension, 16,000,000 decoded pixels)
- show selected-file metadata in a left-side details pane, including daemon-served tag groups for the selected file
- show daemon-owned PTR sync status and a manual sync action in a dedicated popup launched from the top-level `Network` menu, mirroring a small part of Hydrus's review-services visibility without moving PTR network logic into the client
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

The thin-client-specific daemon endpoints for this prototype are:

- `GET /v1/library/recent?offset=<n>&limit=<n>`
  - returns recent local files for the thumbnail grid
- `GET /v1/files/content?file_id=<id>`
  - streams the managed original file
- `GET /v1/files/thumbnail?file_id=<id>`
  - streams the managed thumbnail when present
  - fresh JPEG/PNG/GIF imports now attempt to create a managed thumbnail immediately
- `POST /v1/import/local_file`
  - imports one daemon-local file path through the public thin-client contract
  - supported JPEG/PNG/GIF still-image imports now attempt best-effort thumbnail generation after placement
- `POST /v1/import/upload`
  - imports one uploaded file through the public thin-client contract
  - this is the preferred path for the desktop client because it remains safe when `hydrusd` runs on another host or over LAN
- `POST /v1/files/trash`
  - moves one file into the local trash domain through the public thin-client contract
- `GET /service/ptr/status`
  - returns daemon-owned anonymous PTR sync state for polling
- `POST /service/ptr/sync`
  - starts one daemon-owned anonymous PTR sync pass and returns the immediate status payload

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
- immediate thumbnail availability should currently only be expected for JPEG,
  PNG, and GIF still-image imports; other media types may browse without a
  thumbnail until broader generation support lands
- the current PTR slice is still on-demand only; clients can poll status and trigger a sync, and the daemon now downloads/registers repository update blobs, but automatic scheduling, richer job control, and definition/content application remain later work

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
    - selected-file preview for supported JPEG/PNG/GIF image types
    - selected-file tag metadata
    - selected-file metadata details
- `Network > PTR Sync` opens a dedicated popup window for daemon-owned PTR status, refresh, and manual sync actions
- dominant center thumbnail grid
- bottom status line

What this prototype intentionally does **not** include yet:

- Hydrus-style tag trees or search panes
- dense multi-pane workstation layout
- permanent preview/export focus
- broader page/service management UI

## Validation purpose

The prototype exists to validate:

- daemon/client contract clarity
- SQLite browse responsiveness after add/trash mutations
- managed `client_files` correctness under daemon ownership
- public import round trips from UI -> daemon -> DB -> browse
- public trash round trips from UI -> daemon -> DB -> browse
- daemon-owned PTR status/trigger behavior before downloaded definitions/content are processed into local tag/query state
