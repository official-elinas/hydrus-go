# hydrus-go finish test plan for May 9

This document is the finish-line validation plan for the current `hydrus-go`
daemon-first milestone. It is not a full Hydrus parity plan. It is the ordered
test sequence required to honestly say the current slice is done.

## 1. Freeze scope first

Before running finish-line QA, write down what **May 9 done** means.

Decide explicitly:

- whether native in-app video playback is in scope or intentionally deferred
- whether broader PTR/repository workflows beyond current pending-add / commit are in scope
- whether this milestone means "current daemon-first slice is done" or "broader parity is done"

**Pass if:** one short written statement exists saying what is in and out for May 9.

## 2. Run the canonical automated gates

Do not rely on raw `make test` alone as the finish gate. Run and save the full output of:

```bash
make fmt
go test -timeout 30m ./...
go test -tags fyne ./internal/desktop/fyneapp -count=1
go test -count=1 ./internal/app ./internal/importing ./internal/ptrsync ./internal/db/hydrusdb
make build
make check-desktop
make build-desktop-linux
make build-desktop-windows
```

Notes:

- `make check-desktop` is a compile/type-check gate, not a behavior test
- `make build-desktop-windows` is a build gate, not a runtime validation gate

**Pass if:** all commands exit 0 and logs are saved.

## 3. Test fresh bootstrap from `hydrusd`

Use an empty directory and verify the native bootstrap path:

```bash
./bin/hydrusd --listen 127.0.0.1:55999
```

That should create `./db` in the current working directory by default. If you
want to override the bundle location, set `HYDRUS_GO_DB_DIR` explicitly before
launching.

Check:

- first start creates a usable bundle in `./db`
- the daemon does **not** create a top-level `./thumbnails` directory during bootstrap; managed thumbnail storage metadata is seeded in the bundle and thin-client thumbnail resolution still works when thumbnails exist
- second start reopens the bundle instead of recreating it
- partial or invalid bundle states fail fast
- these endpoints work:
  - `GET /healthz`
  - `GET /api_version`
  - `GET /verify_access_key`
  - `GET /session_key`
  - `GET /get_services`

**Pass if:** first start bootstraps, second start reopens cleanly, and auth/service endpoints work.

## 4. Test against a real existing Python-generated Hydrus bundle

Point `HYDRUS_GO_DB_DIR` at a real existing Hydrus client DB directory and start
the daemon normally.

Check:

- `GET /get_services`
- `GET /get_service`
- `GET /get_files/file_metadata`
- `GET /v1/library/recent`
- `GET /v1/files/content`
- `GET /v1/files/thumbnail`

**Pass if:** the daemon reads real existing-bundle data correctly, not just fresh-bootstrap data.

## 5. Test access/session auth flow end to end

Run the real access/session sequence:

1. `GET /api_version`
2. `GET /verify_access_key`
3. `GET /session_key`
4. use the session key on protected endpoints

**Pass if:** access key and session key both work for protected requests.

## 6. Test recent browse and asset serving

API:

- `GET /v1/library/recent?offset=0&limit=<n>`
- `GET /v1/files/thumbnail?file_id=<id>`
- `GET /v1/files/content?file_id=<id>`

Desktop:

- connect the Fyne client
- load the grid
- scroll to load more
- refresh repeatedly
- change selection repeatedly

**Pass if:** recent items load, thumbnails/originals resolve, incremental loading works, and refresh preserves sane selection state.

## 7. Test the search matrix

### 7.1 Tag search

Test:

- one explicit tag
- two-tag AND query
- one missing tag query

### 7.2 Daemon-backed `system:` predicates

Test:

- `system:size`
- `system:width`
- `system:height`
- `system:favorite`
- `system:favourite`
- `system:resolution`

### 7.3 Sorts

Test:

- default newest
- `import_oldest`
- `size_desc`
- `size_asc`

### 7.4 Known fallback behavior

Test and document expectations for:

- `system:local`
- `system:trashed`
- `system:deleted`
- unsupported free-text terms being ignored rather than applied as client-local filters

**Pass if:** daemon-backed predicates/sorts return correct results and unsupported terms are visibly ignored rather than misapplied as client-local filters.

## 8. Test local-path import

From the daemon host, test:

- `POST /v1/import/local_file`

Use at least:

- one JPEG
- one PNG
- one GIF

Verify after import:

- item appears in recent browse
- metadata is populated
- thumbnail exists for still images
- original content endpoint works

**Pass if:** path import round-trips from path -> DB -> recent/grid -> metadata -> content/thumbnail.

## 9. Test upload import

Test:

- `POST /v1/import/upload`
- desktop file picker
- desktop folder picker
- drag-and-drop

Verify:

- queue items process
- failed items are isolated cleanly
- successful items appear in recent browse
- retry/remove/prune/clear controls behave correctly

**Pass if:** the remote-safe upload flow works and the queue survives mixed-good / mixed-bad inputs.

## 10. Test trash round trip

Test both API and desktop UI trash behavior.

Verify:

- file state changes as expected
- recent/grid refreshes correctly
- metadata reflects trash/deletion state correctly afterward

**Pass if:** trash is daemon-owned, observable, and leaves no stale UI state.

## 11. Test tagging and autocomplete round trip

Test:

- `GET /v1/tags/autocomplete`
- desktop tag popup/editor
- `POST /add_tags/add_tags`

Then verify:

1. add a tag
2. search by that tag
3. fetch file metadata
4. confirm the tag appears in metadata and search results

**Pass if:** tag mutation -> search -> metadata works as a full round trip.

## 12. Test PTR status and manual sync

Use both API and desktop UI:

- `GET /service/ptr/status`
- `POST /service/ptr/sync`
- desktop `Network > PTR Sync`

Observe:

- idle -> syncing -> complete/retrying/failure transitions
- pending counts visible
- retry/busy states visible and not silent

**Pass if:** PTR state is clearly daemon-owned and operator-visible.

## 13. Test PTR pending staging and commit

Run the current narrow contribution flow end to end.

Test:

- stage pending mappings
- verify `GET /manage_services/pending_counts`
- commit pending mappings
- verify counts update afterward

Use both API and desktop UI where applicable.

**Pass if:** staged mappings appear in pending counts and commit changes visible state.

## 14. Test import -> PTR -> search value

Flow:

1. import a file
2. get it into the relevant tag/PTR path
3. run sync/commit flow as appropriate
4. reopen/restart the daemon
5. search again for the resulting tag state

**Pass if:** the file’s tag/search state remains correct after reopen/restart.

## 15. Test still-image viewer behavior

For JPEG/PNG/GIF:

- double-click from the grid
- resize the watcher
- use arrow keys
- use mouse wheel
- change selection from the gallery
- verify selected tile highlight tracks viewer state

**Pass if:** watcher stays in-app, remains resizable, and navigation/selection stay in sync.

## 16. Test video behavior explicitly

Try at least one video file in the watcher.

Expected outcomes:

- if video is in scope: it must actually play
- if video is out of scope: the fallback/unsupported message must be explicit and documented

**Pass if:** there is no ambiguity about the intended behavior.

## 17. Test disconnect/reconnect queue behavior

Flow:

1. connect the desktop client
2. queue imports
3. disconnect daemon/network
4. keep queue staged
5. reconnect
6. verify resume behavior

**Pass if:** the queue survives reconnect and resumes exactly as designed.

## 18. Test LAN mode

Run the daemon remotely:

```bash
make run-lan
```

or:

```bash
./bin/hydrusd --listen 0.0.0.0:5555
```

From another machine, connect the desktop client to the daemon host IP.

Run the real workflow:

- connect
- browse
- search
- upload import
- trash
- tag edit
- PTR status/sync

**Pass if:** the desktop behaves like a real client and does not rely on local-path assumptions.

## 19. Test Windows runtime on actual Windows

Do not stop at cross-compilation.

Build:

```bash
make build-desktop-windows
```

Then run the `.exe` on real Windows hardware and test:

- connect to a Linux-hosted daemon
- browse
- import one still image
- open viewer
- trash
- tag edit/autocomplete
- PTR popup/status

**Pass if:** the actual Windows binary works, not just the cross-build.

## 20. Run one performance pass on a non-trivial library

At minimum, record observed latency for:

- daemon startup
- first recent page load
- next page load
- one tag search
- one `system:` search
- one upload import
- one trash mutation
- PTR status refresh
- one full sync trigger

Use a real library, not only fixture data.

**Pass if:** performance is acceptable for the milestone and any obvious bottlenecks are either fixed or documented.

## 21. Final doc freeze and sign-off

Before declaring May 9 done, reconcile docs with observed behavior.

Confirm or update:

- `README.md`
- `docs/STATUS.md`
- `docs/NEAR_PARITY_TRIAGE.md`
- `docs/PARITY_ROADMAP.md`

Make sure:

- current daemon-backed predicates are listed correctly
- deferred items are clearly deferred
- the video decision is explicit
- manual QA claims are backed by actual recorded results

**Pass if:** docs match tested reality.

## Blockers to an honest May 9 "done"

Do not call this done if any of these remain unresolved:

1. the canonical automated gate is still unreliable or ambiguous
2. the video scope decision is still undecided
3. no real Python-generated Hydrus bundle was tested
4. no live PTR smoke test was run
5. no actual Windows runtime smoke test was run
6. tag mutation round trip was not verified
7. LAN import/browse/search was not verified
8. docs still contradict the code or the observed test results

## Things that do not count as "tested everything"

Useful, but not sufficient by themselves:

- `make check-desktop` (compile/type-check only)
- `make build-desktop-windows` (build only)
- fixture-backed PTR tests only (no live remote check)
- fresh bootstrap only, without testing a real existing Hydrus bundle

## Shortest honest definition of done

The current milestone is done only if:

- all automated gates are green
- real existing-bundle mode works
- the desktop works against a remote daemon
- PTR works live enough to prove value
- the still-image viewer is solid
- video is either shipped or explicitly deferred
- Windows runtime is actually smoke-tested
- docs match reality
