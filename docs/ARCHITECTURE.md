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

- a direct port of the legacy Hydrus desktop UI
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
- no public write API yet; the daemon runtime still opens the bundle read-only today

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
- it remains internal-only; there is still no public HTTP import endpoint or runtime write wiring yet

The DB-backed layer currently:

- optionally opens a real Hydrus client DB bundle via `HYDRUS_GO_DB_DIR`
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

The first internal import checkpoint now proves that a caller-prepared local file
can:

- be placed into the managed `client_files` layout
- be recorded in the Hydrus bundle with serialized `BEGIN IMMEDIATE` writes
- round-trip immediately through the existing metadata readers

That checkpoint intentionally stops short of:

- hashing or MIME sniffing the source file
- thumbnail generation
- public HTTP write endpoints
- runtime daemon write enablement

The deeper Hydrus client-core behaviors are still pending:

- full media-result metadata parity, especially tags/ratings/viewing stats/notes/detailed URL info
- broader DB-backed import orchestration beyond the prepared-file internal slice, especially hashing/sniffing, thumbnail generation, and a public write surface
- file serving and broader managed file-store lifecycle behavior
- search/tagging engine behavior
- richer stateful background processing
