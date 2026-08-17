import type { Component } from 'solid-js'

/**
 * Shield — Lucide `shield` under ISC.
 * Uses currentColor so it follows the container's text colour.
 */
const ShieldIcon: Component = () => (
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
    <path d="M20 13c0 5-3.5 7.5-8 9-4.5-1.5-8-4-8-9V5l8-3 8 3z" />
  </svg>
)

export default ShieldIcon
