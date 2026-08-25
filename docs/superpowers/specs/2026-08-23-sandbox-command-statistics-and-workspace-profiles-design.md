# Sandbox command, statistics, and workspace profiles design

**Status:** Approved design  
**Beads:** `nocx-a0qhd.2`, `.3`, `.4`, `.5`, `.7`  
**Amends:** ADR-0020, ADR-0039, ADR-0041, ADR-0043

## 1. Problem

PR #91 now stores the cause of sandbox non-restorability as a pane-subject `sandbox_grants` row and exposes one mouse entry through the activity-bar shield. Three connected gaps remain:

1. A keyboard user cannot request the same conversion from the command editor.
2. Denied-access diagnostics are buried inside Settings, although they are operational state rather than configuration.
3. The two standard path lists are the only reusable defaults, so unrelated workspaces share one policy.

The extension must preserve AD-1, AD-6, AD-7, AD-9, and AD-11: PTY bytes never enter JSON; the backend never sniffs the stream; session identity stays server-authoritative; retried writes remain idempotent; and the backend alone composes and enforces policy.

## 2. Product decisions

- The exact trimmed command `/sandbox` toggles the active terminal by replacement. It has no arguments.
- Applying always shows the permissions dialog, pre-filled from the effective profile. A durable profile is a default, not standing authority.
- Removing uses the existing ordinary-shell replacement path and requires no permissions dialog.
- Each named workspace has zero or one profile. A missing profile inherits the standard profile.
- Tabs in the default workspace use the standard profile.
- Denied-access promotion targets the event's workspace. A named workspace with no profile receives a copy-on-write profile initialized from the current standard profile. A default-workspace event updates the standard profile.
- A dedicated global activity-bar action opens a singleton Sandbox statistics tab. It is not a sidebar view and is distinct from the active-tab conversion shield.
- Settings keeps sandbox configuration, including the standard profile, but no longer contains the Sandbox access inbox.

## 3. Domain model

### 3.1 Profile versus grant

A profile is a durable, reusable default. A grant is authority issued to one pane incarnation.

```text
standard profile ─┐
                  ├─ effective workspace profile ── permission confirmation
workspace profile ┘                                + one-launch narrowing
                                                     │
                                                     ▼
                                             immutable pane grant
```

Moving a tab between workspaces does not mutate its live grant. Editing a profile does not mutate or restart live panes. Relaunch is always a new pane and a new grant.

### 3.2 Workspace payload

`workspaces.payload` already exists as the table's sparse extension; schema v12 does not change. `content.Workspace` does not currently expose it, so the repository gains profile-specific methods rather than placing private policy metadata in `layout.read`.

Payload namespace:

```json
{
  "sandboxProfile": {
    "schemaVersion": 1,
    "revision": 7,
    "writablePaths": ["/canonical/project"],
    "readOnlyPaths": ["/canonical/reference"]
  }
}
```

Invariants:

- `revision` is monotonic per workspace and is the optimistic-concurrency token.
- Paths are canonical existing directories, bounded by the current sandbox limits.
- Cross-class and protected-root rules are identical to current standard profile validation.
- Updating `sandboxProfile` preserves every unrelated payload key.
- Absence means fallback to the standard profile; it is not an empty policy.
- The default workspace does not materialize this payload key. Its profile remains the current typed settings values `sandbox.allowedWritablePaths` and `sandbox.allowedReadOnlyPaths`.

Selected storage approach: workspace payload JSON. Normalized profile/path tables would force a schema discard for one profile and two bounded lists. A separate document store would create a second writer for workspace-owned state.

### 3.3 Grant provenance

`sandbox_grants.payload` becomes a backward-tolerant envelope:

```json
{
  "realized": {
    "backend": "landlock",
    "workspace": "/canonical/project",
    "writableRoots": [],
    "readOnlyRoots": [],
    "homeProjections": []
  },
  "provenance": {
    "workspaceId": "workspace-id",
    "profileSource": "standard",
    "profileRevision": 42
  }
}
```

`profileSource` is `standard` or `workspace`. Decoding first inspects the top-level `realized` member: when present, decode the envelope strictly; when absent, decode the whole object as legacy `SessionInfo`. A legacy result exposes realized roots but sets provenance to `{profileSource: "legacy", profileRevision: null}`. The statistics tab labels it `Legacy grant — profile provenance unavailable` and offers no profile-staleness relaunch decision. Entry-point names are not persisted: shield and `/sandbox` mint identical authority, and recording UI behavior would add data without changing enforcement.

A newly written standard grant stores the settings revision it realized; a workspace grant stores its per-workspace revision. Only legacy grants report a null revision, so the statistics surface can detect stale grants for either current profile source.

## 4. Backend seams

### 4.1 Repository

`LayoutRepository` gains:

```go
type WorkspaceSandboxProfile struct {
    SchemaVersion int
    Revision      int64
    WritablePaths []string
    ReadOnlyPaths []string
}

WorkspaceSandboxProfile(ctx, workspaceID) (*WorkspaceSandboxProfile, error)
SetWorkspaceSandboxProfile(ctx, workspaceID, expectedRevision int64, profile WorkspaceSandboxProfile) (int64, error)
SandboxGrantForPane(ctx, paneID string) (*SandboxGrant, error)
```

`SetWorkspaceSandboxProfile` executes read → expected-revision check → payload-key replacement → write inside one `sqliteContent.run` operation. It preserves unrelated payload keys and refuses the default workspace; standard-profile writes continue through the settings store.

Validation is shared with standard-profile writes. One backend function canonicalizes, bounds, de-duplicates, and checks RO/RW conflicts before either store commits.

### 4.2 Effective profile service

An application service resolves:

```text
pane → tab → workspace
workspace profile present  → workspace source + workspace revision
workspace profile absent   → standard source + settings revision
```

The renderer never chooses `workspaceId`, profile source, or effective roots. It names the existing pane and sends the revision it displayed. The backend resolves the workspace chain again immediately before grant insertion.

### 4.3 RPC contracts

New strict contracts:

```text
sandbox.profile.get
  params: { paneId }
  result: {
    workspaceId,
    source: "standard" | "workspace",
    revision,
    inherited,
    writablePaths[],
    readOnlyPaths[]
  }

sandbox.profile.set
  params: { workspaceId, expectedRevision, writablePaths[], readOnlyPaths[] }
  result: { workspaceId, revision, writablePaths[], readOnlyPaths[] }

sandbox.grant.get
  params: { paneId }
  result: null | { issuedAt, realized, provenance }
```

`open.sandbox` adds nullable `profileRevision`. `settingsRevision` remains required for every launch because the same atomic settings snapshot owns `sandbox.enabled`; for standard-profile launches it also versions the two standard path lists. The backend derives the source after resolving pane → workspace. A standard-source request must carry `profileRevision: null` and is stale when `settingsRevision` differs. A workspace-source request must carry the exact per-workspace `sandboxProfile.revision` while still carrying `settingsRevision`; either mismatch is `-32602`. A workspace profile replaces standard roots; a missing workspace profile falls back whole to standard. This avoids two policy owners and an undocumented merge rule.

### 4.4 Denied-access provenance and promotion

`AccessSession` receives backend-owned `paneId` and `workspaceId` beside `SessionIdentity`. `AccessEvent` explicitly returns those identifiers in `sandbox.access.list`; this result is already PrivateMetadata and path-bearing. Notifications remain revision-only.

`handleOpen` already resolves pane and layout workspace before constructing the local session. It copies those backend-owned identifiers into JSON-excluded fields on `sandbox.Request`; `Service.Prepare` passes them to `AccessInbox.BeginSession`. They are provenance, not part of the server-authoritative `{sessionId, instanceId, sessionEpoch}` identity and never come from `open.sandbox`.

`AccessGrantStore` becomes workspace-aware internally:

```go
PromoteSandboxPath(workspaceID string, access AccessClass, path string) (revision int64, err error)
```

The RPC still accepts only backend-minted `eventId` plus decision. `Resolve` obtains `workspaceId` from the event; the renderer cannot redirect authority.

Promotion rules:

- default workspace → append to the standard profile through the existing settings writer;
- named workspace with profile → append to that profile at its current revision;
- named workspace without profile → atomically snapshot the standard profile, add the path, and create revision 1 workspace profile;
- missing/deleted workspace → reject path-free and leave the event resolvable only by dismiss.

Running grants never change.

## 5. `/sandbox` command

### 5.1 Typed interception

`/sandbox` is a nocx editor command, not shell syntax. A new parser recognizes only the exact trimmed, case-sensitive string. `/sandbox x`, `/Sandbox`, and embedded occurrences remain ordinary shell input.

The current boolean `beforeSubmit` seam can only veto while preserving the draft. It cannot express a successfully consumed internal command. Replace or extend it with a closed outcome:

```ts
type InternalCommandOutcome =
  { kind: 'notHandled' } | { kind: 'consumed' } | { kind: 'refused'; reason: string }
```

Editor behavior:

| Outcome      | Draft         | PTY/history/ledger | UI                |
| ------------ | ------------- | ------------------ | ----------------- |
| `notHandled` | normal submit | normal             | normal            |
| `consumed`   | clear         | none               | conversion starts |
| `refused`    | keep          | none               | reason toast      |

Recognition runs before secret resolution, shell planning, editor handoff, terminal bytes, and ledger creation.

### 5.2 Shared conversion controller

Extract `convertActiveTabToSandboxed` from `main.tsx` into `sandbox-convert.ts`. One controller owns:

- current eligibility and complete `SandboxStatus`;
- one in-flight guard shared by shield, `/sandbox`, and future relaunch;
- apply flow: profile snapshot → permissions dialog → sandbox pane → durable create → transcript install → strip replacement → source close;
- remove flow: ordinary pane → durable create → transcript install → strip replacement → source close;
- visible failure reporting and replacement cleanup.

Toggle state:

| Active surface                             | `/sandbox` result          |
| ------------------------------------------ | -------------------------- |
| ordinary local terminal, verified cwd      | apply with confirmation    |
| sandboxed local terminal, verified cwd     | remove by replacement      |
| non-terminal or SSH                        | refuse, keep draft         |
| cwd unverified                             | refuse, keep draft         |
| feature/backend unavailable while applying | refuse with backend reason |
| conversion already running                 | refuse as in-progress      |

No second conversion implementation is allowed in editor or statistics code.

## 6. Sandbox statistics surface

### 6.1 Shell placement

Add a global bottom-zone activity-bar action `sandbox-statistics`. It uses a visually distinct sandbox-report icon; reusing the selected conversion shield icon would make two different actions indistinguishable.

The action opens/focuses:

```ts
SURFACE_SANDBOX_STATISTICS = 'nocx.sandbox-statistics'
SINGLETON_SANDBOX_STATISTICS = 'nocx.sandbox-statistics'
```

`SandboxStatisticsContent` extends `SolidPaneContent` and is registered through `SurfaceRegistry`. Its descriptor has no restore descriptor. A second activation focuses the existing pane through the existing singleton-key behavior. The action is selected when that surface is active.

The existing top-zone shield remains the apply/remove action for the active terminal.

### 6.2 Information architecture

The singleton tab contains four sections:

Because opening a tab makes the statistics surface itself active, pane-scoped sections bind to the source terminal selected immediately before the activity-bar action. Activating the singleton again from another terminal retargets that same tab. Switching to another terminal hides statistics, so no stale context is visible. Relaunch first activates the bound source pane, waits for its activation callback, and then invokes the pane-checked conversion controller.

1. **Enforcement** — backend, available/unavailable reason, ABI, observer state.
2. **Source tab** — ordinary/sandboxed, grant issue time, workspace, profile source/revision, realized RO/RW/projection counts and explicit root lists.
3. **Workspace profile** — inherited standard or explicit workspace profile, edit action, stale/relaunch notice. Editing affects future grants only.
4. **Denied access** — the existing bounded inbox, lost counter, filter, dismiss, add read-only, and add read/write actions.

State table:

| State                             | Rendering                                                                                |
| --------------------------------- | ---------------------------------------------------------------------------------------- |
| loading                           | existing `EmptyState` loading treatment                                                  |
| backend unavailable               | persistent `StatusCard` with safe reason/detail; inbox may still render if available     |
| no bound terminal                 | source-tab section explains selection requirement; global status/inbox remain usable     |
| ordinary source tab               | effective profile shown; no grant section                                                |
| sandbox source tab                | realized grant and provenance shown                                                      |
| profile revision newer than grant | `Relaunch to use updated profile`; activates source, then delegates to shared controller |
| inbox empty                       | existing empty collection treatment                                                      |
| inbox lost > 0                    | persistent warning, not toast                                                            |
| RPC failure                       | section-local retry; other sections remain usable                                        |

Accessibility:

- Activity action participates in existing roving toolbar navigation.
- Sections use existing `PageSection`, `StatusCard`, `CollectionView`, and `RecordRow` grammar.
- Status is never conveyed by colour or icon alone.
- Focus moves to the singleton tab through normal pane activation; reopening preserves the existing tab rather than remounting a duplicate.

### 6.3 Settings clean cutover

Remove the `sandbox-access` page registration, navigation item, and `SandboxAccessClient` dependency from `SettingsContent`. Keep `sandbox.enabled` and the standard RO/RW profile settings. Re-home and rename `SandboxAccessSettings` as a reusable denied-access section owned by the statistics surface.

## 7. Profile editing UX

The Workspace profile section edits exactly one profile for the active named workspace.

- **Using standard profile:** show inherited values and `Create workspace profile`.
- **Explicit profile:** show revision, RO/RW lists, `Edit`, and `Use standard profile`.
- Creating copies the current standard profile before edits; this makes fallback-to-explicit transition visible.
- Deleting the explicit profile returns the workspace to live standard-profile inheritance and does not affect running grants.
- Saving uses `expectedRevision`; stale save reloads and asks the user to reapply edits.
- Path selection uses the native directory picker. No free-form unvalidated path becomes authority.
- The permissions dialog labels profile defaults separately from one-launch additions/removals. It never writes profile changes implicitly.

The default workspace has no visible workspace chrome; its standard profile remains editable in Settings and readable in the statistics tab.

## 8. Failure and concurrency rules

- Profile reads may use the read pool. Every profile mutation runs through the content writer goroutine and opens one SQL transaction; inside it the repository reads the current payload, checks `expectedRevision`, replaces only `sandboxProfile` in the decoded object, and updates the row. `sqliteContent.run` supplies writer ordering; the transaction supplies atomic read-modify-write.
- Immediately before helper start, issuance calls `settings.Registry.WithSnapshot` with the displayed settings revision. While that settings snapshot is locked, `InsertSandboxGrantIfCurrent` enters the content writer transaction, re-resolves the open pane's workspace, rechecks whether the workspace profile is absent or at the expected revision, and inserts the provisional grant. A tab move, standard-profile edit, workspace-profile edit/delete, duplicate launch, or pane close therefore refuses before process start; the check and insert cannot separate.
- Grant rollback after start/readiness failure and startup sweep remain unchanged.
- Promotion takes no renderer-supplied expected revision. For a named workspace, the application service reads the latest explicit profile or snapshots the standard profile for copy-on-write, appends and canonicalizes the path, then uses the repository's expected-revision compare-and-set. A concurrent profile edit causes a bounded retry from a fresh read; after three lost races the promotion refuses without resolving the event. Every individual write remains a serialized SQL transaction, so no partial profile is visible and neither successful update is lost.
- Closing/deleting a workspace invalidates later promotion for its stale events.
- Statistics RPC failures do not disable enforcement or widen policy.

## 9. Security and privacy

- Profiles are `PrivateMetadata`; paths appear only in explicit profile/grant/access RPC results.
- `sandbox.access.changed` stays path-free.
- `/sandbox` produces no PTY bytes, command entry, history item, or telemetry record.
- The backend validates profile source, workspace membership, revision, directories, class conflicts, and bounds.
- The renderer sends deltas and expected revision, never final effective roots or workspace provenance.
- No plaintext secrets, argv, environment, command text, or terminal content enter profile/grant statistics.
- Statistics are live product state, not telemetry: no durable counts, time series, rankings, export, cloud logging, or cross-restart denial history.

## 10. Test matrix

### Backend

- Profile absent falls back to standard; explicit profile replaces standard rather than merging.
- Profile payload preserves unrelated keys.
- Create/update/delete revision transitions and stale-write refusal.
- Canonicalization, bounds, protected roots, and RO/RW conflict refusal.
- Concurrent profile writes serialize without lost updates.
- Open rejects stale profile revision before prepare and records correct provenance after readiness.
- Grant payload legacy decoding remains valid.
- Promotion targets backend-owned workspace; copy-on-write creation includes the standard profile plus promoted path.
- Deleted workspace promotion refuses path-free.
- Startup sweep and provisional rollback remain green.

### Frontend

- Exact `/sandbox` parser table and no-PTY/no-ledger consumption.
- Consumed versus refused draft behavior.
- Apply/remove through one controller; shared in-flight guard.
- Confirmation always shown for apply, including saved profiles.
- Source preservation, replacement cleanup, transcript boundary, and close ordering.
- Singleton statistics dedup and activity-action selected state.
- Independent loading/error states for status, profile, grant, and inbox.
- Settings navigation no longer contains Sandbox access.
- Profile inherited/explicit/stale states and native-picker writes.

### End to end

- `/sandbox` apply opens pre-filled dialog and replaces the tab; a second `/sandbox` removes it.
- No literal `/sandbox` reaches the shell or command history.
- Statistics action opens one tab and focuses it on the second activation.
- Named workspace profile and standard fallback both mint expected grants.
- Denied-access promotion changes only the originating workspace's future grants.
- Restart closes sandbox panes while preserving workspace profiles.

## 11. Delivery order

1. Amend ADR-0020 and add ADR-0044; update AD-11.
2. Add workspace profile repository/application service and contracts.
3. Add grant/access provenance and workspace-aware promotion.
4. Extract shared conversion controller.
5. Add typed `/sandbox` interception.
6. Add singleton statistics surface and remove Settings access page.
7. Add profile editor and relaunch notice.
8. Run native enforcement, full CI, e2e, and security gates.

The order keeps the frontend from inventing profile or grant state before the backend owns it.
