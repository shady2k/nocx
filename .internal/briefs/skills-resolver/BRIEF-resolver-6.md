# Brief — task 6: the epic's happy path, against a fake forge

You are implementing `nocx-295uk.6`, the LAST child of `nocx-295uk` and the
check that lets the epic close. Everything it exercises is already built and on
`main`-merged `feat/skills-page`: `skills.resolve`, an install that names a
resolution, and an approval window that shows the route.

**Run `pwd` before anything else.** Your worktree is the only tree you write to.

## Read first, in this order

1. `AGENTS.md`, and **testing rule 2 twice** — this bead exists only because of
   it. An epic closes when one automated check has watched a person do the
   thing, and this is that check. Also read "There is no 'not ours'".
2. `.internal/specs/2026-09-06-the-agent-finds-the-source-nocx-resolves-it-design.md`
   §3 and §6 — the transcript this test re-enacts.
3. `internal/skill/github.go` — `GitHubAdapter`, its `apiBase`/`rawBase` fields,
   `NewGitHubAdapter`, and the address recognizer near the bottom that keys on
   the literal hostnames `github.com` and `raw.githubusercontent.com`.
4. `internal/app/app.go` around the fetch seam: `apifetch.New(apiRouteTable,
logger)`, `skill.NewStore(..., skill.WithFetcher(apiFetcher))`, and
   `type Option func(*optionSet)` with `WithRealSystemKeystore` and
   `WithLogFilePath` as the two examples of what an Option looks like here.
5. `internal/transport/ws_skill_install_approval_test.go` — how a test drives
   the real socket, reads `agent.approvalRequested`, and validates against the
   contract.

## What the run must assert

One run, in this order, as one person's motion:

- a documentation page is fetched and names a repository;
- `skills.resolve` on that repository offers **nine** candidates;
- **one** is chosen and installed;
- the approval carries **both origins** and the **resolved commit**;
- the bytes on disk after the answer match the bundle that was shown;
- `enabled` is **false**.

The last one is not a detail: a skill from outside this machine arrives switched
OFF, and a test that never looks would not notice the day it stops.

## Decided here, so you do not decide it

**The fake forge is an `httptest` server the test owns**, serving three things:
the documentation page, the API answering the ref and the tree, and the raw
files. Not the real GitHub — a test against it measures GitHub's uptime and rate
limit, which AGENTS.md forbids, and not a recorded fixture either, because the
point is that the adapter's own requests are the ones being answered.

**The repository ADDRESS stays a real GitHub address** — `github.com/<owner>/
<repo>` — because the recognizer keys on that hostname as a string and returns
"not a GitHub address" for anything else, at which point `skills.resolve`
enumerates nothing and the test measures the fallback instead of the feature.
So the address is GitHub's and the BYTES come from your server. That is what the
`apiBase`/`rawBase` fields are for.

**Reach them through an `app.Option`, not by constructing the adapter beside the
app.** `app.New` is the composition root and DI at a composition root is this
repo's house style; `WithLogFilePath` is the shape to copy. The option names
what it does — where the forge lives — and never mentions tests. It defaults to
today's constants, so the shipped binary is unchanged.

**The test calls `app.New(...)` and drives the real socket.** That IS the
shipped coordinator: `cmd/nocx-server/main.go` is a thin main whose whole body
is `app.New()`. Do not build a second harness beside it, and do not assert
against internal function returns — every assertion above is reachable over the
wire or on disk, and rule 1 says that is where it must be made.

**Do not weaken a policy to make the test pass.** `apifetch` goes through
`httppolicy`, which has an `http://` address rule. If a plain-HTTP loopback
server is refused, say so in your report with the exact refusal and what you
did instead — a TLS test server, or the policy's own existing allowance if it
has one. Turning verification off, adding a bypass flag, or special-casing
loopback in production code is not an option; if none of the honest routes
works, that is a `WORKER_BLOCKED`, not a workaround.

**One skill of the nine is chosen, and the other eight must be visible in the
resolve result** — that is what "nine candidates offered" means. Do not shrink
the fixture to two because nine is tedious to write; generate them.

## What you must NOT do

- No change to the resolution or install BEHAVIOUR. If the happy path does not
  pass, you have found a defect — report it with the failing assertion and the
  file it lives in, and fix it only if it is a genuine defect rather than a
  disagreement with the test you just wrote.
- No `frontend/`. The window is `nocx-295uk.4` and it is closed.
- No `eslint-disable`, no `any` — and on the Go side, no `t.Skip`, no retry, no
  sleep. Wait on an observable state change, never on a duration: a test that
  needs a slow machine is broken on a fast one and has only not been caught yet.
- No commit, no push, no branch. No tracker commands. Leave the tree dirty.
- No repo-wide gates, no `make ci`, no e2e suite, no repo-wide formatting run.

## Verification, scoped, with the exact binaries

```
go build ./internal/app/ ./internal/skill/ ./internal/transport/
go test ./internal/app/ ./internal/skill/ ./internal/transport/
go test -race -run '<your new test>' ./internal/<its package>/
golangci-lint run ./internal/app/... ./internal/skill/... ./internal/transport/...
gofumpt -l internal/app internal/skill internal/transport
node .githooks/check-deadcode.mjs --platform=linux/amd64
```

`internal/transport` takes about two minutes; run it, it owns the socket. The
deadcode ratchet must say **0 NEW** — a new Option reachable only from a test is
exactly the shape it reports, and the answer is that `app.New` genuinely calls
it, never a baseline entry.

Run your new test **at least twice**. A test that passes once and fails once is
reporting a real race, and the passing run is the one that got lucky.

**Baseline before blame.** If a gate fails outside your files, prove whether it
failed before your edits and say which.

## Report

Numbers, not adjectives: paste the test's own output, both runs. Name the option
you added and every line of production code you touched. Disclose every edit
this brief did not ask for. Say what you could not verify.

## When you are done

Print exactly, on its own line:

    WORKER_DONE::forge-happypath-2c8a41

If you cannot finish:

    WORKER_BLOCKED::forge-happypath-2c8a41 <one line why>
