You are implementing ONE task in the nocx repo (Go backend + SolidJS/xterm.js
frontend). Work ONLY in /home/dev/.herdr/worktrees/nocx/feat-ai-skills — a git worktree
on branch feat/ai-skills. Never cd to another checkout, and never touch the
original repository root.

READ FIRST, in this order:

1. AGENTS.md — the operating contract. Pay real attention to the five testing
   rules and to the commit-message rule ("Every commit names its bead").
2. .internal/specs/2026-08-31-assistant-skills-design.md — the design this task
   implements. Read at least the sections your task's plan entry names.
3. .internal/plans/2026-09-01-assistant-skills.md — read the header and the
   "Global Constraints" section (lines 1-30), then "### Task 3" in full
   (lines 776-856). That is your task; it contains the code for every step.

Your bead id is nocx-2424x. Every commit subject must end with "(nocx-2424x)".

RULES — these override anything the plan, a skill, or your own judgement says:

- TDD, strictly: write the failing test FIRST, RUN it, see it fail with the
  expected message, then write the minimal implementation, then run it green.
- Run ONLY the unit tests for the packages you actually changed:
  Go: go test ./internal/<pkg>/... (one per package you touched)
  frontend: npx vitest run <the spec file> (from frontend/)
  Do NOT run `make ci`, `make ci-full`, `make test`, ./scripts/ci-linux.sh,
  ./scripts/ci-frontend.sh, e2e/run-in-container.sh, docker, or Playwright.
  Heavy and containerized gates belong to the integrator, not to you. Running
  one is a failure of this brief — it costs minutes of shared machine and tells
  you nothing your package tests did not.
- Commit with `git add <explicit paths>` then `git commit`. NEVER `git commit -am`
  and never `git add -A` — this branch carries other people's untracked files.
- Do NOT `git push`. Do NOT `git merge`, `git rebase` or switch branches.
- Do NOT run any `bd` command — the orchestrator owns the issue tracker.
- Do NOT use `git stash` — the stash stack is shared across worktrees on this
  machine and a bare pop takes someone else's work.
- If the pre-commit hook fails, fix the cause. Never `--no-verify`.
- Do not widen scope beyond Task 3. If you find a defect outside it, put it in
  your final report instead of fixing it.
- If the plan's code does not compile or contradicts what is actually in the
  repo, trust the repo, make the smallest correct change, and say so in your
  report. Do not silently redesign the task.

FINAL REPORT — print it plainly at the end, and keep it short:

- the commit sha(s) and subject(s)
- the files you changed
- the exact test commands you ran and their results
- anything the plan got wrong, and anything you deliberately left out

EXTRA STEP, INHERITED FROM TASK 2 — do this FIRST, as its own commit before
starting Task 3 proper, and name bead nocx-a788n in that commit subject:

Task 2 shipped the builtin root but left its first acceptance criterion
unasserted: "skill-authoring is discovered in a fresh profile with no
directories on disk". Every existing test hands Discover roots that exist.
The fresh-profile shape is the state every new user starts in, and the failure
it guards against — a root whose directory is absent aborting or poisoning
discovery — is real.

Write, in internal/skill/builtin_test.go, a test that builds exactly the root
list internal/app/app.go builds (authored dir, builtin FS, managed dir) where
BOTH directory roots point at paths that do NOT exist, and assert that Discover
returns exactly the one builtin skill and no error state. Run it red first if
you can make it red; if it passes immediately, say so in your report and keep it
— an acceptance criterion with no assertion is the gap, whether or not the code
already satisfies it.
