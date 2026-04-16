# hydrus-go status

Last updated: 2026-04-16

## Completed

- created `hydrus-go` as a standalone Go module inside the existing Hydrus repo
- initialized an independent git repository and pushed it to GitHub
- added the first headless daemon bootstrap (`hydrusd`)
- added environment-based config, structured logging, and graceful shutdown
- implemented initial HTTP compatibility endpoints:
  - `GET /`
  - `GET /healthz`
  - `GET /api_version`
  - `GET /verify_access_key`
  - `GET /session_key`
  - `GET /get_services`
  - `GET /get_service`
- added bootstrap access-key and in-memory session-key auth flow
- added a fixed Hydrus-compatible bootstrap service catalog
- added tests for config validation, HTTP/auth behavior, and shutdown lifecycle
- documented the daemon-first migration direction and current bootstrap limits

## In Progress

- database/schema reconnaissance against the Python implementation
- deciding SQLite driver and locking strategy for the Go daemon
- defining the first read-only database-backed parity slice

## Next

1. inventory Python database schema creation and migration entry points
2. identify the first read-only database-backed capabilities to port
3. choose the initial SQLite driver and concurrency policy
4. replace the fixed bootstrap service catalog with database-backed state where appropriate
5. establish parity-oriented fixtures and copied-test-database workflow

## Later / Out of Scope for Now

- native Go GUI
- full downloader/parser/subscription parity in the first phase
- legacy Hydrus Server reimplementation
- PTR/server-repository compatibility as an early milestone
- multi-process/distributed architecture

## Open Risks / Unknowns

- SQLite locking behavior under Go concurrency may differ materially from Python behavior
- Hydrus search/tag semantics are much richer than simple SQL lookups
- downloader/parsing parity is large and should not derail the client-core migration
- the managed file-store contract must stay compatible with Hydrus expectations
- LAN support needs deliberate auth/security controls as the daemon surface grows

## Milestone Log

### 2026-04-16 — Milestone 1: headless bootstrap

- completed daemon bootstrap and first compatibility-oriented API slice
- verified with `go test ./...` and `go build ./cmd/hydrusd`
- committed as `feat: bootstrap headless hydrus-go daemon`

### 2026-04-16 — architecture clarified

- confirmed the Go backend should be the long-lived source of truth
- backend is intended to own SQLite, `client_files`, and background processing
- real clients are expected to connect locally first, with LAN support as the next network scope
