---
title: Upload — a file from here onto the machine the active tab is on
status: draft
created: 2026-08-21
bead: nocx-9le.5 (rescoped to upload), brainstorm nocx-0vc5l
builds-on: .internal/specs/2026-08-06-file-manager-design.md
---

# Upload — design

## 0. The one rule

**A file can only be uploaded to the machine the tab is actually on, and the wrong pairing
is not expressible.**

This is the file manager's rule (`§0` of the file-manager design — "the panel shows files of
the machine you are currently in") applied to a consequential write. It is not restated as
renderer discipline; it is a property of the addressing. An upload is addressed by a
`bindingId`, the same backend-issued address `files.list` uses, and the write seam exists
only on the remote provider. So:

- A tab running a local shell has a **local** binding, which carries no write seam, and
  `files.upload` against it is refused by the transport before any handler runs.
- A tab where somebody typed `ssh srv-01` by hand is a `session.KindLocal` session
  (`nocx-520z`) — its binding is the local one, so the refusal is the same refusal, reached
  the same way. Nobody has to remember to check "is this really an SSH session"; there is no
  check to forget, because a local binding has nothing to write through.

That the honest refusal for the hardest case falls out of the addressing rather than out of a
UI condition is the reason the design is shaped this way.

## 1. What a person can do that they could not before

**Put a file from here onto the host their active tab is connected to, by dragging it onto
the terminal or picking it in the Files panel, and watch it arrive.**

The end-to-end check that watches them do it is in `§7`.

## 2. Where this came from

`nocx-9le.5` has owned "upload, download and drag-drop transfer" since the file-manager
design narrowed it on 2026-08-01; listing and reading moved out so two epics would not build
directory listing twice. Everything under it is now built: `internal/filesystem` with a
`local` and an `sftp` provider, `ssh.FSConn` as the SFTP lease on the tab's pooled
connection, `files.{open,list,read,watch,close,reveal}` on the wire with schemas, the Files
panel, and automatic refresh. What is missing is the write.

Tabby was read as prior art (`/home/dev/repos/tabby`) and is named in `§3` where it is
followed and where it is deliberately not.

## 3. Decisions

**D1 — One sink, two sources, and the source is chosen by the gesture.** The engine that
writes a remote file takes an `io.Reader`; how that reader is obtained is a separate
question with two answers. A file picked in a picker, or dropped into a Wails window, is
named by an absolute path on the **backend's** machine and is opened there. A file dropped
into a browser tab exists only as a `File` in the renderer and its bytes are streamed. No
heuristic decides which — the gesture already knows, because a path either exists or does
not.

> Rejected: detecting "is the backend co-located?" and picking a source from that. `dev-web`
> on `localhost` is co-located and has no Wails; the desktop app is co-located and has it.
> The predicate is not the one that matters, and a wrong answer silently reads the wrong
> machine's disk.

**D2 — Bytes travel as a streamed `POST`, not as a new binary msg-type.** The data plane
carries PTY I/O over one TCP connection; a multi-gigabyte upload multiplexed onto it
competes with terminal responsiveness, and would need a flow-control scheme invented for the
purpose. An HTTP request is a separate connection whose backpressure is TCP's. It also keeps
`frame.go`/`frame.ts` — two codecs pinned to each other by golden vectors — about PTY only.
`internal/transport` already runs an `http.ServeMux` (`/session`), so the surface exists.

This crosses AD-1, which allocates the planes on the WebSocket. **ADR required**, recorded
with this design rather than decided inside a commit.

**D3 — The upload endpoint is authorised by a one-shot ticket, not by the connection.** A
`POST` cannot present the per-launch capability token the way the WebSocket does (a
subprotocol on upgrade). So `files.upload` mints an opaque ticket — the pattern `storePlan`
already uses — bound to the binding, the resolved destination and the declared size, one-shot
and short-lived. The request body carries no path: the destination is not expressible by the
caller, so a stolen ticket cannot redirect a write.

**D4 — The collision question is asked before a byte moves.** `files.upload` stats the
destination first and refuses with a typed `collision` outcome when a decision was not
supplied. Uploading a gigabyte and then asking is disrespectful of the person's time and of
the link.

> Not tabby: `SFTPSession.upload` overwrites silently and never asks.

**D5 — Replace atomically where the server allows it, and never leave the destination with
nothing without saying so.** Write to `<name>.nocx-upload-<rand>` in the destination
directory (same filesystem, so a rename is a rename), then `PosixRename`
(`posix-rename@openssh.com`), which replaces atomically. On a server without the extension,
SFTP v3 `rename` refuses an existing destination — this is `nocx-340t`, already paid for
once — and the fallback is `rename(dest → dest.nocx-bak)`, `rename(temp → dest)`,
`unlink(bak)`.

> Not tabby: theirs is `unlink(dest)` then `rename(temp, dest)`, which leaves a window with
> no file at all and the old content already destroyed. Ours leaves the old content on disk
> under a named path for the whole window.

**D6 — Upload is not a `Provider` method.** `filesystem.Provider` is read-only by contract,
and its own documentation says a mutating method must land on **both** providers. A local
`Write` has no caller in this design (a local tab inserts a path, it does not copy), and a
write path with no caller is exactly the `nocx-rtg0` failure — a package reachable through
its read half while its write half is dead. So the seam is optional and discovered by type
assertion on the binding's provider, the way `filesystemEndpointAttester` already makes
`files.reveal` local-only. `Provider` stays read-only; the remote provider gains `Uploader`;
the local one does not, which is `§0`.

**D7 — A local tab's drop inserts the path; it does not copy.** Every terminal does this, and
copying a file onto the machine it is already on is not a thing anybody asked for. This is
the same gesture meaning the thing it means in the context it lands in — not two surfaces
owning one input.

## 4. Scope

### In

- Upload of one or more **files** onto the host of the active tab.
- Two gestures, and only two: **drop onto the tab's terminal** (target: that tab's cwd, the
  same OSC 7 value the Files panel already follows — `nocx-r3bz`) and an **action in the Files
  panel** (target: the folder the panel is showing). Dropping onto an individual folder row
  was considered and left out: it is a third target rule for no gesture the owner asked for.
- Both sources (D1), so the feature works in the desktop app, in `dev-web`, and against a
  networked backend from the first commit.
- Per-transfer progress, speed and cancellation.
- The collision decision, with apply-to-all across a multi-file drop.
- Honest refusal on a tab that has no remote (`§0`).

### Out — each a refusal, not an omission

- **Directories.** Recursive walk, `mkdir`, partial failure mid-tree, symlinks and modes are
  a materially larger problem on the same mechanism. Next wave of the same epic.
- **Download.** The reverse direction reuses the sink's shape but is its own surface (a save
  dialog, a local write path, a different set of failures). Sibling bead.
- **Resume after a broken link.** SFTP can seek, so this is buildable; it needs a durable
  record of a partial transfer, which nothing here has.
- **A queue with several transfers in flight.** One at a time per binding; a multi-file drop
  is sequential. Concurrency is a bounded-resource question against a shared SSH connection
  and does not belong in the first slice.
- **Preserving the source file's mode.** The uploaded file gets the server's default. Tabby
  carries `getMode()`; we have no local mode at all in the stream source, so carrying it in
  one source and not the other would be worse than not carrying it.

## 5. Architecture

### 5.1 The lease gains a write half — `internal/ssh`

`FSConn` today exposes `ReadDir`, `Stat`, `Lstat`, `ReadLink`, `RealPath`, `ReadFile`, plus
`Done`/`LostErr`/`Close`. It owns the lease, the bounded lane, and the cancellation-by-closing
semantics that the whole file manager depends on. The write methods belong there and nowhere
else:

- `Create(path string) (io.WriteCloser, error)` — opens for write, exclusive-create.
- `PosixRename(old, new string) error` — the extension; reports unsupported distinguishably.
- `Rename(old, new string) error` — plain v3 rename, refuses an existing destination.
- `Remove(path string) error`.

Same lane, same hard timeout, same poisoning on a wedged call. A write in flight when the
connection drops is unblocked by the lease closing, exactly as a read is.

### 5.2 The sink — `internal/transfer`

A new package, because writing a remote file with progress, cancellation, a temp name and a
replace strategy is a behaviour nothing currently owns.

```go
type Upload struct {
    Dest      string    // absolute, provider syntax
    Size      int64     // declared; the sink refuses a reader that disagrees
    OnExists  Decision  // Overwrite | KeepBoth | Skip
}

type Sink interface {
    Put(ctx context.Context, u Upload, r io.Reader, progress func(n int64)) (Outcome, error)
}
```

`KeepBoth` resolves the name before the transfer starts, not after: `report.pdf` becomes
`report-1.pdf`, incrementing until a name is free, and the resolved name is what the ticket is
bound to. Resolving it afterwards would mean a second collision could appear during the
transfer and the person would be asked twice about one file.

`Put` is the only place that knows the temp name, the two replace strategies and the cleanup.
It is given a reader and does not know or care which source produced it — which is what makes
D1 one implementation rather than two.

`filesystem.Uploader` is the optional seam (D6) and has exactly one method,
`Sink() transfer.Sink`. The sftp provider implements it, returning a sink over its own
`FSConn`; `endpointAttestedProvider` forwards it, as it forwards the attestation;
`local.Provider` does not implement it at all, so the type assertion in the transport fails
and `files.upload` is refused. That failed assertion is `§0`.

### 5.3 Wire — control plane

`files.upload` (params: `bindingId`, `destDir`, `name`, `size`, `source`, optional
`sourcePath`, optional `onExists`). Authorised exactly like every other `files.*` call:
`Registry.Acquire` re-checks that the binding's session is in the requesting connection's
`connState`. Three outcomes:

- `{"collision":"exists"}` — nothing started; the renderer asks and calls again with
  `onExists`.
- `{"transferId":"…"}` — `source:"path"`; the backend opened the file and the transfer is
  running.
- `{"transferId":"…","ticket":"…","url":"/upload/…"}` — `source:"stream"`; the sink is waiting
  for a body.

`files.uploadCancel` (params: `transferId`). Idempotent; cancelling a finished transfer is
not an error.

`files.uploadProgress` — notification: `transferId`, `bytes`, `total`. Addressed the way
`files.changed` is: resolved at emit time from the binding's session's current subscriber,
never from the connection that started it, so a reconnect mid-transfer does not lose the
progress feed. Coalesced — at most one in flight per transfer.

`files.uploadDone` — notification: `transferId`, `outcome` (`"written"`, `"skipped"`,
`"cancelled"`, `"failed"`), `error` when failed, and `strandedPath` when a temp or a `.bak`
was left behind (`§6`).

Contracts for all four in `contracts/`, `additionalProperties:false` with explicit
`required`, renderer types generated, Go validated, and one `…_OverTheWireConformsToContract`
per result — the real payload off the real socket.

### 5.4 Wire — the upload endpoint

`POST /upload/{ticket}`, on the existing mux, behind the existing `OriginPolicy`.

- The ticket is claimed on first read; a second `POST` with the same ticket is `409`.
- Unknown, expired or already-claimed ticket: `410`, and the transfer it named is cancelled
  and cleaned.
- `Content-Length` must equal the size declared at mint time; a mismatch is `400` before the
  body is read.
- A body that ends short of `Content-Length` fails the transfer; the temp is removed and the
  destination is never touched.
- A body that exceeds it is cut at the bound and the transfer fails the same way.
- Ticket TTL is short (60 s to _begin_); once the body starts flowing the transfer's own
  timeouts govern, not the ticket's.

### 5.5 Frontend

**Drop targets.** The terminal element and each folder row become drop targets. In a browser
the drop yields `File` objects (source `stream`). In the Wails window, `EnableFileDrop` plus
`data-file-drop-target` delivers **absolute paths and the drop target's attributes** to the Go
side; the terminal element carries the tab's session id as an attribute, so the backend
resolves the destination without a round trip (source `path`).

> `EnableFileDrop` is a window option that is not currently set. Turning it on changes how
> the window handles OS drags app-wide; the tab-strip's own HTML5 drag (`strip-drag.ts`) is a
> DOM drag with no files and must be verified unaffected. Named here as a risk, with a test.

**Picking.** The native `dialog.openFile` where Wails exists; the routed directory/file
picker that `nocx-ult5v` is building over `files.list` where it does not. Both yield a
backend-local path, so both are source `path`.

**Progress.** One kit component per transfer, driven by `files.uploadProgress`. The Files
panel header is already over-full (`nocx-a8cz`) — the Upload action lands there only once
that bead has decided how the header overflows, and this design does not add a seventh
button to a header that cannot hold six.

**Refresh.** Nothing new: on `written`, the destination directory is invalidated and the
existing `files.changed` path re-lists it. If it were not already built, an upload that did
not appear in the tree would be the bug people reported.

## 6. Failure paths, and the invariants as intervals

For every external call there is a test where that call fails. Enumerated, with what is true
on the remote disk afterwards:

| Failure                                         | Destination | Temp                                                    | What the person is told                                             |
| ----------------------------------------------- | ----------- | ------------------------------------------------------- | ------------------------------------------------------------------- |
| `Create` refused (permission, read-only, quota) | untouched   | never existed                                           | the reason, in place                                                |
| Write fails mid-stream (disk full, I/O)         | untouched   | removed                                                 | the reason                                                          |
| Connection lost mid-stream                      | untouched   | **leaked** — the lease is gone, so it cannot be removed | `strandedPath` names it                                             |
| Source read fails / ends short                  | untouched   | removed                                                 | the reason                                                          |
| Ticket expires before the `POST`                | untouched   | never existed                                           | transfer cancelled                                                  |
| `PosixRename` unsupported, fallback runs        | replaced    | consumed                                                | nothing; this is normal                                             |
| Fallback fails between the two renames          | **missing** | consumed                                                | `strandedPath` names `<dest>.nocx-bak`, which holds the old content |
| Cancelled by the person                         | untouched   | removed                                                 | cancelled                                                           |

**Invariant, both ends named.** From the moment `Put` starts until it returns, the
destination path holds either the previous file or the new one — **except** on a server
without `posix-rename@openssh.com`, where between `rename(dest → bak)` and
`rename(temp → dest)` it holds nothing and the previous content is at `<dest>.nocx-bak`. That
window is the price of the extension being absent, it is bounded by one round trip, and when
a failure lands inside it the outcome names the `.bak` path rather than reporting success.

**Invariant, both ends named.** The temp file exists from its creation until either the
rename consumes it or a failure path removes it. The one span where neither can happen is a
lost connection, and that is the only case that leaks — reported, never silent.

**And the paired success assertion.** For every "returns an error when…" above there is a
test that on an ordinary server the upload succeeds — including one that the `.bak` fallback
itself completes and leaves the new content in place with no `.bak` behind.

## 7. Testing

**The end-to-end check (`§1`).** Against `cmd/e2e-sshd`, which already serves the SFTP
subsystem and is already asserted to advertise `posix-rename@openssh.com`: open an SSH tab,
drop a file onto its terminal, and assert the file appears in the tree and its bytes on the
far side match. That is a person doing the thing, watched.

It exercises the **stream** source, and that is forced rather than chosen: the e2e suite runs
the headless path, where there is no Wails, therefore no native picker and no
`EnableFileDrop`. Playwright constructs a `File` and a `DataTransfer` in the page, which is
exactly what a browser drop produces. So the source that CI can watch end to end is the
source `dev-web` uses.

**The path source therefore has no e2e, and this is stated rather than glossed.** It is
covered by the sink's unit tests (the reader is a file either way) and by transport tests for
`source:"path"`. What no automated check in this repo can watch is the Wails file-drop
delivering a real OS path — that needs the desktop app, and it is the one manual step in the
epic's DONE WHEN. Writing it down is the point: the last time a wave claimed coverage it did
not have, it was because the gap was known and unrecorded (`nocx-rtg0`).

**Unit, backend.** The sink against a fake `FSConn` (`internal/filesystem/sftp/fsfake_test.go`
is the precedent) covering every row of `§6`, including a fake that refuses
`posix-rename@openssh.com` so the fallback is exercised where the real fixture cannot.

**Unit, transport.** Ticket lifecycle: one-shot, expiry, size mismatch, short body, long body,
wrong origin, released binding. `files.upload` against a **local** binding is refused — the
test that proves `§0`.

**Unit, frontend.** The collision dialog's apply-to-all across a multi-file drop; a drop on a
local tab inserts the path and starts no transfer; progress rendering from notifications.

**Contract.** Four schemas, generated renderer types, Go conformance, and over-the-wire
conformance for `files.upload`.

**Reachability.** `deadcode -tags gtk3 -whylive` on the sink's `Put` must print a path from
`main`, and the contrast probe on an unwired sibling must print the reflection answer. The
`-filter` form is not evidence here and is not used.

## 8. Bead changes

- `nocx-9le.5` — rescope to **upload only** and make it the epic this design implements;
  acceptance is `§1`'s sentence plus the e2e check. It is currently `P3`; raise it to match
  the wave being worked.
- New sibling under `nocx-9le` — **download**, carrying the half removed above.
- New sibling under `nocx-9le` — **directory upload**, blocked by the upload epic (same
  files).
- `nocx-a8cz` — gains a `blocks` edge onto the upload epic's Files-panel task: the header
  cannot take the Upload action until it has decided how it overflows.
- ADR for D2 (an HTTP upload route beside the WebSocket, against AD-1's plane allocation).

## 9. Open questions

None blocking. Two to settle inside implementation:

1. Whether `EnableFileDrop` disturbs the tab strip's own drag. Verified by test in the first
   task; if it does, the drop-on-terminal gesture uses browser `File` semantics in the
   desktop build too, which costs the path source that gesture but changes nothing else.
2. Where the transfer list lives once there is more than one — deferred with the queue.
