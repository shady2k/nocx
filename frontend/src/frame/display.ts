import './display.css'

import type { CapturedFrame, FrameRow } from './types'

/**
 * Build the static view used while the live xterm buffer continues parsing.
 * This consumes a captured frame only; it never reads the renderer or a live
 * buffer, so the display can remain pinned while new bytes keep arriving.
 */
export function createCapturedFrameView(frame: CapturedFrame): HTMLElement {
  const root = document.createElement('div')
  root.className = 'nocx-freeze-frame'
  root.dataset.source = frame.provenance.source
  root.setAttribute('aria-label', 'Frozen terminal frame')

  for (const row of frame.rows) {
    root.appendChild(createRow(row))
  }
  return root
}

function createRow(row: FrameRow): HTMLElement {
  const element = document.createElement('div')
  element.className = 'nocx-freeze-frame__row'
  if (row.kind === 'text') {
    element.textContent = row.text
    return element
  }

  for (const cell of row.cells) {
    const span = document.createElement('span')
    span.className = 'nocx-freeze-frame__cell'
    span.textContent = cell.char || ' '
    if (cell.attrs.fg !== null) span.style.color = cell.attrs.fg
    if (cell.attrs.bg !== null) span.style.backgroundColor = cell.attrs.bg
    if (cell.attrs.bold) span.style.fontWeight = 'bold'
    if (cell.attrs.italic) span.style.fontStyle = 'italic'
    if (cell.attrs.dim) span.style.opacity = '0.5'
    if (cell.attrs.underline || cell.attrs.strikethrough || cell.attrs.overline) {
      span.style.textDecoration = [
        cell.attrs.underline && 'underline',
        cell.attrs.strikethrough && 'line-through',
        cell.attrs.overline && 'overline',
      ]
        .filter((value): value is string => value !== false)
        .join(' ')
    }
    if (cell.attrs.inverse) span.style.filter = 'invert(1)'
    element.appendChild(span)
  }
  return element
}

/** The kit Badge used to tell the person why the live view is not moving. */
export function createFreezeMarker(): HTMLElement {
  const marker = document.createElement('span')
  marker.className = 'ui-badge'
  marker.dataset.tone = 'warning'
  marker.setAttribute('role', 'status')
  marker.textContent = 'Screen frozen while you ask'
  marker.title = 'The terminal keeps running; this is a pinned view of its screen.'
  return marker
}
