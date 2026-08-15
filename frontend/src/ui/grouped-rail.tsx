/**
 * GroupedRail — a navigation rail with group headings (nocx-dgsp).
 *
 * The kit's grouped settings rail: group headings in catalogue order with
 * their items beneath, and ungrouped items at top level beside the groups.
 * The surface decides WHICH item sits in WHICH group (or at top level); this
 * component renders only the structure, so the grouping vocabulary lives in
 * one place and the kit can name its state.
 *
 * Identity: `.ui-grouped-nav` with `__group`, `__heading`, `__sublist`,
 * `__item`. Each item row is a ghost Button (the appearance lives on the
 * button); the item `li` carries the row identity and `data-selected` (the
 * selected look belongs to Button's ghost variant). Items may carry a count
 * badge (e.g. per-section modified counts) rendered via the kit Badge.
 *
 * A page naming a group the catalogue does not declare throws at render — it
 * must fail a test, never render silently at top level.
 */

import { For, Show, untrack } from 'solid-js'
import { Button } from './button'
import { Badge } from './badge'
import { Caption } from './caption'
export interface GroupedRailGroup {
  /** Id from the catalogue; an item's groupId must be one of these. */
  id: string
  title: string
  /** Rail position; the rail sorts groups by it. */
  order: number
}

export interface GroupedRailItem {
  id: string
  title: string
  /** Group id from the catalogue. Absent means "top level, no group". */
  groupId?: string
  /** Optional count badge. Accessor so the badge can update in place — the
   *  row objects are stable across navigation, and only the accessors read
   *  signals (Solid fine-grained updates, never re-keyed rows). */
  count?: () => number | undefined
  active: () => boolean
  onSelect: () => void
}

export interface GroupedRailProps {
  /** aria-label for the nav element. */
  label: string
  /** The group catalogue, in any order — rendered sorted by `order`. */
  groups: GroupedRailGroup[]
  items: GroupedRailItem[]
}

function renderItem(item: GroupedRailItem) {
  const count = () => {
    const c = item.count?.()
    return c !== undefined && c > 0 ? c : undefined
  }
  return (
    <li class="ui-grouped-nav__item" data-item={item.id} data-selected={item.active() || undefined}>
      <Button variant="ghost" selected={item.active()} onClick={() => item.onSelect()}>
        {item.title}
        <Show when={count() !== undefined}>
          <Badge tone="warning">{String(count())}</Badge>
        </Show>
      </Button>
    </li>
  )
}

export function GroupedRail(props: GroupedRailProps) {
  // Criterion: a group id a page names but the catalogue does not declare
  // fails — never renders silently at top level. One-shot registration
  // validation: the catalogue and the registry are static once the snapshot
  // arrives, so the check runs untracked at render.
  untrack(() => {
    for (const item of props.items) {
      if (item.groupId !== undefined && !props.groups.some((g) => g.id === item.groupId)) {
        throw new Error(
          'GroupedRail: item "' +
            item.id +
            '" names group "' +
            item.groupId +
            '", which the catalogue does not declare',
        )
      }
    }
  })

  const orderedGroups = () => [...props.groups].sort((a, b) => a.order - b.order)
  const topLevel = () => props.items.filter((it) => it.groupId === undefined)
  const itemsIn = (groupId: string) => props.items.filter((it) => it.groupId === groupId)
  // A group whose item list is empty renders nothing at all — no heading, no
  // empty sublist, no margin. The catalogue may declare a group no page lands
  // in for this build (the Test section is not declared in every build); a
  // caption with nothing under it is a row that reads as an item.

  return (
    <nav class="ui-grouped-nav" aria-label={props.label}>
      <ul class="ui-grouped-nav__list">
        <For each={topLevel()}>{(item) => renderItem(item)}</For>
        <For each={orderedGroups().filter((g) => itemsIn(g.id).length > 0)}>
          {(group) => (
            <li class="ui-grouped-nav__group" data-group={group.id}>
              <span class="ui-grouped-nav__heading">
                <Caption size="context">{group.title}</Caption>
              </span>
              <ul class="ui-grouped-nav__sublist">
                <For each={itemsIn(group.id)}>{(item) => renderItem(item)}</For>
              </ul>
            </li>
          )}
        </For>
      </ul>
    </nav>
  )
}
