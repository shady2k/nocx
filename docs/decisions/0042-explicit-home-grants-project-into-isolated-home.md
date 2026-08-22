# ADR-0042 — Explicit host-HOME grants project into the isolated sandbox HOME

- **Status:** Accepted
- **Date:** 2026-08-19
- **Related:** ADR-0036, ADR-0037, ADR-0039, ADR-0040, ADR-0041, ADR-0011, AD-8, AD-11, `nocx-r7dle`
- **Design:** `docs/superpowers/specs/2026-08-19-sandbox-home-projections-design.md`

## Context

A sandboxed shell receives a fresh per-session HOME and fresh `XDG_*_HOME` paths. Native Landlock/Seatbelt rules still authorize canonical host paths selected as explicit read-only or read-write grants, but software that discovers configuration through `~` or default XDG locations looks in the empty runtime tree. Granting `/` does not change environment name resolution and is an excessive compatibility workaround.

The sandbox must preserve its isolated writable HOME without adding a second permission model or a product-specific OpenCode path. Portable unprivileged bind/union mounts are unavailable across Linux and macOS; copying host content would create stale state, duplicate secrets, and unclear write-back semantics.

## Decision

1. The backend resolves host HOME from the last `HOME=` entry in the original command environment, falling back to `os.UserHomeDir`, and canonicalizes it through the existing existing-directory pipeline before rewriting the child environment. A present but unusable HOME is a path-free setup failure (`-32007`).
2. Projection candidates retain provenance from the existing `BuildPolicy` composition. Only the canonical workspace and surviving explicit global/per-tab read-only/read-write grants are candidates. Git common roots, system roots, PATH entries, runtime dependencies, shell/native helpers, devices, runtime HOME/TMP, and every other backend-derived root are excluded.
3. A candidate projects only when it is a strict canonical descendant of host HOME. `/`, host HOME itself, its ancestors, outside paths, and any candidate intersecting the per-session runtime tree remain absolute-only native grants. A broad ancestor never implies a scan or enumeration of HOME children.
4. The projection is a disposable symlink alias under the isolated runtime HOME. It is never an authorization rule and carries no access class. `WritableRoots` and `ReadOnlyRoots` remain the sole native authority, including the explicit writable-child-under-read-only-ancestor exception from ADR-0039.
5. Logical projection metadata is immutable, required in sandbox session metadata, bounded, deterministic, and validated as part of the helper policy document. The physical forest is minimal: a projected ancestor supplies discoverability for projected descendants, while sibling leaves receive private synthetic parents.
6. Linux and macOS materialize projections before native enforcement with directory-relative no-follow operations. Runtime root, runtime HOME, and synthetic parents must be backend-owned non-symlink directories with exact mode `0700`; final links point to the exact canonical source and never replace an entry. The source is re-resolved immediately before linking.
7. Any collision, unsafe parent, ownership/mode fault, changed or missing source, invalid metadata, or syscall failure fails closed, removes only the fresh runtime tree, returns a path-free setup error, and registers no session. Runtime cleanup removes links without following them and never touches host targets.
8. A child may unlink or replace an alias because runtime HOME is writable. This changes discoverability only: access through a replacement is governed by the replacement target’s existing native rule. An ungranted target remains denied.
9. No host-HOME scan, content copy, credential inspection, bind mount, renderer-authored mapping, application-name special case, new setting, or launch toggle is permitted.

## Consequences

- Explicit configuration/state grants below HOME work with ordinary `~/…` and default XDG discovery while each sandbox still owns a disposable HOME.
- Session metadata can explain the alias separately from the authoritative RO/RW root lists; empty metadata is encoded as `[]`, never `null`.
- `/`, HOME, and ancestor grants remain absolute-only. Users must explicitly select a descendant to make it discoverable under the isolated HOME.
- Symlink target resolution becomes a native release contract. Real Landlock and packaged Seatbelt smokes must prove read-only denial, read-write persistence, nested classes, and retarget denial. A macOS failure is stop-ship; no automatic fallback preserves both isolation and authority.
- The existing ADR-0037 threat boundary remains: immediate re-resolution catches ordinary source drift but does not claim inode-stable authorization against a separate already-unrestricted same-user process racing setup.

## Rejected alternatives

- **Grant `/` or host HOME:** does not fix HOME/XDG name resolution and grants excessive authority.
- **Copy selected directories:** duplicates potentially secret-bearing content, creates stale snapshots, and invents write-back semantics.
- **Bind/union mounts:** not a portable unprivileged per-tab mechanism on both supported platforms.
- **Project derived roots:** turns backend execution requirements into user-visible namespace authority and lets broad roots imply children.
- **Add access class to each projection:** creates a second permission model and misrepresents a writable child below a read-only projected ancestor.
- **OpenCode-specific mapping:** violates the interactive-shell contract in ADR-0040 and does not solve the general discovery defect.
