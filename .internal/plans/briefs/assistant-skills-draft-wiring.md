You are implementing ONE task in the nocx repo (Go backend + SolidJS frontend).
Work ONLY in /home/dev/.herdr/worktrees/nocx/feat-ai-skills, branch feat/ai-skills.

READ FIRST, in this order:

1. AGENTS.md — the operating contract. The five testing rules and the
   commit-message rule.
2. .internal/specs/2026-08-31-assistant-skills-design.md — sections 6, 7 and 8.
3. .internal/plans/2026-09-01-assistant-skills.md — the "Global Constraints"
   section (lines 1-30) and "### Task 4" (lines 857-954), which contains the code
   and the reasoning for what you are wiring.

Your bead id is nocx-9ysnr. Every commit subject ends with "(nocx-9ysnr)".

WHAT IS ALREADY THERE, AND WHY THIS IS A SEPARATE TASK

Task 4 landed `profile.RoleSummarizing` and, in `internal/assistant/skilldraft.go`,
`ComposeDraftInput` and `DraftSkill`. They have NO production caller. Task 4's plan
entry named the consumer as the kernel handing the draft to the person's approval as
proposed `skills.create` arguments — but `skills.create` did not exist until Task 6,
which came after. Task 4 was sequenced before the thing it consumes. Task 6 has since
landed `skills.create/update/delete`, and Task 7 landed the rule that every skill
mutation asks the person, carrying the classifier verdict and the scan finding with
the proposal. Your job is the missing join.

The Task 6 worker confirmed the shapes line up: `DraftSkill` returns
(name, description, body), which is exactly what `skills.create` accepts.

WHAT TO BUILD

- `internal/assistant/kernel.go` resolves `profile.RoleSummarizing` through
  `profile.ResolveRole` — that function is THE ONE role resolver in the product and
  you must not grow a second one — and builds its model client the way
  `internal/assistant/classifier.go` builds the classifier's. Read that file first:
  it is the pattern for "a second model call needs an endpoint, a model and a
  credential", and it already solved it.
- The draft's name, description and body become the proposed `skills.create`
  arguments the person sees in the approval. Task 7's approval payload already
  carries the finding and the classifier fact; do not build a parallel one.
- The whole flow — the person says "remember how to do this", the assistant drafts,
  the person is asked, the skill is written — is exercised in ONE test.
- A failed summarizing call does not block the ask: the assistant answers the
  question, says it could not draft the skill and why, and nothing is written.
  Test that, one case per failure: unassigned role, endpoint gone, call error.
- An unassigned `summarizing` spends the answering role's endpoint, which is what
  the roles surface already tells the person (nocx-0s2gh.3). Do not invent a
  different fallback.

EVIDENCE REQUIRED IN YOUR REPORT — a claim that it is wired is not enough:

    deadcode -tags gtk3 -whylive 'github.com/shady2k/nocx/internal/assistant.DraftSkill' ./...

Paste the output. Be aware, and say so if you see it: RTA reports a method reached
through an interface as reflection-reachable, so a path that runs through
reflect.Value.Call proves nothing. If the probe is inconclusive, the real evidence is
a test that goes through the production seam — write that instead and name it.

RULES — these override anything the plan or a skill says:

- TDD: the failing test first, RUN it, see it fail, then implement.
- Run ONLY: go test ./internal/assistant/... ./internal/profile/... ./internal/transport/...
  Nothing containerized, no e2e, no make ci, no Playwright, no docker.
- git add explicit paths; never -am, never -A. No push, no merge, no rebase, no bd.
- If the pre-commit hook fails, fix the cause; never --no-verify.
- If the plan's code contradicts the repo, trust the repo, make the smallest correct
  change, and say so.

FINAL REPORT: commit sha, files changed, the red-then-green output, the -whylive
output with your reading of it, and anything you deliberately left out.
