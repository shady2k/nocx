# Git manager — design

- **Status:** draft, revision 7 (six adversarial review rounds + an owner correction to the seam), awaiting owner approval
- **Date:** 2026-08-06
- **Brainstorming bead:** `nocx-5nej`
- **Predecessor this is modelled on:** `.internal/specs/2026-08-06-file-manager-design.md`

## 0. The one rule

**The panel shows the repository your shell is standing in, and never another one.**

Everything below follows from that sentence: why the repository's identity is asked of git
on every cwd change rather than computed from the path, why the panel on an SSH tab shows
nothing rather than the local repository, and why the Commit button disappears with the
repository rather than staying live "on the last one we knew".

The rule is stricter here than it was for the file tree, and the reason is the blast
radius. A tree listing the wrong machine is a nuisance you notice and correct. A `Commit`
that landed in the wrong repository is a corrupted history somebody discovers a day later,
on a branch they did not touch.

## 1. What this is

A **Git** view in the existing left activity bar, third after Files and Ports, showing the
working state of the repository the active tab's shell is in.

- **Primary reading:** what changed, split into Staged and Unstaged, with the branch, its
  upstream and ahead/behind above them.
- **Primary action:** stage and unstage files, then commit them — with a real commit
  message and a real Amend.
- **Secondary reading:** a unified diff of one file, in its own read-only tab.

### Why a git panel in a terminal at all

`docs/vision.md` does not list source control, so the argument has to be made rather than
assumed, and it is the same argument the file tree made: **when an agent TUI occupies the
terminal, the terminal cannot be used to run git.** `git status`, `git diff` and
`git add -p` all need a free prompt. The nocx user runs an agent in the tab and wants to
see what it just wrote and turn it into a commit — the one moment the normal tools are
unavailable.

Unlike the file tree, this argument does not immediately drag the remote case in with it,
because the way to reach a remote repository is already decided and is not exec — see D3.

## 2. What this design crosses, and what those documents already decided

AGENTS.md requires a brief that crosses a boundary to name the `AD`s and ADRs it touches
and what they already decided, **before** it says what to build. Revision 1 of this spec
did not, and an adversarial review found two boundaries it had silently walked across.

| Boundary                   | What it already decides                                                                                                                                                                                                                                                                                                         | What this design does about it                                                                                                                                                                                                                                                                                                                        |
| -------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **AD-1**, cwd on the wire  | "cwd/OSC/prompt markers do **not** cross the control plane… only feed UI + the next `open{cwd}`" — **amended 2026-08-02** (`nocx-m64b`): typed facts derived from renderer state **may** cross, the test being "the backend receives a value it could not have inferred from the stream, never a byte stream it must interpret" | `git.open{cwd}` sends the verified OSC 7 cwd as a typed string. It passes the amendment's test — a path is not a byte stream and the backend parses no VT. Precedent, twice: `history.record` carries a `cwd` today, and `files.open` sends the verified cwd as `rootPath` (`files-client.ts:23`). Naming it here is the obligation revision 1 missed |
| **AD-9**, reconnect        | The backend session and its replay ring survive a dropped WebSocket. The file manager states the consequence explicitly: "**A binding is bounded by its session, not by its WebSocket**… Losing the WebSocket changes nothing" (`file-manager-design.md:370`)                                                                   | Bindings here are session-bounded too, and `git.changed` resolves its destination **at emit time** exactly as `files.changed` does. Revision 1 said the opposite ("every binding is gone on reconnect") and was wrong                                                                                                                                 |
| **AD-6**, byte-blindness   | The backend never parses the byte stream; OSC 7 is surfaced frontend-side                                                                                                                                                                                                                                                       | Unchanged and relied upon: this is _why_ the cwd must be a wire parameter rather than something the backend reads for itself                                                                                                                                                                                                                          |
| **AD-2 / deferred Tier-B** | A cross-compiled Go helper "augmenting the remote shell, **feeding** the reserved `metadata` msg-type". A one-way feed — nothing there defines request/response process execution                                                                                                                                               | D3 defers the remote case to it, and the deferred bead carries the honest statement that an **operation channel is a protocol decision the relay does not have yet** — not merely "a second implementation"                                                                                                                                           |
| **AD-8 / one owner**       | One owner per behaviour; find the existing answer and extend it                                                                                                                                                                                                                                                                 | Three existing answers are reused rather than re-written: the binding/registry shape, the store's response-scope discipline (D17), and `files.changed`'s notification contract                                                                                                                                                                        |

## 3. Decisions

| #   | Decision                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      | Rejected alternative, and why                                                                                                                                                                                                                                                                                                                                                                            |
| --- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| D1  | **A repository is addressed by a `bindingId` the backend issues** from `{sessionId, cwd}`. Only `git.open` takes a `sessionId`; every later call reaches its `Repo` through `Registry.Acquire`, which re-checks caller ownership                                                                                                                                                                                                                                                                                                                                                                                                                                              | Every call carrying `{sessionId, path}`. It spreads the authorisation check across every handler, where one that forgets it is a hole. This is `filesystem` D1/D15 restated                                                                                                                                                                                                                              |
| D2  | **The repository is resolved by running `git rev-parse --show-toplevel` in the tab's verified OSC 7 cwd.** No verified cwd → no repository, and the panel says so                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                             | Deriving it from `session.Cwd()`. That is the session's **start** cwd, and for an SSH session with no explicit cwd it is the **local** `os.UserHomeDir()` — the $HOME substitution AD-5 forbids applying silently                                                                                                                                                                                        |
| D3  | **The local case runs git here; the remote case runs it on the remote helper** — amended 2026-08-13, see [the remote-helper design](2026-08-13-remote-helper-design.md). Revision 1 deferred the remote case to `nocx-if6` phase B; it does not wait on it, because that epic is a persistent PTY lease and git needs none of that. What the helper supplies and no other channel could is stdin (D8), bounds applied where the work happens (D9), and cancellation against a process group                                                                                                                                                                                   | Running git over `DiscoveryConn.Exec` — one shell command string per call (`ssh_discovery.go:23`) with a 64 KiB capture ceiling, so every path becomes a quoting problem and a large diff is unrepresentable                                                                                                                                                                                             |
| D4  | **git owns repository identity. Every cwd change re-asks `rev-parse --show-toplevel`**; the answer, not the path, decides whether to re-bind                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  | A path-prefix test to skip the subprocess. It cannot be made correct: a nested repository (`/outer/vendor/inner`) is inside the prefix and is a **different** repository, so the panel would go on staging `/outer`. Plain string-prefix confuses `/repo` with `/repo2` as a bonus                                                                                                                       |
| D5  | **The `git` binary, through `os/exec`, with argv and no shell.** Both reference products do this and neither ships a git library                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              | `go-git`. It is a second opinion on "what does git think", it diverges in the corners, and it **never runs hooks** — a commit from the panel would silently bypass this repository's own pre-commit gate                                                                                                                                                                                                 |
| D6  | **git runs with an explicitly-constructed environment whose limits are stated in the product**, not with the backend's own                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                    | Inheriting `os.Environ()` silently. A GUI-launched `.app` has a bare launchd PATH; the pre-commit hook would not find `go`, `node` or `bd`. See D6 in full below — revision 1 described the PTY wrongly and promised more than this can deliver                                                                                                                                                          |
| D7  | **One `git status --porcelain=v2 -z --branch --untracked-files=all` answers everything the panel's header and lists need**                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                    | A second subprocess for the branch and a third for ahead/behind. porcelain v2's `# branch.*` headers already carry them                                                                                                                                                                                                                                                                                  |
| D8  | **Paths and messages never ride in argv for a mutation**: `--pathspec-from-file=- --pathspec-file-nul`, `commit -F -`                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         | Putting them in argv. argv has an OS length cap, a path beginning with `-` is read as an option, and a commit message with newlines and quotes is the normal case                                                                                                                                                                                                                                        |
| D9  | **Status is bounded by RECORDS, not by bytes, and the parser keeps counting past the bound.** The lists hold the first `MaxStatusEntries`; `Total` is exact when the stream was consumed to the end, and the panel says "more than N" when it was not                                                                                                                                                                                                                                                                                                                                                                                                                         | Killing the process at a byte ceiling and still claiming an exact total. Once capture stops, the unseen records are unknowable — a promise the mechanism cannot keep. Rendering a silent prefix is worse still (`filesystem` D14)                                                                                                                                                                        |
| D10 | **git computes the diff; the frontend renders text.** Unified only                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                            | Shipping `@codemirror/merge` and diffing two file versions in the browser — a second diff algorithm disagreeing with `git diff` exactly where nobody looks                                                                                                                                                                                                                                               |
| D11 | **A failed commit is one `failed` state carrying git's own stdout and stderr, and the message stays in the form.** We do not classify _why_                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   | Splitting `hookFailed` from `failed`. git exposes no machine-readable discriminator: non-zero can be a hook, signing, `index.lock`, corruption or config, and hook output has no required format. Parsing prose would be a second git-error classifier — the thing D5 refuses to build                                                                                                                   |
| D12 | **A mutation returns the fresh status, and D17's response-scope discipline is what makes that stick**                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         | Letting the next poll discover it. It races — and "one poll in flight" alone does not fix it, because a poll issued _before_ the mutation can land _after_ it                                                                                                                                                                                                                                            |
| D13 | **Refresh is polling, gated on the sidebar's `visible()`** — the same accessor Ports gates its sampling on (`sidebar.tsx:40`) — plus a manual refresh and the D12 post-mutation status                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        | fsnotify on `.git` (a second refresh mechanism), a recursive worktree watch (a second `.gitignore` implementation), OSC 133 command-end (`filesystem` D5 rejected it: an agent is one long command), and window-focus gating — **no such predicate exists in this codebase**; inventing one is new application state with its own owner and tests, and it is not in this epic                            |
| D14 | **What the panel cannot do, it does not draw.** On an SSH tab the mutation controls are **absent**, not disabled                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              | Disabled buttons with a tooltip. `files.reveal` set this precedent: a disabled control advertises a capability the surface does not have                                                                                                                                                                                                                                                                 |
| D15 | **`internal/git` declares its own `Caller` interface** rather than importing `internal/filesystem`'s identical one                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                            | Hoisting a shared `Caller` into a third package, or importing across feature packages. A consumer-declared interface is the Go idiom; the duplication is one method signature                                                                                                                                                                                                                            |
| D16 | **The local/remote seam is one `Repo` interface of named operations**, the same shape the file manager's `Provider` has. Spawning, process groups and pipes are private to `local`; the domain types and the porcelain parser are shared; argv construction is linked by whatever spawns git. **Hardened 2026-08-13: no operation accepts argv, and there is no exception** — not a read-only variant, not a debug build. orca's relay reached the same named-operation conclusion independently and kept one generic `git.exec`; that single exception cost it a 300-line allowlist validator whose own comment records that `--file /etc/passwd --list` leaks file contents | A process-shaped seam (`Run(argv, stdin, out) → exit`). It reads as the narrower abstraction, but it makes the relay **emulate a local process** — group signals, pipes, an exit status — to satisfy a contract written from the local case. `exec` is one channel; a WebSocket to the helper is another (owner, 2026-08-06)                                                                             |
| D17 | **Every response carries the scope it was issued for plus a monotonic status epoch, and is dropped if either is stale.** `{tabId, generation, bindingId, epoch}`; the epoch advances before **every** status-producing request — each mutation and each manual refresh, not only "each refresh cycle"                                                                                                                                                                                                                                                                                                                                                                         | "One request in flight at a time", and a generation that moves only on re-scope. Neither orders two requests issued under one scope: a poll begun _before_ a mutation carries the same generation and can still overwrite the mutation's fresh status afterwards. The Files store owns the scope half of this answer already (rule 2, `files-store.ts:12`); the epoch is the ordering half it needs here |
| D18 | **A mutation is bounded by an interval with both ends named, and mutations run in one lane.** It may only _begin_ on the store's current binding, at most one is in flight, and its _result_ applies only under D17. An in-flight mutation is never cancelled                                                                                                                                                                                                                                                                                                                                                                                                                 | Cancelling a mutation on re-bind — killing `git commit` mid-hook can leave `index.lock` and a half-run hook, a cure worse than the disease. And letting two mutations overlap: their statuses can return out of order and leave the panel showing a repository state that never existed                                                                                                                  |
| D19 | **Stage-all and unstage-all are their own wire operations with named commands, and both refuse while a conflict is unresolved**                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                               | Sending the rendered rows as `paths[]` — under D9's cap that is "stage the prefix we showed". And running either one blind during a merge: both are destructive, measured, not reasoned — see D19 in full                                                                                                                                                                                                |

### D6 in full: what a resolved environment can and cannot promise

Revision 1 said git would run with "what the PTY's login shell would have computed". That
is false twice over, and the correction matters because the whole point of D5 is that the
panel and the terminal agree.

**The PTY does not run a login shell.** `internal/pty/pty_local.go:132` runs
`exec.Command(shell, "-i")` — interactive, not login — starting from
`scrubLauncherSession(os.Environ())` plus `TERM`, `COLORTERM` and configured overrides
(`:134`). The shell then reads its _interactive_ startup files.

**And no cached environment can equal the tab's.** The environment a hook needs may have
been created _inside that tab, after it started_: `direnv`, a Python virtualenv, a Nix
shell, an `export PATH=…` the user typed. A value resolved once at backend start cannot
contain any of it, and AD-6 forbids us learning it from the stream.

So the guarantee is narrowed to what is deliverable, and the gap is a product-visible fact
rather than a footnote:

1. git runs with an environment resolved once from the user's shell **non-interactively
   and bounded** by a deadline and an output cap, cached for the process lifetime.
2. That environment is **named in the product**: the commit surface states that a hook
   runs in nocx's resolved environment, not in the tab's. A hook that passes in the
   terminal and fails here is then an explicable difference rather than a mystery.
3. If resolution fails, git still runs — with `os.Environ()` — and the panel **says so**
   before the first commit, because a hook that silently could not find its tools is the
   exact failure this decision exists to prevent.

Making the panel's commit run in the tab's live environment is a real want and a different
mechanism (the shell would have to report it, `nocx-pu4`'s territory). It is out of scope
and gets a bead rather than a promise.

### D5 in full: why shelling out is the right second implementation

The panel must say the same thing as `git` typed in the tab next to it. A library is by
construction a second implementation of "what does git think", and the divergences are
silent: it agrees about a modified file and disagrees about a sparse checkout, a
submodule, or a `core.excludesFile` the user set five years ago.

The decisive one is hooks. This repository's own pre-commit hook is the quality gate for
every commit in it. `go-git` does not run hooks. A Commit button that silently produced
commits nobody's gate had seen would be green everywhere you look and wrong where you did
not.

**This reasoning goes in the code**, at `internal/git`'s package doc, not only here.

### D16 in full: the seam is the operation, not the process

Every surface that reaches a machine gets a local and a remote implementation behind one
interface (AD-8). The question is at what level, and the first draft of this decision got
it wrong in a way worth recording, because the wrong answer was defended with a real-sounding
argument.

**The rejected answer was a process-shaped seam** — `Runner.Run(ctx, dir, argv, stdin, out)`
returning an exit code — on the reasoning that git's _operations_ do not differ between
machines (same binary, same argv, same `--porcelain=v2 -z`), so only execution needs
swapping. The supporting argument was that a higher seam would force the remote
implementation to parse porcelain a second time, which AD-8 forbids.

**That argument is void, and the owner is right.** The remote implementation is not foreign
code: AD-2 is "one Go codebase, multiple build targets", and the relay is one of those
targets. It links the _same_ `porcelain.Parse`. The parser is shared by construction at any
seam level, so it never was an argument for one.

What remains is the objection against the process shape, and it is decisive: **`exec` is one
channel, the relay is another, and a process-shaped interface makes the second channel
emulate the first.** `argv`, `stdin`, a live `stdout` sink, an exit status, an isolated
process group and an `INT → TERM → KILL` escalation are facts about spawning a child on this
machine. Over a WebSocket to a helper there is no process group to signal and no pipe to
close; the relay would have to reproduce those semantics as protocol obligations in order to
satisfy a contract written from the local case. That is a local implementation detail leaking
into the shared contract — the same defect in the opposite direction.

**So the seam is the operation**, and both surfaces now have the same shape:

| Surface      | Seam                                                                                                                           |
| ------------ | ------------------------------------------------------------------------------------------------------------------------------ |
| File manager | `Provider`: `Root` / `List` / `Read` / `Watch` / `Canonical`                                                                   |
| Git manager  | `RepoFactory`: `Open` · `Repo`: `Status` / `Diff` / `Stage` / `Unstage` / `StageAll` / `UnstageAll` / `Commit` / `HeadMessage` |

What each side then owns:

- **`local`** spawns git. Process groups, the `INT → TERM → KILL` escalation, the early-cut
  protocol, the resolved environment — all of it is private to this package, where it
  belongs, and none of it is a promise the relay has to keep.
- **`relay`** (later) sends operations to the helper over the control channel. Cancellation
  is a message; bounding is enforced on the machine doing the work, which is the only place
  it can be enforced cheaply.
- **Shared, machine-independent**: the domain types, `porcelain.Parse`, and the bounding
  constants. Note which side of the relay `gitargs` falls on: argv is spawning vocabulary,
  so it is linked by whatever **actually spawns git** — `local`, and the copy of `local`
  compiled into the helper build — and **not** by the relay _client_. The client sends named
  operations; if it built argv it would either put process vocabulary on the wire or compute
  an answer nobody uses. The helper is not a third implementation: it is `local`, running on
  the other machine, which is what AD-2's "one core, multiple build targets" buys.
- Every state a caller can observe —
  `Completeness`, `tooLarge`, `binary`, `gone` — lives in the domain types precisely so the
  remote implementation can report them too, rather than being artefacts of how a local pipe
  was cut.

`ctx` is the whole cancellation contract at the seam. Each implementation honours it with
the mechanism its channel actually has.

The remote work is then one implementation of one interface — _given_ a channel, which the
relay does not have yet (§2). Because the seam is operation-shaped, the protocol that has to
be designed is a request/response over a closed set of named operations, which is a great
deal easier to specify, version and test than a generic remote-process pipe would have been.

### D19 in full: what "all" runs, decided by measurement

"Stage all" and "Unstage all" look like the two most trivial buttons in the panel. Both are
destructive in a state the panel explicitly supports, and the evidence is empirical (git
2.55), not reasoned:

| Command                                           | Measured behaviour                                                                                                                                                                        |
| ------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `git add -A` with an unresolved conflict          | The record goes from `u UU …` to `1 M. …` — git **marks the conflict resolved** using the worktree file, which at that moment still contains `<<<<<<<` markers. A commit follows silently |
| `git reset` (bare) during a conflicted merge      | Exit 0, and **`.git/MERGE_HEAD` is gone**: a button labelled "Unstage all" has aborted the merge, leaving the marker-laden file as an ordinary modification                               |
| `git reset` (bare) on an **unborn** branch        | Exit 0, the staged file becomes untracked. It works                                                                                                                                       |
| `git restore --staged <path>` on an unborn branch | `fatal: could not resolve 'HEAD'`                                                                                                                                                         |

So:

- **Stage-all is `git add -A`**, and it is **refused, visibly, while any entry is
  conflicted.** Not disabled-with-a-tooltip when the panel is otherwise live — refused with
  the reason, because the alternative is a button that resolves conflicts by accident.
- **Unstage-all is bare `git reset`** — no `HEAD`, no pathspec. That is what makes it work
  on an unborn branch, where `git restore --staged` and `git reset HEAD -- …` both fail on
  an unresolvable `HEAD`. **No special unborn path is needed**, and none should be built.
- **Unstage-all is refused during a conflicted merge** for the second row above.
- Conflicts remain unstageable individually (§4), so the two refusals are one rule, not a
  special case: while a merge is unresolved, the panel does not touch the index.

## 4. Scope

### In

- A **Git** view in the existing activity bar, ordered after Files and Ports.
- Repository resolution from the active tab's verified cwd, re-asked on every cwd change
  (D2, D4).
- Header: branch name, upstream name, ahead/behind, and a total changed count.
- Two lists — **Staged** and **Unstaged** — one row per file with a type icon, a
  one-letter status and the path.
- **Open a unified diff** of one file in its own read-only tab; sides chosen by the list
  the row was in.
- **Stage / unstage** one file; **stage-all / unstage-all** as their own operations (D19).
- **Commit**: subject, body, and **Amend** prefilled from `HEAD`.
- Polling while the view is visible, a manual refresh, and the post-mutation status.
- Honest states, each visible in the panel and not only in a log: no verified cwd, not a
  repository, git absent, git too old, unborn branch, detached HEAD, too many changes,
  remote tab, resolved-environment degraded.

### Out — each a refusal, not an omission

- **The remote repository.** Waits on the relay, `nocx-if6` phase B (D3). Filed there as a
  child that carries the exec-channel protocol question (§2).
- **discard / restore.** The one action that destroys work with no undo. Its own bead, with
  a confirmation design, and it must handle the three cases the panel would otherwise
  conflate: an untracked file (discard means **delete from disk**), a tracked modification
  (`restore`), and a partially staged file.
- **Branch checkout, branch creation, stash.** Checkout changes the reality underneath an
  agent running in the terminal right now, and checkout with a dirty tree is a
  stash-or-refuse decision. Its own bead.
- **push / pull / fetch / publish / Create PR.** Its own epic; it needs remote
  authentication and a "which action is appropriate now" resolver.
- **Git status markers in the file tree** — `nocx-terg` stays a separate bead and gains a
  dependency edge on this epic. The store is nevertheless designed so a second consumer
  reads the same state rather than issuing its own status.
- **Multi-repository, submodules, worktrees as a list.** nocx has no "project" concept.
  A submodule appears as one row with its own status letter; entering it is out.
- **Hunk- and line-level staging.** Needs a patch editor. Its own bead.
- **Split (side-by-side) diff**, and **syntax highlighting inside the diff.** Both their
  own beads; the `git.diff` contract is shaped so the first can be added by addition.
- **Merge conflicts as a surface.** A file in conflict shows with status `U` and is not
  stageable from the panel.
- **A resizable split between the two lists**, and **view modes (tree / list / combined)**.
- **Running the panel's commit in the tab's live environment** (D6). Its own bead.
- **Window-focus gating of the poll** (D13). No such predicate exists; adding one is
  application state with its own owner.

### Neither in nor out — what this section missed

The two lists above are exhaustive about what was _considered_. They are not exhaustive
about what the reference products do, and the difference cost a round.

The owner opened the delivered panel beside orca's and asked where the commit list was,
and where the per-file line counts were. Both are prominent in the screenshots that
opened this design — orca gives the lower half of its panel to `COMMITS` and puts a green
`+N` and a red `−N` on every row — and **neither appears anywhere above**. Not as a
refusal, which is what this section is for; as an omission.

That is a defect in the method, not only in the list. Every "Out" entry here was written
by asking _what could this deliverable grow into_, and answering from the deliverable.
Nothing asked _what does the thing I was shown do that this does not_, which is the only
question that catches a gap between a design and its own reference. The acceptance
criterion inherited the blind spot: the epic proved its happy path end to end and closed
green while missing half of what the owner had pointed at.

Both are now epics of their own — `nocx-i4ki` (line counts) and `nocx-6b15` (the commit
list) — with the graph, commit selection and commit diffs deliberately out of the second.

## 5. Architecture

### 5.1 Backend — `internal/git`

```
internal/git/
  git.go          package doc (D5, D16), domain types, Repo + Caller interfaces
  binding.go      Binding, Handle, Registry, Acquire      (mirrors filesystem/binding.go)
  bounds.go       MaxStatusEntries and the work ceilings, shared as policy
  spawn/          linked ONLY by code that runs git — local here, and local inside the
    porcelain.go    helper build. NOT by the relay client, which sends named operations:
    gitargs.go      a client that built argv or parsed porcelain would either put process
                    vocabulary on the wire or compute an answer nobody uses
  errors.go       the errors transport switches on
  local/
    local.go      Repo over a spawned git: argv, process group, INT→TERM→KILL, early cut
    shellenv.go   the resolved environment and its degraded state (D6)
    diff.go       the three diff invocations and their bounded result
    commit.go     commit orchestration
    capability.go git presence + version probe, cached
  relay/          (deferred, nocx-if6 phase B) Repo over the relay's operation channel
```

The split is D16's: `git/` is what both machines share, `git/local/` is everything true only
about spawning a child here. `commit.go` and `diff.go` sit under `local/` because they
_orchestrate invocations_; what they produce — `CommitOutcome`, `Diff` and their states —
is declared in `git.go`, so the relay implementation returns the same values without
inheriting the local mechanics.

#### The `Repo` seam (D16)

```go
// Repo is one repository on one machine. It is the whole local/remote seam:
// local spawns git here, relay sends operations to the helper there, and
// nothing above this interface knows which. ctx is the entire cancellation
// contract — each implementation honours it with the mechanism its channel
// has, and neither imposes the other's.
type Repo interface {
    Status(ctx context.Context) (Status, error)
    Diff(ctx context.Context, path string, side Side, maxBytes int64) (Diff, error)
    Stage(ctx context.Context, paths []string) (Status, error)
    Unstage(ctx context.Context, paths []string) (Status, error)
    StageAll(ctx context.Context) (Status, error)
    UnstageAll(ctx context.Context) (Status, error)
    Commit(ctx context.Context, msg string, amend bool) (CommitOutcome, error)
    HeadMessage(ctx context.Context) (HeadMessage, error)
    Close() error
}

// RepoFactory opens one. Resolution — is git here, is it new enough, and is
// this directory inside a repository — happens BEFORE a Repo can exist, so it
// cannot be a Repo method; and it must not be done with os/exec in the
// composition layer, which would put local mechanics back above the seam with
// no remote counterpart. So it is its own seam, with the same two
// implementations.
//
// local: probe capability, run rev-parse, build a Repo on the answer.
// relay: send one open operation to the helper and build a client Repo on its
// answer. One round trip, not three.
type RepoFactory interface {
    Open(ctx context.Context, cwd string) (Repo, OpenOutcome, error)
}
```

`OpenOutcome` carries the state table below plus the resolved `toplevel`, `gitDir`, the git
version and the environment state (D6).

**The factory is capability's only owner.** Revision 6 also left a `Capability` method on
`Repo`, which gave one fact two owners that could disagree — and it was on the wrong side
besides: git's presence, its version and the environment it runs in are all determined
_before_ a repository exists, nothing after `git.open` asks for them, and the panel already
receives them in the open result. The method is gone.

**Ownership of the returned `Repo` transfers to the registry, and only on success.** Three
rules, because between `Open` returning and `Register` succeeding there is an interval where
a live `Repo` belongs to nobody — the same leak class as the stale open in §5.4, one layer
earlier:

1. `Repo` is non-nil **iff** the outcome is `ok`. Go cannot encode that in a three-value
   return, so the composition layer **checks both directions explicitly** and both checks
   are tested: `(nil, ok, nil)` is an internal error, and a non-nil `Repo` on any refusing
   outcome is closed before the refusal is returned.
2. If `Register` fails, the composition layer calls `Repo.Close`, surfaces any close error
   alongside the registration error, and returns no binding.
3. After `Register` succeeds the registry owns it, and `Close` is reached only through the
   binding.

Every value these interfaces return — `Completeness`, each diff state, the commit outcome
and its truncation mark — is declared in `git.go`, machine-independently, **so that the
relay can report it too.** The test each one has to pass: _does it describe what the user is
being shown, or how a local pipe was cut?_ `Cut` failed that test and was removed from the
domain in revision 6 — it is the fact that `local` killed a child, which the relay could
only reproduce by pretending to have processes. It stays private to `local`, which maps it
to `tooLarge` or to an incomplete `Completeness` before returning through `Repo`.

#### Inside `local`: what spawning a child costs, and why it stays here

None of the following is part of the seam. It is what `local` must do, and confining it here
is the point of D16.

- **argv, never a shell**, built by the shared `gitargs` so both implementations ask git the
  same question.
- **stdout is streamed, not buffered** (D9), with the sink able to say `ErrEnough` — "I have
  all I need" — after which the execution is cancelled, reaped, reported as `Cut`, and the
  resulting broken pipe is **not** a failure.
- **Cancellation is what ADR-0020 already decided it means in this repo**: the child runs in
  its **own process group** and cancellation escalates `INT → TERM → KILL` against the
  group. `git diff` can spawn descendants — a configured textconv filter, an external diff
  driver — and killing only the direct child would leave them holding the inherited pipe
  open, so the read never sees EOF. Then reap the direct child and close both pipes.
- **stderr is bounded and says so.** The result carries a truncation mark, because D11
  promises the user git's own account of a failure and a silently clipped account is a worse
  lie than one that admits it. The bound is applied by discarding, never by returning an
  error from the stderr writer — that would stop the reader while the child is still writing
  and deadlock it.
- **A non-zero exit is data, not an error.** git says ordinary things with exit status: `1`
  from `diff --no-index` means "there are differences". An error is reserved for "the
  invocation could not be made or completed".

There are **two bounding policies, because there are two different questions**:

- **Status drains to the end — up to a work ceiling.** The parser retains
  `MaxStatusEntries` records and keeps counting the rest, which is the only way `Total` can
  be exact; past the retention point that is a NUL scan and costs nothing. But the scan is
  not the cost that matters: `--untracked-files=all` makes **git** traverse the filesystem
  and format a record for every untracked file, so a generated tree with millions of them
  would hold a subprocess, a handler and the polling slot for as long as the traversal
  takes. Draining "to the end" is therefore bounded by a wall-clock and byte ceiling; when
  it is reached the status sink returns `ErrEnough` exactly as the diff sink does, and the
  domain result is `Completeness: cut` and `Total` is a **lower bound**.

  Revision 3 left that state unreachable — the type admitted a cut status and the tests
  demanded one while the stated policy never produced it.

- **Diff terminates deliberately.** A diff has no total worth counting, and
  `git diff --no-index /dev/null <huge-file>` would otherwise stream gigabytes we throw
  away — memory-bounded but not work-bounded, holding a subprocess and a handler open for
  as long as the file is long. At `maxBytes` the sink says `ErrEnough`, `local` kills
  and reaps the child. `local`'s private execution result is marked cut; what crosses `Repo`
  is the domain state `tooLarge`, and nothing else. The broken pipe that
  follows is expected, not a failure.

**Stderr is bounded and says so.** `StderrTruncated` exists because D11 promises the user
git's own account of a failure, and a silently clipped account is a worse lie than a
clipped one that admits it. The bound is applied by discarding, never by returning an error
from the stderr writer — that would stop the reader while the child is still writing and
deadlock it.

`local` has three further properties that are load-bearing and each have a test:

1. **argv, never a shell.** No path or message is ever interpolated into a command string.
2. **The resolved environment and its honest degrade** (D6).
3. **Bounded stderr and a bounded retained stdout**, with the bound reported rather than
   hidden.

#### Repository resolution

`git.open` receives `{sessionId, cwd}`. The composition layer:

1. Checks `caller.Owns(sessionId)` — the authorisation choke point, satisfied by
   `connState.Owns` (`internal/transport/ws.go:753`).
2. Refuses immediately if the session is an SSH session (D3, D14) — before anything is
   spawned or sent.
3. Calls `RepoFactory.Open(ctx, cwd)`.
4. On `ok`, `Registry.Register(sessionID, repo)` mints the binding.

**The composition layer runs no git of its own**, and that is the point of the factory
seam. Probing capability and running `rev-parse` are the two things that must happen before
a `Repo` exists, so revision 5 left them in the composition layer — where they could only be
`os/exec`, which is local mechanics back above the seam with no remote counterpart. They now
live inside the factory's implementations: `local` probes and runs `rev-parse` here; the
relay sends **one** open operation and lets the helper do both there, which is also one
round trip instead of three.

Every outcome other than (4) is a **state in the result**, not a JSON-RPC error:

| State               | Cause                                                    |
| ------------------- | -------------------------------------------------------- |
| `ok`                | a repository was resolved                                |
| `notARepository`    | `rev-parse` said no                                      |
| `noCwd`             | the caller had no verified cwd to offer                  |
| `remoteUnsupported` | the session is an SSH session (D3)                       |
| `gitUnavailable`    | no `git` on PATH                                         |
| `gitTooOld`         | below the floor; the result carries the version it found |

`rev-parse`'s output is validated, not trusted: **exactly two lines**, each absolute and
non-empty — git prints one line per requested value, so `--show-toplevel --absolute-git-dir`
answers the worktree root and then the git directory. Anything else is `notARepository`,
never a path we hand to a subprocess. (Revision 2 said "exactly one line", which would have
rejected every repository in existence. Verified: `git rev-parse --show-toplevel
--absolute-git-dir` in an ordinary repository prints two lines.)

Both values are load-bearing, and D4's correctness rests on them:

| Where the cwd is        | `--show-toplevel`        | `--absolute-git-dir`            |
| ----------------------- | ------------------------ | ------------------------------- |
| an ordinary repository  | the worktree root        | `<root>/.git`                   |
| a **nested** repository | the **nested** root      | the nested repository's git dir |
| a checked-out submodule | the **submodule's** root | the submodule's git dir         |
| a linked worktree       | the **linked** worktree  | `<main>/.git/worktrees/<name>`  |

All four verified empirically on git 2.55. The binding's identity is the **pair**: two
linked worktrees of one repository share history but are different working trees, and a
panel that treated them as one would stage in the wrong one.

**The version floor is 2.25** (January 2020), set by `--pathspec-from-file`
/`--pathspec-file-nul`; `--porcelain=v2` needs only 2.11. One constant, whose comment names
which flag bought it.

#### `Handle`

```go
type Handle interface {
    Status(ctx context.Context) (Status, error)
    Diff(ctx context.Context, path string, side Side, maxBytes int64) (Diff, error)
    Stage(ctx context.Context, paths []string) (Status, error)
    Unstage(ctx context.Context, paths []string) (Status, error)
    StageAll(ctx context.Context) (Status, error)     // D19
    UnstageAll(ctx context.Context) (Status, error)   // D19
    Commit(ctx context.Context, msg string, amend bool) (CommitOutcome, error)
    HeadMessage(ctx context.Context) (HeadMessage, error)
}
```

`Registry.Acquire` returns the handle plus a release, holds the use-guard for the call's
duration, and re-checks ownership on every acquisition.

**The binding interval, stated with both ends** (D18, and the lesson `internal/vault`
bought): _a binding is reachable from the moment `Register` returns until `Close` returns;
`Close` waits for every acquired handle to drain, so no provider call is in flight after
`Close` returns._ The consequence, named rather than left implicit: **a mutation that has
already begun completes against the repository it was authorised against, even if the
shell has since moved.** That is correct — the user pressed Commit on that repository's
staged set — and D17 is what stops its result painting the repository they moved to.

#### Domain types

```go
type Status struct {
    Branch     string // "" when detached
    Detached   bool
    Unborn     bool
    Head       string // short hash; "" when unborn
    Upstream   string // "" when the branch has none
    Ahead      int
    Behind     int
    Staged     []Entry
    Unstaged   []Entry
    Conflicted []Entry
    Total        int          // records counted (see Completeness)
    Completeness Completeness // ONE discriminator, switched on first
}
```

`Staged`, `Unstaged` and `Conflicted` are never nil — an empty set marshals as `[]`, not
`null`. That exact bug was found by the first contract schema this repository ever ran.

**`Completeness` is one discriminator, not two booleans**, and the reason is a state two
booleans got wrong:

| Value      | Meaning                                                         | `Total`     |
| ---------- | --------------------------------------------------------------- | ----------- |
| `complete` | every record was observed and every one is in the lists         | exact       |
| `capped`   | every record was observed; more existed than `MaxStatusEntries` | exact       |
| `cut`      | the traversal was stopped at the work ceiling before its end    | lower bound |

Revision 5 had `Truncated` ("more records than the lists hold") and `TotalExact` as separate
flags, and the panel gated its incomplete-status state on `Truncated` alone. A traversal that
hit the **wall-clock** ceiling after 100 records — below the cap — would then set
`TotalExact: false` and leave `Truncated: false`, and the panel would render an apparently
complete 100-file status for a repository that might hold millions. One discriminator, and
the panel switches on it first (the `filesystem` D14 habit).

#### `porcelain.go`

One `git status --porcelain=v2 -z --branch --untracked-files=all` produces everything (D7),
and the parser is **streaming**: it consumes every record, retains the first
`MaxStatusEntries`, and counts the rest (D9). Counting is a NUL scan and costs nothing;
this is what lets `Total` be exact while the lists stay bounded.

Three properties, each a real repository's real output and each with a captured-bytes test:

- **`-z` is not decoration.** Records are NUL-terminated because a path may contain a
  newline. A line-oriented parser is correct until somebody checks in such a file.
- **Rename records carry two paths in one record**, separated by a NUL of their own.
  Getting this wrong shifts every subsequent record by one field.
- **A file can be in both lists** — `XY` with both columns non-`.`. The panel's row key is
  therefore `{side, path}`, not `path`.

Plus the headers: `# branch.head` (the literal `(detached)` when detached),
`# branch.upstream`, `# branch.ab +N -M`, and the **absence** of the latter two, which is
what "no upstream" looks like — never a zero.

#### `commit.go`

1. **Refuse early if nothing is staged** — `nothingToCommit`, before running a hook that
   would then fail confusingly.
2. **`git commit -F - [--amend]`**, message on stdin (D8).
3. **Interpret the exit.** Zero → `ok`, with the new head. Non-zero → `failed`, carrying
   git's stdout **and** stderr as far as the bound allows, with `outputTruncated` set when
   it was reached, and the panel rendering that mark. We do not guess whether a hook, a
   signing key or an index lock produced it; git's own output is the only accurate account.

**There is no identity preflight.** Revision 1 ran `git config user.email`/`user.name`
first; that is a second implementation of "can git identify the author", and git's real
answer also involves environment variables, conditional includes and `user.useConfigOnly`.
It could refuse a commit git would accept. Missing identity is an ordinary `failed` whose
output happens to be git's four-paragraph explanation — and the panel may _recognise_ that
output to add one clarifying line, never to override git's verdict.

`--amend` on an unborn branch is refused before invocation. **There is no `--no-verify`**
in this design and no setting that adds one.

#### `diff.go`

| Row is in       | Invocation                                           |
| --------------- | ---------------------------------------------------- |
| Staged          | `git diff --cached --no-color -- <path>`             |
| Unstaged        | `git diff --no-color -- <path>`                      |
| Untracked (`?`) | `git diff --no-index --no-color -- /dev/null <path>` |

The untracked row is the interesting one: an untracked file has nothing to diff against,
and `--no-index` against `/dev/null` is git's own answer, producing a real all-additions
diff. It exits **1** when there are differences — which is why `local` treats a non-zero
exit as data.

| State      | Meaning                                                               |
| ---------- | --------------------------------------------------------------------- |
| `ok`       | unified diff text, possibly `truncated`                               |
| `binary`   | git said `Binary files differ`; there is nothing to render            |
| `tooLarge` | the byte bound was reached; the retained text is a prefix and says so |
| `empty`    | no differences — the file changed back, or the poll raced the click   |
| `gone`     | the path no longer exists in that side                                |

`empty` and `gone` exist because the panel is polling: a row can be clicked in the same
second an agent reverts the file.

### 5.2 Wire — control plane, JSON-RPC (AD-1)

Ten methods and one notification, in a new `internal/transport/ws_git.go` beside
`ws_files.go`.

| Method            | Params                                 | Result                                                            |
| ----------------- | -------------------------------------- | ----------------------------------------------------------------- |
| `git.open`        | `{sessionId, cwd?}`                    | `{state, bindingId?, toplevel?, gitVersion?, envState?, status?}` |
| `git.status`      | `{bindingId}`                          | `{status}`                                                        |
| `git.diff`        | `{bindingId, path, side, maxBytes}`    | `{state, text, truncated}`                                        |
| `git.stage`       | `{bindingId, paths[]}`                 | `{status}`                                                        |
| `git.unstage`     | `{bindingId, paths[]}`                 | `{status}`                                                        |
| `git.stageAll`    | `{bindingId}`                          | `{status}`                                                        |
| `git.unstageAll`  | `{bindingId}`                          | `{status}`                                                        |
| `git.commit`      | `{bindingId, message, amend}`          | `{state, head?, output?, outputTruncated, status}`                |
| `git.headMessage` | `{bindingId}`                          | `{state, message}` — the Amend prefill                            |
| `git.close`       | `{bindingId}`                          | `{closed}`                                                        |
| `git.changed`     | _(notification)_ `{bindingId, reason}` | the binding is gone                                               |

Notes that are decisions, not descriptions:

- **`git.open` returns the first `status` inline** — otherwise every open is two round
  trips and one guaranteed frame of empty lists.
- **`git.open` reports `envState`** (D6) so the panel can say, before the first commit,
  that hooks will run in a degraded environment.
- **`paths[]` never means "all".** An empty array is a no-op; "all" is `git.stageAll`
  (D19). An overloaded empty list is how "stage everything" quietly becomes "stage the
  rows we rendered".
- **`side` is an enum** — `staged` | `unstaged` | `untracked` — because the sides of a diff
  are a closed set and a schema that says `string` is theatre.
- **`git.changed` follows `files.changed` exactly**: it announces that a binding is gone
  and **its destination is resolved at emit time** — the binding records its `sessionId`,
  and the backend writes to that session's _current_ subscriber. That is what survives an
  AD-9 reconnect **for a live session**, and it is why the notification's `reason` never
  says "connection lost": a lost connection is not something you can tell the connection
  that was lost. **There is exactly one reason, `sessionClosed`**, and it has exactly one
  producer: the session teardown path that already closes a session's file bindings
  (`ws_files.go:915`). Revision
  2 also listed `repositoryGone`, with no producer, no closing transition and no answer to
  "may a call still acquire the binding after the notification is written" — an
  unimplementable state is worse than an absent one. A repository that disappears under a
  live binding is discovered the ordinary way instead: the next `git.status` fails, and the
  store re-resolves through `git.open`, which answers `notARepository`.
- **The notification's interval, both ends:** the binding is removed from the registry
  **before** the notification is written, so no call can acquire it after a client has been
  told it is gone; `Close` then drains whatever was already in flight (D18). An in-flight
  call that loses the race gets `unknownBinding`, which is the correct answer — the store
  re-resolves on it.
- **Emit-time lookup does not work on the teardown path, and this is why `nocx-lzfb`
  exists.** Both teardown paths remove the session's receiver _before_ they clean up
  bindings: `monitorExit` calls `removeRx` at `ws.go:1701` and `filesSessionClosed` only at
  `:1711`, and the explicit-close path does the same at `:1745`/`:1747`. A notifier that
  asks "who is subscribed to this session" at that moment finds nobody, which is precisely
  why closing a terminal destroys its file bindings silently today. `monitorExit` already
  knows the answer: it **captures** the subscriber at `ws.go:1696`, before removal, and
  uses that capture for its own `exit` notification at `:1717`.

  So the git teardown takes the captured subscriber as a parameter rather than looking one
  up, and the explicit-close path — which captures nothing today — captures it the same way
  before `removeRx`. **Emit-time resolution remains the rule for a live session** (that is
  what survives an AD-9 reconnect); the captured subscriber is the rule for the one moment
  when the session is being torn down and there is nothing left to look up. The same fix is
  what `nocx-lzfb` needs, and the bead is updated to say so.

- **Bindings survive a WebSocket reconnect**, because they are bounded by the session
  (§2, AD-9). The store re-uses its `bindingId` after re-attach and only re-opens when a
  call answers `unknownBinding`.

Every method is authorised the same way `files.*` is.

### 5.3 Contracts

One schema per shape, `additionalProperties: false` plus an explicit `required`, generated
TS committed, and both Go checks.

```
contracts/git.open.schema.json        contracts/git.stageAll.schema.json
contracts/git.status.schema.json      contracts/git.unstageAll.schema.json
contracts/git.diff.schema.json        contracts/git.commit.schema.json
contracts/git.stage.schema.json       contracts/git.headMessage.schema.json
contracts/git.unstage.schema.json     contracts/git.close.schema.json
contracts/git.changed.schema.json
```

`status` appears in seven results — `open`, `status`, `stage`, `unstage`, `stageAll`,
`unstageAll`, `commit`. It is declared **once** and referenced with `$ref`.

**`git.changed` is a notification, not a result**, and its schema covers the **`params`
object only** — the shape `files.changed.schema.json` already established. Its
over-the-wire test drives a real server emission and asserts the addressing (a binding
whose session has a subscriber reaches that subscriber), rather than validating a payload
the test built.

The `git.diff` result carries `state` + `text`; a later split view adds `oldText`/`newText`
as new **optional** fields, so the schema grows by addition.

### 5.4 Frontend — `frontend/src/git/`

```
frontend/src/git/
  git-client.ts     one method per wire call; every result a GENERATED type
  git-store.ts      binding lifecycle, response-scope discipline (D17), poll controller,
                    mutation gate (D18), commit form
  git-view.tsx      createGitView(deps): SidebarViewDescriptor
  git-panel.tsx     the panel body
  git-diff/
    open-git-diff.ts       openGitDiff(target) + surface registration
    git-diff-content.tsx   the diff surface
```

#### The store's state machine

The input is the same reactive `activeOrigin()` accessor the Files panel takes;
`ActiveOrigin` already carries `sessionId`, `kind`, `cwd`, `cwdVerified` and `cwdFollow`
(`frontend/src/tab-content.ts:40`).

| State            | Reached when                         | What the panel shows                                                                     |
| ---------------- | ------------------------------------ | ---------------------------------------------------------------------------------------- |
| `noTab`          | `activeOrigin()` is null             | empty state, no controls                                                                 |
| `remote`         | `origin.kind === 'ssh'`              | "Git on a remote host isn't supported yet" — and **no** mutation controls                |
| `noCwd`          | `cwdVerified` is false               | what is missing and why: no shell integration on this session                            |
| `notARepository` | `git.open` said so                   | the directory, and that it is not a repository                                           |
| `gitUnavailable` | no git on PATH                       | how to install it                                                                        |
| `gitTooOld`      | below the floor                      | the version found and the version needed                                                 |
| `ready`          | a binding and a status               | the panel                                                                                |
| `tooManyChanges` | `status.completeness !== 'complete'` | the count: "N changes, showing the first M" when `capped`, "more than N" when `cut` (D9) |

`cwdFollow` is honoured as the Files panel honours it: a diff tab's frozen origin says no,
so activating a diff tab never re-binds the panel to something stale.

#### Response-scope discipline (D17)

Every request captures `{tabId, generation, bindingId, epoch}`. A response applies only if
the first three still match the store's current state **and** its `epoch` is not older than
the newest already applied. `generation` bumps on every re-scope; **`epoch` bumps before
every status-producing request** — each poll, each manual refresh and each mutation.

The scope half is `files-store.ts` rule 2, reused rather than re-derived. The epoch is the
half this panel additionally needs, and the reason is precise: a poll and a mutation issued
under one unchanged scope would otherwise carry identical guards, so a poll begun _before_
the mutation could land _after_ it and repaint the pre-mutation state — the very race D12
claims to close. A guard that cannot distinguish two requests cannot order them.

Together they make four otherwise-real races impossible:

- Two `git.open`s racing on a fast A→B tab switch and landing out of order.
- A poll issued _before_ a mutation landing _after_ it (the epoch, not the scope).
- Two mutations overlapping — prevented upstream by D18's single lane, and caught by the
  epoch if the lane is ever widened.
- A `git.diff` for the row that was clicked before the panel re-bound.

**A stale `git.open` is closed, not merely dropped.** A successful open has already
registered a live binding on the backend; discarding its response would leak it — which is
`nocx-myts` in the Files panel, filed and open. So the rule has two halves: if the response
is stale, the store calls `git.close` on the `bindingId` it just received and never stores
it. `files-store.ts:325` distinguishes an open from an ordinary scoped call for exactly
this reason.

**The Amend prefill carries its own token.** `git.headMessage` is not made stale by
anything in the D17 triple: tick Amend, untick it, and the in-flight reply still matches
scope and epoch and would fill a form the user has abandoned — and tick-untick-tick lets
the first reply satisfy the second request's intent. Each prefill request therefore carries
an amend token that the checkbox bumps on every transition, and a reply whose token is not
current is discarded.

#### The mutation gate (D18)

A mutation is issued only against `store.binding()` as it is at the moment of the click; a
re-bind between the click and the send cancels the send. **At most one mutation is in
flight**: while one is running the controls that issue another are disabled, so two fast
clicks cannot produce two overlapping index writes whose statuses return out of order. Once
sent, a mutation is never cancelled, and its result is subject to D17 like any other.

#### Polling (D13)

Runs only while the sidebar's `visible()` is true and the store is `ready`.

- Interval: **5 s**, matching `POLL_INTERVAL_MS` in `ports.tsx:94`. A second cadence in the
  same sidebar would be a second answer to "how often may a panel poll"; if git status
  proves cheap enough to justify a faster tick, that is a change to a shared constant with
  a measurement behind it, not a private number.
- One poll in flight at a time, never queued; a mutation in flight suppresses the next.
- A poll that errors does not clear the lists. It marks the status stale and leaves the
  last good one on screen.
- Manual refresh is a header `IconButton`, matching Files and Ports.

#### The panel body, and the two kit gaps this epic must close

Read the kit first (`frontend/src/ui/README.md`). Most of the surface is already there:

| Element                          | Kit component                                            |
| -------------------------------- | -------------------------------------------------------- |
| Branch / upstream / ahead-behind | `StatusCard` or a `Toolbar` row + `Badge`                |
| Filter box                       | `SearchField`                                            |
| Section headers with counts      | `Section`                                                |
| Stage / unstage affordances      | `IconButton`                                             |
| Commit subject / body            | `TextField`, `TextField multiline` (`text-field.tsx:20`) |
| Amend                            | `Checkbox`                                               |
| Commit                           | `Button`                                                 |
| Empty and error states           | `EmptyState`, `StatusCard`                               |
| Transient failures               | `showToast`                                              |

**The row: `TreeRow` is wrong, `CollectionRow` is the container, and the status vocabulary
is the genuinely new part.** Revision 1 claimed `TreeRow` already had the slots; it does
not. `TreeRowProps` takes a fixed filesystem `TreeRowKind`, decides the type glyph itself —
`kindGlyph`'s comment says in as many words that "a surface never supplies a glyph"
(`tree-row.tsx:88`) — offers one generic trailing `badge`, and renders `role="treeitem"`
with disclosure semantics the Git lists do not have.

The kit's rule is 90% fit → extend, so the candidates were checked rather than assumed:

| Candidate           | Verdict                                                                                                                                                            |
| ------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `TreeRow`           | No: filesystem `kind` vocabulary, kit-decided glyph, `role="treeitem"` + disclosure                                                                                |
| `EditableRowList`   | No: it owns add/remove controls (`row-list.tsx:26`) — a different surface contract                                                                                 |
| `MarkerList`        | No: fixed semantic tones and glyphs (`marker-list.tsx:21`)                                                                                                         |
| **`CollectionRow`** | **Yes, as the container.** A flat `role="listitem"` with caller-supplied `info` and `actions` (`collection-view.tsx:38`) — no filesystem vocabulary, no disclosure |

So the row is **built on `CollectionRow`, not beside it** — but `CollectionRow` cannot host
it as it stands, and that gap is work this epic owns rather than a wrapper's problem. It
takes only `{info, actions}`, hardcodes `tabIndex={-1}` and `role="listitem"`
(`collection-view.tsx:38`), exposes no activation, no keyboard handler and no disabled
state, and its CSS fixes manager-page density — `gap: var(--space-3)`,
`padding: var(--space-2) var(--space-3)` (`collection-view.css:37`) — with no dense variant.
A git row must be activatable (a click opens the diff) and must be dense enough for a
sidebar. Wrapping cannot supply either: adding an interactive element around a
`tabIndex={-1}` row nests two interactive rows, and tightening the spacing from outside is
the repaint the kit forbids.

So the work is two steps, in the kit, in this order:

1. **Extend `CollectionRow`** with typed activation (`onActivate` plus the keyboard and
   focus semantics that go with it) and a density variant as a typed `data-*`. Connections
   and Credentials keep their current behaviour by default.
2. **Add `FileStatusRow`**, composing the extended `CollectionRow` and owning the one thing
   that is genuinely new: the status vocabulary. `M`/`A`/`D`/`R`/`C`/`U`/`?` and their
   tones are one concept with one owner, and a surface picking its own colours is exactly
   the repaint the kit forbids.

Both get a CSS file, an identity class, tests and a README row.

**There is no reusable read-only CodeMirror host either**, and extracting one needs a
stated contract or it will drift. `FileViewerContent` (`file-viewer-content.tsx:144`) is a
concrete `BaseTabContent` subclass that builds its own `EditorView`, and its single
extension array mixes generic invariants with viewer-specific behaviour: read-only,
highlighting, language selection, theme (`:181`). "Extract a host and use it for both" is
not yet implementable, so the contract is named here and is what step 7 of §6 delivers:

| Concern                                                      | Owner after extraction                                                                                                                                                                                                                                 |
| ------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `EditorView` construction, read-only enforcement, base theme | the **host** — one place, not two                                                                                                                                                                                                                      |
| `focus()`                                                    | the **host**, exposed as a method. `FileViewerContent.focus()` delegates to `EditorView.focus()` today (`:218`); after extraction the viewer must reach it through the host, or it reaches past the abstraction to the view and dual ownership is back |
| `dispose()`                                                  | the **host**, exposed as a method. The viewer destroys the editor on its ordinary `dispose()` (`:222`), independently of whether the abort signal fires — so signal-driven teardown alone is not enough                                                |
| Caller extensions                                            | the **caller**, appended after the host's; the host never inspects them                                                                                                                                                                                |
| Document replacement                                         | the **host**, as one `setDoc(text)` — both surfaces are snapshot-plus-offer (D7)                                                                                                                                                                       |
| Disposal (`EditorView.destroy` on abort or dispose)          | the **host**, driven by the `AbortSignal` it is given                                                                                                                                                                                                  |
| Language selection                                           | the **viewer** — the diff has none in this slice                                                                                                                                                                                                       |
| Diff `+`/`-`/hunk decoration                                 | the **diff surface**                                                                                                                                                                                                                                   |
| Per-surface notices                                          | each surface; the host renders no chrome                                                                                                                                                                                                               |

The viewer's existing tests prove the extraction did not regress the viewer. They do not
prove the host has one owner — that is what the contract above is for, and the host gets
its own test for read-only enforcement and disposal.

A surface may **place** kit components and may never **repaint** them.

#### The diff tab

Registered like the file viewer, as its own surface, with

```
singletonKey = `git:${toplevel}:${side}:${path}`
```

so clicking the same row twice focuses one tab, while the staged and unstaged diffs of one
file are legitimately two tabs.

The content decorates the unified text: `+` lines, `-` lines, hunk headers. **No syntax
highlighting inside the diff in this slice** — named in Out, with its own bead.

The tab is a **snapshot plus an offer** (`filesystem` D7): a diff whose underlying status
changed says so and offers Reload; it never re-reads underneath somebody reading it.

#### The commit form

- Lives in the store, per binding. Switching views and back keeps it; switching repository
  does not carry it across.
- **Not persisted, and `.git/COMMIT_EDITMSG` is not written** — that file belongs to git,
  and to a `git commit` the user may be running in the terminal at that moment.
- Amend prefills from `git.headMessage` once, when the box is ticked, and only into an
  empty form — never over text the user has typed.
- Commit is disabled when nothing is staged or the subject is empty. On `failed` the
  message **stays** and git's output appears in the panel (D11).

### 5.5 Lifecycle

| Event                                   | Behaviour                                                                                                                                                                     |
| --------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Tab switch                              | re-resolve; close the old binding, open the new; both guarded by D17                                                                                                          |
| Any cwd change                          | re-ask `rev-parse` (D4); same toplevel → keep the binding; different → close and open                                                                                         |
| `cd` out of a repository                | close, state `notARepository`                                                                                                                                                 |
| Session closes                          | the backend removes the bindings from the registry, emits `git.changed{reason:'sessionClosed'}` to that session's current subscriber, then drains; the store drops to `noTab` |
| Repository deleted under a live binding | no notification: the next `git.status` fails, the store re-resolves, `git.open` answers `notARepository`                                                                      |
| **WebSocket reconnect**                 | **nothing.** Bindings are session-bounded (AD-9, §2). After re-attach the store keeps using its `bindingId`; it re-opens only if a call answers `unknownBinding`              |
| Sidebar collapsed                       | polling stops; the binding stays                                                                                                                                              |
| Mutation in flight on close             | `Close` waits for it to drain (D18); its result is dropped by D17 if the scope has moved                                                                                      |
| App quit                                | nothing to persist                                                                                                                                                            |

## 6. Sequence

1. **`internal/git` core, one dependency-ordered step**: the `Repo` and `RepoFactory`
   interfaces, the domain types, `spawn/porcelain.go` streaming with its captured-bytes
   tests and `spawn/gitargs.go`, and `local`'s execution mechanics — argv, streamed stdout,
   process-group cancellation — plus **the resolved environment (D6) with its degraded
   state**. No wire, no UI.

   The environment is not a later step, and revision 5 had it as one: the open outcome must
   report whether the environment is resolved or degraded, so a step that shipped before the
   resolver could not deliver what it declared. D6's paired tests — the failure paths **and**
   "on an ordinary machine it succeeds" — belong here with it.

2. **`RepoFactory.Open`** end to end, and it is where capability lives: git presence, the
   version floor, the environment state, `rev-parse` with its two-line validation, the
   outcome table, and the ownership-transfer rule with its leak test.
3. **Binding, Registry, Acquire, `Handle.Status`** — the read half, with the interval test.
4. **Wire read half**: `git.open`, `git.status`, `git.close`, `git.changed`, their schemas,
   generated types, both contract tests, and the notification's addressing test — which
   includes the teardown-order fix in §5.2 (capture the subscriber before `removeRx` on
   both paths). Without it the notification is undeliverable, which is the state
   `files.changed` is in today.
5. **The kit's row, in two steps** — extend `CollectionRow` with typed activation and a
   density variant (Connections and Credentials unchanged), then add `FileStatusRow` on
   top of it owning the status glyph-and-tone table. CSS, identity classes, tests, README
   rows.
6. **Panel, read-only**: view descriptor, store with D17, all eight states, polling.
7. **Extract the read-only CodeMirror host** from `FileViewerContent`, to the ownership
   contract in §5.4; the viewer keeps working on it. The viewer's existing tests prove no
   regression; the host's own tests prove read-only enforcement and disposal.
8. **Diff**: `git.diff`, the diff surface on the extracted host, the three sides.
9. **Mutations**: `git.stage`, `git.unstage`, `git.stageAll`, `git.unstageAll`, the
   mutation gate.
10. **Commit**: `commit -F -`, amend, the `failed` state with git's output, and the form.
11. **The epic's happy path e2e** (§7) — written when the epic is created, run here.

Steps 1–2, 3–4 and 5 are the natural parallel front; 6 onward is one chain.

## 7. Testing

**The epic's happy path (rule 2).** One automated check watches a user do what they could
not do before, headless against `cmd/devharness`:

> In a temporary git repository, edit a tracked file → the panel shows it under Unstaged →
> click the row → a diff tab opens showing the change → stage it → it moves to Staged →
> type a subject → Commit → both lists are empty, and the header reflects the new head.

**Plus the in-scope actions a store test cannot reach (rule 1).** The connection manager
shipped with 1041 green tests and no way to create a group; a store test cannot prove a
control is rendered, enabled from the state a user starts in, and wired. So the e2e set
also covers:

- **Amend**: the checkbox is present and enabled with a commit on `HEAD`, ticking it fills
  the form from `HEAD`, and committing produces one commit rather than two.
- **Stage-all and unstage-all**: operable from the panel, correct on a repository whose
  status is capped, and **refused with a reason while a conflict is unresolved** — the two
  measured hazards in D19, each with a test that starts from a real conflicted merge and
  asserts that `MERGE_HEAD` still exists afterwards and the conflicted record is still `u`.
- **Unstage-all on an unborn branch** succeeds — the case that dictated bare `git reset`.
- **A failing commit is visible**: with a hook that exits non-zero, the panel shows git's
  output and the typed message is still in the form afterwards.
- **The remote refusal**: on an SSH tab the mutation controls are absent from the DOM, not
  merely disabled.

**Failure paths (rule 3) — one per external call, no representatives.** Every invocation
this design makes: version probe, `rev-parse`, `status`, `stage`, `unstage`, `stageAll`,
`unstageAll`, `commit`, the post-commit head read, `headMessage`, and each of the three
diff forms. For each: non-zero exit, unreadable stderr, context cancelled mid-call, and
where applicable a truncated stream. Plus the partial procedures, each with a stated answer:

- stage succeeds, the status that follows it fails → the mutation happened; the panel says
  the view is stale rather than reverting the row.
- commit succeeds, reading the new head fails → the commit happened; that is what the panel
  must say.
- binding `Close` fails; `Repo.Close` fails; **binding-id generation fails after a
  successful `Open`** — asserting the `Repo` was closed and no binding was returned, which
  is the ownership-transfer rule in §5.1 and the leak this design has now been caught
  leaving twice.
- the factory returns a malformed outcome in either direction: `(nil, ok, nil)` is an
  internal error, and a non-nil `Repo` on a refusing outcome is closed rather than dropped.
- `rev-parse` succeeds with malformed or multi-line output → `notARepository`.
- the environment resolver fails, times out, and succeeds.
- `rev-parse` answers one line, three lines, or a relative path → `notARepository`.
- a diff that reaches `maxBytes`: the child is killed and reaped, the result is `tooLarge`
  and **two assertions, not one**: `local`'s private execution result records the cut, and
  the public `Diff` is `tooLarge` and carries no cut flag of its own. The broken pipe is
  **not** reported as a failure.
- stderr exceeding its bound: `StderrTruncated` is set, the reader does not deadlock, and
  the commit surface renders the truncation mark.
- a `git.open` whose response is stale: the store calls `git.close` on the returned
  binding, and the backend's registry is empty afterwards — the `nocx-myts` leak, asserted
  rather than hoped for.
- a status whose traversal hits the work ceiling: the child is cut and reaped, the result
  carries a lower bound with `completeness: 'cut'`, and the panel says "more than N".
- a cut invocation whose child spawns a descendant that ignores `TERM`: the escalation
  reaches `KILL`, the process group is gone, and the read sees EOF rather than hanging.
- **the session-close notification actually arrives**: an over-the-socket test that closes
  a session and asserts the client receives `git.changed`. This is the test whose absence
  let `nocx-lzfb` ship — a notification nobody asserted the delivery of.

**Intervals with both ends (rule 3).** Two are stated and tested: the binding interval in
§5.1, and D18's mutation interval — _a mutation may begin only while its binding is the
store's current one, and its result is applied only while its scope still holds_.

**Acceptance criteria as assertions (rule 4).** Every bead states its criteria as
assertions, in the bead.

**The wire (rule 5).** Each of the eleven shapes gets a schema, a DTO conformance test and
an over-the-socket test. `status` is exercised through: an empty repository, an unborn
branch, a detached HEAD, a rename, a file in both lists, a path containing a newline, a
a `capped` status with an exact total, and a `cut` status whose total is a lower bound —
including the case that dictated a single discriminator: a `cut` that happened **below** the
record cap, where the lists hold every record that was observed and nothing about their
length says the status is incomplete.

**Frontend store tests** cover every transition in §5.4 without a socket, including each of
the four D17 races by name, the amend-token sequence (tick, untick, reply — the form stays
empty; tick, untick, tick — the first reply is discarded), the mutation gate refusing a
second concurrent mutation, a stale successful open being closed, a failed poll leaving the
last good status on screen, and `cwdFollow: false` not re-binding.

## 8. Bead changes

- **New epic**, labelled `mvp`, acceptance criterion = the happy path in §7, with §4's Out
  list named in it. Children per §6, sequenced with `blocks` so the ready front stays ~3.
- **`nocx-terg`** — stays separate; `bd dep add nocx-terg <this-epic>`.
- **New bead under `nocx-if6`**: "the Git panel works on an SSH tab, over the relay". It
  carries D3's reasoning **and** the honest statement from §2: the reserved `metadata`
  msg-type is a one-way feed, so the relay needs a channel it does not have, and designing
  it is a protocol decision rather than a coding task. Because the seam is operation-shaped
  (D16), what that protocol must carry is a **closed set of named operation
  requests and responses** — `open`, `status`, `diff`, `stage`, `unstage`, `stageAll`,
  `unstageAll`, `commit`, `headMessage` — plus cancellation, results bounded on the machine
  doing the work, and the domain outcomes of §5.1. **Not** stdin, separated stderr or an
  exit status: those are the vocabulary of the process-shaped seam this design rejected, and
  asking the relay for them would reintroduce it through the bead.
- **New standalone beads**: discard; branch checkout/create; push/pull/PR (an epic); hunk
  staging; split diff; syntax highlighting inside the diff; conflicts as a surface;
  running the panel's commit in the tab's live environment (D6); window-focus gating of
  sidebar polling, if it is ever wanted, as shared sidebar state.
- **`nocx-lzfb`** (a session dying destroys its file bindings silently) gets the mechanism
  written into it: the teardown paths remove the session receiver at `ws.go:1701` / `:1745`
  before cleaning up bindings, so an emit-time subscriber lookup finds nobody. The fix —
  capture the subscriber before removal, as `monitorExit` already does for `exit` at
  `:1696` — is shared with this epic's step 4, and whichever lands first should do it for
  both.
- **`nocx-hzfe`** is worth noting as encountered: the local gate assumes
  `frontend/node_modules` exists.

## 9. Open questions

Three, and they are the same three after five review rounds: every one is an unmeasured
constant rather than an undecided design. Each is recorded with what would settle it, so
the measurement is a task and not an opinion.

1. **`MaxStatusEntries`.** 5,000 is the proposal — large enough that a real change set is
   never capped, small enough that a stray `node_modules` is caught.
   **Settled by:** peak retained memory and render time at the cap, plus the distribution of
   status sizes across representative repositories including a monorepo and a generated
   tree.
2. **Untracked directories.** `--untracked-files=all` lists every file inside an untracked
   directory, which is what makes an un-ignored `node_modules` a five-figure status;
   `normal` collapses to the directory but makes staging it ambiguous. Proposal: keep `all`
   and let D9's ceiling answer.
   **Settled by:** how often `capped` or `cut` fires on ordinary repositories that contain a
   common un-ignored build tree, and whether `normal`'s collapsed rows are usable when they
   do.
3. **The 5 s poll interval** is inherited from Ports rather than measured for git.
   **Settled by:** subprocess duration, CPU and I/O of one `git status` on a large local
   repository, and how often consecutive polls overlap with the sidebar left open. If it
   argues for a different cadence, the answer is a measured shared constant — not a private
   one for this panel.

## 10. Review history

- **2026-08-06, revision 1** — brainstormed with the owner (`nocx-5nej`). Five decisions
  taken by the owner directly: the termic-shaped slice, local-only with the relay carrying
  the remote case, live cwd-following repository resolution, unified diff in a tab, and
  stage/unstage/commit/amend with discard held back.
- **2026-08-06, revision 2** — adversarial review against the code (codex). Fifteen
  findings accepted, of which three were false claims revision 1 made about existing code
  (`TreeRow`'s slots, a reusable CodeMirror host, a window-focus predicate in Ports), one
  was a direct contradiction of AD-9 and the sibling spec (bindings dying on WebSocket
  reconnect), and one was the failure to reuse `files-store.ts`'s response-scope
  discipline. Three findings were argued down rather than accepted, and the argument is in
  the text: cancelling an in-flight mutation on re-bind (D18 — the cure is worse), the
  claim that `git.open{cwd}` crosses a boundary AD-1 forbids (§2 — AD-1's 2026-08-02
  amendment permits it and two shipped methods already rely on that), and the framing of
  the relay deferral (§2, §8 — the direction is architecture-sanctioned; what is missing
  is a protocol, and that now rides in the bead). **All three pushbacks were accepted by
  the reviewer.**
- **2026-08-06, revision 3** — second adversarial pass on revision 2. Eleven findings, ten
  accepted: a validation rule that would have rejected every repository (`rev-parse` prints
  two lines, not one); D17's guard being unable to order two requests issued under one
  scope, which needed the epoch; concurrent mutations having no lane; a stale successful
  open leaking its binding rather than merely being dropped; a diff bound that bounded
  memory but not work; `Result` having no way to admit a clipped stderr while D11 promised
  "verbatim"; D19 naming no commands; the kit row mandated without evaluating
  `CollectionRow`; the CodeMirror extraction having no ownership contract; and the amend
  prefill's own race. One was corrected in the other direction, by measurement: the review
  held that unstage-all fails on an unborn branch under both `git reset` and
  `git restore --staged` and would need a `git rm --cached` special case. Bare `git reset`
  in fact succeeds there; only `git restore --staged` fails. Measuring it also turned up a
  hazard neither side had: bare `git reset` during a conflicted merge deletes `MERGE_HEAD`,
  so "Unstage all" would have silently aborted a merge. Both are now D19's refusals.
- **2026-08-06, revision 4** — third pass, five findings, all accepted. The most valuable
  was structural rather than local: the session-teardown paths remove the session receiver
  before they clean up bindings (`ws.go:1701`, `:1745`), so a notification that resolves its
  destination at emit time is **undeliverable on exactly the event it exists for** — which
  is the mechanism behind the open bug `nocx-lzfb`, not a new problem. Also accepted:
  status was still work-unbounded (the retention cap bounds memory, not git's traversal),
  which left the cut state unreachable while the tests demanded it; `ErrEnough` killed
  one process rather than the process group, where ADR-0020 already decided the answer
  (`INT → TERM → KILL` against an isolated group); `CollectionRow` cannot host an
  activatable dense row without being extended first; and the host contract omitted
  `focus()` and `dispose()`, both of which `FileViewerContent` calls directly today. The
  reviewer confirmed the revision-3 measurement and withdrew its unborn-branch prescription.
- **2026-08-06, revision 5 — the owner moved the seam**, and the argument that had been
  defending its old position was void. Revisions 1–4 put the local/remote boundary at the
  process (`Runner.Run(argv, stdin, out)`), justified by "a higher seam would make the relay
  parse porcelain twice". The owner's objection: the git binary is **one channel** and the
  relay's WebSocket is another, so a process-shaped interface forces the second to emulate
  the first. It is decisive, and the counter-argument collapses on AD-2 — the relay is a
  build target of _this_ codebase, so it links the _same_ parser at any seam level, and the
  duplication was never on the table. D16 is rewritten: the seam is `Repo`, a closed set of
  named operations, mirroring the file manager's `Provider`; spawning, process groups,
  pipes and the early-cut protocol are now private to `internal/git/local`, and every state
  a user can observe is declared machine-independently so the relay can report it too.
- **2026-08-06, revision 6** — a pass aimed squarely at the debris a late boundary move
  leaves, which is what it found. Seven findings, all accepted. Moving the seam had left
  repository resolution with **no abstraction to run in**: capability and `rev-parse` must
  happen before a `Repo` exists, so revision 5 left them in the composition layer as
  `os/exec` — local mechanics back above the seam. Hence `RepoFactory`. `Cut` had been kept
  in the domain types in the name of "the relay can report it too" while being exactly the
  thing the relay cannot have: it is now private to `local`. `gitargs` had been assigned to
  "every implementation", conflating the relay _client_ (which sends operations) with the
  helper (which is `local` on the other machine). And the two-boolean status completeness
  had a state that rendered as complete when it was not — a traversal cut below the record
  cap — now one `Completeness` discriminator. Plus three pieces of stale vocabulary.
- **2026-08-06, revision 7** — six findings, all accepted, every one a consequence of
  revision 6's own fix. `RepoFactory` had introduced a second leak interval of the class
  already found twice: a live `Repo` between `Open` returning and `Register` succeeding,
  owned by nobody. It now has an explicit ownership-transfer rule and a test. `Capability`
  had ended up with two owners — the factory outcome and a `Repo` method that could
  disagree with it — and the method is gone. The `gitargs` ownership fixed in revision 6's
  prose still read the old way in D16's decision row, which is the row an implementer
  copies. The deferred relay bead still asked for stdin, separated stderr and an exit
  status — the vocabulary of the rejected seam, which would have reintroduced it through
  the backlog. And `Cut`, removed from the domain, was still named in a public diff result
  and its test.

  A final pass found two stale seam descriptions left by revision 7's own edits — the seam
  table and the sequence still named `Repo.Capability`, and the package layout still put
  `porcelain` and `gitargs` where "every implementation" links them. Both are fixed: the
  two live under `spawn/`, linked only by code that runs git.

  **Verdict: implementable as written**, with the three unmeasured constants in §9 recorded
  as risks rather than defects. Across six rounds the review raised 49 findings: 45 accepted,
  3 argued down with evidence and withdrawn by the reviewer, and 1 corrected in the opposite
  direction by measurement.

- **2026-08-07, after delivery** — the owner opened the shipped panel and made three
  reports, none of which any gate could have made.

  **The rows were broken in half.** Every row rendered its status letter and file glyph on
  one line and the path on the next, long paths running past the panel into the terminal.
  The three parts carry flex-item declarations and their parent was a plain block. The
  wrapper that made them a row had been removed during acceptance — correctly, because it
  had been placed _outside_ `CollectionRow`, where it orphaned `role="listitem"` from its
  list; incorrectly, because it belonged _inside_ the info slot rather than nowhere. Ten
  unit tests stayed green: jsdom computes no layout, so a component test cannot see a row
  broken into three lines, and the e2e asserted rows are visible and clickable, which a
  wrapped row is. **The suite now carries a geometric assertion** — bounding boxes, in a
  real browser — because that is the only place this class of defect is visible.

  Fixing the wrap exposed the reason the row exists at all: with the path rendered as one
  string it ellipsises at the tail, and the tail is the file name, so twelve files under
  one deep directory read as twelve identical `graphify-out/cache/…` rows. The panel's
  whole job is to say _which file changed_. Name first, directory after it and dimmed, the
  way both reference products read a path in a rail.

  **Half of what the reference products show was missing** — see "Neither in nor out"
  in §4, which records the omission and why the method allowed it.

  **The panel took ~6 seconds to answer, including for a directory that is not a
  repository.** git is not the cost: on the owner's own repository, with 761 untracked
  files, `status --porcelain=v2 -z -uall` is 11 ms and `rev-parse` is 2 ms. `Factory.Open`
  resolves the shell environment _first_, before probing git and before `rev-parse`, and
  that resolution runs `shell -i -c "export -p"` with a 5 s timeout which on the owner's
  machine expires — the panel's own "degraded environment: context deadline exceeded" is
  that deadline. `envCache.resolve` deliberately does not cache a failure, so the full
  timeout is paid on _every_ open. D6's reason for the resolved environment is real and
  specific to the commit path; nothing in status, diff or log needs it. Filed P0 as
  `nocx-6pz0`.

  One shape under all three: **every criterion this design wrote was one the design could
  check about itself.** Layout, parity with the artefact the owner supplied, and latency
  are the three things a spec cannot assert and a suite did not — and all three were
  found in the first minute the owner looked at the product.

- **2026-08-07, second wave** — the owner compared the delivered panel against orca side by
  side, and the gap was not subtle. Seven changes came out of it, and the pattern under
  them is worth more than the list.

  **What was missing had been promised or shown.** The line counts and the commit list are
  in the screenshots that opened this design; §4 recorded neither as an inclusion nor a
  refusal (see "Neither in nor out"). The **filter box** is worse than an omission — the kit
  table in §5.4 lists `Filter box → SearchField` and the panel simply never had one, so the
  epic closed against a criterion that watched one file through the happy path, and one
  file needs no filter. Both are now delivered, along with a resizable sidebar, collapsible
  sections, branch copy, and hosting links for the branch and every commit.

  **Two defects were in the kit, not the surface.** `.ui-badge` declared no `white-space`,
  so a long value wrapped INSIDE the pill: a branch named `fix/e2e-files-reveal-and-container`
  rendered as a three-line block and a commit's ref badges stacked four deep, tripling the
  row. A badge is a chip — one line, whatever it holds — and it clips now. The branch then
  stopped being a badge at all, because a branch name is neither short nor bounded and even
  clipped it spent the pill's padding on the one string in that header that must be read in
  full. And the sidebar's own inset was doubled by the panel adding its own on top: 48px of
  air in a 240px rail, taken from the file names.

  **The rail is 240px, and that was the real constraint.** Every CSS optimisation available
  bought about 15px. `#sidebar { width: 240px }` was hard-coded, `--sidebar-width` was
  declared and read by nothing, and `base.css` told the reader the sidebar was
  "user-resizable" — false. A drag handle buys whatever the user wants, and now exists
  (`role="separator"`, keyboard-operable, 200–640px, persisted).

  **A gate that exists and is never invoked is the same defect as unreachable code.** A
  botched conflict resolution left `e2e/git-panel.spec.ts` syntactically invalid, and it
  passed every gate in the pre-commit hook — prettier ran under `cd frontend`, and the ROOT
  `tsconfig.json` that covers `e2e/**/*.ts` was invoked by nothing. That tsconfig exists
  BECAUSE two specs once shipped as compile errors and ran anyway. The e2e suite is where
  several properties of this product are asserted at all, including the geometric assertion
  that caught the broken row.

  **And the latency fix regressed the thing it was fixing.** `nocx-6pz0` correctly stopped
  `Open` waiting on the shell environment — but `envState` is delivered by `git.open` and by
  nothing else, so a value that was final when Open waited became a snapshot that can never
  be corrected. One open landing before the background resolution settles pins "degraded"
  for the lifetime of the binding, while commits run with a perfectly good environment. That
  is D6 inverted: the decision exists so a REAL degradation is visible, and a warning that
  cannot be withdrawn teaches the user to ignore it. Filed as `nocx-69ey`.

  The measurement that found it is the one worth keeping: `$SHELL -i -c 'export -p'` is
  31 ms on that machine and the resolver returns `resolved` in 22 ms. Two rounds had been
  spent theorising about a hanging shell.
