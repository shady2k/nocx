/**
 * SearchField — a text input that looks like a search box: magnifier glyph
 * inside the field, on the leading edge.
 *
 * The icon lives in the component rather than in each caller's CSS, because a
 * search field without one is not a search field — it is a text box that
 * happens to filter, and every surface would otherwise have to remember to
 * draw the affordance itself.
 *
 * Two forms share the same identities (`ui-search-field` + `__icon` + `__input`):
 * the interactive `SearchField` component (React) and the read-only display
 * `createSearchFieldDisplay` (vanilla DOM) below. The recall overlay uses the
 * display form: its keys are owned by the editor's keyboard arbiter before any
 * element sees them, so a focusable input would be a lie — the field states
 * where typing goes without pretending to accept it. The identities being the
 * same is the point: one vocabulary for "a search field", whether it takes
 * focus or not.
 *
 * Justified by callers:
 * - settings.ts: st-search wrapper + st-search-input, type='text', placeholder, aria-label
 * - settings-content.ts: type='search' variant, st-search class on input
 */

import { SearchIcon } from './icons'

export interface SearchFieldProps {
  value: string
  onInput: (value: string) => void
  placeholder?: string
  ariaLabel?: string
  disabled?: boolean
  onKeyDown?: (e: KeyboardEvent) => void
}

export function SearchField(props: SearchFieldProps) {
  const onInput = (e: Event) => {
    const target = e.currentTarget as HTMLInputElement
    props.onInput(target.value)
  }

  return (
    <span class="ui-search-field">
      <span class="ui-search-field__icon">
        <SearchIcon />
      </span>
      <input
        type="search"
        class="ui-search-field__input"
        value={props.value}
        placeholder={props.placeholder ?? ''}
        aria-label={props.ariaLabel ?? undefined}
        disabled={props.disabled === true}
        onKeyDown={(e) => props.onKeyDown?.(e)}
        onInput={onInput}
      />
    </span>
  )
}

/**
 * The same magnifier the React form renders (Lucide `search`, ISC) — a Solid
 * component cannot be invoked from the vanilla factory, so the markup is
 * restated here; keeping the two in one module is what stops them drifting.
 */
const SEARCH_ICON_SVG =
  '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" ' +
  'stroke="currentColor" stroke-width="2" stroke-linecap="round" ' +
  'stroke-linejoin="round" aria-hidden="true"><circle cx="11" cy="11" r="8"/>' +
  '<path d="m21 21-4.3-4.3"/></svg>'

export interface SearchFieldDisplayOptions {
  /** The text the field carries — the recall overlay's filter. */
  value: string
  /** Shown via `data-placeholder` when `value` is empty. */
  placeholder?: string
  ariaLabel?: string
}

/**
 * The kit's search field as a read-only display: magnifier, the value, a
 * caret, and a placeholder when empty — but NO focusable input. The caller
 * owns all keys (the recall overlay's arbiter captures them before any
 * element), so the field is the panel's statement of "this is where typing
 * goes", not an interactive control. The value is emitted as text on
 * `data-empty`-marked elements so the CSS can switch placeholder and caret
 * without the caller re-rendering.
 */
export function createSearchFieldDisplay(opts: SearchFieldDisplayOptions): HTMLElement {
  const root = document.createElement('span')
  root.className = 'ui-search-field'

  const icon = document.createElement('span')
  icon.className = 'ui-search-field__icon'
  icon.innerHTML = SEARCH_ICON_SVG
  root.appendChild(icon)

  const input = document.createElement('span')
  input.className = 'ui-search-field__input'
  input.dataset.display = 'true'
  if (opts.value === '') input.dataset.empty = 'true'
  if (opts.placeholder !== undefined && opts.placeholder !== '') {
    input.dataset.placeholder = opts.placeholder
  }
  input.setAttribute('role', 'searchbox')
  if (opts.ariaLabel !== undefined) input.setAttribute('aria-label', opts.ariaLabel)
  if (opts.value !== '') input.appendChild(document.createTextNode(opts.value))

  const caret = document.createElement('span')
  caret.className = 'ui-search-field__caret'
  caret.setAttribute('aria-hidden', 'true')
  input.appendChild(caret)

  root.appendChild(input)
  return root
}
