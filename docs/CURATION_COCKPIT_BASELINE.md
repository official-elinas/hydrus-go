# feat/curation-cockpit baseline

Last updated: 2026-04-26

## Goal

Capture the current branch state for `feat/curation-cockpit` before the next
backend iteration.

This document is intended to answer two practical questions:

- what is now present and testable on this branch?
- what is still missing before `hydrus-go` reaches practical Hydrus parity?

It also records the latest test/build history used to validate the branch so a
manual testing pass can start from a concrete baseline.

## Current branch scope

The current branch has already moved beyond the earlier thin-client browse shell
and now includes:

- DB-backed tag suggestions and a desktop autocomplete flow
- manual database integrity checking through the daemon and desktop UI
- desktop gallery filtering with a first local `system:` predicate layer
- desktop sorting by date, name, and size
- selected-file tag rendering in more Hydrus-like grouped/colored form
- selected-preview caching and reconnect-oriented desktop polish
- PTR status/manual-sync handling with retry-aware status text

Recent branch-specific follow-up commits include:

- `bdfa2b2` `fix: reset desktop loads on reconnect attempts`
- `61e8597` `fix: retry transient PTR polling errors`
- `d995440` `feat: cache selected desktop previews`
- `534135c` `feat: color selected tags in the detail pane`
- `179650b` `style: polish the preview placeholder layout`
- `5c59aa3` `style: format desktop prototype updates`

## What is now testable on this branch

The branch is ready for a manual user test pass covering:

- native-Go first-start bootstrap through `hydrusd`
- daemon connect/auth/session flow from the desktop client
- recent browsing with incremental loading
- queued file upload/import through the daemon-owned upload path
- retry/remove/prune queue actions in the desktop client
- selected JPEG/PNG/GIF original preview through the daemon content endpoint
- selected-preview cache reuse after reselection/reconnect
- trashing a selected file and refreshing the grid afterward
- daemon-backed selected-file metadata/tag display
- desktop search filtering, autocomplete, sort modes, and first local
  `system:` predicates
- daemon-owned PTR status refresh and manual sync triggering
- daemon-owned database integrity check from the desktop menu

## What is still lacking

### Highest-priority backend gaps

These remain the major missing pieces even after the curation-cockpit desktop
work:

1. **PTR definition/content application**
   - update blobs are downloaded and registered locally, but not yet applied
     into local mappings/tag state
   - imported/local files do not yet gain real query-visible tag value from PTR
     sync-in work

2. **Real search/query foundation**
   - the desktop can filter the currently loaded recent grid, but the daemon does
     not yet expose a real Hydrus-like search/query API
   - there is still no backend equivalent of `search_files`

3. **Tag mutation / pending-state parity**
   - metadata can be read and pending PTR mappings can be staged/committed only
     through the current narrow surfaces
   - there is not yet a broader public mutation model for local tag add/delete,
     repository petitions, pending-count review, or full review-services-style
     workflows

4. **PTR sync-out/upload parity**
   - the daemon still only supports anonymous read/sync-in behavior
   - pending upload eligibility, upload payload generation, repository upload,
     and last-upload state are still missing

### Important desktop/product gaps

The branch is more usable than before, but it is still not a full Hydrus-like
 workstation:

- no daemon-driven search page
- no real tag tree/search pane backed by query endpoints
- no multi-select result actions
- no archive/inbox/undelete/permanent-delete workflow
- no broader media preview/player coverage beyond the current still-image slice
- no richer review-services/service-management pages

### Sync continuation and ETA limitations

The current PTR sync implementation is **good enough for checkpointed
continuation**, but not for full in-flight resume semantics:

- persisted state is sufficient for later sync passes to continue from stored
  checkpoints like `metadata_slice` and processed/downloaded counts
- interrupted daemon runs do **not** resurrect the exact in-flight sync worker
  or active lease after restart

The current PTR sync implementation is **not good enough for real finish ETA**:

- the daemon exposes completed-work counters and retry timing
- it does not yet expose enough total-work/rate information for a meaningful
  time-to-finish estimate
- the only time-based status currently supported is retry/backoff countdown

### Validation gaps still remaining

The branch still needs real manual validation in the environments the docs call
out explicitly:

- Windows-over-LAN smoke testing against a live `hydrusd` instance
- add/trash latency validation on realistic libraries
- reconnect and queue-resume validation outside of focused package tests
- PTR manual sync validation against a real remote and local library state

## Test and build history for this baseline

The following commands were re-run to confirm the current branch baseline before
manual testing:

### Desktop tests

```bash
go test -tags fyne ./internal/desktop/fyneapp
```

Result:

- passed

This covers the current desktop-side helper logic and formatting, including the
recent curation/reconnect/preview follow-up work.

### API tests

```bash
go test ./internal/api/httpapi
```

Result:

- passed

### Desktop daemon client tests

```bash
go test ./internal/desktop/daemonclient
```

Result:

- passed

### Targeted Hydrus DB tests

```bash
go test ./internal/db/hydrusdb -run 'TestBundleWriteTransactions|TestBundleSuggestTags'
```

Result:

- passed

This targeted DB validation was used because the broader PTR/runtime DB package
test surface has historically included unrelated long-running behavior.

### Daemon build

```bash
go build ./cmd/hydrusd
make build
go build -o ./hydrusd ./cmd/hydrusd
```

Result:

- succeeded

### Host desktop build

```bash
make build-desktop
go build -tags fyne -o ./hydrus-desktop ./cmd/hydrus-desktop
```

Result:

- succeeded

### Explicit Linux desktop build

```bash
make build-desktop-linux
```

Result:

- succeeded

### Windows desktop build

```bash
make build-desktop-windows
```

Result:

- succeeded
- produced `bin/hydrus-desktop.exe`

### WASM desktop validation build

```bash
make check-desktop
```

Result:

- succeeded
- produced `bin/hydrus-desktop.wasm`

## Current build artifacts available for manual testing

- daemon binaries: `./hydrusd`, `./bin/hydrusd`
- host/Linux desktop binaries: `./hydrus-desktop`, `./bin/hydrus-desktop`
- Windows desktop binary: `./bin/hydrus-desktop.exe`
- WASM desktop validation artifact: `./bin/hydrus-desktop.wasm`

These artifacts were rebuilt from the current `feat/curation-cockpit` checkout
for the next manual test pass.

## Recommended manual test checklist

For the next user-driven validation pass, test in this order:

1. start `hydrusd` against a real or freshly bootstrapped library
2. connect from the desktop client over the intended URL
3. browse recent files and confirm incremental loading remains stable
4. import supported still images through the upload path
5. verify queue resume/retry/remove/clear behavior
6. verify selected preview caching and reconnect behavior
7. trash a selected file and confirm refresh behavior
8. exercise search autocomplete, sort modes, and local `system:` predicates
9. open PTR status, trigger a manual sync, and verify retry/countdown wording
10. run the database integrity check from the desktop menu

## What should happen next after testing

If the manual testing pass does not reveal a blocker, the next engineering
milestone should still be:

- **apply downloaded PTR definitions/content into local mappings/tag state**

That remains the first missing backend step that turns the current PTR transport
work into real local tag/query value.
