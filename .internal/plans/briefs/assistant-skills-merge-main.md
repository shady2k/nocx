You are integrating current `origin/main` into this feature branch and adapting the
skills feature to an upstream refactor. Work ONLY in
/home/dev/.herdr/worktrees/nocx/feat-ai-skills, branch feat/ai-skills.

READ FIRST: AGENTS.md — the operating contract. Two parts matter most here: the
commit-message rule, and "Look for the existing answer before you write a second
one". Also run `bd recall lesson-nocx-resolving-a-conflict-where-two-branches` and
read it before you resolve anything — it is about this exact repo and this exact
kind of merge, and its lesson is that a resolution can be correct LINE BY LINE and
still drop a rule that had no name to grep for.

Bead: nocx-si0dh. The commit subject ends with "(nocx-si0dh)".

WHY THIS IS NEEDED

The branch was cut from an older main. Running `./scripts/ci-linux.sh --no-keyring
-- ./internal/transport/...` in the container gives five deterministic failures:

    TestRunLease_WallClockTerminalizesAndTheLedgerNamesIt
    TestRunLease_InactivityTerminalizesAndTheLedgerNamesIt
    TestRunLease_EscalationReachesTermForAnIntIgnoringProcess
    TestRunLease_EscalationReachesKillForAnIntAndTermIgnoringProcess
    TestRunLease_OutputBudgetBoundsAndTheBlockNamesIt

I bisected them: they are red at Task 0 (84a0c8b8), which touches only
internal/agenttools — so they are NOT ours. They are the terminal-margin
fragility upstream already fixed in `50e4d107 fix(transport): the run-lease tests
stop letting the terminal margin decide (nocx-3n0f3)` and `3056116d fix(transport):
the output budget test streams output instead of racing its own accounting
(nocx-qmtr2)`. Merging main brings both. Do not try to fix those tests yourself.

WHAT THE MERGE ACTUALLY REQUIRES — I started it and aborted; here is what I found.

`git merge origin/main` conflicts in 12 files, 18 hunks. Most are unions (both
sides added a field, an option or an import). The substantive ones:

1. **`Declaration.Effect` changed type upstream: `content.Effect` → `[]content.Effect`**
   (internal/agenttools/registry.go). Upstream calls it "the set of classes this
   tool can resolve to"; `session.run` now declares observe, mutate-reversible,
   mutate-destructive, delegate AND cross-boundary. Our four `skills.*`
   declarations still use the singular form and will not compile. Adapt them —
   each skills row states the set it can actually resolve to, not a copy of a
   neighbour's list.

2. **Task 7's rule depends on that field.** `isSkillMutationTool` in
   internal/assistant/kernel.go reads `tool.ScopeFamily == "skill" && tool.Effect
!= content.EffectObserve`. With a SET, "is not observe" is no longer a
   comparison. Restate the rule so it still means what it was bought for: EVERY
   skills mutation always asks the person, whatever their standing decision for
   that effect class. The test that guards it is
   `TestASkillsWriteNeverAutoPermits` / `TestASkillsDeleteNeverAutoPermits` in
   internal/assistant/skills_write_policy_test.go — they must still pass, and
   they are the definition of done for this part.
   Check `ForGrant`'s `effectPermitted[t.Effect]` the same way: with a set, "the
   grant permits this tool" needs a decision about ANY vs ALL. Upstream already
   made that decision for its own rows — find it and use it, do not invent a
   second answer.

3. **The run fence.** internal/transport/ws_readscreen.go. Main added
   `{Kind: content.ResourceDestination, ID: "*"}` for its new `fetch.url` tool;
   we replaced the `content` root with family roots. Both belong. The resolution:

       scopes := []content.GrantScope{
           {Kind: content.ResourceSession, ID: sessionID},
           {Kind: content.ResourcePath, ID: "/"},
           {Kind: content.ResourceContent, ID: "note"},
           {Kind: content.ResourceContent, ID: "snippet"},
           {Kind: content.ResourceDestination, ID: "*"},
       }
       if s.skillsEnabled() {
           scopes = append(scopes, content.GrantScope{Kind: content.ResourceContent, ID: "skill"})
       }
       g := p.AsGrant(scopes)

4. **internal/agenttools/registry_test.go** — main added
   `TestDeclarationsHaveExpectedEffectSets` with an explicit map and
   `len(declarations) != 17`. Keep it AND our two tests, and extend the map with
   the four skills rows and the count with them. Do not delete either side's test.

5. The rest are unions: ws.go (skillLibrary+agentTools vs agentFetcher, and their
   two With… options), ws_agent.go (the agenttools and apifetch imports; the
   promptFacts call keeps our `skillRefs` argument AND main's
   `automaticSessionItems`; AskParams gets Skills+SkillDraft AND Fetcher),
   assistant.go, engine.go, execute.go — in each, both sides added a distinct
   field and both are needed. registry.go's two comment conflicts take MAIN's
   text (it describes main's larger tool table) and its import hunk keeps both
   `slices` and `net/url`.

HOW TO WORK

- Merge, resolve, then make it compile, then make the package tests pass. Do not
  amend history or rebase; a merge commit is what this is.
- After resolving, re-read each function you touched TOP TO BOTTOM against both
  sides, and ask of every rule on the side you dropped: does the survivor still
  refuse what that one refused? That is the lesson bead's instruction and it is
  the part a compiler cannot do for you.
- Run: go test ./internal/agenttools/... ./internal/assistant/... ./internal/skill/... ./internal/transport/... ./internal/app/... ./internal/content/...
  and `npm --prefix frontend run contracts:check`.
- Do NOT run the container, e2e, Playwright, docker or make ci — I run those.
- git add explicit paths. No push. No bd.
- Never --no-verify.

FINAL REPORT: the merge commit sha, every conflict and how you resolved it (one
line each), what you changed for findings 1 and 2 and why that still enforces
"a skills mutation always asks", the test commands and their results, and anything
you could not resolve confidently.
