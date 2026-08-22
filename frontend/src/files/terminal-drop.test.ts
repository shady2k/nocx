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

describe('a BROWSER drop on a LOCAL tab has no path, so it uploads', () => {
  it('sends the file into the tab’s verified cwd, like any other drop', async () => {
    const h = harness(LOCAL)
    h.services.nextResult = [{ transferId: 't1', ticket: 'tk', url: '/upload/tk' }]
    fire(h.element, 'drop', filesTransfer([new File(['hello'], 'notes.txt')]))
    await settle()

    // The corrected D9: whoever has the path inserts it, whoever has only
    // the bytes uploads them. A browser `File` is bytes and a name, so the
    // bytes go onto the backend's machine — which is the machine this tab's
    // shell is on, so R1 is satisfied rather than bent.
    expect(h.bindings).toEqual([LOCAL.sessionId])
    expect(h.services.uploads).toEqual([
      { bindingId: 'f'.repeat(32), destDir: '/Users/me', name: 'notes.txt', size: 5 },
    ])
    expect(h.services.bodies).toEqual([{ url: '/upload/tk', size: 5 }])
    expect(h.store.transfer('t1')?.phase).toBe('running')
    // And no path was made up for the prompt.
    expect(h.inserted).toEqual([])
  })

  it('sends every file of a multi-file drop, and says nothing about browsers', async () => {
    const h = harness(LOCAL)
    h.services.nextResult = [{ transferId: 't1' }, { transferId: 't2' }]
    fire(
      h.element,
      'drop',
      filesTransfer([new File(['a'], 'my report.txt'), new File(['b'], 'plain.txt')]),
    )
    await settle()

    expect(h.services.uploads.map((u) => u.name)).toEqual(['my report.txt', 'plain.txt'])
    expect(h.said).toEqual([])
  })

  it('takes the SAME collision question and cancellation a remote drop takes', async () => {
    // Nothing about the collision path is per-transport, and this is what
    // says so: the flow the local branch reaches is the flow the remote one
    // reaches, so the ask the harness wires (answer: skip) governs both.
    const h = harness(LOCAL)
    h.services.nextResult = [{ collision: 'exists' }]
    fire(h.element, 'drop', filesTransfer([new File(['hello'], 'notes.txt')]))
    await settle()

    // Asked, answered "skip", and the answer went back on a second call —
    // and no body was ever sent, because a skip moves nothing.
    expect(h.services.uploads).toHaveLength(2)
    expect(h.services.uploads[1]).toMatchObject({ name: 'notes.txt', onExists: 'skip' })
    expect(h.services.bodies).toEqual([])
  })

  it('refuses an unverified cwd rather than guessing a directory on this machine', async () => {
    // The same refusal a remote tab already gets. An upload is a write, and
    // nocx does not write into a directory it only guessed — on its own
    // machine as much as on anybody else's.
    const h = harness({ ...LOCAL, cwdVerified: false })
    fire(h.element, 'drop', filesTransfer([new File(['a'], 'a.txt')]))
    await settle()

    expect(h.services.uploads).toEqual([])
    expect(h.said[0].message).toContain('directory')
    expect(h.said[0].level).toBe('warning')
  })

  it('refuses a local tab that has no cwd at all', async () => {
    const h = harness({ ...LOCAL, cwd: null })
    fire(h.element, 'drop', filesTransfer([new File(['a'], 'a.txt')]))
    await settle()
    expect(h.services.uploads).toEqual([])
    expect(h.said[0].message).toContain('directory')
  })
})

describe('a native drop on a LOCAL tab inserts the path D9 promised', () => {
  it('inserts the absolute path, not the base name', async () => {
    const h = harness(LOCAL, { native: true })
    h.services.emitDropped({
      sessionId: LOCAL.sessionId,
      sources: [
        {
          sourceTicket: '',
          name: 'report.pdf',
          size: 12,
          localPath: '/home/dev/Downloads/report.pdf',
        },
      ],
    })
    await settle()

    expect(h.inserted).toEqual(['/home/dev/Downloads/report.pdf'])
    // Still the whole of D9: no bytes move onto the machine they are on.
    expect(h.services.uploads).toEqual([])
    expect(h.services.bodies).toEqual([])
    expect(h.bindings).toEqual([])
  })

  it('quotes a path a shell would otherwise split', async () => {
    const h = harness(LOCAL, { native: true })
    h.services.emitDropped({
      sessionId: LOCAL.sessionId,
      sources: [
        { sourceTicket: '', name: 'my report.txt', size: 1, localPath: '/home/dev/my report.txt' },
        { sourceTicket: '', name: 'plain.txt', size: 1, localPath: '/home/dev/plain.txt' },
      ],
    })
    await settle()
    expect(h.inserted).toEqual([`'/home/dev/my report.txt' /home/dev/plain.txt`])
  })

  it('refuses rather than inserting a name when the path did not arrive', async () => {
    // Neither half of the rule applies: no path, and a native local drop
    // mints no source ticket (ws_upload_source.go: nothing is minted for a
    // local tab), so there are no bytes to upload either. Starting a
    // transfer whose body can never arrive would leave the person watching
    // "uploading" forever.
    const h = harness(LOCAL, { native: true })
    h.services.emitDropped({
      sessionId: LOCAL.sessionId,
      sources: [{ sourceTicket: '', name: 'report.pdf', size: 12 }],
    })
    await settle()
    expect(h.inserted).toEqual([])
    expect(h.services.uploads).toEqual([])
    expect(h.said).toHaveLength(1)
  })

  it('still inserts, and starts no transfer, now that the browser half uploads', async () => {
    // The half that already worked and would break silently: the desktop
    // drop must keep inserting the absolute path, not fall through to the
    // upload the browser half now takes.
    const h = harness(LOCAL, { native: true })
    h.services.nextResult = [{ transferId: 't1', ticket: 'tk', url: '/upload/tk' }]
    h.services.emitDropped({
      sessionId: LOCAL.sessionId,
      sources: [
        { sourceTicket: '', name: 'a.pdf', size: 1, localPath: '/home/dev/a.pdf' },
        { sourceTicket: '', name: 'b.pdf', size: 2, localPath: '/home/dev/b.pdf' },
      ],
    })
    await settle()

    expect(h.inserted).toEqual(['/home/dev/a.pdf /home/dev/b.pdf'])
    expect(h.services.uploads).toEqual([])
    expect(h.services.bodies).toEqual([])
    expect(h.bindings).toEqual([])
    expect(h.store.transfers()).toEqual([])
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

  it('uploads a remote drop and inserts nothing — no path reaches the prompt', async () => {
    const h = harness(REMOTE, { native: true })
    h.services.nextResult = [{ transferId: 't1' }]
    h.services.emitDropped({
      sessionId: REMOTE.sessionId,
      sources: [{ sourceTicket: 'c'.repeat(32), name: 'notes.txt', size: 500 }],
    })
    await settle()
    // The regression that would matter: a remote tab is where a credential
    // exists and bytes move, and it learns a name and a size and nothing
    // else about the backend's filesystem (R2).
    expect(h.services.uploads).toHaveLength(1)
    expect(h.inserted).toEqual([])
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
