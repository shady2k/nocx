// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest'
import { MAX_BACKUP_BYTES, readBackupText, downloadText } from './backup-file'

describe('MAX_BACKUP_BYTES', () => {
  it('is 8 MiB', () => {
    expect(MAX_BACKUP_BYTES).toBe(8 * 1024 * 1024)
  })
})

describe('readBackupText', () => {
  it('reads file text content', async () => {
    const content = '{"format":"nocx-backup","version":1}'
    const file = new File([content], 'backup.json', { type: 'application/json' })
    const result = await readBackupText(file)
    expect(result).toBe(content)
  })

  it('rejects files exceeding MAX_BACKUP_BYTES', async () => {
    const large = 'x'.repeat(MAX_BACKUP_BYTES + 1)
    const file = new File([large], 'large.json', { type: 'application/json' })
    await expect(readBackupText(file)).rejects.toThrow('exceeds')
  })

  it('accepts files exactly at MAX_BACKUP_BYTES', async () => {
    const exact = 'x'.repeat(MAX_BACKUP_BYTES)
    const file = new File([exact], 'exact.json', { type: 'application/json' })
    const result = await readBackupText(file)
    expect(result).toBe(exact)
  })
})

describe('downloadText', () => {
  it('creates a blob URL and clicks a link', () => {
    const createObjectURL = vi.fn(() => 'blob:test')
    const revokeObjectURL = vi.fn()
    URL.createObjectURL = createObjectURL
    URL.revokeObjectURL = revokeObjectURL

    const clickSpy = vi.fn()
    const origCreateElement = document.createElement.bind(document)
    vi.spyOn(document, 'createElement').mockImplementation((tag: string) => {
      const el = origCreateElement(tag)
      if (tag === 'a') {
        vi.spyOn(el, 'click').mockImplementation(clickSpy)
      }
      return el
    })

    downloadText('test.json', '{}', 'application/json')

    expect(createObjectURL).toHaveBeenCalled()
    expect(clickSpy).toHaveBeenCalled()
    // cleanup
    vi.restoreAllMocks()
  })

  it('uses the provided filename', () => {
    const origCreateElement = document.createElement.bind(document)
    let capturedDownload = ''
    vi.spyOn(document, 'createElement').mockImplementation((tag: string) => {
      const el = origCreateElement(tag)
      if (tag === 'a') {
        Object.defineProperty(el, 'download', {
          set(v: string) { capturedDownload = v },
          get() { return capturedDownload },
        })
        vi.spyOn(el, 'click').mockImplementation(() => {})
      }
      return el
    })

    downloadText('my-backup.json', '{}')
    expect(capturedDownload).toBe('my-backup.json')
    vi.restoreAllMocks()
  })
})
