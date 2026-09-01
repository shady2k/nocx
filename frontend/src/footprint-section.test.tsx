// @vitest-environment jsdom
/**
 * User-path tests for the remote footprint surface (nocx-mlm7 P10): mounted
 * through the REAL ConnectionsView, the page a person reaches, with a mocked
 * FootprintClient. What a user can do: see what nocx wrote on which host and
 * where, remove it from a host a saved connection reaches, and read plainly
 * that a host without one must be removed by hand (~/.nocx).
 */
import { describe, it, expect, vi, afterEach } from 'vitest'
import { cleanup, render, fireEvent } from '@solidjs/testing-library'
import { ConnectionsView } from './connections'
import { ProfileClient } from './profiles'
import { Dispatcher } from './dispatcher'
import { fixedEndpoint } from './endpoint'
import { FootprintClient } from './footprint-client'
import type { ShellFootprintStatusResult } from './generated/shell.footprint.status'
import { clearToasts, toasts } from './ui'

function mockProfileClient(): ProfileClient {
  const pc = new ProfileClient(new Dispatcher(fixedEndpoint(9876)))
  vi.spyOn(pc, 'listProfiles').mockResolvedValue([])
  vi.spyOn(pc, 'listGroups').mockResolvedValue([])
  vi.spyOn(pc, 'sessionStatus').mockResolvedValue({ statuses: {} })
  vi.spyOn(pc, 'loadEffective').mockResolvedValue({ profiles: [] })
  vi.spyOn(pc, 'connectionTest').mockResolvedValue({ outcome: 'accepted' } as never)
  vi.spyOn(pc, 'trustHostKey').mockResolvedValue({ fingerprint: 'SHA256:abc' })
  return pc
}

const INSTALLED: ShellFootprintStatusResult = {
  destinations: [
    {
      identity: 'pi@192.168.0.93:22',
      generation: 'v10',
      path: '~/.nocx',
      protocolVersion: '1',
      scriptVersion: '0.6.0',
      lastObservedAt: '2026-08-05T10:00:00Z',
      removableProfileId: 'ssh:p1',
    },
    {
      identity: 'root@10.0.0.7:22',
      generation: 'v9',
      path: '~/.nocx',
      protocolVersion: '1',
      scriptVersion: '0.5.2',
      lastObservedAt: '2026-07-30T08:00:00Z',
      removableProfileId: null,
    },
  ],
}

const WITH_HELPER: ShellFootprintStatusResult = {
  destinations: [],
  helpers: [
    {
      identity: 'u@db01:22',
      fingerprint: 'SHA256:deadbeef',
      path: '~/.nocx/helper/1-linux-amd64-abc/',
      hash: 'abcdef0123456789',
      installedAt: '2026-08-10T09:00:00Z',
      removableProfileId: 'ssh:p1',
    },
  ],
}

const WITH_HELPER_NO_SAVED_CONNECTION: ShellFootprintStatusResult = {
  destinations: [],
  helpers: [
    {
      identity: 'root@10.0.0.7:22',
      fingerprint: 'SHA256:deadbeef',
      path: '~/.nocx/helper/1-linux-amd64-abc/',
      hash: 'abcdef0123456789',
      installedAt: '2026-08-10T09:00:00Z',
      removableProfileId: null,
    },
  ],
}

function mountWithFootprint(status: ShellFootprintStatusResult) {
  const client = mockProfileClient()
  const footprintClient = new FootprintClient(new Dispatcher(fixedEndpoint(9876)))
  const statusSpy = vi.spyOn(footprintClient, 'status').mockResolvedValue(status)
  const uninstallSpy = vi.spyOn(footprintClient, 'uninstall').mockResolvedValue({
    removed: ['integration/v10/nocx.zsh', 'manifest.json'],
    conflicts: ['integration/v10/nocx.bash'],
  })
  const helperUninstallSpy = vi
    .spyOn(footprintClient, 'helperUninstall')
    .mockResolvedValue({ removed: true })
  const container = document.body.appendChild(document.createElement('div'))
  render(
    () => (
      <ConnectionsView
        client={client}
        footprintClient={footprintClient}
        onConnect={() => undefined}
        onNavigateToSecrets={() => undefined}
      />
    ),
    { container },
  )
  return { container, statusSpy, uninstallSpy, helperUninstallSpy }
}

afterEach(() => {
  clearToasts()
  vi.clearAllMocks()
  cleanup()
})

describe('remote footprint', () => {
  it('names what nocx wrote and where, and offers uninstall only where a saved connection exists', async () => {
    const { container } = mountWithFootprint(INSTALLED)

    await vi.waitFor(() => {
      expect(container.textContent).toContain('pi@192.168.0.93:22')
    })

    const text = container.textContent ?? ''
    // The installed destination shows generation, path, versions and when it
    // was last SEEN — the observation label, never a claim of what is there now.
    expect(text).toContain('v10')
    expect(text).toContain('~/.nocx')
    expect(text).toContain('protocol 1')
    expect(text).toContain('scripts 0.6.0')
    expect(text).toContain('last seen')

    // One Uninstall button: for the destination a saved connection resolves
    // to. The direct-host destination has no button — absence of the saved
    // connection IS the explanation.
    const uninstallButtons = [...container.querySelectorAll('button')].filter((b) =>
      b.textContent?.includes('Uninstall'),
    )
    expect(uninstallButtons.length).toBe(1)
    expect(text).toContain('Removal needs a saved connection')
    expect(text).toContain('root@10.0.0.7:22')
  })

  it('removes through the saved connection after confirmation and reports removed and conflicts', async () => {
    const { container, statusSpy, uninstallSpy } = mountWithFootprint(INSTALLED)

    await vi.waitFor(() => {
      expect(container.textContent).toContain('pi@192.168.0.93:22')
    })

    // The user asks to remove the host's integration.
    const uninstallButton = [...container.querySelectorAll('button')].find((b) =>
      b.textContent?.includes('Uninstall'),
    )!
    fireEvent.click(uninstallButton)

    // The confirmation dialog names the destination and the boundary. The
    // page renders its other dialogs (quick connect, import) closed but
    // present, so the confirm is found by its message, not by being first.
    await vi.waitFor(() => {
      expect(
        [...document.querySelectorAll('dialog')].some((d) =>
          d.textContent?.includes('Only manifest-owned, unmodified files are removed'),
        ),
      ).toBe(true)
    })
    const dialog = [...document.querySelectorAll('dialog')].find((d) =>
      d.textContent?.includes('Only manifest-owned, unmodified files are removed'),
    )!
    expect(dialog.textContent).toContain('pi@192.168.0.93:22')

    // Confirming drives the uninstall through the saved connection.
    const confirm = [...dialog.querySelectorAll('button')].find((b) =>
      b.textContent?.includes('Uninstall'),
    )!
    fireEvent.click(confirm)

    await vi.waitFor(() => {
      expect(uninstallSpy).toHaveBeenCalledWith('ssh:p1')
    })

    // The outcome is reported: removed AND conflicts (a conflict is
    // information the user acts on, never swallowed), then the surface
    // refreshes to what remains.
    await vi.waitFor(() => {
      expect(
        toasts().some((t) => t.message.includes('Removed 2 file(s) from pi@192.168.0.93:22')),
      ).toBe(true)
    })
    expect(
      toasts().some((t) => t.level === 'warning' && t.message.includes('1 modified file(s) kept')),
    ).toBe(true)
    await vi.waitFor(() => {
      expect(statusSpy).toHaveBeenCalledTimes(2)
    })
  })

  it('says so when nothing has ever been installed', async () => {
    const { container } = mountWithFootprint({ destinations: [] })

    await vi.waitFor(() => {
      expect(container.textContent).toContain('Nothing installed')
    })
    expect(container.textContent).toContain('nocx has not left shell integration on any host')
  })

  it('lists the installed helper and offers to remove it where a saved connection exists', async () => {
    const { container } = mountWithFootprint(WITH_HELPER)

    await vi.waitFor(() => {
      expect(container.textContent).toContain('u@db01:22')
    })
    const text = container.textContent ?? ''
    expect(text).toContain('Remote helper')
    expect(text).toContain('~/.nocx/helper/')
    expect(text).toContain('hash abcdef012345')
    // A saved connection resolves to this machine, so the row offers the
    // uninstall action — an action that is valid from the state the user
    // is in (AGENTS.md rule 1).
    const uninstallButtons = [...container.querySelectorAll('button')].filter((b) =>
      b.textContent?.includes('Uninstall'),
    )
    expect(uninstallButtons.length).toBe(1)
  })

  it('removes the helper through the saved connection after confirmation, stating what stays', async () => {
    const { container, statusSpy, helperUninstallSpy } = mountWithFootprint(WITH_HELPER)

    await vi.waitFor(() => {
      expect(container.textContent).toContain('u@db01:22')
    })

    // The next status read (after the uninstall) answers an empty helper
    // list — the observation was forgotten on the backend.
    statusSpy.mockResolvedValueOnce({ destinations: [], helpers: [] })

    const uninstallButton = [...container.querySelectorAll('button')].find((b) =>
      b.textContent?.includes('Uninstall'),
    )!
    fireEvent.click(uninstallButton)

    // The confirmation names the boundary AND the consent decision: the
    // whole helper tree is removed, every live helper-hosted session ends,
    // and consent for this machine is revoked.
    await vi.waitFor(() => {
      expect(
        [...document.querySelectorAll('dialog')].some((d) =>
          d.textContent?.includes('consent for this machine is revoked'),
        ),
      ).toBe(true)
    })
    const dialog = [...document.querySelectorAll('dialog')].find((d) =>
      d.textContent?.includes('consent for this machine is revoked'),
    )!
    expect(dialog.textContent).toContain('u@db01:22')

    const confirm = [...dialog.querySelectorAll('button')].find((b) =>
      b.textContent?.includes('Uninstall'),
    )!
    fireEvent.click(confirm)

    await vi.waitFor(() => {
      expect(helperUninstallSpy).toHaveBeenCalledWith(
        'ssh:p1',
        'SHA256:deadbeef',
        '~/.nocx/helper/1-linux-amd64-abc/',
      )
    })
    await vi.waitFor(() => {
      expect(toasts().some((t) => t.message.includes('Removed the helper from u@db01:22'))).toBe(
        true,
      )
    })
    // The surface refreshes to what remains — the row is gone from the
    // product, which is the acceptance criterion: a user can remove the
    // helper from a host, and the screen stops advertising it.
    await vi.waitFor(() => {
      expect(container.textContent).not.toContain('u@db01:22')
    })
    expect(statusSpy).toHaveBeenCalledTimes(2)
  })

  it('says plainly when no saved connection can remove the helper', async () => {
    const { container } = mountWithFootprint(WITH_HELPER_NO_SAVED_CONNECTION)

    await vi.waitFor(() => {
      expect(container.textContent).toContain('root@10.0.0.7:22')
    })
    const text = container.textContent ?? ''
    expect(text).toContain('Removal and consent revocation need a saved connection')
    const uninstallButtons = [...container.querySelectorAll('button')].filter((b) =>
      b.textContent?.includes('Uninstall'),
    )
    expect(uninstallButtons.length).toBe(0)
  })
})
