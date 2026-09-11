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
   The permission captures (Bash and Write dialogs) pass
   `.claude/skills/nocx-detection-verify/permission-ask-settings.json` as `[settings]`, which
   asks (rather than auto-allows or auto-denies) `Bash(touch marker.txt)`, so the recorded
   dialog is a real approval prompt rather than a tool call that ran unattended.
   It builds `agent-capture`, runs `claude` under `-env-file` in a fresh `/var/tmp/nocx-detect-*` directory
   and refuses if Claude would read any configuration outside it — including a `HOME` or
   `CLAUDE_CONFIG_DIR` that is missing or that points at real configuration, and a symlinked run
   directory or ancestor. `run.env` inside that directory sets both. Scripts live in
   `internal/agentdriver/testdata/captures/scripts/lmstudio-*.script`.
   - `claude` must be on `PATH`; the script fails immediately, with a message, if it is not.
   - API refused: endpoint `http://127.0.0.1:9`.
   - API waiting ("Waiting for API response · will retry in"): do NOT use a listener that
     accepts a connection and never answers (`python3 -c 'import socket;s=socket.socket();
     s.bind(("127.0.0.1",18999));s.listen();c=[s.accept() for _ in range(64)]'`) — tried twice
     for nocx-nru89.9, once at `lmstudio-api.script`'s documented 40s final wait and once with
     the wait extended to 190s (~228s of real elapsed time, just under this script's 240s hard
     capture ceiling), and in both runs Claude's TUI never advances past a plain, ever-growing
     spinner. It treats an accepted-but-silent connection as still in flight, not a failure, so
     it never draws this chrome. The corpus's only recorded evidence of it is
     `claude-lmstudio-turn@72000` (manifest.json's `api-waiting` entry), where it appeared
     incidentally during a real slow LM Studio response. Reproducing it deliberately likely
     needs a connection that answers with a mid-stream failure rather than one that never
     answers at all — untried as of nocx-nru89.9.
   - `<out without .jsonl>.meta.json`, beside the capture itself (not in the `/var/tmp` run
     directory, which is deleted along with everything else once the run is done), records what
     actually ran: the resolved `claude` binary and its digest, the run's own environment variable
     names, the names (never values) of any variables the resolved launcher itself sets or
     prefixes — the Nix wrapper does — and the Claude version (`-version-arg --version`, run once
     under the same isolated environment). Read it before trusting a capture, and before
     committing it: it must hold no home path, email address, token or private network address —
     variable names, `/nix/store` paths and `/var/tmp/nocx-detect-*` paths are fine, because only
     names are ever recorded, never values (so `ANTHROPIC_BASE_URL`'s endpoint never lands in it).
     A launcher that could not be parsed (a binary, not a wrapper script) says so instead of
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
