---
title: Upload — a file from here onto the machine the active tab is on
status: draft
created: 2026-08-21
revised: 2026-08-21 (codex adversarial review — see §10)
bead: nocx-9le.5 (rescoped to upload), brainstorm nocx-0vc5l
builds-on: .internal/specs/2026-08-06-file-manager-design.md
---

# Upload — design

## 0. The two rules

**R1 — A file can only be uploaded to the machine the tab is actually on.**

This is the file-manager rule (`§0` there: "the panel shows files of the machine you are
currently in") applied to a consequential write. It is not restated as renderer discipline; it
is a property of the addressing. An upload is addressed by a `bindingId`, and the only route
to a filesystem is the `Handle` that `Registry.Acquire` returns after re-checking that the
binding's session belongs to the requesting connection. A binding whose provider cannot write
refuses, the way a local `Watch` refuses with `ErrWatchUnavailable` rather than returning a
channel that never fires — it holds no sink, so there is no check to forget.

**R1 forbids the WRONG host; it does not forbid this one.** A local tab's binding views the
backend's own machine, which is exactly the machine that tab's shell is on, so uploading into
its cwd satisfies R1. That is what a browser drop on a local tab does, and D9 says why. What R1
refuses is a write addressed to a machine the tab is not on, and the addressing is what makes
that inexpressible: the binding names one session's filesystem and nothing else.

> **This paragraph corrects an earlier draft of this section.** It said "a local binding's
> `Upload` refuses", and gave a tab where somebody typed `ssh srv-01` by hand — a
> `session.KindLocal` session (`nocx-520z`) — as the case it protects. The first half was a
> consequence of D9's original form and is gone with it; the second is a real and unchanged
> concern, and it is one the addressing does NOT answer: such a tab's binding views the backend
> while its shell is on another host, and neither the binding nor R1 can tell. What refuses the
> drop there today is weaker and renderer-side: walking into a hand-typed `ssh` starts a fresh
> integration domain whose environment record is blank
> (`frontend/src/lifecycle/domain-environment.ts`), so the origin carries no verified cwd and
> `§5.5`'s unverified-cwd refusal fires. That holds until the far shell emits its own OSC 7,
> which attributes a REMOTE path to a tab whose binding is local. The same shape predates this
> change on the remote side — an `ssh` tab walked by hand onto a second host uploads to the
> first — so it is a standing gap in R1's coverage rather than one this correction opened. Named
> here so the next reader knows it is a live question; tracked outside this spec.

**R2 — The renderer may name the destination. It may never name the source.**

A destination is scoped by a binding the backend issued. A _source path_ is not scoped by
anything: it is a path on the backend's own disk, and a renderer that could supply one could
ask the backend to read `~/.ssh/id_ed25519`, the vault, or any file the backend user can read,
and send it to a host of the renderer's choosing. Binding ownership proves which terminal the
caller owns; it proves nothing about the backend's filesystem.

Therefore `files.upload` has no `sourcePath` parameter. A source that lives on the backend's
disk is named by a **source ticket** minted backend-side at the moment a human chose the file —
in the native picker, or by dropping it on the window — and handed to the renderer as an
opaque id it can echo but not author. What the renderer cannot spell, it cannot ask for.

> R2 is the finding this design most needed. The first draft accepted `source` and
> `sourcePath` from the wire and claimed the gesture made the wrong answer inexpressible. The
> gesture does no such thing: a parameter is a parameter.

## 1. What a person can do that they could not before

**Put a file from here onto the host their active tab is connected to, by dragging it onto the
terminal or picking it in the Files panel, and watch it arrive.**

The end-to-end check that watches them do it is in `§7`.

## 2. Where this came from

`nocx-9le.5` has owned "upload, download and drag-drop transfer" since the file-manager design
narrowed it on 2026-08-01. Everything under it has landed since: `internal/filesystem` with a
`local` and an `sftp` provider, `ssh.FSConn` as the SFTP lease on the tab's pooled connection,
`files.{open,list,read,watch,close,reveal}` on the wire with schemas, the Files panel, and
automatic refresh. What is missing is the write.

Tabby was read as prior art (`/home/dev/repos/tabby`) and is named in `§3` where it is followed
and where it is deliberately not.

## 3. Decisions

**D1 — One sink, two sources, and the source is named by a ticket either way.** The engine that
writes a remote file takes an `io.Reader`. Where that reader comes from is a separate question
with two answers: a file on the backend's disk, opened there, or a stream of bytes from the
renderer. Both are addressed by an opaque backend-minted ticket (R2), so the two differ in
plumbing and not in authority.

**D2 — A transfer runs on its own lease, never the tree's.** `fsConn.run` holds one of four
lane slots for the duration of its callback and arms a watchdog that poisons the whole lease if
the callback has not returned in `fsHardTimeout` — 30 seconds
(`internal/ssh/ssh_fsconn.go:115`, `:123`, `:352`). An upload is not a wedged call and routinely
outlives that. Two shapes were therefore rejected: one lane call spanning the whole
open-write-close (any upload over 30 s poisons the lease and takes the Files panel down with
it), and a naked handle returned from a lane call (every subsequent write escapes the lane, the
timeout, the classification and cancellation entirely).

What we do instead: the transfer takes its **own** `FSConn` lease. `FSConn` already owns its own
pooled reference rather than the tab's, and shares the tab's connection when the pool key matches
(AD-4) — a second lease is the mechanism working as designed, not a workaround. On that lease,
**each chunk write is one lane call**, so no call ever escapes the lane and the existing
watchdog is exactly right: a chunk is bounded, and 30 seconds for a bounded chunk means the
link is gone. A poisoned transfer lease kills the transfer and nothing else.

Liveness of the transfer as a whole is a **stall** rule — no chunk has completed for N seconds —
never a rule about total duration. A 2 GB upload over a slow link is a working upload.

**D3 — Bytes travel as a streamed `POST`, not as a new binary msg-type.** The data plane carries
PTY I/O over one TCP connection, and an upload runs renderer→backend, the same direction as
keystrokes: bulk PTY output is the other direction and never queues ahead of input, whereas an
upload on that socket would. A `POST` gives the transfer its own independently flow-controlled
byte stream — the sender stops when the sink stops reading, one connection's window against
another's, and the backend's single read loop is not in the path at all. Multiplexing it onto
the WebSocket would require the missing half to be invented: application-level credit in the
client→server direction, an ack for it, a chunk sequence and reconnect semantics, all of it
existing so that a full upload queue cannot block the one read loop that carries every
session's keystrokes and cancellations. `WebSocket.bufferedAmount` is not a substitute — it
reports what the browser has queued, not whether the backend read it, whether its bounded
queue has room, or whether the far filesystem accepted the last chunk. It also keeps
`frame.go` and `frame.ts` — two codecs pinned to each other by golden vectors — about PTY
only. `internal/transport` already runs an `http.ServeMux` (`/session`), so the surface exists.

This crosses AD-1, which allocates the planes on the WebSocket. **ADR required**, recorded with
this design rather than decided inside a commit.

**D4 — Both tickets are bearer credentials, and the design says so.** `/session` is authorised
by the per-launch capability token carried as a WebSocket subprotocol
(`internal/transport/ws_auth.go`), with `OriginPolicy` as a second, weaker guard. A `POST`
cannot present a subprotocol, so the sink ticket is the credential. Consequences, stated rather
than implied:

- Possession authorises **both** the destination and the bytes written to it. A stolen sink
  ticket cannot redirect a write, but it can win the one-shot race and put attacker-chosen
  content at that path. That is an integrity violation, not merely a denial of service.
- Both tickets are minted from `crypto/rand` at no less than 128 bits, are never logged, never
  appear in an error string, and travel in the request **path** only because the request never
  leaves loopback; the `OriginPolicy` that guards `/session` guards this route too.
- The source ticket additionally never leaves the backend's own address space as a path: the
  renderer learns a display name and a size, never the directory the file came from.
- **A CORS surface, and it is part of the price.** The renderer resolves the upload URL against
  the socket's origin rather than the document's, because under `dev-web` the page is vite's
  and the backend is not — so the route is cross-origin by construction in the configuration
  the product is developed and tested in, and unreachable from a browser until the server says
  so. It is scoped to `/upload/{ticket}` and to nothing else on the mux: the origin is decided
  by the same `OriginPolicy` and decided **before** the ticket is looked up or claimed, an
  `OPTIONS` preflight answers `204` without touching the ticket, the requesting origin is
  echoed exactly and never as `*`, `Vary: Origin` is always sent, credentials are never
  allowed, and the allow-list is `POST` plus `Content-Type` and nothing more. The headers go on
  **every** reply including `400`, `409`, `410` and `5xx` — a browser hands the page nothing at
  all from a cross-origin reply that does not name it, so without them a `410` and a dropped
  connection arrive as the same "Failed to fetch".
- **`Content-Length` is the browser's to set, not the renderer's.** It is a forbidden header;
  an attempt to set it is silently dropped. The sink still requires it and still matches it
  against the declared size, and what makes that hold is the renderer refusing a blob whose own
  length is not the declared one before the request is made.

**D5 — The collision question is asked before a byte moves, and `O_EXCL` is the arbiter.**
`files.upload` stats the destination first and refuses with a typed `collision` outcome when no
decision was supplied. But a stat is a moment and a transfer is a span, so the stat is advisory
and the create is authoritative: the temp file is created with `O_WRONLY|O_CREATE|O_EXCL`, and
for `KeepBoth` the _final_ name is probed the same way. A lost race fails the create and the
next free suffix is tried, bounded at 32 attempts before the transfer fails with a typed error.

> `sftp.Client.Create` is `O_RDWR|O_CREATE|O_TRUNC`
> (`github.com/pkg/sftp@v1.13.11/client.go:304`) — it truncates. `OpenFile` with explicit flags
> is the call; a worker following the obvious method name would silently destroy a concurrent
> transfer's temp file.

> Not tabby: `SFTPSession.upload` overwrites silently and never asks.

**D6 — Replace atomically where the server allows it, and never leave the destination empty
without saying so.** Write to `<name>.nocx-upload-<rand>` in the destination directory (same
filesystem, so a rename is a rename), then `PosixRename` (`posix-rename@openssh.com`), which
replaces atomically. On a server without the extension, SFTP v3 `rename` refuses an existing
destination — this is `nocx-340t`, paid for once already — and the fallback is
`rename(dest → <dest>.nocx-bak-<rand>)`, `rename(temp → dest)`, `unlink(bak)`. The backup name
carries the same random suffix as the temp so two concurrent fallbacks cannot collide on it.

> Not tabby: theirs is `unlink(dest)` then `rename(temp, dest)`, which destroys the old content
> first and leaves a window holding nothing. Ours leaves the old content on disk under a named
> path for the whole window.

**D7 — Upload is not a `Provider` method; it is a `Handle` method, and the write seam is
optional.** `filesystem.Provider` is read-only by contract, and its own documentation says a
mutating method must land on **both** providers. So the write half is a separate, optional
interface — `filesystem.Uploader`, one method, `Sink() transfer.Sink`.

The seam is resolved where the attester already is — in `filesystemOpenService.OpenBinding`,
which type-asserts the provider _before_ `Register` (`internal/capability/files.go`) — and the
capability is recorded on the binding beside `endpointID`. `Handle` gains `Upload`, which is
refused on a binding that has no uploader. That failed capability **is** R1: a provider that
cannot write implements no `Uploader`, so its binding holds a nil sink, and the refusal is a
missing field rather than a check somebody performs.

**Both shipped providers implement it.** `sftp.Provider` writes over the tab's lease;
`local.Provider` writes through `os` (`internal/filesystem/local/upload.go`). Neither carries
its own copy of the transfer logic: `internal/transfer` already declares `RemoteFS` — `Create`,
`PosixRename`, `Rename`, `Remove` — and both sides satisfy it, so the temp file, `O_EXCL`, the
promote, progress, cancellation and the stranded-path accounting are ONE implementation. That
is D1 ("one sink, two sources") holding at the destination end as well. On the local side
`PosixRename` is `os.Rename`, which is `rename(2)` and replaces atomically, so the two-rename
fallback and its window where the destination holds nothing are unreachable there.

> **This paragraph replaces the opposite claim, and the correction is the point.** D7 first
> withheld the seam from `local.Provider` and said so in these words: "A local `Write` has no
> caller in this design (a local tab inserts a path, it does not copy), and a write path with no
> caller is exactly the `nocx-rtg0` failure." That reasoning was sound **only while D9 was**, and
> D9 was reasoned from the desktop build alone (see below). Correcting D9 gave the seam a caller,
> so it is now implemented rather than dead. What R1 rests on did not move: it rests on a nil
> sink, and it still does.

**The capability this adds, said in both halves, because an unwritten capability is one nobody
reviews.** The backend now **writes a file to its own filesystem at the renderer's request**, at
a caller-supplied destination directory and name. That is new. It is **not** an escalation: the
same client can already type `cat > file` into that same tab, the request reaches the seam only
through a binding whose session the connection owns (D15), `destDir` and `name` are validated by
`§5.3` before anything is stat'd, and R2 is untouched — the renderer still cannot name a SOURCE
on the backend's disk, so it can put bytes it holds somewhere, and can never ask the backend to
read something and send it. What is genuinely new is the destination side, and the honest
statement of its scope is the one `scp` gives: the write lands wherever that name resolved on
that machine at commit time, which the file manager's D8 already decided is navigation scope and
not a sandbox.

> The first draft said transport would type-assert "the binding's provider". It cannot:
> `Binding.provider` is unexported and `Acquire` returns a `Handle` exposing only
> `Root/List/Read/Watch` (`internal/filesystem/binding.go:15`, `:42`). The structural guarantee
> is deliberate and this design goes through it, not around it.

**D8 — A transfer does not hold the binding's use-guard.** `Binding.Close` waits for every
in-flight guard to drain (`internal/filesystem/binding.go:187`). A guard held for the length of
an upload would make `files.close` and session teardown block for as long as the upload runs.
So the guard is held only for the synchronous `files.upload` call, which mints and starts; the
running transfer is registered in a per-session set. Closing the binding, closing the session or
losing the connection **cancels** that set and waits for it to unwind, bounded — it never blocks
on it. A cancelled transfer takes the cleanup path in `§6`.

**D9 — Whoever has the path inserts it; whoever has only the bytes uploads them.**

That is the whole rule, and it appeals to nothing about which machine anything is on:

- The **Wails** drop yields an **absolute path**. Go took it from the runtime and, for a local
  tab — which mints no source ticket — sends it back in `files.dropped` as `localPath`. There is
  a path, so it is shell-quoted and inserted at the prompt, and no byte moves. Copying a file
  onto the machine it is already on is not a thing anybody asked for.
- A **browser** drop yields a `File`: a name, a size, and no location. There is nothing to
  insert, so the bytes are **uploaded into the tab's cwd**, through the same `files.upload` call
  a remote drop makes.

Neither branch inserts a base name in place of a path. It looks like a path, resolves against
whatever the shell's cwd happens to be, and so runs the command against a different file or
none.

A drop that has neither — no path and no bytes — is refused rather than started: a source with
no `blob` and no `sourceTicket` would open a transfer whose body can never arrive, and the
person would watch "uploading" for ever.

> **D9 first said the opposite of half of this, and the next reader needs to know why.** It read:
> a local tab's drop inserts the path and does not copy, "only the desktop build can keep this
> promise", and `dev-web` and a browser against a networked backend "refuse the gesture and say
> why". That was reasoned entirely from the **desktop** build, where the UI and the tab's shell
> are provably one machine and "local" therefore means "the same machine as the file".
>
> In a browser the premise is false. A **local tab** is a shell on the **backend's** machine; the
> dropped file is on the **browser's**. Under `make dev-web` those coincide physically and the
> renderer still has no path to insert; against a networked backend they are genuinely different
> machines and copying is the only thing the gesture can mean. The refusal was never a defence,
> it was a consequence of the wrong premise — which is why the browser case now falls out of the
> rule instead of needing an exception.

**This does not weaken R1.** R1 forbids sending a file to the **wrong** host. The backend's own
machine is exactly the host a local tab's shell is on, so a local upload satisfies R1 rather than
bending it. R1's structural expression moved with the rule and did not weaken: it is "a provider
that cannot write implements no `Uploader`, and its binding refuses", not "the local provider is
that provider" (D7).

Sending the path outward does not weaken `R2`, which is a rule about **direction**. `R2`'s
threat is a renderer that can NAME a source inbound — a path it can spell is a file it can ask
the backend to read and send to a host of its choosing — and the defence is structural:
`files.upload` has no `sourcePath` and no `source`, and its decoder refuses unknown fields, so
the request cannot express one. That shape is unchanged. This path runs the other way, to the
same human who just chose the file, for their own command line on the machine the file is
already on, and no wire field takes it back.

## 4. Scope

### In

- Upload of one or more **files** onto the host of the active tab.
- Two gestures, and only two: **drop onto the tab's terminal** (target: that tab's cwd, the same
  OSC 7 value the Files panel already follows — `nocx-r3bz`) and an **action in the Files panel**
  (target: the folder the panel is showing).
- Both sources (D1), so the feature works in the desktop app, in `dev-web`, and against a
  networked backend from the first commit.
- Per-transfer progress, speed and cancellation.
- The collision decision, with apply-to-all across a multi-file drop.
- Upload into a **local** tab's cwd from a browser, where the gesture yields bytes and no path
  (D9). The desktop's own drop on a local tab inserts instead, and starts no transfer.
- Honest refusal where there is nothing to write through, or nowhere honest to write (R1, and
  the unverified-cwd refusal of `§5.5`).

### Out — each a refusal, not an omission

- **Dropping onto an individual folder row.** A third target rule for a gesture nobody asked
  for. The panel's current folder is the panel's target.
- **Directories.** Recursive walk, `mkdir`, partial failure mid-tree, symlinks and modes are a
  materially larger problem on the same mechanism. Next wave of the same epic.
- **Download.** The reverse direction reuses the sink's shape but is its own surface. Sibling
  bead.
- **Resume after a broken link.** Buildable — SFTP can seek — but it needs a durable record of a
  partial transfer, which nothing here has.
- **A queue with several transfers in flight.** One at a time per binding; a multi-file drop is
  sequential.
- **Preserving the source file's mode.** The uploaded file gets the server's default. Tabby
  carries `getMode()`; the stream source has no mode at all, and carrying it in one source and
  not the other is worse than not carrying it.

## 5. Architecture

### 5.1 The lease gains a write half — `internal/ssh`

`FSConn` gains four methods, each one lane call, each with the existing watchdog and poisoning
semantics:

- `Create(path string) (FSFile, error)` — `OpenFile` with `O_WRONLY|O_CREATE|O_EXCL` (D5).
- `PosixRename(old, new string) error` — reports "extension unsupported" distinguishably from
  every other failure, because the fallback keys on exactly that.
- `Rename(old, new string) error` — plain v3 rename; refuses an existing destination.
- `Remove(path string) error`.

`FSFile` is a handle whose `Write` and `Close` are themselves lane calls on the same lease. The
handle exists between calls; **no call happens outside the lane**, which is the property that
matters. Poisoning closes the subsystem and invalidates the handle, which is what unblocks a
wedged write.

Transfer leases are constructed exactly like tree leases and differ only in who holds them
(D2).

### 5.2 The sink — `internal/transfer`

A new package, because writing a remote file with progress, cancellation, a temp name and a
replace strategy is a behaviour nothing currently owns.

```go
type Upload struct {
    DestDir  string   // absolute, provider syntax, validated by the provider
    Name     string   // ONE path component; see §5.3
    Size     int64    // declared; the sink refuses a reader that disagrees
    OnExists Decision // Overwrite | KeepBoth | Skip
}

type Sink interface {
    Put(ctx context.Context, u Upload, r io.Reader, progress func(n int64)) (Outcome, error)
}
```

`Put` is the only place that knows the temp name, the two replace strategies and the cleanup. It
is given a reader and does not know which source produced it — which is what makes D1 one
implementation rather than two.

`Outcome` carries the resolved final name (`KeepBoth` may have changed it) and a list —
never a single field — of paths left behind (`§6`).

### 5.3 Wire — control plane

`files.upload` params: `bindingId`, `destDir`, `name`, `size`, `sourceTicket` (optional), and
`onExists` (optional). **There is no `sourcePath` and no `source` discriminator**: a request with
a `sourceTicket` is a path upload, one without is a stream upload (R2).

Authorised exactly like every other `files.*` call: `Registry.Acquire` re-checks that the
binding's session is in the requesting connection's `connState`, and the `Handle` refuses if the
binding has no uploader (D7).

**Path validation, because the destination is caller-supplied.** `destDir` goes through the
transport's existing `validateFSPath` — absolute, clean, bounded — and then through the
provider, which owns path syntax. `name` must be exactly one path component: non-empty, no
separator in either syntax, not `.` and not `..`, within the provider's name bound. A request
that fails either is `-32602` before anything is stat'd.

What this does **not** buy, stated rather than assumed: `destDir` may be, or may become, a
symlink to somewhere else, and a directory can be replaced between validation and commit.
Confining to the binding's root is not the answer — the file manager's D8 already decided the
root is navigation scope, not a sandbox, and a symlink may leave it. So the guarantee is
"the write lands where that name resolved on the server at commit time", which is the guarantee
`scp` gives, and it is the honest one.

Three outcomes:

- `{"collision":"exists"}` — nothing started; the renderer asks and calls again with `onExists`.
- `{"transferId":"…"}` — a source ticket was supplied; the transfer is running.
- `{"transferId":"…","ticket":"…","url":"/upload/…"}` — the sink is waiting for a body.

`files.uploadCancel` params: `transferId`. Idempotent; cancelling a finished transfer is not an
error.

`files.uploadProgress` — notification: `transferId`, `bytes`, `total`. **Live only and
explicitly lossy**: emitted to the binding's session's current subscriber, resolved at emit
time, and dropped when there is none. It is an indicator, not a ledger.

`files.uploadDone` — notification: `transferId`, `outcome` (`written` | `skipped` | `cancelled` |
`failed`), `finalName`, `error` when failed, and `stranded[]` when anything was left behind. This
one is **retained per session and flushed on attach**, the way `files.changed` accumulates a
dirty set and delivers it on re-attach (`internal/transport/ws_files.go:919`, `:932`, `:956`).
Current-subscriber addressing alone is only half that precedent, and the half that loses
terminal outcomes — a lost `uploadDone` leaves the UI saying "uploading" forever. Retention is
bounded and cleared on delivery.

Contracts for all four in `contracts/`, `additionalProperties:false` with explicit `required`,
renderer types generated, Go validated.

### 5.4 Wire — the upload endpoint

`POST /upload/{ticket}`, on the existing mux, behind the same `OriginPolicy` that guards
`/session`.

The ticket has exactly four states, and each maps to one status:

| State                                        | Status                 | Effect on the transfer                  |
| -------------------------------------------- | ---------------------- | --------------------------------------- |
| unknown (never minted, or already forgotten) | `410 Gone`             | none; there is nothing to name          |
| minted, unclaimed                            | claimed → body is read | runs                                    |
| claimed, transfer still running              | `409 Conflict`         | untouched — the first claimant keeps it |
| claimed, transfer finished                   | `410 Gone`             | none                                    |

Expiry is **not** one of those states: a ticket that is not claimed within its TTL is dropped by
the mint-side timer, which cancels the transfer it named at that moment. By the time a late
`POST` arrives the ticket is simply unknown. This removes the first draft's contradiction, where
`410` both meant "names nothing" and "cancel what it names".

- **Claim** happens after headers are validated and immediately before the body is read; that is
  the enforceable event the TTL closes at.
- `Content-Length` is required and must equal the size declared at mint time; a mismatch is
  `400` before the body is read.
- A body ending short of `Content-Length` fails the transfer; the temp is removed and the
  destination is never touched. A body exceeding it is cut at the bound and fails the same way.
- **This route sets its own deadlines.** The shared `http.Server` has `ReadHeaderTimeout: 0`
  deliberately, because `/session` is a long-lived upgrade (`internal/transport/ws.go:1101`); the
  upload handler therefore applies its own header deadline and a per-read stall deadline on the
  body. Without them, valid headers followed by silence hold a transfer and a lease open
  indefinitely.

### 5.5 Frontend

**Drop target.** The terminal element, and nothing else (`§4`). In a browser the drop yields
`File` objects and the upload is a stream. In the Wails window, `EnableFileDrop` plus
`data-file-drop-target` delivers absolute paths and the target element's attributes to the Go
side; Go **mints a source ticket** and emits it to the renderer with a display name and a size
(R2). The renderer then calls `files.upload` with its own `bindingId` like any other caller — so
the native gesture joins the same authorised path, rather than becoming a second addressing
scheme that skips `connState`.

> Verified, not assumed: Wails beta.9 installs document-level listeners that act only on drags
> whose `types` contain `Files`
> (`wails/v3@v3.0.0-beta.9/internal/runtime/desktop/@wailsio/runtime/src/window.ts:712`). The tab
> strip's drag sets `application/x-nocx-tab` (`frontend/src/layout/strip-drag.ts:15`,
> `frontend/src/tab.tsx:183`), so it is not a files drag and passes through. The design commits
> to the Wails route; a regression test asserts tab reordering still works with `EnableFileDrop`
> on. The first draft carried a "fall back to browser semantics if disturbed" clause — that was
> speculative generality about a conflict that does not exist, and it is cut.

**Picking.** The native picker where Wails exists, the routed picker `nocx-ult5v` is building
over `files.list` where it does not. Both must return a **source ticket** rather than a path.
`dialog.openFile` returns a path today and stays as it is for its existing callers; upload uses
a sibling method that mints instead of returning.

**Progress.** One kit component per transfer, driven by `files.uploadProgress`, with in-flight
state derived from `files.upload`'s result and `files.uploadDone` — never from having seen a
progress notification, which may never arrive.

The Files panel header is already over-full (`nocx-a8cz`); the Upload action lands there only
once that bead has decided how the header overflows.

**Refresh.** Nothing new: on `written`, the destination directory is invalidated and the existing
`files.changed` path re-lists it.

## 6. Failure paths, and the invariants as intervals

For every external call there is a test where that call fails. `dest` is the destination, `temp`
the upload temp, `bak` the fallback backup.

| Failure                                              | `dest`                                  | Left behind                                | Reported                                                                                                    |
| ---------------------------------------------------- | --------------------------------------- | ------------------------------------------ | ----------------------------------------------------------------------------------------------------------- |
| `Create` refused (permission, read-only, quota)      | untouched                               | —                                          | the reason, in place                                                                                        |
| `Create` refused `EEXIST` (lost the `O_EXCL` race)   | untouched                               | —                                          | retried, next suffix                                                                                        |
| Write fails mid-stream (disk full, I/O)              | untouched                               | — (temp removed)                           | the reason                                                                                                  |
| Write fails and `Remove(temp)` **also** fails        | untouched                               | `temp`                                     | both reasons; `stranded:[temp]`                                                                             |
| `Close` on the handle fails after all bytes written  | untouched                               | `temp`                                     | treated as failure; `stranded:[temp]`                                                                       |
| Connection lost mid-stream                           | untouched                               | `temp` (lease gone; nothing can remove it) | `stranded:[temp]`                                                                                           |
| Source read fails / ends short                       | untouched                               | —                                          | the reason                                                                                                  |
| Ticket TTL elapsed before claim                      | untouched                               | —                                          | cancelled at expiry                                                                                         |
| `PosixRename` unsupported → fallback runs            | replaced                                | —                                          | nothing; normal                                                                                             |
| `rename(dest → bak)` fails                           | untouched (old)                         | `temp`                                     | the reason; `stranded:[temp]`                                                                               |
| Loss/cancel after `dest → bak`, before `temp → dest` | **missing**                             | `bak`, `temp`                              | `stranded:[bak,temp]`, and `bak` is named as the old content                                                |
| `rename(temp → dest)` fails after `dest → bak`       | **missing**                             | `bak`, `temp`                              | as above                                                                                                    |
| `unlink(bak)` fails after a successful promote       | new content                             | `bak`                                      | success **with** `stranded:[bak]`                                                                           |
| Cancelled by the person                              | untouched                               | —                                          | cancelled                                                                                                   |
| Cancel arrives between the two renames               | **missing** until the promote completes | —                                          | the promote is **not** abandoned: cancellation after `dest → bak` is honoured only once `dest` exists again |

That last row is a rule, not an observation: **cancellation is refused inside the fallback's
two-rename window.** A cancel that landed there would be the one path that deliberately leaves a
person with no file, and "I pressed cancel" must never be how the destination goes missing.

**Invariant, both ends named.** From the moment `Put` starts until it returns, `dest` holds
either its previous content or the new content — **except** on a server without
`posix-rename@openssh.com`, where between `rename(dest → bak)` and `rename(temp → dest)` it
holds nothing and the previous content is at `bak`. That window is one round trip, cancellation
cannot open it wider, and every outcome that lands inside it names `bak`.

**Invariant, both ends named.** `temp` exists from a successful `Create` until either a rename
consumes it or a `Remove` succeeds. `Remove` is an external call and can fail, so the closing
event is a _successful_ removal, not an attempted one; when it does not happen — a failed
`Remove`, a lost lease — the path is reported in `stranded`, never dropped silently.

**Races that are accepted, and their outcomes.** Two `KeepBoth` uploads choosing the same suffix:
`O_EXCL` fails one, which retries. `Overwrite` when the destination changed after the person
answered: the new content replaces whatever is there at commit time — that is what overwrite
means — unless the destination became a directory, where the rename fails and is reported. A
second nocx process, or an ordinary process on the host, is indistinguishable from either and
needs no separate rule.

**And the paired success assertions.** For every "returns an error when…" above there is a test
that on an ordinary server the upload succeeds — including one where the `.bak` fallback itself
completes, leaves the new content in place and no `bak` behind.

## 7. Testing

**The end-to-end check (`§1`).** Against `cmd/e2e-sshd`, which serves a real `pkg/sftp` server
(`cmd/e2e-sshd/main.go:548`) and is already asserted to advertise `posix-rename@openssh.com`
(`cmd/e2e-sshd/main_test.go:218`): open an SSH tab, drop a file onto its terminal, and assert it
appears in the tree and its bytes on the far side match.

It exercises the **stream** source, forced rather than chosen: the suite runs the headless path,
where there is no Wails, therefore no native picker and no `EnableFileDrop`. Playwright
constructs a `File` and a `DataTransfer` in the page, which is what a browser drop produces.

**The path source therefore has no e2e, and this is stated rather than glossed.** It is covered
by the sink's unit tests (the reader is a file either way) and by transport tests for the source
ticket. What no automated check in this repo can watch is Wails file-drop delivering a real OS
path; that needs the desktop app and is the one manual step in the epic's DONE WHEN. The last
time a wave claimed coverage it did not have, the gap was known and unrecorded (`nocx-rtg0`).

**Unit, backend.** The sink against a fake `FSConn` (`internal/filesystem/sftp/fsfake_test.go`
is the precedent) covering every row of `§6`, including a fake that refuses
`posix-rename@openssh.com` so the fallback is exercised, a fake whose `Remove` fails, and a fake
whose `Close` fails after a complete write. Plus: a write longer than `fsHardTimeout` in total
completes, proving D2's per-chunk lane rule (each chunk is short; only the whole is long).

The local sink is tested against the real filesystem under `t.TempDir()`, not against a fake:
`os` IS the dependency, so a fake would only restate this package's own beliefs about it. Its
failure paths are the ones `os` can actually produce — a destination directory that is not
there, a read-only directory (which must classify as `fs.ErrPermission`, or `KeepBoth` spends
all 32 attempts on it and reports "no free name", which is false), a source shorter than
declared, and a cancelled context. `RemoteFS`'s three unstated contracts — `Create` refusing a
taken name, `PosixRename` replacing without ever reporting unsupported, `Rename` refusing an
existing destination — are asserted directly, because the compiler cannot check them and the
sink's correctness rests on them.

**Unit, transport.** Sink-ticket lifecycle across all four states of `§5.4`, size mismatch, short
body, long body, wrong origin, headers-then-silence. Source-ticket lifecycle: one-shot, expiry,
and that it cannot be minted from the wire. `files.upload` against a binding whose provider **cannot write** is
refused — the test that proves R1 — and one against an ordinary **local** binding runs the whole
round trip onto the backend's own disk, which is the test that proves the local half of D9 over
the real wire rather than in a unit. A request carrying anything path-shaped as a source is
rejected by the schema — the test that proves R2.

**Unit, frontend.** The collision dialog's apply-to-all across a multi-file drop; a **native**
drop on a local tab inserts the path and starts no transfer, while a **browser** drop on the
same tab uploads into its cwd and takes the same collision question — both halves of D9, and the
insert half is the one that already worked and would break silently; in-flight state survives a reconnect that
drops every progress notification and delivers only `files.uploadDone` on attach; tab reordering
still works with `EnableFileDrop` on.

**Contract.** Four schemas, generated renderer types, Go conformance, and
`…_OverTheWireConformsToContract` for **all four** — both results and both notifications. A
notification validated only as a struct the test built is the gap AGENTS.md names.

**Reachability.** `deadcode -tags gtk3 -whylive` on the sink's `Put` must print a path from
`main`, with the contrast probe on an unwired sibling in the same package. The `-filter` form is
not evidence here and is not used.

## 8. Bead changes

- `nocx-9le.5` — rescope to **upload only** and make it the epic this design implements;
  acceptance is `§1`'s sentence plus the e2e check. Raise from `P3` to match the wave.
- New sibling under `nocx-9le` — **download**.
- New sibling under `nocx-9le` — **directory upload**, blocked by the upload epic (same files).
- `nocx-a8cz` — gains a `blocks` edge onto the upload epic's Files-panel task.
- ADR for D3 (an HTTP upload route beside the WebSocket, against AD-1's plane allocation).

## 9. Open questions

None blocking. One to settle inside implementation: where the transfer list lives once there is
more than one — deferred with the queue.

## 10. Review history

**2026-08-21, codex adversarial review.** Seventeen findings; the material ones and what they
changed:

- The lane could not carry a long write in either shape proposed. → D2 rewritten: own lease,
  per-chunk lane calls, stall-based liveness.
- The `Uploader` seam was described as a type assertion transport cannot perform. → D7 rewritten
  onto the assertion site the attester already uses, with the capability recorded on the binding
  and `Upload` on the `Handle`.
- `sourcePath` on the wire was arbitrary backend-file read authority. → R2 added; source tickets.
- The sink ticket's threat model was understated ("cannot redirect a write" is true and
  insufficient). → D4 added.
- `destDir`/`name` had no confinement rule. → `§5.3`, with what it does not buy stated.
- Collision handling was TOCTOU. → D5: `O_EXCL` is the arbiter.
- `sftp.Client.Create` truncates. → D5 names `OpenFile` and the flags.
- The binding guard would have blocked `files.close` for the length of an upload. → D8.
- Progress/done addressing was half the `files.changed` precedent. → `§5.3`: done is retained and
  flushed on attach.
- The fallback's partial failures were one table row. → `§6` expanded; `stranded` is a list;
  cancellation is refused inside the two-rename window.
- The temp-cleanup invariant closed on an attempted action. → restated to close on a successful
  one.
- The ticket TTL was not defined at an enforceable event, and `410`/`409` contradicted each
  other. → `§5.4` state table; expiry cancels at expiry.
- The Wails/tab-drag conflict was speculative; codex found the runtime only acts on `Files`
  drags. → committed to the Wails route, fallback cut, regression test kept.
- `§4` and `§5.5` disagreed about folder-row drops. → out, in both places.
- Over-the-wire conformance was promised for one shape and demanded for one. → all four.

**2026-08-22, `nocx-9le.5.22` — D9 was reasoned from one build.** The owner reported that a
browser drop on a local tab refuses instead of uploading. It was a design error, not an
implementation one: D9 said a local tab inserts a path and never copies "because copying a file
onto the machine it is already on is not a thing anybody asked for", which is true of the
desktop build and false in a browser, where a "local" tab is a shell on the BACKEND's machine
and the file is on the BROWSER's. D9 was rewritten to turn on what the gesture YIELDS — whoever
has the path inserts it, whoever has only the bytes uploads them — so the browser case falls out
instead of needing an exception. D7's withheld write seam was a consequence of D9's first form,
so it landed too: `local.Provider` implements `Uploader` over `os`, reusing `internal/transfer`
verbatim. R1 is unchanged and its structural expression moved with it, from "a local binding
refuses" to "a binding whose provider cannot write refuses". The new capability — the backend
writing to its own filesystem at the renderer's request — is stated in D7, with why it is not an
escalation and what about it is genuinely new.
