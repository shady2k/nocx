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
   and refuses if Claude would read any configuration outside it. Scripts live in
   `internal/agentdriver/testdata/captures/scripts/lmstudio-*.script`.
   - API refused: endpoint `http://127.0.0.1:9`.
   - API waiting: start `python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",18999));s.listen();c=[s.accept() for _ in range(64)]'`
     in another pane and use endpoint `http://127.0.0.1:18999`.
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
