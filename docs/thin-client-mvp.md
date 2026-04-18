# thin desktop client prototype

## Goal

Build a simple multi-platform desktop prototype that validates `hydrusd`
recent-browse, selected-file preview, add, and trash workflows against a real
Hydrus library before PTR work begins.

This is **not** a full Hydrus desktop parity effort yet.

The first client should stay closer to the shape of
`image-tests/comfyui-image-browser.png` than to the dense multi-pane Hydrus
workstation UI in `image-tests/hydrus.png`.

## Chosen stack

- **Fyne in Go**
- `hydrusd` remains the owner of SQLite, `client_files`, imports, trash, and later PTR

Why this stack:

- avoids JS/TS and Python as the main UI runtime
- avoids introducing a C++/CMake toolchain for the first prototype
- keeps the thin client in the same language/tooling stack as the daemon
- is sufficient for a dark, native-feeling prototype that proves the daemon contract

## First prototype slice

The first prototype iteration is intentionally narrow:

- connect to the local daemon
- browse recent local files in a dense thumbnail grid
- preview selected JPEG/PNG/GIF originals through the daemon's `/v1/files/content` endpoint
  - keep preview bounded for thin-client responsiveness (currently 16 MiB payload,
    8192px maximum dimension, 16,000,000 decoded pixels)
- show selected-file metadata in a narrow sidebar
- add one local file through the daemon's local-path import endpoint
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
- `POST /v1/files/trash`
  - moves one file into the local trash domain through the public thin-client contract

Important note:

- the current import endpoint is **daemon-local path based**
- the prototype therefore assumes the selected file path is meaningful from the
  `hydrusd` host's point of view
- the prototype can now be tested against either an existing Hydrus client
  bundle or an empty/missing `HYDRUS_GO_DB_DIR` that `hydrusd` bootstraps on
  first start
- fresh bootstrap runs on the daemon host, so
  `HYDRUS_GO_BOOTSTRAP_HYDRUS_ROOT` or `--bootstrap-hydrus-root` must point at
  an upstream Hydrus Python checkout on that host
- immediate thumbnail availability should currently only be expected for JPEG,
  PNG, and GIF still-image imports; other media types may browse without a
  thumbnail until broader generation support lands

## Prototype UI shape

Minimum window structure:

- compact top action bar: connect, refresh, add file, trash selected
- narrow left sidebar:
  - daemon connection state
  - selected-file preview for supported JPEG/PNG/GIF image types
  - selected-file metadata
  - last action/result text
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
- performance before PTR synchronization work begins
