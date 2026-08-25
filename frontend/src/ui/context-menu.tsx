/**
 * ContextMenu — the kit's menu primitive: a small, non-modal popover of
 * actions anchored at a point (the row the user right-clicked). The one
 * vocabulary for "right-click a thing, pick an action" — the README's
 * "Popover/Menu/Combobox: not built" row is this component.
 *
 * What it owns, all in context-menu.css:
 * - the shell: fixed at (x, y), clamped so it never runs off the viewport;
 * - the item list: `ui-context-menu__item` native buttons, one per action;
 * - dismissal: outside pointerdown, Escape, or picking an item;
 * - keyboard: ArrowDown/ArrowUp/Home/End move focus, Enter/Space activate
 *   (native button semantics), and the first item takes focus on open.
 *
 * It is deliberately non-modal and transient: clicking anywhere outside
 * closes it, and there is no focus trap — a menu that blocks the app it
 * floated over is a dialog wearing a menu's clothes. The caller supplies
 * the items (id, label, onSelect) and the anchor point; the component only
 * asks what it needs and never knows what the actions mean.
 *
 * The surface may place the menu (choosing when to open it and where) and
 * may never repaint it — items are the kit's own buttons.
 */
import { For, Show, createEffect, onCleanup, type Component } from 'solid-js'
import { Portal } from 'solid-js/web'
import { clampMenuPosition } from './menu-geometry'

export interface ContextMenuItem {
  /** Stable identity for the item — keying and data-testid. */
  id: string
  label: string
  /**
   * The action's mark, from the kit's icon set. Optional, and a menu may mix
   * rows with and without one: the icon column is reserved either way, so the
   * labels stay in a single column instead of stepping in and out as rows
   * acquire marks. A glyph is the fastest way back to an action a person has
   * used before — they stop reading the menu and start pointing at it — which
   * is exactly what a menu of frequent actions is for.
   *
   * A COMPONENT, NOT AN ELEMENT. An element is DOM the moment it is written,
   * so a row carrying one can only be built where a document exists — and the
   * modules that BUILD rows (workspace-menu.ts) are deliberately pure, tested
   * without a renderer. Naming the component defers the DOM to this file,
   * which is the only place that has one.
   */
  icon?: Component
  onSelect: () => void
}

export interface ContextMenuProps {
  /**
   * One non-interactive line at the top, naming what the menu is ABOUT.
   *
   * A menu whose rows are all actions cannot state a fact, and some menus are
   * opened at a thing rather than at a place — a variable in an address, say,
   * where "what is this and is it answered" is most of what the person came
   * for and the action is the smaller half. It is not a row: it takes no
   * focus, answers no key and is skipped by the keyboard walk, because a line
   * that cannot be chosen must not look like one that can.
   */
  header?: string
  /** Show the menu at (x, y) viewport coordinates. */
  open: boolean
  x: number
  y: number
  items: ContextMenuItem[]
  /** Called when the menu dismisses itself: outside pointerdown, Escape,
   *  or an item being picked. The caller owns the open state. */
  onClose: () => void
  'data-testid'?: string
}

export function ContextMenu(props: ContextMenuProps) {
  let element: HTMLDivElement | undefined
  /** Whoever held the keyboard when the menu took it. */
  let opener: HTMLElement | null = null

  /**
   * Hand the keyboard back to the opener.
   *
   * THE MENU TAKES FOCUS ON OPEN — it must, the rows are keyboard-walkable —
   * so it owes it back, and it used to keep it. Nothing noticed while every
   * row did its work and vanished: focus fell to <body> and the terminal's
   * document-level rescue picked up the next keystroke. It became visible the
   * moment a row started opening ANOTHER overlay, which the tab-strip rework
   * did by making "Quick connect…" a row here. The picker records
   * `document.activeElement` as the thing to restore when it closes; that was
   * the menu item, which is unmounted by then, and `focus()` on a detached
   * node is a silent no-op. So escaping the picker left the caret out of the
   * prompt the person had been typing in.
   *
   * Which is why this runs BEFORE the row's `onSelect`: the action is what
   * opens the next overlay, and the next overlay reads the focus this restores.
   */
  function releaseFocus(): void {
    const el = opener
    opener = null
    if (el === null || !el.isConnected) return
    // Only while the menu still HOLDS the keyboard. An outside pointerdown is
    // on its way to a new owner — the element it lands on takes focus itself,
    // and pulling it back to the opener over the top of that would make a
    // click somewhere else land somewhere else again.
    const active = document.activeElement
    if (element && !element.contains(active)) return
    el.focus({ preventScroll: true })
  }

  // Position and focus on open. The anchor does not move while the menu is
  // up, so both are measured once per open — never re-derived per change.
  createEffect(() => {
    if (!props.open) return
    const el = element
    if (!el) return
    // The rect is the laid-out size; the position is the shared clamp
    // (menu-geometry.ts), so a menu near the bottom or right edge flips
    // inward instead of overflowing — the same geometry the scrollback's
    // imperative block menu uses (nocx-vnirv.2).
    const rect = el.getBoundingClientRect()
    const { left, top } = clampMenuPosition(
      { x: props.x, y: props.y },
      { width: rect.width, height: rect.height },
      { width: window.innerWidth, height: window.innerHeight },
    )
    el.style.left = `${left}px`
    el.style.top = `${top}px`
    opener = document.activeElement instanceof HTMLElement ? document.activeElement : null
    el.querySelector<HTMLButtonElement>('[role="menuitem"]')?.focus()
  })

  // Document-level dismissal, attached only while open: an outside
  // pointerdown (the pointer that will click somewhere else) and Escape
  // both close the menu. The item buttons are inside the menu, so their
  // pointerdowns are contained and the subsequent click activates them.
  createEffect(() => {
    if (!props.open) return
    const onPointerDown = (e: PointerEvent): void => {
      const el = element
      if (el && e.target instanceof Node && !el.contains(e.target)) props.onClose()
    }
    const onKeyDown = (e: KeyboardEvent): void => {
      if (e.key === 'Escape') {
        e.stopPropagation()
        releaseFocus()
        props.onClose()
        return
      }
      const items = [...(element?.querySelectorAll<HTMLButtonElement>('[role="menuitem"]') ?? [])]
      if (items.length === 0) return
      const current = document.activeElement instanceof HTMLElement ? document.activeElement : null
      const index = current !== null ? items.indexOf(current as HTMLButtonElement) : -1
      let next = -1
      if (e.key === 'ArrowDown') next = index + 1 < items.length ? index + 1 : 0
      else if (e.key === 'ArrowUp') next = index - 1 >= 0 ? index - 1 : items.length - 1
      else if (e.key === 'Home') next = 0
      else if (e.key === 'End') next = items.length - 1
      if (next >= 0) {
        e.preventDefault()
        items[next]?.focus()
      }
    }
    document.addEventListener('pointerdown', onPointerDown)
    document.addEventListener('keydown', onKeyDown)
    onCleanup(() => {
      document.removeEventListener('pointerdown', onPointerDown)
      document.removeEventListener('keydown', onKeyDown)
    })
  })

  return (
    <Show when={props.open}>
      <Portal>
        <div
          class="ui-context-menu"
          role="menu"
          data-testid={props['data-testid']}
          ref={(el) => {
            element = el
          }}
        >
          {/* Not a row: no role, no tabindex, nothing the keyboard walk can
              land on. A line that cannot be chosen must not look like one
              that can. */}
          <Show when={props.header !== undefined}>
            <div class="ui-context-menu__header">{props.header}</div>
          </Show>
          <For each={props.items}>
            {(item) => (
              <button
                type="button"
                class="ui-context-menu__item"
                role="menuitem"
                onClick={() => {
                  releaseFocus()
                  props.onClose()
                  item.onSelect()
                }}
              >
                <span class="ui-context-menu__icon" aria-hidden="true">
                  <Show when={item.icon} keyed>
                    {(Icon) => <Icon />}
                  </Show>
                </span>
                <span class="ui-context-menu__label">{item.label}</span>
              </button>
            )}
          </For>
        </div>
      </Portal>
    </Show>
  )
}
