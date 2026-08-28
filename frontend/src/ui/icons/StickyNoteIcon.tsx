import type { Component } from 'solid-js'

/** StickyNote — Lucide `sticky-note` under ISC. A note the user wrote: the
 *  Notes panel's mark, and the "New note" row in the tab strip's menu.
 *
 *  It exists because TextQuoteIcon was doing both jobs. That glyph means a
 *  SAVED PHRASE — the snippets library — everywhere else, and the two sit
 *  one row apart in the same menu, where one mark for two meanings is a
 *  person pointing at the wrong row. The folded corner is the difference
 *  that survives 16px. */
const StickyNoteIcon: Component = () => (
  <svg
    xmlns="http://www.w3.org/2000/svg"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width="2"
    stroke-linecap="round"
    stroke-linejoin="round"
    aria-hidden="true"
  >
    <path d="M16 3H5a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h11l5-5V5a2 2 0 0 0-2-2z" />
    <path d="M16 21v-4a1 1 0 0 1 1-1h4" />
  </svg>
)

export default StickyNoteIcon
