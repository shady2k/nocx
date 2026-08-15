// THE frontend half of `terminal.wrapOutput` (nocx-ex636): the default wrap
// for a command block's output, declared in Go and applied here as one
// attribute on the document root. The CSS reads it (style.css,
// `:root[data-output-wrap]`); nothing else in the renderer branches on the
// setting, so there is one owner of "does an untouched block wrap".
//
// The per-block ⋮ override is deliberately untouched by this: a block the
// person switched carries `data-wrap` and the setting's rules exclude it.
// The setting says what a block does UNTIL somebody says otherwise, which
// is why changing it repaints the untouched blocks and leaves the rest.

/** The declared key. Named here, once, for the same reason main.tsx names
 *  `tab.placement`: a consumer of one specific setting has to say which. */
export const OUTPUT_WRAP_KEY = 'terminal.wrapOutput'

/** The declared default (internal/settings/settings.go: OutputWrap). Used
 *  before the first snapshot arrives and whenever the fetch fails — the
 *  first frame must not paint the opposite of what the backend will say. */
export const OUTPUT_WRAP_DEFAULT = true

/** Paint the decision on the document root. Only a real boolean moves it:
 *  a snapshot that does not carry the key (an older backend, a failed
 *  fetch) leaves the declared default in place rather than guessing. */
export function applyOutputWrap(
  value: unknown,
  root: HTMLElement = document.documentElement,
): void {
  const on = typeof value === 'boolean' ? value : OUTPUT_WRAP_DEFAULT
  root.dataset.outputWrap = on ? 'on' : 'off'
}
