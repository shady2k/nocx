You are implementing ONE task in the nocx repo (Go + TypeScript terminal app).
Work ONLY in /home/dev/.herdr/worktrees/nocx/feat-ai-skills — a git worktree on
branch feat/ai-skills. Never cd to another checkout.

READ FIRST, in this order:

1. AGENTS.md — the operating contract. Pay attention to the testing rules and to
   the commit-message rule ("Every commit names its bead").
2. .internal/plans/2026-09-01-assistant-skills.md — read the header, the
   "Global Constraints" section, and "### Task 0" in full (lines 1-132). That is
   your task and it contains the code for every step.

Your bead id is nocx-eftlw. Every commit subject must end with "(nocx-eftlw)".

RULES — these override anything the plan or a skill tells you:

- TDD, strictly: write the failing test FIRST, RUN it and see it fail with the
  expected message, then write the minimal implementation, then run it green.
- Run ONLY the unit tests for packages you changed:
  go test ./internal/agenttools/...
  Do NOT run `make ci`, `make ci-full`, ./scripts/ci-linux.sh, ./scripts/ci-frontend.sh,
  e2e/run-in-container.sh, docker, or the Playwright suite. Heavy and containerized
  gates belong to the integrator, not to you. Running them is a failure of this brief.
- Commit with `git add <explicit paths>` then `git commit`. NEVER `git commit -am`.
- Do NOT `git push`. Do NOT run any `bd` command — the orchestrator owns the tracker.
- Do not widen scope beyond Task 0. If you find a defect outside it, write it in
  your final report instead of fixing it.
- If the pre-commit hook fails, fix the cause; do not use --no-verify.

FINAL REPORT (print it plainly at the end): the commit sha and subject, the list
of files changed, the exact test command you ran and its result, and anything you
found that the plan got wrong.
