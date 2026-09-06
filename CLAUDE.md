# CLAUDE.md

Agent working rules for this repo live in **[AGENTS.md](AGENTS.md)** — it is the operating
contract, including the git authority rules and the tracker. It is imported here, so it is
always in context:

@AGENTS.md

Sources of truth: [`docs/vision.md`](docs/vision.md) (what & why) and
[`docs/architecture.md`](docs/architecture.md) (the binding architecture spine).
The task backlog lives in beads — run `br ready`, or `scripts/br-queue.sh` when the
question is "what next".

**Fresh clone?** The [README setup](README.md#agent-tooling) lists the tooling to
install per machine — `br`, `cm`, and the `beads-superpowers` Claude Code plugin.
`make init` wires up the repo but installs none of them.

## The tracker is `br`, and the plugin still says `bd`

Since 2026-09-05 the tracker is **`br` (beads_rust)**: SQLite plus a tracked
`.beads/issues.jsonl`, no Dolt, no daemon, and **no git — `br` never runs it.**
Memories left the tracker with `bd`; they live in cass-memory (`cm`).

The `beads-superpowers` plugin is kept for its process skills and still speaks `bd`.
**AGENTS.md carries the full `bd` → `br` table, and it wins over any skill that says
otherwise** — that is the plugin's own rule about repository instructions. The ones
you will hit first:

```bash
scripts/br-queue.sh                # what to work on next  (was: bare `bd ready`)
br show <id>                       # view an issue
br update <id> --claim             # claim work
br close <id> --reason "..."       # complete work, with evidence a stranger can check
cm context "<what you are doing>"  # memories  (was: `bd memories`)
```

Still forbidden, unchanged: TodoWrite, TaskCreate and markdown TODO lists. `br` is the
tracker for all work, including your own checklists.

## Session completion

Subordinate to whatever the user actually asked for. `br` will not do any of the git
steps for you.

1. **File beads for remaining work** — anything that needs follow-up, before you forget it.
2. **Run the quality gates** if code changed. Which ones, and whose job they are, is in
   AGENTS.md under Git authority: a worker runs the unit tests for what it touched, the
   coordinator runs `make ci-full` on the merged tree.
3. **Update issue status** — close what is finished, and set anything you stopped holding
   back to `open` in the same minute. An unheld bead in `in_progress` is invisible to
   `br ready` and to every colleague looking for work.
4. **Send the backlog out with the code:**

   ```bash
   br sync --flush-only
   git add .beads/issues.jsonl
   git commit          # same commit as the code it describes, or one right beside it
   git push
   ```

5. **Write down anything that was bought.** If something in this session cost a
   measurement or a wrong turn and is not derivable from the repository, it goes in
   `.cass/playbook.yaml` — AGENTS.md has the three tests and the `import --repo`
   route. Nothing does this for you: there is no reflection hook, and `cm reflect`
   never runs on its own.
6. **Hand off** — changed files, what you validated, bead status, and anything you left
   blocked, in those words.

## Code search

`grep`, `glob` and reading the file are one way in; the `repowise` MCP tools are the
other, and neither outranks the other. Use `grep` for _does this exist, and who calls
it_, and `repowise` when the question is about history or risk. Read
[AGENTS.md](AGENTS.md#code-search) first: it says what `repowise` may and may not be
believed about, and why an earlier index (`graphify`) was removed outright.
