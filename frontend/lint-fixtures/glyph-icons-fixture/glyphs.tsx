// Glyph-icons fixture — negative fixtures for check-glyph-icons.mjs (nocx-9bpeq.5).
//
// Intentional violations (the gate asserts these fire):
//   close-x      — a × as a button's JSX child
//   more-dots    — a ⋮ assigned to textContent
//   folder-emoji — an emoji at the start of a textContent template
//
// Must stay silent:
//   times-title  — × in a title attribute (a multiplication sign)
//   idle-marker  — a glyph constant compared against text

import { CloseIcon } from '../../src/ui/icons'

export function CloseX() {
  return <button aria-label="close-x">{'×'}</button>
}

export function moreDots(el: HTMLElement): void {
  el.textContent = '⋮' // more-dots
}

export function folderEmoji(el: HTMLElement, label: string): void {
  el.textContent = `📁 ${label}` // folder-emoji
}

export function TimesTitle(props: { n: number }) {
  return (
    <b title={`times-title ×${props.n}`}>
      <CloseIcon />
    </b>
  )
}

const IDLE = '✳'
export const idleMarker = (screen: string): boolean => screen.startsWith(IDLE)
