// THE frontend half of `assistant.expandReasoning` (nocx-y9e88): whether an
// answer's thinking note opens by itself. Declared in Go
// (internal/settings/settings.go: AssistantExpandReasoning) and applied here,
// exactly the way `terminal.wrapOutput` is applied by output-wrap.ts — one
// attribute on the document root, one owner of the question.
//
// It differs from output-wrap in one respect and the difference is forced by
// the platform: a block's wrap is a CSS decision, so the attribute alone is
// enough, while `<details open>` is DOM state that no stylesheet can set. So
// this module does the second half itself — the notes already on screen are
// opened or closed when the value CHANGES.
//
// That repaint is what stops the setting from being contradicted by the
// surface a person is looking at (AGENTS.md: a soft degrade the UI
// contradicts is how a feature that does not exist survives a release). It
// fires on a CHANGE and never on a re-application, which is what keeps the
// setting a default rather than a lock: any settings write refetches the
// whole snapshot and applies every key again, so repainting unconditionally
// would reopen a note the person had just closed every time they changed
// their theme.

/** The declared key. Named here, once, like OUTPUT_WRAP_KEY: a consumer of
 *  one specific setting has to say which. */
export const REASONING_EXPANDED_KEY = 'assistant.expandReasoning'

/** The declared default (internal/settings/settings.go:
 *  AssistantExpandReasoning). Used before the first snapshot arrives and
 *  whenever the fetch fails — the answer is what a person came for, so a
 *  note that has not heard from the backend stays shut. */
export const REASONING_EXPANDED_DEFAULT = false

/** Does a note built RIGHT NOW start open? Read at construction by the
 *  answer flow, so a note inherits the value that was current when the model
 *  started thinking. */
export function reasoningStartsExpanded(root: HTMLElement = document.documentElement): boolean {
  const painted = root.dataset.reasoningExpanded
  return painted === undefined ? REASONING_EXPANDED_DEFAULT : painted === 'on'
}

/** Paint the decision on the document root, and bring the notes already on
 *  screen into line with it. Only a real boolean moves it: a snapshot that
 *  does not carry the key (an older backend, a failed fetch) leaves the
 *  declared default in place rather than guessing. */
export function applyReasoningExpanded(
  value: unknown,
  root: HTMLElement = document.documentElement,
): void {
  const on = typeof value === 'boolean' ? value : REASONING_EXPANDED_DEFAULT
  const next = on ? 'on' : 'off'
  const changed = root.dataset.reasoningExpanded !== next
  root.dataset.reasoningExpanded = next
  if (!changed) return
  for (const el of root.querySelectorAll<HTMLDetailsElement>('details.ui-reasoning')) {
    el.open = on
  }
}
