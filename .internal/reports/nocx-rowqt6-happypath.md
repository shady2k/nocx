# External Claude worker happy path

Task: `nocx-rowqt.6`.

The automated check is `TestExternalClaudeDrivesAWorkerEndToEnd` in
`internal/app/worker_happypath_test.go`. It starts the real worker endpoint, pane grid,
authenticated lifecycle channel, worker record, and pane-backed launcher, then re-executes
the test binary as a separate external coordinator. That process calls the published worker
socket in this order:

1. `workers.spawn` creates a worker pane and returns a live participant.
2. `workers.wait` waits for the worker declaration.
3. `workers.holdings` reads the declared summary while the worker remains live.
4. `workers.close` closes the worker and returns `ended: true`.

The parent then reads the worker record and checks all of these facts: one completed worker,
the coordinator session as its group, a non-empty external worker session, the exact
worker declaration, and one real pane watch. The external process calls the JSON-RPC worker
surface directly; no assistant model or `internal/assistant` model loop is constructed.
The shared `assistant.ToolDispatcher` remains only the endpoint's existing capability
dispatch seam. Herdr is not used.

The CI worker command is a local `claude`-shaped executable because CI cannot require a
subscription CLI. It writes the staged declaration surface and is explicitly not evidence
that the vendor model selected these calls. The manual run below is the separate vendor
evidence.

## Mutation evidence

The test was run red twice, with deliberate mutations:

```text
$ NOCX_TEST_WORKER_HAPPY_MUTATION=skip-report go test ./internal/app -run TestExternalClaudeDrivesAWorkerEndToEnd -count=1 -v
    worker_happypath_test.go:597: external coordinator: exit status 1
        stdout: --- FAIL: TestWorkerHappyExternalCoordinator (11.07s)
            worker_happypath_test.go:101: worker "050a01dadf4b09263469673f61f80187" summary = "", want "read AGENTS.md and reported from an external worker\n"
        FAIL
--- FAIL: TestExternalClaudeDrivesAWorkerEndToEnd (11.14s)
FAIL
FAIL	github.com/shady2k/nocx/internal/app	11.160s
FAIL
```

`skip-report` models a worker that never declares its result. The external cycle therefore
cannot report a successful declaration.

```text
$ NOCX_TEST_WORKER_HAPPY_MUTATION=wrong-summary go test ./internal/app -run TestExternalClaudeDrivesAWorkerEndToEnd -count=1 -v
    worker_happypath_test.go:597: external coordinator: exit status 1
        stdout: --- FAIL: TestWorkerHappyExternalCoordinator (0.15s)
            worker_happypath_test.go:101: worker "9569607941a5971c23ddc728b1e03199" summary = "a different result\n", want "read AGENTS.md and reported from an external worker\n"
        FAIL
--- FAIL: TestExternalClaudeDrivesAWorkerEndToEnd (0.22s)
FAIL
FAIL	github.com/shady2k/nocx/internal/app	0.235s
FAIL
```

`wrong-summary` models a declaration for the wrong work. The exact-summary assertion rejects
it, so neither mutation can claim a green cycle.

## Manual vendor run

The installed vendor CLI reported:

```text
$ claude --version
2.1.260 (Claude Code)
```

The manual coordinator test launches the real Claude CLI with a temporary MCP configuration
pointing at `go run ./cmd/nocx-helper mcp`; its worker command launches a second real Claude
process in the worker pane. The repeatable command is:

```text
$ NOCX_MANUAL_REAL_COORDINATOR=1 go test ./internal/app -run TestManualRealClaudeCoordinator -count=1 -v
```

Observed worker lifecycle and record evidence from the run:

```text
2026/09/07 17:44:03 INFO worker participant spawned participant=1b536286a2137e37eb09048caf594383 worker=37eaed37f1442b4aa70f3ce259367a00 session_id=7555e5b93c0353d7e33ee2a22b1ef4f5 pane_id=01a07c53-83c5-76d4-886a-9e7f2504ac60
2026/09/07 17:44:03 INFO agent enrolled lane=lane-436bcc95c952cb21 session_id=7555e5b93c0353d7e33ee2a22b1ef4f5 agent=claude cols=120 rows=24
2026/09/07 17:44:27 INFO worker participant reported participant=1b536286a2137e37eb09048caf594383 ok=true
2026/09/07 17:44:30 INFO worker participant closed participant=1b536286a2137e37eb09048caf594383 session_id=7555e5b93c0353d7e33ee2a22b1ef4f5
    worker_happypath_test.go:705: manual worker record: id=1b536286a2137e37eb09048caf594383 group=37eaed37f1442b4aa70f3ce259367a00 state=completed session=7555e5b93c0353d7e33ee2a22b1ef4f5 summary="read AGENTS.md and reported from an external worker\n"
--- PASS: TestManualRealClaudeCoordinator (40.63s)
PASS
ok   github.com/shady2k/nocx/internal/app	40.650s
```

Claude's final stream reported these four direct calls and results:

```text
1. mcp__nocx__workers_spawn
{"id":"1b536286a2137e37eb09048caf594383","state":"live"}
2. mcp__nocx__workers_wait
{"participants":[{"id":"1b536286a2137e37eb09048caf594383","state":"live","task":"read AGENTS.md and report","summary":"read AGENTS.md and reported from an external worker\n"}]}
3. mcp__nocx__workers_holdings
{"participants":[{"id":"1b536286a2137e37eb09048caf594383","state":"live","task":"read AGENTS.md and report","summary":"read AGENTS.md and reported from an external worker\n"}]}
4. mcp__nocx__workers_close
{"id":"1b536286a2137e37eb09048caf594383","ended":true}
```

## Verification notes

The prescribed formatting and vet gates were clean:

```text
$ gofmt -l internal/app
(no output)
$ go vet ./internal/app/...
(no output)
```

The first uncached full app gate reported one cleanup failure in
`TestEpicE2E_MaxSessions1LeavesAWorkingUnintegratedPrompt/a_typed_ssh`:

```text
carrier_epic_e2e_test.go:713: MEASURED the named reason: generation-unavailable
carrier_epic_e2e_test.go:734: MEASURED a typed ssh under MaxSessions 1: 1 authentication(s)
testing.go:1464: TempDir RemoveAll cleanup: unlinkat /tmp/TestEpicE2E_MaxSessions1LeavesAWorkingUnintegratedPrompta_typed1765773782/001/home: directory not empty
```

The same test passed at the required baseline commit `f75b60ca` in a detached
worktree (`ok github.com/shady2k/nocx/internal/app 134.241s`). The current tree then
passed an uncached rerun (`go test: 2 packages ok`). The initial red was
therefore not reproducible on the rerun; the full command output remains in the worker
transcript.

The manual run did not involve herdr. The test creates its own temporary runtime directory,
content store, endpoint, pane grid, sessions, and worker record. A reader can repeat the
run from this worktree with the command above; the temporary MCP config is generated by the
test and the helper is built by `go run`.
