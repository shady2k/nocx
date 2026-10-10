# ADR-0084 — Native saves pin the destination and join cleanup

- **Status:** Accepted
- **Date:** 2026-10-09
- **Decided by:** PR #259 hardening implementation, requested by the owner.
- **Supplements:** [ADR-0083](0083-native-download-commit-is-confirmed-after-the-client-promotes.md).
- **Related:** `nocx-9le.8.4`.

## Context

A pathname selected by the native dialog is not a durable destination capability. Resolving its parent again after the dialog allows a rename and symlink replacement to redirect creation, promotion or cleanup. Creating a replacement as an ordinary upload also widens a private file under umask 022. Cancellation alone does not prove cleanup: shutdown can return while the sink still owns a temporary file.

## Decision

Prepare opens the selected parent with `os.OpenRoot` and retains it with one filename. All subsequent temporary creation, atomic replacement and removal are relative to this root. The rooted filesystem adapter reuses the existing `transfer.Sink`, including its copy bounds, sync, promotion and cleanup; there is no second receive loop. The receiver consumes an injected destination interface; the composition root supplies the local implementation.

Native temporary and final files use mode 0600, including replacements of publicly readable files. Native saves are private by default; preserving an old public mode is deliberately not part of this operation. Ordinary upload creation and its umask behavior remain unchanged.

Shutdown prevents new saves, interrupts their HTTP readers and joins accepted saves outside the registry mutex, including already discarded saves. It does not wait for a non-cooperative OS prompt; a prompt returning after shutdown cannot publish a capability. Prepared cancellation releases the root and retains a bounded, expiring cancellation fact, so a racing Save reports cancelled rather than a source failure.

ADR-0083's accepted-outcome rule also applies when the timeout arm fires: ACK acceptance and the terminal decision are ordered by the same transfer mutex. An accepted saved ACK wins; a completed timeout refuses a new saved ACK. Cancellation before GET has no possible local commit and settles without waiting for an ACK timer. Matching cancelled completion after server-known cancellation is idempotent and emits no second terminal account. Once delivery starts, the existing confirmation/uncertainty policy remains.

## Consequences

An output remains tied to the selected directory even if its original name changes. Invalid or unavailable parents fail during Prepare. Successful Prepare cannot guarantee future write permission or available disk space; later failures still preserve the existing destination and report a destination failure.

The desktop process may wait for active filesystem close/cleanup during shutdown. This is necessary to avoid exiting with owned temporary files. Destination paths, handles, filesystem errors and bytes remain off the WebSocket; no credential, session, transport-plane or browser attachment policy changes.
