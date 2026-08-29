// @vitest-environment jsdom
// ResizeHandle — the kit's "drag to resize" separator (nocx-qmcu). The
// contract a user reaches: the edge between two panes is a focusable
// separator that responds to the mouse AND the keyboard, reports its value
// through ARIA, and never lets a drag produce a value outside the bounds.
import { describe, expect, it, vi, afterEach } from 'vitest'
import { render, screen, fireEvent, cleanup } from '@solidjs/testing-library'
import { ResizeHandle, type ResizeHandleProps } from './resize-handle'

afterEach(() => cleanup())

function subject(overrides?: Partial<ResizeHandleProps>) {
  const props: ResizeHandleProps = {
    value: 240,
    min: 200,
    max: 640,
    ariaLabel: 'Resize sidebar',
    onChange: vi.fn(),
    onCommit: vi.fn(),
    ...overrides,
  }
  return render(() => <ResizeHandle {...props} />)
}

describe('ResizeHandle', () => {
  it('renders a keyboard-focusable vertical separator naming its value', () => {
    subject()
    const sep = screen.getByRole('separator', { name: 'Resize sidebar' })
    expect(sep.getAttribute('aria-orientation')).toBe('vertical')
    expect(sep.getAttribute('aria-valuenow')).toBe('240')
    expect(sep.getAttribute('aria-valuemin')).toBe('200')
    expect(sep.getAttribute('aria-valuemax')).toBe('640')
    expect(sep.tabIndex).toBe(0)
    expect(sep.classList.contains('ui-resize-handle')).toBe(true)
  })

  it('reports and commits one step for ArrowRight, clamped to the max', () => {
    const onChange = vi.fn()
    const onCommit = vi.fn()
    subject({ value: 630, onChange, onCommit })
    const sep = screen.getByRole('separator')
    fireEvent.keyDown(sep, { key: 'ArrowRight' })
    expect(onChange).toHaveBeenCalledWith(638)
    expect(onCommit).toHaveBeenCalledWith(638)

    fireEvent.keyDown(sep, { key: 'ArrowRight' })
    // Already at 638: one more step hits the 640 bound, not 646.
    expect(onChange).toHaveBeenLastCalledWith(640)
    expect(onCommit).toHaveBeenLastCalledWith(640)
  })

  it('ArrowLeft steps down; Home and End jump to the bounds', () => {
    const onChange = vi.fn()
    subject({ value: 240, onChange })
    const sep = screen.getByRole('separator')
    fireEvent.keyDown(sep, { key: 'ArrowLeft' })
    expect(onChange).toHaveBeenLastCalledWith(232)
    fireEvent.keyDown(sep, { key: 'Home' })
    expect(onChange).toHaveBeenLastCalledWith(200)
    fireEvent.keyDown(sep, { key: 'End' })
    expect(onChange).toHaveBeenLastCalledWith(640)
  })

  it('a step that does not move the value fires nothing (no revision churn)', () => {
    const onChange = vi.fn()
    const onCommit = vi.fn()
    subject({ value: 200, min: 200, onChange, onCommit })
    const sep = screen.getByRole('separator')
    fireEvent.keyDown(sep, { key: 'ArrowLeft' })
    expect(onChange).not.toHaveBeenCalled()
    expect(onCommit).not.toHaveBeenCalled()
  })

  it('a drag reports live changes and commits once on release', () => {
    const onChange = vi.fn()
    const onCommit = vi.fn()
    const onDragStateChange = vi.fn()
    subject({ value: 240, onChange, onCommit, onDragStateChange })
    const sep = screen.getByRole('separator')

    fireEvent.pointerDown(sep, { clientX: 100, pointerId: 1 })
    expect(onDragStateChange).toHaveBeenLastCalledWith(true)
    fireEvent.pointerMove(sep, { clientX: 140, pointerId: 1 })
    fireEvent.pointerMove(sep, { clientX: 150, pointerId: 1 })
    expect(onChange).toHaveBeenLastCalledWith(290)
    expect(onCommit).not.toHaveBeenCalled() // still dragging

    fireEvent.pointerUp(sep, { clientX: 150, pointerId: 1 })
    expect(onCommit).toHaveBeenCalledWith(290)
    expect(onDragStateChange).toHaveBeenLastCalledWith(false)
  })

  it('a drag cannot escape the bounds', () => {
    const onChange = vi.fn()
    subject({ value: 620, onChange })
    const sep = screen.getByRole('separator')
    fireEvent.pointerDown(sep, { clientX: 100, pointerId: 1 })
    fireEvent.pointerMove(sep, { clientX: 500, pointerId: 1 }) // +400 → 1020
    expect(onChange).toHaveBeenLastCalledWith(640)
    fireEvent.pointerMove(sep, { clientX: -400, pointerId: 1 }) // −500 → 120
    expect(onChange).toHaveBeenLastCalledWith(200)
    fireEvent.pointerUp(sep, { clientX: -400, pointerId: 1 })
  })

  it('aria-valuenow tracks the live drag position', () => {
    subject()
    const sep = screen.getByRole('separator')
    fireEvent.pointerDown(sep, { clientX: 100, pointerId: 1 })
    fireEvent.pointerMove(sep, { clientX: 180, pointerId: 1 })
    expect(sep.getAttribute('aria-valuenow')).toBe('320')
    fireEvent.pointerUp(sep, { clientX: 180, pointerId: 1 })
    expect(sep.getAttribute('aria-valuenow')).toBe('320')
  })

  it('a pointercancel ends the drag and commits the live value', () => {
    const onCommit = vi.fn()
    const onDragStateChange = vi.fn()
    subject({ value: 240, onCommit, onDragStateChange })
    const sep = screen.getByRole('separator')
    fireEvent.pointerDown(sep, { clientX: 100, pointerId: 1 })
    fireEvent.pointerMove(sep, { clientX: 130, pointerId: 1 })
    fireEvent.pointerCancel(sep, { clientX: 130, pointerId: 1 })
    expect(onCommit).toHaveBeenCalledWith(270)
    expect(onDragStateChange).toHaveBeenLastCalledWith(false)
  })

  // ── The pane on the other side (nocx-ls38w) ──────────────────────────────
  //
  // The sidebar moved to the window's trailing edge, so its handle is on the
  // panel's LEADING edge and the pane it measures is AFTER the separator.
  // What inverts is the mapping from a gesture to a width, and only for the
  // gestures that actually move the separator.

  it('pane="after": dragging the separator left widens the pane after it', () => {
    const onChange = vi.fn()
    const onCommit = vi.fn()
    subject({ value: 240, pane: 'after', onChange, onCommit })
    const sep = screen.getByRole('separator')

    fireEvent.pointerDown(sep, { clientX: 500, pointerId: 1 })
    fireEvent.pointerMove(sep, { clientX: 400, pointerId: 1 })
    expect(onChange).toHaveBeenLastCalledWith(340)
    expect(sep.getAttribute('aria-valuenow')).toBe('340')

    fireEvent.pointerUp(sep, { clientX: 400, pointerId: 1 })
    expect(onCommit).toHaveBeenLastCalledWith(340)
  })

  it('pane="after": the on-axis keys invert, the off-axis and absolute ones do not', () => {
    const onChange = vi.fn()
    subject({ value: 240, pane: 'after', onChange })
    const sep = screen.getByRole('separator')

    // Physical: the separator follows the arrow, so LEFT grows the pane that
    // is to the right of it.
    fireEvent.keyDown(sep, { key: 'ArrowLeft' })
    expect(onChange).toHaveBeenLastCalledWith(248)
    fireEvent.keyDown(sep, { key: 'ArrowRight' })
    expect(onChange).toHaveBeenLastCalledWith(240)
    expect(sep.getAttribute('aria-valuenow')).toBe('240')

    // Off-axis: Up and Down move nothing on a vertical separator. They are
    // APG's plain "increase / decrease", not a direction on screen, so they
    // mean the same thing on both sides.
    fireEvent.keyDown(sep, { key: 'ArrowUp' })
    expect(onChange).toHaveBeenLastCalledWith(248)
    fireEvent.keyDown(sep, { key: 'ArrowDown' })
    expect(onChange).toHaveBeenLastCalledWith(240)

    // Absolute on both sides.
    fireEvent.keyDown(sep, { key: 'Home' })
    expect(onChange).toHaveBeenLastCalledWith(200)
    fireEvent.keyDown(sep, { key: 'End' })
    expect(onChange).toHaveBeenLastCalledWith(640)
  })
})
