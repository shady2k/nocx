// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor, cleanup } from '@solidjs/testing-library'
import { BackupRestoreSection } from './backup-restore-section'
import type { ProfileClient, BackupCreateResult, RestorePreview, RestoreResult } from './profiles'
import { MAX_BACKUP_BYTES } from './backup-file'

function mockClient(overrides: Partial<ProfileClient> = {}): ProfileClient {
  return {
    createBackup: vi.fn().mockResolvedValue({
      fileName: 'nocx-backup-20260730T120000Z.json',
      contents:
        '{"format":"nocx-backup","version":1,"createdAt":"2026-07-30T12:00:00Z","settings":{"overrides":{}},"connections":{"profiles":[],"groups":[]}}',
      summary: {
        settings: 0,
        connections: 0,
        groups: 0,
        credentialBindingsRemoved: 0,
        groupCredentialBindingsRemoved: 0,
        groupDefaultKeysOmitted: 0,
      },
    } satisfies BackupCreateResult),
    previewBackupRestore: vi.fn().mockResolvedValue({
      previewToken: 'abc123',
      createdAt: '2026-07-30T12:00:00Z',
      strategy: 'merge',
      settings: { included: 0, changed: 0, reset: 0 },
      connections: { included: 0, added: 0, updated: 0, removed: 0 },
      groups: { included: 0, added: 0, updated: 0, removed: 0 },
      connectionsRequiringCredential: [],
      omissions: {
        credentialBindingsRemoved: 0,
        groupCredentialBindingsRemoved: 0,
        groupDefaultKeysOmitted: 0,
      },
    } satisfies RestorePreview),
    restoreBackup: vi.fn().mockResolvedValue({
      strategy: 'merge',
      settingsChanged: 0,
      settingsReset: 0,
      connectionsAdded: 0,
      connectionsUpdated: 0,
      connectionsRemoved: 0,
      groupsAdded: 0,
      groupsUpdated: 0,
      groupsRemoved: 0,
      groupCredentialBindingsRemoved: 0,
      connectionsRequiringCredential: [],
    } satisfies RestoreResult),
    saveBackupToFile: vi.fn().mockResolvedValue({ path: '/tmp/backup.json' }),
    ...overrides,
  } as unknown as ProfileClient
}

describe('BackupRestoreSection', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  afterEach(() => {
    cleanup()
  })

  it('renders create and restore headings', () => {
    render(() => <BackupRestoreSection profileClient={mockClient()} />)
    const headings = screen.getAllByRole('heading')
    const texts = headings.map((h) => h.textContent)
    expect(texts).toContain('Create backup')
    expect(texts).toContain('Restore backup')
  })

  it('shows plaintext warning', () => {
    render(() => <BackupRestoreSection profileClient={mockClient()} />)
    expect(screen.getByText(/plaintext warning/i)).toBeTruthy()
  })

  it('shows carries/omits in marker list', () => {
    render(() => <BackupRestoreSection profileClient={mockClient()} />)
    expect(screen.getByText(/settings overrides.*ssh connections/i)).toBeTruthy()
    expect(screen.getByText(/credential records.*УЗ/i)).toBeTruthy()
  })

  it('create button is enabled initially', () => {
    render(() => <BackupRestoreSection profileClient={mockClient()} />)
    const btn = screen.getByRole('button', { name: 'Create backup' })
    expect(btn).toBeTruthy()
    expect((btn as HTMLButtonElement).disabled).toBe(false)
  })

  it('calls createBackup on button click', async () => {
    const client = mockClient()
    render(() => <BackupRestoreSection profileClient={client} />)
    fireEvent.click(screen.getByRole('button', { name: 'Create backup' }))
    await waitFor(() => {
      expect(client.createBackup).toHaveBeenCalled()
    })
  })

  it('disables create button while creating', async () => {
    const client = mockClient({
      createBackup: vi.fn().mockImplementation(() => new Promise(() => {})),
    })
    render(() => <BackupRestoreSection profileClient={client} />)
    fireEvent.click(screen.getByRole('button', { name: 'Create backup' }))
    await waitFor(() => {
      expect(
        (screen.getByRole('button', { name: 'Creating…' }) as HTMLButtonElement).disabled,
      ).toBe(true)
    })
  })

  it('shows file size limit in restore section', () => {
    render(() => <BackupRestoreSection profileClient={mockClient()} />)
    expect(screen.getByText(new RegExp(`${MAX_BACKUP_BYTES / 1024 / 1024}`))).toBeTruthy()
  })

  it('renders file input for restore', () => {
    render(() => <BackupRestoreSection profileClient={mockClient()} />)
    expect(screen.getByText('Choose backup file…')).toBeTruthy()
  })

  it('restore merge/restore buttons hidden without preview', () => {
    render(() => <BackupRestoreSection profileClient={mockClient()} />)
    expect(screen.queryByText('Merge backup')).toBeNull()
    expect(screen.queryByText('Replace configuration')).toBeNull()
  })
})
