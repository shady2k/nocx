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
import { cleanup, fireEvent, render } from '@solidjs/testing-library'
import { clearToasts, toasts } from './toast'
import { CodeBlock, mountCodeBlockCopyButton } from './code-block'

afterEach(() => {
  cleanup()
  clearToasts()
  document.body.innerHTML = ''
})

function imperativeBlock() {
  const wrapper = document.createElement('div')
  const block = document.createElement('div')
  block.className = 'ui-code-block ui-code-block-wrap'
  const host = document.createElement('div')
  block.append(host)
  wrapper.append(block)
  document.body.append(wrapper)
  return { wrapper, block, host }
}

function assertHeader(wrapper: HTMLElement, label: string): HTMLElement {
  const header = wrapper.querySelector<HTMLElement>('.ui-code-block__header')
  if (!header) throw new Error('CodeBlock rendered no header')
  expect(header.textContent).toContain(label)
  expect(header.querySelector('[aria-label="Copy code"]')).not.toBeNull()
  expect(header.nextElementSibling?.classList.contains('ui-code-block')).toBe(true)
  return header
}

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

  it('keeps compact prompts cornered and hides the header label', () => {
    expect(CSS).toMatch(
      /\.ui-prompt\[data-density='compact'\] \.ui-code-block-wrap--copy > \.ui-code-block__header\s*\{[^}]*display:\s*contents;/s,
    )
    expect(CSS).toMatch(
      /\.ui-prompt\[data-density='compact'\] \.ui-code-block-wrap--copy > \.ui-code-block__header > \.ui-code-block__label\s*\{[^}]*display:\s*none;/s,
    )
    expect(CSS).toMatch(
      /\.ui-code-block__header > \.ui-code-block__copy-host > \.ui-icon-button\s*\{[^}]*margin-left:\s*auto;/s,
    )
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

  it('renders a labeled header strip beside the copy control', () => {
    const host = document.createElement('div')
    document.body.appendChild(host)
    render(
      () => (
        <CodeBlock copy={() => Promise.resolve()} label="bash" ariaLabel="Payload">
          {'printf hello'}
        </CodeBlock>
      ),
      { container: host },
    )

    assertHeader(host, 'bash')
    expect(host.querySelector('.ui-code-block')?.textContent).toBe('printf hello')
  })

  it('uses a generic code label when the caller has no kind', () => {
    const host = document.createElement('div')
    document.body.appendChild(host)
    render(
      () => (
        <CodeBlock copy={() => Promise.resolve()} ariaLabel="Payload">
          {'printf hello'}
        </CodeBlock>
      ),
      { container: host },
    )

    assertHeader(host, 'Code')
  })

  it('mounts the same labeled header around an imperative block', () => {
    const { wrapper, host } = imperativeBlock()
    const dispose = mountCodeBlockCopyButton(host, {
      label: 'bash',
      getText: () => 'printf hello',
      copy: () => Promise.resolve(),
    })

    assertHeader(wrapper, 'bash')
    expect(wrapper.querySelector('.ui-code-block')?.contains(host)).toBe(false)

    dispose()
    wrapper.remove()
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

    const button = host.querySelector<HTMLButtonElement>('[aria-label="Copy code"]')!
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

    const button = host.querySelector<HTMLButtonElement>('[aria-label="Copy code"]')!
    fireEvent.click(button)

    await vi.waitFor(() => expect(toasts().length).toBe(1))
    expect(button.getAttribute('aria-label')).toBe('Copy code')
    expect(toasts()[0].message).toBe('Could not copy code')
    expect(toasts()[0].level).toBe('danger')
  })
})

describe('CodeBlock answer variant', () => {
  it('marks an answer code block as the bordered answer variant', () => {
    const el = block()
    expect(el.dataset.variant).toBeUndefined()

    const host = document.createElement('div')
    document.body.appendChild(host)
    render(
      () => (
        <CodeBlock variant="answer" ariaLabel="Answer code">
          {'fenced answer'}
        </CodeBlock>
      ),
      { container: host },
    )

    const answer = host.querySelector<HTMLElement>('.ui-code-block')
    expect(answer?.dataset.variant).toBe('answer')
    expect(answer?.classList.contains('ui-code-block')).toBe(true)
  })

  it('defines the answer border in the CodeBlock stylesheet', () => {
    expect(CSS).toMatch(/\.ui-code-block\[data-variant='answer'\][^}]*border:\s*1px solid/s)
  })
})
