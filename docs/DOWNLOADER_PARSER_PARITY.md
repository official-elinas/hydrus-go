# downloader and parser parity plan

Last updated: 2026-04-26

## Current gap

`hydrus-go` still has no downloader, parser, watcher, or subscription system.
The current daemon can import local/uploaded files, browse recent files, expose
metadata, and run the early PTR sync path, but it cannot yet turn a gallery
query or URL into downloaded files and tags.

## Python Hydrus systems to port

The Python client implements downloader behavior as a pipeline:

1. **URL classification and generation**
   - `hydrus/client/networking/ClientNetworkingFunctions.py`
   - `hydrus/client/networking/ClientNetworkingURLClass.py`
   - `hydrus/client/networking/ClientNetworkingDomain.py`
   - `hydrus/client/networking/ClientNetworkingGUG.py`
   - user query text is expanded by Gallery URL Generators, matched by URL
     classes, normalized, and linked to parsers through the domain manager

2. **Page parsing**
   - `hydrus/client/parsing/ClientParsing.py`
   - `hydrus/client/parsing/ClientParsingResults.py`
   - page parsers group parse formulas, content parsers, and subsidiary page
     parsers into parsed file URLs, gallery URLs, tags, notes, and other
     metadata

3. **Seed queues and imports**
   - `hydrus/client/importing/ClientImporting.py`
   - `hydrus/client/importing/ClientImportFileSeeds.py`
   - `hydrus/client/importing/ClientImportGallerySeeds.py`
   - parser results become file seeds and gallery seeds, which track status,
     errors, retry state, and what still needs work

4. **Downloader entry points**
   - `hydrus/client/importing/ClientImportSimpleURLs.py`
   - `hydrus/client/importing/ClientImportGallery.py`
   - `hydrus/client/importing/ClientImportWatchers.py`
   - `hydrus/client/importing/ClientImportSubscriptionQuery.py`
   - `hydrus/client/importing/ClientImportSubscriptions.py`
   - `hydrus/client/ClientDownloading.py`
   - simple URL imports, gallery imports, watchers, quick downloads, and
     subscriptions all reuse the URL/parser/seed machinery

## Recommended Go implementation order

1. **URL foundation**
   - add URL normalization, URL class matching, parser-link lookup, and Gallery
     URL Generator support behind daemon-owned packages
   - persist/import serializable downloader definitions before exposing a UI

2. **Parser foundation**
   - port the smallest useful page-parser model: HTML/JSON parse formulas,
     content parsers, page parsers, and parsed post/content result types
   - keep parser results independent from HTTP downloading so they can be tested
     with static fixtures first

3. **Seed queue foundation**
   - implement file seed and gallery seed state machines with status, retry,
     error text, source URL, normalized URL, and discovered child URLs
   - connect successful file seeds to the existing daemon import/write path

4. **Simple downloader slice**
   - expose a daemon-owned one-shot URL import flow before adding gallery loops
     or subscription scheduling
   - use the same auth, status, and background-job patterns already used by PTR
     sync where possible

5. **Gallery, watcher, and subscription slices**
   - add gallery traversal after one-shot URL importing works
   - add watcher cadence and subscription scheduling only after the seed queues
     can be resumed and inspected safely

## UI note: top-right `NORMAL`

The current `hydrus-go` Fyne source does not contain a `NORMAL`/`Normal` label,
and the Fyne shell does not create one in `internal/desktop/fyneapp`. Fyne v2
desktop windows use native GLFW/window-manager decorations for title bars, while
app code controls only the canvas content passed through `SetContent`.

If `NORMAL` appears inside the app content area, capture a screenshot and remove
the owning widget. If it appears in native window chrome, it is controlled by the
desktop environment, window manager, or tooling rather than by the current Fyne
layout code.
