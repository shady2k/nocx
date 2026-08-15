import { Show, type JSX } from 'solid-js'
import { SearchField } from './search-field'
import { Toolbar } from './toolbar'

export interface CollectionViewProps {
  searchValue: string
  onSearch: (value: string) => void
  searchPlaceholder: string
  searchLabel: string
  actions: JSX.Element
  hasItems: boolean
  empty: JSX.Element
  children: JSX.Element
}

/** Shared searchable manager surface used by Connections and Credentials. */
export function CollectionView(props: CollectionViewProps) {
  return (
    <div class="ui-collection-view">
      <Toolbar ariaLabel="Collection actions">
        <div class="ui-collection-view__search">
          <SearchField
            value={props.searchValue}
            onInput={props.onSearch}
            placeholder={props.searchPlaceholder}
            ariaLabel={props.searchLabel}
          />
        </div>
        <div class="ui-collection-view__actions">{props.actions}</div>
      </Toolbar>
      <Show when={props.hasItems} fallback={props.empty}>
        <div class="ui-collection-view__body">{props.children}</div>
      </Show>
    </div>
  )
}

export interface CollectionRowProps {
  /** Free-form row body. Kept for the rows RecordRow does not describe —
   *  Secrets' glyph + two-line body rows and the Git panel's dense commit
   *  rows (subject over a meta line of hash, time and several ref badges) —
   *  and only for those. A record that is a title, a kind, meta text and a
   *  status belongs in RecordRow: that composite owns the name/meta grammar
   *  so a surface cannot invent a second one (nocx-pp3y.3). */
  info: JSX.Element
  actions: JSX.Element
  /** Makes the row activatable: reachable (tabIndex 0), operable with
   *  Enter/Space, and a click anywhere except the actions slot fires it.
   *  A control inside `actions` owns its click — the row never swallows it. */
  onActivate?: (e: MouseEvent | KeyboardEvent) => void
  /** The caller's selection vocabulary — the row only renders it. */
  selected?: boolean
  /** The current keyboard target; reads stronger than selection. */
  focused?: boolean
  /** Row density: `default` for manager pages, `dense` for sidebar lists
   *  (the Git panel's rows). A typed data-*, never a caller class. */
  density?: 'default' | 'dense'
}

/** The shared name/meta/actions row inside a CollectionView.
 *
 *  Activation: the row cannot install a stopPropagation on caller-supplied
 *  action elements, so instead it ignores any click that lands inside the
 *  actions slot (the TreeRow disclosure pattern, inverted — there the
 *  button stops propagation, here the row checks containment). */
export function CollectionRow(props: CollectionRowProps) {
  let actionsEl: HTMLDivElement | undefined
  const activatable = () => props.onActivate !== undefined

  const handleClick = (e: MouseEvent) => {
    if (!activatable()) return
    if (actionsEl && actionsEl.contains(e.target as Node)) return
    props.onActivate?.(e)
  }

  const handleKeyDown = (e: KeyboardEvent) => {
    if (!activatable()) return
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault() // Space must not scroll the list
      props.onActivate?.(e)
    }
  }

  return (
    <div
      class="ui-collection-row"
      role="listitem"
      tabIndex={activatable() ? 0 : -1}
      data-density={props.density ?? 'default'}
      data-selected={props.selected === true ? 'true' : undefined}
      data-focused={props.focused === true ? 'true' : undefined}
      onClick={handleClick}
      onKeyDown={handleKeyDown}
    >
      <div class="ui-collection-row__info">{props.info}</div>
      <div
        class="ui-collection-row__actions"
        ref={(el) => {
          actionsEl = el
        }}
      >
        {props.actions}
      </div>
    </div>
  )
}
