// Negative fixture for nocx/no-inline-markup.
//
// This file intentionally contains every prohibited pattern the rule catches.
// It MUST produce lint errors. If the gate goes green on this file, the rule
// has silently regressed.
//
// Excluded from tsconfig include and from build — do not type-check or bundle.

/* eslint-disable @typescript-eslint/no-unused-vars, no-console */
/* eslint-disable @typescript-eslint/no-unsafe-return */

import type { Component } from 'solid-js'

// Rule: nocx/no-inline-markup — class owned by a ui component used outside ui/
function BypassPageBody() {
  return <div class="ui-page__body">content</div>
}

// Rule: nocx/no-inline-markup — class owned by a ui component used outside ui/
function BypassPageRail() {
  return <div class="ui-page__rail">side content</div>
}

// Rule: nocx/no-inline-markup — class owned by a ui component used outside ui/
function BypassPage() {
  return <div class="ui-page">page</div>
}

// Rule: nocx/no-inline-markup — inline style prop in application surface
function InlineStyle() {
  return <div style={{ marginTop: '8px', display: 'flex' }}>styled content</div>
}

// Rule: nocx/no-inline-markup — multiple violations on one element
// (class bypass + inline style)
function BothViolations() {
  return (
    <div class="ui-page__body" style={{ padding: '4px' }}>
      wrapped
    </div>
  )
}

// Rule: nocx/no-inline-markup — className alias (React-style)
function BypassWithClassName() {
  return <div className="ui-page__body">content</div>
}

// Export so TypeScript treats this as a module
export const _unused: Component<Record<string, never>> = () => null
