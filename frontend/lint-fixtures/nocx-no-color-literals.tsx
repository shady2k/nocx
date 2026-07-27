// Negative fixture for nocx/no-color-literals.
//
// This file intentionally contains every prohibited colour pattern the rule
// catches in JSX: inline style colour properties, SVG fill/stroke attributes,
// and the color-mix laundering case.
//
// It MUST produce lint errors. If the gate goes green on this file, the rule
// has silently regressed.
//
// Excluded from tsconfig include and from build — do not type-check or bundle.

/* eslint-disable @typescript-eslint/no-unused-vars, no-console */
/* eslint-disable @typescript-eslint/no-unsafe-return */

import type { Component } from 'solid-js'
import { createEffect } from 'solid-js'

// Rule: nocx/no-color-literals — inline style with hex colour
function InlineHexColor() {
  return <div style={{ color: '#ff0000' }}>Red text</div>
}

// Rule: nocx/no-color-literals — inline style with named colour
function InlineNamedColor() {
  return <div style={{ background: 'red' }}>Red bg</div>
}

// Rule: nocx/no-color-literals — SVG fill attribute with hex
function SvgFillHex() {
  return (
    <svg width="100" height="100">
      <rect width="50" height="50" fill="#ff0000" />
    </svg>
  )
}

// Rule: nocx/no-color-literals — SVG stroke attribute with hex
function SvgStrokeHex() {
  return (
    <svg width="100" height="100">
      <circle cx="50" cy="50" r="40" stroke="#00ff00" stroke-width="2" />
    </svg>
  )
}

// Rule: nocx/no-color-literals — SVG fill with named colour
function SvgFillNamed() {
  return (
    <svg width="100" height="100">
      <rect width="50" height="50" fill="red" />
    </svg>
  )
}

// Rule: nocx/no-color-literals — inline style with rgb()
function InlineRgb() {
  return <div style={{ color: 'rgb(255, 0, 0)' }}>RGB text</div>
}

// Rule: nocx/no-color-literals — standalone white (not in color-mix)
function StandaloneWhite() {
  return <div style={{ background: 'white' }}>White bg</div>
}

// Rule: nocx/no-color-literals — standalone black (not in color-mix)
function StandaloneBlack() {
  return <div style={{ background: 'black' }}>Black bg</div>
}

// Rule: nocx/no-color-literals — var(--x) with hex fallback
function VarWithFallback() {
  return <div style={{ color: 'var(--x, #fff)' }}>Fallback</div>
}

// These are ALLOWED and should NOT cause violations:

function AllowedVarOnly() {
  return <div style={{ color: 'var(--color-text)' }}>Token</div>
}

function AllowedCurrentColor() {
  return <div style={{ color: 'currentColor' }}>Current</div>
}

function AllowedTransparent() {
  return <div style={{ background: 'transparent' }}>Transparent</div>
}

function AllowedInherit() {
  return <div style={{ color: 'inherit' }}>Inherit</div>
}

function AllowedSvgFillCurrentColor() {
  return (
    <svg width="100" height="100">
      <rect width="50" height="50" fill="currentColor" />
    </svg>
  )
}

function AllowedNonColorStyleProp() {
  return <div style={{ fontSize: '14px', padding: '8px' }}>No colour</div>
}

// Reactive context to ensure the fixture has a Solid component shape
function ReactiveContainer() {
  const items = () => [1, 2, 3]
  createEffect(() => {
    console.log(items())
  })
  return <div>ok</div>
}

// Export so TypeScript treats this as a module
export const _unused: Component<Record<string, never>> = () => null
