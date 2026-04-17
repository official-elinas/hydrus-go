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

This repository currently provides two early slices:

- standalone Go module and daemon entrypoint
- environment-based configuration
- structured logging
- graceful shutdown
- initial local HTTP API
- DB-backed read-only Hydrus client bundle opening
- Hydrus-compatible service catalog foundation
- access key and session key flow for the initial compatibility endpoints
- initial DB-backed file metadata compatibility, including a first full/default non-tag slice
- an internal prepared-file import checkpoint that composes managed placement with serialized DB writes

Project notes live in:

- [`docs/STATUS.md`](docs/STATUS.md) — what is done, active, and next
- [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) — current system shape
- [`docs/DECISIONS.md`](docs/DECISIONS.md) — important architectural decisions

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

Protected endpoints accept either of these credentials:

- `Hydrus-Client-API-Access-Key: <64-char hex>`
- `Hydrus-Client-API-Session-Key: <64-char hex>`

For the currently implemented GET endpoints, the same names may also be sent as
query parameters.

These endpoints are the first compatibility-oriented slices of the Hydrus Client
API. They are intentionally narrow and are meant to establish a stable daemon
foundation before broader database, import, storage, and search behavior are
added.

## Read-only DB-backed mode

If `HYDRUS_GO_DB_DIR` points at an existing Hydrus client database directory,
the daemon will open the client bundle read-only and switch these endpoints to
live DB-backed behavior:

- `GET /get_services`
- `GET /get_service`
- `GET /get_files/file_metadata`

Expected bundle files today:

- `client.db`
- `client.master.db`
- `client.caches.db`
- `client.mappings.db`
- optional: `client.temp.db`

Implementation notes for this first slice:

- uses `modernc.org/sqlite`
- forces a single SQLite connection so attached DB aliases remain stable
- opens the bundle read-only and enables `PRAGMA query_only = ON`
- does not write new file identifiers or mutate the Hydrus DB
- expands `GET /get_files/file_metadata` in safe read-only vertical slices rather than attempting full parity at once

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

This is still an early migration milestone, not feature parity.

Important current limitations:

- DB-backed daemon/runtime mode is still read-only only
- an internal prepared-file import checkpoint now exists, but there is still no public import/write API
- deterministic managed `client_files` path resolution and internal file placement are now composed with minimal DB writes for internal round-trip testing only
- `GET /get_files/file_metadata` currently supports:
  - `only_return_identifiers=true`
  - `only_return_basic_information=true`
  - optional `include_blurhash=true` in basic mode
  - default/full read-only non-tag metadata for:
    - `file_services`
    - `time_modified` and `time_modified_details`
    - `time_archived`
    - `is_inbox`, `is_local`, `is_trashed`, `is_deleted`
    - `known_urls`
    - `pixel_hash`
    - `ipfs_multihashes`
    - `has_transparency`, `has_exif`, `has_human_readable_embedded_metadata`, `has_icc_profile`
  - `include_milliseconds=true` for the implemented full-mode timestamp fields
  - optional `include_services_object=false`
- full/default `GET /get_files/file_metadata` parity is still incomplete; this slice does not yet implement:
  - `tags`
  - `ratings`
  - `file_viewing_statistics`
  - `include_notes=true`
  - `detailed_url_information=true`
  - exact thumbnail-dimension parity
- `create_new_file_ids=true` is intentionally rejected in read-only mode
- no public DB-backed local import flow yet
- no hashing/sniffing import pipeline yet
- no search/tagging engine yet
- no downloader/subscription/parsing system yet

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

If no API access key is configured, one will be generated on startup and written
to the daemon logs.

To run in DB-backed read-only mode, also set:

```bash
export HYDRUS_GO_DB_DIR=/path/to/hydrus/db
```

## Developer loop

```bash
make fmt
make test
make build
make run
```

This bootstrap currently targets the Go toolchain declared in `go.mod`
(`go 1.22`).

## Environment variables

- `HYDRUS_GO_LISTEN_ADDR` (default: `127.0.0.1:45869`)
- `HYDRUS_GO_DB_DIR` (optional path to a Hydrus client DB directory)
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
- `HYDRUS_GO_ENABLE_CORS=true` is intentionally broad in this bootstrap slice
  and should currently be treated as a development convenience rather than a
  hardened browser security policy.

## Immediate next milestones

- extend the internal prepared-file import checkpoint into a broader public import flow
- add hashing/sniffing and thumbnail work on top of the new managed-placement + DB-write path
- PTR integration so imported files can participate in repository tag/update flows
- broader default/full metadata parity for `GET /get_files/file_metadata`
- search/tagging model on top of imported and PTR-synced data
- broader Client API compatibility
