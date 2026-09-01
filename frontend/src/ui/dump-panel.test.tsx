// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render } from '@solidjs/testing-library'
import { DumpPanel } from './dump-panel'

const originalShowModal = HTMLDialogElement.prototype.showModal.bind(HTMLDialogElement.prototype)
const originalClose = HTMLDialogElement.prototype.close.bind(HTMLDialogElement.prototype)

beforeEach(() => {
  HTMLDialogElement.prototype.showModal = vi.fn()
  HTMLDialogElement.prototype.close = vi.fn()
})

afterEach(() => {
  cleanup()
  HTMLDialogElement.prototype.showModal = originalShowModal
  HTMLDialogElement.prototype.close = originalClose
})

function subject(dump: {
  request: Array<{ text: string; truncated: boolean }>
  response: Array<{ text: string; truncated: boolean }>
}) {
  return render(() => <DumpPanel dump={dump} copy={vi.fn(async () => {})} onClose={vi.fn()} />)
}

describe('DumpPanel', () => {
  it('identifies the panel and scopes truncation to the drive that was capped', () => {
    subject({
      request: [{ text: 'request bytes', truncated: true }],
      response: [{ text: 'response bytes', truncated: false }],
    })

    const panel = document.querySelector('.ui-dump-panel')
    const drives = [...document.querySelectorAll<HTMLElement>('.ui-dump-panel__drive')]

    expect(panel).not.toBeNull()
    expect(drives).toHaveLength(2)
    expect(drives[0].textContent).toContain('Truncated at the 1 MiB capture limit.')
    expect(drives[1].textContent).not.toContain('Truncated at the 1 MiB capture limit.')
  })

  it("renders the 'no recorded drive' fallback for an empty direction", () => {
    subject({
      request: [],
      response: [{ text: 'response bytes', truncated: false }],
    })

    const drives = [...document.querySelectorAll<HTMLElement>('.ui-dump-panel__drive')]

    expect(drives[0].textContent).toContain('No recorded drive.')
    expect(drives[1].textContent).not.toContain('No recorded drive.')
  })

  it('pretty-prints and highlights a request drive', async () => {
    subject({
      request: [{ text: '{"messages":[{"role":"user","content":"hello"}]}', truncated: false }],
      response: [],
    })
    await Promise.resolve()

    const request = document.querySelector<HTMLElement>('[aria-label="Request drive 1"]')
    expect(request?.textContent).toContain('"messages": [')
    expect(request?.textContent).toContain('\n')
    expect(request?.querySelector('.tok-string')).not.toBeNull()
  })

  it('shows the assembled response before collapsed raw SSE chunks', async () => {
    subject({
      request: [],
      response: [
        {
          text:
            'data: {"choices":[{"delta":{"role":"assistant","content":"Hello "}}]}\n\n' +
            'data: {"choices":[{"delta":{"content":"world"}}]}\n\n' +
            'data: {"choices":[{"delta":{"tool_calls":[{"id":"call_1","function":{"name":"session.read","arguments":"{\\"sessionId\\":\\"s1\\"}"}}]}}]}\n\n' +
            'data: [DONE]\n\n',
          truncated: false,
        },
      ],
    })
    await Promise.resolve()

    const response = document.querySelector<HTMLElement>('.ui-dump-panel__drive:nth-of-type(2)')
    expect(response?.querySelector('.ui-dump-panel__assembled')?.textContent).toContain(
      'Hello world',
    )
    expect(response?.querySelector('.ui-dump-panel__assembled')?.textContent).toContain(
      'session.read',
    )
    const raw = response?.querySelector('details')
    expect(raw?.open).toBe(false)
    expect(raw?.textContent).toContain('data:')
    expect(raw?.querySelector('.tok-meta')).not.toBeNull()
  })

  it('renders both sides of the highlighting ceiling', async () => {
    const under = '{"value":"' + 'x'.repeat(256 * 1024 - 20) + '"}'
    const over = '{"value":"' + 'x'.repeat(256 * 1024 + 20) + '"}'
    subject({
      request: [
        { text: under, truncated: false },
        { text: over, truncated: false },
      ],
      response: [],
    })
    await Promise.resolve()

    const blocks = [...document.querySelectorAll<HTMLElement>('[aria-label^="Request drive"]')]
    expect(blocks[0].querySelector('.tok-string')).not.toBeNull()
    expect(blocks[1].querySelector('.tok-string')).toBeNull()
    expect(blocks[1].closest('.ui-dump-panel__entry')?.textContent).toContain(
      'Highlighting is disabled for large dumps',
    )
  })

  it('opens the same dump as a full-page reader with working text search', async () => {
    subject({
      request: [{ text: '{"question":"find-me"}', truncated: false }],
      response: [
        {
          text: 'data: {"choices":[{"delta":{"content":"other"}}]}\n\ndata: [DONE]\n\n',
          truncated: false,
        },
      ],
    })
    const open = document.querySelector<HTMLButtonElement>('[aria-label="Open full page"]')
    expect(open).not.toBeNull()
    if (!open) throw new Error('Open full page control was not rendered')
    fireEvent.click(open)
    await Promise.resolve()

    const panel = document.querySelector<HTMLElement>('.nocx-dialog__panel')
    expect(panel?.dataset.size).toBe('full')
    expect(document.querySelector('.ui-dump-panel__drive')?.textContent).toContain('find-me')
    expect(document.querySelectorAll('.ui-dump-panel__raw')).toHaveLength(1)
    const search = document.querySelector<HTMLInputElement>('[aria-label="Search dump"]')
    expect(search).not.toBeNull()
    fireEvent.input(search!, { target: { value: 'find-me' } })
    await Promise.resolve()
    expect(document.querySelector('.ui-dump-panel__entry')?.textContent).toContain('find-me')
    expect(document.querySelectorAll('.ui-dump-panel__entry')).toHaveLength(1)
  })
})
