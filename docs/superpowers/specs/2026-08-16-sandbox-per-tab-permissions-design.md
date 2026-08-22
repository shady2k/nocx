---
title: Sandbox writable allowlist and per-tab permission overrides
status: approved
created: 2026-08-16
bead: nocx-y46q.14
supersedes: ADR-0036 clauses named by ADR-0037
---

> **Amended 2026-08-18:** ADR-0040 and
> `2026-08-18-sandbox-shell-contract-restoration-design.md` supersede this document's
> fixed-OpenCode action copy, status-intent check, and confirmation label. The two-class
> permission model, strict DTO, revision gate, dialog, and native policy rules remain current.

# Sandbox writable allowlist and per-tab permission overrides

## 1. Scope

Extend the unmerged, opt-in filesystem sandbox with:

- a persisted global baseline of additional writable directories;
- launch-time additions and exact baseline removals for one new tab;
- one pre-launch permission dialog;
- identical policy composition on Linux Landlock and macOS Seatbelt.

The sandbox remains default-off, local-session-only, filesystem-only, and immutable after launch. Network, process, environment, IPC, device, credential, read-only user grants, per-file grants, globs, presets, policy restore, and live mutation are non-goals.

## 2. Invariants

1. Only a present, valid `sandbox` object opts in. Omitted means ordinary local; `null` is invalid.
2. Backend code is the sole policy author. The renderer sends workspace plus bounded deltas, never policy roots.
3. The global baseline is read atomically from the settings registry at launch.
4. A settings revision mismatch fails rather than applying a grant the dialog did not display.
5. One canonical workspace drives `cmd.Dir`, policy input, session CWD, and result metadata.
6. No sandbox request ever falls back to an ordinary shell.
7. A session is registered only after native enforcement readiness is acknowledged.
8. Effective policy and `SessionInfo` are immutable deep copies.
9. Running tabs never change when settings or the feature flag change.
10. User paths never enter logs, status reasons, or generic wire errors.
11. Runtime home/tmp remain mandatory writable roots and cannot be removed.
12. Both backends consume the same validated `Policy` document.

## 3. Persisted setting

### 3.1 Declaration

```text
key:         sandbox.allowedWritablePaths
section:     Experimental
label:       Sandbox writable allowlist
control:     paths
dataClass:   privateMetadata
default:     []
maxEntries:  32
```

Description: “Additional folders available read/write in every new sandboxed tab. The workspace is always writable; changes affect new tabs only.”

### 3.2 Typed registry contract

Add `ControlPaths`, `PathListSpec`, `PathList`, `MustRegisterPathList`, `Registry.GetPaths`, and `Registry.SetPaths`. The descriptor remains the single declaration site; `settings.describe`, snapshot, persistence, change notifications, backup/restore, and reset use existing registry flows.

`SetPaths` and `ApplyValues` validate the entire candidate before one commit:

1. JSON value is an array of strings;
2. length ≤ 32;
3. each string is non-empty, absolute, and contains no NUL/control rune;
4. `filepath.Abs → filepath.EvalSymlinks → os.Stat` succeeds;
5. each target is an existing directory;
6. canonical duplicates collapse first-wins;
7. no write occurs on any failure.

On load, a recognized `paths` value is type-checked and count-bounded before entering registry state. A path that disappeared after a valid save remains visible but makes a sandbox launch fail closed; silently dropping it would hide that the requested baseline was not realized.

The shared `DocumentStore` enforces an 8 MiB limit on both read and write, matching the control-plane document ceiling. Oversized documents are rejected before unbounded allocation/JSON decode.

## 4. Renderer UX

### 4.1 Settings editor

Use `EditableRowList`:

- rows display canonical paths;
- row remove sends the complete remaining array with `settings.set`;
- **Add folder** calls `dialog.openDirectory`, appends the chosen absolute directory, then saves;
- cancel is a no-op;
- backend validation errors render in the row’s existing setting-error slot;
- no free-form incremental editor.

### 4.2 Launch dialog

Quick Connect → **Sandboxed opencode…** performs:

1. `settings.getSnapshot` and `sandbox.status`, including fixed launch-intent availability;
2. workspace `dialog.openDirectory`;
3. a **Sandbox permissions** modal initialized from `sandbox.allowedWritablePaths`;
4. baseline checkboxes (checked by default); unchecked entries become `remove`;
5. **Add folder for this tab** adds ephemeral entries; removing those rows removes the addition;
6. **Start sandboxed opencode** sends the request; Cancel creates nothing.

The modal says “Additional writable folders” and separately names the mandatory workspace. It does not pretend to enumerate backend-derived Git/runtime roots. The post-launch shield tooltip remains authoritative and lists the returned canonical `writableRoots`.

## 5. Launch DTO and strict decode

```ts
interface OpenSandboxRequest {
  workspace: string
  settingsRevision: number
  add?: string[]
  remove?: string[]
}
```

Rules:

- object required when member present; `null` rejected;
- `workspace` and integer `settingsRevision >= 0` required;
- `add`/`remove`, when present, are non-null arrays with ≤ 32 unique strings;
- unknown members, duplicate object keys, wrong types, and trailing JSON rejected;
- outer `open` params are strictly decoded; the obsolete `enhanced` renderer member is removed;
- SSH/profile/direct-host requests with `sandbox` are invalid.

The backend takes one `SettingsSnapshot`. It requires `snapshot.Revision == settingsRevision`, reads `sandbox.enabled` and `sandbox.allowedWritablePaths` from that same snapshot, and deep-copies the paths. Any intervening settings mutation rejects with `-32602` and asks the user to reopen the permission dialog.

## 6. Policy model

Internal request:

```go
type Request struct {
    Workspace string
    Global    []string
    Add       []string
    Remove    []string
}
```

These slices are backend-owned copies. `internal/sandbox` imports no settings package.

### 6.1 Canonicalization

Workspace, globals, additions, and removals use one existing-directory pipeline:

```text
non-empty → no NUL/control → absolute → Abs → EvalSymlinks → Stat(dir)
```

Workspace and request deltas failing validation map to `-32602`. A persisted global failing revalidation is backend state failure `-32007`. No path text crosses the wire or logs.

### 6.2 Composition

Canonical roots in order:

```text
workspace
+ reciprocal Git common directory, if valid
+ canonical(global) minus exact canonical(remove)
+ canonical(add)
+ runtime home
+ runtime tmp
```

Rules:

- removal must match one canonical global entry exactly;
- removals cannot target workspace, Git, additions, runtime home/tmp, or implicit roots;
- the same canonical identity in add and remove is invalid;
- dedupe is canonical first-wins;
- a narrower removal cannot carve a deny hole under a remaining writable ancestor; the UI does not claim otherwise;
- workspace/global/add candidates equal to or ancestors of documented system read-only roots are rejected;
- final policy still passes `maxRoots = 256` and `maxPolicyBytes = 64 KiB` after all system, runtime, device, and user entries are present;
- `WritableRoots` and result metadata are copied from the installed policy, never echoed input.

## 7. Native enforcement

### 7.1 Linux

Retain strict go-landlock configuration, ABI floor 3/cap 8, `PR_SET_NO_NEW_PRIVS`, helper re-exec, memfd policy, and readiness pipe. The helper applies the composed canonical policy before writing ready and `exec`ing the shell.

The threat model starts with the new shell and descendants. A separate already-unrestricted same-user process racing parent path replacement during setup is excluded; the design therefore does not claim O_PATH-fd identity binding across that actor. Paths are revalidated at the enforcement boundary. Pre-existing in-root hard links remain the documented Landlock limitation.

### 7.2 macOS

`exec.Cmd.Start` proves only that `/usr/bin/sandbox-exec` loaded, not that it applied the profile. Use a post-profile shim:

```text
sandbox-exec -p <profile> <nocx-executable>
  __sandbox-seatbelt-exec <status-fd> <shell> <shell-args...>
```

`sandbox-exec` applies Seatbelt before execing the nocx shim. The shim writes byte `0` to the inherited readiness pipe and then `unix.Exec`s the real shell. Profile rejection causes EOF/nonzero before the shim and no session is registered. Cleanup kills/waits the wrapper and removes the runtime root exactly once.

The deterministic SBPL renderer consumes the same composed policy. The packaged macOS smoke must prove that the shim can write the inherited pipe under deny-default and that a rejected profile yields a typed setup failure.

## 8. Errors

| Condition                                         |   Code | Stable data.reason    |
| ------------------------------------------------- | -----: | --------------------- |
| sandbox disabled                                  | -32005 | `feature-disabled`    |
| backend unavailable                               | -32006 | backend status reason |
| policy/setup/readiness/global-state failure       | -32007 | `setup-failed`        |
| malformed workspace/delta/revision/stale revision | -32602 | omitted               |

All failures occur before session registration. No unsandboxed retry.

## 9. Required behavioral tests

### Go

- path-list declaration, canonical save, dedupe, count/type/path errors, persistence/reset/restore;
- DocumentStore read/write bound;
- policy global/remove/add order and canonical identity;
- unmatched removal, add/remove conflict, protected-root ancestor, bounds;
- runtime home/tmp and Git remain mandatory;
- strict `open` JSON: null, unknown, duplicate, wrong type, oversized arrays;
- settings revision and atomic snapshot;
- canonical workspace reaches `session.Config.Cwd`;
- no session/PTY on every failure class;
- macOS wrapper shape, ready byte, EOF/failure, cleanup;
- Linux existing enforcement and helper lifecycle unchanged.

### TypeScript / browser

- generated declaration accepts `paths`;
- settings add/remove uses native picker and persists arrays;
- picker cancellation and unavailable picker are visible/no-op as appropriate;
- permission dialog computes exact add/remove deltas;
- request contains workspace/revision/deltas and no policy roots;
- ordinary/SSH request DTOs unchanged;
- running tab marker/tooltip immutable;
- post-launch tooltip shows realized global/add/runtime roots.

### Release gates

- focused Go and Vitest suites;
- Linux enforcement smoke and built-artifact smoke;
- macOS native and built-artifact smoke in macOS CI;
- browser-driven settings and Quick Connect flow;
- Wails build;
- `make ci`, `gosec ./...`, root/frontend `npm audit`.
