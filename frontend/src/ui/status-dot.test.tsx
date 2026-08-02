// @vitest-environment jsdom
import { describe, expect, it, afterEach } from 'vitest'
import { render, cleanup } from '@solidjs/testing-library'
import { StatusDot } from './status-dot'

afterEach(() => cleanup())

describe('StatusDot', () => {
  it('carries its identity and its tone', () => {
    render(() => (
      <StatusDot tone="error" accessibleName="Not answering">
        Keychain
      </StatusDot>
    ))
    const dot = document.querySelector('.ui-status-dot')
    expect(dot).not.toBeNull()
    expect(dot!.getAttribute('data-tone')).toBe('error')
  })

  // The half that is easy to drop. A dot with no accessible name tells a
  // screen-reader user nothing at all about the thing it is marking.
  it('says what the colour means, and hides the colour from assistive technology', () => {
    render(() => (
      <StatusDot tone="ok" accessibleName="Available">
        Keychain
      </StatusDot>
    ))
    expect(document.querySelector('.ui-status-dot')!.getAttribute('aria-hidden')).toBe('true')
    const name = document.querySelector('.ui-visually-hidden')
    expect(name).not.toBeNull()
    expect(name!.textContent).toBe('Available')
  })

  it('renders no wrapper, so it sits directly in the row that places it', () => {
    const { container } = render(() => (
      <StatusDot tone="warning" accessibleName="Read-only">
        <span>Keychain</span>
      </StatusDot>
    ))
    expect(container.firstElementChild!.classList.contains('ui-status-dot')).toBe(true)
    expect(container.children.length).toBe(3)
  })

  // "Keychain, read-only" — not "read-only, Keychain". The dot has to come
  // first for the eye and the state has to come last for the ear, which is
  // exactly why the label is a child rather than a sibling.
  it('reads as label-then-state, with the dot drawn first', () => {
    const { container } = render(() => (
      <StatusDot tone="error" accessibleName="Not answering">
        <span>Keychain</span>
      </StatusDot>
    ))
    expect(container.textContent).toBe('KeychainNot answering')
    expect(container.firstElementChild!.classList.contains('ui-status-dot')).toBe(true)
  })
})
