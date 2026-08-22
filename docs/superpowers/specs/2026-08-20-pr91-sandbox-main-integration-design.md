# nocx-6ftmj — PR #91 sandbox → current main: binding integration design

- **Bead:** `nocx-6ftmj`
- **Artifact role:** the ONE chosen end-to-end architecture. It names exact
  files, symbols, contracts and test assertions; it leaves no product or
  architecture choice open. It is the binding companion to
  `_bmad-output/planning-artifacts/nocx-6ftmj-sandbox-main-integration-analysis.md`
  (the requirements/conflict survey) and is summarised as the implementation
  contract in
  `_bmad-output/implementation-artifacts/spec-nocx-6ftmj-sandbox-main-integration.md`.
- **Date:** 2026-08-20
- **Target:** current main `916f2acb` (PaneManager / `panes.ts` / `layout/` /
  `internal/workspace` / two-phase `open`). **Source:** PR #91 final behaviour
  `8640647f` (`internal/sandbox`, sandbox ADRs 0033–0039, AD-11).

---

## 1. Owner-fixed decisions (not re-derivable, binding)

1. **ADR renumbering:** the PR's seven sandbox ADRs shift **+2**, preserving all
   seven as numbered ADRs (none folded):
   `0033→0035`, `0034→0036`, `0035→0037`, `0036→0038`, `0037→0039`,
   `0038→0040`, `0039→0041`. Main's `0033`/`0034` are untouched.
2. **V1 sandbox pane is absent after restart.** It is never restored, never
   reopened as an ordinary local pane, regardless of `sandbox.enabled` or a
   vanished baseline.
3. **Developer → Sandbox access** remains the settings section; the two
   `PrivateMetadata` path baselines remain under the same settings registry.
4. **The layout owner gains an explicit backend-owned non-restorable/ephemeral
   property.** The design does NOT overload `PaneKind` with a third value and
   does NOT omit `paneId`. The pane keeps its durable `paneId`/`workspaceId` and
   its history anchor while the session is live; its closed layout row remains
   for history; only the _open_ (in-window) pane is excluded at restart.
5. **Sandbox launch affordance is a More-menu row, not a third strip glyph**
   (UX review §3.4, Option B). The PR's dedicated ShieldIcon button beside `+`

> **Superseded:** the More-menu and Quick Connect entry-point decisions in this
> section are replaced by [ADR-0043](../../decisions/0043-sandbox-grants-and-the-shield-entry.md).

is NOT carried over. "New sandboxed tab" is a named row in main's existing
More menu (`tab-strip.tsx` `ContextMenu` items), gated by `sandbox.enabled`,
wired to `onNewSandboxedTab`. The Quick Connect action `__sandboxed_local__`
(`Sandboxed shell…`) is unchanged (A1).

---

## 2. The chosen design, end to end

The relocation is: **every PR sandbox fact expressed "per tab" becomes "per
pane"**, and the one load-bearing invariant is carried by a **persisted,
backend-owned `ephemeral` flag on the pane**, acted on only by the backend's
startup sweep and by the sandbox `open` gate.

### 2.1 The persisted field and its migration/default

**New persisted pane column:** `ephemeral`.

- SQLite (`internal/content/sqlite.go`, `schemaV1` → `CREATE TABLE IF NOT
EXISTS panes`): add

  ```sql
  ephemeral  INTEGER NOT NULL DEFAULT 0 CHECK (ephemeral IN (0,1)),
  ```

  placed after `closed_at`, before `digest`.

- **Migration = the existing protocol, not a new one.** This store writes no
  migrations by design (`sqlite.go` §"We write no migrations (greenfield)").
  Adding the column therefore bumps `schemaVersion` from `11` → `12`; the
  existing `resetIfSchemaChanged` rebuilds the file and logs "history
  discarded". This is the project's own contract for every schema change and is
  NOT silently worked around. `rebuildDropOrder` already contains `panes`, so
  no change there.
- **Default is `false`.** Every ordinary local pane, every SSH pane, and every
  backend-minted/replacement pane carries `ephemeral = false`. Only the sandbox
  action's pane carries `true`. This makes the field additive and byte-identical
  on the wire for all existing paths once the value is `false` (the field is
  still present — it is a required pane property, not an `omitempty`).

**Go domain type** (`internal/content/layout.go`, `type Pane struct`): add

```go
// Ephemeral marks a pane the layout records but must never restore
// (nocx-6ftmj): a sandboxed local shell. It is backend-owned — the store is
// its only writer and the startup sweep is its only actor. It is NOT PaneKind:
// the pane's pipe is still a local PTY; this says whether it may come back.
Ephemeral bool
```

**Every writer/reader of `Pane`** (`internal/content/layout_sqlite.go`):

- `paneFields` (digest input) adds `p.Ephemeral` after `string(p.Kind)` (D7).
- `insertPane` `INSERT` column list adds `ephemeral`.
- `paneByID` `SELECT` + `Scan` add `ephemeral`.
- `Panes()` inline `SELECT` + `Scan` (`layout_sqlite.go:802`) add `ephemeral`.
- `storedWorkspace` (the only read-back, via `tabByID` + `paneByID`) inherits
  it with no extra edit. Main has no `scanPanes`/`storedTab`/`storedPane` —
  those names are corrected here (dev review D4).
- **Read seam for the open gate** (`internal/content/layout.go`
  `LayoutRepository` + `internal/content/layout_sqlite.go`): add
  `IsPaneEphemeral(ctx context.Context, paneID string) (bool, error)` — one
  `SELECT ephemeral FROM panes WHERE id = ? AND closed_at IS NULL`, reusing
  `WorkspaceForPane`'s closed-row predicate, `ErrNoSuchPane` on no row. This is
  the ONLY new `LayoutRepository` method the open handler calls beyond
  `WorkspaceForPane`, and it is a read off the pool with no gate — it never
  enters the gated layout write whose deadlock inside the open operation
  `openHandlers` documents.
- **`layoutStub`** (`internal/content/stub.go`, `var _ LayoutRepository =
(*layoutStub)(nil)` — an explicit, non-embedding implementation) MUST gain
  the two no-op methods (`CloseEphemeralPanes`, `IsPaneEphemeral`) or the
  package stops compiling (dev review D6). Test fakes that _embed_ the
  interface are safe.

**Transport wire** (`internal/transport/ws_layout_handlers.go`):

- `type paneWire` gains `Ephemeral bool json:"ephemeral"`; `wirePane` copies
  `p.Ephemeral`.
- `type firstPaneParams` and `type paneCreateParams` each gain
  `Ephemeral bool json:"ephemeral"`; their `.pane(tabID)` builders copy it.
- `validatePaneFacts` gains an `ephemeral bool` parameter and a rule: **an
  ephemeral pane must be `kind == "local"`** (`"ephemeral is only admitted on
kind = local"`); `validateFirstPane` and `validatePaneCreateRaw` pass it
  through. This is the backend's _authorization_ of the flag, not a blind copy.

**Contract schema** (`contracts/panes.create.schema.json`, `$defs/pane`):

- Add to `required`: `"ephemeral"`.
- Add property:

  ```json
  "ephemeral": {
    "description": "Whether the pane is non-restorable (nocx-6ftmj): true for a sandboxed local shell, which the startup sweep closes and never restores as an ordinary local pane. Backend-owned: the store is its only writer and only the backend acts on it. It is NOT a kind — a sandboxed pane is still kind=local. Always present, never omitted.",
    "type": "boolean"
  }
  ```

  `tabs.create`, `workspaces.create`, `panes.move`, `panes.setCwd` and
  `layout.read` all reference `panes.create.schema.json#/$defs/pane`, so the
  field propagates automatically; no per-file edits. `panes.setCwd` is the
  referencer the original enumeration omitted (dev review D5).

**Frontend generated type:** `frontend/src/generated/panes.create.ts` (and
therefore `layout.read.ts`) regenerate with `ephemeral: boolean` on `Pane`.

**Frontend hand-written seam** (`frontend/src/layout/layout-store.ts`,
`frontend/src/layout/layout-client.ts`):

- `interface NewPane` gains `ephemeral: boolean`.
- `interface PaneFacts` gains `ephemeral: boolean`.
- `paneFacts(id, pane)` copies `ephemeral: pane.ephemeral`.

---

### 2.2 Creation → open → close → startup-sweep ordering

**Creation (renderer, PaneManager).**

1. The sandbox action has two entry points, both funneling into the existing
   `openSandboxedShell` flow (`frontend/src/sandbox-open.ts`): one fresh
   settings snapshot → native directory picker (workspace) → permission dialog

> **Superseded:** [ADR-0043](../../decisions/0043-sandbox-grants-and-the-shield-entry.md)
> consolidates sandbox launch into the active sidebar-panel shield.

(deltas) → `deps.newSandboxedTab(workspace, launch)`.

- Quick Connect action `Sandboxed shell…` (`id: "__sandboxed_local__"`),
  unchanged (A1).
- A named **"New sandboxed tab"** row in main's More menu (`tab-strip.tsx`
  `ContextMenu` items), gated by `sandbox.enabled`, wired to
  `onNewSandboxedTab` (UX review §3.4, Option B). The PR's dedicated
  ShieldIcon strip button is NOT carried over — main's strip stays at two
  glyphs (`+`, `More`).

2. `newSandboxedTab` is **renamed and relocated** to
   `PaneManager.newSandboxedPane(workspace: string, launch: SandboxLaunch): Pane`
   (`frontend/src/panes.ts`). It is the only new public PaneManager method.
3. `newSandboxedPane` mints the identity through a new
   `mintPane('local', null, /* ephemeral */ true)` arm so the layout row is
   created with `ephemeral: true` **before any session exists**. The pane row,
   its `paneId`, its `tabId`, its `workspaceId` and its history anchor are
   therefore durable and ordinary in every respect _except_ the flag.
4. It builds the terminal content with the sandbox request
   (`{workspace, settingsRevision, addWritable, removeWritable, addReadOnly,
removeReadOnly}`) and `onSandboxedChange: () => pane.setSandboxed()`; the
   `ContentDescriptor` is `{surfaceType: SURFACE_TERMINAL, singletonKey: null,
restoreDescriptor: null, supportsAttention: true, defaultTitle: ''}`.
5. The pane/chrome marker relocates from `Tab.sandboxed`/`setSandboxed()` to
   `Pane.sandboxed`/`setSandboxed()`; `tab-strip.tsx`/`tab.tsx` read it off the
   pane model (`data-sandboxed`), as main already does for pane attention. The
   marker span carries **both** `aria-label="Sandboxed"` and a native
   `title="Filesystem-isolated"` (the kit's ADR-0014 tooltip is the native
   `title` attribute), mirroring the warning mark's `aria-label`+`title`
   pattern (UX review §3.1).

**Open (backend, two-phase — kept two-phase).**

Phase 0 (pre-gate, in `handleOpen` before `Prepare`, main's ordering preserved):

1. Decode + validate `cols`/`rows` (`-32602`).
2. `workspaceForOpen(ctx, params.PaneID)` — main unchanged (pane → tab →
   workspace; empty paneId → default workspace).
3. If `params.Sandbox != nil` (presence is the sole opt-in; `null` already
   rejected at decode):
   a. local-only boundary: `kind != "ssh"`, no `profileId`, no `host`, else
   `-32602`.
   b. `h.settings == nil` → `-32005` `feature-disabled`.
   c. one atomic `GetSnapshot()`; snapshot error → `-32007` `setup-failed`.
   d. `sandbox.enabled` false → `-32005`; `snap.Revision !=
params.Sandbox.SettingsRevision` → `-32602` (revision mismatch).
   e. `sandboxBaselines(snap)` → `-32007` on error.
   f. `sandbox.CanonicalizeWorkspace(params.Sandbox.Workspace)` → `-32602` on
   error.
   g. **ephemeral gate:** `params.PaneID` must be non-empty AND the pane it
   names must have `ephemeral == true`, else `-32602`. The read is
   `h.panes.IsPaneEphemeral(ctx, params.PaneID)` — the read seam extended in
   §2.1, off the pool with no gate, exactly like `WorkspaceForPane`. A nil
   seam (`h.panes == nil`, content store not wired), a pane that is not
   ephemeral, or a `paneId` naming a closed/nonexistent pane is refused
   `-32602` — never defaulted. (A sandbox request with no `paneId`, or a pane
   created without the flag, is refused — this is the fail-closed half of the
   invariant.)

   h. build `sandbox.Request{Workspace, GlobalWritable, GlobalReadOnly,
AddWritable, RemoveWritable, AddReadOnly, RemoveReadOnly}`.

4. Build `session.Config` with `Kind: session.KindLocal`, `PaneID`,
   `Parent`, `Enhanced: true`, `Sandbox: sandboxReq`, and — for sandbox only —
   `Cwd = sandboxReq.Workspace`.

Phase 1 — `h.op.Prepare(...)`: unchanged; SSH resolve only, `[config, session]`
gates, no sandbox work.

Phase 2 — `h.op.Dial(...)`: `svc.Open(ctx, cfg)`. For a sandbox request, the
PTY factory runs the native prepare (Linux helper re-exec / macOS
`sandbox-exec`) and the post-enforcement readiness handshake; the session
registers **only after** native readiness (unchanged from the PR). The session
identity `{sessionId, instanceId, sessionEpoch}` is minted by the registry
before native prepare, and `sandbox.Request.Identity` is bound to exactly that
incarnation before `pty.NewPTY` runs.

Result: `openResult` gains `Sandbox *sandbox.SessionInfo json:"sandbox,omitempty"`
and `workspaceId`/`parent` stay required (see §2.4).

**Why the ephemeral mark is written at creation, not at open.** The open
handler already runs inside the open operation holding `[config, session]` and
the execution lane; the layout store's _read_ seam was deliberately chosen over
the gated `LayoutOperation` to avoid a second lane permit (the deadlock is
documented in `openHandlers`). Marking the pane at open-success time would
either deadlock (acquiring the layout write inside the gate) or leave a crash
window in which a sandboxed session exists while its pane is still restorable.
Writing the flag at creation closes both: the row is non-restorable from the
first instant it exists, and the open gate (§2.2 phase 0.g) makes a sandbox
session _only_ expressible in a non-restorable pane.

**Close.** `closePane`/`panes.close`/`tabs.close`/`workspaces.close` are
untouched. A closed sandbox pane is already marked `closed_at` and is therefore
outside the window set and outside the startup sweep. Its row (and its blocks'
`entries.pane_id` anchor) persists for history — the `ephemeral` flag changes
nothing about the close path.

**Startup sweep (the restore invariant, backend-owned).**

- New method on `content.LayoutRepository`:

  ```go
  // CloseEphemeralPanes marks every OPEN pane that is ephemeral (a sandboxed
  // local shell) CLOSED, in one transaction, unwinding the chain exactly like
  // every other close: a tab whose last open pane was ephemeral is marked
  // closed, and a workspace whose last open tab that left is dissolved
  // (minted replacement when the application is left empty). It is the
  // sandbox pane's "never restored" (nocx-6ftmj): an ephemeral pane must not
  // reach the window a renderer adopts.
  CloseEphemeralPanes(ctx context.Context) error
  ```

  Implementation in `internal/content/layout_sqlite.go` reuses `markTabClosed`,
  `dissolveTabIfEmpty`, `dissolveWorkspaceIfEmpty`, and `mintReplacementIfEmpty`
  — the same one-transaction unwind `DeletePane`/`DeleteTab` use. It never
  deletes the pane row (blocks stay anchored), only marks it closed.

- Composition root (`internal/app/app.go`): call
  `db.Layout().CloseEphemeralPanes(ctx)` **unconditionally**, immediately after
  `content.Open` and **before** `clearWindowOnCleanStart`, so it runs whether
  or not `restore.onStartup` is set and before the transport can serve the
  first `layout.read`. Failure is a `WARN`, same posture as
  `clearWindowOnCleanStart` (a store failure must not refuse to start; but see
  the defence-in-depth below, which keeps the renderer from adopting an
  ephemeral row even if the sweep failed).
- `clearWindowOnCleanStart` (`restore.onStartup` off) is unchanged and runs
  after it; `ClearWindow` is a superset that would have closed the ephemeral
  panes anyway, so the unconditional sweep matters only for the restore-on
  case, which is the case that was previously a privilege escalation.

**Renderer adoption defence-in-depth** (`frontend/src/panes.ts`, `adopt(row)`):
before the `kind === 'ssh'` branch, add

```ts
if (row.ephemeral) {
  // A sandboxed pane must never be adopted. The backend sweep normally closed
  // it before layout.read answered; reaching here means the sweep did not run,
  // and drawing a fresh local shell would be the exact escalation the flag
  // exists to prevent.
  return
}
```

This is not the owner of the invariant (the backend sweep is) — it is the
second, independent refusal, so a store failure that leaves an ephemeral row in
the window still cannot become an unsandboxed shell.

---

### 2.3 PaneManager API (exact)

`frontend/src/panes.ts`:

- **Add** `newSandboxedPane(workspace: string, launch: SandboxLaunch): Pane`.
- **Add** `Pane.sandboxed: boolean` (getter) and `Pane.setSandboxed(): void`
  (relocated from `Tab`).
- **Extend** `mintPane(kind: 'local' | 'ssh', endpoint: string | null,
ephemeral: boolean)` and its one caller `buildLocalPane` (and the sandbox
  caller) so the pane-create request carries `ephemeral`.
- **Extend** `adopt(row: PaneRow)` with the `row.ephemeral` early return.
- **No other change** to `boot()`, `readLayout()`, `renderFromLayout()`,
  `newPane()`, `newSSHPane()`, `closePane()`, or the MRU stack. The sandbox
  action is additive, not a new code path through the strip.

`frontend/src/main.tsx`: re-express the sandbox wiring against `PaneManager`:
`openSandboxedTab`/`getSandboxState`/`sandboxEnabled` are unchanged in logic;
`newSandboxedTab: (workspace, launch) => paneManager.newSandboxedPane(workspace,
launch)` replaces the `TabManager` call; the Quick Connect sandbox provider
stays; the strip's `setSandboxEnabled`/`onNewSandboxedTab` now gate and
dispatch the More-menu row (§2.11), not a dedicated strip glyph.

---

### 2.4 Request/result union (exact)

`contracts/open.schema.json` (union of main + PR):

- `required` = main's list **plus nothing dropped**:
  `["sessionId","instanceId","sessionEpoch","workspaceId","cwd","desiredMode","parent"]`.
- Add optional `"sandbox"` — the PR's exact `$defs` block (object,
  `additionalProperties: false`, required `["backend","workspace",
"writableRoots","readOnlyRoots","homeProjections"]`, `backend` enum
  `["landlock","seatbelt"]` — "unsupported" is only a `sandbox.status` value,
  never an open-result value). Absent for ordinary and SSH sessions.

Go DTO `internal/transport/ws_session_handlers.go`:

- `openResult` = main's fields (`SessionID`, `InstanceID`, `SessionEpoch`,
  `WorkspaceID`, `Cwd`, `DesiredMode`, `Parent`) **plus**
  `Sandbox *sandbox.SessionInfo json:"sandbox,omitempty"`.
- `openParams`/`openSandboxParams` decode from the PR (`sandbox` optional,
  `null` rejected).

Frontend `frontend/src/ipc.ts`:

- `OpenResult` gains `sandbox?: Open['sandbox']` (generated).
- `SandboxRequest`/`SandboxLaunch`/`SessionSandboxInfo` types carry over
  unchanged (they are already per-launch/per-session, tab-agnostic).

---

### 2.5 Two-phase backend control flow (exact)

`internal/transport/ws_session_handlers.go`, `handleOpen`, in this order:

1. `decodeOpenParams` (PR's strict streaming decoder) EXTENDED with two main
   cases — `"parent"` (decode `openParentParams`) and `"paneId"` (decode
   string → `p.PaneID`) — keeping the strict unknown-member/duplicate/
   trailing-data refusals; `sandbox` → `decodeOpenSandbox` stays, `enhanced`
   stays rejected (dev review D1). `-32602` on error.
2. `cols/rows` check — `-32602`.
3. `workspaceForOpen` (main) — `-32602` on resolve failure.
4. Sandbox pre-validation block (PR's logic, re-homed before `Prepare`):
   local-only → settings snapshot → enabled → revision → baselines →
   canonicalize workspace → **ephemeral pane gate**. Each maps to its code
   (§2.6).
5. `session.Config` (main + `Sandbox`, `PaneID`, `Parent`, `Enhanced`).
6. `h.op.Prepare` (main) — SSH resolve only.
7. `h.op.Dial` (main) — `svc.Open(ctx, cfg)`; sandbox native prepare +
   enforcement + readiness happen inside the PTY factory during this dial.
8. Error classification after `Dial` (merge main's `answerOpenFailure` with the
   PR's sandbox classification, §2.6).
9. Ring/discovery/ack path (main) unchanged; the ack now carries
   `workspaceId`/`parent` **and** `sandbox` when present.

`internal/session/session.go`:

- `Config` = main's fields (`Kind`, `Cols`, `Rows`, `XPixel`, `YPixel`,
  `Enhanced`, `ProfileID`, `CredentialID`, `Parent`, `PaneID`) **plus**
  `Sandbox *sandbox.Request`.
- `Session` interface = main's methods **plus** `SandboxInfo()
*sandbox.SessionInfo`.
- `realSession` = main's fields (`parent`, `paneID`, `liveness`…) **plus**
  `sandboxInfo *sandbox.SessionInfo`, populated from
  `pty.SandboxInfoProvider` after native readiness; `SandboxInfo()` returns
  `s.sandboxInfo.Clone()`.
- In the local branch, when `cfg.Sandbox != nil`, copy the request, bind
  `Identity = {SessionID: string(id), InstanceID: string(r.instanceID), Epoch:
epoch}` (minted before native prepare), and pass through `pty.Config.Sandbox`.

`internal/pty` (PR already has this): `Config.Sandbox *sandbox.Request`,
`SandboxInfoProvider` interface, and the sandboxed `NewPTY` path. These are
imported as-is; no relocation needed — they were already pane/session-shaped,
not tab-shaped.

---

### 2.6 Error ownership (exact)

The **transport** (`openHandlers`) is the single owner of wire-code mapping;
`internal/sandbox` only ever returns typed sentinels whose internal `error`
values may carry paths for diagnosis, and the transport never logs or echoes
those paths.

| Condition                                                                                                                                                              | Wire code | `data.reason`       |
| ---------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------- | ------------------- |
| decode failure, cols/rows, local-only boundary, revision mismatch, canonicalize-workspace failure, pane not ephemeral / no paneId, post-launch `ErrInvalidPermissions` | `-32602`  | path-free           |
| `h.settings == nil` or `sandbox.enabled` false                                                                                                                         | `-32005`  | `feature-disabled`  |
| `sandbox.StatusError`                                                                                                                                                  | `-32006`  | the status `reason` |
| settings snapshot error, baselines error, `sandbox.SetupError`                                                                                                         | `-32007`  | `setup-failed`      |

`-32010/-32011/-32012` are already capture codes (`ws_capture.go`:
capture-expired / capture-consumed / capture-save-failed); the project rule
"two error classes may not share one code" forbids reuse, and `-32005..-32007`
are free in main (dev review D2).

Merging with main: `handleOpen`'s post-`Dial` `err` branch classifies
`sandbox.StatusError` / `sandbox.SetupError` / `sandbox.ErrInvalidPermissions`
**before** the existing `answerOpenFailure` gate/lineage/vault/host-key
taxonomy; `answerOpenFailure` keeps its existing `-32602` lineage refusals and
`-32603` everything-else mapping. Logging uses the status's `backend`/`reason`/
`abi` fields only, never a path.

The denied-access inbox (`sandbox.access.*`) and `sandbox.status` are
transported through the PR's `ws_sandbox.go`/`ws_sandbox_access.go` verbatim;
their error/reason surfaces are already path-free and do not change.

---

### 2.7 Generated contracts and baseline artifacts

- **Schemas → TS:** `npm run contracts` regenerates
  `frontend/src/generated/open.ts` (union), `panes.create.ts`,
  `tabs.create.ts`, `workspaces.create.ts`, `panes.move.ts`, `panes.setCwd.ts`,
  `layout.read.ts` (the `ephemeral` field), and the six sandbox files brought
  over from the PR
  contracts (`sandbox.status`, `sandbox.access.changed`,
  `sandbox.access.list`, `sandbox.access.resolve`, `sandbox.access.status`,
  `dialog.openDirectory`). Never hand-edit any generated file.
- **`contracts/*.schema.json`:** six sandbox schemas + `dialog.openDirectory`
  are added to `contracts/` from the PR; `open.schema.json` and
  `panes.create.schema.json` are reconciled by hand (the only two schema edits
  this design makes).
- **`go.mod`/`go.sum`:** `go mod tidy` (adds
  `github.com/landlock-lsm/go-landlock v0.9.0`). Never hand-merge `go.sum`.
- **`.beads/issues.jsonl`:** regenerated via `bd dolt pull` (AGENTS.md /
  `.internal/HANDOFF`); never resolved by hand. This is a _deferred_ step — it
  must run after the merge, when the backlog in Dolt has the post-merge bead
  set.
- **`frontend/package.json.md5`** and
  **`frontend/lint-fixtures/dead-exports-baseline.json`:** regenerated (the md5
  by the lockfile step, the baseline by the lint baseline command); never
  hand-edited.

---

### 2.8 ADR renumber mapping

| PR (old)                                                     | New                                                          | Note                                   |
| ------------------------------------------------------------ | ------------------------------------------------------------ | -------------------------------------- |
| `0033-native-per-tab-filesystem-sandbox.md`                  | `0036-native-per-tab-filesystem-sandbox.md`                  | —                                      |
| `0034-sandbox-writable-allowlist-and-launch-overrides.md`    | `0037-sandbox-writable-allowlist-and-launch-overrides.md`    | kept, superseded in part by 0038       |
| `0035-sandboxed-opencode-is-a-fixed-authenticated-launch.md` | `0038-sandboxed-opencode-is-a-fixed-authenticated-launch.md` | superseded by 0039's interactive shell |
| `0036-sandbox-two-class-directory-grants.md`                 | `0039-sandbox-two-class-directory-grants.md`                 | —                                      |
| `0037-sandbox-launches-an-interactive-shell.md`              | `0040-sandbox-launches-an-interactive-shell.md`              | —                                      |
| `0038-sandbox-denied-access-inbox.md`                        | `0041-sandbox-denied-access-inbox.md`                        | —                                      |
| `0039-explicit-home-grants-project-into-isolated-home.md`    | `0042-explicit-home-grants-project-into-isolated-home.md`    | —                                      |

Main `0033-ui-state-is-a-document-not-a-setting.md` and
`0034-consent-belongs-to-the-machine-not-the-connection.md` are unchanged.
Every cross-reference inside the seven renumbered ADRs is re-pointed to the new
numbers (the +2 shift is mechanical and complete — no `0033`/`0034` reference
survives pointing at a sandbox ADR). `docs/architecture.md` adds the PR's
**AD-11** (after main's AD-10) with its link list re-pointed to
`0035, 0036, 0038, 0039, 0040, 0041`, and the sentence "ADR-0040 supersedes
ADR-0038's fixed-OpenCode launch target" (the old "0037 supersedes 0035").

---

### 2.9 Tracker regeneration approach

`.beads/issues.jsonl` is a passive, generated export of the Dolt backlog. The
resolution sequence is:

1. Complete the source merge (the `UU` on `.beads/issues.jsonl` is not
   resolved by hand — it is left conflicted or deleted and regenerated).
2. `bd dolt pull` (the command AGENTS.md and `.internal/HANDOFF` name) after
   the merge commit, so the JSONL restates the post-merge bead set including
   `nocx-6ftmj`.
3. Verify the export is a clean regenerate (no hand edits) before staging.

The design records this as a **checkpoint in the merge**, not a source edit.

---

### 2.10 Test assertions (observable)

Reuse A1–A13 from the analysis; add the pane-specific assertions that pin this
design:

- **T1 — flag persists:** `panes.create` (and `tabs.create`/`workspaces.create`
  first pane) with `ephemeral: true` returns a pane with `ephemeral: true`;
  `layout.read` returns it.
- **T2 — authorization:** `ephemeral: true` on `kind: "ssh"` is refused
  `-32602`; `ephemeral` on an ordinary local pane is `false` by default and is
  the only value the ordinary `newPane` path sends.
- **T3 — open gate:** a sandbox `open` naming a non-ephemeral pane (or no
  `paneId`) is refused `-32602` with no session registered and no spawn.
- **T4 — sweep:** seed one open ephemeral pane beside one open ordinary pane;
  `CloseEphemeralPanes` marks the ephemeral pane (and, when it was the last,
  its tab and workspace) closed and leaves the ordinary pane untouched; the
  pane row survives (block anchor intact).
- **T5 — restore:** after the sweep, `layout.read` does not include the
  ephemeral pane; the renderer's `adopt` never calls `buildLocalPane` for a
  row with `ephemeral: true`.
- **T6 — union:** a sandboxed open result carries `sandbox` plus
  `workspaceId` + `parent`; an ordinary and an SSH open carry `workspaceId` +
  `parent` and **no** `sandbox` key (schema `additionalProperties: false`
  plus the DTO `omitempty`).
- **T7 — identity order:** for a sandboxed open, the backend mints
  `{sessionId, instanceId, sessionEpoch}` before native prepare and the
  denied-access recorder binds that exact incarnation.
- **T8 — fail closed:** disabled flag → `-32005`; unavailable backend →
  `-32006` with `data.reason`; setup/policy failure → `-32007`; malformed →
  `-32602`; each leaves no registered session and no unsandboxed fallback.
- **T9 — privacy:** no user path/argv/env/PTY byte appears in any sandbox error
  reason, log, inbox row, or promotion request (the transport strips paths from
  every sandbox sentinel it maps).

### 2.11 UI kit corrections (binding from UX review)

The UX review (`nocx-6ftmj-sandbox-main-integration-ux-review.md`, PASS with
corrections) binds these UI changes — all additive kit-level carry-overs, no new
pattern:

1. **Shield marker tooltip** (§3.1). The `data-sandboxed` marker span carries
   `aria-label="Sandboxed"` **and** `title="Filesystem-isolated"` — the kit's
   native-tooltip pattern (ADR-0014), matching the warning mark's
   `aria-label`+`title` pairing. (`frontend/src/tab.tsx`; §2.2 step 5.)
2. **Select `ariaLabel`** (§3.2). Main's `Select`
   (`frontend/src/ui/select.tsx`) gains `ariaLabel?: string` in `SelectProps`,
   applied to the native `<select>` — the PR's `Select` already does both; the
   access-settings consumer (`ariaLabel="Filter by application"`) is the only
   one at merge.
3. **Settings `control: 'paths'`** (§3.3). Main's settings-domain control union
   gains `'paths'` (`frontend/src/settings-domain.ts`), and the `paths` control
   renderer (the PR's `EditableRowList` block — `addLabel="Add folder"`,
   `emptyLabel="No folders — the sandboxed tab is limited to its workspace."`,
   `removeLabel`) carries into `frontend/src/settings.tsx`. `dataClass:
'privateMetadata'` is already supported by main.
4. **Permissions dialog copy** (§3.5). The primary footer button becomes
   **"Open sandboxed shell"** (was "Open sandboxed tab"), consistent with the
   Quick Connect action label. (`frontend/src/sandbox-permissions-dialog.tsx`.)
5. **Access settings loading** (§3.6). The loading state reuses the kit
   `Spinner` (the one loading indicator) in place of `EmptyState title="Loading
sandbox access"`, if it fits without introducing a second loading pattern.
   (`frontend/src/sandbox-access-settings.tsx`.)

No other UI change: Quick Connect action, permissions dialog kit usage,
Settings → Developer → Sandbox access placement, workspace membership, badge
tones, and post-restart absence are all PASS per the review.

---

## 3. Rejected alternatives and why each fails

1. **`restoreDescriptor`-only (frontend, non-persisted).** The PR expressed the
   invariant as `restoreDescriptor: null` on a `ContentDescriptor`. In the
   PaneManager world `adopt()` derives the descriptor from the **persisted**
   `kind` (`ssh` → reconnect, otherwise → `buildLocalPane`); a frontend-only
   descriptor is not in the chain, so after restart a `kind: "local"` pane has
   no record that it was sandboxed and is reopened unsandboxed. Fails A3. The
   persisted `ephemeral` flag is the fix: it is the thing `adopt` reads from the
   row, not from memory.
2. **Omitting `paneId` (no durable identity).** Sandbox must not drop the
   pane's `paneId`/`workspaceId`/history anchor; the contract requires them
   while live and a closed row for history. Omitting `paneId` would break block
   anchoring (`entries.pane_id`), the `open` pane→tab→workspace walk, and the
   `secrets.paneClosed` correlation. Fails the durable-identity contract and A2.
3. **`kind: "sandbox"` (overloading `PaneKind`).** `kind` already has exactly
   two values with a single meaning — "where the pipe goes" — and is the field
   `adopt()` branches on; the schema `CHECK (kind IN ('local','ssh'))` and the
   `$defs/pane` enum enforce it. Adding `sandbox` makes one field own two facts
   (pipe direction AND restorability), breaks the binary `adopt` branch, makes
   the renderer the author of "sandbox" via the create request, and contradicts
   the owner's "explicit property rather than overloading local kind". Fails
   single-owner (AD-8).
4. **Renderer cleanup (renderer closes ephemeral panes on startup).** The
   renderer is not the chain owner (AD-8); it draws what the backend answers. A
   renderer sweep would be N close transactions (the exact defect
   `clearWindowOnCleanStart` documents), would mint a replacement tab on the
   last close, and — critically — runs after `layout.read` has already served
   the ephemeral rows, so it would have to adopt-then-drop (opening a shell for
   a turn, the same harm the clean-start `readLayoutWithoutAdopting` exists to
   avoid). Fails the "closed/excluded **before** renderer adoption" contract.
5. **Marking the pane ephemeral at open-success time (post-open write).** This
   is the only alternative that is truly "backend-authored from enforcement",
   but it is rejected for two concrete reasons: (a) the open handler holds
   `[config, session]` + the execution lane and may not acquire the gated
   layout write without the deadlock `openHandlers` documents; (b) it leaves a
   crash window in which a sandboxed session exists while its pane is still
   restorable. Creation-time marking plus the open gate (§2.2 phase 0.g) closes
   both with no deadlock and no window.

---

## 4. Rollback boundaries

- **Schema/flag:** the `ephemeral` column is additive with default `false`.
  Reverting the feature = stop sending `ephemeral: true` from the sandbox
  action and renumber/remove the ADRs; no persisted row with `true` can be
  produced again, and any already-written `true` rows are already closed by the
  sweep. The `schemaVersion` bump (11→12) is a one-way rebuild; a rollback of
  the _schema_ is a `git revert` of the bump (older binaries rebuild again —
  history loss is already the store's documented behaviour, not a new hazard).
- **Open contract:** `sandbox` is optional and `omitempty`; removing the sandbox
  half restores the byte-identical main result. `workspaceId`/`parent` are
  never dropped, so the ordinary path is untouched in both directions.
- **Session:** `Sandbox` on `Config` and `SandboxInfo()` on `Session` are
  additive; removing them is a delete of the field, its one populator, and its
  one consumer, with no behavioural change to ordinary/SSH sessions.
- **Startup sweep:** `CloseEphemeralPanes` only affects panes with
  `ephemeral = true`; with no such panes it is a no-op. Removing the feature
  leaves it inert. The clean-start `ClearWindow` path is untouched.
- **What is NOT a rollback boundary:** the ADR renumber is a content move
  (`git mv` + re-point); undoing it is a rename back, with no code dependency.
  `.beads/issues.jsonl` is regenerated, so "rolling it back" is another
  `bd dolt pull`.

---

## 5. Security review

- **Never-unsandboxed invariant (A3, the core).** Enforced by three independent
  layers: (1) the pane row is born `ephemeral: true`; (2) the sandbox `open`
  gate refuses a sandbox request on a non-ephemeral pane, so a sandbox session
  cannot exist in a restorable pane; (3) the backend startup sweep closes every
  open ephemeral pane before the first `layout.read`, and the renderer's
  `adopt` independently refuses an ephemeral row. A renderer can only ever make
  the outcome _more_ conservative (drop a pane on restart), never _less_
  (restore a sandbox pane unsandboxed).
- **Renderer never authors policy (A4).** The `open.sandbox` request carries
  exactly `{workspace, settingsRevision, addWritable, removeWritable,
addReadOnly, removeReadOnly}`; baselines, effective roots, Git/runtime roots
  and native clauses stay backend-composed. The `ephemeral` flag is a
  restorability fact, not a policy fact — it grants no access and changes no
  root.
- **Fail closed (A6).** The `-32005/-32006/-32007/-32602` mapping is owned by
  the transport and each path leaves no registered session. A session registers
  only after native readiness (Landlock restriction in the helper / Seatbelt
  shim acknowledgement), unchanged from the PR.
- **Session identity order (A8).** `{sessionId, instanceId, sessionEpoch}` is
  minted by the registry before native prepare; `sandbox.Request.Identity` is
  bound to that exact incarnation, and the denied-access recorder correlates by
  it, never by path or PID.
- **`workspaceId` is provenance (A7/§7).** Nothing new reads authority from it:
  the ephemeral gate reads the **pane** flag, not the workspace; the sweep
  closes by pane flag; policy is workspace-agnostic except for the canonical
  workspace path already in the PR.
- **Denied-access inbox (A9/A13).** In-memory, bounded, `PrivateMetadata`,
  path-free on the wire; promotions resolve only a backend-minted event ID and
  atomically append to the latest baseline without mutating running sessions.
  Observer is separate from Landlock enforcement.
- **Privacy.** No sandbox sentinel crosses the log/wire boundary with a user
  path, argv, env, command text, PTY byte, or secret. Home projections are
  discoverability aliases, not authorization.
- **Host agent state stays outside the cage.** Ephemeral HOME/XDG only; the
  design introduces no new copy/read/grant of host OpenCode credentials, DB,
  logs, plugins, or config.

---

## 6. Acceptance traceability

| Analysis criterion               | Design section                                       |
| -------------------------------- | ---------------------------------------------------- |
| A1 action identity               | §2.2 creation (Quick Connect, `__sandboxed_local__`) |
| A2 pane not tab                  | §2.3 PaneManager relocation                          |
| A3 never unsandboxed on restore  | §2.1 flag, §2.2 sweep, §2.3 `adopt`, T4/T5           |
| A4 renderer never authors policy | §2.2 phase 0, §2.4, §5                               |
| A5 backend composes/enforces     | §2.2 phase 2, §2.5, T6                               |
| A6 fail closed                   | §2.6, T8                                             |
| A7 open contract union           | §2.4, T6                                             |
| A8 session identity order        | §2.2 phase 2, §2.5, T7                               |
| A9 denied-access inbox           | §2.7 (unchanged PR surfaces), §5                     |
| A10 ADR integrity                | §2.8                                                 |
| A11 generated artifacts          | §2.7, §2.9                                           |
| A12 ordinary path untouched      | §2.1 default `false`, §2.4 `omitempty`               |
| A13 privacy                      | §2.6, §5                                             |

---

## 7. Exact change index

**Backend**

- `internal/content/sqlite.go` — `schemaVersion` 11→12; `panes` table +
  `ephemeral` column.
- `internal/content/layout.go` — `Pane.Ephemeral bool`; `LayoutRepository` +
  `CloseEphemeralPanes` and `IsPaneEphemeral`.
- `internal/content/layout_sqlite.go` — `paneFields`, `insertPane`, `paneByID`,
  `Panes()` (inline scan); `CloseEphemeralPanes` and `IsPaneEphemeral`
  implementations.
- `internal/content/stub.go` — `layoutStub` gains the two no-op methods (D6).
- `internal/transport/ws_layout_handlers.go` — `paneWire`, `firstPaneParams`,
  `paneCreateParams`, `validatePaneFacts`, `validateFirstPane`,
  `validatePaneCreateRaw` + `ephemeral`.
- `internal/transport/ws.go` — `openParams.Sandbox *openSandboxParams` +
  `openSandboxParams`; `decodeOpenParams`/`decodeOpenSandbox` extended with
  `parent`/`paneId` cases (D1).
- `internal/transport/ws_session_handlers.go` — `paneWorkspaces` seam +
  `IsPaneEphemeral`; `openHandlers.settings`, `openResult.Sandbox`,
  `openParams`/`openSandboxParams`, `handleOpen` phase-0 sandbox block +
  ephemeral gate + error classification (`-32005/-32006/-32007`).
- `internal/transport/ws_sandbox.go` + `ws_sandbox_access.go` —
  `sandbox.status` + `sandbox.access.*` inbox (verbatim).
- `internal/session/session.go` — `Config.Sandbox`, `Session.SandboxInfo`,
  `realSession.sandboxInfo`, local-branch identity binding.
- `internal/sandbox/**` — the whole package (source + 16 test files) verbatim
  from the PR, re-pointed.
- `internal/pty/*` — `Config.Sandbox *sandbox.Request`, `SandboxInfoProvider`,
  `WithSandboxService`, sandboxed `NewPTY` path.
- `internal/settings/settings.go` — `SandboxEnabled`,
  `SandboxAllowedWritablePaths`, `SandboxAllowedReadOnlyPaths` (keys
  `sandbox.enabled` / `sandbox.allowedWritablePaths` /
  `sandbox.allowedReadOnlyPaths`).
- `internal/app/app.go` — `CloseEphemeralPanes` call at startup (before
  `clearWindowOnCleanStart`); sandbox service construction
  (`sandbox.NewAccessInbox`, `sandbox.NewWithAccess`, `sandboxGrantStore`,
  `pty.WithSandboxService`, `transport.WithSandboxService`) + sandbox branch in
  `newLocal`.
- `main.go` — `sandbox.MaybeHelper()` / `sandbox.MaybeArtifactSmoke()`
  early-exit; `dialog.openDirectory` (`OpenDirectory`).
- `contracts/open.schema.json`, `contracts/panes.create.schema.json` — union
  and `ephemeral`; six sandbox schemas + `dialog.openDirectory` added.
  `panes.setCwd.schema.json` is also a `$defs/pane` referencer (no edit, but
  `panes.setCwd.ts` regenerates; D5).

**Frontend**

- `frontend/src/panes.ts` — `newSandboxedPane`, `Pane.sandboxed`/
  `setSandboxed`, `mintPane` ephemeral arm, `adopt` guard.
- `frontend/src/layout/layout-store.ts` — `NewPane.ephemeral`, `paneFacts`.
- `frontend/src/layout/layout-client.ts` — `PaneFacts.ephemeral`.
- `frontend/src/ipc.ts` — `OpenResult.sandbox`; sandbox wire types.
- `frontend/src/main.tsx` — PaneManager sandbox wiring; strip
  `setSandboxEnabled`/`onNewSandboxedTab` → More-menu row.
- `frontend/src/tab-strip.tsx` — More-menu "New sandboxed tab" row (ShieldIcon,
  gated by `sandbox.enabled`); no dedicated strip glyph.
- `frontend/src/tab.tsx` — `data-sandboxed` marker + `title` tooltip.
- `frontend/src/ui/select.tsx` — `ariaLabel` prop on `Select`.
- `frontend/src/settings-domain.ts` + `settings.tsx` — `control: 'paths'` type
  - `paths` renderer (`EditableRowList`).
- `frontend/src/sandbox-open.ts`, `sandbox-permissions-dialog.tsx` (primary →
  "Open sandboxed shell"), `sandbox-access-settings.tsx` (kit `Spinner`),
  `quick-connect.tsx`, `settings*.ts(x)`, `styles/surfaces/settings.css`
  (`sandbox-access-*` classes) — carry over from PR, re-pointed to
  PaneManager/settings surfaces.
- Generated: `open.ts`, `panes.create.ts`, `tabs.create.ts`, `workspaces.create.ts`,
  `panes.move.ts`, `panes.setCwd.ts`, `layout.read.ts`, `sandbox.status.ts`,
  `sandbox.access.*.ts`, `dialog.openDirectory.ts`.

**Docs**

- `docs/decisions/` — seven sandbox ADRs `git mv` +2, re-pointed.
- `docs/architecture.md` — add AD-11, re-point its ADR links.

**Tracker/artifacts**

- `.beads/issues.jsonl` — `bd dolt pull` (deferred to post-merge).
- `frontend/package.json.md5`, `frontend/lint-fixtures/dead-exports-baseline.json`
  — regenerated.
- `go.mod`/`go.sum` — `go mod tidy`.
