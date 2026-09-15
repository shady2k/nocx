import type { Component } from 'solid-js'

/**
 * Rightward chevron — Lucide `chevron-right` under ISC.
 * The command row's sigil (spec 2026-09-15 §4), replacing the mockup's `›`
 * as a text glyph. Uses currentColor so it follows the container's text
 * colour.
 */
const ChevronRightIcon: Component = () => (
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
    <path d="m9 18 6-6-6-6" />
  </svg>
)

export default ChevronRightIcon
