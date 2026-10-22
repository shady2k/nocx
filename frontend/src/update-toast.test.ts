// @vitest-environment jsdom
/**
 * update-toast — the actionable announcement of a newer build
 * (nocx-1ebrs.54).
 *
 * The check finds a newer build; this module is the half a person answers.
 * It asserts through the kit's own toast queue — the same `showToast` every
 * other surface uses — and through the recorded actions, so what is tested
 * is what a person sees and presses, not an internal state field.
 *
 * The rules under test are the bead's own: the toast appears once per
 * version, Later dismisses only that version until the next app start, a
 * newer version is announced even after an older one was dismissed, Install
 * now uses the existing ApplyUpdate path and reports downloading then
 * installed, and a failure is kept with its cause. Restart now is offered
 * only where a restart seam exists — the coordinator-survival restart is
 * nocx-1ebrs.53's outcome, and this side links to it rather than inventing
 * it.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { createUpdateToast } from './update-toast'
import { toasts, clearToasts } from './ui/toast'

const NOTES = 'https://example.com/releases/0.7.0'

function actionsOf(i: number): { label: string; onClick: () => void }[] {
  return toasts()[i]?.actions ?? []
}

function action(label: string): { label: string; onClick: () => void } {
  const found = toasts()
    .flatMap((t) => t.actions ?? [])
    .find((a) => a.label === label)
  if (!found) throw new Error(`no action labelled ${label}; queue: ${JSON.stringify(toasts())}`)
  return found
}

beforeEach(() => {
  clearToasts()
})

afterEach(() => {
  clearToasts()
})

describe('createUpdateToast', () => {
  it('announces the newer build as an actionable toast, kept up until answered', () => {
    const toast = createUpdateToast({ apply: vi.fn() })
    toast.showAvailable('0.7.0', NOTES)

    expect(toasts()).toHaveLength(1)
    const [announced] = toasts()
    expect(announced.message).toBe('nocx 0.7.0 is available')
    expect(announced.link).toEqual({ label: 'Release notes', href: NOTES })
    expect(actionsOf(0).map((a) => a.label)).toEqual(['Install now', 'Later'])
    // It holds actions a person may press at any moment, so it may not
    // dismiss itself: a toast that vanished with its Install button was
    // never an offer.
    expect(announced.duration).toBe(0)
  })

  it('does not stack a second announcement for the version it already announced', () => {
    const toast = createUpdateToast({ apply: vi.fn() })
    toast.showAvailable('0.7.0', NOTES)
    toast.showAvailable('0.7.0', NOTES)
    expect(toasts()).toHaveLength(1)
  })

  it('Later dismisses only this version, until the next app start', () => {
    const toast = createUpdateToast({ apply: vi.fn() })
    toast.showAvailable('0.7.0', NOTES)
    action('Later').onClick()
    // The kit's own test asserts that a pressed action closes the toast; the
    // queue here stands in for the DOM, so clear it the way the kit did.
    clearToasts()

    // The 24-hour recheck in the same run: the same version stays quiet.
    toast.showAvailable('0.7.0', NOTES)
    expect(toasts()).toHaveLength(0)

    // A newer version is announced even after the older one was dismissed.
    toast.showAvailable('0.7.1', NOTES)
    expect(toasts()).toHaveLength(1)
    expect(toasts()[0].message).toBe('nocx 0.7.1 is available')
  })

  it('Install now uses the ApplyUpdate path and reports downloading, then installed', async () => {
    const apply = vi.fn<() => Promise<void>>().mockResolvedValue(undefined)
    const toast = createUpdateToast({ apply })
    toast.showAvailable('0.7.0', NOTES)
    action('Install now').onClick()

    expect(apply).toHaveBeenCalledTimes(1)
    expect(toasts().map((t) => t.message)).toEqual(['Downloading nocx 0.7.0…'])

    await vi.waitFor(() =>
      expect(toasts().map((t) => t.message)).toEqual(['nocx 0.7.0 installed — restart to apply']),
    )
    expect(toasts()[0].level).toBe('success')
  })

  it('offers Restart now through the seam, and activating it reaches the seam', async () => {
    const restart = vi.fn()
    const toast = createUpdateToast({ apply: vi.fn().mockResolvedValue(undefined), restart })
    toast.showAvailable('0.7.0', NOTES)
    action('Install now').onClick()

    await vi.waitFor(() => expect(toasts()[0].message).toContain('installed'))
    expect(toasts()[0].actions?.map((a) => a.label)).toEqual(['Restart now'])
    toasts()[0].actions![0].onClick()
    expect(restart).toHaveBeenCalledTimes(1)
  })

  it('offers no dead Restart now while no restart seam exists (nocx-1ebrs.53)', async () => {
    const toast = createUpdateToast({ apply: vi.fn().mockResolvedValue(undefined) })
    toast.showAvailable('0.7.0', NOTES)
    action('Install now').onClick()

    await vi.waitFor(() => expect(toasts()[0].message).toContain('installed'))
    expect(toasts()[0].actions).toBeUndefined()
  })

  it('keeps an install failure with its cause, and does not claim it installed', async () => {
    const apply = vi.fn<() => Promise<void>>().mockRejectedValue(new Error('disk full'))
    const toast = createUpdateToast({ apply })
    toast.showAvailable('0.7.0', NOTES)
    action('Install now').onClick()

    await vi.waitFor(() => expect(toasts()[0].message).toBe('Update failed: disk full'))
    expect(toasts()[0].level).toBe('danger')
    // A failure stays up until answered — the rule every danger toast
    // already follows.
    expect(toasts()[0].duration).toBe(0)
  })

  it('does not re-announce a version while its install is already in flight', async () => {
    let release: (() => void) | null = null
    const apply = () =>
      new Promise<void>((resolve) => {
        release = resolve
      })
    const toast = createUpdateToast({ apply })
    toast.showAvailable('0.7.0', NOTES)
    action('Install now').onClick()

    // The interval's recheck lands mid-download.
    toast.showAvailable('0.7.0', NOTES)
    expect(toasts().map((t) => t.message)).toEqual(['Downloading nocx 0.7.0…'])

    release!()
    await vi.waitFor(() => expect(toasts()[0].message).toContain('installed'))
  })
})
