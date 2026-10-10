# ADR-0083 — A native download is complete only after the client promotes it

- **Status:** Accepted
- **Date:** 2026-09-28
- **Decided by:** approved implementation plan
- **Amends:** [ADR-0037](0037-an-http-download-route-beside-the-websocket.md), the response framing and browser-only destination consequences.
- **Reads, does not change:** AD-1, AD-3, AD-6, AD-8; [ADR-0026](0026-control-plane-runs-off-the-read-loop.md); [ADR-0036](0036-an-http-upload-route-beside-the-websocket.md); ADR-0011.
- **Related:** `nocx-9le.8.4`.

## Context

`files.download` already authorizes and pins a remote source, while `GET /download/{ticket}` streams its bytes beside the WebSocket. That response currently treats completion of the HTTP source stream as completion of the user's download. It is sufficient for a browser attachment, where HTTP owns the receive operation, but not for Linux desktop: the WebView cannot reliably save the response, and a native destination must be written atomically by the desktop process.

A native receiver has two distinct commit points. The server can prove that the remote source ended successfully; only the desktop can prove that the local temporary file was synced and atomically promoted. Declaring `files.downloadDone: sent` at the first point would publish a false success if local writing, sync, or rename then failed. Conversely, making the HTTP handler wait for the desktop acknowledgement would deadlock: the desktop cannot acknowledge until it consumes the HTTP EOF.

## Decision

1. **Two receivers share the existing transfer registry and ticket.** An optional `destination: "native"` on `files.download` selects native delivery; absence retains the browser attachment behavior. Ticket ownership, origin checks, one-shot claim, TTL, remote source, and bounded streaming remain shared. No second source loop, ticket store, or WebSocket byte path is introduced.
2. **The native HTTP response reports only source completion.** It has no `Content-Length`, declares `X-Nocx-Download-Status` as a trailer before the body, and ends with exactly `sent`, `failed`, or `cancelled`. The status is `sent` only after the complete source read—including errors after the advertised size—has succeeded. The handler waits for that source stage, not for final transfer settlement. The browser response keeps its existing attachment and length framing.
3. **The desktop owns local commit.** It consumes the response through the existing atomic `transfer.Sink`; trailer validation is part of the reader, before sink sync and promotion. A local destination failure cannot promote a partial file. File data never enters JSON, and a local destination path or opaque native handle never crosses the WebSocket.
4. **A narrow completion RPC settles native accounting.** `files.downloadComplete` accepts only a transfer id and one of `saved`, `cancelled`, `source-failed`, or `destination-failed`. The first accepted outcome wins; equal duplicates are idempotent. `saved` is accepted only after successful source completion. The transfer worker alone maps this client confirmation plus source result into the existing retained `files.downloadDone` and `transfer.finished` terminal records.
5. **Timeout is uncertainty, not proof of failure to write.** If local completion is not confirmed within 30 seconds after source completion, the backend settles failed with a fixed message stating that completion was not confirmed. It makes no claim that a file is absent: a client may have committed and lost its acknowledgement.
6. **Transport work remains off the read loop.** Completion uses the existing bounded submission lane and a strict typed decode. The read-loop handler performs only validation, ownership/state checks, and a nonblocking outcome handoff. The desktop serializes all OS file prompts through one bounded admission gate; an uninterruptible native prompt keeps its permit until the platform call actually returns.

## Consequences

- The terminal account reflects the local atomic commit rather than merely successful remote reads; browser accounting remains tied to the existing streamed attachment behavior.
- Native cancellation, source failure, and local destination failure are distinct, and the wire carries no local path or platform error detail.
- The native HTTP body remains bounded-memory and cannot declare success before the source result is known.
- A lost completion acknowledgement may end as "not confirmed" even if the file was committed. This is intentional; retries of the one-shot HTTP ticket and false success are both refused.
- The desktop host remains a thin Wails adapter around an injected receiver; the transfer engine and authorization remain in the Go core.
