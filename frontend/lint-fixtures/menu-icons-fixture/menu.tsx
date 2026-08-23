// Menu-icons fixture — negative fixtures for check-menu-icons.mjs
// (nocx-inbw1). Every literal here is valid TypeScript that compiles and
// renders, so a checker that quietly stopped firing would look exactly like
// a menu with icons.
//
// Intentional violations (the gate asserts these fire):
//   bare-row       — a context-menu item literal with no icon at all
//   pushed-row     — the same omission through the push() shape the call
//                    sites use for a conditional row
//   undefined-row  — `icon: undefined`, which reserves the column and marks
//                    nothing; a rule that accepted the KEY would let this
//                    through and the next call site would find it
//
// Must stay silent (the gate asserts these do NOT fire):
//   marked-row     — the correct spelling, an icon from the kit's set
//   not-a-menu-row — id + label with no onSelect: not a menu item, and a
//                    rule that flagged it would be reporting every option
//                    object in the codebase

import { PencilIcon } from '../../src/ui/icons'

export function fixtureItems() {
  const items = [
    {
      id: 'bare-row',
      label: 'Bare row',
      onSelect: () => {},
    },
    {
      id: 'marked-row',
      label: 'Marked row',
      icon: PencilIcon,
      onSelect: () => {},
    },
    {
      id: 'undefined-row',
      label: 'Undefined row',
      icon: undefined,
      onSelect: () => {},
    },
  ]
  items.push({
    id: 'pushed-row',
    label: 'Pushed row',
    onSelect: () => {},
  })
  return items
}

/** Not a menu item: no onSelect, so nothing here is picked from a menu. */
export const fixtureOption = {
  id: 'not-a-menu-row',
  label: 'Not a menu row',
  value: 3,
}
