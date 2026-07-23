// Input-ownership state machine (ADR-0004 §1). nocx owns keyboard input ONLY in
// PROMPT_READY; every other state routes keys raw to the PTY. Enhanced states are
// reachable ONLY from OSC 133 markers and the xterm alt-buffer event — never
// inferred from bytes/termios. Fail-open: anything unexpected falls back to RAW.
export type InputState = 'RAW' | 'PROMPT_READY' | 'RUNNING_RAW' | 'ALT_SCREEN'

export type InputEvent =
  | { type: 'marker'; kind: 'A' | 'B' | 'C' | 'D' }
  | { type: 'buffer'; buffer: 'normal' | 'alternate' }
  | { type: 'submit' }
  | { type: 'reset' }
  | { type: 'exit' }

export interface Machine {
  state: InputState
  // The current prompt→C→D cycle reached C through a clean A/B. Consumers gate
  // block actions (e.g. re-run) on this; anomalies clear it (Task 3).
  trusted: boolean
  // DOM keyboard ownership is authorized ONLY after A→B (ADR-0006 §4).
  // A alone leaves owned:false; C/submit/alt-buffer/reset/exit clear it.
  owned: boolean
}

export function initialMachine(): Machine {
  return { state: 'RAW', trusted: false, owned: false }
}

export class InputStateController {
  private machine = initialMachine()
  private subs: Array<(m: Machine) => void> = []

  get state(): InputState { return this.machine.state }
  get trusted(): boolean { return this.machine.trusted }

  dispatch(e: InputEvent): void {
    const next = reduce(this.machine, e)
    if (next.state === this.machine.state && next.trusted === this.machine.trusted && next.owned === this.machine.owned) {
      this.machine = next
      return
    }
    this.machine = next
    for (const cb of this.subs) cb(next)
  }

  onChange(cb: (m: Machine) => void): void { this.subs.push(cb) }
}

export function reduce(m: Machine, e: InputEvent): Machine {
  switch (e.type) {
    case 'buffer':
      return e.buffer === 'alternate'
        ? { state: 'ALT_SCREEN', trusted: false, owned: false }
        : { state: 'RAW', trusted: false, owned: false }
    case 'reset':
    case 'exit':
      return { state: 'RAW', trusted: false, owned: false }
    case 'submit':
      return { state: 'RUNNING_RAW', trusted: m.trusted, owned: false }
    case 'marker':
      switch (e.kind) {
        case 'A':
          // Fresh prompt. Trusted only when we arrived cleanly (from RAW after a
          // finished command or from initial). An A that interrupts RUNNING_RAW
          // (no D, or a nested prompt) is a resync: PROMPT_READY but untrusted.
          // Ownership requires the full A→B sequence (ADR-0006 §4).
          return { state: 'PROMPT_READY', trusted: m.state !== 'RUNNING_RAW', owned: false }
        case 'B':
          // B only means "input ready" when we are already at a prompt.
          // DOM ownership is granted ONLY when an A preceded this B.
          return { state: 'PROMPT_READY', trusted: m.state === 'PROMPT_READY' && m.trusted, owned: m.state === 'PROMPT_READY' }
        case 'C':
          // Command start. Trusted only if a clean prompt preceded it; an orphan
          // or nested C runs raw but disables downstream actions.
          return { state: 'RUNNING_RAW', trusted: m.state === 'PROMPT_READY' && m.trusted, owned: false }
        case 'D':
          // Finished — only meaningful while a command is running. Orphan D
          // (e.g. empty Enter emits D with no preceding C) is ignored.
          return m.state === 'RUNNING_RAW' ? { state: 'RAW', trusted: m.trusted, owned: false } : m
      }
  }
}
