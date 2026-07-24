// Submit orchestration for the command editor (ADR-0004 §2, nocx-4ff.12).
// The ordering is the fix: tell the state machine we left the prompt so
// ownership clears, refocus the grid so PS2 continuation + running-program
// keys reach the PTY, THEN send the bracketed-paste handoff.
export interface SubmitDeps {
  dispatchSubmit(): void
  focusGrid(): void
  sendDoc(doc: string): void
}

export function submitCommand(doc: string, deps: SubmitDeps): void {
  deps.dispatchSubmit()
  deps.focusGrid()
  deps.sendDoc(doc)
}
