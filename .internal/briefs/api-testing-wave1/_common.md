## Ground rules — read before anything

1. `pwd` first. Your worktree path is stated in your task section; every path you create or
   edit is under it. The spec and plan quote paths from the coordinator's checkout — they are
   repo-relative, resolve them against YOUR root.
2. **Do not commit, push or branch.** The coordinator integrates. Leave your work
   uncommitted in the worktree.
3. **Do not touch beads / `bd`.** The coordinator owns the tracker.
4. **No repo-wide gates.** Other workers are mid-write in neighbouring packages, so
   `go build ./...` or the full test suite will show you THEIR half-written files and you
   will escalate on a phantom. Verify only your own packages, with the exact commands in
   your section.
5. **No formatting runs** beyond `gofmt -w` on files you wrote. Formatting is a final wave.
6. **Do not edit files another worker owns** — the list is in your section. Escalate instead.
7. Read `AGENTS.md` in your worktree before the first edit. Its testing rules are binding,
   especially: a test asserts what a user can do; every external call has a test where it
   fails, paired with one where it succeeds; invariants are stated with BOTH ends.
8. TDD: the failing test comes first, and you run it and see it fail before implementing.
9. Numbers, not adjectives, in your report: counts, exact commands run, every problem you
   saw and deliberately left.
