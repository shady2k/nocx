You are fixing ONE defect in the nocx repo. Work ONLY in
/home/dev/.herdr/worktrees/nocx/feat-ai-skills, branch feat/ai-skills.

READ FIRST: AGENTS.md (the operating contract — the testing rules and the
commit-message rule).

Bead: nocx-u68z1. The commit subject ends with "(nocx-u68z1)".

THE DEFECT — it exists only in the merge, and both branches were green.

Task 5 (121dc24a) fenced files.read/files.edit/files.create out of the skill
roots. It passed the roots to the registry from internal/app/app.go like this:

    []string{skillRoots[0].Dir, skillRoots[1].Dir}

Task 2 (d41028d1) then inserted the BUILTIN root into the middle of that same
slice, and its Dir is "" because a builtin root is an embed.FS, not a directory.
The two commits touch different lines, so the merge was clean and silent. Look at
internal/app/app.go:554-558 and :1600 as they stand now: the fence is handed
["<config>/skills", ""], the empty string is discarded by an IsAbs guard inside
refuseSkillPath, and the MANAGED root — the one that holds model-drafted skill
bodies, which is the entire reason the fence exists — is no longer fenced. A model
can write a SKILL.md there with files.create today.

WHAT TO DO — one commit, TDD as always.

1. The failing test comes first, and it must fail on the tree as it is now.
   Put the derivation where the type lives: add to internal/skill a function that
   takes []skill.Root and returns the filesystem roots — every root with a
   non-empty Dir — and test it with exactly the three-root list app.go builds
   (authored dir, builtin FS, managed dir), asserting BOTH directory roots come
   back and the builtin contributes nothing. Positional indexing is what broke;
   the fix is that nothing downstream may depend on position or count.

2. Then make internal/app/app.go call it instead of indexing. Do not hand-write a
   second filter at the call site — that is the same mistake in a new place.

3. Second, smaller finding in the same commit. internal/assistant/assistant.go:
   NewClient calls NewClientAndRegistry(..., nil) and newClient calls
   assembleToolRegistry(toolsFS, nil), so both build a registry with NO fence, in
   silence. The comment directly above NewClient says the safety floor is
   "mandatory so production and tests cannot silently omit it" — the skill roots
   got the opposite treatment and are the same kind of boundary. Make the omission
   impossible to make by accident: no nil default that silently disables the
   fence. Choose the smallest change that achieves that (a required parameter, or
   an explicit named constructor for the no-filesystem-roots case that says in its
   name and comment that it is for callers with no skill roots) and say in the
   commit body why you chose that shape.

RULES:

- Run ONLY: go test ./internal/skill/... ./internal/agenttools/... ./internal/assistant/... ./internal/app/...
  Nothing containerized, no e2e, no make ci, no Playwright.
- git add explicit paths; never -am, never -A. No push, no merge, no rebase, no bd.
- If the pre-commit hook fails, fix the cause; never --no-verify.

FINAL REPORT: the commit sha, files changed, the exact red-then-green test output
for step 1, and which shape you chose for step 3 and why.
