// @vitest-environment jsdom
//
// A download appears in the operations list, as THE SAME ROW an upload
// draws, with progress and with a cancel that reaches the wire.
//
// That is why this mounts the real operations view over a model fed by BOTH
// sources rather than asserting the projection's fields alone. The
// projection has unit assertions below, but they cannot report the thing
// the acceptance criterion is about: that download joined the list the
// product already has instead of growing a second one. Two rows of the same
// component, in one list, one of them cancellable through the download
// seam, is what says so.
import { cleanup } from '@solidjs/testing-library'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { fakeClock, fakeDownloadServices } from './download-fixtures'
import { createDownloadStore, type DownloadStore } from './download-store'
import { downloadOperations } from './download-operations'
import { beginTransfer, fakeUploadServices } from './upload-fixtures'
import { createUploadStore, type UploadStore } from './upload-store'
import { uploadOperations } from './upload-operations'
import { createOperationsModel } from '../operations/operations'
import {
  createOperationsView,
  OPERATIONS_VIEW_ID,
  type OperationsViewDeps,
} from '../operations/operations-view'
import { mountSidebar, type SidebarHandle } from '../sidebar'

let handle: SidebarHandle | null = null

function mount(deps: OperationsViewDeps = {}): {
  uploads: UploadStore
  downloads: DownloadStore
  services: ReturnType<typeof fakeDownloadServices>
} {
  const uploadServices = fakeUploadServices()
  const uploads = createUploadStore({ services: uploadServices, now: fakeClock().now })
  const services = fakeDownloadServices()
  const downloads = createDownloadStore({ services, now: fakeClock().now })
  const model = createOperationsModel([uploadOperations(uploads), downloadOperations(downloads)])

  const bar = document.createElement('div')
  bar.id = 'activitybar'
  const panel = document.createElement('div')
  panel.id = 'sidebar'
  document.body.append(bar, panel)
  handle = mountSidebar(bar, panel, [createOperationsView(model, deps)], [])
  document.querySelector<HTMLElement>(`button[data-view="${OPERATIONS_VIEW_ID}"]`)!.click()
  return { uploads, downloads, services }
}

afterEach(() => {
  handle?.destroy()
  handle = null
  document.body.replaceChildren()
  cleanup()
  vi.useRealTimers()
})

/** Both clocks the throttled list reads — the injected one and the fake
 *  timer its release is scheduled on. The list publishes NUMBERS a few
 *  times a second (render-throttle.ts), so a byte count asserted in the
 *  same tick it was emitted is asserted before the surface has drawn it.
 *  Driving both by hand rather than waiting is the rule: a test may not
 *  depend on timing. */
function ticking(): { deps: OperationsViewDeps; advance: (ms: number) => void } {
  vi.useFakeTimers()
  let t = 1000
  return {
    deps: { now: () => t },
    advance: (ms: number) => {
      t += ms
      vi.advanceTimersByTime(ms)
    },
  }
}

const rows = () => [...document.querySelectorAll<HTMLElement>('.ui-operation-row')]

function rowFor(title: string): HTMLElement {
  const row = rows().find((r) => r.querySelector('.ui-operation-row__title')?.textContent === title)
  if (!row) throw new Error(`no operation row titled ${title}`)
  return row
}

function cancelIn(row: HTMLElement): HTMLElement | undefined {
  // The row's own actions live beside it in the CollectionRow, so the
  // button is looked up on the shared list row rather than inside the info
  // span.
  const collection = row.closest('.ui-collection-row') ?? row.parentElement
  return [...(collection?.querySelectorAll<HTMLElement>('button') ?? [])].find((b) =>
    // BY ITS ACCESSIBLE NAME. Cancel became an icon button so it would stop
    // taking a third of a rail-width row from the content (nocx-hbdw4.5),
    // and an icon button has no text content at all.
    (b.getAttribute('aria-label') ?? '').startsWith('Cancel'),
  )
}

describe('a download in the operations list', () => {
  it('is the same row an upload is, in the one list, beside it', () => {
    const m = mount()
    beginTransfer(m.uploads, {
      transferId: 'u1',
      name: 'up.iso',
      destDir: '/srv',
      machine: 'alice@srv-01',
      size: 10,
    })
    m.downloads.begin({
      transferId: 'd1',
      name: 'down.iso',
      sourcePath: '/srv/down.iso',
      machine: 'alice@srv-01',
      size: 400,
    })
    expect(rows()).toHaveLength(2)
    // The same component: both carry the kit row's identity class, and the
    // download's glyph is the one operation-row's table names for its kind.
    expect(rowFor('down.iso')).not.toBeUndefined()
    expect(rowFor('up.iso')).not.toBeUndefined()
  })

  it('draws progress as the bytes arrive', () => {
    // Through the THROTTLE the merged list now publishes numbers behind
    // (render-throttle.ts): a byte count is held for a fraction of a
    // second, so this advances both of its clocks rather than waiting on
    // one. A download is subject to exactly the same rule as an upload,
    // which is the point — it is the same list.
    const c = ticking()
    const m = mount(c.deps)
    m.downloads.begin({
      transferId: 'd1',
      name: 'down.iso',
      sourcePath: '/srv/down.iso',
      machine: 'alice@srv-01',
      size: 400,
    })
    c.advance(250)
    m.services.emitProgress({ transferId: 'd1', bytes: 100, total: 400 })
    c.advance(250)
    const bar = rowFor('down.iso').querySelector('[role="progressbar"]')
    expect(bar?.getAttribute('aria-valuenow')).toBe('25')
  })

  it('says WHICH MACHINE the file came from, the way an upload row says where it went', () => {
    // The list is global — one list for every tab — so "/srv/down.iso" with
    // no machine beside it is a path that exists on every host a person has
    // open. Both rows carry it, from one derivation (machine-name.ts), and
    // the row draws them the same way.
    const m = mount()
    beginTransfer(m.uploads, {
      transferId: 'u1',
      name: 'up.iso',
      destDir: '/srv',
      machine: 'alice@srv-01',
      size: 10,
    })
    m.downloads.begin({
      transferId: 'd1',
      name: 'down.iso',
      sourcePath: '/srv/down.iso',
      machine: 'bob@srv-02',
      size: 400,
    })
    expect(rowFor('down.iso').textContent).toContain('bob@srv-02')
    expect(rowFor('up.iso').textContent).toContain('alice@srv-01')
    // And not each other's: two rows in one list must not blur into one
    // machine, which is the whole reason the field is on the item.
    expect(rowFor('down.iso').textContent).not.toContain('alice@srv-01')
  })

  it('offers a cancel that reaches files.downloadCancel', () => {
    const m = mount()
    m.downloads.begin({
      transferId: 'd1',
      name: 'down.iso',
      sourcePath: '/srv/d',
      machine: 'alice@srv-01',
      size: 400,
    })
    cancelIn(rowFor('down.iso'))!.click()
    expect(m.services.cancels).toEqual(['d1'])
  })

  it('takes the cancel away once it is over, and says Done', () => {
    const m = mount()
    m.downloads.begin({
      transferId: 'd1',
      name: 'down.iso',
      sourcePath: '/srv/d',
      machine: 'alice@srv-01',
      size: 400,
    })
    m.services.emitDone({
      transferId: 'd1',
      outcome: 'sent',
      name: 'down.iso',
      bytes: 400,
      total: 400,
    })
    expect(cancelIn(rowFor('down.iso'))).toBeUndefined()
    // `sent` reads the same word `written` does: they differ on the wire
    // and they do not differ to a person.
    expect(rowFor('down.iso').textContent).toContain('Done')
  })
})

describe('the projection itself', () => {
  function unit() {
    const services = fakeDownloadServices()
    const store = createDownloadStore({ services, now: fakeClock().now })
    return { services, store, ops: downloadOperations(store) }
  }

  it('carries what the row draws, and nothing the store did not say', () => {
    const f = unit()
    f.store.begin({
      transferId: 'd1',
      name: 'big.iso',
      sourcePath: '/srv/big.iso',
      machine: 'alice@srv-01',
      size: 400,
    })
    const [op, ...rest] = f.ops()
    expect(rest).toEqual([])
    // The WHOLE record but for the closure: a partial assertion is how a
    // field nobody set stays undefined until a surface reads it.
    const { cancel, ...data } = op
    expect(data).toEqual({
      id: 'd1',
      kind: 'download',
      title: 'big.iso',
      destination: '/srv/big.iso',
      machine: 'alice@srv-01',
      phase: 'running',
      done: null,
      total: 400,
      speedBytesPerSecond: null,
      error: null,
      startedAt: 1_000,
      endedAt: null,
    })
    expect(typeof cancel).toBe('function')
  })

  it('shows where the bytes CAME FROM, because nobody knows where they went', () => {
    // The browser chose the destination by its own mechanism and never told
    // the page. The source path is what lets a person tell two files of the
    // same name apart; a guessed "~/Downloads" would be an invention.
    const f = unit()
    f.store.begin({
      transferId: 'd1',
      name: 'a.txt',
      sourcePath: '/etc/a.txt',
      machine: 'alice@srv-01',
      size: 1,
    })
    expect(f.ops()[0].destination).toBe('/etc/a.txt')
  })

  it('carries no source for a transfer it never saw start', () => {
    const f = unit()
    f.services.emitDone({
      transferId: 'd9',
      outcome: 'sent',
      name: 'orphan.bin',
      bytes: 1,
      total: 1,
    })
    expect(f.ops()[0].destination).toBe('')
    // Nor a machine, and nor a start: this renderer never saw the call that
    // would have carried either.
    expect(f.ops()[0].machine).toBe('')
    expect(f.ops()[0].startedAt).toBeNull()
    expect(f.ops()[0].title).toBe('orphan.bin')
  })

  it('offers a cancel while the work is live, on both non-terminal phases', () => {
    const f = unit()
    f.store.begin({
      transferId: 'd1',
      name: 'a',
      sourcePath: '/a',
      machine: 'alice@srv-01',
      size: 1,
    })
    expect(f.ops()[0].cancel).not.toBeNull()
    // `unsettled` especially: the renderer lost sight of a transfer the
    // backend may still be sending, and files.downloadCancel reaches it.
    f.store.unsettle('d1', 'the connection dropped')
    expect(f.ops()[0].cancel).not.toBeNull()
  })

  it('offers none once it is over, on every terminal outcome', () => {
    for (const outcome of ['sent', 'cancelled', 'failed'] as const) {
      const f = unit()
      f.store.begin({
        transferId: 'd1',
        name: 'a',
        sourcePath: '/a',
        machine: 'alice@srv-01',
        size: 1,
      })
      f.services.emitDone({ transferId: 'd1', outcome, name: 'a', bytes: 1, total: 1 })
      expect(f.ops()[0].cancel).toBeNull()
    }
  })
})
