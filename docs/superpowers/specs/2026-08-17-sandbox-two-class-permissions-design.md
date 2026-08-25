---
title: Sandbox two-class directory permissions
status: approved
created: 2026-08-17
bead: nocx-83oba
decision: ADR-0039
supersedes: 2026-08-16-sandbox-per-tab-permissions-design.md where ADR-0039 states
---

> **Amended 2026-08-18:** ADR-0040 and
> `2026-08-18-sandbox-shell-contract-restoration-design.md` supersede the
> fixed-OpenCode action/status wording only. The two-class directory contract
> and all policy, DTO, dialog, metadata, and validation decisions remain current.

# Sandbox two-class directory permissions

## 1. Scope

Extend the opt-in local filesystem sandbox with explicit user-selected directory classes:

- read-only: content may be read/traversed, never created, removed, renamed, or modified;
- read-write: content may be read and modified;
- mandatory workspace: always read-write.

Persisted baselines apply to future tabs. A pre-launch modal may subtract baseline entries and add ephemeral entries for one tab. Grants are immutable once the process starts.

The current filesystem-only boundary remains. Network, process, environment, IPC, device, credential, per-file, glob, preset, restore, live mutation, Windows, and host OpenCode-state bridging remain out of scope.

## 2. Binding invariants

1. Omitted `open.sandbox` means ordinary local. Present `null`, malformed, unknown, or oversized data is invalid and never degrades to ordinary local.
2. The backend is the sole policy author. The renderer sends workspace, one settings revision, and bounded class-scoped deltas only.
3. Both baselines are read from one atomic settings snapshot. Any revision mismatch fails before policy construction.
4. Workspace, Git common directory, runtime home/tmp, system/runtime roots, shell, and opencode executable are backend-derived.
5. Workspace, Git, and runtime home/tmp are mandatory read-write and cannot be removed or downgraded.
6. A requested read-only root may not be equal to or descend from an effective writable root. The backend rejects a grant the native additive policy cannot honor.
7. A writable child under a read-only ancestor is allowed and remains the only cross-class containment exception.
8. A session registers only after Landlock/Seatbelt readiness. Every failure is fail-closed; no unsandboxed retry.
9. Installed policy and `SessionInfo` are immutable deep copies.
10. User paths do not enter errors, stable reasons, or logs.
11. Both native backends consume the same validated `Policy`.
12. Bounds are enforced before unbounded work: 32 entries per persisted list and per delta array; 256 realized roots; 64 KiB serialized policy; 16,384 PATH entries/candidates; 1 MiB per ELF metadata section; 4 KiB per dynamic string; 256 strings per dynamic tag; 64 MiB aggregate ELF metadata; 65,536 dependency nodes/resolution probes; and dependency depth 64. Relative PATH entries are ignored rather than resolved against the backend working directory.

## 3. Settings model

### 3.1 Declarations

```text
key:         sandbox.allowedReadOnlyPaths
section:     Experimental
label:       Sandbox read-only folders
control:     paths
dataClass:   privateMetadata
default:     []
maxEntries:  32

key:         sandbox.allowedWritablePaths
section:     Experimental
label:       Sandbox read & write folders
control:     paths
dataClass:   privateMetadata
default:     []
maxEntries:  32
```

Descriptions state that entries apply to every **new** sandboxed tab and that the workspace is always read-write.

Both declarations use `PathListSpec`, `Registry.GetPaths`, and `Registry.SetPaths`. Candidate arrays are validated atomically: non-null string array, count bound, non-empty absolute path, no NUL/control rune, `Abs → EvalSymlinks → Stat(dir)`, canonical first-wins dedupe. Every single-setting, bulk, restore, and reset mutation validates the final pair and rejects a read-only path equal to or below a writable path before one commit. A failure writes nothing.

A stored path that disappears remains observable and blocks launch; silently dropping it would claim a policy was installed when it was not. Policy construction repeats cross-class validation from the atomic launch snapshot so legacy, corrupt, or filesystem-aliased persisted state still fails closed as a setup failure rather than applying writable-wins.

### 3.2 Settings UX

The generated Settings surface renders the two `paths` declarations as separate `EditableRowList` controls.

- **Add folder** opens the native directory picker and appends one canonical directory to the selected class.
- The user repeats the action for additional directories; prior rows remain.
- Remove sends the complete remaining array for that setting.
- Cancellation is a no-op.
- Backend validation failures use the setting row's existing error slot.
- No free-form path editor and no unbounded/multi-value text parser.
- A directory must not be kept in both lists. To change class, remove it from the old list, then add it to the new list.

## 4. Launch UX

Quick Connect → **Sandboxed opencode…** performs:

1. live sandbox/intent availability check;
2. one fresh settings snapshot containing both baselines and the revision;
3. native workspace picker;
4. **Sandbox permissions** modal;
5. strict open request after confirmation.

When `sandbox.enabled` is true, both tab-strip orientations place a shield action
immediately beside **New tab** (`+`). The action enters this same five-step flow;
it does not own a second launch path. The shield is absent while the setting is
false, and settings changes update the current strip live, including after an
orientation replacement.

The modal shows:

- **Workspace** — canonical picker result, “Read & write (required)”, no remove control;
- **Read-only folders** — checked persisted baseline plus ephemeral read-only additions;
- **Read & write folders** — checked persisted baseline plus ephemeral writable additions;
- section hint: changes apply only to this new tab;
- Cancel and **Open sandboxed tab** footer actions.

Each section owns one `EditableRowList` and one picker action. Persisted baseline entries occupy one visual row each and read top-to-bottom; paths never flow side-by-side. Picking a path already active in the same class is a no-op. Picking a path active in the other class produces visible feedback and no contradictory delta; the user removes/unchecks the old-class row first. Baseline uncheck becomes removal for that class. Removing an ephemeral row removes only its addition.

The dialog keeps the kit's keyboard, Escape, focus restoration, disabled-while-picker-open, and accessible list/remove labels. It introduces no new CSS vocabulary.

## 5. Strict request contract

```ts
interface OpenSandboxRequest {
  workspace: string
  settingsRevision: number
  addWritable?: string[]
  removeWritable?: string[]
  addReadOnly?: string[]
  removeReadOnly?: string[]
}
```

Rules:

- object required when `sandbox` is present; `null` rejected;
- `workspace` and integer `settingsRevision >= 0` required;
- each delta, when present, is a non-null array of at most 32 unique strings;
- unknown/duplicate members, wrong types, duplicate entries, and trailing data rejected;
- writable-only `add`/`remove` are removed, not aliased;
- SSH/profile/direct-host opens with `sandbox` are invalid;
- request contains no baseline, effective root, Git/runtime root, executable, command, or native clause.

The backend takes one `SettingsSnapshot`, checks `sandbox.enabled`, checks the exact revision, and deep-copies both path-list values. A missing/corrupt/over-count setting is `-32007`, never an empty fallback.

Internal request:

```go
type Request struct {
    Workspace      string
    GlobalWritable []string
    GlobalReadOnly []string
    AddWritable    []string
    RemoveWritable []string
    AddReadOnly    []string
    RemoveReadOnly []string
    RuntimeRoot    string // backend-only
}
```

## 6. Canonical composition

Every user path uses the same existing-directory pipeline:

```text
non-empty → no NUL/control → absolute → Abs → EvalSymlinks → Stat(dir)
```

Canonicalize all six class lists before comparison. Exact identity uses the canonical spelling fast path plus filesystem identity (`os.SameFile`) so case/normalization aliases dedupe first-wins, match exact removals, and collide across add/remove lists without turning exact removal into descendant removal.

Apply removals only to the corresponding canonical baseline. An unmatched removal, mandatory-root removal, or same-class add/remove collision is invalid. Removing a baseline path from one class and adding it to the other is a valid per-tab access change.

Compose writable roots in deterministic order:

```text
workspace
+ reciprocal Git common directory, if valid
+ GlobalWritable − RemoveWritable
+ AddWritable
+ runtime home
+ runtime tmp
```

Compose read-only roots in deterministic order:

```text
canonical system roots
+ GlobalReadOnly − RemoveReadOnly
+ AddReadOnly
+ PATH directories
+ executable/runtime loader roots
```

Then validate cross-class semantics on canonical paths:

- exact RW/RO identity: reject;
- RO equal to or below RW: reject;
- RW below RO: allow;
- writable candidate equal to or ancestor of a documented system read-only root: reject (existing execution-floor protection);
- same-class ancestor redundancy: allow and retain deterministic first occurrence.

`ValidatePolicy` repeats exact/conflicting-containment checks at the enforcement boundary. Final root count and serialized size remain bounded.

## 7. Native enforcement

### Linux Landlock

No new rule kind. The Linux helper maps `Policy.ReadOnlyRoots` to `landlock.RODirs` and `Policy.WritableRoots` to `landlock.RWDirs`. Helper re-exec, memfd/private-FD policy transfer, ABI floor/cap, `no_new_privs`, readiness pipe, environment stripping, and cleanup remain unchanged. Runtime dependency discovery scans absolute PATH entries only and parses ELF interpreter/dynamic metadata through the explicit per-section, per-string, aggregate-byte, node, depth, and filesystem-probe budgets above. Each ELF file closes before dependency recursion; exhaustion fails setup with a path-free error.

The enforcement probe must prove in one launched cage:

- reading a file inside a user read-only root succeeds;
- creating/appending/renaming/removing there fails;
- reading and writing inside a user writable root succeeds;
- an outside path remains denied.

### macOS Seatbelt

No new clause kind. Read-only roots render `file-read*` subpath clauses. Writable roots render both `file-read*` and `file-write*` subpath clauses. Profile escaping, deny-default base policy, post-profile re-exec shim, readiness acknowledgement, and cleanup remain unchanged.

The native macOS artifact smoke proves the same positive and negative verdicts after real `sandbox-exec` enforcement. Darwin cross-compilation proves source portability but is not enforcement evidence.

## 8. Result metadata

`open` gains required immutable metadata:

```ts
interface SessionSandboxInfo {
  backend: 'landlock' | 'seatbelt'
  workspace: string
  writableRoots: string[]
  readOnlyRoots: string[]
}
```

`writableRoots` and `readOnlyRoots` are full deep copies of installed `Policy` slices after readiness. The renderer never derives them from its request. Ordinary and SSH results omit `sandbox`.

The tab tooltip identifies backend and shows both classes. UI may line-break/abbreviate display, but the session object retains the complete authoritative arrays.

## 9. Errors

| Condition                                                                              |   Code | Stable reason         |
| -------------------------------------------------------------------------------------- | -----: | --------------------- |
| feature off / missing settings service                                                 | -32005 | `feature-disabled`    |
| native backend unavailable                                                             | -32006 | backend status reason |
| invalid delta, removal, cross-class request conflict, stale revision                   | -32602 | omitted               |
| corrupt/vanished/conflicting persisted baseline, final policy/native/readiness failure | -32007 | `setup-failed`        |

All failures occur before session registration and carry no user path.

## 10. Verification contract

### Go

- declaration, canonical save, append, dedupe, count/type/path failure, persistence/reset/restore for both settings;
- common policy composition order for both classes;
- exact conflict, RO-below-RW rejection, RW-below-RO allowance, per-tab class change, unmatched removal, same-class add/remove collision;
- strict six-list bounds plus final policy bounds;
- strict JSON decode for four delta members and rejection of obsolete `add`/`remove`;
- one-snapshot revision gate and corrupt-baseline setup failures;
- `SessionInfo`/PTY/open result deep-copy both root arrays;
- Linux and macOS real enforcement smokes exercise read/deny-write/read-write/outside-deny.

### TypeScript/browser

- Settings renders both lists and repeated picker actions append without replacement;
- cancellation and picker errors remain no-op/visible;
- modal computes four exact deltas and prevents cross-class duplicate picks;
- request contains only workspace/revision/four deltas;
- tab launch copies all arrays; tooltip reports both realized classes;
- ordinary and SSH opens unchanged;
- browser flow finds both Experimental settings and the enabled Quick Connect action.

### Release

Focused Go/Vitest, contract generation/check, Linux built-artifact smoke, macOS native artifact smoke, browser-driven flow, Wails production build and launch smoke, `make ci`, `gosec ./...`, and root/frontend dependency audits.
