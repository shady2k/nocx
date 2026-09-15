import type { Component } from 'solid-js'

/**
 * Check — Lucide `check` under ISC.
 * Uses currentColor so it follows the container's text colour.
 */
const CheckIcon: Component = () => (
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
    <path d="m20 6-11 11-5-5" />
  </svg>
)

export default CheckIcon
