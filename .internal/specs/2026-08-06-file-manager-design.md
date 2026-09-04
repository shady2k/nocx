---
title: File manager — a session-aware file tree and a read-only viewer
status: draft
created: 2026-08-06
supersedes: .internal/specs/2026-08-01-file-manager-design.md (branch shady2k/feat-file-manager)
bead: nocx-708q (rescoped), brainstorm nocx-gglz
---

# File manager — design

## 0. The one rule

**The panel shows files of the machine you are currently in, and never of another one.**

Everything below follows from that sentence: why a filesystem is addressed by a binding the
backend issued rather than by anything the client can name, why a reconnect may not silently
refresh a viewer, and why a local file's tab title is deliberately plainer than a remote
one's.

The rule exists because the panel's actions are consequential. A tree that opens a file and
copies a path is not decoration — if it lists the local `~/orca/workspaces/…` beside a shell
on `srv-01`, it shows one machine's files while the user acts on another's. Being merely
_less useful_ on SSH would be a scheduling question; being _wrong_ on SSH is a design
question.

## 1. What this is

A **Files** view in the existing left activity bar, rendering the filesystem tree of the
**active tab's machine**: local for a local shell, remote over SFTP for an SSH session.

- **Primary action:** open a file in its own tab, read-only.
- **Secondary actions:** copy the path (relative and absolute); show in the OS file manager,
  local tabs only.

### Why a tree in a terminal at all

`docs/vision.md` does not list a file explorer, so the argument has to be made rather than
assumed. It is this: **when an agent TUI occupies the terminal, the terminal cannot be used
to look at files.** `bat`, `less` and `nvim` all need a free prompt. The nocx user runs an
agent in the tab and wants to see what it just wrote — the one moment the normal tools are
unavailable. The panel is file access while the terminal is busy.

That argument only holds if the panel follows the user onto the remote host, because that is
where the agent frequently runs. **SFTP is therefore inside this epic, not after it.**

## 2. What changed since the 2026-08-01 draft, and why

The superseded draft was reviewed adversarially once and was still wrong in five load-bearing
places. Four were found by a second review against the code on 2026-08-06; the fifth is a
scope decision by the owner. Each is recorded here because each one, uncaught, would have
been discovered during implementation at the worst possible time.

| Was                                                                      | Is                                                                                                                    | Verified at                                                |
| ------------------------------------------------------------------------ | --------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------- |
| "This epic delivers the multi-view sidebar mechanism plus one view"      | The mechanism shipped with Ports. Files is one more `SidebarViewDescriptor`                                           | `frontend/src/main.tsx:295`, `frontend/src/sidebar.tsx:50` |
| Root comes from the session's cwd                                        | On SSH that cwd can be the **local** home. Root comes from the provider                                               | `internal/session/session.go:271`                          |
| A verified OSC 7 cwd overrides the provider's root (D2)                  | The root is the constant filesystem root `/`; a verified cwd REVEALS (expands and selects) from it and never re-roots | `files-store.ts:555`                                       |
| Every call carries `sessionId`; a viewer restores by `{origin, path}`    | Calls carry a backend-issued binding. `sessionId` is minted fresh per Open                                            | `internal/session/session.go:172`                          |
| SFTP needs a new pool-lease seam, and cancellation is goroutine+deadline | Both already exist as `DiscoveryConn`; we extend that answer                                                          | `internal/ssh/ssh_discovery.go:13`, `:378`                 |
| Refresh on OSC 133 command-end                                           | Refresh comes from the filesystem — an agent is one long command                                                      | `frontend/src/agent-status.ts:3`                           |

The fourth row is the one worth reading twice. The draft proposed running each uncancellable
`pkg/sftp` call on its own goroutine, returning on `ctx.Done()`, and letting the goroutine
"drain under a hard deadline". That has an impossible branch: **when the deadline expires,
nothing makes the blocked goroutine return.** The deadline was a word, not a mechanism.
`DiscoveryConn` already solves exactly this, and says so in its own doc comment: it cancels a
non-context-aware remote operation _by closing the session_, then **waits**, "so no goroutine
outlives the call". We extend that rather than write a second, worse answer — AGENTS.md,
"look for the existing answer before you write a second one".

## 3. Decisions

| #   | Decision                                                                                                                                                                                                                                                                                                                                                                                                                                                                | Rejected alternative, and why                                                                                                                                                                                                                                                                                                                                           |
| --- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| D1  | **A filesystem is addressed by a `bindingId` the backend issues** from a `sessionId`. Only `files.open` takes a `sessionId`, and every later call reaches its provider only through `Registry.Acquire`                                                                                                                                                                                                                                                                  | Every call taking `sessionId`. It keeps the wrong pairing inexpressible (good) but spreads the authorisation check of D15 across every handler, where one that forgets it is a hole. The choke point is `Acquire`, not the handler                                                                                                                                      |
| D2  | **The provider computes the root, and the composition layer pins it to the filesystem root `/`.** Nothing overrides it — a verified OSC 7 cwd is never handed to `files.open`; a cwd change REVEALS: the chain from `/` down to the new cwd is expanded and the target selected, never collapsing anything. Re-rooting would throw expansion state away on every `cd`, cage the user inside the cwd, and yank the panel backwards every time a viewer tab was activated | "A verified OSC 7 cwd overrides the root" (the old rule). Re-rooting discards expansion state on every `cd` and would need a second binding (a close/open round trip that kills the viewer's binding); and the session's `Cwd()` — the **local** `os.UserHomeDir()` for an SSH session with no explicit cwd — was a `$HOME` substitution AD-5 forbids applying silently |
| D3  | **The SFTP lease is a sibling of `DiscoveryConn`**: its own pooled reference; listing cancelled by context, everything else by closing the subsystem                                                                                                                                                                                                                                                                                                                    | A goroutine-per-call with a drain deadline. Unbounded stuck calls, and a deadline that unblocks nothing. Also the blanket claim "pkg/sftp is not cancellable" — one method is (§5.1)                                                                                                                                                                                    |
| D4  | **No client-minted _identity_ crosses the wire.** The backend issues `{bindingId, endpointId}`; the client's `{tabId, generation}` is checked in the closure that issued the call and is never sent                                                                                                                                                                                                                                                                     | One `Origin` struct carrying both, or echoing a client token. `tabId` is a frontend integer the backend cannot attest; sending it adds a field to every schema to re-derive what the caller never forgot                                                                                                                                                                |
| D5  | **Refresh comes from the filesystem**: fsnotify locally, polling over SFTP, both behind one provider-side watch capability                                                                                                                                                                                                                                                                                                                                              | OSC 133 command-end (blind inside a long-running agent) and agent-activity heuristics (a file can be changed by anyone — cron, another session, another person)                                                                                                                                                                                                         |
| D6  | **No automatic rebind after reconnect.** A stale viewer offers **Reload**, enabled only when a live session's `endpointId` matches                                                                                                                                                                                                                                                                                                                                      | Silent rebind on endpoint match. Same identity check, but it moves content under a reader who did not ask. Rebinding by profile id — which the draft did — is worse still: a profile is editable                                                                                                                                                                        |
| D7  | **The viewer is a snapshot plus an offer.** A file that changed is announced, never silently reloaded                                                                                                                                                                                                                                                                                                                                                                   | Live-following content. A log you are reading scrolls out from under you                                                                                                                                                                                                                                                                                                |
| D8  | **Root is navigation scope, not a sandbox.** No `..` row; a symlink may leave the root and is rendered plainly                                                                                                                                                                                                                                                                                                                                                          | Enforcing containment. It would be security theatre: the real boundary is the account's own permissions, and pretending otherwise invites someone to rely on it                                                                                                                                                                                                         |
| D9  | **Directory symlinks expand.** Every listing returns its own `canonical`; the controller compares it with the expanded ancestors it already holds and marks a match cyclic before rendering                                                                                                                                                                                                                                                                             | Refusing to expand them — hides half of every real tree. And computing cycles in `Entry`, where `List(path,page)` has no ancestor chain to compute from and canonicalising each child is N+1 round trips                                                                                                                                                                |
| D10 | **No row virtualisation.** A page of N children per directory plus an explicit "show next N"                                                                                                                                                                                                                                                                                                                                                                            | Virtualised rows — what Orca needed (`@tanstack/react-virtual`). Deferred to `nocx-goi0`. A cap without pagination is worse than either                                                                                                                                                                                                                                 |
| D11 | **New methods live under `files.*`, not `fs.*`**                                                                                                                                                                                                                                                                                                                                                                                                                        | Extending `fs.*`. `contracts/fs.complete.schema.json` declares that namespace **local-only** ("the provider is inactive on a remote session"). A remote-capable `fs.list` beside it invites a fatal misread                                                                                                                                                             |
| D12 | **One live viewer per `{endpointId, canonical path}`** via the existing `singletonKey`                                                                                                                                                                                                                                                                                                                                                                                  | A tab per click. `tabs.ts:543` already deduplicates                                                                                                                                                                                                                                                                                                                     |
| D13 | **File bytes never reach disk.** Tab restore does not exist yet; if and when it does, a viewer persists identity only                                                                                                                                                                                                                                                                                                                                                   | Persisting the bytes — up to 2 MiB of possibly-secret remote content in unencrypted config storage. And describing a restart behaviour the app cannot perform (§5.4)                                                                                                                                                                                                    |
| D14 | **A directory too large to enumerate is a state, not a wait**: an entry cap the user can reason about, an elapsed-time cap they cannot, and no polling for a capped directory                                                                                                                                                                                                                                                                                           | Listing whatever arrived. A complete-looking prefix of a directory is worse than an honest refusal                                                                                                                                                                                                                                                                      |
| D15 | **`files.open` is authorised by `connState`, not by the global session registry**                                                                                                                                                                                                                                                                                                                                                                                       | `registry.Get` alone. Any authenticated socket that learned another connection's session id could open that session's filesystem. `ws.go:652` exists for exactly this                                                                                                                                                                                                   |

### D11 in full: why a second directory lister is justified

`internal/completion` already lists remote directories, through `SSHCompleter` running bash
over a `DiscoveryConn`. Under the AGENTS.md rule this must be justified rather than
duplicated silently.

They answer different questions. Completion asks _"what does the shell think completes this
prefix"_ — a question only a shell can answer, because it includes bash's own completion
rules. The tree asks _"what does this directory contain, with sizes, modes and mtimes"_ — a
filesystem question, and SFTP answers it structurally, works when remote **exec** is
forbidden while SFTP is allowed, and does not spend a shell. Parsing `ls` output for the tree
would be the second implementation, not this one.

**This reasoning goes in the code**, at `internal/filesystem`'s package doc, not only here.

## 4. Scope

### In

- Files view in the existing activity bar, ordered before Ports.
- Per-tab root at the filesystem root from D2; a verified cwd reveals; lazy expansion with
  pagination; collapse.
- Automatic refresh (§5.5) with a visible mode indicator and a manual refresh action.
- Open a **regular file** in its own tab, read-only, with syntax highlighting.
- Copy path — relative to root, and absolute.
- Show in the OS file manager — **local tabs only; absent, not disabled, on a remote tab.**
- Both providers: local **and** SFTP. **The epic does not close until SFTP lands.**
- Lifecycle: session close, connection loss, WebSocket reconnect (§5.6). **Not app restart** —
  tab restore has no reader today (§5.4).

### Out — each a refusal, not an omission

- **All mutation**: create, rename, delete, move, duplicate, drag-and-drop. The next epic.
- **Editing.** The viewer is read-only. The next epic.
- **Upload / download / drag-drop transfer.** Stays in `nocx-9le.5`.
- **Insert path at the prompt, and shell-escaped copy.** Both need the originating terminal
  as an explicit target and a shell-dialect quoting decision; own bead.
- **Name filter and content search** (`nocx-bkmy`). A filter over lazily-loaded nodes
  silently fails to find files that exist — worse than no control.
- **Git status markers** (`nocx-terg`). No git surface exists in the backend.
- **Row virtualisation** (`nocx-goi0`).
- **Multi-selection.**
- **A dotfile toggle.** Dotfiles are shown, as in both reference products.

## 5. Architecture

### 5.1 Backend — `internal/filesystem`

```go
type Provider interface {
    Root(ctx context.Context) (Root, error)
    List(ctx context.Context, path string, page Page) (Listing, error)
    Read(ctx context.Context, path string, maxBytes int64) (Content, error)
    Watch(ctx context.Context, path string) (Watch, error)
    Canonical(ctx context.Context, path string) (string, error)
    Close() error
}

type Root    struct { Path, Display string; Inferred bool; InferredReason string }
type Page    struct { Offset, Limit int }
type Entry   struct {
    Name, Path string
    Kind       Kind   // regular | dir | symlink | other
    LinkTarget string // symlinks only
    LinkKind   Kind   // what the link resolves to; `other` when broken
    Size       int64
    ModTime    time.Time
    Mode       uint32
}
type Listing struct {
    Path      string
    Canonical string // the provider-canonical identity of the directory listed (D9)
    Entries   []Entry
    Offset, Total int
    HasMore   bool
    Rev       string
}
type Content struct {
    Path      string
    Canonical string // identity of the object actually read; what singletonKey uses
    Text      string // always valid UTF-8
    Size      int64
    ModTime   time.Time
    Truncated bool
    Binary    bool
    Lossy     bool
    Changed   bool // size or mtime differed before vs after the read
}
type Watch interface { Events() <-chan struct{}; Mode() WatchMode; Close() error }
```

The interface has **no mutating method**, so mutation cannot be added to one provider without
changing the contract for both. It is a rule about symmetry, not a permanent ban: the next
epic adds mutating methods, and adds them to both.

**`Kind` replaces the draft's `IsDir`/`IsSymlink` pair** because the two encode a lattice the
product must not flatten. A FIFO blocks forever on read; a device or a procfs pseudo-file has
no meaningful size and may produce unbounded or ever-changing content. What may be done with
a row is a table, not a boolean:

| `Kind`                         | Open                            | Expand                  |
| ------------------------------ | ------------------------------- | ----------------------- |
| `regular`                      | yes                             | —                       |
| `symlink` → `LinkKind=regular` | yes, after canonical resolution | —                       |
| `dir`                          | —                               | yes                     |
| `symlink` → `LinkKind=dir`     | —                               | yes, unless cyclic (D9) |
| broken symlink, `other`        | no                              | no                      |

**The backend enforces this from metadata it reads at the time of the call**, never from the
`LinkKind` the UI was handed. A symlink can be retargeted between the list and the read, so a
UI-supplied kind is a claim about the past. This is the only guard between "open a file" and
"block forever on a FIFO somebody swapped in".

**Ordering is backend-owned and deterministic before pagination**: directories first, then
files, each by the UTF-8 byte order of the name, case-sensitive. The frontend never re-sorts
— a `localeCompare` in the renderer would disagree with the server's paging boundaries and
make "show next N" duplicate and skip rows. `Listing.Entries` is always non-nil: an empty
directory marshals as `[]`, never `null`.

**`Rev` is a cheap digest** of the listing — each entry's name, size, mtime, mode, kind,
**`LinkTarget` and `LinkKind`**. The last two are not decoration: a symlink retargeted to
another file of the same kind and size would otherwise leave the digest unchanged while the
rendered target and the expansion destination both changed.

It is what the SFTP watcher compares, and what makes pagination safe. **When `Rev` changes,
the directory is re-listed in ONE call** — `offset=0, limit=<count currently displayed>` —
and every displayed row is replaced atomically. Re-fetching page 1, then page 2, then page 3
and calling them one generation would be a lie: the directory can change between them, and a
client-side generation orders responses without making several backend listings one snapshot.
The page-size ceiling must therefore admit this refresh form.

#### A directory can be too big to show, and that is a state

`Total`, the global directories-first ordering and a whole-directory digest all require the
provider to enumerate the **complete** directory before it can return any page. So remote
work is proportional to directory size, not to the number of rows displayed — the opposite of
what "poll the displayed pages only" sounds like. Two bounds, and they answer different
questions:

- **An entry-count cap** is the product limit, because it is the one a user can reason about:
  _"this directory has more than N entries; nocx does not display directories this large"_.
  Above it the directory is a `tooLarge {observedCount, limit}` state — no pagination offered,
  and **polling disabled for that directory specifically**, since a capped directory would
  otherwise re-enumerate on every tick to compute a digest it will refuse anyway. Manual retry
  stays.
- **An elapsed-time cap** is the operational limit: it is what stops a laggy or non-replying
  server from holding an operation slot. It yields `timedOut {timeout}`. It is deliberately
  _not_ the user-facing explanation, because the same directory would pass on one network and
  fail on another.
- A response-size ceiling guards memory, because equal entry counts do not cost equal bytes.

Partial results are **discarded**, never rendered: an apparently complete prefix of a
directory is worse than an honest refusal. And `tooLarge` reports `observedCount` only when a
complete enumeration was actually paid for — otherwise it says "more than N" rather than
inventing a total.

**Path syntax belongs to the provider.** `local` uses `path/filepath`; `sftp` uses `path`,
because SFTP specifies POSIX-style paths regardless of the OS nocx runs on. `filepath` must
not appear in transport or in code shared by both providers.

**Three path kinds, never conflated.** _Display_ (`~`-abbreviated, for the header), _lexical_
(from the root, for "copy relative path"), _canonical_ (provider-canonicalised, the identity
used by `singletonKey` and by the D9 cycle check).

**Canonical identity is returned by both reads.** `Listing.Canonical` comes back from every
successful list, not only from symlinks, so the root and every ordinary ancestor speak the same
identity vocabulary; `Content.Canonical` comes back from every read, because `Entry.Path` is
lexical and `singletonKey` needs identity — without it two symlinks to one file open two tabs
claiming to be different files, which is D12 failing in exactly the case it exists for. The provider resolves
the canonical directory **and then lists that**, rather than canonicalising and listing
independently — otherwise a symlink retargeted between the two calls returns the identity of
A with the entries of B. That is not atomicity against remote mutation, which SFTP cannot
offer; it is internal coherence of one operation, which it can.

**Reading is bounded and streamed.** `maxBytes <= 0` means the server default; the effective
limit is `min(requested, 2 MiB)` — the parameter can only lower the ceiling. The provider
reads at most `effectiveLimit + 1` bytes and never the whole file, so the memory guard holds
for a 40 GB file; `Truncated` is true iff that extra byte was readable. Size and mtime are
sampled before and after; a difference sets `Changed`, which is how the viewer can say "this
changed while I was reading it" instead of presenting an unknowable mixture.

**`Binary` is a heuristic and is labelled as one**: a NUL among the bytes actually read. A
binary whose first bytes are NUL-free reads as text; accepted. When `Binary`, `Text` is empty
and the viewer says "binary file, N bytes" — never base64.

#### The SFTP lease (D3)

`internal/ssh` gains `RealClient.FSConn(ctx, host, opts…) (ssh.FSConn, error)`, a **sibling of
`DiscoveryConn`** (`ssh_discovery.go:378`) built from the same two ingredients:
`pool.AcquireDial` plus a release func. It differs in what it exposes — an SFTP subsystem
rather than `Exec` — and is identical in the three properties that matter:

- It owns **its own** pooled reference, never the tab's, so closing the terminal cannot drop
  the transport under an in-flight read.
- `Done()` closes on connection loss and **not** on `Close()`, so an intentional stop is not
  read as a lost connection.

`internal/filesystem` declares its own narrow consumer interface for it, the way
`internal/discovery/discovery.go:113` does. `ssh.SSH` (`ssh.go:113`) is **not** widened: it
stays `Connect`/`Close`, and a feature that needs a lease depends on a lease interface.

#### Cancellation: measured, not assumed

The superseded draft said "pkg/sftp calls are not context-cancellable" and built everything on
it. Checked against the pinned module: of the 39 `*Client` methods in
`pkg/sftp@v1.13.11/client.go`, exactly one public method takes a context. So the truth is
split, and the design splits with it:

- **Listing is natively cancellable.** `ReadDirContext` (`client.go:379`) issues repeated
  `SSH_FXP_READDIR` packets and checks the context on each one. The elapsed-time cap above is
  enforced there, properly, with no subsystem surgery — which matters because listing is the
  operation most likely to be slow.
- **Everything else is not.** `Stat`, `Lstat`, `Open`, `RealPath` and `File.Read` take no
  context. For those, **cancellation is closing**: the lane closes the subsystem to unblock
  the call, then waits.

One SFTP client per binding multiplexes all its requests, so a **bounded operation lane** caps
concurrent in-flight calls per binding. On a hard timeout the client is closed and poisoned,
its pooled reference released, and the binding reports itself dead — a visible state, not a
retry loop.

**This is an acceptance condition, not a claim.** The implementation must demonstrate, against
a server that accepts requests and never replies, that closing the SFTP subsystem does unblock
each non-context call we make. Until that is shown, the design may promise **either** "no
operation goroutine outlives close" **or** "close returns within a hard deadline", and not
both — the superseded draft promised both, which is what made its deadline a word rather than
a mechanism. If the demonstration fails, the choice must be made explicitly and written here.

**Three different things are called a lease and must not share a name in code.** The _pooled
SSH reference_ (`ssh.FSConn`), the _operation slot_ in the lane, and the _use-guard_ that
keeps a binding alive across one handler call are three objects with three lifetimes.

#### Bindings

```go
// Binding is opaque outside this package: its provider is unexported, so there
// is no route to a filesystem that skips Acquire.
type Binding struct {
    id         string
    endpointID string   // attestation; empty for local
    provider   Provider // unexported on purpose — see Acquire
}

func (b *Binding) ID() string         { return b.id }
func (b *Binding) EndpointID() string { return b.endpointID }

// Handle is the only thing that can reach a filesystem. It is what Acquire
// returns, it holds the use-guard for its lifetime, and it is invalid after
// release.
type Handle interface {
    Root(ctx context.Context) (Root, error)
    List(ctx context.Context, path string, page Page) (Listing, error)
    Read(ctx context.Context, path string, maxBytes int64) (Content, error)
    Watch(ctx context.Context, paths []string) (WatchMode, error)
}

// Caller is who is asking. filesystem declares it and transport satisfies it —
// the direction internal/discovery/discovery.go:113 already established, and the
// only one available: connState and wsConn are unexported in transport, and a
// filesystem that imported transport would point the dependency backwards.
type Caller interface {
    Owns(sessionID session.ID) bool
}

func (r *Registry) Acquire(id string, c Caller) (Handle, func(), error)
```

`connState` does **not** satisfy `Caller` as it stands: Go matches interface methods by name,
and its method is `has(sid)` (`ws.go:1361`). `transport` therefore adds an exported
`Owns(session.ID) bool` that forwards to `has` — one line, no new state, and the authorisation
answer still comes from the one place that already owns it.

`Provider` stays exported because `local` and `sftp` implement it from their own packages.
What is unreachable is a **bound** provider: nothing outside `filesystem` can get one out of a
`Binding`, so "every handler must remember to check" is not a discipline anybody has to keep.

A `Registry` maps `bindingId → Binding`. `files.open{sessionId}` resolves the session, builds
the provider, takes the pooled reference, and returns the id. Every later call takes the id.

**A binding is bounded by its session, not by its WebSocket.** Two different lifetimes, and
conflating them breaks AD-9:

- Closing the **terminal** closes its bindings. The pooled reference exists to protect an
  in-flight read from a concurrent close, not to keep an SSH connection alive because a file
  viewer is open somewhere. A viewer whose binding is gone keeps what it already has on
  screen, says the source is unavailable, and **issues no further calls** (§5.6). The rejected
  alternative — a binding that survives its terminal — means closing your SSH tab leaves nocx
  connected, which no user would predict. If that capability is ever wanted it is an explicit
  "keep the connection for open files", visibly owned, not a silent lease semantic.
- Losing the **WebSocket** changes nothing. AD-9 keeps the backend session and its replay ring
  alive, the old `*wsConn` is destroyed (`ws.go:831`), and `attach` installs a new one as the
  session's subscriber. A binding that stored its originating `*wsConn` would spend the rest
  of its life writing notifications to a closed socket.

**Handlers hold a use-guard, not a lookup.** `Reg.Get` returns a session pointer that
`Reg.Close` can invalidate immediately (`session.go:240`); a handler that resolved a binding
and then called it can hit a closing provider. The guard is held for the call's duration and
close waits on it, under the acceptance condition above. Testing only "unknown binding id"
does not cover this — the race is between lookup and use.

#### `endpointId` (D4, D6)

**v1 attests the resolved SSH destination and route. It does not attest the physical host.**
That sentence is the honest claim and it is deliberately weaker than the draft's "what the
transport actually reached".

```
endpointId = "v1:" + base64url(SHA-256(canonical encoding of the attestation))
```

The attestation is a **structured, ordered** record — hops as an array, each with resolved
address, port and effective principal — with a pinned canonical serialisation. Not a
concatenation with separators: `A→B→target` and `A→Btarget` must not be able to collide, and
`‖`-joined fields collide unless every field is length-prefixed. The effective principal is
the authenticated SSH username, not a credential id.

**Host-key fingerprints are deliberately not in v1**, and this is a trade, not an oversight.
The threat this feature named is a profile edited between the drop and the reconnect — host,
user, port, jump route — and the fields above cover it completely. Fingerprints answer a
different question, and answer it imperfectly: a legitimate key rotation changes one, a server
with several trusted host keys can negotiate a different one on a later connection, and an SSH
host certificate can renew while the trusted CA identity is unchanged. So a naive fingerprint
would disable Reload after ordinary maintenance, with a message that reads like a security
alert and is not one.

What is genuinely lost is real and is recorded here rather than hidden: **`prod.example.com`
rebuilt onto a new VM, the user accepting the changed host key, is a different filesystem that
v1 will call the same endpoint.** SSH's host-key check answers "may I authenticate to this
endpoint", not "is this the same filesystem that produced the open viewer" — the two are not
the same question, and v1 answers only the first. A v2 should carry stable _verified_ host
identity — the CA principal where certificates are in use, the accepted key otherwise — rather
than hashing whichever key happened to be negotiated. **The version prefix is what makes
deferring it safe**: v2 will not match v1, so a viewer restored across the change refuses
rather than silently matching. The versioning earns its keep on its first use.

The capture is also new plumbing whenever it lands: successful host-key verification currently
delegates to a callback and discards the key, only `ProbeConfigWithResult` (`ssh_probe.go:36`)
retains a fingerprint, jump hops verify through their own callback (`ssh_dial.go:230`), and a
pooled reuse performs no handshake at all. So attestation must be captured **at first dial and
stored on the pooled connection**, and read from there by every later lease — never
reconstructed afterwards from current config or `known_hosts`, both of which may have changed.

Neither existing key is this. `ssh.IdentityKey` (`ssh_resolver.go:272`) is the resolved
`user@host:port` from the `ssh -G` answer, and omits the jump route. The pool key
(`ssh_dial.go:43`) additionally separates the credential principal, which is closer, but it is
an authorisation boundary for connection sharing. Both are documented as authorisation
boundaries, so quietly widening either here would widen that boundary too.

### 5.2 Wire — control plane, JSON-RPC (AD-1)

| Method         | Params                             | Result                                                                                                                          |
| -------------- | ---------------------------------- | ------------------------------------------------------------------------------------------------------------------------------- |
| `files.open`   | `{sessionId, rootPath?}`           | `{bindingId, endpointId, root:{path,display,inferred,inferredReason}}`                                                          |
| `files.list`   | `{bindingId, path, offset, limit}` | `{state:'ok', path, canonical, entries[], offset, total, hasMore, rev}`, or `{state:'tooLarge', …}`, or `{state:'timedOut', …}` |
| `files.read`   | `{bindingId, path, maxBytes}`      | `{path, canonical, text, size, modTime, truncated, binary, lossy, changed}`                                                     |
| `files.watch`  | `{bindingId, paths[]}`             | `{mode, degradedReason}` — replaces the watch set for this binding                                                              |
| `files.close`  | `{bindingId}`                      | `{}`                                                                                                                            |
| `files.reveal` | `{bindingId, path}`                | `{}` — local bindings only; errors on a remote one                                                                              |

A change is announced by a **server-initiated notification**. Two existing mechanisms supply
the two halves, and they must not be confused: `broadcastSettingsChanged` (`ws.go:2888`) is
the precedent for the **shape** — a `jsonrpc` frame with `method` and `params` and no `id` —
but it writes to every connection, which is the wrong addressing here. The precedent for
addressing **one** connection is `sessionRx.subscriber` (`ws.go:43`), which already binds a
session's output to a single `*wsConn`; and every control handler already receives its
`wconn *wsConn` (e.g. `handleOpen`, `ws.go:1049`), so the connection that opened a binding is
in scope when the binding is created.

```
<-- {"jsonrpc":"2.0","method":"files.changed","params":{"bindingId":"…","path":"…","rev":"…"}}
    // rev is optional — see below
```

**The destination is resolved at emit time, never stored.** A binding records the `sessionId`
it belongs to; when it has something to announce, the backend asks that session for its
**current** subscriber and writes only there. That is what survives an AD-9 reconnect: after
`attach` installs a new `*wsConn`, notifications follow it without anything being transferred.
When there is temporarily no subscriber, invalidations accumulate as a **set of dirty paths**,
not a queue of events — naturally bounded, because a path can only be dirty if it is in the
watch set. On re-attach the set is emitted once and cleared. Collapsing to a single
`files.changed` would lose which directories moved and force a re-list of every expanded one;
queueing every event would replay a burst that meant one change.

The notification carries **no entries**: it is an invalidation, and the client re-lists through
`files.list`, so exactly one code path renders a directory.

**`rev` is optional, and its absence is not a defect.** It is present exactly when the backend
already knows it and costs nothing to include — SFTP polling necessarily computed the new
digest, because computing it is how the change was detected at all. It is absent for a local
`fsnotify` event, where the kernel said "something happened" and nothing has been re-listed.
Making it required would force the backend to list a directory in order to announce that it
should be listed, which is the same work done twice and a race besides. So `rev` is what lets
a client skip a re-list it has already applied, when that shortcut is available, and the
client's own comparison after re-listing is what it falls back on when it is not.

`files.watch` **replaces** the watch set rather than adding to it, so collapsing a directory
cannot leak a watch: the client sends the set it currently wants and the backend diffs. The
swap is atomic and idempotent — a newly-added watch that fails to establish must not take the
healthy existing watches down with it.

**`files.list` returns a discriminated union, and `state` is the discriminator.** The three
outcomes of D14 are not an object with everything optional — that shape accepts all three at
once and none of them precisely. So the schema is a `oneOf` of three closed branches and the
normal one carries `state: 'ok'`, which the §5.2 table does not list because the table predates
the decision.

One consequence is worth stating because a gate can be fooled by it: **`additionalProperties:
false` cannot sit at the top level of a `oneOf`.** With no top-level `properties` it would
forbid every branch's fields and make the schema unsatisfiable, so the closure lives in each
branch instead. Every accepted object is still closed — but a checker that inspects only the
root object will not see it, and will report the schema as unguarded when it is not.

#### The two guards on the wire

**`rootPath` is how the composition layer pins the root.** The panel always passes the
filesystem root `/` — a cwd is never an address here, so the panel's root is independent of
what the shell can or cannot report. The provider interprets the path by its own rules and
falls back to `Root()` only when it is absent or unusable; without the parameter the
constant root was a decision with nowhere to happen.

**`sessionId` appears exactly once**, on `files.open`. That is what keeps the wrong pairing
inexpressible: no parameter can ask for the local filesystem of an SSH session, and no caller
can name a filesystem the backend did not hand out.

**A `bindingId` is not a bearer token, and is unguessable anyway.** Both, because either alone
is a hole. The id is minted from `crypto/rand`, the way the per-launch capability token already
is (`ws.go`, `nocx-hl3`), so it cannot be guessed or enumerated. And every later call re-checks
that the binding's session is in the **requesting** connection's `connState` — one map lookup,
and it is what holds if an id ever reaches a log, a screenshot or a crash report. Checking only
at `files.open` would make every subsequent call trust a string.

**There is still exactly one place that authorises, and it is not the handler.** The re-check
lives inside a single `Registry.Acquire(bindingId, caller) (Handle, release, error)` — the same
call that takes the use-guard of §5.1 — and **no handler may reach a filesystem by any other
route.** That is what keeps D1's claim true: a handler cannot forget a check it never
performs, and the alternative D1 rejected — `sessionId` on every call, checked by every
handler — is rejected precisely because there the check is copied N times and the Nth copy is
the hole. Enforced the way this repo enforces such things: the provider is unexported and
`Acquire` is the only thing that returns one.

**`files.open` succeeds only if `state.has(sessionId)` on the requesting connection.** This is
the wire-level enforcement of §0 and it is not a new invention — `connState` exists for
exactly this and says so (`ws.go:652`: "gates data-frame/resize/close so a connection cannot
touch a session it has not opened or reattached to"), and `handleResize` already does
`if !state.has(sid)` before `registry.Get` (`ws.go:1361`). Resolving through the global
`session.Registry` instead would let any authenticated WebSocket that learned another
connection's session id open that session's filesystem. Tested with two live clients: B knows
A's valid session id, and `files.open` on B fails.

`files.reveal` **errors** on a remote binding rather than silently doing nothing. The UI does
not offer it there at all; the backend refuses anyway, because a UI-only guard is one bug away
from being no guard.

Further guards, each with a test: the 2 MiB ceiling and the streamed read; the openability
table of §5.1 enforced from freshly-read metadata; paths absolute and cleaned by the
**provider's** rules; permission denied an explicit node state, never a silently empty
directory; a dead SFTP channel a rendered error.

### 5.3 Contracts

Every method above gets a JSON Schema in `contracts/` **in the same commit** (`nocx-bt3w`),
with `additionalProperties: false` and explicit `required`, and three checks each.
**`files.changed` is the seventh wire shape and gets the same three**, notification or not: it
is unsolicited, it is the only shape whose addressing can be wrong, and an untested
notification is exactly where an addressing defect hides.

1. `npm run contracts:check` — the committed generated types match the schema.
2. `…_DTOConformsToContract` — the Go struct marshals to something the schema accepts.
3. `…_OverTheWireConformsToContract` — the real result off the real socket. This is the one
   that catches a field the server never sends.

The renderer's types are generated and re-exported; the client declares nothing of its own.

### 5.4 Frontend

**Files is one more sidebar view.** `frontend/src/main.tsx:295` already registers Ports as
"the first real one" and names Explorer as future work; `sidebar.tsx:50` already defines
`SidebarViewDescriptor {id,title,icon,view,actions,order}` and gives every view a reactive
`visible()`. **Files is the first icon in the activity bar** — it registers with an `order`
below Ports, so it sits at the top of the view zone, above every other view present or later
added. That is a product requirement, not a consequence of a number somebody picked, and it
gets an assertion: the first icon in the activity bar's view zone is Files. **Nothing about
the shell is rebuilt or forked.**

**`activeOrigin()` goes on the `TabContent` seam, and reaches the view through
`SidebarViewProps`.** Today `SidebarViewProps` exposes only `activeProfileId()`, designed for
Ports and not enough here: an alias tab has no profile, a profile is editable, and local is
the synthetic string `"local"` (`ports-client.ts:11`). Files needs
`activeOrigin(): {tabId, sessionId, kind, cwd, cwdVerified, binding} | null`.

`TabManager` must ask the **active tab** for it, not test what the active content is.
`tabs.ts:698` currently derives the active scope by checking whether the content is
`TerminalContent`; adding a second `instanceof FileViewerContent` branch there would make
`TabManager` own a growing switch over content implementations, against the polymorphism the
`TabContent` seam exists for (`tab-content.ts:1`). So the accessor is an optional capability
on `TabContent`: terminal content answers from its session, viewer content answers from the
binding it was opened with, and `TabManager` never learns which class replied.

This is an addition to the shared seam, not a private copy inside the view — a private copy is
exactly the pattern `nocx-ycet` exists to end.

**Two authorities — and only one of them crosses the wire (D4).**

```ts
interface Binding {
  readonly bindingId: string // backend-issued
  readonly endpointId: string | null // backend-attested; null for local
}
```

The client half is **not** a wire shape. The JSON-RPC request id already correlates a result
with its exact request, and the code that issued the call already knows, in its own closure,
which `{tabId, generation, bindingId}` it issued for. So the check happens where the promise
resolves: apply the result only if that captured triple still matches the view's current
state, and only if the generation is not older than what has been applied. That is what stops
a `files.list` for tab A, still in flight when the user activates tab B, from painting A's
remote listing into B's tree.

Echoing a client-minted token through the backend would have added a field to every params
and result schema, every DTO and every handler, to re-derive something the caller never
forgot. **No client-minted _identity_ crosses the wire** — paths, limits and watch sets
obviously do, and are inputs rather than claims about who is asking. It removes the
possibility of authorising on `tabId` by making `tabId` unavailable to authorise on.

`files.changed` is the exception that proves it: being unsolicited, it has no request to
correlate with, so it carries `{bindingId, path, rev}` — backend identity only — and the
client maps `bindingId` to its current tree generation itself.

**Panel focus.** The panel follows the active tab's origin. A viewer tab has no terminal
session, so it answers with the binding it was opened from and the panel keeps showing that
machine — **never a silent fall back to local**, which would breach §0 in the same gesture as
the panel's own primary action. But "keeps showing that machine" is about pixels, not calls:
when the binding is dead the panel keeps the last tree **visible** and issues no `list`,
`watch` or `read` against it.

**The panel follows the terminal's cwd by revealing, not by re-rooting** (nocx-r3bz). The
tree is rooted at the filesystem root `/` and stays there; a VERIFIED OSC 7 cwd on the
active origin walks the chain from the root down to itself — listing and expanding each
level that is not already expanded, paging where the target sits beyond the first page —
and selects the target, which the panel scrolls into view. Nothing is ever collapsed: a
directory the user opened by hand stays open, and re-rooting — the alternative — would throw
expansion state away on every `cd` and cage the user inside the cwd. An unverified or absent
cwd reveals nothing; with the root a constant, nothing is substituted and AD-5 has nothing
to surface, so the panel shows `/` and highlights nothing, full stop. A viewer tab answers
with NO opinion about where the user is — its frozen origin must never drive a reveal — so
activating a viewer moves nothing.

**Kit only.** `Toolbar`, `IconButton`, `EmptyState`, `Spinner` come from `ui/`. Neither
reference product had an off-the-shelf tree to copy — Orca hand-writes ~41 files over
`@tanstack/react-virtual` and `@dnd-kit`, termic hand-writes `FileTree.tsx` over Radix, and
both are React. Kobalte, the Solid equivalent, was measured and rejected
(`2026-07-27-kobalte-spike-report.md`: ~34 KB gzip of shared core against a 25–35 KB total
budget) and has no tree primitive regardless. So **`ui/tree-row.tsx` is a new kit component**
— one module, one CSS file in `styles/components/`, a stable identity class, a test, a row in
the kit README. It is not built inside the surface. Where `CollectionView`'s row variance
already fits, it is extended rather than forked.

**Viewer tab.** A `ContentDescriptor` with `singletonKey = "${endpointId ?? 'local'}:${canonicalPath}"`
(D12), and a `restoreDescriptor` of **`null`** — see below.

**But tab restore does not exist yet, so the viewer's `restoreDescriptor` is `null`.**
`restoreDescriptor` is written in four places — `tabs.ts:456` and `:504`, `main.tsx:226`,
`state/tab-model.ts:255` — typed `unknown` (`tab-content.ts:143`), and **read nowhere**.
Nothing serialises the tab list; nothing reconstructs a tab from a descriptor. That is a
reachable write path with an unreachable read path: the defect AGENTS.md records this
repository having shipped once already.

Writing a fifth one and calling it free would be committing the same defect while quoting the
rule against it. The `{type:'file', endpointId, path, displayHost}` shape above is what the
restore bead should adopt when it supplies a reader; until then the viewer passes `null`, as
`main.tsx:226` already does. The restart row leaves the lifecycle table for the same reason.

Content is CodeMirror 6 in read-only mode:
already a dependency, and it brings line numbers, selection and search for free. Syntax
highlighting needs `@codemirror/language` plus language modes — a **small registry** of the
formats that actually turn up in terminal work, with plain text as the correct fallback, not
one package per language that exists.

**Titles carry provenance asymmetrically.** A remote file is `srv-01 · nginx.conf` plus the
profile colour badge (`nocx-9le.4`); a local file is the basename alone. **Absence of a host
marker is what means "this machine"**, so the marker must never be spent on the local case.

### 5.5 Refresh (D5)

Watching is a provider capability. The panel is told "this directory changed" and never
learns which mechanism said so.

**Local — fsnotify** (new dependency), one non-recursive watch per **expanded** directory.

- Events are **invalidation hints, never the diff**: coalesce, then re-list the directory and
  compare `Rev`. An editor's write-temp-then-rename produces a burst that means one change.
- Watches are re-established when a watched directory is renamed, deleted, recreated, or the
  backend reports overflow.
- There is a ceiling on watched directories and on outstanding re-lists. Expanding a large
  dependency tree must not exhaust inotify watches or descriptors.

**Remote — polling.** SFTP has no change notification, and this asymmetry is honest rather
than a gap to be closed cleverly. Poll the expanded directories, compare `Rev`, emit only on
difference. Jittered interval, per-host concurrency limit, exponential slow-down on repeated
failure, paused while `visible()` is false, and one immediate poll when the panel becomes
visible again.

**A poll costs a whole directory, not a page** — see §5.1: `Total`, the ordering and the
digest all need the complete enumeration. So the per-tick cost scales with how large the
expanded directories are, not with how many rows are on screen, and a directory that hit the
entry cap is **not polled at all**. Saying this here rather than discovering it in profiling
is the difference between a poll interval chosen and one guessed.

**Degradation is a state, not a toast — but it is a badge, not a banner.** Three levels, and
the distinction between the first two is the whole point:

- **Remote polling is the designed mode and warns about nothing.** SFTP has no notifications;
  saying so permanently would be noise, and noise is what teaches a user to stop reading.
- **A local watch that could not be established** falls back to polling and reports
  `mode: 'polling', degradedReason`. That is a persistent **warning badge beside Refresh** —
  "Polling", with the detail on hover: live watching is unavailable, we are checking
  periodically. The transition still gets its toast; the badge is what answers "why is this
  stale?" ten minutes later, when the toast is long gone. It clears the instant watching
  recovers.
- **Refresh that has actually stopped** — polling failing repeatedly — escalates to a sticky
  inline banner with Retry, because that is materially worse than degraded-but-working and must
  not look the same.

A soft degrade the UI does not admit is how a feature that does not work survives a release.
A permanent banner over a feature that _is_ working is how the admission stops being read.

**Rejected, and why they stay rejected.** A shell hook emitting changes would observe only
what passes through that shell — not the agent, not cron, not another session, not another
person — while the product claims automatic refresh; a partial observer sold as a complete
one is worse than polling. `inotifywait`/`fswatch` assume software on the host. The Tier-B
remote helper is the architecturally sanctioned path — `architecture.md:203` names "richer
remote metadata (file-tree)" as its revisit trigger and reserves the `metadata` msg-type for
its feed — and it is a **later** epic (`nocx-if6`), consent-gated, that must augment polling
and degrade back to it. Bending the current shell-integration scripts into a provisional
helper is the trap.

### 5.6 Lifecycle

Four different things get called "disconnected", and only two are ours.

| State                             | Panel                              | Open viewer tabs                                                                  |
| --------------------------------- | ---------------------------------- | --------------------------------------------------------------------------------- |
| WebSocket drop (frontend↔backend) | Unchanged — AD-9 replays by offset | Unchanged; notifications follow the new subscriber after `attach`                 |
| SSH connection lost               | "Connection lost" banner; no poll  | Content stays; **Reload** disabled                                                |
| Originating terminal closed       | Follows the next active tab        | Content stays; "source unavailable"; **Reload** disabled; **no calls issued**     |
| Reconnected, `endpointId` matches | Rebinds; refresh resumes           | **Reload** enabled — user-invoked, never automatic (D6)                           |
| Reconnected, `endpointId` differs | Rebinds to the new machine         | Stays stale; "reconnected to a different host or user"; **Reload** stays disabled |

The **app-restart** row is deliberately absent: tab restore does not exist (§5.4), so a row
describing it would be a promise this epic cannot keep. D13 says only what is enforceable
today — if and when restore lands, identity persists and bytes never do.

D6 is the load-bearing line. A profile can be edited between the drop and the reconnect —
host, user, port, jump route. Rebinding on profile id would refresh a viewer labelled
`root@srv-01 · /etc/nginx.conf` from `deploy@srv-02` while keeping the label. Matching on the
backend's attestation, and only on an explicit Reload, is what makes that impossible.

**SSH reconnect itself is not this feature's to build.** It belongs to `nocx-9le`, does not
exist yet, and this design **consumes** it. The panel works without it, just without a way
back. That is a dependency edge, filed, not an assumption.

## 6. Sequence

1. **ADR** recording §3. Required by `nocx-708q` before implementation.
2. `internal/filesystem` + the `local` provider + `files.open/list/read/close` + contracts +
   the binding registry, its `connState` authorisation and its use-guard.
3. Files view (first icon in the activity bar) + tree row in `ui/` + viewer tab + dedup.
4. `ssh.FSConn` + the `sftp` provider + `endpointId` + the lifecycle of §5.6.
   **The epic does not close before this.**
5. Watching: local fsnotify, then SFTP polling, then the degraded-mode indicator.
6. Copy path (relative, absolute) and `files.reveal`.

## 7. Testing

**Every external call has a test where it fails.** Permission denied; ENOENT; a directory
larger than one page; a file over 2 MiB; a binary; a NUL at byte 9000; invalid UTF-8; a file
that changes size mid-read; a FIFO; a dead SFTP channel; a server that accepts and never
replies; a session that dies between lookup and use; a symlink cycle; an inotify watch that
cannot be established; a directory above the entry cap; an enumeration that exceeds the time
cap; a symlink retargeted between the list and the read.

**Falsifying §0** — the scenarios that would have caught the previous drafts:

- Switch A→B while `files.list(A)` is in flight; assert B's tree never shows A's entries.
- Close the originating terminal with a viewer open; assert the viewer reports the source
  unavailable and **never** re-reads through any other binding.
- Reconnect to a **different** endpoint; assert Reload stays disabled and nothing refreshes.
- **Two live WebSocket clients: B knows A's valid `sessionId`; assert `files.open` on B fails.**
  This is §0 enforced on the wire, and it is the test that would catch the whole rule being
  routed around by resolving through the global registry.
- Drop and re-attach the WebSocket with a watch active; assert `files.changed` reaches the NEW
  connection — the assertion that fails if a binding stored its originating `*wsConn`.
- Two out-of-order refresh responses; assert the older is dropped.
- A remote path whose syntax differs from the local OS; assert the provider's rules were used.
- An SSH session opened with no explicit cwd; assert the panel still opens with the
  filesystem root — `files.open` is called with `rootPath: '/'` whether or not the cwd is
  verified — and that a verified cwd change reveals (expands and selects) without re-rooting.
- A directory that gains an entry between page 1 and page 2; assert no row is duplicated or
  skipped.

**"And on a normal machine it succeeds."** For every "returns an error when…" above, the
paired test that the ordinary path works — the `contentkey` lesson.

**One user-reachable end-to-end assertion**, through the seam a person touches:

> From a cold start the Files icon is **first** in the activity bar, present and enabled, and
> the panel is open on it; the tree shows the root; expanding a directory lists a page and
> "show next" reveals the rest; clicking a file opens a tab whose content matches the file;
> its title carries the host iff the origin is remote; writing to the file from outside nocx
> makes the row update without anyone pressing anything.

An earlier draft of that sentence began "from a cold start **with the panel collapsed**", and
the acceptance run failed on it. The premise was stale, not the product: `mountSidebar`
activates the first registered view and does not collapse, which `f1621f2` established long
before this epic — checked against the commits rather than assumed, because the alternative
reading was that this epic had regressed it. Both reference products also show the tree on
launch. Clicking the icon while its own view is open **collapses** the panel, which is the
existing toggle behaviour and is what the acceptance spec now asserts.

The last clause — the row updating with nobody pressing anything — is the one this epic does
not meet, deliberately: watching is `nocx-rkk9` and `Provider.Watch` returns a typed
unavailable error rather than a channel that never fires. It is written here unmet rather than
quietly removed, because a criterion edited to match what was built cannot fail.

Headless via `cmd/devharness` plus the `NOCX_WS_PORT` shim — no wails, no GTK, no display.
The e2e suite gets a disposable `$HOME` (`NOCX_E2E_HOME_DIR`).

**Invariants as intervals, both ends named:**

- From the moment a binding is issued until it is closed or its connection is lost, the
  tree's root is the filesystem root `/` and never changes; a verified OSC 7 cwd reveals
  (expands and selects) from it and never collapses anything, including a late OSC 7.
- From the moment a local watch fails until it is re-established or the view is closed, the
  header states that refresh is polling.

**Contracts** — the three checks of §5.3, for all six methods **and for the `files.changed`
notification**.

**Reachability** — `deadcode -filter 'nocx/internal/filesystem' ./...` is clean and
`internal/app/app.go` wires the providers. Then the other direction: assert a caller for the
watch path specifically. A reachable read path hiding an unreachable write path in the same
package is precisely what `nocx-rtg0` shipped.

## 8. Bead changes

| Bead             | Change                                                                                                                                                                | Reason                                                                                                         |
| ---------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------- |
| `nocx-708q`      | Rescope to this document. Drop "deliver the multi-view mechanism" — it shipped with Ports. Drop the name filter and git markers. SFTP enters the completion criterion | `main.tsx:295`; otherwise the feature is dead where it differentiates                                          |
| `nocx-708q`      | **Remove** the dependency edge on `nocx-jv3q`                                                                                                                         | Tab groups are not a precondition; provenance rides on the tab                                                 |
| `nocx-jv3q.1/.2` | **No change.** The previous draft legislated their grouping key and removed drag-between-groups; that is reverted                                                     | Not this design's decision to make                                                                             |
| `nocx-9le.5`     | Narrow to upload/download/drag-drop transfer; listing and reading move here                                                                                           | Otherwise two epics build directory listing twice                                                              |
| `nocx-9le.4`     | Link as the provenance carrier for viewer tab badges                                                                                                                  | The colour badge gains a second consumer                                                                       |
| `nocx-goi0`      | Unchanged; still deferred by D10                                                                                                                                      |                                                                                                                |
| `nocx-bkmy`      | Unchanged; still deferred                                                                                                                                             |                                                                                                                |
| `nocx-terg`      | Unchanged; still deferred                                                                                                                                             |                                                                                                                |
| new, epic        | **File manager: mutation** — create, rename, delete, move, duplicate, and editing with conflict detection, both providers                                             | The owner's second slice; needs its own design for atomic write, trash-vs-unlink asymmetry and conflict policy |
| new, `nocx-9le`  | **SSH session reconnect on connection loss**, with an `ask`/`auto`/`never` setting                                                                                    | §5.6 consumes it; no bead covers it                                                                            |
| new              | `ssh.FSConn` — an SFTP-capable sibling of `DiscoveryConn`                                                                                                             | D3                                                                                                             |
| new              | `SidebarViewProps.activeOrigin()`                                                                                                                                     | §5.4; `activeProfileId` cannot express an alias tab                                                            |
| new              | `files.reveal` native seam (Wails)                                                                                                                                    | No such capability exists in the backend                                                                       |
| new              | Insert path at the originating prompt + shell-escaped copy                                                                                                            | §4 Out; needs a dialect decision                                                                               |
| new              | **Tab restore has no reader.** `restoreDescriptor` is written in four places and consumed nowhere; either implement restore or delete the field                       | §5.4; a write path with no read path — the `nocx-rtg0` shape, found again                                      |
| new              | `endpointId` **v2**: verified host identity per hop, captured at first dial and stored on the pooled connection                                                       | §5.1 records exactly what v1 does not attest; the version prefix is what makes adding it safe                  |

## 9. Open questions

None blocking. The page size (D10), the 2 MiB ceiling, the entry and time caps (D14), the poll
interval, the operation-lane timeout and the watched-directory ceiling are starting numbers,
to be tuned once the panel is in daily use. Each is named in code with the reason it is a
number rather than a constant somebody picked.

One item is deliberately deferred with its cost written down rather than left open: `endpointId`
v1 does not attest the physical host (§5.1). The case it misses is named there, and v2 is a
bead.

## 10. Review history

Seven adversarial rounds, ending when one returned nothing.

- **2026-08-01, round 1** — three breaches of §0 in the first draft: a response applied to the
  wrong tab, an action aimed at the active tab, a reconnect rebinding by profile id.
- **2026-08-06, round 2** — the five items in §2. Three were things the repository had already
  solved or already contradicted and the document had neither read nor cited: `DiscoveryConn`,
  `main.tsx:295`, `resolveSessionCwd`.
- **2026-08-06, round 3** — against this document. Seven findings, of which two would have
  shipped defects that no amount of later testing of _this_ feature would have surfaced:
  **`files.open` resolving through the global session registry instead of `connState`**, which
  is a cross-connection read of another session's filesystem; and **a binding storing its
  originating `*wsConn`**, which sends every notification to a closed socket after an AD-9
  reconnect that the lifecycle table promised was invisible. Three more were contradictions
  inside the document — a `RequestToken` in §5.4 that appeared in no wire shape, a
  `Provider.Canonical` with no method to reach it, "every method gets a contract" against "all
  six methods" — and each would have been resolved by whoever wrote the handler, silently and
  in one direction.

Four of round 3's recommendations were argued rather than accepted, and the argument changed
three of them:

- **Session-bounded bindings** stand. A read-only viewer keeping an SSH transport alive after
  the user closed the terminal is behaviour nobody would predict; the reviewer conceded, with
  the correction — taken — that the bound is the _session_, not the WebSocket.
- **`Provider.Root`** stands, because local and SFTP genuinely compute it differently. The
  reviewer's refinement was taken — the composition layer owns the root — and carried
  further by nocx-r3bz: the layer pins it to the filesystem root `/`, and a verified OSC 7
  cwd reveals from it instead of overriding (D2).
- **Expanding directory symlinks** stands. The reviewer's layer objection was right and was
  taken — `Entry.Cycle` was uncomputable where it sat — but the fix here is cheaper than the
  one offered: `Listing.canonical` on a call the client already makes, instead of a
  `files.canonical` method and a round trip per expansion.
- **Host-key fingerprints out of `endpointId` v1** stands, but the reviewer was right that the
  justification was not: SSH's host-key check answers "may I authenticate", not "is this the
  same filesystem". §5.1 now records the case v1 misses instead of claiming there is none.

- **2026-08-06, rounds 4–7** — the document was then re-read against itself until a round
  returned nothing. Each of these found less than the one before, which is what convergence
  looks like: an OSC 7 override that D2 decided and no wire shape could express; a
  `singletonKey` that needed a canonical file path `files.read` did not return; a `Binding`
  whose exported `Provider` field contradicted, in the same document, the claim that `Acquire`
  was the only route to a filesystem; an `Acquire` whose signature took two types that are
  unexported in `transport`, which would have inverted the dependency; and finally a `Caller`
  interface that `connState` was said to satisfy and does not, because Go matches interface
  methods by name and its method is called `has`.

  None of these is interesting on its own. Together they are the argument for the loop: **every
  one was introduced by a fix to the round before it.** A document is not correct because its
  last edit was correct.

The pattern worth keeping from all seven rounds is the same one: **every finding lived in the
gap between what the document asserted and what the code says.** Round 2 found it in the
repository the draft had not read; round 3 found it in the document's own unread halves.
