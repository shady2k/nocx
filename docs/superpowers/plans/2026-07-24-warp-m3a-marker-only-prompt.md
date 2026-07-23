# Warp Command Experience — M3a: Marker-only prompt mode (shell side) — Implementation Plan

> **For agentic workers:** implement task-by-task, TDD. Steps use `- [ ]`. Binding contract: **ADR-0006** (`docs/decisions/0006-marker-only-prompt-mode.md`) — read it first.

**Goal:** When `NOCX_PROMPT_MODE=marker-only` is set, the integrated shell renders a visually empty, marker-only prompt — reapplied every prompt at the last hook so prompt frameworks can't clobber it — while `NOCX_SHELL_INTEGRATION=1` alone keeps today's visible prompt. Baseline behavior is unchanged when the mode is unset.

**Architecture:** Per ADR-0006: two static-at-spawn env contracts; a per-prompt overlay (NOT a one-time `PS1=''` and NOT save/restore of a captured string); zsh suppressor kept last in `precmd_functions`; bash reordered so `PS1=marker-only` is the final prompt action.

**Tech Stack:** zsh, bash, Go (embedded scripts, exec tests via creack/pty).

## Global Constraints

- **Disjoint files only.** This worker touches ONLY: `internal/shellintegration/scripts/nocx.zsh`, `internal/shellintegration/scripts/nocx.bash`, `internal/shellintegration/scripts.go`, `internal/shellintegration/shellintegration.go`, `internal/shellintegration/scripts_exec_test.go`, and `internal/app/app.go`. Another worker is editing `frontend/**` in parallel — do NOT touch it.
- **Do NOT run the frontend suite or `npm` anything.** Coordinator runs full verification at the end. You MAY run `go test ./internal/shellintegration/...`.
- **Commit only your own files** with explicit paths; on `.git/index.lock` wait 2s and retry.
- **Fail-open (ADR-0006 §5):** when `NOCX_PROMPT_MODE` is unset/empty, behavior is byte-for-byte today's (visible prompt + appended `B`). Never empty `PS2`/`PS3`/`read` prompts.
- Bump `version` in `scripts.go` (scripts changed → `EnsureInstalled` rewrites).

---

### Task 1: Env contract — `NOCX_PROMPT_MODE` + `NOCX_SESSION_ID`

**Files:**
- Modify: `internal/shellintegration/scripts.go` (consts + version bump)
- Modify: `internal/shellintegration/shellintegration.go` (`ActivationEnv`)
- Modify: `internal/app/app.go` (`NewPTY` — pass enhanced flag)
- Test: `internal/shellintegration/shellintegration_test.go`

**Interfaces:**
- Produces: `ActivationEnv(enhanced bool) []string` returning `["NOCX_SHELL_INTEGRATION=1"]` when `enhanced` is false, and additionally `"NOCX_PROMPT_MODE=marker-only"` + `"NOCX_SESSION_ID=<random hex>"` when true. Session id via `crypto/rand` (16 bytes hex).
- Consumes in `app.go`: a config/flag `EnhancedInput bool` on the factory (default **false**). `NewPTY` calls `ActivationEnv(f.enhancedInput)`.

**Acceptance Criteria:**
- `ActivationEnv(false)` returns exactly `["NOCX_SHELL_INTEGRATION=1"]`.
- `ActivationEnv(true)` additionally includes `NOCX_PROMPT_MODE=marker-only` and a non-empty `NOCX_SESSION_ID`.
- Default app wiring passes `false` (feature off) — no behavior change by default.

- [ ] **Step 1: Write the failing test**

```go
// shellintegration_test.go (add)
func TestActivationEnvEnhanced(t *testing.T) {
	base := ActivationEnv(false)
	if len(base) != 1 || base[0] != "NOCX_SHELL_INTEGRATION=1" {
		t.Fatalf("baseline env = %v", base)
	}
	enh := ActivationEnv(true)
	joined := strings.Join(enh, "\n")
	if !strings.Contains(joined, "NOCX_PROMPT_MODE=marker-only") {
		t.Errorf("enhanced env missing NOCX_PROMPT_MODE: %v", enh)
	}
	var sid string
	for _, e := range enh {
		if strings.HasPrefix(e, "NOCX_SESSION_ID=") {
			sid = strings.TrimPrefix(e, "NOCX_SESSION_ID=")
		}
	}
	if sid == "" {
		t.Errorf("enhanced env missing non-empty NOCX_SESSION_ID: %v", enh)
	}
}
```

- [ ] **Step 2: Run to verify failure** — `go test ./internal/shellintegration/ -run TestActivationEnv` → FAIL (signature mismatch / missing symbols).

- [ ] **Step 3: Implement.** In `scripts.go` add consts:

```go
const promptModeEnvVar = "NOCX_PROMPT_MODE"
const promptModeMarkerOnly = "marker-only"
const sessionIDEnvVar = "NOCX_SESSION_ID"
```

Bump `const version` (e.g. `"3"`). In `shellintegration.go` change `ActivationEnv` to take `enhanced bool`:

```go
func (s *Stub) ActivationEnv(enhanced bool) []string {
	env := []string{activationEnvVar + "=1"}
	if enhanced {
		env = append(env,
			promptModeEnvVar+"="+promptModeMarkerOnly,
			sessionIDEnvVar+"="+newSessionID(),
		)
	}
	return env
}

func newSessionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "nocx"
	}
	return hex.EncodeToString(b[:])
}
```

Update the `ShellIntegration` interface `ActivationEnv()` → `ActivationEnv(enhanced bool)`. In `app.go`, add `enhancedInput bool` to `localPTYFactory`, default false where constructed, and call `f.shint.ActivationEnv(f.enhancedInput)`.

- [ ] **Step 4: Run to verify pass** — `go test ./internal/shellintegration/...` PASS; `go build ./...` clean.

- [ ] **Step 5: Commit**

```bash
git add internal/shellintegration/scripts.go internal/shellintegration/shellintegration.go internal/shellintegration/shellintegration_test.go internal/app/app.go
git commit -m "feat(shellint): NOCX_PROMPT_MODE + NOCX_SESSION_ID env contract (nocx-5mn.4, ADR-0006)"
```

---

### Task 2: zsh marker-only overlay (per-prompt, last hook)

**Files:**
- Modify: `internal/shellintegration/scripts/nocx.zsh`
- Test: `internal/shellintegration/scripts_exec_test.go`

**Acceptance Criteria (ADR-0006 §2):**
- With `NOCX_PROMPT_MODE=marker-only`: after each prompt, `PROMPT`/`PS1` contains only the non-printing `OSC 133 B` (no visible glyphs); `RPROMPT`/`RPS1` cleared; reasserted every prompt even if a later `precmd` hook rewrites `PS1`.
- With the mode unset: unchanged — `B` is appended to the visible `PS1`.
- The suppressor `precmd` runs **last** in `precmd_functions` (status-capture stays first).

- [ ] **Step 1: Write the failing exec test** — spawn interactive zsh with a hostile precmd that sets `PS1='HOSTILE$ '`, and assert the rendered prompt after two commands carries no `HOSTILE` when mode is marker-only.

```go
// scripts_exec_test.go (add) — pattern mirrors existing exec tests in this file.
func TestZshMarkerOnlyBeatsHostilePrompt(t *testing.T) {
	out := runInteractiveZsh(t, map[string]string{
		"NOCX_SHELL_INTEGRATION": "1",
		"NOCX_PROMPT_MODE":       "marker-only",
	}, []string{
		`precmd() { PS1='HOSTILE$ ' }`, // user framework rewrites PS1 each prompt
		`add-zsh-hook precmd precmd`,
		`echo one`,
		`echo two`,
	})
	if strings.Contains(out, "HOSTILE") {
		t.Errorf("marker-only prompt was clobbered by a later precmd hook:\n%s", out)
	}
}
```
(If no `runInteractiveZsh` helper exists, add a small creack/pty runner mirroring the file's current OSC-133 exec tests.)

- [ ] **Step 2: Run to verify failure** — `go test ./internal/shellintegration/ -run MarkerOnly` FAIL (HOSTILE present — one-time PS1 is clobbered).

- [ ] **Step 3: Implement** — in `nocx.zsh`, replace the load-time `PS1` append with a mode-aware per-prompt suppressor kept last:

```zsh
# Non-printing B marker (zsh %{...%} so it takes zero prompt width).
__nocx_b_marker=$'%{\e]133;B\a%}'

if [[ "${NOCX_PROMPT_MODE:-}" == "marker-only" ]]; then
    # Enhanced mode: reassert a marker-only prompt AFTER frameworks run, every
    # prompt. Kept last in precmd_functions so a framework precmd that rewrote
    # PS1 cannot win. Do NOT touch PS2/PS3 (continuation/secondary stay native).
    __nocx_marker_only_prompt() {
        PROMPT="$__nocx_b_marker"
        PS1="$__nocx_b_marker"
        RPROMPT=''
        RPS1=''
    }
    add-zsh-hook precmd __nocx_marker_only_prompt
    # Force it last, deduped, on every source.
    precmd_functions=(${precmd_functions:#__nocx_marker_only_prompt} __nocx_marker_only_prompt)
elif [[ -z "${__nocx_prompt_wrapped:-}" ]]; then
    PS1="${PS1:-}""$__nocx_b_marker"
    __nocx_prompt_wrapped=1
fi
```

Add a comment: powerlevel10k **instant prompt** renders before `.zshrc` finishes; under marker-only it should be disabled by the user (documented) or a one-prompt artifact is accepted — nocx cannot retroactively suppress instant-prompt output.

- [ ] **Step 4: Run to verify pass** — `go test ./internal/shellintegration/...` PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/shellintegration/scripts/nocx.zsh internal/shellintegration/scripts_exec_test.go
git commit -m "feat(shellint): zsh marker-only per-prompt overlay, last hook (ADR-0006)"
```

---

### Task 3: bash marker-only overlay (reordered PROMPT_COMMAND)

**Files:**
- Modify: `internal/shellintegration/scripts/nocx.bash`
- Test: `internal/shellintegration/scripts_exec_test.go`

**Acceptance Criteria (ADR-0006 §2):**
- With `NOCX_PROMPT_MODE=marker-only`: `__nocx_prompt_command` runs the user/framework `PROMPT_COMMAND` FIRST, then emits `D`/`A`/`OSC 7`, then sets `PS1` to the marker-only `B` prompt as the **final** action. Array-form `PROMPT_COMMAND` is supported.
- Mode unset: unchanged from today (append `B` to visible `PS1`, markers emitted before the old `PROMPT_COMMAND`).

- [ ] **Step 1: Write the failing exec test** — hostile `PROMPT_COMMAND='PS1="HOSTILE$ "'`; assert no `HOSTILE` in marker-only mode.

```go
func TestBashMarkerOnlyBeatsHostilePrompt(t *testing.T) {
	out := runInteractiveBash(t, map[string]string{
		"NOCX_SHELL_INTEGRATION": "1",
		"NOCX_PROMPT_MODE":       "marker-only",
	}, []string{
		`PROMPT_COMMAND='PS1="HOSTILE$ "'`,
		`echo one`, `echo two`,
	})
	if strings.Contains(out, "HOSTILE") {
		t.Errorf("bash marker-only clobbered by framework PROMPT_COMMAND:\n%s", out)
	}
}
```

- [ ] **Step 2: Run to verify failure** — FAIL (HOSTILE present: current order emits markers then runs old PC which sets PS1 last → wins).

- [ ] **Step 3: Implement** — in `nocx.bash`, make `__nocx_prompt_command` mode-aware and reordered; set marker-only `PS1` last; keep the non-marker-only path exactly as today:

```bash
__nocx_b_marker='\[\e]133;B\a\]'

__nocx_prompt_command() {
    local __nocx_exit=$?
    __nocx_in_prompt_command=1
    # 1) run the user/framework prompt command FIRST (string or array form).
    if [[ -n "${__nocx_old_pc_arr+x}" ]]; then
        local __c; for __c in "${__nocx_old_pc_arr[@]}"; do eval "$__c"; done
    elif [[ -n "${__nocx_old_pc:-}" ]]; then
        eval "$__nocx_old_pc"
    fi
    # 2) emit D/A/OSC7 (was __nocx_precmd).
    __nocx_precmd "$__nocx_exit"
    # 3) in marker-only mode, set the marker-only prompt as the FINAL action.
    if [[ "${NOCX_PROMPT_MODE:-}" == "marker-only" ]]; then
        PS1="$__nocx_b_marker"
    fi
    __nocx_in_prompt_command=0
}
```

Capture the prior `PROMPT_COMMAND` into `__nocx_old_pc` (string) or `__nocx_old_pc_arr` (array) at install. In non-marker-only mode, keep appending `B` to `PS1` as today (the existing PS1 wrap block stays, guarded by the mode check).

- [ ] **Step 4: Run to verify pass** — `go test ./internal/shellintegration/...` PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/shellintegration/scripts/nocx.bash internal/shellintegration/scripts_exec_test.go
git commit -m "feat(shellint): bash marker-only overlay, PROMPT_COMMAND reordered (ADR-0006)"
```

---

## Not in this milestone (tracked, not cut — ADR-0006)
- Frontend-readiness gating before spawning an enhanced PTY, and the state-independent native-mode escape — frontend responsibilities (M3b + follow-on).
- Nested-shell suppression policy via `NOCX_SESSION_ID` matching — deferred; MVP inherits env but the frontend only enables ownership for the top-level session.
- Remote (SSH) enhanced negotiation — deferred (ADR-0006 §6).
