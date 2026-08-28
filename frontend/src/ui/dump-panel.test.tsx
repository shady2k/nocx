// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render } from '@solidjs/testing-library'
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
})
