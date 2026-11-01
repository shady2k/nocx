// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@solidjs/testing-library'
import { createUpdateController } from './update-controller'
import { ToastHost } from './ui/toast'
import { mountSidebar } from './sidebar'

const { logWarn } = vi.hoisted(() => ({ logWarn: vi.fn() }))
vi.mock('./log', () => ({ log: { warn: logWarn } }))

const NOTES = 'https://example.com/releases/2.0.0'
afterEach(() => {
  cleanup()
  document.body.replaceChildren()
})

function mountIndicator(controller: ReturnType<typeof createUpdateController>) {
  const bar = document.createElement('div')
  const panel = document.createElement('div')
  document.body.append(bar, panel)
  const handle = mountSidebar(
    bar,
    panel,
    [],
    [{ id: 'settings', title: 'Settings', icon: () => <svg />, onActivate: vi.fn() }],
    null,
    undefined,
    undefined,
    undefined,
    undefined,
    {
      title: 'Update available',
      icon: () => <svg />,
      level: () => 'neutral',
      available: () => controller.state().kind !== 'hidden',
      onActivate: controller.showOffer,
    },
  )
  return { bar, panel, handle }
}

describe('shared update offer', () => {
  it.each(['throw', 'string'])('keeps held update retryable after a %s refusal', async (shape) => {
    const raw = 'stop failed /private/state Errno 5'
    const finish = vi
      .fn<() => Promise<void>>()
      .mockImplementationOnce(() => {
        if (shape === 'throw') throw new Error(raw)
        // Deliberately exercise a non-Error rejection from a native boundary.
        // eslint-disable-next-line @typescript-eslint/prefer-promise-reject-errors
        return Promise.reject(raw)
      })
      .mockResolvedValue(undefined)
    const controller = createUpdateController({ finish })
    render(() => <ToastHost />)
    mountIndicator(controller)
    controller.showHeldByCoordinator('2.0.0')
    fireEvent.click(screen.getByRole('button', { name: 'Update available' }))
    fireEvent.click(screen.getByRole('button', { name: 'Close nocx' }))
    await vi.waitFor(() =>
      expect(document.querySelector('.ui-toast[data-level="danger"]')).not.toBeNull(),
    )
    expect(screen.getByRole('status').textContent).not.toMatch(/\/|Errno/)
    expect(logWarn).toHaveBeenCalledWith('update action failed', { error: raw })
    controller.showAvailable('2.0.1', NOTES)
    fireEvent.click(screen.getByRole('button', { name: 'Update available' }))
    expect(screen.getByRole('status').textContent).toContain('2.0.0 installed')
    fireEvent.click(screen.getByRole('button', { name: 'Close nocx' }))
    await vi.waitFor(() => expect(finish).toHaveBeenCalledTimes(2))
  })

  it.each(['held-first', 'check-first'])(
    'reads held startup through the startup seam: %s',
    async (order) => {
      const controller = createUpdateController({ finish: vi.fn() })
      render(() => <ToastHost />)
      mountIndicator(controller)
      if (order === 'check-first') controller.showAvailable('2.0.1', NOTES)
      await controller.restore({
        getState: () => Promise.resolve({ HeldByOlderCoordinator: true, Version: '2.0.0' }),
      })
      if (order === 'held-first') controller.showAvailable('2.0.1', NOTES)
      fireEvent.click(screen.getByRole('button', { name: 'Update available' }))
      expect(screen.getByRole('status').textContent).toContain('2.0.0 installed')
      expect(screen.getByRole('status').textContent).toContain('closes open sessions')
      expect(screen.getAllByRole('button', { name: 'Close nocx' })).toHaveLength(1)
      expect(screen.queryByRole('button', { name: /^Install/ })).toBeNull()
    },
  )

  it('leaves ordinary pending startup quiet and logs a failed state read', async () => {
    const controller = createUpdateController()
    render(() => <ToastHost />)
    mountIndicator(controller)
    await controller.restore({
      getState: () => Promise.resolve({ HeldByOlderCoordinator: false, Version: '2.0.0' }),
    })
    expect(screen.queryByRole('button', { name: 'Update available' })).toBeNull()
    const error = new Error('state read failed /private/state')
    await controller.restore({ getState: () => Promise.reject(error) })
    expect(logWarn).toHaveBeenCalledWith('reading update state failed', { error: error.message })
    expect(screen.queryByRole('button', { name: 'Update available' })).toBeNull()
  })

  it('keeps a held coordinator visible across checks and finishes only once, with the session warning', async () => {
    let resolve!: () => void
    const finish = vi.fn(
      () =>
        new Promise<void>((r) => {
          resolve = r
        }),
    )
    const apply = vi.fn()
    const restart = vi.fn()
    const controller = createUpdateController({ apply, restart, finish })
    render(() => <ToastHost />)
    const first = mountIndicator(controller)
    controller.showHeldByCoordinator('2.0.0')
    controller.showAvailable('2.0.1', NOTES)
    first.handle.destroy()
    first.bar.remove()
    first.panel.remove()
    mountIndicator(controller)
    fireEvent.click(screen.getByRole('button', { name: 'Update available' }))
    expect(screen.getByRole('status').textContent).toContain('2.0.0 installed')
    expect(screen.getByRole('status').textContent).toContain('closes open sessions')
    expect(screen.queryByRole('button', { name: /^Install/ })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Close nocx' }))
    fireEvent.click(screen.getByRole('button', { name: 'Update available' }))
    expect(screen.queryByRole('button', { name: 'Close nocx' })).toBeNull()
    await vi.waitFor(() => expect(finish).toHaveBeenCalledTimes(1))
    expect(apply).not.toHaveBeenCalled()
    expect(restart).not.toHaveBeenCalled()
    resolve()
  })

  it('explains a refused held finish without losing the installed version or retry', async () => {
    const error = new Error('stop failed /private/state Errno 5')
    const finish = vi.fn().mockRejectedValueOnce(error).mockResolvedValue(undefined)
    const controller = createUpdateController({ finish })
    render(() => <ToastHost />)
    mountIndicator(controller)
    controller.showAvailable('2.0.1', NOTES)
    controller.showHeldByCoordinator('2.0.0')
    fireEvent.click(screen.getByRole('button', { name: 'Update available' }))
    fireEvent.click(screen.getByRole('button', { name: 'Close nocx' }))
    await vi.waitFor(() =>
      expect(document.querySelector('.ui-toast[data-level="danger"]')).not.toBeNull(),
    )
    expect(screen.getByRole('status').textContent).not.toMatch(/\/|Errno/)
    expect(logWarn).toHaveBeenCalledWith('update action failed', { error: error.message })
    fireEvent.click(screen.getByRole('button', { name: 'Update available' }))
    expect(screen.getByRole('status').textContent).toContain('2.0.0 installed')
    fireEvent.click(screen.getByRole('button', { name: 'Close nocx' }))
    await vi.waitFor(() => expect(finish).toHaveBeenCalledTimes(2))
  })

  it.each([
    [
      'announcement',
      'update apply: preflight refused: cannot install the update; allow writes to Applications and try again: /private/tmp/nocx Errno 13',
      'cannot install the update; allow writes to Applications and try again',
    ],
    [
      'announcement',
      'update apply: preflight refused: cannot find your home folder — check your account settings and try again: /private/tmp/nocx Errno 13',
      'cannot find your home folder — check your account settings and try again',
    ],
    [
      'announcement',
      'update apply: preflight refused: the Applications folder is not writable — allow writes to Applications and try again: /private/tmp/nocx Errno 13',
      'the Applications folder is not writable — allow writes to Applications and try again',
    ],
    [
      'announcement',
      'update apply: preflight refused: the AppImage directory is not writable — move the AppImage to a directory you can write to, launch it there, and try again: /private/tmp/nocx Errno 13',
      'the AppImage directory is not writable — move the AppImage to a directory you can write to, launch it there, and try again',
    ],
    [
      'announcement',
      'update apply: preflight refused: this is a development build; self-update is not available: /private/tmp/nocx Errno 13',
      'this is a development build; self-update is not available',
    ],
    [
      'announcement',
      'update apply: preflight refused: this is not a distributed AppImage build: /private/tmp/nocx Errno 13',
      'this is not a distributed AppImage build',
    ],
    [
      'announcement',
      'update apply: preflight refused: rollback: this was a first installation, with no previous target to restore: /private/tmp/nocx Errno 13',
      'rollback: this was a first installation, with no previous target to restore',
    ],
    [
      'announcement',
      'update apply: preflight refused: the update install target is unavailable: /private/tmp/nocx Errno 13',
      'the update install target is unavailable',
    ],
    [
      'announcement',
      'update apply: preflight refused: this window cannot quit the application: /private/tmp/nocx Errno 13',
      'this window cannot quit the application',
    ],
    [
      'announcement',
      'update apply: preflight refused: state directory is not configured: /private/tmp/nocx Errno 13',
      'state directory is not configured',
    ],
    [
      'announcement',
      'download /tmp/update Errno 5',
      'The update could not be downloaded. Check your connection and try again.',
    ],
    [
      'announcement',
      'verify signature /tmp/nocx',
      'The update could not be verified. Check for updates and download it again.',
    ],
    [
      'announcement',
      'extract /tmp/nocx Errno 28',
      'The update could not be unpacked. Check free space and try again.',
    ],
    [
      'announcement',
      'exchange /Applications/nocx Errno 13',
      'The update could not replace the app. Allow writes to the app’s folder and try again.',
    ],
    [
      'announcement',
      'not installed at /private/app',
      'This copy of nocx cannot be updated here. Install the latest release in a folder you can write to, then try again.',
    ],
    [
      'announcement',
      'unknown secret /private/log Errno 7',
      'The update could not finish. Try again, or install the latest release manually.',
    ],
    [
      'indicator',
      'update apply: preflight refused: cannot install the update; allow writes to Applications and try again: /private/tmp/nocx Errno 13',
      'cannot install the update; allow writes to Applications and try again',
    ],
    [
      'indicator',
      'update apply: preflight refused: cannot find your home folder — check your account settings and try again: /private/tmp/nocx Errno 13',
      'cannot find your home folder — check your account settings and try again',
    ],
    [
      'indicator',
      'update apply: preflight refused: the Applications folder is not writable — allow writes to Applications and try again: /private/tmp/nocx Errno 13',
      'the Applications folder is not writable — allow writes to Applications and try again',
    ],
    [
      'indicator',
      'update apply: preflight refused: the AppImage directory is not writable — move the AppImage to a directory you can write to, launch it there, and try again: /private/tmp/nocx Errno 13',
      'the AppImage directory is not writable — move the AppImage to a directory you can write to, launch it there, and try again',
    ],
    [
      'indicator',
      'update apply: preflight refused: this is a development build; self-update is not available: /private/tmp/nocx Errno 13',
      'this is a development build; self-update is not available',
    ],
    [
      'indicator',
      'update apply: preflight refused: this is not a distributed AppImage build: /private/tmp/nocx Errno 13',
      'this is not a distributed AppImage build',
    ],
    [
      'indicator',
      'update apply: preflight refused: rollback: this was a first installation, with no previous target to restore: /private/tmp/nocx Errno 13',
      'rollback: this was a first installation, with no previous target to restore',
    ],
    [
      'indicator',
      'update apply: preflight refused: the update install target is unavailable: /private/tmp/nocx Errno 13',
      'the update install target is unavailable',
    ],
    [
      'indicator',
      'update apply: preflight refused: this window cannot quit the application: /private/tmp/nocx Errno 13',
      'this window cannot quit the application',
    ],
    [
      'indicator',
      'update apply: preflight refused: state directory is not configured: /private/tmp/nocx Errno 13',
      'state directory is not configured',
    ],
    [
      'indicator',
      'download /tmp/update Errno 5',
      'The update could not be downloaded. Check your connection and try again.',
    ],
    [
      'indicator',
      'verify signature /tmp/nocx',
      'The update could not be verified. Check for updates and download it again.',
    ],
    [
      'indicator',
      'extract /tmp/nocx Errno 28',
      'The update could not be unpacked. Check free space and try again.',
    ],
    [
      'indicator',
      'exchange /Applications/nocx Errno 13',
      'The update could not replace the app. Allow writes to the app’s folder and try again.',
    ],
    [
      'indicator',
      'not installed at /private/app',
      'This copy of nocx cannot be updated here. Install the latest release in a folder you can write to, then try again.',
    ],
    [
      'indicator',
      'unknown secret /private/log Errno 7',
      'The update could not finish. Try again, or install the latest release manually.',
    ],
  ])(
    'keeps %s install failure detail in the log and says safe words for %s',
    async (route, raw, expected) => {
      const error = new Error(raw)
      const apply = vi.fn().mockRejectedValueOnce(error).mockResolvedValue(undefined)
      const controller = createUpdateController({ apply })
      render(() => <ToastHost />)
      mountIndicator(controller)
      controller.showAvailable('2.0.0', NOTES)
      if (route === 'indicator')
        fireEvent.click(screen.getByRole('button', { name: 'Update available' }))
      fireEvent.click(
        screen.getByRole('button', { name: route === 'indicator' ? /^Install$/ : 'Install now' }),
      )
      await vi.waitFor(() => expect(screen.getByRole('status').textContent).toContain(expected))
      expect(screen.getByRole('status').textContent).not.toMatch(/\/|Errno/)
      expect(document.querySelector('.ui-toast[data-level="danger"]')).not.toBeNull()
      expect(logWarn).toHaveBeenCalledWith('update action failed', { error: raw })
      fireEvent.click(screen.getByRole('button', { name: 'Update available' }))
      fireEvent.click(screen.getByRole('button', { name: /^Install$/ }))
      await vi.waitFor(() =>
        expect(screen.getByRole('status').textContent).toContain('2.0.0 installed'),
      )
      expect(apply).toHaveBeenCalledTimes(2)
    },
  )

  it.each(['announcement', 'indicator'])(
    'opens %s release notes through the synchronous opener and explains refusal',
    async (route) => {
      const open = vi.fn<(url: string) => Promise<void>>().mockResolvedValue(undefined)
      const controller = createUpdateController({ apply: vi.fn() })
      render(() => <ToastHost openUrl={open} />)
      mountIndicator(controller)
      controller.showAvailable('2.0.0', NOTES)
      if (route === 'indicator')
        fireEvent.click(screen.getByRole('button', { name: 'Update available' }))
      const link = screen.getByRole('link', { name: 'Release notes' })
      fireEvent.click(link)
      expect(open).toHaveBeenCalledWith(NOTES)
      expect(screen.getByRole('status').textContent).toContain('2.0.0')
      open.mockRejectedValueOnce(new Error('browser refused /private/path Errno 13'))
      fireEvent.click(link)
      await vi.waitFor(() =>
        expect(screen.getByRole('status').textContent).toContain(
          'The browser could not be opened.',
        ),
      )
      expect(screen.getByRole('status').textContent).not.toContain('/private/path')
      expect(document.querySelector('.ui-toast[data-level="danger"]')).not.toBeNull()
    },
  )

  it('announces once per version even after Later or dismiss, while the indicator stays findable', () => {
    const controller = createUpdateController({ apply: vi.fn() })
    render(() => <ToastHost />)
    mountIndicator(controller)
    controller.showAvailable('2.0.0', NOTES)
    expect(screen.getAllByRole('button', { name: 'Install now' })).toHaveLength(1)
    expect(document.querySelectorAll('.ui-toast')).toHaveLength(1)
    fireEvent.click(screen.getByRole('button', { name: 'Later' }))
    controller.showAvailable('2.0.0', NOTES)
    expect(document.querySelectorAll('.ui-toast')).toHaveLength(0)
    fireEvent.click(screen.getByRole('button', { name: 'Update available' }))
    expect(screen.getAllByRole('button', { name: /^Install$/ })).toHaveLength(1)
    controller.showAvailable('2.0.1', NOTES)
    expect(document.querySelectorAll('.ui-toast')).toHaveLength(1)
    expect(screen.getByRole('status').textContent).toContain('2.0.1')
    fireEvent.click(screen.getByRole('button', { name: 'Dismiss notification' }))
    controller.showAvailable('2.0.1', NOTES)
    controller.showAvailable('2.0.0', NOTES)
    expect(document.querySelectorAll('.ui-toast')).toHaveLength(0)
  })

  it('the announcement uses the same single-flight apply and keeps replacement progress after the kit action dismisses', async () => {
    let resolve!: () => void
    const apply = vi.fn(
      () =>
        new Promise<void>((r) => {
          resolve = r
        }),
    )
    const restart = vi.fn()
    const controller = createUpdateController({ apply, restart })
    render(() => <ToastHost />)
    mountIndicator(controller)
    controller.showAvailable('2.0.0', NOTES)
    fireEvent.click(screen.getByRole('button', { name: 'Install now' }))
    expect(screen.getByRole('status').textContent).toContain('Downloading nocx 2.0.0')
    fireEvent.click(screen.getByRole('button', { name: 'Update available' }))
    await controller.apply()
    controller.showAvailable('2.0.1', NOTES)
    expect(apply).toHaveBeenCalledTimes(1)
    expect(document.querySelectorAll('.ui-toast')).toHaveLength(1)
    resolve()
    await vi.waitFor(() =>
      expect(screen.getByRole('status').textContent).toContain('2.0.0 installed'),
    )
    fireEvent.click(screen.getByRole('button', { name: 'Restart now' }))
    await vi.waitFor(() => expect(restart).toHaveBeenCalledTimes(1))
  })

  it('opens one offer beside Settings without taking tab space and applies once across sidebar rebuilds', async () => {
    let resolve!: () => void
    const apply = vi.fn(
      () =>
        new Promise<void>((r) => {
          resolve = r
        }),
    )
    const controller = createUpdateController({ apply })
    render(() => (
      <>
        <div id="tabbar" />
        <ToastHost />
      </>
    ))
    controller.showAvailable('2.0.0', NOTES)
    const first = mountIndicator(controller)
    fireEvent.click(screen.getByRole('button', { name: 'Update available' }))
    expect(screen.getByRole('status').textContent).toContain('2.0.0')
    expect(screen.getAllByRole('button', { name: /^Install$/ })).toHaveLength(1)
    expect(document.querySelector('#tabbar')?.textContent).toBe('')
    fireEvent.click(screen.getByRole('button', { name: /^Install$/ }))
    expect(apply).toHaveBeenCalledTimes(1)
    expect(screen.getByRole('status').textContent).toContain('Downloading')
    first.handle.destroy()
    first.bar.remove()
    first.panel.remove()
    mountIndicator(controller)
    fireEvent.click(screen.getByRole('button', { name: 'Update available' }))
    controller.showAvailable('2.0.1', NOTES)
    await controller.apply()
    expect(apply).toHaveBeenCalledTimes(1)
    expect(screen.queryByRole('button', { name: /^Install$/ })).toBeNull()
    resolve()
    await vi.waitFor(() =>
      expect(screen.getByRole('status').textContent).toContain('2.0.0 installed'),
    )
  })

  it('says a failed startup-state read out loud, not only in the log (review, .66.1.1)', async () => {
    const controller = createUpdateController()
    render(() => <ToastHost />)
    mountIndicator(controller)

    const onFailure = vi.fn()
    await controller.restore({
      getState: () => Promise.reject(new Error('socket closed')),
      onFailure,
    })

    // The controller reports; the composition root decides what a person sees
    // (the desktop window says it in a warning toast, a browser stand has no
    // updater to warn about — review, nocx-1ebrs.66.1.1).
    expect(logWarn).toHaveBeenCalledWith('reading update state failed', expect.anything())
    expect(onFailure).toHaveBeenCalledWith('socket closed')
  })
})

// ── A nightly offer names the build it would install (nocx-vh2hr.1.5) ──────
//
// The owner's report: installed 0.8.1 (commit b905772), offered a nightly of
// the same 0.8.1 built from 60caa03d — and the notice said "nocx 0.8.1 is
// available" to someone already on 0.8.1, with release notes that do not
// exist for a nightly. On the nightly channel the notice names the offered
// build, and its link is the range the backend composed, or nothing.
describe('a nightly offer names the build (nocx-vh2hr.1.5)', () => {
  // What the backend composes when the installed and offered builds are both
  // stamped: the manifest's own host and repository, one commit range.
  const FROM = 'a'.repeat(40)
  const OFFERED = '60caa03d' + 'b'.repeat(32)
  const COMPARE = `https://github.com/shady2k/nocx/compare/${FROM}...${OFFERED}`
  const NIGHTLY = {
    channel: 'nightly',
    build: { commit: OFFERED, builtAt: '2026-10-30T07:50:35Z' },
  }

  it('names the offered build beside the version it shares with the installed one', () => {
    const controller = createUpdateController({ apply: vi.fn() })
    render(() => <ToastHost />)
    mountIndicator(controller)
    controller.showAvailable('0.8.1', COMPARE, NIGHTLY)

    expect(screen.getByRole('status').textContent).toContain('nocx 0.8.1, build 60caa03')
    expect(screen.getByRole('status').textContent).toContain('is available')
    expect(screen.getByRole('link', { name: 'What changed' }).getAttribute('href')).toBe(COMPARE)
  })

  it('names the build on a nightly that also moves the version', () => {
    const controller = createUpdateController({ apply: vi.fn() })
    render(() => <ToastHost />)
    mountIndicator(controller)
    controller.showAvailable('0.8.2', COMPARE, NIGHTLY)

    expect(screen.getByRole('status').textContent).toContain('nocx 0.8.2, build 60caa03')
    expect(screen.getByRole('link', { name: 'What changed' }).getAttribute('href')).toBe(COMPARE)
  })

  it('renders no link when the backend could not compose a range', () => {
    const controller = createUpdateController({ apply: vi.fn() })
    render(() => <ToastHost />)
    mountIndicator(controller)
    controller.showAvailable('0.8.1', '', NIGHTLY)

    expect(screen.getByRole('status').textContent).toContain('build 60caa03')
    expect(screen.queryByRole('link')).toBeNull()
  })

  it('leaves the stable offer exactly as it was', () => {
    const controller = createUpdateController({ apply: vi.fn() })
    render(() => <ToastHost />)
    mountIndicator(controller)
    controller.showAvailable('2.0.1', NOTES, {
      channel: 'stable',
      build: { commit: OFFERED, builtAt: '2026-10-30T07:50:35Z' },
    })

    const message = screen.getByRole('status').textContent ?? ''
    expect(message).toContain('nocx 2.0.1 is available')
    expect(message).not.toContain('build')
    expect(screen.getByRole('link', { name: 'Release notes' }).getAttribute('href')).toBe(NOTES)
  })

  it('announces a second nightly of the same version as its own build', () => {
    const controller = createUpdateController({ apply: vi.fn() })
    render(() => <ToastHost />)
    mountIndicator(controller)
    controller.showAvailable('0.8.1', COMPARE, NIGHTLY)
    fireEvent.click(screen.getByRole('button', { name: 'Later' }))

    const later = '70caa03d' + 'c'.repeat(32)
    controller.showAvailable('0.8.1', COMPARE, {
      channel: 'nightly',
      build: { commit: later, builtAt: '2026-10-31T07:50:35Z' },
    })

    expect(document.querySelectorAll('.ui-toast')).toHaveLength(1)
    expect(screen.getByRole('status').textContent).toContain('build 70caa03')
  })
})
