---
title: 'Project explicit sandbox grants into isolated HOME'
type: 'feature'
created: '2026-08-19'
status: 'done'
review_loop_iteration: 0
baseline_commit: 'cef0a1732bda85e9673b0cce29970cc0b8f25bda'
context:
  - '{project-root}/docs/architecture.md'
  - '{project-root}/docs/superpowers/specs/2026-08-19-sandbox-home-projections-design.md'
---

<frozen-after-approval reason="human-owned intent — do not modify unless human renegotiates">

## Intent

**Problem:** Sandboxed shells receive a fresh HOME/XDG tree, so programs that discover explicitly granted host configuration through `~/…` see an empty profile even though native rules authorize the canonical host path.

**Approach:** Preserve isolated HOME, but let the backend project eligible explicit host-HOME grants into their normal relative locations as disposable symlinks before native enforcement. The projection is metadata/discoverability only; existing canonical RO/RW roots remain the sole permission authority.

## Boundaries & Constraints

**Always:** Resolve and canonicalize host HOME from the last pre-sandbox `HOME=` value, falling back to `os.UserHomeDir`; fail path-free with `-32012` when unusable. Candidate provenance is only workspace plus surviving explicit global/per-tab RO/RW grants. Accept only strict canonical descendants that do not intersect the runtime tree. Keep deterministic non-nil metadata, validate the helper document, create a minimal no-follow mode-0700 symlink forest, and register a session only after native readiness.

**Ask First:** Any change to AD-11, ADR-0034’s threat model, the symlink model, native RO/RW authority, or the stop-ship response to a failed packaged macOS Seatbelt smoke.

**Never:** Scan host HOME, enumerate children implied by `/`, project HOME/ancestors/outside/derived Git/system/PATH/runtime/shell/device roots, copy content, bind mount, inspect credentials, add an application special case, let the renderer author mappings, mutate a running tab, add a fallback, or touch unrelated `go.mod`/`frontend/package.json.md5` changes.

## I/O & Edge-Case Matrix

| Scenario | Input / State | Expected Output / Behavior | Error Handling |
|---|---|---|---|
| Eligible explicit grant | Canonical RO/RW root strictly below host HOME | Required logical mapping and exact guest symlink; native target class unchanged | Fail closed before registration if materialization fails |
| Nested grants | RO ancestor plus explicit RW child | One topmost physical link; both logical mappings retained; child remains RW | No projection access class |
| Ineligible root | `/`, HOME, ancestor, outside, derived root, or runtime intersection | Absolute native grant only; no mapping | No scan or implied child mapping |
| Corrupt internal policy | nil/oversized/duplicate/traversal/conflicting projection | Helper rejects document | Path-free setup failure |
| Child retarget | Guest link replaced with outside or another granted target | Outside denied; granted target receives only its existing class | Native Landlock/Seatbelt remains authoritative |

</frozen-after-approval>

## Code Map

- `internal/sandbox/policy.go` — effective grant composition, HOME resolution, metadata validation and bounds.
- `internal/sandbox/home_projection*.go` — logical/minimal-forest planning and Linux/macOS no-follow materialization.
- `internal/sandbox/sandbox_{linux,darwin}.go` — pre-enforcement materialization call sites and cleanup.
- `internal/sandbox/{probe_child_test.go,artifact_smoke.go}` / `cmd/sandboxprobe` — native and packaged behavior proof.
- `internal/sandbox/sandbox.go`, `internal/pty/pty_local.go`, `internal/session`, `internal/transport` — immutable deep-copied open metadata.
- `contracts/open.schema.json`, `frontend/src/{ipc.ts,terminal-content.ts,sandbox-permissions-dialog.tsx}` — required wire shape and user-visible explanation.

## Tasks & Acceptance

**Execution:**
- [x] `docs/architecture.md`, `docs/decisions/0039-*.md` — bind projection provenance, alias-only authority, exclusions, and non-goals.
- [x] `internal/sandbox/home_projection*_test.go` — add red planner, validation, materialization, cleanup, and security tests.
- [x] `internal/sandbox/policy.go`, `home_projection*.go`, `sandbox_{linux,darwin}.go` — plan, validate, materialize, and fail closed.
- [x] `internal/{sandbox,pty,session,transport}` — preserve required non-nil metadata across every copy and fixture.
- [x] `contracts/open.schema.json`, generated TypeScript, `ipc.ts`, `terminal-content.ts` — expose mapping and test populated/empty tooltips.
- [x] `internal/settings/settings.go`, `sandbox-permissions-dialog.tsx` — explain strict-descendant projection and credential-sensitive RO/RW authority.
- [x] Native probe and artifact smoke paths — prove default HOME/XDG lookup, RO denial, RW persistence, nesting, retarget denial, and cleanup.

**Acceptance Criteria:**
- Given explicit config/state grants below host HOME and no `/` grant, when a sandboxed shell uses `$HOME` or default XDG discovery, then it reaches the host directories through projections with exact native RO/RW behavior.
- Given empty or ineligible candidates, when `open` succeeds, then `homeProjections` is required and encoded as `[]`, and the tooltip states that HOME is isolated.
- Given a malformed plan, collision, parent symlink, ownership/mode fault, source drift, or syscall failure, when preparation runs, then no session registers, the fresh runtime tree is removed, the host target survives, and the error contains no path.
- Given a retargeted guest symlink, when the child accesses it, then an ungranted target is denied and another granted target receives no authority beyond its native root class.

## Spec Change Log

## Design Notes

The logical list is richer than the physical forest: descendant mappings stay observable even when an ancestor link makes extra links unnecessary. This preserves the explicit RW-child-under-RO-ancestor exception without inventing projection access classes.

## Verification

**Commands:**
- `go test ./internal/sandbox ./internal/pty ./internal/session ./internal/transport` — planner, materializer, copies, and wire fixtures pass.
- `cd frontend && npm run contracts:check && npm test -- --run src/terminal-content.test.ts src/ipc.test.ts src/tabs.test.ts src/sandbox-permissions-dialog.test.tsx && npm run typecheck` — generated contract, tooltip/copy, fixtures, and types pass.
- `make sandbox-smoke-linux` — real Landlock behavior passes on synthetic HOME fixtures.
- `make ci && gosec ./... && cd frontend && npm audit` — repository and security gates pass before PR update.
- Hosted `make sandbox-smoke-macos` and `make sandbox-smoke-macos-artifact` — Seatbelt source/package semantics pass before shipping.

## Suggested Review Order

**Contract and authority**

- Start with the complete discoverability-only contract and rejected alternatives.
  [`2026-08-19-sandbox-home-projections-design.md:1`](../../docs/superpowers/specs/2026-08-19-sandbox-home-projections-design.md#L1)

- ADR-0039 binds strict provenance, exclusions, and native target authority.
  [`0039-explicit-home-grants-project-into-isolated-home.md:1`](../../docs/decisions/0039-explicit-home-grants-project-into-isolated-home.md#L1)

- AD-11 places projections inside the existing backend-authorized sandbox boundary.
  [`architecture.md:182`](../../docs/architecture.md#L182)

**Planning and validation**

- BuildPolicy retains effective explicit-root provenance before derived roots join policy.
  [`policy.go:50`](../../internal/sandbox/policy.go#L50)

- Logical planning proves strict descent, runtime separation, and deterministic first-wins mappings.
  [`home_projection.go:52`](../../internal/sandbox/home_projection.go#L52)

- Helper-document validation enforces bounds, canonical roots, safe paths, and exact representation.
  [`home_projection.go:111`](../../internal/sandbox/home_projection.go#L111)

**Materialization and native enforcement**

- The Unix materializer plans first, verifies private directories, then uses no-follow relative syscalls.
  [`home_projection_unix.go:20`](../../internal/sandbox/home_projection_unix.go#L20)

- Linux materializes before environment rewrite and Landlock helper serialization.
  [`sandbox_linux.go:62`](../../internal/sandbox/sandbox_linux.go#L62)

- macOS materializes after trusted-executable validation and before Seatbelt profile rendering.
  [`sandbox_darwin.go:145`](../../internal/sandbox/sandbox_darwin.go#L145)

**Wire and user explanation**

- The open schema makes bounded projection metadata required and closed.
  [`open.schema.json:48`](../../contracts/open.schema.json#L48)

- SessionInfo owns immutable deep-copied realized metadata.
  [`sandbox.go:146`](../../internal/sandbox/sandbox.go#L146)

- The tooltip separates authoritative roots from populated or isolated HOME state.
  [`terminal-content.ts:1981`](../../frontend/src/terminal-content.ts#L1981)

- Settings explains strict-descendant aliases and credential-sensitive authority.
  [`settings.go:828`](../../internal/settings/settings.go#L828)

**Behavioral proof**

- Real Landlock smoke exercises XDG lookup, nesting, retargeting, and persistence.
  [`helper_linux_test.go:67`](../../internal/sandbox/helper_linux_test.go#L67)

- Artifact smoke reaches the same policy/materialization path through the built executable.
  [`artifact_smoke.go:44`](../../internal/sandbox/artifact_smoke.go#L44)