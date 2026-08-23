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
  machine: 'deploy@srv-01',
}

const LOCAL: DropOrigin = {
  sessionId: 'b'.repeat(32),
  kind: 'local',
  cwd: '/Users/me',
  cwdVerified: true,
  machine: 'This machine',
}

/**
 * A DataTransfer jsdom does not have, built the way a browser builds one:
 * `files` for the bytes and `items` for what each entry IS, in the same
 * order. A real drop always carries both, so a fake that carried only
 * `files` would let a folder through in the test and nowhere else.
 *
 * `folders` names the entries whose `webkitGetAsEntry()` answers
 * `isDirectory` — which is what a dropped folder looks like, since it
 * arrives as an ordinary `File` with the folder's name and its directory
 * entry's size.
 */
function filesTransfer(files: File[], folders: string[] = []): DataTransfer {
  const items = files.map((f) => ({
    kind: 'file',
    webkitGetAsEntry: () => ({
      isDirectory: folders.includes(f.name),
      isFile: !folders.includes(f.name),
    }),
  }))
  return { types: ['Files'], files, items } as unknown as DataTransfer
}

/** An engine that does not implement `items` at all — the fallback path,
 *  where nothing can be said about an entry and every one is treated as a
 *  file, which is what happened everywhere before the check existed. */
function transferWithoutItems(files: File[]): DataTransfer {
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
      target: 'terminal',
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
      target: 'terminal',
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
      target: 'terminal',
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
      target: 'terminal',
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

  it('ignores a drop on another surface of the same session', async () => {
    // The same tab, a different surface — the import ask. The pane must not
    // type the export's path at the person's prompt because they dropped it
    // into a dialog that happens to name this tab.
    const h = harness(LOCAL, { native: true })
    h.services.emitDropped({
      sessionId: LOCAL.sessionId,
      target: 'api-import',
      sources: [{ sourceTicket: '', name: 'acme.json', size: 2, localPath: '/work/acme.json' }],
    })
    await settle()
    expect(h.inserted).toEqual([])

    h.services.emitDropped({
      sessionId: LOCAL.sessionId,
      target: 'terminal',
      sources: [{ sourceTicket: '', name: 'acme.json', size: 2, localPath: '/work/acme.json' }],
    })
    await settle()
    expect(h.inserted).toHaveLength(1)
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
      target: 'terminal',
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
      target: 'terminal',
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
      target: 'terminal',
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
      target: 'terminal',
      sources: [{ sourceTicket: 'c'.repeat(32), name: 'a.txt', size: 1 }],
    })
    fire(h.element, 'drop', filesTransfer([new File(['a'], 'a.txt')]))
    await settle()
    expect(h.services.uploads).toEqual([])
  })
})

// ── A dropped FOLDER is refused, not attempted (nocx-hbdw4.6, amendment) ─
//
// The owner dropped a folder and got two rows with no file name, `0%`,
// "Waiting for the server" and a toast reading "Failed to fetch" — a
// network error offered as the answer to "can I send a folder". Directories
// are out of scope (design §4), so the answer belongs at the gesture,
// before anything is registered.
describe('a folder in a browser drop', () => {
  it('is not sent, and never becomes an operation', async () => {
    const h = harness(REMOTE)
    fire(h.element, 'drop', filesTransfer([new File([''], 'Photos')], ['Photos']))
    await settle()
    // Nothing on the wire, and no row: a row implies something is
    // happening to the thing it names, and nothing ever will.
    expect(h.services.uploads).toEqual([])
    expect(h.services.bodies).toEqual([])
    expect(h.store.transfers()).toEqual([])
    // No binding was even opened for it.
    expect(h.bindings).toEqual([])
  })

  it('is answered with the limit, not with whatever failed downstream', async () => {
    const h = harness(REMOTE)
    fire(h.element, 'drop', filesTransfer([new File([''], 'Photos')], ['Photos']))
    await settle()
    expect(h.said).toHaveLength(1)
    expect(h.said[0].message).toContain('cannot upload a folder yet')
    expect(h.said[0].message).toContain('Photos')
    // And it says what to do instead — somebody dropping a folder asked
    // for something reasonable.
    expect(h.said[0].message).toContain('archive')
    // Never the downstream symptom.
    expect(h.said[0].message).not.toContain('Failed to fetch')
    expect(h.said[0].level).toBe('warning')
  })

  it('does not take the files dropped beside it down with it', async () => {
    // Refusing the whole batch punishes the files that could be sent;
    // sending them silently leaves the person to work out which item is
    // missing. So: send the files, name the folder, say how many are going.
    const h = harness(REMOTE)
    h.services.nextResult = [{ transferId: 't1' }, { transferId: 't2' }, { transferId: 't3' }]
    fire(
      h.element,
      'drop',
      filesTransfer(
        [
          new File(['a'], 'a.txt'),
          new File([''], 'Photos'),
          new File(['b'], 'b.txt'),
          new File(['c'], 'c.txt'),
        ],
        ['Photos'],
      ),
    )
    await settle()
    expect(h.services.uploads.map((u) => u.name)).toEqual(['a.txt', 'b.txt', 'c.txt'])
    expect(h.said).toHaveLength(1)
    expect(h.said[0].message).toContain('Photos')
    expect(h.said[0].message).toContain('The other 3 files are being uploaded.')
    // The three that could be sent are the three rows, and the folder is
    // not among them.
    expect(h.store.transfers().map((t) => t.name)).toEqual(['a.txt', 'b.txt', 'c.txt'])
  })

  it('names every folder when more than one was dropped', async () => {
    const h = harness(REMOTE)
    fire(
      h.element,
      'drop',
      filesTransfer([new File([''], 'Photos'), new File([''], 'Docs')], ['Photos', 'Docs']),
    )
    await settle()
    expect(h.said).toHaveLength(1)
    expect(h.said[0].message).toContain('cannot upload folders yet')
    expect(h.said[0].message).toContain('Photos, Docs')
  })

  it('is decided by the entry API and not by what the File looks like', async () => {
    // 192 B is a directory entry's size on one filesystem, and an empty
    // MIME type is what plenty of real files have. A file that looks
    // exactly like the owner's folder — same size, no type, no extension —
    // is still uploaded.
    const h = harness(REMOTE)
    h.services.nextResult = [{ transferId: 't1' }]
    fire(h.element, 'drop', filesTransfer([new File(['x'.repeat(192)], 'Photos')]))
    await settle()
    expect(h.services.uploads.map((u) => [u.name, u.size])).toEqual([['Photos', 192]])
    expect(h.said).toEqual([])
  })

  it('says nothing about folders when an engine cannot tell it about them', async () => {
    // No `items` means nothing can be said, and a guess would refuse real
    // files. The drop behaves exactly as it did before the check existed.
    const h = harness(REMOTE)
    h.services.nextResult = [{ transferId: 't1' }]
    fire(h.element, 'drop', transferWithoutItems([new File(['a'], 'a.txt')]))
    await settle()
    expect(h.services.uploads.map((u) => u.name)).toEqual(['a.txt'])
    expect(h.said).toEqual([])
  })
})

// ── The name is the platform's, character for character ─────────────────
//
// The toast that came back from the folder drop read `15498198pril: Failed
// to fetch`, and `15498198pril` is not what anybody would call a folder —
// so the report was that something builds a display name from a timestamp
// and a fragment, and that whatever it is would mislabel a real file too.
//
// It does not exist. Every name on this path is carried, never composed:
// the browser half takes `File.name` verbatim (below), the Wails half takes
// `filepath.Base` of the dropped path (ws_upload_source.go), the picker
// takes the same, and nothing between here and the row touches either. This
// test is that claim in a form that can fail: a name only a generator could
// have produced goes in, and the same characters come out on the row.
describe('what a row calls a dropped file', () => {
  it('is the name the platform gave it, unaltered', async () => {
    const odd = '15498198pril'
    const h = harness(REMOTE)
    h.services.nextResult = [{ transferId: 't1' }]
    fire(h.element, 'drop', filesTransfer([new File(['a'], odd)]))
    await settle()
    expect(h.services.uploads.map((u) => u.name)).toEqual([odd])
    expect(h.store.transfers().map((t) => t.name)).toEqual([odd])
  })
})
