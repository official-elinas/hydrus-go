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

If your current environment does not have native desktop headers installed, use
`make check-desktop` to type-check the tagged Fyne code path.
