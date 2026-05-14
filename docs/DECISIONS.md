# hydrus-go decisions

## 2026-04-16 — `hydrus-go/` remains the migration root

**Decision**

Keep all new Go implementation work under `hydrus-go/` as its own module and git
repository.

**Why**

- preserves the Python project intact as the reference implementation
- keeps the migration boundary explicit
- supports side-by-side parity work without mixing Go into the Python package tree

**Consequence**

The migration progresses as a sibling implementation rather than an in-place
rewrite.

---

## 2026-04-16 — daemon-first architecture

**Decision**

Build `hydrus-go` as a headless daemon/backend rather than a desktop-first port.

**Why**

- Hydrus's core value is in its database, file store, search, and automation logic
- Go is a strong fit for the backend/core but not the best reason to recreate the PyQt monolith
- an API-first backend enables multiple clients and cleaner separation of concerns

**Consequence**

The Go system will be designed around backend ownership and client connections,
not direct UI logic.

---

## 2026-04-16 — backend owns SQLite and `client_files`

**Decision**

The daemon is the source of truth for the SQLite database, managed file storage,
and background processing.

**Why**

- centralizes consistency and locking behavior
- avoids multiple clients mutating state independently
- matches the long-term goal of real clients connecting to a shared backend

**Consequence**

Clients should use APIs instead of directly touching the database or file store.

---

## 2026-04-16 — local and LAN first

**Decision**

Support local-only first, then LAN-connected clients as the initial remote scope.

**Why**

- aligns with Hydrus's privacy-first model
- keeps early networking/security posture manageable
- is enough to validate the daemon-client split without overreaching

**Consequence**

The daemon defaults to local-only behavior, with non-local access treated as an
explicit opt-in.

---

## 2026-04-16 — legacy Hydrus Server is not a migration target

**Decision**

Do not treat a direct reimplementation of the old Hydrus Server as part of this
migration roadmap.

**Why**

- it is not the primary value center of Hydrus for this migration
- it would drag effort away from the client-core/backend rewrite
- the user explicitly deprioritized it

**Consequence**

The migration stays focused on daemon ownership of the client library backend
rather than on legacy server parity.

---

## 2026-04-16 — first DB-backed slice is read-only and single-connection

**Decision**

Open the Hydrus client SQLite bundle read-only on a single dedicated connection
for the first DB-backed migration slice.

**Why**

- Hydrus uses attached SQLite databases rather than a single file
- attached DB aliases are connection-local, so a pooled multi-connection design
  would be error-prone too early
- the first parity target only needs query behavior, not writes
- this keeps the early Go daemon aligned with Hydrus's serialized DB access model

**Consequence**

The daemon can safely serve early DB-backed read APIs, but it does not yet
support DB mutations or concurrent write-oriented workflows.

---

## 2026-04-16 — first DB driver is `modernc.org/sqlite`

**Decision**

Use `modernc.org/sqlite` for the first Hydrus DB integration slice.

**Why**

- it works with the current Go 1.22 toolchain in this repo
- it avoids requiring CGO for the first portable backend milestone
- it is sufficient for read-only attached-bundle access and test fixtures

**Consequence**

The driver choice should stay narrow behind the DB bundle package so it can be
revisited later if write-path, locking, or performance needs change.

---

## 2026-04-16 — default `file_metadata` parity ships in explicit non-tag slices first

**Decision**

Implement default/full `GET /get_files/file_metadata` incrementally, beginning
with a DB-backed non-tag subset instead of waiting for total Hydrus parity.

**Why**

- keeps the migration moving with small, testable vertical slices
- lets the daemon reuse existing derived DB state before porting the full media-result stack
- avoids fabricating partially correct tag/rating/viewing behavior just to remove a `501`
- makes unsupported behaviors explicit when the backing semantics are not ready yet

**Consequence**

The endpoint now returns a documented default/full non-tag subset while deeper
parity work for tags, ratings, notes, detailed URLs, and viewing statistics
continues in later slices.

---

## 2026-04-16 — Hydrus bundle writes go through one serialized `BEGIN IMMEDIATE` runner

**Decision**

Add writable Hydrus bundle support behind an internal serialized transaction
runner, and require future DB mutations to go through that single path.

**Why**

- Hydrus's Python client already centers write behavior around a serialized DB worker model
- attached SQLite bundle aliases are tied to one connection, so uncontrolled pooled writes are risky
- local import, managed file placement, and later PTR sync all need a safe mutation primitive before public write APIs exist
- this creates a narrow, testable write foundation without prematurely exposing writable HTTP surfaces

**Consequence**

The project can begin Phase 4 safely with fixture-backed writable DB primitives,
while the daemon runtime and public APIs remain read-only until import behavior
and `client_files` ownership are fully designed.

---

## 2026-04-16 — managed `client_files` layout rules live in a pure storage package first

**Decision**

Implement Hydrus managed file and thumbnail path derivation in a dedicated pure
package before introducing directory creation, file copy/move orchestration, or
public import APIs.

**Why**

- import safety depends on deterministic managed paths as much as on safe DB writes
- separating path/layout logic from DB code keeps `hydrusdb` focused on bundle access and transactions
- this yields a small, testable Phase 4 slice that can be validated without mutating real libraries
- later import work can compose hashing, path resolution, file placement, and DB mutation as distinct steps

**Consequence**

The project now has a documented and tested home for managed `client_files`
layout rules, while actual directory creation and file placement remain later
Phase 4 work.

---

## 2026-04-16 — managed file placement publishes from a destination-local temp file without overwriting conflicts

**Decision**

Implement the first managed file/thumbnails placement helper by writing to a
temp file in the destination directory and then publishing without overwriting
conflicting existing files.

**Why**

- keeping the temp file in the destination directory avoids cross-device publication issues
- no-overwrite publication is safer than blind replacement while import semantics are still being defined
- this makes the placement layer testable and usable before DB mutation integration exists
- it gives later import code a clear contract for idempotent re-placement and conflict detection

**Consequence**

The project now owns deterministic storage placement behavior internally, but it
now composes that behavior with serialized DB writes in an internal prepared-file
import checkpoint; public write APIs still remain later work.

---

## 2026-04-16 — first real import checkpoint is internal and caller-prepared

**Decision**

Make the first end-to-end import slice internal-only and require the caller to
provide the prepared file metadata (hash, MIME, size, and basic media fields)
instead of starting with a public upload/hashing/sniffing API.

**Why**

- it proves that managed placement and serialized DB writes compose correctly before exposing a writable HTTP surface
- it keeps Phase 4 narrow enough to validate with fixture-backed round-trip tests
- it lets the existing metadata readers verify the resulting Hydrus DB state immediately
- it avoids prematurely locking the project into a public import contract before hashing, MIME sniffing, and thumbnail generation are ported

**Consequence**

The project now has an internal prepared-file import path that places files in
managed `client_files`, records the minimal Hydrus DB rows for a fresh local
import, and reads that state back through the existing metadata APIs. Runtime
daemon wiring and public write endpoints remain read-only for now.

---

## 2026-04-16 — thin desktop client comes before PTR, and it used Qt 6 Widgets initially (superseded)

**Decision (superseded on 2026-04-17 by the Fyne pivot below)**

Prioritize a thin multi-platform desktop client before PTR work, and make Qt 6
Widgets in C++ the first client stack.

**Why**

- the immediate product need was to validate import, browse, preview, export, and trash-first workflows against the Go daemon
- SQLite and managed `client_files` performance should be proven through a real client before PTR synchronization work begins
- Qt aligns with Hydrus's desktop direction without pulling the project into JS/TS or Python UI runtime decisions
- a thin native client is a better first validation target than attempting full Hydrus UI parity immediately

**Consequence**

The roadmap first prioritized thin-client-facing daemon APIs and a Qt desktop
MVP before PTR. That client-stack choice is now superseded, but the
daemon-before-PTR sequencing still stands.

---

## 2026-04-16 — public import runtime uses separate read and write bundles

**Decision**

When the daemon exposes public local-path imports, keep read handlers on a
read-only Hydrus bundle and route mutations through a separate writable bundle.

**Why**

- a Hydrus bundle is tied to one dedicated SQLite connection with attached database aliases
- if reads and writes share the same writable connection, browse/metadata handlers can observe uncommitted transaction state
- a split read/write bundle model preserves the current read-oriented behavior while still allowing serialized public imports

**Consequence**

The daemon now opens two bundle connections when `HYDRUS_GO_DB_DIR` is set:
one query-only read bundle for service discovery, metadata, browse, and asset
streaming; and one writable bundle for public import and trash mutations. This
becomes the basis for thin-client workflow testing alongside ongoing PTR work.

---

## 2026-04-17 — thin desktop client pivots from Qt to Fyne

**Decision**

Supersede the earlier Qt client direction and make a Go-native Fyne prototype the
active desktop path.

**Why**

- the project goal for the first client is to validate `hydrusd` add/trash behavior, not to begin a long-term full workstation UI rewrite
- keeping the thin client in Go avoids a split toolchain across Go + C++/CMake for this prototype phase
- the user explicitly wants to avoid C/C++ and keep the first client native and cross-platform in the Go stack
- the prototype UI can stay intentionally closer to `image-tests/comfyui-image-browser.png` than to full Hydrus parity

**Consequence**

The active tree now removes the Qt scaffold, adds a Fyne desktop entrypoint, and
documents the prototype as a daemon-first test harness for browse, add, and
trash validation against `hydrusd`.

---

## 2026-04-17 — first desktop deliverable is a mutation-testing prototype

**Decision**

Treat the first Fyne desktop client as a prototype for validating daemon and DB
mutation workflows rather than as a broader end-user app milestone.

**Why**

- the immediate technical risk is whether import and trash flows behave correctly against a real Hydrus bundle under Go ownership
- a focused prototype keeps the UI small enough to validate SQLite and managed `client_files` behavior without overcommitting to Hydrus parity too early
- the image references in `image-tests/` point toward a simple thumbnail-browser shell with a narrow utility sidebar, which fits this validation role well

**Consequence**

The first Fyne window prioritizes connect, refresh, add file, trash selected,
recent grid browsing, selected-file metadata, and bounded selected-file original
preview. Export/search/tagging polish remains later work.

---

## 2026-04-17 — selected original preview stays image-only and bounded in the first prototype

**Decision**

Use the existing `GET /v1/files/content` endpoint for selected-file preview in
the Fyne prototype, but keep that preview limited to JPEG/PNG/GIF and enforce
strict payload and decoded-image limits.

**Why**

- validating daemon-served original files is high value for the thin-client MVP
- unrestricted full-original preview would create avoidable memory and bandwidth risk during Windows-over-LAN testing
- the first prototype only needs enough preview capability to prove the content-serving contract, not to become a full media viewer

**Consequence**

The desktop client now previews selected JPEG/PNG/GIF originals through
`/v1/files/content`, but it rejects oversized payloads and very large decoded
images to keep the thin client responsive while broader preview/export behavior
remains later work.

---

## 2026-04-20 — anonymous PTR sync remains daemon-owned and API-triggered

**Decision**

Keep anonymous PTR sync inside `hydrusd` as a daemon-owned background job with
HTTP trigger/status surfaces, rather than letting clients touch PTR network or
SQLite state directly.

**Why**

- preserves the daemon-first boundary for SQLite, managed files, and repository sync state
- keeps PTR lease ownership, failure recording, and shutdown cleanup in one place
- matches the thin-client direction where UI clients poll daemon-owned state instead of opening their own PTR sessions

**Consequence**

The current PTR slice exposes `GET /service/ptr/status` and `POST /service/ptr/sync`, and the daemon now also owns anonymous PTR `/update` download plus local `repository updates` registration. Downloaded definitions/content are still not applied into local mappings or tag/query state; that remains later backend work.

## 2026-05-14 — PTR background polling after first manual opt-in (not yet implemented)

**Decision**

Once the user has triggered at least one manual PTR sync, the daemon should
automatically re-run PTR sync in the background on a periodic interval
(target: approximately every 24 hours) without requiring further user action.

**Why**

- a user who has opted in once has expressed intent to stay up to date with the
  Public Tag Repository; silently going stale after the first sync is unexpected
- matches the Python Hydrus behavior where the client schedules periodic PTR
  update checks after the repository is enabled
- the daemon already holds all required context: the opt-in marker, the PTR
  config, the writable bundle, and the sync runner lifecycle

**Trigger condition**

Background polling should activate only when **all** of the following are true:

1. the PTR opt-in marker file has been written (i.e. the user has triggered at
   least one manual sync in the past, even across daemon restarts)
2. no sync pass is currently active
3. the time elapsed since the last completed sync exceeds the configured
   interval (default: 24 hours)

The interval should be configurable via an environment variable or config field,
with 24 hours as the default. PTR sync should remain opt-in: the background
scheduler must never fire on a fresh daemon start where the user has not
previously opted in.

**Implementation sketch (not yet done)**

- `Manager` startup: if the opt-in marker is present and no active run exists,
  start a ticker/sleep loop in `runnerCtx`
- on each tick: call the same `beginSync` path that `Trigger` uses, guarded by
  `runMu`; skip silently if a run is already active
- `Manager` fields to add: `autoSyncInterval time.Duration`,
  `lastSyncCompletedAt time.Time` (persisted or derived from PTR status)
- the background goroutine must respect `runnerCtx` cancellation for clean
  shutdown
- expose the next scheduled sync time in `coreptrsync.Status` so the UI can
  show it

**Files to touch when implementing**

- `internal/ptrsync/manager.go` — scheduler loop and startup check
- `internal/core/ptrsync/types.go` — add `NextScheduledSyncAt` to `Status`
- `internal/config/config.go` — `HYDRUS_GO_PTR_AUTO_SYNC_INTERVAL` env var
- `internal/api/httpapi/ptrsync.go` — surface `NextScheduledSyncAt` in response
