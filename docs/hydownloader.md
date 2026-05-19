# hydownloader integration

Last updated: 2026-05-05

## What this is

`hydrus-go` does not currently implement a native Go downloader/parser stack.
The current downloader slice is an external bridge to upstream
`hydownloader`, which itself is built around `gallery-dl`.

In practice, `hydrusd` supervises `hydownloader-daemon`, forwards queue and
status requests to it, and exposes the Hydrus Client API mutation endpoints
that `hydownloader`'s importer expects.

## Responsibility split

`hydrusd` owns:

- daemon startup and shutdown
- Hydrus DB access and managed `client_files`
- the stable HTTP API that the desktop client talks to
- hydownloader root initialization and supervision
- Hydrus-compatible import endpoints used by hydownloader autoimport

`hydownloader` plus `gallery-dl` own:

- downloader definitions
- gallery traversal and site-specific parsing
- URL queue processing
- subscription scheduling
- actual remote download work

## How it is wired into `hydrusd`

Config parsing lives in `internal/config/config.go`.

Relevant env vars:

- `HYDRUS_GO_ENABLE_HYDOWNLOADER`
- `HYDRUS_GO_HYDOWNLOADER_ROOT`
- `HYDRUS_GO_HYDOWNLOADER_HOST`
- `HYDRUS_GO_HYDOWNLOADER_PORT`
- `HYDRUS_GO_HYDOWNLOADER_ACCESS_KEY`
- `HYDRUS_GO_HYDOWNLOADER_AUTOIMPORT`
- `HYDRUS_GO_HYDOWNLOADER_DAEMON_BIN`
- `HYDRUS_GO_HYDOWNLOADER_TOOLS_BIN`
- `HYDRUS_GO_PUBLIC_API_URL`

`hydrusd` constructs the external manager in `internal/app/app.go` and passes
it:

- the hydownloader config
- the Hydrus callback base URL
- the Hydrus Client API access key

The hydownloader manager lives in
`internal/downloader/hydownloader/manager.go`.

On startup it:

- creates the hydownloader root if needed
- runs `hydownloader-tools init-db --path <root>` when the root is not yet initialized
- patches `hydownloader-config.json` with daemon host, port, and access key
- patches `hydownloader-import-jobs.py` with `defAPIURL` and `defAPIKey`
- starts `hydownloader-daemon`
- waits for the hydownloader API to become reachable

## Request flow

The desktop client talks only to `hydrusd`.

For downloader work, the flow is:

1. desktop client calls `hydrusd`
2. `hydrusd` forwards the request to `hydownloader-daemon`
3. hydownloader processes the job through its own queue and downloader logic
4. hydownloader autoimport calls back into `hydrusd`
5. `hydrusd` imports the files and metadata into the Hydrus bundle

The current public downloader endpoints are:

- `GET /v1/downloader/status`
- `GET /v1/downloader/downloaders`
- `POST /v1/downloader/url`
- `POST /v1/downloader/subscription`

The Hydrus-compatible mutation endpoints used by hydownloader importer are:

- `POST /add_files/add_file`
- `POST /add_urls/associate_url`
- `POST /add_tags/add_tags`
- `POST /add_notes/set_notes`
- `POST /edit_times/set_time`
- `POST /manage_database/force_commit`

## What works today

- queue one URL through hydownloader
- queue one downloader-specific subscription through hydownloader
- ask hydrusd for hydownloader status
- ask hydrusd for the downloader map that hydownloader reports
- let hydownloader autoimport finished downloads back into the Hydrus DB through `hydrusd`

## What does not exist yet

This is not full Hydrus downloader parity.

`hydrus-go` does not yet provide a native Go implementation of:

- gallery parser execution
- downloader definitions
- native gallery traversal
- watcher/download page logic
- native subscription scheduling

That work is still tracked as downloader/parser parity, while the current slice
is only the external hydownloader bridge.

## LAN deployment note

For a Linux host running `hydrusd` plus hydownloader and a Windows desktop
client over LAN:

- the Windows client should connect to `hydrusd`
- hydownloader should run on the Linux daemon host next to `hydrusd`
- if `hydrusd` listens on `0.0.0.0:<port>`, set
  `HYDRUS_GO_PUBLIC_API_URL=http://<linux-lan-ip>:<port>`

That explicit public API URL matters because hydownloader importer needs a real
callback host, not a bind address like `0.0.0.0`.

## Practical bottom line

The current model is:

`desktop client -> hydrusd -> hydownloader -> gallery-dl -> hydrusd import endpoints -> Hydrus DB`

`hydrusd` is the daemon/backend and ingest target.

`hydownloader` is the downloader engine and scheduler.

`gallery-dl` is the site/parser backend under hydownloader.
