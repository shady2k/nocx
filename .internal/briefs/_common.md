# Ground rules — every worker on epic nocx-9wsb5

**Run `pwd` first.** Your worktree path is given in your own brief. Every path in the
design document is repo-relative; resolve them against YOUR checkout, never against
another. Creating a file under someone else's tree while editing yours relatively is a
silent split-brain that reports success.

**The design is the contract:**
`.internal/specs/2026-08-29-notification-centre-refinements-design.md`
Read the sections your brief names, in full, before writing anything. It is committed on
your base, so it is in your tree.

**TDD.** Red, then green, then refactor. The failing test comes first. The acceptance
criteria in your brief are written as assertions on purpose — turn them into tests, do
not paraphrase them into prose.

**Do NOT:**

- commit, push, branch, or touch `git stash`
- run repo-wide gates: no `make ci`, no `make ci-full`, no `./scripts/ci-*.sh`,
  no `e2e/run-in-container.sh`, no `go test ./...`, no full `npm test`.
  Parallel workers share this machine; a whole-project run compiles a neighbour's
  half-written file and you will escalate on a phantom blocker. The coordinator runs the
  gates once, at the end, on the merged tree.
- run any formatter across the repo. Formatting is a final single-worker wave.
- touch the issue tracker (`bd`). The coordinator owns it.
- edit files another worker owns. Your brief names them. Escalate instead.

**DO verify, scoped to your own files:**

- Go: `go build ./...` and `go test ./internal/<your package>/...` — nothing wider.
  Note that `go build` does NOT compile `_test.go`; `go vet ./internal/<pkg>/...` does.
- TypeScript, **BOTH projects**:
  `cd frontend && ./node_modules/.bin/tsc --noEmit -p tsconfig.json && ./node_modules/.bin/tsc --noEmit -p tsconfig.test.json`.
  That exact binary — `npx tsc` can fail to resolve in a fresh worktree. This is not a
  repo-wide gate and you may not skip it: vitest transpiles and strips types, so your
  tests can pass while your files do not compile. The second project is the one that
  catches a fixture your change made stale — a test building an object the type now
  requires another field on — and it is the one a worker who ran only the first has
  never seen. `npm run typecheck` runs both.
- TypeScript tests, scoped: `cd frontend && ./node_modules/.bin/vitest run <your files>`.
- If `frontend/node_modules` is missing, run `cd frontend && npm ci` once.

**Report in numbers, not adjectives.** Tests before and after. Every problem you saw and
deliberately left. Anything you could not verify — silence is not "nothing to report".

**When you finish**, print the completion line from the last section of your own brief
file. It is written only there, never in anything sent to your terminal, so it cannot
match itself.
