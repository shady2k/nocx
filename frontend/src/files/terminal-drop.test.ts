// @vitest-environment jsdom
// The terminal drop, driven the way a person drives it: a drag arrives at
// the pane element, and what happens next depends on the machine the tab
// is on — not on which code path a reader of the implementation expects.
import { beforeEach, describe, expect, it } from 'vitest'
import { attachTerminalDrop, type DropOrigin } from './terminal-drop'
import { createUploadFlow } from './upload-flow'
import { createUploadStore } from './upload-store'
import { fakeClock, fakeUploadServices } from './upload-fixtures'
import type { ToastLevel } from '../ui/toast'

const REMOTE: DropOrigin = {
  sessionId: 'a'.repeat(32),
  kind: 'ssh',
  cwd: '/home/deploy',
  cwdVerified: true,
}

const LOCAL: DropOrigin = {
  sessionId: 'b'.repeat(32),
  kind: 'local',
  cwd: '/Users/me',
  cwdVerified: true,
}

/** A DataTransfer jsdom does not have: the two things a handler reads. */
function filesTransfer(files: File[]): DataTransfer {
  return { types: ['Files'], files } as unknown as DataTransfer
}

/** What the tab strip's own drag looks like — no `Files`, so nothing here
 *  may act on it (frontend/src/layout/strip-drag.ts). */
function tabTransfer(): DataTransfer {
  return { types: ['application/x-nocx-tab'], files: [] } as unknown as DataTransfer
}

function fire(el: HTMLElement, type: string, dataTransfer: DataTransfer): DragEvent {
  const e = new Event(type, { bubbles: true, cancelable: true }) as DragEvent
  Object.defineProperty(e, 'dataTransfer', { value: dataTransfer })
  el.dispatchEvent(e)
  return e
}

function harness(origin: DropOrigin | null, opts: { native?: boolean } = {}) {
  const element = document.createElement('div')
  document.body.appendChild(element)
  const services = fakeUploadServices()
  const store = createUploadStore({ services, now: fakeClock().now })
  const said: Array<{ message: string; level: ToastLevel }> = []
  const report = (message: string, level: ToastLevel) => said.push({ message, level })
  const flow = createUploadFlow({
    services,
    store,
    ask: () => Promise.resolve({ answer: 'skip', applyToAll: false }),
    report,
  })
  const inserted: string[] = []
  const bindings: string[] = []
  let current = origin
  const detach = attachTerminalDrop({
    element,
    origin: () => current,
    services,
    flow,
    bindingFor: (sessionId) => {
      bindings.push(sessionId)
      return Promise.resolve('f'.repeat(32))
    },
    insert: (text) => inserted.push(text),
    report,
    native: () => opts.native === true,
  })
  return {
    element,
    services,
    store,
    said,
    inserted,
    bindings,
    detach,
    setOrigin: (o: DropOrigin | null) => {
      current = o
    },
  }
}

/** Let the drop's own promise chain settle — the handler is deliberately
 *  fire-and-forget, because a DOM listener cannot be awaited. */
async function settle(): Promise<void> {
  for (let i = 0; i < 8; i++) await Promise.resolve()
}

beforeEach(() => {
  document.body.innerHTML = ''
})

describe('a drop on a remote tab uploads into that tab’s cwd', () => {
  it('sends the file to the verified OSC 7 directory', async () => {
    const h = harness(REMOTE)
    h.services.nextResult = [{ transferId: 't1', ticket: 'tk', url: '/upload/tk' }]
    fire(h.element, 'drop', filesTransfer([new File(['hello'], 'notes.txt')]))
    await settle()

    expect(h.bindings).toEqual([REMOTE.sessionId])
    expect(h.services.uploads).toEqual([
      { bindingId: 'f'.repeat(32), destDir: '/home/deploy', name: 'notes.txt', size: 5 },
    ])
    expect(h.services.bodies).toEqual([{ url: '/upload/tk', size: 5 }])
    expect(h.store.transfer('t1')?.phase).toBe('running')
  })

  it('sends every file of a multi-file drop', async () => {
    const h = harness(REMOTE)
    h.services.nextResult = [{ transferId: 't1' }, { transferId: 't2' }]
    fire(h.element, 'drop', filesTransfer([new File(['a'], 'a.txt'), new File(['bb'], 'b.txt')]))
    await settle()
    expect(h.services.uploads.map((u) => u.name)).toEqual(['a.txt', 'b.txt'])
  })
})

describe('a drop on a LOCAL tab inserts the name and starts no transfer (D9)', () => {
  it('calls no upload method at all', async () => {
    const h = harness(LOCAL)
    fire(h.element, 'drop', filesTransfer([new File(['hello'], 'notes.txt')]))
    await settle()

    expect(h.inserted).toEqual(['notes.txt'])
    // The whole of D9: copying a file onto the machine it is already on is
    // not a thing anybody asked for.
    expect(h.services.uploads).toEqual([])
    expect(h.services.bodies).toEqual([])
    expect(h.store.transfers()).toEqual([])
    expect(h.bindings).toEqual([])
  })

  it('quotes a name a shell would otherwise split', async () => {
    const h = harness(LOCAL)
    fire(
      h.element,
      'drop',
      filesTransfer([new File(['a'], 'my report.txt'), new File(['b'], 'plain.txt')]),
    )
    await settle()
    expect(h.inserted).toEqual([`'my report.txt' plain.txt`])
  })
})

describe('a drag that is not a files drag is left entirely alone', () => {
  it('does not claim the tab strip’s reorder drag', () => {
    const h = harness(REMOTE)
    const over = fire(h.element, 'dragover', tabTransfer())
    const drop = fire(h.element, 'drop', tabTransfer())
    // Unclaimed: the strip's own handler sees an event nobody cancelled.
    expect(over.defaultPrevented).toBe(false)
    expect(drop.defaultPrevented).toBe(false)
    expect(h.element.dataset.dropActive).toBeUndefined()
    expect(h.services.uploads).toEqual([])
  })

  it('accepts a files drag, which is what makes the drop possible at all', () => {
    const h = harness(REMOTE)
    const over = fire(h.element, 'dragover', filesTransfer([]))
    expect(over.defaultPrevented).toBe(true)
    expect(h.element.dataset.dropActive).toBe('')
    fire(h.element, 'dragleave', filesTransfer([]))
    expect(h.element.dataset.dropActive).toBeUndefined()
  })
})

describe('the native drop, which never becomes a DOM event we act on', () => {
  it('uploads the tickets files.dropped minted, and sends no body', async () => {
    const h = harness(REMOTE, { native: true })
    h.services.nextResult = [{ transferId: 't1' }]
    h.services.emitDropped({
      sessionId: REMOTE.sessionId,
      sources: [{ sourceTicket: 'c'.repeat(32), name: 'notes.txt', size: 500 }],
    })
    await settle()
    expect(h.services.uploads[0].sourceTicket).toBe('c'.repeat(32))
    expect(h.services.bodies).toEqual([])
  })

  it('ignores a drop that landed on another tab', async () => {
    const h = harness(REMOTE, { native: true })
    h.services.emitDropped({
      sessionId: 'd'.repeat(32),
      sources: [{ sourceTicket: 'c'.repeat(32), name: 'notes.txt', size: 1 }],
    })
    await settle()
    expect(h.services.uploads).toEqual([])
  })

  it('does not also send the DOM drop, which would upload every file twice', async () => {
    const h = harness(REMOTE, { native: true })
    fire(h.element, 'drop', filesTransfer([new File(['hello'], 'notes.txt')]))
    await settle()
    expect(h.services.uploads).toEqual([])
  })
})

describe('when there is nowhere to put it, it says so', () => {
  it('refuses a tab that cannot speak for a machine', async () => {
    const h = harness(null)
    fire(h.element, 'drop', filesTransfer([new File(['a'], 'a.txt')]))
    await settle()
    expect(h.services.uploads).toEqual([])
    expect(h.said[0].message).toContain('which machine')
  })

  it('refuses an unverified cwd rather than guessing a directory on a server', async () => {
    const h = harness({ ...REMOTE, cwdVerified: false })
    fire(h.element, 'drop', filesTransfer([new File(['a'], 'a.txt')]))
    await settle()
    expect(h.services.uploads).toEqual([])
    expect(h.said[0].message).toContain('directory')
  })

  it('refuses when no binding could be had, rather than failing silently', async () => {
    const element = document.createElement('div')
    const services = fakeUploadServices()
    const store = createUploadStore({ services, now: fakeClock().now })
    const said: string[] = []
    const flow = createUploadFlow({
      services,
      store,
      ask: () => Promise.resolve({ answer: 'skip', applyToAll: false }),
      report: (m) => said.push(m),
    })
    attachTerminalDrop({
      element,
      origin: () => REMOTE,
      services,
      flow,
      bindingFor: () => Promise.resolve(null),
      insert: () => {},
      report: (m) => said.push(m),
      native: () => false,
    })
    fire(element, 'drop', filesTransfer([new File(['a'], 'a.txt')]))
    await settle()
    expect(services.uploads).toEqual([])
    expect(said[0]).toContain('could not be reached')
  })
})

describe('detaching', () => {
  it('stops listening on both halves and clears the attributes', async () => {
    const h = harness(REMOTE, { native: true })
    h.detach()
    h.services.emitDropped({
      sessionId: REMOTE.sessionId,
      sources: [{ sourceTicket: 'c'.repeat(32), name: 'a.txt', size: 1 }],
    })
    fire(h.element, 'drop', filesTransfer([new File(['a'], 'a.txt')]))
    await settle()
    expect(h.services.uploads).toEqual([])
  })
})
