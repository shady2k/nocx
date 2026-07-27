// Negative fixture for nocx/no-raw-controls.
//
// This file intentionally contains every prohibited pattern the rule catches.
// It MUST produce lint errors. If the gate goes green on this file, the rule
// has silently regressed.
//
// Excluded from tsconfig include and from build — do not type-check or bundle.

/* eslint-disable @typescript-eslint/no-unused-vars, no-console */
/* eslint-disable @typescript-eslint/no-unsafe-return */

import type { Component } from 'solid-js'

// Rule: nocx/no-raw-controls — raw <button> in application surface
function RawButton() {
  return <button>Click me</button>
}

// Rule: nocx/no-raw-controls — raw <select> in application surface
function RawSelect() {
  return (
    <select>
      <option value="a">A</option>
    </select>
  )
}

// Rule: nocx/no-raw-controls — raw <textarea> in application surface
function RawTextarea() {
  return <textarea />
}

// Rule: nocx/no-raw-controls — raw <input type="checkbox">
function RawCheckbox() {
  return <input type="checkbox" />
}

// Rule: nocx/no-raw-controls — raw <input type="radio">
function RawRadio() {
  return <input type="radio" />
}

// Rule: nocx/no-raw-controls — raw <input type="text">
function RawTextInput() {
  return <input type="text" />
}

// Rule: nocx/no-raw-controls — raw <input type="password">
function RawPasswordInput() {
  return <input type="password" />
}

// Rule: nocx/no-raw-controls — raw <input type="search">
function RawSearchInput() {
  return <input type="search" />
}

// Rule: nocx/no-raw-controls — innerHTML assignment
function InnerHTMLAssignment() {
  const el = document.createElement('div')
  el.innerHTML = '<span>test</span>'
  return el
}

// Export so TypeScript treats this as a module
export const _unused: Component<Record<string, never>> = () => null
