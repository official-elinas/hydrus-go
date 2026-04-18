# hydrus-go Fyne prototype

This subtree documents the first Fyne-based thin desktop prototype for
`hydrus-go`.

The prototype is intentionally narrow:

- it connects to `hydrusd` over HTTP/JSON
- it does not touch SQLite or `client_files` directly
- it exists to validate daemon/database add/trash behavior through a real UI
- it is shaped more like `image-tests/comfyui-image-browser.png` than full
  Hydrus workstation parity

## Current prototype loop

- connect to local `hydrusd`
- load recent local files into a thumbnail grid
- inspect selected-file metadata
- add one local file through the daemon's local-path import endpoint
- trash one selected file through the daemon's trash endpoint

## Local run sketch

```bash
go run -tags fyne ./cmd/hydrus-desktop
```

The daemon should already be running separately, typically at
`http://127.0.0.1:45869`.

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

If your current environment does not have native desktop headers installed, use
`make check-desktop` to type-check the tagged Fyne code path. That validation
target writes `bin/hydrus-desktop.wasm`.
