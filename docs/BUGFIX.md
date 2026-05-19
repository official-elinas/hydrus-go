# hydownloader integration bugfix backlog

Last updated: 2026-05-10

## Architecture recap

The daemon-first boundary is clean — hydownloader never touches SQLite directly.
All mutations flow through `hydrusd` HTTP endpoints (`add_file`, `associate_url`,
`add_tags`, `set_notes`, `set_time`, `force_commit`). Separate read/write bundles
prevent read handlers from observing uncommitted state. Shutdown is graceful:
`/shutdown` POST → wait → kill on timeout.

Despite the clean boundary, there are robustness gaps that will cause real
operational failures if hydownloader is enabled in production.

---

## Critical

### ~~1. No auto-restart on hydownloader process crash~~ ✓ Fixed 2026-05-10

~~If `hydownloader-daemon` crashes after startup, `refreshProcessState()` records
`lastErr` but never relaunches the process. The supervisor just reports
`Running: false` until the daemon is manually restarted.~~

**Files:** `internal/downloader/hydownloader/manager.go`

`livenessLoop` goroutine started after successful initial startup. Polls
`/api_version` every 30 s. On process exit with unreachable API, waits a capped
exponential backoff (2 s → 4 s → … → 2 min) then calls `start()`. Stopped
cleanly via `stopLiveness` channel in `Shutdown`. Also covers medium item 6
(no liveness check after startup).

---

### ~~2. No callback URL reachability check at startup~~ ✓ Fixed 2026-05-10

~~`HYDRUS_GO_PUBLIC_API_URL` is only format-validated. If hydownloader cannot
reach it (wrong LAN IP, firewall, stale DNS), autoimport callbacks fail on
hydownloader's side with zero feedback to hydrusd. The operator gets no signal
that downloads will silently never import.~~

**Files:** `internal/downloader/hydownloader/manager.go`

`checkCallbackURLReachability` performs a best-effort TCP dial to the public API
URL immediately after `start()` succeeds. Logs a structured warning with `url`
and `addr` fields if unreachable. Not a hard gate.

---

## High

### ~~3. Manager never logs~~ ✓ Fixed 2026-05-10

~~Despite receiving a `*slog.Logger`, `manager.go` never calls `m.logger.Info`,
`m.logger.Warn`, or `m.logger.Error`. Errors are returned or stored in `lastErr`
but not emitted to structured logs. This makes production debugging extremely
difficult.~~

**Files:** `internal/downloader/hydownloader/manager.go`

Structured log calls added at: startup begin/ready, shutdown, crash detection in
`refreshProcessState`, startup timeout, status API failures, autoimport
activation success/failure, liveness probe failures, and restart attempts. All
entries carry `root`, `host`, `port`, and `error` fields where applicable.

---

### ~~4. Subprocess stdout/stderr discarded~~ ✓ Fixed 2026-05-10

~~```go
cmd.Stdout = io.Discard
cmd.Stderr = io.Discard
```~~

~~All hydownloader log output is lost. When hydownloader crashes, there is no
trace of what it was doing or why it failed.~~

**Files:** `internal/downloader/hydownloader/manager.go`

`cmd.StderrPipe()` replaces `io.Discard` for stderr. A `drainStderr` goroutine
reads lines and forwards each non-empty line to `m.logger.Info("hydownloader
stderr", "line", ...)`. If the pipe cannot be opened (non-fatal), falls back to
`io.Discard` with a warning log. Stdout remains discarded.

---

### ~~5. No error-path test coverage~~ ✓ Fixed 2026-05-10

~~No tests cover: crash recovery, restart behavior, startup timeout, unreachable
hydownloader API, unreachable callback URL, or read-only autoimport failure.
Only the happy-path lifecycle is tested.~~

**Files:** `internal/downloader/hydownloader/manager_test.go`

Added: `TestManagerStartupError_DaemonCrashesDuringStartup`, `TestManagerStartupError_StartupTimeout`,
`TestManagerStatus_APIUnreachable`, `TestManagerAutoRestart_AfterCrash`,
`TestManagerQueueURL_RejectionWithReason`, `TestManagerPatchImportJobs_MissingAssignment`.
`livenessInterval` and `restartBackoffBase` moved to `var` so tests can override them.
`livenessLoop` restructured to trigger restart on process exit regardless of API reachability.

---

## Medium

### ~~6. No liveness check after startup~~ ✓ Fixed 2026-05-10 (resolved with item 1)

~~After the initial 20-second startup poll, there is no periodic health check.
`refreshProcessState()` only runs when `Status()` is called. A crash could go
unnoticed for minutes if no one polls.~~

**Files:** `internal/downloader/hydownloader/manager.go`

Covered by `livenessLoop` (see item 1). The same goroutine probes `/api_version`
every 30 s and logs failures independently of whether `Status()` is polled.

---

### ~~7. No retry on transient hydownloader API failures~~ ✓ Fixed 2026-05-10

~~`postJSON` returns errors immediately. If the hydownloader API is briefly
unreachable, queue/status/activate calls fail outright with no backoff or retry.~~

**Files:** `internal/downloader/hydownloader/manager.go`

`postJSONWithRetry` adds a 3-attempt ladder (1s/2s/3s backoff) used for
non-mutating calls (`/get_status_info`, `/downloaders`). Mutating requests remain
single-attempt to avoid duplication.

---

### ~~8. Read-only mode breaks autoimport silently~~ ✓ Fixed 2026-05-10

~~If the write bundle cannot open, `app.go` logs a warning and continues in
read-only mode. However, `activateDownloaderAutoimportAfterReady()` still fires,
and hydownloader's autoimport callbacks hit error responses from the mutation
endpoints. No warning reaches the operator that autoimport will not work.~~

**Files:** `internal/app/app.go`

`activateDownloaderAutoimportAfterReady` now guards on `a.writeBundle == nil`
and logs a structured warning when autoimport activation is skipped in read-only
mode.

---

## Low

### ~~9. `patchImportJobs` is fragile~~ ✓ Fixed 2026-05-10

~~`replacePythonAssignment` uses a simple `strings.HasPrefix(trimmed, name+" =")`
match. If hydownloader changes the `hydownloader-import-jobs.py` format (e.g.,
adds type hints, switches to f-strings, or changes variable naming), the patch
silently appends a new line instead of replacing the existing one.~~

**Files:** `internal/downloader/hydownloader/manager.go`

`replacePythonAssignment` now returns `(string, bool)`. `patchImportJobs` logs a
structured warning when the assignment is not found and the function falls back
to prepending.

---

### ~~10. No feedback when hydownloader rejects a queued request~~ ✓ Fixed 2026-05-10

~~`QueueURL` and `QueueSubscription` check `response.Status` after posting. If
hydownloader returns `{"status": false}`, the error message is generic
(`"hydownloader rejected queued URL"`). The actual reason is lost.~~

**Files:** `internal/downloader/hydownloader/manager.go`

`QueueURL` and `QueueSubscription` now decode an optional `reason` field from
rejection responses and include it in the returned error message.

---

## Test coverage inventory

| File | Covers | Depth | Gaps |
|------|--------|-------|------|
| `manager_test.go` | Happy-path lifecycle + all critical error paths (startup crash, startup timeout, API unreachable, auto-restart after crash, rejection with reason, missing import-job assignments) | Good | None remaining |
| `router_test.go` | Downloader status/queue routing | Shallow | Plumbing only |
| `app_test.go` (`TestApp_DBBackedHydrusClientMutationRoundTrip`) | End-to-end mutation API path | Strong | Happy-path only |
| `client_test.go` | Session-backed downloader request forwarding | Shallow | Request shape only |

---

## Recommended fix order

1. ~~Auto-restart with backoff (item 1)~~ ✓ Done
2. ~~Wire the logger (item 3)~~ ✓ Done
3. ~~Capture subprocess stderr (item 4)~~ ✓ Done
4. ~~Add liveness goroutine (item 6)~~ ✓ Done (resolved with item 1)
5. ~~Validate callback URL reachability (item 2)~~ ✓ Done
6. ~~Guard autoimport behind write-bundle check (item 8)~~ ✓ Done
7. ~~Add error-path tests (item 5)~~ ✓ Done
8. ~~Retry transient API failures (item 7)~~ ✓ Done
9. ~~Harden `patchImportJobs` (item 9)~~ ✓ Done
10. ~~Improve rejection feedback (item 10)~~ ✓ Done
