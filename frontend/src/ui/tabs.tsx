/**
 * Tabs — a list of sections and the pane of the selected one.
 *
 * For a record being edited in a dialog whose fields do not fit one column:
 * the sections are views of one thing, and switching between them is not
 * navigation.
 *
 * Default orientation is `vertical` — the list is a rail down the leading edge,
 * the pane fills the rest. That is the default rather than an equal alternative
 * because a horizontal strip fails at exactly the point this component is for:
 * Tabby's own connection editor wraps its seven tabs onto a second row, and a
 * wrapped strip stops reading as one control. A rail grows downward without
 * changing shape. `horizontal` is there for the two-section case, where a rail
 * is more furniture than the content deserves.
 *
 * ## Deliberately not shared with two things that look identical
 *
 * The **settings rail** is the same shape and a different job: its sections are
 * pages with deep links and their own identity, so they are navigation, and
 * calling them tabs would put a `tabpanel` in the accessibility tree that does
 * not exist. It keeps its own rail. A `semantics` prop was written here and
 * removed before it shipped — variance with no consumer is speculation, and the
 * moment to add it is the moment something asks.
 *
 * The **terminal tab strip** (`tab-strip.tsx`, vertical variant) implements the
 * same ARIA pattern down to the roving tabindex, and is still a different
 * component: its membership is live and user-mutable (create, close by middle
 * click, reorder by drag) and its rows carry activity and agent indicators and
 * a title driven by the PTY. `feature-components.json` records that decision.
 * What the two genuinely share is about 25 lines of keyboard handling; if a
 * third consumer ever appears, the thing to extract is that helper, not a
 * component.
 *
 * THE IDENTITY CONTRACT — the same one row-list.tsx states, bought a second
 * time and in a worse way.
 *
 * Rows and panels are keyed by POSITION (`<Index>`), never by the item
 * object, and each panel's `content` is captured ONCE for its position. The
 * reason is what callers actually pass: an `items` array written inline in
 * JSX. Any signal that expression reads — a label carrying a count, a status
 * derived from the form — makes Solid rebuild the whole array on every
 * keystroke, with brand-new item objects in it. Keyed by reference, `<For>`
 * then disposed and rebuilt every panel, which destroyed the input the
 * person was typing into: THE FOCUS LEFT THE FIELD AFTER EVERY CHARACTER.
 * That is the defect this file was carrying, and it was reachable from any
 * caller whose tab labels were not constants.
 *
 * A captured `content` closure does NOT go stale: it reads its own
 * surface's props and signals, which are live. What it must not do is
 * capture a VALUE — the same rule row-list states for `row()`.
 *
 * The consequence, stated so nobody has to rediscover it: the tab at a
 * position is the same tab for the life of the component. Reordering the
 * items array or swapping which section sits at index 2 is not supported —
 * change what a section RENDERS, never which section a position is.
 */

import { Index, Show, children, untrack, type JSX } from 'solid-js'
import { Button } from './button'
import { StatusDot } from './status-dot'

export interface TabItemStatus {
  /** The visual tone of the row's status indicator. */
  tone: 'ok' | 'warning' | 'error'
  /** An accessible name for the status, read by assistive technology. */
  accessibleName: string
}

export interface TabItem {
  id: string
  label: string
  /** The section's content. Called for every section, not only the current one. */
  content: () => JSX.Element
  /** Optional status indicator shown on the row. No status = unchanged rendering. */
  status?: TabItemStatus
}

export interface TabsProps {
  items: TabItem[]
  /** The current item's id. */
  active: string
  onChange: (id: string) => void
  orientation?: 'vertical' | 'horizontal'
  ariaLabel?: string
  /**
   * A slot at the trailing end of the tab row — HORIZONTAL only.
   *
   * For what belongs to the WHOLE set of sections and is the same whichever
   * one is open: the run card puts its status, size and elapsed here, and
   * those three are facts about the exchange rather than about the view of it
   * a person happens to have chosen.
   *
   * NOT for a control drawn on one tab only. That was this slot's first use —
   * the request form's body kind and auth scheme — and it made the row that
   * NAMES the sections also hold one section's contents, swapped under the
   * tabs as a person moved between them. It also made the row overflow: the
   * bar measured 566px in a 496px column and the excess travelled up until
   * the surface drew a horizontal scrollbar (nocx-kdawd). A control present
   * for exactly one section IS that section's content, and it belongs at the
   * top of the panel it governs (nocx-n9npi). The test is mechanical: if what
   * you are about to pass is wrapped in a `<Show when={active === …}>`, it
   * does not go here.
   *
   * It sits beside the tablist and never inside it: a `tablist` whose
   * children are not tabs is a broken one, so the row is a box holding both.
   */
  actions?: JSX.Element
}

export function Tabs(props: TabsProps) {
  const orientation = () => props.orientation ?? 'vertical'
  const actions = children(() => props.actions)

  const move = (delta: number) => {
    const items = props.items
    const i = items.findIndex((t) => t.id === props.active)
    if (i < 0) return
    // Wraps, as the pattern specifies: the end of the list is not a wall.
    const next = items[(i + delta + items.length) % items.length]
    props.onChange(next.id)
  }

  const onKeyDown = (e: KeyboardEvent) => {
    const vertical = orientation() === 'vertical'
    switch (e.key) {
      case vertical ? 'ArrowUp' : 'ArrowLeft':
        e.preventDefault()
        move(-1)
        break
      case vertical ? 'ArrowDown' : 'ArrowRight':
        e.preventDefault()
        move(1)
        break
      case 'Home':
        e.preventDefault()
        if (props.items.length > 0) props.onChange(props.items[0].id)
        break
      case 'End':
        e.preventDefault()
        if (props.items.length > 0) props.onChange(props.items[props.items.length - 1].id)
        break
    }
  }

  return (
    <div class="ui-tabs" data-orientation={orientation()}>
      <div class="ui-tabs__bar">
        <div
          class="ui-tabs__list"
          role="tablist"
          aria-orientation={orientation()}
          aria-label={props.ariaLabel ?? undefined}
          onKeyDown={onKeyDown}
        >
          {/* A row is a ghost Button with `selected` — which is what the settings
            rail already is. A second way to draw "the current choice in a list"
            is the duplication the kit exists to prevent, and Button's own doc
            names this as its case. */}
          <Index each={props.items}>
            {(item) => (
              <Button
                variant="ghost"
                selected={props.active === item().id}
                role="tab"
                id={`ui-tab-${item().id}`}
                aria-controls={`ui-tabpanel-${item().id}`}
                // Roving tabindex: one tab stop for the whole list.
                tabIndex={props.active === item().id ? 0 : -1}
                onClick={() => props.onChange(item().id)}
              >
                {/* `item()` is read INSIDE the row, so a label that changes —
                    a count, a dirty mark — updates the text in place instead
                    of replacing the button. */}
                <Show when={item().status} fallback={item().label}>
                  {(status) => (
                    <StatusDot tone={status().tone} accessibleName={status().accessibleName}>
                      {item().label}
                    </StatusDot>
                  )}
                </Show>
              </Button>
            )}
          </Index>
        </div>
        {/* READ ONCE, THROUGH `children`. A JSX element passed as a prop is
            a GETTER: every access re-creates the whole subtree it describes.
            Guarding this with `<Show when={props.actions}>` read it twice
            per evaluation — and re-evaluated whenever anything that subtree
            read had changed, which for a control bound to the form being
            edited is every keystroke. Each pass built two fresh Selects,
            mounted one and left the other with its effects: the tab stopped
            responding within a minute of typing.
            `children()` is the kit's guarantee that it is read exactly once
            however the caller wrote it, and the wrapper is unconditional
            because an absent slot is a zero-width box that cannot leak. */}
        <div class="ui-tabs__actions">{actions()}</div>
      </div>
      {/* Every section is still rendered (the content functions are called
          for all of them, so each keeps its own state), but only the active
          one takes layout space. The inactive panels carry `hidden`, which
          every browser renders as `display: none`: they contribute no height,
          so the box is the ACTIVE section's size, not the tallest's. The
          dialog around this animates the resulting height change, which is
          what buys back the stability the shared-cell approach provided.

          `hidden` also keeps the inactive panels out of the tab order and out
          of the accessibility tree, exactly like the `visibility: hidden`
          they replaced — it only stops them contributing their size, which
          was the entire reason that choice was made and is now the point. */}
      <div class="ui-tabs__panels">
        <Index each={props.items}>
          {(item) => {
            // ONCE per position, and untracked so reading it never
            // subscribes this panel to the array being rebuilt. Everything
            // the section renders stays live: the closure reads its own
            // surface's props, which are the reactive thing.
            const content = untrack(() => item().content)
            return (
              <div
                class="ui-tabs__panel"
                role="tabpanel"
                id={`ui-tabpanel-${item().id}`}
                aria-labelledby={`ui-tab-${item().id}`}
                data-active={props.active === item().id ? 'true' : undefined}
                hidden={props.active !== item().id}
                tabIndex={props.active === item().id ? 0 : -1}
              >
                {content()}
              </div>
            )
          }}
        </Index>
      </div>
    </div>
  )
}
