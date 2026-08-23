// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
import { pickUploadSources } from './upload-picker'
import { fakeUploadServices } from './upload-fixtures'

describe('in the Wails window the picker mints a ticket', () => {
  it('answers with the ticket, the name and the size — and never a path', async () => {
    const services = fakeUploadServices()
    services.nextPick = { sourceTicket: 'c'.repeat(32), name: 'notes.txt', size: 12 }
    const picked = await pickUploadSources({
      services,
      report: () => {},
      native: () => true,
    })
    expect(picked).toEqual([{ name: 'notes.txt', size: 12, sourceTicket: 'c'.repeat(32) }])
    // The renderer holds no bytes on this path: the file lives on the
    // backend's machine and the ticket is all it may know about it.
    expect(picked[0].blob).toBeUndefined()
  })

  it('reads an empty ticket as cancel, not as a file', async () => {
    const services = fakeUploadServices()
    // 0 is also a genuinely empty file, which is why the TICKET is what
    // says whether anything was chosen.
    services.nextPick = { sourceTicket: '', name: '', size: 0 }
    await expect(
      pickUploadSources({ services, report: () => {}, native: () => true }),
    ).resolves.toEqual([])
  })

  it('says so when the picker is not there, rather than failing silently', async () => {
    const services = fakeUploadServices()
    services.pickError = new Error('Method not found')
    const said: string[] = []
    const picked = await pickUploadSources({
      services,
      report: (m) => said.push(m),
      native: () => true,
    })
    expect(picked).toEqual([])
    expect(said[0]).toContain('not available')
  })
})

describe('in a browser the picker hands over bytes', () => {
  it('raises the platform picker and answers with what was chosen', async () => {
    const services = fakeUploadServices()
    const chosen = [new File(['hello'], 'a.txt'), new File(['ab'], 'b.txt')]
    // The picker is the one step a test cannot perform, so the click is
    // intercepted and the choice is made for it.
    const click = vi.spyOn(HTMLInputElement.prototype, 'click').mockImplementation(function (
      this: HTMLInputElement,
    ) {
      Object.defineProperty(this, 'files', { value: chosen })
      this.dispatchEvent(new Event('change'))
    })
    try {
      const picked = await pickUploadSources({
        services,
        report: () => {},
        native: () => false,
      })
      expect(picked.map((p) => [p.name, p.size])).toEqual([
        ['a.txt', 5],
        ['b.txt', 2],
      ])
      expect(picked[0].blob).toBe(chosen[0])
      // It never asks the backend for a source on this path — there is no
      // Wails to ask, and deliberately no fallback that would let the
      // renderer name one instead.
      expect(services.nextPick).toBeNull()
      expect(click).toHaveBeenCalled()
    } finally {
      click.mockRestore()
    }
  })

  it('leaves nothing in the document behind it', async () => {
    const services = fakeUploadServices()
    const click = vi.spyOn(HTMLInputElement.prototype, 'click').mockImplementation(function (
      this: HTMLInputElement,
    ) {
      this.dispatchEvent(new Event('cancel'))
    })
    try {
      await expect(
        pickUploadSources({ services, report: () => {}, native: () => false }),
      ).resolves.toEqual([])
      expect(document.querySelector('input[type="file"]')).toBeNull()
    } finally {
      click.mockRestore()
    }
  })
})
