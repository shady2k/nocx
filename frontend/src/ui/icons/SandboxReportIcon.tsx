import type { Component } from 'solid-js'

/**
 * Sandbox report — Lucide-style shield carrying a bar chart, under ISC.
 * Deliberately NOT the shield alone: the activity bar already spends
 * ShieldIcon on the active-tab conversion action, and two different
 * actions sharing one glyph are indistinguishable (design §6.1). The
 * chart bars say "this one is the report surface".
 * Uses currentColor so it follows the container's text colour.
 */
const SandboxReportIcon: Component = () => (
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
    <path d="M9.5 14.5V12" />
    <path d="M12.5 14.5V9.5" />
    <path d="M15.5 14.5V11" />
  </svg>
)

export default SandboxReportIcon
