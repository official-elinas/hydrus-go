# hydrus-go context

## Purpose of this document

This document is the entry point for `hydrus-go/docs/`. It explains how to read
the documentation set, how to interpret the current project state, why
`hydrusd` is the architectural center of the Go work, and what the recent
documentation pass actually did.

This is not the milestone ledger. Use [STATUS.md](./STATUS.md) for the running
checklist and dated milestone log.

## The documentation set

Use the docs directory like this:

| File | Use it for |
| --- | --- |
| [STATUS.md](./STATUS.md) | What is completed, in progress, and next. This is the main milestone log. |
| [ARCHITECTURE.md](./ARCHITECTURE.md) | The current runtime model, ownership boundaries, and daemon/client split. |
| [DECISIONS.md](./DECISIONS.md) | Why major architectural choices were made. |
| [PARITY_ROADMAP.md](./PARITY_ROADMAP.md) | Prioritized parity gaps and recommended implementation order. |
| [NEAR_PARITY_TRIAGE.md](./NEAR_PARITY_TRIAGE.md) | The most code-grounded short-horizon parity snapshot. Use this when older docs drift from the live branch. |
| [thin-client-mvp.md](./thin-client-mvp.md) | The intended scope and contract for the first desktop client. |
| [CURATION_COCKPIT_BASELINE.md](./CURATION_COCKPIT_BASELINE.md) | Branch-specific baseline and manual validation notes for the curation cockpit direction. |
| [DOWNLOADER_PARSER_PARITY.md](./DOWNLOADER_PARSER_PARITY.md) | Downloader and parser parity planning, not current implementation parity. |

Start with [../README.md](../README.md) for the top-level summary, then read
[STATUS.md](./STATUS.md), [ARCHITECTURE.md](./ARCHITECTURE.md), and
[DECISIONS.md](./DECISIONS.md) together.

## Translation, not 1:1 conversion

`hydrus-go` is being built alongside the Python Hydrus codebase, not inside it.
The original Python project remains the reference implementation. The Go work is
not a line-by-line port and it is not a direct PyQt desktop rewrite.

The repo states this in several places:

- [../README.md](../README.md) says the goal is not a line-by-line translation
  and instead describes a stable, testable, API-first daemon.
- [ARCHITECTURE.md](./ARCHITECTURE.md) says the project should avoid line-by-line
  translation and prefer backend/core extraction over UI replication.
- [DECISIONS.md](./DECISIONS.md) records that `hydrus-go/` remains a sibling
  migration root so the Python project stays intact.
- [NEAR_PARITY_TRIAGE.md](./NEAR_PARITY_TRIAGE.md) explicitly warns that the Go
  project is a conversion of behavior and capability, not a 1:1 PyQt
  translation.

The practical meaning is that the project is translating Hydrus's core behavior
and ownership model into Go: SQLite bundle access, managed `client_files`, PTR
work, search/tagging surfaces, and stable APIs for clients. It is not trying to
clone the Python process structure or desktop UI verbatim.

## Why `hydrusd` is the architectural center

`hydrusd` is the daemon/backend entrypoint for the Go system. The current docs
and code all point to the same model:

- the daemon is the source of truth for the Hydrus SQLite bundle
- the daemon owns managed `client_files`
- the daemon owns background jobs such as PTR work
- clients talk to the daemon over APIs instead of touching SQLite directly

That is why the thin desktop prototype is described as a client of `hydrusd`
rather than as the primary application. The desktop shell exists to exercise the
daemon contract, not to replace the old Python monolith in one step.

Bootstrap is also daemon-owned. `hydrusd` can now create a fresh canonical
client bundle in Go when `HYDRUS_GO_DB_DIR` points at a missing or empty target,
and plain `hydrusd` now defaults to seeding `./db` when no DB directory is
configured unless bootstrap is explicitly disabled. The old Python bootstrap
knobs were removed because first-start bootstrap no longer shells out to
Python. At the same time, the daemon does not yet claim general in-place
migration or repair of arbitrary existing bundles; partial or invalid bundle
states still fail fast.

This daemon-first model is what makes local and LAN clients on supported desktop
platforms feasible. The backend owns state once, and every client can consume it
through the same API surface.

The current repo documentation explicitly calls out Linux and Windows desktop
build targets for the thin client and treats the client as a consumer of
`hydrusd`, not as a peer that owns state itself. The same architectural split is
why the project can support additional client platforms later without moving
SQLite ownership back into the UI.

## Current progress snapshot

The current project state is best read as an incremental daemon-first migration.
Based on [STATUS.md](./STATUS.md), the repository already has the headless
daemon bootstrap, DB-backed read slices, writable local import/trash foundations,
PTR sync-in foundations with definition/content application, and the first
daemon-backed local-library search slice.

The desktop prototype exists to validate the daemon contract, but the project is
still intentionally short of full Hydrus parity. The main remaining work is in
broader daemon-side search semantics, richer mutation flows, fuller PTR sync-out
behavior, and the larger downloader/parser surface.

When older roadmap text and the live branch diverge, use
[NEAR_PARITY_TRIAGE.md](./NEAR_PARITY_TRIAGE.md) as the short-horizon checkpoint
because it was written specifically to reconcile current code against older
status language.

## Most recent recorded implementation checkpoints

The most recent implementation milestones already recorded in
[STATUS.md](./STATUS.md) now extend beyond the earlier 2026-04-26
near-parity-search checkpoint. In particular, the 2026-05-03 entry captures the
recent PTR continuity, existing-DB retry/resume hardening, invalid repository
definition compatibility fallback, clearer daemon-side `is_complete` vs
`is_up_to_date` PTR status semantics, and the refreshed docs/OpenAPI surface.

Those entries are still milestone log snapshots, not a claim of full Hydrus
parity. When in doubt, read the latest dated entry in `STATUS.md` first and use
the live code as the final authority.

## What the recent documentation pass actually did

The recent pass that produced this document was a read-only analysis and
documentation sweep. It reviewed the docs tree, the top-level hydrus-go project
documents, the `hydrusd` startup/bootstrap path, and the recent session history
so the current repo state could be described without guessing.

That pass did not change the daemon behavior. It did not modify backend logic.
It also did not make UI changes. Existing Fyne milestones recorded elsewhere in
[STATUS.md](./STATUS.md) remain historical implementation notes, not new work
created by this documentation pass.

The point of this file is to give future work a stable orientation document: how
to navigate the docs, how to understand the daemon-first migration stance, and
how to avoid reading the Go effort as a literal 1:1 rewrite of the Python code.
