# ADR-y45sp41c — A stored-row read is a page with a slot, and the renderer reads what is on screen first

- **Status:** Accepted (owner decision, 2026-11-10, `nocx-large-block-read-response-queue-overflow-t7xhe`)
- **Date:** 2026-11-10
- **Refines:** [ADR-8eej3e66](8eej3e66-every-operation-is-catalogued-with-its-lifetime-and-busy-is-not-an-answer.md)
  §7 for one internal retry. It does not supersede that record: a person's calls are still
  never refused for capacity, and the refusal below never reaches a person.
- **Related:** [ADR-2yzsq8mk](2yzsq8mk-a-normal-command-is-one-column-and-a-full-screen-program-owns-the-width.md)
  (output never jumps; a scrolled-up reader is never moved), [ADR-0019](0019-a-ledger-is-the-one-authoritative-memory.md)
  §6/§7 (the recall read does not haul bytes), `nocx-2v80t.3.7` (streamed block rows).

## Context and problem

A reconnect asks the renderer to refresh every mounted block. It did that by reading each
block's whole stored body: `frontend/src/terminal-content.ts` looped over every mounted
entry, `blockRowsForEntry` fetched `ledger.get` and then the entire `application/x-nocx-rows`
artifact, and the responses shared one connection.

The response queue is 64 frames (`internal/transport/outbound`), and overflow closes the
connection. One 5000-row artifact measures 353,052 bytes as a JSON-RPC envelope; 64 of them
are well inside the 32 MiB byte budget and still overflow by **count**. A transcript of 500
blocks is 500 concurrent requests even when not one command is large. The overflow policy
did what it is designed to do — it closed a socket it could not drain — and the person saw
the Connection status modal for a refresh they never asked for.

The duplicate fix (`2648f1b4`) coalesced two reads of ONE entry. It could not help N
different entries, which is the ordinary shape of a reconnect.

## Decision

The owner's decision of 2026-11-10, in four parts.

### 1. A stored-row read is a page

`ledger.artifact` accepts `fromRow` and `rowLimit` on a stored-rows artifact and answers one
page: the rows at or after `fromRow`, at most `rowLimit` of them, with the artifact's own
`firstRow`, `lastRow`, `rowCount`, `rangeStart`, `rangeCount`, `hasMoreBefore` and
`hasMoreAfter`. A body that is not stored rows has no row page, and a range on one is
invalid params.

The whole-artifact coordinates ride with every page because the renderer reserves the
block's height from `rowCount` **before any page arrives**. A reconnect therefore reads what
is on screen first, then older blocks newest-to-oldest, and within a large block only the
rows in and around the viewport; more pages follow the person's scrolling. Nothing moves
when they arrive: the height was already there.

A closed block's payload carries `rowCount` and `firstRow` so a restore can place the block
without reading a byte of it. A store written before those fields keeps the old whole-body
read for that block; the fallback is per block, not a second code path.

### 2. The server holds a slot until the RESPONSE has left

One connection has four stored-row read slots (`RowReadSlots`). A paged read takes one
before it asks the store and gives it back only when its response has left on the socket —
written, or dropped with the connection — never when the handler returns. Enqueueing is not
delivery, and the queue depth is exactly the thing that overflowed.

With no slot free the server answers **at once** with a JSON-RPC error: code `-32014`,
`data.retryAfterMs` (`contracts/row.readDeferred.schema.json`). It does not queue the request
without bound, does not stop reading the socket (the read loop is untouched), and does not
close the connection. Four is deliberately below both the 64-frame critical queue and the
content lane's eight permits.

Whole-body reads are not on this path. The product's block reader pages every stored-rows
artifact, so only a page can grow the queue; a diagnostic read of a whole body keeps the
shape it had.

### 3. The renderer has its own priority queue

`frontend/src/scrollback/row-read-queue.ts` owns stored-row reads per connection. It keeps at
most two in flight, shares one read between callers of the same key, and orders the rest by
what is actually needed now: visible first, then the transcript newest to oldest. The order
is read at every decision, so a block that scrolls into view overtakes one that has not.

On `-32014` the queue waits the stated time and then takes the **most urgent request it holds
at that moment**. It does not re-send the same request in a loop. The person never sees the
refusal; a refusal here is a scheduling fact, not a busy state.

### 4. Live output and input are not on this path

PTY bytes, input, notifications, and every call a person makes remain what they were. This
decision touches only the durable stored-row read that a reconnect or restore performs.

## Considered alternatives

**Raise the 64-frame queue.** Rejected: the queue is a bound on memory and on how long one
stuck renderer can make the server hold frames. A larger number buys a larger transcript
before the same modal appears and weakens the bound everywhere else.

**Drop the response instead of closing.** Rejected on ADR-8eej3e66's own rule: a response is
half a promise, and dropping it strands a caller. Silence is the one outcome that must not
survive.

**One slot per connection.** Workable, and the smallest bound, but four lets a visible block
and the one above it land in the same frame without four visible blocks competing. The
number is a bound, not a promise; the renderer's queue is what makes it invisible.

**Read whole bodies, but serialize them.** Rejected: it fixes the frame count and leaves the
bytes. A 5000-row command would still cross the socket in full for a page's worth of what the
person can see, on every growth notification.

## Consequences

- The protocol surface changed: a new error and error-data schema, new result fields, new
  params. `ProtocolVersion` is 24 and `testdata/protocol-surface-v24.golden` is the new
  comparison point.
- A reconnect briefly shows a large block at its reserved height with its body absent until
  its page lands. That is the decision, and it is why the reservation is exact.
- `ledger.artifact`'s paged form scans the artifact's chunks to locate a page. The scan is
  the store's cost and the response is the page; the queue is what the bound is about.
- The internal refusal is the one capacity-shaped answer in the protocol, and it is
  explicitly not an answer to a person. Any future surface that wants to show it has
  misread this record.
