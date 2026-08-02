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
  info: JSX.Element
  actions: JSX.Element
}

/** The shared name/meta/actions row inside a CollectionView. */
export function CollectionRow(props: CollectionRowProps) {
  return (
    <div class="ui-collection-row" role="listitem" tabIndex={-1}>
      <div class="ui-collection-row__info">{props.info}</div>
      <div class="ui-collection-row__actions">{props.actions}</div>
    </div>
  )
}
