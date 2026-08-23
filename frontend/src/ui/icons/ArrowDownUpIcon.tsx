import type { Component } from 'solid-js'

/**
 * A down arrow beside an up arrow — Lucide `arrow-down-up` under ISC.
 * The activity bar's operations glyph: what nocx moves for somebody is
 * bytes in one direction or the other.
 * Uses currentColor so it follows the container's text colour.
 */
const ArrowDownUpIcon: Component = () => (
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
    <path d="m3 16 4 4 4-4" />
    <path d="M7 20V4" />
    <path d="m21 8-4-4-4 4" />
    <path d="M17 4v16" />
  </svg>
)

export default ArrowDownUpIcon
