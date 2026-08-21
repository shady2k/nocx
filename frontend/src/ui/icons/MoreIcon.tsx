import type { Component } from 'solid-js'

/**
 * Vertical ellipsis — Lucide `ellipsis-vertical` under ISC.
 *
 * The kit's mark for "the rest of the actions on this thing": a control that
 * opens a menu of what did not earn a place in the strip. Uses currentColor
 * so it follows the container's text colour.
 */
const MoreIcon: Component = () => (
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
    <circle cx="12" cy="5" r="1" />
    <circle cx="12" cy="12" r="1" />
    <circle cx="12" cy="19" r="1" />
  </svg>
)

export default MoreIcon
