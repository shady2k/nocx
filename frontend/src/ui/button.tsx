/**
 * Button — the terminal's action affordance.
 *
 * The button carries `class="ui-button"` on the `<button>` element and
 * `data-variant` / `data-size` for variance (§3.1 of the design spec).
 *
 * Variant vocabulary: `default` (neutral), `primary` (accent-filled),
 * `danger` (danger outline), `ghost` (a list row), `dashed` (an empty
 * creation slot), and `workspace` (the full-height coloured tab-strip badge).
 *
 * ## When to use each variant
 *
 * - **primary** — the one action a section exists for. At most one per
 *   section. A control that reveals, expands or navigates is NOT a section's
 *   primary, even when it is the only button in the section: disclosure does
 *   not change data. A button rendered once per row of a list is NEVER
 *   primary — the emphasis is spent by repetition.
 * - **default** — everything else that is a real action. This is the default
 *   when no variant is specified.
 * - **danger** — destructive and irreversible.
 * - **ghost** — a control that reads as a row rather than a button
 *   (e.g. the settings rail's nav items).
 * - **dashed** — the empty slot where one more of something is made: the last
 *   column of a board, the tile after the last card. It is not an outline
 *   button and not a quieter `default`; it is a shape that reads as a PLACE
 *   rather than as a thing, which is what stops the reader counting it among
 *   the things beside it. Use it only where activating it CREATES.
 *
 * `class` is intentionally absent as a prop — appearance is locked to
 * the kit (§3.6). Layout and placement belong to a parent wrapper or
 * a typed prop.
 *
 * `secondary` adds a quieter second line beneath the main label. Both lines
 * remain inside the native button, so assistive technology names the complete
 * answer while the visual hierarchy keeps the action readable.
 */
import { Show, splitProps } from 'solid-js'
import type { JSX } from 'solid-js'

export type ButtonVariant = 'default' | 'primary' | 'danger' | 'ghost' | 'dashed' | 'workspace'
export type ButtonSize = 'sm' | 'md'

export interface ButtonProps {
  /**
   * Declared as `never` rather than omitted, which is what makes this work at both
   * levels: `never` refuses the prop at compile time AND keeps it a valid key for
   * `splitProps`, so it can be discarded at runtime too. `Omit` on the intersection
   * gives only the first, and the runtime half is the one that matters — `{...rest}`
   * is spread AFTER the identity, so a class that slips past the type does not add
   * itself, it REPLACES `ui-button` and leaves the element unstyled (§3.6).
   */
  class?: never
  className?: never
  children: JSX.Element
  /** Optional quieter line beneath the button's main label. */
  secondary?: string
  onClick: (e: MouseEvent) => void
  disabled?: boolean
  title?: string
  ariaLabel?: string
  type?: 'button' | 'submit' | 'reset'
  variant?: ButtonVariant
  size?: ButtonSize
  /**
   * Selected, for a button that represents a current choice — a navigation row, a
   * segmented control. Rendered as `aria-selected`, so the state is in the
   * accessibility tree rather than only in the paint, and `button.css` draws it.
   *
   * This exists because the settings rail was painting it from outside: background,
   * colour and weight declared on `.ui-settings-section-nav-active > .ui-button`.
   * A surface may place a component and may not repaint it (§3.6), and a state the
   * component can name is a state the component should draw.
   */
  selected?: boolean
  /**
   * Roving-tabindex participation. Chrome controls that sit inside a toolbar
   * managing their own focus order need -1 so they are not a second tab stop.
   */
  tabIndex?: number
}

/**
 * `class` and `className` are omitted from the intrinsic props deliberately.
 *
 * Removing `class` from ButtonProps was not enough: intersecting with
 * `JSX.IntrinsicElements['button']` hands it straight back, and because it is no
 * longer in `knownKeys` it lands in `rest` and gets spread onto the element — so the
 * escape hatch stayed open while looking closed. Omit is the enforcement; a lint rule
 * would be a second mechanism for something the type system can simply refuse.
 */
type ButtonAttrs = ButtonProps & JSX.IntrinsicElements['button']

export function Button(props: ButtonAttrs) {
  // `class` and `className` are split off and DISCARDED, not forwarded. The type
  // already refuses them, but `{...rest}` is spread after the identity, so anything
  // that slipped past the type — plain JS, a ts-expect-error, a cast — would not
  // merely add a class, it would REPLACE the identity and leave the element unstyled.
  const knownKeys = [
    'class',
    'className',
    'secondary',
    'children',
    'onClick',
    'disabled',
    'title',
    'ariaLabel',
    'type',
    'variant',
    'size',
    'selected',
    'tabIndex',
  ] as const
  const [local, rest] = splitProps(props, knownKeys)
  return (
    <button
      class="ui-button"
      data-variant={local.variant ?? 'default'}
      data-secondary={local.secondary !== undefined ? 'true' : undefined}
      {...(local.size && local.size !== 'md' ? { 'data-size': local.size } : {})}
      type={local.type ?? 'button'}
      disabled={local.disabled === true}
      aria-selected={local.selected === true ? 'true' : undefined}
      title={local.title ?? ''}
      aria-label={local.ariaLabel ?? undefined}
      tabIndex={local.tabIndex}
      onClick={(e: MouseEvent) => local.onClick(e)}
      {...rest}
    >
      <Show when={local.secondary !== undefined} fallback={local.children}>
        <span class="ui-button__label">{local.children}</span>{' '}
        <span class="ui-button__secondary">{local.secondary}</span>
      </Show>
    </button>
  )
}
