# W11 — bind a stored credential to its resolved target host (nocx-mon, PR11-T5)

Worker in an Orca wave. The coordinator owns the branch, the commits and the issue
tracker. Work in `/home/dev/orca/workspaces/nocx/pr-11-boundary`.

**Run `bd show nocx-mon` first and read it in full.**

## The threat

`Credential.Host` is optional, and empty means "works for any host"
(`profile.go:105-107`).

The same authenticated renderer that can call `open` can also create profiles. So it can
create a profile pointing at a host **it controls**, attach a victim credential to it, and
have the backend dutifully submit that password to the attacker's server. T4 (landed,
`fe6e614`) stopped the password being _returned_ to the renderer — this is the other
direction, and T4 did not address it. If anything T4 makes it sharper: the renderer now
names a credential by ID and the backend resolves it, so the renderer never needs to know
the secret in order to spend it.

Strict `known_hosts` limits which hosts will answer. That is host authentication, not
credential authorization — it does not stop a credential being aimed somewhere it was never
meant to go.

## What to build

A binding check at resolve time: a stored credential may only be submitted to the target it
belongs to.

Decisions you must make and record, with reasoning, in the code:

- **What "belongs to" means.** Host alone, or host+port+user? Be precise about which
  components are part of identity and which are not, and say why.
- **What an empty `Credential.Host` means from now on.** Today it means "any host", which
  is the hole. Narrowing it changes behaviour for existing profiles — decide whether empty
  becomes "bind on first use", "refuse", or something else, and be explicit about what
  happens to credentials already stored with it empty. Silently breaking existing setups is
  as bad as leaving the hole.
- **Where the check lives.** It must be somewhere the renderer cannot route around. The
  resolver is the obvious candidate because everything funnels through it after T4 — verify
  that is actually true rather than assuming it.

## Verification

TDD per `AGENTS.md`. The test that matters is the attack, not the happy path:

- a credential bound to host A, a profile pointing at host B, `open` on that profile →
  **refused**, and the refusal happens before any dial;
- the same for the jump host — `JumpCredentials` is the newer path and the easier to miss;
- a correctly bound credential still connects, so the check is not simply "deny";
- whatever you decide about empty `Host`, a test pins that decision.

Scope Go runs to `./internal/connection/... ./internal/profile/...`. Another worker is
active on `nocx-7l4` in `internal/transport/ws.go` and `internal/credential/**` — a
repo-wide run would compile its half-written files and report a phantom blocker.

## Ground rules

- No commits, no pushes, no branches. No `git stash`.
- You own `internal/connection/**` and `internal/profile/**`. Do not touch
  `internal/transport/ws.go` or `internal/credential/**` — the other worker owns them.
  Escalate instead of crossing.
- Do not touch beads / `bd`.
- Never weaken a control to make a test pass.
- Before reporting done: `git diff HEAD -- internal | grep '^-'` and read it.
- **`gofumpt -l .` is the gate, not `gofmt`** — the last worker reported clean on the wrong
  one and it had to be fixed after the fact.
- Report numbers, not adjectives. If you cannot avoid a compromise, name it in the report
  rather than leaving it to be found — an exception stated is a decision, an exception
  omitted is a trap.

## When done

Write `.internal/reports/t5-credential-binding.md`, then `worker_done` from your own
terminal with the `taskId`/`dispatchId` from the dispatch preamble.
