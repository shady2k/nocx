// The download gesture, end to end through the seam: mint, record, save.
//
// The failure paths are the point. For every external call the flow makes
// there is a test where that call fails, and the question each answers is
// not "did it go wrong" but "what is now true about the row": a refused
// mint leaves NO row, because a row for a transfer the backend never
// created can never receive a done frame and would sit unfinished for the
// life of the session.
import { describe, expect, it } from 'vitest'

import { createDownloadFlow } from './download-flow'
import { downloadResultFixture, fakeDownloadServices, fakeSaver } from './download-fixtures'
import { createDownloadStore } from './download-store'

function fixture() {
  const services = fakeDownloadServices()
  const store = createDownloadStore({ services })
  const saver = fakeSaver()
  const said: { message: string; level: string }[] = []
  const flow = createDownloadFlow({
    services,
    store,
    saver,
    report: (message, level) => said.push({ message, level }),
  })
  return { services, store, saver, flow, said }
}

describe('fetching one file', () => {
  it('names the file and nothing else — no destination crosses the seam', async () => {
    const f = fixture()
    f.services.nextResult.push(downloadResultFixture())
    await f.flow.fetch({ bindingId: 'b1', path: '/srv/big.iso', machine: 'alice@srv-01' })
    // The MACHINE is not on the wire and must not be: it is a label
    // `machine-name.ts` produced for a person, and the backend already
    // knows which host a binding names.
    expect(f.services.downloads).toEqual([{ bindingId: 'b1', path: '/srv/big.iso' }])
  })

  it('records the row from the RESULT, taking the backend`s name and size', async () => {
    // Not the tree's name: the backend measured and named the file on the
    // handle it opened, and a name carried from a row would be the
    // renderer's second opinion about it.
    const f = fixture()
    f.services.nextResult.push(downloadResultFixture({ name: 'measured.iso', size: 4096 }))
    await f.flow.fetch({ bindingId: 'b1', path: '/srv/big.iso', machine: 'alice@srv-01' })
    expect(f.store.transfers()).toHaveLength(1)
    expect(f.store.transfers()[0]).toMatchObject({
      name: 'measured.iso',
      size: 4096,
      sourcePath: '/srv/big.iso',
      phase: 'running',
    })
  })

  it('hands the platform the URL resolved against the socket, exactly once', async () => {
    // The result's url is a PATH on the backend's HTTP surface. Under
    // `dev-web` the page's origin is vite's and the backend is elsewhere,
    // so an unresolved path would fetch from the wrong server. Once,
    // because the ticket is one-shot.
    const f = fixture()
    const ticket = 'b'.repeat(64)
    f.services.nextResult.push(downloadResultFixture({ url: `/download/${ticket}` }))
    await f.flow.fetch({ bindingId: 'b1', path: '/srv/big.iso', machine: 'alice@srv-01' })
    expect(f.saver.saved).toEqual([`http://127.0.0.1:7331/download/${ticket}`])
  })

  it('says nothing on the happy path', async () => {
    // The file appearing in the person's downloads IS the report, exactly
    // as an uploaded file appearing in the destination is.
    const f = fixture()
    f.services.nextResult.push(downloadResultFixture())
    await f.flow.fetch({ bindingId: 'b1', path: '/srv/big.iso', machine: 'alice@srv-01' })
    expect(f.said).toEqual([])
  })
})

describe('when it cannot start', () => {
  it('a refused files.download reports and leaves NO row behind', async () => {
    // A row here would never receive a done frame — nothing was created to
    // send one — so it would sit "running" until the session ended.
    const f = fixture()
    f.services.download = () => Promise.reject(new Error('binding is closed'))
    await f.flow.fetch({ bindingId: 'b1', path: '/srv/big.iso', machine: 'alice@srv-01' })
    expect(f.store.transfers()).toEqual([])
    expect(f.said).toEqual([
      {
        message: 'Could not download /srv/big.iso: binding is closed',
        level: 'danger',
      },
    ])
    expect(f.saver.saved).toEqual([])
  })

  it('never rejects, whatever the seam does', async () => {
    const f = fixture()
    f.services.download = () => Promise.reject(new Error('boom'))
    await expect(
      f.flow.fetch({ bindingId: 'b1', path: '/x', machine: 'alice@srv-01' }),
    ).resolves.toBeUndefined()
  })

  it('with no connection to fetch over, the row FAILS rather than sitting at nothing', async () => {
    // The transfer exists on the backend and its ticket will expire
    // unredeemed. A row left running would be the renderer claiming a
    // download is in progress that it has not even asked for.
    const f = fixture()
    f.services.origin = null
    f.services.nextResult.push(downloadResultFixture({ name: 'big.iso' }))
    await f.flow.fetch({ bindingId: 'b1', path: '/srv/big.iso', machine: 'alice@srv-01' })
    expect(f.saver.saved).toEqual([])
    expect(f.store.transfers()[0]).toMatchObject({ phase: 'failed' })
    expect(f.store.transfers()[0].error).toContain('no connection')
    expect(f.said).toEqual([
      {
        message: 'big.iso: there is no connection to the backend to fetch the bytes over',
        level: 'danger',
      },
    ])
  })
})
