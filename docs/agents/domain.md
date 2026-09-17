# Domain Docs

How the engineering skills should consume this repo's domain documentation when exploring
the codebase.

## Before exploring, read these

- **[`AGENTS.md`](../../AGENTS.md)** — the operating contract, and in practice this repo's
  `CONTEXT.md`: the working vocabulary, the backlog rules, the testing rules, the git
  authority rules. `CLAUDE.md` imports it, so it is always in context.
- **[`docs/vision.md`](../vision.md)** — what is being built, MVP scope, what is
  explicitly out.
- **[`docs/architecture.md`](../architecture.md)** — the binding spine: invariants
  `AD-1`…`AD-10`, the module map, the WebSocket protocol. **The ADs are binding.**
- **[`docs/decisions/`](../decisions/)** — the ADRs. Read the ones touching the area you
  are about to work in; `INDEX.md` is the listing.
- **[`frontend/src/ui/README.md`](../../frontend/src/ui/README.md)** — before any UI
  element.

This is a **single-context** repo. There is no `CONTEXT.md` or `CONTEXT-MAP.md`; if
`/domain-modeling` later wants one it creates it at the root, lazily, when a term is
actually resolved. Do not create one upfront and do not flag its absence.

**ADRs live in `docs/decisions/`, not `docs/adr/`.** Never create a second ADR directory.

## File structure

```
/
├── AGENTS.md            ← the operating contract (this repo's CONTEXT.md)
├── CLAUDE.md            ← a pointer to AGENTS.md, and nothing else
├── docs/
│   ├── vision.md
│   ├── architecture.md  ← AD-1…AD-10, binding
│   ├── decisions/       ← ADRs, with INDEX.md
│   └── agents/          ← this directory: what the engineering skills read
├── contracts/           ← one JSON Schema per JSON-RPC result shape
└── internal/, frontend/, e2e/
```

## Use the project's vocabulary

When your output names a domain concept — an issue title, a refactor proposal, a
hypothesis, a test name — use the term as `AGENTS.md` and `docs/architecture.md` define
it. The module map is also the area-label vocabulary, so drifting to a synonym costs
twice: the prose reads wrong and the bead files under the wrong area.

If the concept is in neither document, that is a signal: either you are inventing language
the project does not use (reconsider), or there is a real gap (note it for
`/domain-modeling`).

## Flag ADR conflicts

If your output contradicts an existing ADR, surface it rather than silently overriding:

> _Contradicts ADR-0056 (repowise is kept for history, not for search), but worth
> reopening because…_

**An accepted ADR is never edited. A change is a NEW record that supersedes it** — the
owner's rule, with the spelling in `AGENTS.md` and the row in `docs/decisions/INDEX.md`.
Never amend an ADR in place, whatever the practice visible in the older records.
