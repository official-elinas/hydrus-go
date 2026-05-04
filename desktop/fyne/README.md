# hydrus-go Fyne prototype

This subtree documents the first Fyne-based thin desktop prototype for
`hydrus-go`.

The prototype is intentionally narrow:

- it connects to `hydrusd` over HTTP/JSON
- it does not touch SQLite or `client_files` directly
- it exists to validate daemon/database browse/import/trash behavior, bulk queue orchestration, and bounded selected-file preview through a real UI
- it is shaped more like `image-tests/comfyui-image-browser.png` than full
  Hydrus workstation parity

## Current prototype loop

- connect to local or remote `hydrusd`
- load recent local files into a thumbnail grid
  - load additional recent results as you scroll, backed by the daemon's recent-files endpoint
- queue imports from:
  - the single-file picker
  - the folder picker
  - drag-and-drop of files or folders into the window
- queue downloader-managed gallery/post URLs through the existing `Import URL` dialog when hydownloader integration is enabled in `hydrusd`
- process the queue sequentially through the daemon's remote-safe upload endpoint
- review queued items in a dedicated list with retry/remove controls
- keep queue staging usable while disconnected and resume processing when a
  usable daemon connection is restored
- skip unsupported dropped items or unreadable paths with user-facing feedback
- preview selected still images and video poster frames through the daemon's content endpoint, using direct decode where possible and local FFmpeg conversion otherwise
  - preview is intentionally bounded to keep the thin client responsive
  - current limits: 16 MiB payload, 8192px maximum dimension, 16,000,000 decoded pixels
- play supported videos inside the watcher through an FFmpeg-backed muted in-app frame-stream path when a local `ffmpeg` binary is available
- inspect selected-file tag metadata and file metadata in separate details sections
- inspect daemon-owned PTR sync state and trigger a manual sync pass from a dedicated popup launched through `Network > PTR Sync`
- clear the queue when idle or prune/retry finished items from the queue review pane
- trash one selected file through the daemon's trash endpoint

## Local run sketch

```bash
go run -tags fyne ./cmd/hydrus-desktop
```

The daemon should already be running separately, typically at
`http://127.0.0.1:45869`.

If you do not already have a Hydrus client bundle, you can start the daemon
against a new or empty `HYDRUS_GO_DB_DIR` and let it bootstrap one natively:

```bash
export HYDRUS_GO_DB_DIR=/tmp/hydrus-go-fyne-smoke-db
./bin/hydrusd
```

For temporary LAN testing, `make run-lan` starts `hydrusd` with the runtime flag
`--listen 0.0.0.0:45869`. Override the port with
`make run-lan LAN_LISTEN_ADDR=0.0.0.0:9999`, or launch the daemon directly with
`./bin/hydrusd --listen 0.0.0.0:5555`.

For a desktop client running on another machine, change the connect URL from the
default localhost value to the Linux daemon host (for example
`http://192.168.1.50:45869`).

To build desktop binaries without running them in-place:

```bash
make build-desktop-linux
make build-desktop-windows
```

`build-desktop-linux` is native-by-default (`linux/amd64` with `LINUX_CC=gcc`).
If you change `LINUX_GOARCH`, provide a matching Linux toolchain through
`LINUX_CC`.

`build-desktop-windows` links `bin/hydrus-desktop.exe` as a Windows GUI
subsystem executable so double-click launching it does not open an extra
terminal window.

If your current environment does not have native desktop headers installed, use
`make check-desktop` to type-check the tagged Fyne code path. That validation
target writes `bin/hydrus-desktop.wasm`.
