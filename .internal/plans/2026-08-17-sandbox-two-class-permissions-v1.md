---
title: Sandbox two-class directory permissions implementation plan
status: approved
created: 2026-08-17
bead: nocx-83oba
adr: docs/decisions/0039-sandbox-two-class-directory-grants.md
spec: docs/superpowers/specs/2026-08-17-sandbox-two-class-permissions-design.md
---

# Sandbox two-class directory permissions — implementation plan

## Contract fixed before code

- Settings: existing `sandbox.allowedWritablePaths`, new `sandbox.allowedReadOnlyPaths`; both `paths`, max 32.
- Request: `{workspace, settingsRevision, addWritable?, removeWritable?, addReadOnly?, removeReadOnly?}`; old `add`/`remove` rejected.
- Internal request: class-scoped global/add/remove slices.
- Result: required `sandbox.{backend,workspace,writableRoots,readOnlyRoots}`; both root arrays are full installed `Policy` slices.
- Conflict rule: reject exact cross-class identity and any RO root equal to/below an effective RW root; allow RW child under RO ancestor.
- Failure class follows provenance: request/delta `-32602`, persisted baseline `-32007`.
- Native policy remains one document; no Landlock/Seatbelt rule-kind changes.

## Slice 1 — Common policy core (red → green → refactor)

### Red

Extend `internal/sandbox/policy_test.go`:

- compose global/add/remove read-only entries canonically;
- read-only removal matches only read-only baseline;
- per-tab upgrade/downgrade (remove one class + add the other);
- exact RW/RO collision;
- RO child below workspace/user RW/Git/runtime root rejection;
- RW child below RO ancestor allowed;
- request-caused conflict is `ValidationError`; baseline-caused conflict is `SetupError`;
- same-class add/remove collision and 32-entry bounds for every list;
- `ValidatePolicy` independently rejects unenforceable RO-below-RW documents;
- final policy root and 64 KiB bounds remain active.

### Green

Update `internal/sandbox/sandbox.go` and `policy.go`:

- cleanly rename `Global/Add/Remove` to class-scoped writable names and add class-scoped read-only fields;
- canonicalize six lists through the existing pipeline;
- share exact-removal validation without hiding failure provenance;
- compose both classes in deterministic order;
- classify cross-class conflicts before `normalize` can silently remove duplicates;
- add containment validation at BuildPolicy and ValidatePolicy boundaries.

No native renderer changes should be necessary: `helper_linux.go` and `profile.go` already map the two policy slices.

## Slice 2 — Typed settings (red → green)

### Red

Extend `internal/settings/settings_test.go` for `sandbox.allowedReadOnlyPaths` declaration, canonical save, append, dedupe, invalid path/type/count rejection, snapshot, persistence/reload, reset, restore, and atomic cross-class conflict rejection through every mutation path.

### Green

Register one new `MustRegisterPathList` declaration in `internal/settings/settings.go`. Reuse the existing `ControlPaths` storage and canonicalization. Validate the final read-only/read-write pair before every single-setting, bulk, restore, and reset commit; policy construction remains the independent fail-closed authorization backstop for legacy or corrupt state.

## Slice 3 — Strict transport and result contract (red → green)

### Red

Extend `internal/transport/ws_sandbox_test.go`, settings wire tests, session/PTY immutability tests, and contract conformance:

- four explicit delta arrays accepted and old ambiguous members rejected;
- null, duplicate key/entry, wrong type, unknown member, and >32 rejected;
- both baselines read from the same snapshot/revision;
- missing/corrupt/over-count baseline is setup failure;
- class-scoped deltas reach `sandbox.Request` as deep copies;
- open result carries both full installed root arrays;
- mutating an accessor result cannot mutate policy/session state.

### Green

Update:

- `openSandboxParams` and strict decoder;
- baseline extraction helper;
- open handler request composition;
- `SessionInfo`, `Clone`, and `LocalPty.SandboxInfo`;
- `contracts/open.schema.json`, then regenerate `frontend/src/generated/open.ts`.

No aliases for `add`/`remove`.

## Slice 4 — Frontend flow (red → green → refactor)

### Red/migrate in one cut

Update exact-shape fixtures and add observable tests in:

- `sandbox-permissions-dialog.test.tsx`;
- `sandbox-open.test.ts`;
- `settings-paths.test.tsx`;
- `ipc.test.ts`;
- `tabs.test.ts`;
- `terminal-content.test.ts`.

Required behavior:

- two baseline sections;
- repeated additions append in each class;
- cancel is a no-op;
- baseline uncheck and ephemeral removal produce the correct class delta;
- cross-class duplicate selection never emits contradictory deltas;
- all launch arrays copied at tab creation;
- empty arrays omitted on wire; non-empty arrays use new names;
- tooltip reports read-only and writable installed roots.

### Green

Update `ipc.ts`, `sandbox-open.ts`, `sandbox-permissions-dialog.tsx`, `tabs.ts`, and `terminal-content.ts`. Settings is declaration-driven; only copy/tests should need adjustment unless class-specific empty text proves necessary.

### Refactor

Extract one local class-section component only if it removes real duplicated state/handlers. Keep it in the dialog file; do not create a generic permission framework.

## Slice 5 — Native and product verification

- Extend the shared sandbox probe so a user read-only directory is readable but denies create/append/rename/remove, a user writable directory permits read/write, and an outside directory remains denied.
- Linux: focused policy tests, Landlock process smoke, built-artifact smoke.
- Darwin: cross-compile package/tests, real Seatbelt process smoke and built-artifact smoke in macOS CI.
- Cross-platform policy tests prove filesystem-equivalent exact identity; Linux runtime tests prove relative PATH entries are skipped and ELF metadata/dependency work stops at path-free explicit budgets.
- Browser: both Experimental settings visible and Quick Connect action still gated; native picker mechanics remain covered by unit/Wails smoke.
- Wails production build and startup smoke.
- `make ci`, `gosec ./...`, root/frontend dependency audits.

## Documentation cleanup

ADR-0039 and the new design spec are the authoritative delta; ADR-0037 remains append-only history. Update AD-11 to cite ADR-0039 and name both baselines/classes. Keep ADR-0036/0037 and vision scope unchanged.

## Complete callsite migration

Affected callers/types: settings declaration; `sandbox.Request`; `BuildPolicy`; `SessionInfo`; PTY metadata; transport strict DTO/snapshot helper/open handler; open schema/generated type; frontend IPC launch/result; sandbox flow/dialog; tab request copy; terminal tooltip; all exact-shape tests and enforcement probes.

Intentionally unchanged: Quick Connect availability model, fixed opencode intent, authenticated nocxify lifecycle, ordinary local/SSH DTOs, native rule renderers, settings capability/RPC generic machinery, dialog picker contract, Makefile/CI target names.
