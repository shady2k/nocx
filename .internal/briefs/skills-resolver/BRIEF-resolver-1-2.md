# Brief — the resolver, tasks 1 and 2

You are implementing the front of epic `nocx-295uk` in the nocx repository.

**Run `pwd` before anything else.** Your worktree is the ONLY tree you may
write to. Every path in this brief is relative to it. If you find yourself
about to write an absolute path from somebody else's checkout, stop.

## Read first, in this order

1. `AGENTS.md` — the operating contract. The testing rules and the commit
   message format are binding, not advisory.
2. `.internal/specs/2026-09-06-the-agent-finds-the-source-nocx-resolves-it-design.md`
   — the design you are building. §0 names the boundaries; §4 is your two tasks.
3. `.internal/specs/2026-09-04-skills-under-the-same-policy-design.md` §5,
   including the block marked "Corrected 2026-09-06" — it says which parts of
   the machine come back and why.
4. `internal/skill/preview.go` and `internal/skill/install.go` — in full. Task 2
   is a widening of what is already there, and you cannot widen what you have
   not read.
5. `internal/skill/bundle.go` — the header comment decides what belongs to a
   skill. None of it changes; you are changing where the FIRST address comes
   from.

## Task 1 — `nocx-295uk.1`: the GitHub adapter

Resolve any GitHub address into an install plan. Three mechanical steps, all
through `internal/apifetch` and therefore `internal/httppolicy`. **Do not
construct an `http.Client`** — a second one would be a second answer to which
addresses may be reached, and it would agree with the first everywhere anybody
looked.

1. **Canonicalize** any of these to `owner/repo`: a repository page, a `blob`
   page, a `raw.githubusercontent.com` SKILL.md address, a bare `owner/repo`
   string. A raw SKILL.md address additionally yields the ref and the path it
   sat under.
2. **Ref to commit sha.** An explicit ref, or the repository's default branch.
3. **Commit to candidates.** The paths in that tree matching `<dir>/SKILL.md`
   and a root `SKILL.md`. Then, per candidate, a BOUNDED prefix read for the
   frontmatter `name` and `description`.

Assertions this must satisfy — write them as tests, not as prose:

- The candidate count and the per-candidate read are bounded, and **hitting a
  bound is a refusal that names itself.** Never a silent truncation: a
  repository with four hundred skills is a real thing, and "we showed you some
  of them" is the exact failure this epic exists to remove.
- A **rate-limited** forge API refuses in words that distinguish it from "that
  repository does not exist". Those are opposite instructions to a person.
- An address on no adapter's forge is **not an error**. It answers "I cannot
  enumerate that", and the caller falls back to today's `skills.install` with
  the plain address — which stays exactly as it is.
- Per AGENTS.md testing rule 3: for every external call, there is a test where
  that call fails. The forge API is three calls; all three get one.

Do not reach the real GitHub from any test. Serve a fake forge from the test
itself. A test against the real thing measures GitHub's uptime and rate limit.

## Task 2 — `nocx-295uk.2`: the resolution handle

`internal/skill/preview.go` already holds ONE slot recording what it last
showed a person, and `install.go` already refuses to write anything that does
not match it — _"nothing has been read from that address in this session, so
there is nothing to install"_.

**The resolution handle is that slot generalized from one document to one
resolution.** It is not a second thing that answers the same question; if you
find yourself writing a parallel store with its own mutex and its own expiry,
you have taken the wrong turn and the spec §4 explains why (AD-8).

Assertions:

- Installing **spends** the handle (`forgetPreview`'s job today), because an
  approval is for one occasion and not a standing permit.
- Resolving again forgets the previous resolution, and the refusal that
  produces is recoverable by resolving again.
- `skills.install` accepts **either** an address (today's path, unchanged and
  still tested) **or** a handle plus explicit paths. With a handle the backend
  fetches those paths **at the resolved commit**, and the model carries neither
  the bytes nor the address they came from.
- The digest comparison at install is unchanged: the bytes written are the
  bytes shown.
- State the invariant with BOTH ENDS (AGENTS.md testing rule 3). Not "the
  handle is recorded when a resolution succeeds" but "the handle is valid from
  the moment the resolution is recorded until an install spends it or a second
  resolution replaces it" — and test the closing event, not only the opening
  one.

## What you must NOT do

- **No registry declaration and no contract schema for `skills.resolve`.** That
  is task 3 (`nocx-295uk.3`) and it is somebody else's. Your work ends at the
  `internal/skill` seam that task 3 will call.
- **Nothing in `frontend/`.** That is task 4.
- **No `git commit`, no `git push`, no branch.** Leave the tree dirty; the
  coordinator reads the diff.
- **Do not touch beads.** Only the coordinator owns the tracker. Everything you
  need is in this brief.
- **No repo-wide gates.** Do not run `make ci`, `make ci-full`,
  `scripts/ci-linux.sh`, or the e2e suite. Another worker's half-written file
  would make you escalate on a phantom.
- **No formatting run over the repository.** `gofumpt` on the files you touched
  is right; a repo-wide format is a later, single-worker wave.

## Verification, scoped to your files

Name the exact binary. Run, and paste the real output in your report:

```
go build ./internal/skill/ ./internal/apifetch/
go test ./internal/skill/ -run '<your test names>' -v
go vet ./internal/skill/
gofumpt -l internal/skill/
```

`go build` does not compile `_test.go` files, so `go test` is the type check
here and it is not optional.

**Baseline before blame.** If a test outside your change fails, prove whether it
failed before your edit — `git stash push -u -m resolver-baseline`, run it,
`git stash apply <sha>` — and say which it was. Do not attribute a pre-existing
failure to yourself, and do not attribute your own failure to the tree.

## Report

Numbers, not adjectives. Say what you ran and what came back, what you could not
verify, and every decision you made that the brief did not make for you. Where
this brief says "call out anything you could not verify", silence is not the
same as nothing to report.

## When you are done

Print exactly, on its own line:

    WORKER_DONE::resolver-a7f31c

If you cannot finish, print exactly:

    WORKER_BLOCKED::resolver-a7f31c <one line why>
