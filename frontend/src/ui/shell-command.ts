// markShellCommand — the terminal-command token presentation (spec
// 2026-09-15 §1.6): `› git diff --stat` with `git` in accent and
// `diff --stat` in body text, not the generic `.tok-*` rainbow every other
// `.tok-*` consumer (assistant code fences, etc.) keeps unchanged.
//
// The shared tokenizer (shell-highlight.ts) already emits `tok-*` classes on
// both the live CM6 editor and a frozen block's header text — this helper
// changes NEITHER of those. It only marks the ANCESTOR host that scopes how
// those classes are PAINTED (styles/components/shell-command.css), so a
// caller that wants the quieter terminal palette applies it to the element
// it already owns (a header's command span, the editor's DOM root) without
// a second tokenizer or a second `.tok-*` vocabulary.
//
// Vanilla, like Meta and PromptContext: a one-line DOM mutation needs no
// component of its own, and this identity is applied to a host another
// surface builds (blocks.ts's header, editor.ts's CM6 mount), never to an
// element this module creates.

/** The identity class — see styles/components/shell-command.css. */
export const SHELL_COMMAND_CLASS = 'ui-shell-command'

/**
 * Apply the terminal-command presentation to `el` (idempotent — safe to call
 * on every render). `el` must already contain `.tok-*` spans from the shared
 * shell tokenizer (shell-highlight.ts, scrollback/shell-paint.ts); this
 * function never inserts, removes or re-tokenizes document characters.
 */
export function markShellCommand(el: HTMLElement): void {
  el.classList.add(SHELL_COMMAND_CLASS)
}
