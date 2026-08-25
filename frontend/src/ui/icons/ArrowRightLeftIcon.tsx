import type { Component } from 'solid-js'

/**
 * ArrowRightLeft — Lucide `arrow-right-left` under ISC.
 *
 * Two horizontal arrows, the upper one leaving to the right and the lower one
 * returning to the left: a request going out and a response coming back. It
 * is the API workbench's glyph (nocx-zccer), and it replaced `ArrowRightIcon`
 * — a single arrow, which every other rail in this product uses to mean "go
 * there", so the entry read as navigation rather than as an exchange.
 *
 * The pair is what carries the meaning, and it is also what makes it legible
 * at 16px beside `FolderIcon`, `PlugIcon`, a git branch and a list: two
 * stacked shafts at different heights read as a round trip where one arrow
 * of any weight reads as a direction. The heads are at opposite ends for the
 * same reason — two arrows drawn the same way round are a double chevron.
 */
const ArrowRightLeftIcon: Component = () => (
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
    <path d="m16 3 4 4-4 4" />
    <path d="M20 7H4" />
    <path d="m8 21-4-4 4-4" />
    <path d="M4 17H20" />
  </svg>
)

export default ArrowRightLeftIcon
