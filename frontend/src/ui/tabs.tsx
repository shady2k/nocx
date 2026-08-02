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
 */

import { For, Show, type JSX } from 'solid-js'
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
}

export function Tabs(props: TabsProps) {
  const orientation = () => props.orientation ?? 'vertical'

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
        <For each={props.items}>
          {(item) => (
            <Button
              variant="ghost"
              selected={props.active === item.id}
              role="tab"
              id={`ui-tab-${item.id}`}
              aria-controls={`ui-tabpanel-${item.id}`}
              // Roving tabindex: one tab stop for the whole list.
              tabIndex={props.active === item.id ? 0 : -1}
              onClick={() => props.onChange(item.id)}
            >
              <Show when={item.status} fallback={item.label}>
                {(status) => (
                  <StatusDot tone={status().tone} accessibleName={status().accessibleName}>
                    {item.label}
                  </StatusDot>
                )}
              </Show>
            </Button>
          )}
        </For>
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
        <For each={props.items}>
          {(item) => (
            <div
              class="ui-tabs__panel"
              role="tabpanel"
              id={`ui-tabpanel-${item.id}`}
              aria-labelledby={`ui-tab-${item.id}`}
              data-active={props.active === item.id ? 'true' : undefined}
              hidden={props.active !== item.id}
              tabIndex={props.active === item.id ? 0 : -1}
            >
              {item.content()}
            </div>
          )}
        </For>
      </div>
    </div>
  )
}
