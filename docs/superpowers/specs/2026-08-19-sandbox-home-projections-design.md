# Sandbox HOME projections for explicit grants — design

**Bead:** `nocx-r7dle`  
**Decision:** [ADR-0042](../../decisions/0042-explicit-home-grants-project-into-isolated-home.md)  
**Scope:** experimental local filesystem sandbox on Linux and macOS

## Product contract

A sandboxed shell keeps its fresh per-session `HOME` and `XDG_*_HOME` values. When the backend authorizes an explicit directory strictly below the original host HOME, the same directory is also discoverable at its normal relative `~/…` location inside the isolated HOME. This fixes programs that discover configuration by name rather than by an absolute grant.

The projection is a disposable symlink alias. It never grants access. The canonical workspace plus the effective explicit global/per-tab read-only and read-write roots remain the candidate set, and the existing `ReadOnlyRoots` and `WritableRoots` remain the only access-class authority. A read-only projected ancestor may therefore contain an explicitly writable projected child without adding an access class to projection metadata.

The sandbox tab tooltip keeps the complete authoritative writable/read-only root lists and adds one line:

- populated: `Home projections: ~/.config/opencode -> /host/home/.config/opencode, …`;
- empty: `Home: isolated; no host folders projected`.

Settings and launch copy explain that eligible folders can contain credentials and receive exactly the selected read-only or read-write authority. No setting or launch toggle is added.

## Candidate provenance and HOME resolution

`BuildPolicy` remains the sole composition owner. While it composes the already-canonical effective user roots, it retains this ordered projection candidate list:

1. canonical workspace;
2. effective global writable entries after exact removals;
3. per-tab writable additions;
4. effective global read-only entries after exact removals;
5. per-tab read-only additions.

Git common roots, system roots, PATH entries, runtime dependency roots, shell/native helper paths, devices, runtime HOME/TMP, and any other backend-derived roots are never candidates. A broad grant such as `/` does not enumerate or imply children below HOME.

The host HOME is the last `HOME=` value in the original `CommandSpec.Env`, matching the existing last-value environment convention. If absent, the backend uses `os.UserHomeDir`. The selected value passes through `canonicalExistingDir` before `sandboxEnv` rewrites the child environment. A present but unusable HOME, or a failed fallback, is a path-free `SetupError` (`-32007`), not an empty projection set.

## Logical projection planner

For each candidate `G`, the backend computes `rel = filepath.Rel(hostHome, G)` and accepts the pair only when all independent proofs hold:

- `G` is a strict canonical descendant of host HOME (`pathWithin(hostHome, G)` plus rejection of identity);
- `rel` is non-empty, non-absolute, not `.`, and contains no `..` component;
- `filepath.Join(runtimeHome, rel)` is a strict descendant of canonical runtime HOME;
- `G` neither contains nor is contained by the canonical per-session runtime root.

The stored pair is:

```go
type HomeProjection struct {
    HostPath     string `json:"hostPath"`
    RelativePath string `json:"relativePath"`
}
```

`HostPath` is canonical. `RelativePath` is cleaned and slash-normalized. First occurrence wins for canonical host and relative identities; output follows policy candidate order. `HomeProjections` is bounded to `1 + 4*maxUserPaths` (129) and is always a non-nil slice, including the zero case.

`ValidatePolicy` treats projection metadata as an internal untrusted document because the Linux helper decodes it from the policy FD. It rejects nil, over-limit, duplicate, absolute, empty, dot, traversal-bearing, or non-clean relative paths. Every `HostPath` must be an exact realized workspace/read-only/read-write root; being merely below a broad root is insufficient. The joined guest path must remain below `Policy.Home`. `Policy.Bytes()` keeps the existing 64 KiB bound over the complete document.

## Minimal physical forest

The planner builds the complete physical action list before touching disk. Logical metadata retains every projection, but physical links are minimal:

- if one projected relative path is an ancestor of another, only the topmost link is created;
- sibling leaves receive private synthetic parent directories and one exact final link each;
- no operation is planned beneath a final projection link.

Example: projections for `.config/opencode` and `.config/other` create private mode-`0700` `.config/` plus two links. Adding a projection for `.config` changes the physical plan to one `.config` link while all three logical mappings remain visible.

The Linux/macOS materializer uses directory-relative Unix operations through `golang.org/x/sys/unix`: no-follow directory opens, `mkdirat`, and `symlinkat`. It verifies runtime root, runtime HOME, and every synthetic parent as directories owned by the effective backend user with exact mode `0700`. Existing entries are collisions; nothing is repaired, merged, or overwritten. It never calls `MkdirAll`, follows a parent symlink, or creates below an already-created projection.

Immediately before each final link, the backend re-runs the canonical existing-directory pipeline on the source and requires the exact canonical result to remain unchanged. The link target is exactly `HostPath`. This narrows ordinary disappearance or symlink drift but does not claim inode-stable protection against a separate already-unrestricted same-user process; ADR-0037 keeps that actor outside the threat model.

Any collision, ownership/mode fault, non-directory or symlink parent, changed/vanished source, partial syscall failure, or malformed plan returns the path-free `NewSetupErrorf("runtime home projection failed")`. Both platform `Prepare` fail closures remove only the fresh runtime tree and register no session. `RemoveRuntimeRoot` removes links without following them, so host targets survive cleanup.

## Native enforcement order

Linux order:

1. `BuildPolicy` and `ValidatePolicy`;
2. materialize projections;
3. rewrite child HOME/XDG/TMP environment;
4. serialize helper payload;
5. helper revalidates policy, installs Landlock, acknowledges readiness, and execs.

macOS order:

1. `BuildPolicy`;
2. add and validate backend trusted executables;
3. materialize projections;
4. rewrite environment and render SBPL;
5. `sandbox-exec` applies Seatbelt, shim acknowledges readiness, and execs.

Landlock rules and SBPL clauses are unchanged. Native target resolution must apply the existing exact target class. The child may unlink or replace a projection because runtime HOME is writable; that can only change discoverability. A replacement to an ungranted target remains denied, and a replacement to another granted target receives only that target’s native class.

## Immutable metadata and wire

`Policy`, `SessionInfo`, every clone path, and `LocalPty.SandboxInfo` carry a required non-nil `HomeProjections` slice. The `open` schema requires `sandbox.homeProjections`, limits it to 129 unique closed objects, requires absolute host paths and safe relative paths, and generates the committed TypeScript type. The renderer sends no projection request and never authors a mapping.

## Verification contract

Unit tests cover HOME last-value/fallback/failure, exact candidate provenance, removals, canonical aliases, strict-descendant exclusions, runtime intersections, deterministic dedupe, nested classes, metadata bounds/validation, minimal physical links, sibling parents, no-follow rejection, collisions, path-free failures, partial cleanup, and host-target survival.

The common native probe and release artifact probe create only synthetic disposable host HOME fixtures. With no `/` grant they prove lookup through runtime `$HOME` and default XDG locations, read-only read success, read-only create/truncate/remove denial, read-write persistence, nested read-only ancestor plus writable child behavior, and denial after retargeting a guest link outside every granted root.

Linux Landlock evidence is required locally. macOS source and packaged Seatbelt jobs are release gates, not inferred from Linux. If Seatbelt permits retarget escape or loses target-class semantics, the feature is stop-ship; there is no automatic fallback that preserves both isolated HOME and the permission contract.

## Non-goals

No host-HOME scan, content copy, bind/union mount, credential inspection, renderer-authored mapping, application-name special case, new setting, environment isolation expansion, live permission mutation, Windows backend, or compatibility alias.
