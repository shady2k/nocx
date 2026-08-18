# ADR-0037 — The per-tab filesystem sandbox launches an interactive shell

- **Status:** Accepted
- **Date:** 2026-08-18
- **Related:** ADR-0033, ADR-0034, ADR-0035, ADR-0036, AD-7, AD-8, AD-11, `nocx-y46q.18`
- **Supersedes:** ADR-0035 launch-target decision and every fixed-OpenCode clause it added to ADR-0033/AD-11; ADR-0035's private runtime-tree bootstrap placement remains adopted
- **Design:** `.internal/specs/2026-08-02-native-filesystem-sandbox-design.md`
- **Change proposal:** `_bmad-output/implementation-artifacts/2026-08-18-sandbox-shell-contract-restoration-change-proposal.md`

## Context

The original sandbox contract defines `Sandboxed shell…`: an explicitly selected local login shell constrained to a canonical workspace and permission set. Follow-up `nocx-y46q.17` correctly found that nocxify's private rcfile was created outside the future cage, then incorrectly classified the absence of an automatic OpenCode launch as a defect. ADR-0035 combined the required artifact-placement repair with a new fixed-agent product intent.

That intent contradicts the parent epic, its design, and its frontend slice. It also gives every tab an ephemeral OpenCode home, causing first-run behavior to repeat. The owner rejected the product substitution on 2026-08-18.

## Decision

1. The `sandbox.enabled` action is **`Sandboxed shell…`**, with stable id `__sandboxed_local__`. It opens a new interactive local login shell in the selected canonical workspace after permission confirmation.
2. The shell performs authenticated local nocxify bootstrap and remains the foreground process. nocx does not resolve, authorize, or execute OpenCode. Running `opencode` is an ordinary user command inside the resulting cage.
3. The private bootstrap repair from ADR-0035 remains: the composition root creates the per-session runtime tree first; bash rcfile or zsh ZDOTDIR is written under its writable `tmp/`; the native backend adopts the same tree; every failure and close path removes it idempotently.
4. Sandbox launch remains strict. A sandbox request with no supported enhanced bash/zsh tier or lifecycle kernel, an artifact failure, an invalid policy, an unavailable backend, or a failed native readiness handshake fails closed and registers no session. It never falls back to an unsandboxed shell.
5. `open.sandbox` remains command-free and backend-authoritative. `sandbox.status` reports native backend availability only; it carries no launch-intent or executable status.
6. The fixed-agent trusted-executable seam is removed. Backend enforcement dependencies such as the macOS in-profile shim remain backend-derived and read-only.

## Amendments to prior decisions

- ADR-0033's action and consequence text reads `Sandboxed shell…`, not `Sandboxed opencode…`; its policy includes the canonical shell and native enforcement dependencies, not a fixed agent executable.
- ADR-0035 decisions 1, 3, 4, and 5 concerning the fixed OpenCode intent are superseded. Its decision 2 concerning artifact placement remains adopted, minus the agent tail.
- ADR-0036 decision 8 no longer includes a fixed OpenCode executable among backend-derived roots. All directory-class and policy-conflict decisions remain unchanged.
- AD-11 binds a sandboxed interactive shell. The renderer still requests permissions, never a command; the backend still authorizes and enforces the immutable policy.

## Consequences

- Opening a sandbox never starts OpenCode implicitly and therefore never exposes OpenCode first-run output as nocxify bootstrap output.
- The user receives the interactive shell promised by the original action and may choose which program to run.
- OpenCode availability no longer disables an otherwise available native sandbox.
- Removing the agent executable and its discovered runtime roots narrows the installed read-only policy.
- Ordinary local tabs, SSH tabs, initial tabs, `+`, and `Cmd/Ctrl+T` remain unchanged.
