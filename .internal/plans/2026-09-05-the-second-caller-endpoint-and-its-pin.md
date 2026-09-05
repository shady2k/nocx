# The second caller's endpoint and its pin — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: use beads-superpowers:subagent-driven-development
> to implement this plan task by task. Each Task is one bead under `nocx-rowqt`.
> **This repository migrated from `bd` to `br` on 2026-09-05** — every tracker command in the
> skills says `bd`; use `br`. Read `.internal/TRACKER-MIGRATION-NOTICE.md` before your first
> tracker command.

**Goal:** An external process running in a nocx pane calls the five wave methods over a private
local socket, is admitted only when the kernel says it belongs to the process tree that enrolled
that pane, and moves the same wave record through the same executors the in-process assistant
uses.

**Architecture:** A listening unix socket owned by `cmd/nocx-server`, beside the existing
discovery socket in the same runtime directory and reusing its path-security primitives. Peer
identity comes from the kernel at `connect(2)`; a `(pid, start-time)` root pins the enrolled
tree and an ancestry walk decides membership. An authorizer turns an admitted peer into an
`assistant.WaveInvocation`; the dispatcher landed in `8c5434c6` does everything after that.

**Tech Stack:** Go 1.x, `golang.org/x/sys/unix`, `github.com/santhosh-tekuri/jsonschema/v6`,
JSON-RPC 2.0 over a unix stream, existing `contracts/tools/wave.*.schema.json`.

## Global Constraints

- Build tag on every Go command on Linux: `-tags gtk3`. Without it cgo fails before our code.
- Three checks, none of which replaces the others: `go build -tags gtk3 ./...` (does not compile
  `_test.go`), `go test -tags gtk3 -race -count=1 <pkgs>`, `golangci-lint run <pkgs>` (the commit
  gate runs it and `go test` does not).
- No repo-wide gates from a task worker — no `make ci`, no containerized suites, no e2e.
- Every commit names its bead in the subject, per `AGENTS.md`.
- **Tasks 1–5 land as one sequence on one branch.** Task 2's endpoint is unreachable until Task 4
  wires it, and the deadcode ratchet is a pre-commit hook: a commit that adds an unpublished
  listener will not pass. Commit per task, but do not push a partial sequence to `main`.
- Nothing this plan builds may claim more authority than the session already has
  (`.internal/specs/2026-09-03-the-waves-authority-model-design.md` A12). Where the code could be
  read as claiming more, it says so in a comment.

## Binding documents — read before Task 1

- `.internal/specs/2026-09-05-the-second-caller-design.md` — D1..D12 and §4, §5, §6, §7. This plan
  implements it and does not re-decide it.
- `.internal/specs/2026-09-03-the-waves-authority-model-design.md` §4 (A8, A9, A10, A12) and §5.
- `.internal/specs/2026-08-15-workspaces-lineage-and-orchestration-design.md` D13, D14.
- `docs/architecture.md` AD-1, AD-6, AD-7, AD-8; ADR-0024 decision 2; ADR-0028.

## Deliberate cuts, surfaced rather than omitted

Each is a bead, not a silence.

1. **The remote helper transport (design D4, D12).** Out. The design's open question 3 says the
   tree does not answer how a helper-hosted socket selects its owning backend bridge. Local only
   here. Bead: file as `nocx-rowqt.11` before Task 1 begins.
2. **D13's human approval.** Not built. The authorizer admits a caller whose tree root is the
   session that enrolled through the lifecycle channel — a real act tied to a pane the person
   opened, and exactly the A12 ceiling. It is NOT the approval D13 asks for. Bead:
   `nocx-rowqt.12`, and Task 3 writes the limitation into the code.
3. **A ledger attempt for an external call.** The in-process path writes an execution attempt
   before every call so a refusal stays auditable; this plan does not give the external path one,
   because what a person should see for an external coordinator's mutations is a product question.
   Bead: `nocx-rowqt.13`. Task 4 logs each admitted call through `slog` so the gap is not silent.
4. **The participant capability and mesh talk** — already `nocx-rowqt.9` and `nocx-d3k46`.

## File structure

| File                                                      | Responsibility                                                                                                                                                                                                                                                                               |
| --------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `internal/coordinator/runtime.go` (create)                | The path-security primitives lifted out of `server.go`: ensure the dir at `0700`, refuse a foreign owner, refuse a symlink or non-socket occupant, refuse an overlong path, bind through a temporary name and publish atomically at `0600`. One answer to the question, used by two sockets. |
| `internal/coordinator/server.go` (modify)                 | Calls the lifted primitives instead of holding its own copy. No behaviour change.                                                                                                                                                                                                            |
| `internal/wavepin/pin.go` (create)                        | `Root` = `(pid, startTime)`; `Pin` resolves a root, `Member` walks ancestry to it. Platform files for start time.                                                                                                                                                                            |
| `internal/wavepin/pin_linux.go`, `pin_darwin.go` (create) | Start time and ppid per platform, the shape `internal/coordinator/peer_*.go` already uses.                                                                                                                                                                                                   |
| `internal/waveendpoint/endpoint.go` (create)              | The listener, the accept loop, the bounded newline-delimited JSON-RPC framing, one response per request id. Knows nothing about wave semantics.                                                                                                                                              |
| `internal/waveendpoint/authorizer.go` (create)            | `Authorizer` interface and its refusals. The endpoint holds one and never constructs an invocation itself.                                                                                                                                                                                   |
| `internal/app/waveauth.go` (create)                       | The one implementation: enrolled-tree lookup → `assistant.WaveInvocation`. Lives in `app` because the enrolment record and the wave record are both there.                                                                                                                                   |
| `internal/app/app.go` (modify)                            | Exposes the built `assistant.WaveDispatcher` and the authorizer, the way `a.Transport` is already exposed.                                                                                                                                                                                   |
| `cmd/nocx-server/main.go` (modify)                        | Starts the wave endpoint after `a.Start`, only when the authorizer is non-nil; closes it before shutdown.                                                                                                                                                                                    |
| `contracts/waveendpoint.md` (create)                      | Which contract documents the endpoint validates against and how it references them.                                                                                                                                                                                                          |

---

### Task 1: Lift the runtime-directory primitives so two sockets share one answer

**Files:**

- Create: `internal/coordinator/runtime.go`
- Modify: `internal/coordinator/server.go` (the dir/socket preparation path), `internal/coordinator/peer.go` (the new `PeerProcess` interface and the method on `SystemPeerCredentials`)
- Test: `internal/coordinator/runtime_test.go`

**Interfaces:**

- Produces: `func PrepareRuntimeDir(dir string, owner PathOwner, selfUID uint32) error`;
  `func BindSocket(dir, name string) (*net.UnixListener, error)`. Both return the existing
  sentinels — `ErrForeignOwner`, `ErrSymlinkPath`, `ErrOccupiedPath`, `ErrPathTooLong`.
  **Also** — corrected 2026-09-05 after the first attempt was blocked, and the worker was right:
  a SECOND, separate interface `PeerProcess { PeerPID(conn *net.UnixConn) (int, error) }` in
  `peer.go`, which `SystemPeerCredentials` also satisfies over the `peerPID` that already exists
  unexported in `peer_linux.go:41` and `peer_darwin.go:40`.
  **`PeerCredentials` is NOT widened.** The first draft said to add the method to it, which is
  wrong twice over: every existing double of that interface — `fixedPeer` in
  `coordinator_test.go` — would stop satisfying it, contradicting this task's own
  behaviour-preserving criterion; and `peer_linux.go` says in as many words that the discovery
  SERVER deliberately does not use a pid, "because peerUID above says why a pid is the wrong
  thing to make a trust decision on". Widening the interface it holds would contradict that
  comment. Two interfaces is the honest shape: the discovery socket keeps asking only what it
  trusts, and the party that pairs a pid with a start time asks for the pid explicitly.
  Keep `peer_linux.go`'s comment as it stands and add beside it that `internal/wavepin` is what
  stops the pid being bare, by pairing it with the start time the helper's own record already
  calls the pid-reuse guard (`internal/helper/proto/session_service.go:384-390`).
- Consumes: nothing new.

**Acceptance Criteria:**

- The discovery socket's own tests pass unchanged, so the lift is behaviour-preserving.
- `PeerPID` reports this process's own pid when the test dials its own listener — the paired
  "and on a normal machine it succeeds" for the refusals below.
- A directory owned by another uid, a symlinked socket path, a non-socket occupant and an
  overlong path each produce their existing sentinel from the new functions.
- `grep -c 'os.MkdirAll' internal/coordinator/server.go` shows the duplicate preparation is gone
  rather than copied.

- [ ] **Step 1: Write the failing test** — `internal/coordinator/runtime_test.go`, asserting the
      four refusals against `PrepareRuntimeDir` and `BindSocket` using a `t.TempDir()` and the
      existing test doubles for `PathOwner`.
- [ ] **Step 2: Run it and watch it fail** — `go test -tags gtk3 -run TestPrepareRuntimeDir ./internal/coordinator/` → `undefined: PrepareRuntimeDir`.
- [ ] **Step 3: Move the code.** Cut the preparation and bind logic out of `server.go` into
      `runtime.go` as the two exported functions. Do not rewrite it; move it, then have `server.go`
      call it.
- [ ] **Step 4: Run both suites** — `go test -tags gtk3 -race -count=1 ./internal/coordinator/...` → PASS, with the pre-existing server tests untouched.
- [ ] **Step 5: Lint and commit** — `golangci-lint run ./internal/coordinator/...`, then
      `git commit -m "refactor(coordinator): one answer to runtime-directory safety, for two sockets (nocx-rowqt.3)"`.

---

### Task 2: The pin — a root that a later request cannot forge

**Files:**

- Create: `internal/wavepin/pin.go`, `internal/wavepin/pin_linux.go`, `internal/wavepin/pin_darwin.go`
- Test: `internal/wavepin/pin_test.go`

**Interfaces:**

- Produces:
  - `type Root struct { PID int; StartTime time.Time }`
  - `type Pinner interface { Pin(pid int) (Root, error); Member(child int, root Root) (bool, error) }`
  - `type SystemPinner struct{}` implementing it
  - `var ErrGone = errors.New("wavepin: the pinned process is gone or was replaced")`
  - `var ErrChainBroken = errors.New("wavepin: ancestry to the pinned root is broken")`
- Consumes: nothing.

**Acceptance Criteria:**

- `Pin` on a live pid returns a root whose start time the OS supplied; `Member(self, root)` is
  true for a child of that root.
- A pid reused after the pinned process exits is refused with `ErrGone`, proven by pinning a
  short-lived process, letting it exit, and asserting the refusal rather than by simulating.
- A process whose ancestry chain to the root is broken — reparented — is refused with
  `ErrChainBroken` rather than admitted.
- **The paired success case**: on an ordinary machine, pinning this test's own process and asking
  `Member` about a child it spawned succeeds. (`AGENTS.md`: for every "returns an error when…"
  there is a paired "and on a normal machine it succeeds".)
- A comment states the ceiling: this is a fence against confusion — a stale pid, the wrong agent,
  an unrelated process — and NOT a defence against a same-uid actor, and the principal is the
  TREE, per D14's "allow this agent and commands it launches".

- [ ] **Step 1: Write the failing tests** for all four criteria above.
- [ ] **Step 2: Run and watch them fail** — `go test -tags gtk3 -run TestPin ./internal/wavepin/`.
- [ ] **Step 3: Implement.** Linux reads `/proc/<pid>/stat` field 22 for start time and field 4
      for ppid; darwin uses `unix.SysctlKinfoProc` for both. Follow the per-OS file pair shape in
      `internal/coordinator/peer_linux.go` and `peer_darwin.go`, and say in a comment why the pair
      exists rather than a build-tagged `if`.
- [ ] **Step 4: Run** — `go test -tags gtk3 -race -count=1 ./internal/wavepin/` → PASS.
- [ ] **Step 5: Lint and commit** — `golangci-lint run ./internal/wavepin/...`, then
      `git commit -m "feat(wavepin): a root the kernel stamped, and an ancestry walk to it (nocx-rowqt.8)"`.

---

### Task 3: The authorizer seam, and the endpoint that holds one

**Files:**

- Create: `internal/waveendpoint/authorizer.go`, `internal/waveendpoint/endpoint.go`
- Test: `internal/waveendpoint/endpoint_test.go`

**Interfaces:**

- Produces:
  - `type Peer struct { UID uint32; PID int }`
  - `type Authorizer interface { Admit(Peer) (assistant.WaveInvocation, error) }` — returning the
    invocation WITHOUT `Method`/`RawParams`, which the endpoint fills per request.
  - `var ErrNotEnrolled = errors.New("waveendpoint: caller is not in an enrolled process tree")`
  - `func New(cfg Config) (*Endpoint, error)`, `(*Endpoint).Start() error`, `(*Endpoint).Close() error`,
    `(*Endpoint).SocketPath() string`
  - `type Config struct { Dir string; Peers coordinator.PeerCredentials; Owner coordinator.PathOwner; SelfUID uint32; Auth Authorizer; Dispatch assistant.WaveDispatcher; Logger *slog.Logger }`
- Consumes: Task 1's `PrepareRuntimeDir`/`BindSocket`; the dispatcher from `8c5434c6`.

**Acceptance Criteria:**

- The socket is `wave.sock` in the same runtime directory, mode `0600`, published atomically.
- Peer credentials are read immediately after `accept` and before any byte of a request is
  parsed; a peer whose credentials cannot be read, and a foreign uid, are refused.
- A request naming a `sessionId` cannot redirect the call: the invocation's session comes from
  the authorizer and the endpoint never reads one from params (design D7).
- Malformed JSON, an oversized envelope, an unknown method and a JSON-RPC _notification_ for a
  wave method are each refused with a standard JSON-RPC error before dispatch, and no wave record
  mutation happens.
- `New` returns an error when `Auth` or `Dispatch` is nil — a nil field is a configuration error,
  the shape `coordinator.Config` already uses.
- Request payloads, task text and mail bodies are never logged.

- [ ] **Step 1: Write the failing tests** with a stub `Authorizer` and a stub `WaveDispatcher`,
      driving a real `net.Dial("unix", …)` against a real listener in a `t.TempDir()`.
- [ ] **Step 2: Run and watch them fail.**
- [ ] **Step 3: Implement the accept loop.** One goroutine per connection; a read deadline per
      request, not per connection, because `wave.wait` is declared with an 11-minute deadline
      (`internal/agenttools/registry.go:788`) while the discovery socket's whole exchange is bounded
      at 30 s (`internal/coordinator/server.go:37`) — do not copy that bound.
- [ ] **Step 4: Run** — `go test -tags gtk3 -race -count=1 ./internal/waveendpoint/` → PASS.
- [ ] **Step 5: Lint and commit** — `git commit -m "feat(waveendpoint): a socket the kernel stamps, and an authorizer it cannot bypass (nocx-rowqt.3)"`.

---

### Task 4: The one authorizer — an enrolled tree becomes an invocation

**Files:**

- Create: `internal/app/waveauth.go`
- Test: `internal/app/waveauth_test.go`

**Interfaces:**

- Consumes: `wavepin.Pinner`, the enrolment record reached through `internal/lifecycle`, the wave
  record built at `internal/app/app.go:1949`.
- Produces: `func newWaveAuthorizer(...) waveendpoint.Authorizer`.

**Acceptance Criteria:**

- A caller whose pid is inside the tree pinned at a pane's enrolment is admitted, and its
  invocation carries THAT pane's session and a grant whose environment scope is the local one —
  so `wave.spawn` is reachable and nothing else widens.
- A caller of the same uid outside every enrolled tree is refused with `ErrNotEnrolled`, and the
  refusal is not changed by anything the caller sends.
- When the enrolment interval closes (`agent_withdraw`), a later call from that tree is refused:
  the interval has two ends and the authorizer honours both.
- The file states in a comment that this is NOT D13's human approval, names `nocx-rowqt.12`, and
  says the ceiling is A12.

- [ ] **Step 1: Write the failing tests** against the real registrar with an in-memory store and a
      fake `Pinner`, so the refusals are exercised without spawning processes.
- [ ] **Step 2: Run and watch them fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** — `go test -tags gtk3 -race -count=1 ./internal/app/` → PASS. This package is
      slow (~170 s); run it once, not in a loop.
- [ ] **Step 5: Lint and commit** — `git commit -m "feat(app): an enrolled tree is what admits a wave caller (nocx-rowqt.8)"`.

---

### Task 5: One live caller per session, and what the slot does not gate

**Files:**

- Modify: `internal/app/waveauth.go`
- Test: `internal/app/waveauth_slot_test.go`

**Interfaces:**

- Produces: the authorizer acquires a per-session controller slot inside `Admit` and releases it
  when the connection the endpoint handed it is observed closed.
- Consumes: Task 4's authorizer.

**Acceptance Criteria:**

- A second live caller for the same session is refused with a named refusal — `session already
has a wave caller` — and can neither read mail nor mutate the record (design D10).
- When the first connection closes, the slot is released only after its in-flight call has
  stopped or settled; a replacement then acquires it and `wave.holdings` answers from the
  session's existing record, which is not copied or reset.
- **The slot gates the coordinator seat and gates no conversation.** A comment says so, citing
  the mesh design's `M1` (talk is mesh from day one) and the authority model's `A1` (membership
  makes a participant addressable, delegation makes it controllable). Each participant has a
  session of its own, so a worker's own calls sit under its own slot. This matters because the
  bare rule reads as star-on-everything, which `M1` rejects by name.
- Nothing here is an idempotency guarantee (design D11): a mutation whose response was lost is
  unknown until `wave.holdings` says otherwise, and no response cache is invented.

- [ ] **Step 1: Write the failing tests** — a second `Admit` for the same session refused; the
      slot released after the first connection closes; a replacement admitted.
- [ ] **Step 2: Run and watch them fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** — `go test -tags gtk3 -race -count=1 ./internal/app/` → PASS.
- [ ] **Step 5: Lint and commit** — `git commit -m "feat(app): one live wave caller per session, and talk is not gated by it (nocx-rowqt.3)"`.

---

### Task 6: Wiring, and the refusal to publish

**Files:**

- Modify: `internal/app/app.go` (expose the dispatcher and the authorizer, beside `Transport`)
- Modify: `cmd/nocx-server/main.go:99-118` (start the endpoint after `a.Start`, close before shutdown)
- Test: `cmd/nocx-server/main_test.go`

**Acceptance Criteria:**

- With an authorizer composed, `nocx-server` publishes `wave.sock` and an admitted caller's
  `wave.holdings` returns what that session holds.
- With the authorizer nil, **no socket file is created at all** — asserted by `os.Stat` on the
  path, not by a request that fails (design D8).
- The endpoint is closed before the app shuts down, and the socket file is gone afterwards.
- `deadcode -tags gtk3 -whylive 'github.com/shady2k/nocx/internal/waveendpoint.Endpoint.Start' ./...`
  prints a path from `main`. Run the contrast too: a symbol you did not wire prints
  "reachable only through reflection". `-filter` is not evidence (`AGENTS.md`).

- [ ] **Step 1: Write the failing test** in `cmd/nocx-server/main_test.go`: start the server, dial
      `wave.sock`, call `wave.holdings`, assert the answer; and a second test asserting no socket file
      exists when no authorizer is composed.
- [ ] **Step 2: Run and watch them fail.**
- [ ] **Step 3: Wire it.** `app` exposes the dispatcher and authorizer as fields the way
      `a.Transport` already is; `cmd/nocx-server` builds `waveendpoint.New` only when both are non-nil.
- [ ] **Step 4: Run** — `go test -tags gtk3 -race -count=1 ./cmd/nocx-server/ ./internal/app/` → PASS.
- [ ] **Step 5: Lint and commit** — `git commit -m "feat(app,server): the wave endpoint is published, or it does not exist (nocx-rowqt.3, nocx-rowqt.8)"`.

---

### Task 7: The wire is a party to the contract

**Files:**

- Create: `contracts/waveendpoint.md`
- Test: `internal/waveendpoint/contract_test.go`

**Acceptance Criteria:**

- Each of the five methods validates its params against the existing
  `contracts/tools/wave.*.schema.json` — referenced, never re-declared (design D9).
- An `…_OverTheWireConformsToContract` test validates the REAL result off the REAL socket, not a
  payload the test built (`AGENTS.md` testing rule 5).
- `npm run contracts:check` passes.

- [ ] **Step 1: Write the failing over-the-wire test.**
- [ ] **Step 2: Run and watch it fail.**
- [ ] **Step 3: Implement the reference mechanism** and write `contracts/waveendpoint.md` naming it.
- [ ] **Step 4: Run** — `go test -tags gtk3 -race -count=1 ./internal/waveendpoint/` and `npm run contracts:check`.
- [ ] **Step 5: Lint and commit** — `git commit -m "test(waveendpoint): the real result off the real socket (nocx-rowqt.3)"`.

---

## After the sequence — the coordinator's job, not a worker's

1. `make ci-full` on the merged tree, once, before any push to `main`.
2. `deadcode` contrast from Task 6 re-run on the merged tree.
3. Close `nocx-rowqt.3` and `nocx-rowqt.8` with evidence a stranger can check, and confirm the
   three cut beads (`.11`, `.12`, `.13`) exist and say what they are waiting for.
