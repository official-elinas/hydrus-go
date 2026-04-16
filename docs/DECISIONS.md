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
