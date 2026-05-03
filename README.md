# hydrus-go

`hydrus-go` is a headless Go reimplementation of the Hydrus client core.

This project is being built incrementally alongside the original Python codebase.
The goal is not a line-by-line translation. The goal is a stable, testable,
API-first daemon that preserves Hydrus's local-first media library model.

The intended deployment model is a **single daemon/backend** that owns the
Hydrus SQLite database, managed `client_files` storage, and background work.
Real clients should connect to that daemon over stable APIs—first locally and
over the LAN, with broader remote access considered later.

## Current status

This repository currently provides the following early migration slices:

- standalone Go module and daemon entrypoint
- environment-based configuration
- structured logging
- graceful shutdown
- initial local HTTP API
- DB-backed read-only Hydrus client bundle opening
- Hydrus-compatible service catalog foundation
- access key and session key flow for the initial compatibility endpoints
- initial DB-backed file metadata compatibility, including first full/default metadata slices for both non-tag fields and a first daemon-served tags payload
- a first daemon-side search foundation for AND-tag queries, `system:` predicates (`size`, `width`, `height`, `favorite`, `resolution`), and server-side sort modes (`import_oldest`, `size_desc`, `size_asc`)
- an internal prepared-file import checkpoint that composes managed placement with serialized DB writes
- first thin-client browse/asset endpoints for recent local files, originals, and thumbnails
- public local-path import, trash, and tag-mutation endpoints for thin-client-driven library testing
- daemon-owned anonymous PTR sync foundations, remote snapshot persistence, real `/update` download plus local repository-update registration, existing-DB restart/continuation handling, batched local repository-update finalization, and daemon status APIs that now distinguish local backlog completion from true up-to-date state
- real-time PTR pending-count visibility and commit support across the daemon, daemonclient, and desktop prototype
- an initial Fyne-based desktop prototype for `hydrusd`, including selected JPEG/PNG/GIF original preview, incremental recent loading, a more resizable split-shell layout, daemon-backed search/sorting, and PTR status/manual sync/pending-count visibility
- real `hydrusd --listen host:port` runtime overrides for temporary LAN testing
- explicit Linux/Windows desktop build targets, including a Windows GUI-subsystem executable for Explorer launches
- a native-Go fresh Hydrus client bundle bootstrap for empty or missing DB directories, with plain `hydrusd` now seeding `./db` by default and verified end-to-end through live smoke testing

Project notes live in:

- [`docs/STATUS.md`](docs/STATUS.md) — what is done, active, and next
- [`docs/PARITY_ROADMAP.md`](docs/PARITY_ROADMAP.md) — prioritized parity gaps and next milestone order
- [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) — current system shape
- [`docs/DECISIONS.md`](docs/DECISIONS.md) — important architectural decisions
- [`docs/thin-client-mvp.md`](docs/thin-client-mvp.md) — first desktop client scope and daemon contract

Machine-readable API reference:

- [`openapi.json`](openapi.json) — OpenAPI 3.1 description of the currently implemented HTTP surface for Swagger/ReDoc-style tooling

## Implemented endpoints

- Public:
  - `GET /`
  - `GET /healthz`
  - `GET /api_version`
- Protected:
  - `GET /verify_access_key`
  - `GET /session_key`
  - `GET /get_services`
  - `GET /get_service`
  - `GET /get_files/file_metadata`
  - `GET /service/ptr/status`
  - `GET /v1/library/recent`
  - `GET /v1/library/search`
  - `GET /v1/files/content`
  - `GET /v1/files/thumbnail`
  - `GET /v1/tags/autocomplete`
  - `GET /manage_services/pending_counts`
  - `POST /service/ptr/sync`
  - `POST /v1/files/trash`
  - `POST /v1/import/local_file`
  - `POST /v1/import/url`
  - `POST /v1/import/upload`
  - `POST /add_tags/add_tags`
  - `POST /manage_services/commit_pending`
  - `POST /manage_database/integrity_check`

Protected endpoints accept either of these credentials as headers:

- `Hydrus-Client-API-Access-Key: <64-char hex>`
- `Hydrus-Client-API-Session-Key: <64-char hex>`

For compatibility with the current thin-client/testing surface, the same names
may also be sent as query parameters. Header credentials remain the preferred
form for both GET and POST requests.

These endpoints are the first compatibility-oriented slices of the Hydrus Client
API. They are intentionally narrow and are meant to establish a stable daemon
foundation before broader database, import, storage, and search behavior are
added.

## DB-backed daemon mode

If `HYDRUS_GO_DB_DIR` points at a valid Hydrus client database directory, the
daemon will open a read bundle and attempt to open a separate writable bundle.
When a readable bundle is available, these endpoints switch to live DB-backed
behavior:

- `GET /get_services`
- `GET /get_service`
- `GET /get_files/file_metadata`
- `GET /service/ptr/status`
- `GET /v1/library/recent`
- `GET /v1/library/search`
- `GET /v1/files/content`
- `GET /v1/files/thumbnail`
- `GET /v1/tags/autocomplete`
- `GET /manage_services/pending_counts`

When the writable bundle is also available, these mutation/status endpoints use
the live Hydrus bundle too:

- `POST /service/ptr/sync`
- `POST /v1/files/trash`
- `POST /v1/import/local_file`
- `POST /v1/import/url`
- `POST /v1/import/upload`
- `POST /add_tags/add_tags`
- `POST /manage_services/commit_pending`
- `POST /manage_database/integrity_check`

Expected bundle files today:

- `client.db`
- `client.master.db`
- `client.caches.db`
- `client.mappings.db`
- optional: `client.temp.db`

Implementation notes for this slice:

- uses `modernc.org/sqlite`
- uses one dedicated connection per bundle so attached DB aliases remain stable
- keeps the read bundle in `PRAGMA query_only = ON`
- writable bundle opens enable SQLite WAL on the main and attached DBs, so `*.db-wal` and `*.db-shm` sidecars are expected while `hydrusd` is running; these are SQLite runtime artifacts, not extra logical Hydrus databases
- uses a separate writable bundle for public local-path imports and trash writes when writable access is available
- degrades safely to read-only daemon mode when the writable bundle cannot be opened
- expands `GET /get_files/file_metadata` in safe read-only vertical slices rather than attempting full parity at once

## Fresh first-start bundle bootstrap

`hydrusd` can now create a fresh canonical client bundle in Go instead of
requiring an existing library bundle.

Plain `hydrusd` now defaults to bootstrapping a fresh canonical bundle into
`./db` when no DB directory is configured.

You can override that behavior with:

- `HYDRUS_GO_DB_DIR=/path/to/new/or/existing/db`
- `HYDRUS_GO_ENABLE_FRESH_CLIENT_BOOTSTRAP=true|false` or
  `--bootstrap-fresh-client[=true|false]`
- optional `HYDRUS_GO_BOOTSTRAP_TIMEOUT` or `--bootstrap-timeout`

If no DB directory is set and bootstrap remains enabled, `hydrusd` defaults the
bundle location to `./db` relative to the current working directory.

Bundle-state behavior today:

- missing DB dir: created and bootstrapped by default unless bootstrap is explicitly disabled
- empty DB dir: bootstraps a fresh canonical bundle by default unless bootstrap is explicitly disabled
- existing valid bundle: bootstrap is skipped and the bundle is opened normally
- partial bundle: startup fails; `hydrus-go` does not repair or overwrite it
- non-empty dir without a canonical bundle: startup fails; fresh bootstrap only
  runs against an empty dir

One concrete first-start example:

```bash
./bin/hydrusd
```

That command now seeds the bundle into `./db` by default. With that layout,
managed thumbnails live in `./thumbnails` while the bootstrapped bundle and
current managed `client_files` root stay under `./db`.

Platform notes for this bootstrap path:

- the current native bootstrap seeds a minimal empty bundle that is sufficient
  for current service discovery, recent browse, import, and trash flows
- fresh native bundle service-list responses now also include `downloader tags`
  and `favourites`
- `downloader tags` expands the grouped `local_tags` discovery bucket, while
  `favourites` stays visible through `services` / `services_v2` like Hydrus's
  rating services rather than introducing a new grouped bucket
- the seeded `favourites` service now carries a real Hydrus-style rating
  dictionary so `services` / `services_v2` expose the expected star-shape and
  colour fields on first start
- it also creates `client.temp.db`, the managed `client_files` root, default
  client-files storage metadata, a `version` row seeded to the current Hydrus
  compatibility target, and a small set of hidden built-ins (`deleted from
  anywhere`, `local notes`, `client api`)
- it does not yet claim full upstream Hydrus bootstrap parity

If you are updating older shell scripts or service units:

- `HYDRUS_GO_ENABLE_PYTHON_FRESH_CLIENT_BOOTSTRAP` was renamed to
  `HYDRUS_GO_ENABLE_FRESH_CLIENT_BOOTSTRAP`
- `HYDRUS_GO_BOOTSTRAP_PYTHON` and `HYDRUS_GO_BOOTSTRAP_HYDRUS_ROOT` were removed
  because the first-start bootstrap no longer shells out to Python
- `--bootstrap-python` and `--bootstrap-hydrus-root` were removed for the same
  reason; keep using `--bootstrap-fresh-client` and `--bootstrap-timeout`

## Bootstrap auth flow

If `HYDRUS_GO_ACCESS_KEY` is not set, the daemon generates a bootstrap access
key on startup. That is convenient for local interactive development, but for
repeatable runs you should set `HYDRUS_GO_ACCESS_KEY` explicitly.

Example flow:

```bash
# public endpoint
curl http://127.0.0.1:45869/api_version

# verify access with a configured or generated access key
curl \
  -H "Hydrus-Client-API-Access-Key: $HYDRUS_KEY" \
  http://127.0.0.1:45869/verify_access_key

# mint a session key
curl \
  -H "Hydrus-Client-API-Access-Key: $HYDRUS_KEY" \
  http://127.0.0.1:45869/session_key

# use the returned session key for follow-up calls
curl \
  -H "Hydrus-Client-API-Session-Key: $HYDRUS_SESSION" \
  http://127.0.0.1:45869/get_services
```

Current session behavior:

- sessions are stored in memory only
- sessions expire after 24 hours of inactivity
- sessions are reset when the daemon restarts

## Current milestone limits

This is still a migration milestone, not feature parity.

Important current limitations:
- search is currently a narrow slice focused on AND-tags, daemon-backed `system:` predicates (`size`, `width`, `height`, `favorite`, `resolution`), and server-side sort modes; complex union/negation logic plus other system predicates are still pending daemon-side support, and unsupported desktop terms are currently ignored rather than applied as local fallback filters
- the desktop grid now uses client-local thumbnail generation from daemon-served originals instead of fetching daemon thumbnail bytes over the LAN; unsupported or oversized originals may still show no preview
- PTR sync now supports definitions/content application and pending mapping staging/commit, and daemon status now distinguishes local backlog completion from true up-to-date state, but broader petition/review-services flows are still missing
- selected-file original preview currently supports JPEG/PNG/GIF only and is intentionally bounded to 16 MiB payloads, 8192px maximum dimension, and 16,000,000 decoded pixels
- selected-file preview is cached in memory, but refresh/reconnect cycles still redownload the same original over LAN while testing
- `GET /get_files/file_metadata` parity is still incomplete; this slice does not yet implement exact thumbnail-dimension parity
- import-time still-image enrichment is currently bounded to the Go JPEG/PNG decode path; animated-media/blurhash parity is still pending
- no public batch import flow yet
- no public permanent delete flow yet
- no native in-app video playback; video files show a placeholder message in the watcher
- full downloader/parser/subscription parity is still missing


The point of this slice is to lock down daemon startup, auth, DB bundle access,
and early API contracts before the deeper Hydrus client core is ported.

## Backend model

`hydrus-go` is **not** targeting a direct desktop-app rewrite first.

Instead:

- the daemon is the source of truth
- the daemon owns SQLite and `client_files`
- clients talk to the daemon through APIs
- local-only remains the default security posture
- LAN access is an explicit supported direction
- the legacy Hydrus Server is not a migration target

## Scope direction

The migration direction is:

- headless local daemon first
- Client API compatibility where it helps real tools and clients
- preserve Hydrus's internal managed file store model
- skip the legacy Hydrus Server

## Running locally

```bash
make run
```

The daemon defaults to `127.0.0.1:45869`, matching Hydrus's default Client API
port.

For temporary LAN testing, use:

```bash
make run-lan
./bin/hydrusd --listen 0.0.0.0:5555
```

The `--listen` runtime flag overrides `HYDRUS_GO_LISTEN_ADDR` for that
invocation and explicitly permits the requested bind address, so
`0.0.0.0:5555` works without separately exporting
`HYDRUS_GO_ALLOW_NON_LOCAL_CONNECTIONS=true`. You can also override the listen
address through the Makefile helper, for example:

```bash
make run-lan LAN_LISTEN_ADDR=0.0.0.0:9999
```

If no API access key is configured, one will be generated on startup and written
to the daemon logs.

To run in DB-backed daemon mode, also set:

```bash
export HYDRUS_GO_DB_DIR=/path/to/hydrus/db
```

If you do not already have a Hydrus bundle, plain `hydrusd` now boots `./db`
automatically. To place the bundle somewhere else, point `HYDRUS_GO_DB_DIR` at
a new or empty directory and launch normally or with an explicit bootstrap
override:

```bash
./bin/hydrusd \
  --listen 0.0.0.0:5555
```

When the configured Hydrus bundle is writable, `hydrusd` will also enable the
public import/trash mutation paths. If the writable bundle cannot be opened, the
daemon degrades safely to read-only behavior.

Fresh daemon-local imports now also attempt best-effort managed thumbnail
generation for JPEG, PNG, and GIF still images so the prototype grid can show
immediate previews. Other formats may still import successfully without a
thumbnail.

## Fyne prototype

The first desktop client is a thin Fyne prototype that connects to `hydrusd`.
It is deliberately closer to `image-tests/comfyui-image-browser.png` than to the
full Hydrus workstation UI, and it exists to validate daemon/database browse,
queued import, trash, and bounded selected-file original preview behavior
through a real local UI while the layout gradually moves toward a more
Hydrus-like multi-pane shell.

Run it with:

```bash
go run -tags fyne ./cmd/hydrus-desktop
```

Or use:

```bash
make run-desktop
```

To build desktop binaries instead of running in-place:

```bash
make build-desktop-linux
make build-desktop-windows
```

Notes:

- the desktop prototype talks to `hydrusd`; it never touches SQLite or
  `client_files` directly
- the main shell now uses a more breathable split layout, and the empty selected-preview state no longer collapses into vertical placeholder text
- `Network > PTR Sync` now reports repository update bundle counts, local caught-up vs up-to-date state, pending download/process bundle backlogs, and separate effective-progress vs raw-network-fetch metrics
- current import testing can be driven through a single-file picker, a folder
  picker, or drag-and-drop into the desktop window; queued items are uploaded
  sequentially through the daemon's remote-safe upload endpoint
- the left import pane now includes queue review controls for retrying failed
  items, removing selected entries, and pruning finished work without touching
  the daemon/API contract
- the queue can be staged while disconnected and will start or resume processing
  when a usable daemon connection is available; the same pane also exposes a
  full `Clear Queue` reset when processing is idle
- for cross-machine LAN testing, point the connect dialog at the daemon host
  (for example `http://<linux-host>:45869`) rather than the default localhost
  URL
- `make build-desktop` keeps the old behavior of building a desktop binary for
  the current host platform
- the current Linux desktop build depends on native windowing/OpenGL headers in
  addition to the Go toolchain
- `make build-desktop-linux` now emits an explicit `linux/amd64` build by
  default and uses `LINUX_CC` (default: `gcc`); if you override
  `LINUX_GOARCH`, provide a matching Linux toolchain via `LINUX_CC`
- the Windows desktop build uses the MinGW cross-compiler configured by
  `WINDOWS_CC` and defaults to `windows/amd64`; override the target
  architecture with `WINDOWS_GOARCH` and use a matching compiler as needed;
  `make build-desktop-windows` links the executable as a GUI app so launching it
  from Explorer does not spawn an extra terminal window
- the current environment in this repo can type-check the Fyne code via WASM,
  but a native Linux build still requires the usual X11/GL development packages
- `make check-desktop` is the canonical non-native validation path for the tagged
  desktop code in environments that do not have those headers installed; it now
  writes `bin/hydrus-desktop.wasm` instead of a misleading repo-root binary

## Developer loop

```bash
make fmt
make test
make build
make run
make build-desktop
make build-desktop-linux
make build-desktop-windows
make check-desktop
make run-desktop
```

This bootstrap currently targets the Go toolchain declared in `go.mod`
(`go 1.22`).

## Environment variables

- `HYDRUS_GO_LISTEN_ADDR` (default: `127.0.0.1:45869`)
- `HYDRUS_GO_DB_DIR` (optional path to a Hydrus client DB directory; when this is unset and bootstrap remains enabled, `hydrusd` defaults to `./db`)
- `HYDRUS_GO_ENABLE_FRESH_CLIENT_BOOTSTRAP` (optional explicit override; plain `hydrusd` currently defaults to enabled bootstrap)
- `HYDRUS_GO_BOOTSTRAP_TIMEOUT` (default: `2m`)
- `HYDRUS_GO_ENABLE_PTR_SYNC` (default: `false`; opt-in only, matching Hydrus' “never connect until you tell it to” stance)
- `HYDRUS_GO_PTR_HOST` (default: `ptr.hydrus.network`)
- `HYDRUS_GO_PTR_PORT` (default: `45871`)
- `HYDRUS_GO_PTR_ACCESS_KEY` (default: documented shared public read-only PTR key)
- `HYDRUS_GO_PTR_SERVICE_NAME` (default: `public tag repository`)
- `HYDRUS_GO_ACCESS_KEY` (optional 64-char hex access key)
- `HYDRUS_GO_ACCESS_NAME` (default: `hydrus-go`)
- `HYDRUS_GO_LOG_LEVEL` (default: `info`)
- `HYDRUS_GO_SHUTDOWN_TIMEOUT` (default: `10s`)
- `HYDRUS_GO_ALLOW_NON_LOCAL_CONNECTIONS` (default: `false`)
- `HYDRUS_GO_ENABLE_CORS` (default: `false`)

## Security notes

- The daemon is local-only by default.
- `HYDRUS_GO_ALLOW_NON_LOCAL_CONNECTIONS=true` is an explicit opt-in.
- CORS is disabled by default.
- PTR sync is opt-in and currently mirrors Hydrus anonymous PTR transport parity:
  - remote PTR TLS accepts self-signed certificates
  - login/session redirects are refused so the shared `Hydrus-Key` header is not forwarded
  - PTR responses are bounded both before and after Hydrus zlib decompression
- `HYDRUS_GO_ENABLE_CORS=true` is intentionally broad in this bootstrap slice
  and should currently be treated as a development convenience rather than a
  hardened browser security policy.

## Immediate next milestones

- iterate on the Fyne prototype's preview caching, reconnect behavior, metadata ergonomics, and remaining visual polish now that the first connect/browse/add/trash/original-preview loop, incremental loading, and PTR status/manual sync are wired
- run real Windows-over-LAN smoke tests against a live `hydrusd` + Hydrus library and tighten any failures quickly
- validate add/trash latency and recent-grid refresh behavior on a real Hydrus library through the prototype
- broaden daemon-owned PTR sync progress and job control for the thin client beyond the current status + manual trigger
- broaden default/full metadata parity for `GET /get_files/file_metadata`
