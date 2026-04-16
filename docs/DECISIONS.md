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

## 2026-04-16 — legacy Hydrus Server is not an early migration target

**Decision**

Do not prioritize a direct reimplementation of the old Hydrus Server.

**Why**

- it is not the primary value center of Hydrus for this migration
- it would drag effort away from the client-core/backend rewrite
- the user explicitly deprioritized it

**Consequence**

If server-like behavior returns, it should emerge from the same headless backend
architecture rather than as a separate legacy-compatible port.
