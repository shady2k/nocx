---
name: nocx-detection-verify
description: Re-record Claude Code screen moments for internal/agentdriver's manifest after a Claude update, isolated from the person's Claude account and configuration, and check the shipped rule against them.
---

# Re-recording Claude's screen moments

Use after `claude --version` changes, or when a pane's reported state looks wrong.

1. **Endpoint.** An Anthropic-compatible local model (the owner's LM Studio; see bead `nocx-34r0i`
   notes 13). Check it: `curl -s <endpoint>/v1/models` lists the model, and a `/v1/messages` call with one
   tool definition answers `stop_reason: tool_use`. Never use an Anthropic account for recording.
2. **Record** with `.claude/skills/nocx-detection-verify/record.sh <endpoint> <model> <script> <out> [cols] [settings]`.
   It builds `agent-capture`, runs `claude` under `-env-file` in a fresh `/var/tmp/nocx-detect-*` directory
   and refuses if Claude would read any configuration outside it — including a `HOME` or
   `CLAUDE_CONFIG_DIR` that is missing or that points at real configuration, and a symlinked run
   directory or ancestor. `run.env` inside that directory sets both. Scripts live in
   `internal/agentdriver/testdata/captures/scripts/lmstudio-*.script`.
   - `claude` must be on `PATH`; the script fails immediately, with a message, if it is not.
   - API refused: endpoint `http://127.0.0.1:9`.
   - API waiting: start `python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",18999));s.listen();c=[s.accept() for _ in range(64)]'`
     in another pane and use endpoint `http://127.0.0.1:18999`.
   - `meta.json` in the run directory records what actually ran: the resolved `claude` binary and its
     digest, the run's own environment variable names, the names (never values) of any variables the
     resolved launcher itself sets or prefixes — the Nix wrapper does — and the Claude version
     (`-version-arg --version`, run once under the same isolated environment). Read it before trusting a
     capture; a launcher that could not be parsed (a binary, not a wrapper script) says so instead of
     silently reporting no additions.
3. **Place marks by reading the replay:** `go run ./cmd/agent-capture replay -at <ms,...> <capture>` around each
   script step, until the moment's identifying text (spec §6.4) is on screen. Never place a mark by timing
   alone.
4. **Update `internal/agentdriver/testdata/captures/manifest.json`:** one entry per inventory moment —
   recorded (capture, `atMs`, the owner's state) or `unverified` with the reason. Add the capture name to
   `captureNames` in `capture_test.go` and a row plus the Claude version to the captures `README.md`.
5. **Check:** `go test ./internal/agentdriver/ -run TestTheManifestHolds -count=1`. A disagreement goes to the
   owner, who decides whether the rule, the mark or the label is wrong; a rule change comes with a test that
   is red first.
6. **Never** approve a tool call, work outside the run directory, or commit a capture from a directory that
   was not isolated.
