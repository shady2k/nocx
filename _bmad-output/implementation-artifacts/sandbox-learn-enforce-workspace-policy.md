# Sandbox Learn/Enforce and workspace policy implementation

## Scope

Replace the pane-only sandbox grant and command/statistics surface with one
backend-owned workspace policy flow:

- `authority_grants` stores execution or pane subjects with an exact subject
  constraint.
- `sandbox.status` reports independent Learn and Enforce modes.
- `open.sandbox` is strict and carries bounded workspace/profile deltas.
- Named workspace profiles use revision-checked copy-on-write persistence.
- Access diagnostics are bounded, memory-only, and require pane identity for
  resolution.
- Settings exposes Developer → Sandbox; generic sandbox rows are not duplicated.
- The contextual shield and `/sandbox` command are the user entry points.

## Invariants verified during implementation

- PTY data remains binary and never enters JSON or persistence.
- Session IDs, pane IDs, workspace IDs, and host validation remain backend-owned.
- Enforce has no ordinary-shell fallback after native setup failure.
- Learn is unrestricted and diagnostic; it is not authorization.
- A running pane's grant is immutable; promotion affects only future launches.
- Startup removes stale pane grants before layout serving.

## Verification record

- Focused Go packages: `internal/app`, `internal/session`, `internal/sandbox`,
  `internal/pty`, `internal/content`, `internal/transport`, `internal/settings`.
- Frontend production and test TypeScript projects compile.
- Focused Vitest coverage includes internal `/sandbox` interception and both
  settings path classes, picker cancellation/errors, conflict handling, and
  complete-array persistence.
- Transport contract tests cover OpenRPC registration and parameter validators.
