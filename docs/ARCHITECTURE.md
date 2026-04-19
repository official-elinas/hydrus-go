# hydrus-go architecture

## Current architectural direction

`hydrus-go` is being built as a **headless daemon/backend**.

The daemon is the source of truth for Hydrus state. It owns:

- the SQLite database
- the managed `client_files` storage layout
- background processing and maintenance jobs
- API surfaces used by external clients

UI clients should connect to the daemon over stable APIs rather than directly
touching the database or file store.

## Network model

- local-only is the default operating mode
- LAN-connected clients are an intended near-term target
- broader remote exposure is explicitly later and should only happen with
  deliberate security hardening

## Migration stance

- preserve the original Python project as the reference implementation
- build new Go code only under `hydrus-go/`
- avoid line-by-line translation
- prefer backend/core extraction over UI replication
- preserve Hydrus's managed-library model rather than turning it into a loose file indexer

## What the daemon will eventually own

Over time, the daemon is expected to absorb:

- database access and migrations
- file import and hashing
- managed file storage
- thumbnails and media metadata extraction
- search and tagging behavior
- duplicates and file relationships
- local API compatibility
- downloader/import automation where it makes sense

## What is not a primary target

- a direct first-pass port of the legacy Hydrus desktop UI
- legacy Hydrus Server parity

The migration is centered on daemon ownership of a local library backend, not a
separate reimplementation of legacy server behavior.

## Current implementation reality

Today, the daemon has three concrete layers:

1. a bootstrap runtime layer for config, auth, logging, lifecycle, and HTTP routing
2. a first read-only DB-backed layer for selected Hydrus Client API parity
3. a narrow internal prepared-file import checkpoint that composes managed placement with serialized DB writes

The `hydrusdb` package now also includes the first internal write foundation:

- an explicit writable bundle open path for future import/mutation work
- a serialized `BEGIN IMMEDIATE` transaction runner
- fixture-backed proof of commit, rollback, and queued-write behavior
- the daemon runtime now uses a separate writable bundle for public local-path imports while keeping reads on a read-only bundle

The storage layer now also has a dedicated managed-files package:

- `internal/storage/clientfiles` resolves deterministic managed file and thumbnail paths
- it performs internal-only managed placement with lazy directory creation and no-overwrite publication
- it is intentionally separate from `hydrusdb` so later import code can compose storage placement with DB transactions rather than mixing concerns

The project now also has a small import-composition layer:

- `internal/importing` accepts a caller-prepared local file description
- it derives the managed destination extension from the Hydrus MIME table
- it places the file into managed `client_files`
- it records the minimal Hydrus DB state through `Bundle.RecordPreparedLocalImport(...)`
- it removes a newly placed managed file on DB failure as a best-effort cleanup step
- it now also powers the first public daemon-local import endpoint for the thin client MVP

The DB-backed layer currently:

- optionally opens a real Hydrus client DB bundle via `HYDRUS_GO_DB_DIR`
- optionally bootstraps a fresh canonical client bundle natively in Go before
  Go opens the bundle
- attaches the external DBs on a single SQLite connection
- keeps that connection read-only with `PRAGMA query_only = ON`
- serves DB-backed service discovery
- serves DB-backed `GET /get_files/file_metadata` in:
  - identifier mode
  - basic mode
  - a first default/full non-tag metadata subset

The current full/default metadata subset includes:

- file service membership and timestamps
- aggregate/local/domain modified timestamps
- archived/inbox/local/trash/deleted state
- known URLs
- pixel hash
- IPFS multihashes
- transparency/EXIF/human-readable/ICC metadata flags
- explicit rejection of `include_notes=true` and `detailed_url_information=true`

The current daemon startup flow for bundle-backed mode is:

1. load config and runtime overrides
2. inspect `HYDRUS_GO_DB_DIR`
3. if fresh bootstrap is enabled and the target directory is missing, create it
4. classify the bundle state as one of:
   - `ready`
   - `empty`
   - `partial`
   - `non-empty without bundle`
5. only the `empty` state is eligible for fresh bootstrap
6. if bootstrap runs, seed a native Go empty bundle with the canonical DB files,
   `client.temp.db`, default managed-storage metadata, and the current built-in
   service seed required by hydrus-go
7. open the resulting bundle from Go and continue with normal read/write wiring

Important boundary for this phase:

- the fresh client bundle is now seeded by a narrow native Go
  schema/bootstrap path aimed at current hydrus-go runtime expectations
- that seed is now a little closer to a real Hydrus first-start bundle:
  - service-list-visible built-ins now include `downloader tags` and
    `favourites`
  - `downloader tags` expands the grouped `local_tags` bucket, while
    `favourites` remains visible through `services` / `services_v2` without a
    new grouped rating bucket, matching current Hydrus behavior
  - `favourites` now uses a real Hydrus-style rating dictionary seed so
    discovery payloads expose the expected star-shape and colour metadata
  - `version` is seeded to the current Hydrus compatibility target
  - default managed `client_files` storage metadata and root are created
  - a small hidden built-in service set is present (`deleted from anywhere`,
    `local notes`, `client api`)
- this first native slice intentionally targets the current Go feature set, not
  full upstream Hydrus bootstrap parity yet
- `hydrus-go` currently fails fast on partial or otherwise invalid bundle
  directories instead of trying to repair or overwrite them

The first internal import checkpoint now proves that a caller-prepared local file
can:

- be placed into the managed `client_files` layout
- be recorded in the Hydrus bundle with serialized `BEGIN IMMEDIATE` writes
- round-trip immediately through the existing metadata readers

That original internal checkpoint started without:

- hashing or MIME sniffing the source file
- thumbnail generation
- public HTTP write endpoints
- runtime daemon write enablement

Those gaps are now partially closed by the public local-path import slice, which
adds:

- daemon-local hashing and MIME detection for `POST /v1/import/local_file`
- runtime write enablement through a separate writable bundle when available
- best-effort managed thumbnail generation for imported JPEG/PNG/GIF still images

The project now also has the first thin-client-oriented browse surface:

- `GET /v1/library/recent` for paged recent local file browsing
- `GET /v1/files/content` for original-file streaming
- `GET /v1/files/thumbnail` for managed thumbnail streaming when available
- `POST /v1/files/trash` for moving one file into the local trash domain
- `POST /v1/import/local_file` for single-file daemon-local imports

These endpoints are intentionally thin-client-oriented rather than strict Hydrus
Client API parity. They exist to validate local add/trash/browse workflows
before the project expands into richer public write APIs and PTR sync.

The planned desktop direction is now:

- a thin Fyne prototype surfaced through `cmd/hydrus-desktop` and documented in `desktop/fyne/`
- daemon remains the owner of SQLite, `client_files`, imports, and later PTR sync
- the first client milestone stays closer to a simple image-browser shell than to full Hydrus workstation parity
- the prototype is specifically meant to exercise `hydrusd` browse/add/trash behavior, selected-file metadata, and original-file serving, not to be a general-purpose Hydrus replacement yet

The current selected-file preview behavior is intentionally narrow:

- the desktop client uses `GET /v1/files/content` for selected JPEG/PNG/GIF items only
- preview requests are bounded to 16 MiB payloads, 8192px maximum dimension, and 16,000,000 decoded pixels
- those limits are deliberate thin-client safety rails so manual LAN testing can validate original-file serving without turning the prototype into an unrestricted media viewer

The runtime storage/DB model for this phase is:

- one read bundle opened with `PRAGMA query_only = ON`
- one separate writable bundle used for public import and trash mutations when writable access is available
- browse/metadata/asset handlers talk to the read bundle so they only observe committed state

The deeper Hydrus client-core behaviors are still pending:

- full media-result metadata parity, especially tags/ratings/viewing stats/notes/detailed URL info
- broader DB-backed import orchestration beyond the single local-path slice, especially richer upload/batch flows and richer import metadata capture
- thumbnail generation for additional media types beyond the current JPEG/PNG/GIF still-image subset
- file serving and broader managed file-store lifecycle behavior
- search/tagging engine behavior
- richer stateful background processing
