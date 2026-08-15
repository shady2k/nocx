/**
 * SuggestionField — a free-text field that offers suggestions (fix-kit-rowlist).
 *
 * The kit's replacement for the native `<datalist>`: a text input whose
 * suggestions render as OUR list, not the platform's. What was wrong with
 * the datalist was ownership — its type, spacing and colours were the
 * browser's, and its lifecycle was not ours (it closed on the very write
 * that carried the keystroke, which is what the guarded value mirror in
 * controlled-value.ts exists to hold closed).
 *
 * The contract:
 *
 * - FREE TEXT always. A suggestion is an addition, never a gate: a value
 *   the list does not contain is typeable and submitted exactly as one the
 *   list offers. The endpoint's models are discovered from the endpoint,
 *   and an endpoint that lists none — GET /models is not universally
 *   implemented — must stay configurable by hand. The list only ever
 *   narrows what is offered; it never refuses a value.
 * - The list floats: it is portalled out of the field's flow so no
 *   ancestor's overflow can clip it (nocx-0plm6 — the endpoint dialog
 *   scrolled and cut the old list at four and a half rows), anchored to
 *   the input's rect, and it CLOSES rather than follows when the page
 *   scrolls under it — a list that floats over content it no longer
 *   anchors to is worse than the list being gone.
 * - Keyboard first: Up/Down move the active option, Enter takes it,
 *   Escape dismisses the list without losing what was typed, and typing
 *   keeps filtering — the list never closes on the input that fills it.
 *   It is a combobox, and it carries the ARIA a combobox needs
 *   (`role="combobox"` + `aria-expanded`/`aria-controls`/
 *   `aria-activedescendant` on the input, `listbox`/`option` on the list).
 * - The list is the caller's to fill, the component's to filter: a prefix
 *   match on the current value (empty value offers everything), so the
 *   caller passes the discovered set and the component decides what to
 *   show. The first match follows the typed value, so Enter takes what the
 *   user is looking at.
 *
 * Composes Field for label/description/error/required exactly like
 * TextField; the input wears its own identity (`ui-suggestion-field__input`)
 * and repeats the base input token references text-field.css carries —
 * kit.css styled all input types from one selector list and the T1 split
 * gives each primitive its own (search-field.css says the same).
 */
import { For, createEffect, createSignal, on, onCleanup } from 'solid-js'
import { Portal } from 'solid-js/web'
import { Field } from './field'
import { mirrorControlledValue } from './controlled-value'

export interface SuggestionFieldProps {
  id?: string
  label?: string
  description?: string
  error?: string
  value: string
  /** Fires on every keystroke AND when a suggestion is taken. */
  onInput?: (value: string) => void
  /** Fires when focus leaves the input, with the current value. */
  onBlur?: (value: string) => void
  /** Fires when the control takes focus. Suggestions are DISCOVERED rather
   *  than known: focus is the moment a person is about to need them, and the
   *  cheapest honest place to go and look. */
  onFocus?: () => void
  /**
   * The values to OFFER. The component filters them by the current value
   * (prefix match); passing the raw discovered set is the whole API. Free
   * text still: the list narrows what is offered, never what is accepted.
   */
  suggestions?: readonly string[]
  placeholder?: string
  disabled?: boolean
  required?: boolean
}

export function SuggestionField(props: SuggestionFieldProps) {
  const inputId = () => props.id ?? ''
  const listId = () => `${inputId()}-suggestions`
  const descriptionId = () => (props.description ? `${inputId()}__desc` : undefined)
  const errorId = () => (props.error ? `${inputId()}__error` : undefined)
  const ariaDescribedBy = () => [descriptionId(), errorId()].filter(Boolean).join(' ') || undefined

  const [open, setOpen] = createSignal(false)
  const [focused, setFocused] = createSignal(false)
  const [active, setActive] = createSignal(-1)

  // The portalled list's anchor and its mount. Plain DOM refs: the
  // component reads them imperatively, never reactively.
  let inputElement: HTMLInputElement | undefined
  let listElement: HTMLUListElement | undefined

  /**
   * Where the list floats. A native modal `<dialog>` renders in the browser
   * TOP LAYER, which no z-index can float anything above — only parentage
   * can. So the list mounts into the nearest owning `<dialog>` element
   * itself (a sibling of its panel), which escapes the panel's clipping
   * (nocx-0plm6) while staying above the dialog. Outside a dialog it mounts
   * on document.body, floating above the page like the context menu.
   */
  const portalMount = () => inputElement?.closest('dialog') ?? document.body
  /** Prefix match on the typed value; an empty value offers everything. */
  const filtered = () => {
    const q = String(props.value).trim().toLowerCase()
    const all = props.suggestions ?? []
    if (q === '') return all
    return all.filter((s) => s.toLowerCase().startsWith(q))
  }

  /** The popup is expanded only when there is something to show. */
  const expanded = () => open() && filtered().length > 0

  /**
   * Suggestions can arrive AFTER focus — the caller discovers them over the
   * wire, and there is no list yet at the moment of focus. Open the moment
   * they land; the person who focused the field is waiting for them.
   *
   * `on` fires only when the COUNT changed, and only a 0 → n transition is
   * an arrival: closing the list (take, Escape) changes no suggestion count,
   * so a list the user just closed stays closed.
   */
  createEffect(
    on(
      () => props.suggestions?.length ?? 0,
      (n, prev) => {
        if (prev === 0 && n > 0 && focused() && !open()) setOpen(true)
      },
    ),
  )

  /**
   * The first match follows the typed value: Enter takes what the user is
   * looking at, and a filter that matches nothing clears the highlight.
   * Tracked on the VALUE (the filter's source), never on open/active: merely
   * opening the list highlights nothing — the user has not chosen yet — and
   * an explicit close is not undone.
   */
  createEffect(
    on(
      () => String(props.value),
      () => {
        if (!expanded()) return
        const n = filtered().length
        const a = active()
        setActive(n === 0 ? -1 : a >= n ? n - 1 : a < 0 ? 0 : a)
      },
    ),
  )

  /** Gap between the input and the floating list. */
  const FLOAT_GAP_PX = 4

  /**
   * Anchor the list to the input's viewport rect and match its width. The
   * model field sits low in a tall dialog — below is exactly where there is
   * no room — so when the space below cannot hold the list, it flips above
   * the input. Measured when the list opens; the dismissal effect below
   * guarantees the anchor cannot move while the list is up.
   */
  createEffect(() => {
    if (!expanded()) return
    const input = inputElement
    const list = listElement
    if (!input || !list) return
    const rect = input.getBoundingClientRect()
    const height = list.offsetHeight
    const roomBelow = window.innerHeight - rect.bottom
    const roomAbove = rect.top
    const flip = roomBelow < height + FLOAT_GAP_PX && roomAbove > height + FLOAT_GAP_PX
    list.style.left = `${rect.left}px`
    list.style.top = `${flip ? rect.top - height - FLOAT_GAP_PX : rect.bottom + FLOAT_GAP_PX}px`
    list.style.width = `${rect.width}px`
  })

  /**
   * Dismissal while open. Two listeners, two jobs:
   *
   * - An outside pointerdown closes the list. The list is portalled, so it
   *   is no longer under the input's blur for free: a click on something
   *   that does not take focus (the dialog panel, a label) never blurs the
   *   input. The input itself and the list are contained — a pointer on an
   *   option must land, never dismiss.
   * - Any scroll closes the list. The list anchors to the input's VIEWPORT
   *   rect; if that anchor moves, the list shows what it no longer sits
   *   next to. Follow-the-input was the alternative, and a list chasing a
   *   scrolling dialog is a second scrolling surface fighting the first.
   *   So it closes instead: one ArrowDown reopens it, and a stale list
   *   over unrelated content can never happen. The one scroll that is not
   *   an anchor move — the list's OWN long-list scrolling — is contained.
   */
  createEffect(() => {
    if (!expanded()) return
    const onPointerDown = (e: PointerEvent): void => {
      const input = inputElement
      const list = listElement
      if (e.target instanceof Node) {
        if (input && input.contains(e.target)) return
        if (list && list.contains(e.target)) return
      }
      setOpen(false)
      setActive(-1)
    }
    const onScroll = (e: Event): void => {
      const list = listElement
      if (list && e.target instanceof Node && list.contains(e.target)) return
      setOpen(false)
      setActive(-1)
    }
    const onResize = (): void => {
      setOpen(false)
      setActive(-1)
    }
    document.addEventListener('pointerdown', onPointerDown)
    document.addEventListener('scroll', onScroll, true)
    window.addEventListener('resize', onResize)
    onCleanup(() => {
      document.removeEventListener('pointerdown', onPointerDown)
      document.removeEventListener('scroll', onScroll, true)
      window.removeEventListener('resize', onResize)
    })
  })

  const onInput = (e: Event) => {
    const value = (e.currentTarget as HTMLInputElement).value
    props.onInput?.(value)
    // Typing never closes the list — it re-filters it. This is the exact
    // defect being replaced: the datalist shut itself on the keystroke.
    setOpen(true)
  }

  const onKeyDown = (e: KeyboardEvent) => {
    const n = filtered().length
    switch (e.key) {
      case 'ArrowDown':
        e.preventDefault()
        if (!expanded()) {
          setOpen(true)
          setActive(n > 0 ? 0 : -1)
        } else {
          setActive(Math.min(active() + 1, n - 1))
        }
        break
      case 'ArrowUp':
        e.preventDefault()
        if (!expanded()) {
          setOpen(true)
          setActive(n > 0 ? n - 1 : -1)
        } else {
          const a = active()
          // From nothing active, Up lands on the LAST option; from the top
          // it clamps rather than wrapping.
          setActive(a < 0 ? n - 1 : a === 0 ? 0 : a - 1)
        }
        break
      case 'Enter': {
        const a = active()
        if (expanded() && a >= 0 && a < n) {
          // Take the option AND keep the key out of the dialog's own Enter
          // handling: while the list is open, Enter belongs to the combobox,
          // never to the form (nocx-0plm6). With the list closed, Enter is
          // left to bubble — the form submits exactly as it always did.
          e.preventDefault()
          e.stopPropagation()
          take(filtered()[a])
        }
        break
      }
      case 'Escape':
        if (expanded()) {
          // Dismiss the list, never the dialog it sits in, and never what
          // was typed: the value is untouched and focus stays in the input.
          e.preventDefault()
          e.stopPropagation()
          setOpen(false)
          setActive(-1)
        }
        break
    }
  }

  /** Take a suggestion through the SAME channel as typing — onInput — so
   *  the caller treats a pick exactly like the value it replaced. */
  const take = (value: string) => {
    props.onInput?.(value)
    setOpen(false)
    setActive(-1)
  }

  const onBlur = (e: FocusEvent) => {
    setFocused(false)
    setOpen(false)
    setActive(-1)
    props.onBlur?.((e.currentTarget as HTMLInputElement).value)
  }

  const optionId = (index: number) => `${listId()}-option-${index}`

  return (
    <div class="ui-suggestion-field">
      <Field
        for={inputId()}
        label={props.label}
        description={props.description}
        error={props.error}
        required={props.required}
      >
        <div class="ui-suggestion-field__control">
          <input
            class="ui-suggestion-field__input"
            id={inputId() || undefined}
            role="combobox"
            aria-expanded={expanded()}
            aria-controls={listId()}
            aria-autocomplete="list"
            aria-activedescendant={expanded() && active() >= 0 ? optionId(active()) : undefined}
            aria-invalid={props.error !== undefined ? true : undefined}
            aria-describedby={ariaDescribedBy()}
            placeholder={props.placeholder ?? ''}
            disabled={props.disabled === true}
            required={props.required === true}
            ref={(element) => {
              inputElement = element
              // mirrorControlledValue reads the accessor inside its own
              // createEffect (a tracked scope); the gate cannot see across
              // that helper boundary.
              // eslint-disable-next-line solid/reactivity -- helper-boundary contract
              mirrorControlledValue(element, () => props.value)
            }}
            onInput={onInput}
            onKeyDown={onKeyDown}
            onFocus={() => {
              setFocused(true)
              setOpen((props.suggestions?.length ?? 0) > 0)
              props.onFocus?.()
            }}
            onBlur={onBlur}
          />
          <Portal mount={portalMount()}>
            <ul
              id={listId()}
              role="listbox"
              class="ui-suggestion-field__list"
              hidden={!expanded()}
              ref={(el) => {
                listElement = el
              }}
            >
              <For each={filtered()}>
                {(s, i) => (
                  <li
                    id={optionId(i())}
                    role="option"
                    aria-selected={active() === i()}
                    class="ui-suggestion-field__option"
                    onMouseDown={(e) => e.preventDefault()}
                    onClick={() => take(s)}
                    onMouseEnter={() => setActive(i())}
                  >
                    {s}
                  </li>
                )}
              </For>
            </ul>
          </Portal>
        </div>
      </Field>
    </div>
  )
}
