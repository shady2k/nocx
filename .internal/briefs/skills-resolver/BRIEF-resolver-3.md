# Brief — task 3: the tool exists, and an install can name a resolution

You are implementing `nocx-295uk.3` in the nocx repository. The two tasks before
it are done and on your base: `internal/skill` already resolves a GitHub
address to a pinned plan and already installs from one. **Nothing you write
goes in `internal/skill`** — that package is finished for this epic's front, and
everything you need from it is already exported.

**Run `pwd` before anything else.** Your worktree is the only tree you may write
to.

## Read first, in this order

1. `AGENTS.md`. The testing rules and the commit format are binding. Rule 5 —
   "the wire is a party to the contract" — governs half two of this brief
   directly, so read that section twice.
2. `.internal/specs/2026-09-06-the-agent-finds-the-source-nocx-resolves-it-design.md`
   — §0 names the boundaries, §3 is what the person must end up seeing, §4 is
   the tool.
3. `internal/skill/resolution.go` and `internal/skill/github.go` — the seam you
   are exposing. `Store.Resolve`, `Store.PreviewResolved`, `Store.InstallResolved`
   and the `Resolution`/`ResolutionCandidate` shapes are what you have to work
   with.
4. `internal/agenttools/registry.go` — the `fetch.url` and `skills.install`
   declarations, side by side. Yours is shaped like the first and lands beside
   the second.
5. `internal/assistant/execute.go` `executeSkillsInstall`, and
   `internal/assistant/kernel.go` `resolveSkillInstall` — the two halves of the
   existing two-step, which is the pattern you are extending rather than
   copying.
6. `contracts/README.md`, and `internal/agenttools/skills_install_wording_test.go`
   — the second is the shape of the wording test half one owes.

## Half one — `skills.resolve` exists and the model can call it

**The declaration.** In `internal/agenttools/registry.go`, beside
`skills.install`. It reaches one address and writes nothing, so it carries
`content.EffectCrossBoundary` and `content.ResourceDestination` and resolves its
resource from the address argument — the same shape `fetch.url` has, for the
same reason. It is **not** a door: its class does not depend on its arguments,
so ADR-0053's mechanism is not involved and you should not reach for it.

**The contract.** `contracts/tools/skills.resolve.schema.json`, with
`additionalProperties: false` and an explicit `required` on both the params and
the result — a schema without both is theatre, and AGENTS.md says so. The result
names the canonical repository, the ref, the resolved commit, the candidates
(path, name, description) and the handle.

**The wiring.** The `SkillLibrary` seam in `internal/assistant/assistant.go`
gains `Resolve`; the executor goes in `execute.go` beside `executeSkillsInstall`;
the deadline is applied the way `resolveSkillInstall` applies it, and for the
reason its comment gives.

**The wording test** is not optional and is not decoration. Read
`skills_install_wording_test.go` first: a model once talked itself out of
calling a tool because a field told it its address was the wrong shape, and this
field is exposed to exactly that failure. It must tell the caller to pass the
address the person gave, or the one a page named, and must not make the caller
judge in advance whether that address is enumerable — refusing is the tool's
job, and an unsupported address is not even a refusal, it is "I cannot enumerate
that" and the caller falls back to `skills.install`.

Half one is finished when the assistant can be asked what is in a repository and
can answer. That is a real thing a person can do, and it is worth confirming
before you start half two.

## Half two — an install can name a resolution, and the approval carries a set

**`skills.install`'s schema accepts either shape**: a `url`, exactly as today and
with today's wording untouched, or a `handle` plus the explicit `paths` chosen
from a resolution. Check what `frontend/scripts/gen-contracts.mjs` does with a
`oneOf` before you commit to one; if it cannot express the alternation, say so in
your report and pick the nearest thing that keeps `additionalProperties: false`
meaningful — do not quietly drop the constraint.

**`ApprovalInstall` becomes a list plus the route.** Today
(`internal/assistant/skillinstall.go`) it is one skill: url, name, description,
digest, files. It becomes the route facts, shared by every skill in the set —
where this started, where it ended up, that the origin changed, the ref, the
resolved commit — and then the skills, each with what it carries now plus its
path inside the repository.

A plain `url` install is a set of one with no route facts. It must keep working
and its existing tests must keep passing unchanged in meaning; if you have to
edit one, say in the report which and why.

**Decided in this brief, so you do not have to decide it:** one call installs a
SET, not one candidate. Spec §3's dialogue offers "which one, or all of them?",
so "all" is a real case; nine approval dialogs for it are worse than one listing
nine skills, and the person reads the same content either way. It also keeps
`InstallResolved`, which already takes a set, from having a half nothing can
reach.

**Rule 5 applies and it is the point of half two.** Three checks, and the third
is the one that matters:

- `npm run contracts:check` — the committed generated file matches the schema;
- the Go struct marshals to something the schema accepts;
- **the real approval, off the real socket, validates against the contract.** A
  test that validates a payload the test itself built proves the struct is
  well-formed, not that the server sends it. Find the existing
  `…OverTheWireConformsToContract` test for this notification and extend it.

## What you must NOT do

- **Nothing in `internal/skill`.** It is finished. If you believe it is wrong,
  say so in the report and stop; do not edit it.
- **Nothing in `frontend/src`** except the generated types that
  `npm run contracts:check` regenerates. The window's appearance is
  `nocx-295uk.4`.
- No `git commit`, no push, no branch. Leave the tree dirty.
- Do not touch beads.
- No repo-wide gates: no `make ci`, no `make ci-full`, no `scripts/ci-linux.sh`,
  no e2e suite. No repo-wide formatting run.

## Verification, scoped, with the exact binaries

```
go build ./internal/agenttools/ ./internal/assistant/ ./internal/transport/
go test ./internal/agenttools/ ./internal/assistant/ ./internal/transport/
golangci-lint run ./internal/agenttools/... ./internal/assistant/... ./internal/transport/...
gofumpt -l internal/agenttools internal/assistant internal/transport
node .githooks/check-deadcode.mjs --platform=linux/amd64
cd frontend && npm run contracts:check
```

`golangci-lint` enables checks `go vet` does not — `shadow` among them — so run
it and not only `go vet`. The deadcode ratchet must say **0 NEW**; if something
you add is unreachable, that means it landed ahead of its consumer, and the
answer is to wire it or drop it, never to add it to the baseline.

`internal/transport` takes about two minutes. Run it anyway: it owns the socket
the wire test goes through.

**Baseline before blame.** If something outside your change fails, prove whether
it failed before your edit and say which.

## Report

Numbers, not adjectives. Paste real output. Disclose every edit you made that
this brief did not ask for, including deletions of lines you judged dead — a
previous worker on this epic deleted a latch-clearing line and did not mention
it, and that is the one thing a report exists to prevent. Say what you could not
verify; silence is not the same as nothing to report.

## When you are done

Print exactly, on its own line:

    WORKER_DONE::resolver-tool-9c4e11

If you cannot finish, print exactly:

    WORKER_BLOCKED::resolver-tool-9c4e11 <one line why>
