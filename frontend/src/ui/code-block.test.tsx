// @vitest-environment jsdom
//
// CodeBlock — the identity it renders, its wrap variance, and its copy control.
//
// The wrap variance is asserted BOTH as the attribute the component renders and
// as the declaration `code-block.css` keys off it, because either half alone is
// green while the pair is broken: an attribute nothing styles changes nothing
// on screen, and a rule nothing sets the attribute for is unreachable CSS.
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@solidjs/testing-library'
import { clearToasts, toasts } from './toast'
import { CodeBlock } from './code-block'

afterEach(() => {
  cleanup()
  clearToasts()
})

function block(wrap?: boolean): HTMLElement {
  const { container } = render(() => (
    <CodeBlock ariaLabel="Machine output" wrap={wrap}>
      {'a very long line of machine output'}
    </CodeBlock>
  ))
  const el = container.querySelector<HTMLElement>('.ui-code-block')
  if (!el) throw new Error('CodeBlock rendered no ui-code-block')
  return el
}

const CSS = readFileSync(resolve(process.cwd(), 'src/styles/components/code-block.css'), 'utf8')

describe('CodeBlock', () => {
  it('wraps by default, which is what a list of short lines wants', () => {
    expect(block().dataset.wrap).toBeUndefined()
    expect(block(true).dataset.wrap).toBeUndefined()
  })

  // A BLOCK HOLDING BYTES SAYS SO, and gets the sideways scroll the editors
  // give the same octets (nocx-kdawd).
  it('a block that must not wrap says so on the element that carries the look', () => {
    expect(block(false).dataset.wrap).toBe('false')
  })

  it('the stylesheet answers that attribute, so the variant is not decoration', () => {
    expect(CSS).toContain("[data-wrap='false']")
    // `pre`, not `pre-wrap`: the line stays one line and the block's own
    // scroll box — declared on `.ui-code-block` above it — is what moves.
    expect(CSS).toMatch(/\[data-wrap='false'\][^}]*white-space:\s*pre;/)
  })

  it('keeps its identity and its accessible name in both forms', () => {
    for (const el of [block(), block(false)]) {
      expect(el.tagName).toBe('PRE')
      expect(el.getAttribute('aria-label')).toBe('Machine output')
      // A scrollable region a mouse wheel alone can move is unreachable by
      // keyboard, and the variant that scrolls sideways needs it most.
      expect(el.tabIndex).toBe(0)
    }
  })

  it('renders a copy control without duplicating the payload', () => {
    const host = document.createElement('div')
    document.body.appendChild(host)
    render(
      () => (
        <CodeBlock copy={() => Promise.resolve()} ariaLabel="Payload">
          {'alpha\nbeta'}
        </CodeBlock>
      ),
      { container: host },
    )

    expect(host.querySelector('.ui-code-block')?.textContent).toBe('alpha\nbeta')
    expect(host.querySelector('[aria-label="Copy code"]')).not.toBeNull()
  })

  it('does not reserve copy-control space when no copy operation is supplied', () => {
    const host = document.createElement('div')
    document.body.appendChild(host)
    render(() => <CodeBlock ariaLabel="Payload">alpha</CodeBlock>, { container: host })

    const wrap = host.querySelector('.ui-code-block-wrap')
    expect(wrap?.classList.contains('ui-code-block-wrap--copy')).toBe(false)
    expect(wrap?.querySelector('.ui-icon-button')).toBeNull()
  })

  // A BLOCK CARRYING AN INLINE COMPONENT HAS NO TEXT TO HAND OVER, and where
  // that component stands in for a secret's bytes (ADR-0021) those bytes are
  // exactly what must not leave through the clipboard. So the control is not
  // offered rather than offered and answering with the empty string.
  it('offers no copy control for a block whose children are not the text', () => {
    const host = document.createElement('div')
    document.body.appendChild(host)
    render(
      () => (
        <CodeBlock copy={() => Promise.resolve()} ariaLabel="Raw request">
          <span class="inline-stand-in">chip</span>
        </CodeBlock>
      ),
      { container: host },
    )

    const wrap = host.querySelector('.ui-code-block-wrap')
    expect(wrap?.querySelector('.inline-stand-in')).not.toBeNull()
    expect(wrap?.classList.contains('ui-code-block-wrap--copy')).toBe(false)
    expect(host.querySelector('[aria-label="Copy code"]')).toBeNull()
  })

  it('copies the exact payload and reports success', async () => {
    const copy = vi.fn().mockResolvedValue(undefined)
    const host = document.createElement('div')
    document.body.appendChild(host)
    render(
      () => (
        <CodeBlock copy={copy} ariaLabel="Payload">
          {'alpha\nbeta'}
        </CodeBlock>
      ),
      { container: host },
    )

    const button = screen.getByRole('button', { name: /copy code/i })
    fireEvent.click(button)

    await vi.waitFor(() => expect(copy).toHaveBeenCalledWith('alpha\nbeta'))
    const successToast = toasts()[toasts().length - 1]
    expect(successToast?.message).toBe('Code copied')
    expect(successToast?.level).toBe('success')
  })

  it('keeps the block usable and reports a clipboard refusal', async () => {
    const copy = vi.fn().mockRejectedValue(new Error('clipboard refused'))
    const host = document.createElement('div')
    document.body.appendChild(host)
    render(
      () => (
        <CodeBlock copy={copy} ariaLabel="Payload">
          alpha
        </CodeBlock>
      ),
      { container: host },
    )

    const button = screen.getByRole('button', { name: /copy code/i })
    fireEvent.click(button)

    await vi.waitFor(() => expect(toasts().length).toBe(1))
    expect(button.getAttribute('aria-label')).toBe('Copy code')
    expect(toasts()[0].message).toBe('Could not copy code')
    expect(toasts()[0].level).toBe('danger')
  })
})
