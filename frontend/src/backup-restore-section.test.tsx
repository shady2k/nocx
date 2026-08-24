// @vitest-environment jsdom
/**
 * BackupRestoreSection — what a user can do, and what must not happen to them.
 *
 * These assert through the seams a person reaches: the button exists, it is
 * enabled from the state they start in, pressing it reaches the client, and the
 * outcome is visible afterwards. The one place that is worth stating twice is
 * the save dialog: `backup.saveToFile` resolves to `null` when the user
 * cancels, and a cancelled save must leave no file behind — a component that
 * treats "cancelled" and "no dialog available" alike hands the user the file
 * they just declined.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor, cleanup } from '@solidjs/testing-library'
import { BackupRestoreSection } from './backup-restore-section'
import type { ProfileClient, BackupCreateResult, RestorePreview, RestoreResult } from './profiles'
import { MAX_BACKUP_BYTES } from './backup-file'

const toasts: { message: string; level?: string }[] = []
vi.mock('./ui/toast', () => ({
  showToast: (t: { message: string; level?: string }) => {
    toasts.push(t)
  },
}))

let confirmAnswer = true
vi.mock('./ui/dialog', () => ({
  showConfirm: () => Promise.resolve(confirmAnswer),
}))

const downloaded: { fileName: string; contents: string }[] = []
vi.mock('./backup-file', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./backup-file')>()
  return {
    ...actual,
    downloadText: (fileName: string, contents: string) => {
      downloaded.push({ fileName, contents })
    },
  }
})

const DOC =
  '{"format":"nocx-backup","version":1,"createdAt":"2026-08-12T12:00:00Z",' +
  '"settings":{"overrides":{}},"connections":{"profiles":[],"groups":[]}}'

const CREATED: BackupCreateResult = {
  fileName: 'nocx-backup-20260812T120000Z.json',
  contents: DOC,
  summary: {
    settings: 2,
    connections: 3,
    groups: 1,
    snippets: 4,
    notes: 3,
    credentialBindingsRemoved: 1,
    groupCredentialBindingsRemoved: 0,
    groupDefaultKeysOmitted: 0,
  },
}

const PREVIEW: RestorePreview = {
  previewToken: 'token-1',
  createdAt: '2026-08-12T12:00:00Z',
  strategy: 'merge',
  settings: { included: 2, changed: 1, reset: 0 },
  connections: { included: 3, added: 2, updated: 1, removed: 0 },
  groups: { included: 1, added: 0, updated: 1, removed: 0 },
  snippets: { included: 4 },
  notes: { included: 3 },
  connectionsRequiringCredential: [{ id: 'p1', name: 'My Server' }],
  omissions: {
    credentialBindingsRemoved: 1,
    groupCredentialBindingsRemoved: 0,
    groupDefaultKeysOmitted: 0,
  },
}

const RESTORED: RestoreResult = {
  strategy: 'merge',
  settingsChanged: 1,
  settingsReset: 0,
  connectionsAdded: 2,
  connectionsUpdated: 1,
  connectionsRemoved: 0,
  groupsAdded: 0,
  groupsUpdated: 1,
  groupsRemoved: 0,
  groupCredentialBindingsRemoved: 0,
  connectionsRequiringCredential: [{ id: 'p1', name: 'My Server' }],
  omissions: {
    credentialBindingsRemoved: 1,
    groupCredentialBindingsRemoved: 0,
    groupDefaultKeysOmitted: 0,
  },
}

/**
 * The spies are held here rather than reached through the client object, so an
 * assertion never detaches a method from its receiver (`@typescript-eslint/
 * unbound-method`) — the same shape input-target.test.ts uses.
 */
interface Spies {
  create: ReturnType<typeof vi.fn>
  save: ReturnType<typeof vi.fn>
  preview: ReturnType<typeof vi.fn>
  restore: ReturnType<typeof vi.fn>
}

function mockClient(overrides: Partial<Spies> = {}): { client: ProfileClient; spies: Spies } {
  const spies: Spies = {
    create: vi.fn().mockResolvedValue(CREATED),
    save: vi.fn().mockResolvedValue({ path: '/home/u/backup.json' }),
    preview: vi.fn().mockResolvedValue(PREVIEW),
    restore: vi.fn().mockResolvedValue(RESTORED),
    ...overrides,
  }
  const client = {
    createBackup: spies.create,
    saveBackupToFile: spies.save,
    previewBackupRestore: spies.preview,
    restoreBackup: spies.restore,
  } as unknown as ProfileClient
  return { client, spies }
}

/** Drives the file input the way a user picking a file does. */
async function chooseFile(text = DOC) {
  const input = document.querySelector('input[type="file"]') as HTMLInputElement
  const file = new File([text], 'backup.json', { type: 'application/json' })
  Object.defineProperty(input, 'files', { value: [file], configurable: true })
  fireEvent.change(input)
  await waitFor(() => expect(screen.getByText(/Restore strategy/i)).toBeTruthy())
}

beforeEach(() => {
  toasts.length = 0
  downloaded.length = 0
  confirmAnswer = true
  vi.clearAllMocks()
})
afterEach(() => cleanup())

describe('creating a backup', () => {
  it('saves through the native dialog and says where the file went', async () => {
    const { client, spies } = mockClient()
    render(() => <BackupRestoreSection profileClient={client} />)

    fireEvent.click(screen.getByRole('button', { name: 'Create backup' }))

    await waitFor(() => expect(spies.save).toHaveBeenCalled())
    expect(spies.save.mock.calls[0]).toEqual([CREATED.fileName, CREATED.contents])
    await waitFor(() => expect(toasts.some((t) => t.message.includes('backup.json'))).toBe(true))
    expect(downloaded).toHaveLength(0)
  })

  it('says what was left behind, on the dialog path as well as the download one', async () => {
    // CREATED.summary drops one credential binding; a user who saved through
    // the dialog must be told that as plainly as one who downloaded.
    const { client } = mockClient()
    render(() => <BackupRestoreSection profileClient={client} />)

    fireEvent.click(screen.getByRole('button', { name: 'Create backup' }))

    await waitFor(() =>
      expect(toasts.some((t) => t.message.includes('1 credential binding(s) removed'))).toBe(true),
    )
  })

  it('cancelling the save dialog leaves no file behind', async () => {
    // backup.saveToFile resolves null for a cancelled dialog — see
    // contracts/backup.saveToFile.schema.json, which models it as `null`.
    const { client } = mockClient({ save: vi.fn().mockResolvedValue(null) })
    render(() => <BackupRestoreSection profileClient={client} />)

    fireEvent.click(screen.getByRole('button', { name: 'Create backup' }))

    await waitFor(() =>
      expect(
        screen.getByRole<HTMLButtonElement>('button', { name: 'Create backup' }).disabled,
      ).toBe(false),
    )
    expect(downloaded).toHaveLength(0)
    expect(toasts.some((t) => t.level === 'danger')).toBe(false)
  })

  it('falls back to a download when no native dialog exists', async () => {
    const { client } = mockClient({
      save: vi.fn().mockRejectedValue(new Error('backup.saveToFile not available')),
    })
    render(() => <BackupRestoreSection profileClient={client} />)

    fireEvent.click(screen.getByRole('button', { name: 'Create backup' }))

    await waitFor(() => expect(downloaded).toHaveLength(1))
    expect(downloaded[0].fileName).toBe(CREATED.fileName)
    await waitFor(() => expect(toasts.some((t) => t.message.includes('3 connections'))).toBe(true))
  })

  it('reports a failed create instead of silently doing nothing', async () => {
    const { client } = mockClient({
      create: vi.fn().mockRejectedValue(new Error('config domain busy')),
    })
    render(() => <BackupRestoreSection profileClient={client} />)

    fireEvent.click(screen.getByRole('button', { name: 'Create backup' }))

    await waitFor(() =>
      expect(
        toasts.some((t) => t.level === 'danger' && t.message.includes('config domain busy')),
      ).toBe(true),
    )
    expect(downloaded).toHaveLength(0)
  })
})

describe('restoring a backup', () => {
  it('offers no restore until a file has been previewed', () => {
    render(() => <BackupRestoreSection profileClient={mockClient().client} />)
    expect(screen.queryByRole('button', { name: /Merge backup|Replace configuration/ })).toBeNull()
  })

  it('previews the chosen file and names the connections that will need a credential', async () => {
    const { client, spies } = mockClient()
    render(() => <BackupRestoreSection profileClient={client} />)

    await chooseFile()

    await waitFor(() => expect(spies.preview).toHaveBeenCalledWith(DOC, 'merge'))
    await waitFor(() => expect(screen.getByText(/My Server/)).toBeTruthy())
  })

  it('shows the snippet and note counts in the preview table', async () => {
    // A REPLACE restore wipes both libraries; the preview must say so
    // before the person commits to it. Notes matter most here — a snippet
    // can be retyped from the thing it automates, and a note cannot be
    // retyped from anything.
    const { client } = mockClient()
    render(() => <BackupRestoreSection profileClient={client} />)

    await chooseFile()

    await waitFor(() => expect(screen.getByText('Included')).toBeTruthy())
    const row = screen.getByText('Included').closest('tr')
    expect(row?.textContent).toBe('Included23143')
  })

  it('re-previews under the other strategy when the user switches it', async () => {
    const { client, spies } = mockClient()
    render(() => <BackupRestoreSection profileClient={client} />)
    await chooseFile()
    await waitFor(() => expect(spies.preview).toHaveBeenCalledWith(DOC, 'merge'))

    fireEvent.click(screen.getByRole('radio', { name: /Replace/i }))

    await waitFor(() => expect(spies.preview).toHaveBeenCalledWith(DOC, 'replace'))
  })

  it('sends the previewed token with the restore, and reports the outcome', async () => {
    const { client, spies } = mockClient()
    render(() => <BackupRestoreSection profileClient={client} />)
    await chooseFile()
    await waitFor(() => expect(screen.getByText(/My Server/)).toBeTruthy())

    fireEvent.click(await screen.findByRole('button', { name: /Merge backup/i }))

    await waitFor(() =>
      expect(spies.restore).toHaveBeenCalledWith(DOC, 'merge', PREVIEW.previewToken),
    )
    await waitFor(() =>
      expect(toasts.some((t) => t.message.includes('Restore complete'))).toBe(true),
    )
  })

  it('does not restore when the user declines the confirmation', async () => {
    confirmAnswer = false
    const { client, spies } = mockClient()
    render(() => <BackupRestoreSection profileClient={client} />)
    await chooseFile()
    await waitFor(() => expect(screen.getByText(/My Server/)).toBeTruthy())

    fireEvent.click(await screen.findByRole('button', { name: /Merge backup/i }))

    await waitFor(() => expect(confirmAnswer).toBe(false))
    expect(spies.restore).not.toHaveBeenCalled()
  })

  it('a stale preview is refused and recomputed rather than retried blind', async () => {
    const { client, spies } = mockClient({
      restore: vi.fn().mockRejectedValue(new Error('invalid document: preview is stale')),
    })
    render(() => <BackupRestoreSection profileClient={client} />)
    await chooseFile()
    await waitFor(() => expect(screen.getByText(/My Server/)).toBeTruthy())
    const previewsBefore = spies.preview.mock.calls.length

    fireEvent.click(await screen.findByRole('button', { name: /Merge backup/i }))

    await waitFor(() =>
      expect(toasts.some((t) => t.level === 'danger' && t.message.includes('stale'))).toBe(true),
    )
    await waitFor(() => expect(spies.preview.mock.calls.length).toBeGreaterThan(previewsBefore))
    expect(spies.restore).toHaveBeenCalledTimes(1)
  })

  it('rejects a file that is not a backup without calling the backend', async () => {
    const { client, spies } = mockClient()
    render(() => <BackupRestoreSection profileClient={client} />)

    const input = document.querySelector('input[type="file"]') as HTMLInputElement
    const file = new File(['not json at all'], 'notes.txt', { type: 'text/plain' })
    Object.defineProperty(input, 'files', { value: [file], configurable: true })
    fireEvent.change(input)

    await waitFor(() => expect(screen.queryByText(/Restore strategy/i)).toBeNull())
    expect(spies.preview).not.toHaveBeenCalled()
  })

  it('a backup file over the size cap is refused and says the limit in a person\u2019s unit', async () => {
    // readBackupText throws exactly one error of its own — the size cap — and
    // it is the only rejection that reach the catch in loadPreview with a
    // nameable cause. The toast must say the limit in MiB, not the raw byte
    // count the error string carries.
    const { client, spies } = mockClient()
    render(() => <BackupRestoreSection profileClient={client} />)

    const input = document.querySelector('input[type="file"]') as HTMLInputElement
    // A 1-byte body with an overridden `size` — exercising the size guard
    // without allocating an 8 MiB buffer.
    const file = new File(['x'], 'big.json', { type: 'application/json' })
    Object.defineProperty(file, 'size', { value: MAX_BACKUP_BYTES + 1 })
    Object.defineProperty(input, 'files', { value: [file], configurable: true })
    fireEvent.change(input)

    await waitFor(() => {
      expect(
        toasts.some((t) => t.level === 'danger' && t.message.includes('larger than 8 MiB')),
      ).toBe(true)
    })
    expect(spies.preview).not.toHaveBeenCalled()
  })

  it('a preview the backend refuses reaches the user as a toast, not in the flow', async () => {
    // Readable bytes can still be refused by the backend — a wrong format, an
    // unsupported version. The outcome of the preview action is a toast
    // (ui/README.md "Toast"); the private `.backup-restore__error` div is
    // gone.
    const { client, spies } = mockClient({
      preview: vi
        .fn()
        .mockRejectedValue(
          new Error('invalid backup document: expected format "nocx-backup", got "other"'),
        ),
    })
    render(() => <BackupRestoreSection profileClient={client} />)

    await chooseFile()

    await waitFor(() => {
      expect(
        toasts.some(
          (t) =>
            t.level === 'danger' &&
            t.message.includes('expected format "nocx-backup", got "other"'),
        ),
      ).toBe(true)
    })
    expect(document.querySelector('.backup-restore__error')).toBeNull()
    expect(spies.preview).toHaveBeenCalled()
  })
})
