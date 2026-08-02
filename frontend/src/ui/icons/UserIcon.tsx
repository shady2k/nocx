import type { Component } from 'solid-js'

/**
 * Person — Lucide `user-round` under ISC. Stands for the SSH agent, which
 * holds the identity on the user's behalf.
 * Uses currentColor so it follows the container's text colour.
 */
const UserIcon: Component = () => (
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
    <circle cx="12" cy="8" r="5" />
    <path d="M20 21a8 8 0 0 0-16 0" />
  </svg>
)

export default UserIcon
