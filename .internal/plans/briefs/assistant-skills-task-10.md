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
   "Global Constraints" section (lines 1-30), then "### Task 10" in full
   (lines 1273-1303). That is your task; it contains the code for every step.

Your bead id is nocx-hzd6t. Every commit subject must end with "(nocx-hzd6t)".

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
- Do not widen scope beyond Task 10. If you find a defect outside it, put it in
  your final report instead of fixing it.
- If the plan's code does not compile or contradicts what is actually in the
  repo, trust the repo, make the smallest correct change, and say so in your
  report. Do not silently redesign the task.

FINAL REPORT — print it plainly at the end, and keep it short:

- the commit sha(s) and subject(s)
- the files you changed
- the exact test commands you ran and their results
- anything the plan got wrong, and anything you deliberately left out

CONTEXT YOU NEED THAT THE PLAN DOES NOT CARRY:

Everything the spec described is on this branch now. Read the closed beads for the
shape if you need it, but the short version:

- Skills live under `<ConfigDir>/skills` (authored), an embedded builtin root, and
  `<ConfigDir>/managed-skills` (written by the assistant). Precedence is
  authored > builtin > managed.
- `skills.read` is offered only when the run's grant covers the `skill` family;
  the grant's content scopes are FAMILY ROOTS (`note`, `snippet`, `skill`), and
  the Settings toggle omits `skill` when skills are off.
- Every skill mutation always asks the person, whatever the policy says, and the
  approval carries the classifier's verdict and any static-scan finding.
- On a `skills.create` proposal the kernel REPLACES the model's own fields with a
  draft generated from the person's question and the assistant's prose only. So
  the name, description and body in the approval are the generated ones — assert
  against those, not against anything the fake model proposed.
- A skill whose bytes changed since approval leaves the prompt index until the
  person approves it again (`skills.approve`).

YOUR SCOPE IS THE SPEC FILE ONLY. Write it, make it as correct as you can by
reading the code, and STOP. Do NOT run it: `e2e/run-in-container.sh`, Playwright,
docker and `make ci` are the integrator's, and running one is a failure of this
brief. If you cannot tell whether a selector or a fixture exists, read the
neighbouring assistant e2e specs and say in your report what you could not verify.
