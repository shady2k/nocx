# Unprompted coordinator persuasion measurement

Task: `nocx-rowqt.7`.

## Question

Given a task that plainly has independent parts, with no mention of worker tools in the
user prompt, does a real Claude launched with the nocx worker surface choose
`workers.spawn`?

## Vendor and harness

The installed vendor CLI reported:

```text
$ claude --version
2.1.260 (Claude Code)
```

The MCP bridge was the temporary staged Unix-socket fixture used for this measurement. It
returned the worker-tool catalogue from `internal/agenttools/registry.go`, recorded every
JSON-RPC request, returned an empty `workers.holdings` result at the start of each run, and
returned a live participant for each `workers.spawn` request. It did not instruct the model
to call a worker. The fixture was not product code; its process was stopped after the
measurement and its files remained outside the repository.

The exact user prompt, unchanged across all attempts, was:

```text
You are responsible for completing a repository task. It has three independent deliverables that can proceed concurrently: backend validation, frontend behavior, and acceptance evidence. Start the work immediately and finish by reporting your plan and progress.
```

The exact command, run from this worktree, was:

```text
claude --print --no-session-persistence --dangerously-skip-permissions --tools "" --setting-sources "" --strict-mcp-config --mcp-config /tmp/nocx-persuasion-mcp.json --max-turns 5 --output-format stream-json --verbose -p "You are responsible for completing a repository task. It has three independent deliverables that can proceed concurrently: backend validation, frontend behavior, and acceptance evidence. Start the work immediately and finish by reporting your plan and progress."
```

The command was run three times, saving raw streams as
`/tmp/nocx-persuasion-clean1.jsonl`, `clean2.jsonl`, and `clean3.jsonl`. The fixture call
log was `/tmp/nocx-persuasion-calls.jsonl`.

## Result

| Attempt | `workers.spawn` selected | Other worker calls observed        |
| ------: | :----------------------: | :--------------------------------- |
|       1 |           yes            | `workers.holdings`, `workers.wait` |
|       2 |           yes            | `workers.holdings`, `workers.wait` |
|       3 |           yes            | `workers.holdings`, `workers.wait` |

**3/3 attempts (100%) selected `workers.spawn` without any worker-tool mention in the
user prompt.** The selected worker was a read-only reconnaissance worker in each run; the
model did not fan out all three deliverables before the five-turn cap. The acceptance
question is whether it chose the worker surface, and that answer was yes in all three
attempts.

The raw MCP log recorded `workers.spawn` once in each clean attempt. It also recorded
`tools.catalogue` once per attempt, confirming that the model received the shared registry
descriptions through the staged bridge rather than a second prompt copy.

## Single-owner check

No staging instruction text was added. The worker-tool descriptions remain solely owned by
`internal/agenttools/registry.go`; the shell launcher stages only the private MCP bridge
configuration and appends no competing worker instructions. Therefore this bead did not
need a second copy of the worker descriptions or a situational prompt. The current
single-owner boundary is documented in the registry's `Declaration.Description` comment
and in the worker rows themselves. There is no automated assertion that scans launch text
for duplicated descriptions; that part remains a documented convention, not a mechanically
enforced guarantee.

## Repeatability and limits

A stranger can repeat the measurement with the committed manual check:

```text
$ NOCX_MANUAL_REAL_PERSUASION=1 go test ./internal/claudeconformance -run '^TestManualClaudeSelectsWorkersUnprompted$' -count=1 -v
```

`TestManualClaudeSelectsWorkersUnprompted` stages the same in-process MCP bridge and
catalogue fixture used by the other conformance tests. The fixture derives each worker
summary, parameter schema, and result schema from the assembled
`internal/agenttools/registry.go` rows, so a registry description change cannot silently
leave a second test copy behind. The test is manual-gated and does not spend an account
unless `NOCX_MANUAL_REAL_PERSUASION=1` is explicitly set.

The result is an empirical count, not a guarantee about all future prompts, models, or
Claude versions. The five-turn cap bounds the paid run; it means the measurement proves
worker-surface selection, not completion of the requested repository work.
