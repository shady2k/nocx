import { describe, expect, it } from 'vitest'

import { updateFailureMessage } from './update-failure'

/**
 * A reconcile refusal caused by debris (nocx-vh2hr.1.14) used to reach the
 * person as "The update could not finish", which is true of nothing and names
 * nothing: retrying it can never succeed. The backend now sends the blocking
 * path; the toast names it and says which step clears it, and it still says
 * only what the backend's own stable words allow.
 */
describe('updateFailureMessage on a reconcile refusal', () => {
  it('names the blocking path and what to do with it', () => {
    const error = new Error(
      "update apply: reconcile before apply: reconcile: the backup at /Applications/.nocx-backup.app is not nocx's own bundle, and this update will not delete another application's copy — move or remove it, then try again",
    )

    expect(updateFailureMessage(error)).toBe(
      'The update is blocked by /Applications/.nocx-backup.app, which is not nocx’s own. Move or remove it, then try again.',
    )
  })

  it('names the swap path the same way', () => {
    const error = new Error(
      "reconcile: the swap at /Applications/.nocx-swap.app is not nocx's own bundle, and this update will not delete another application's copy — move or remove it, then try again",
    )

    expect(updateFailureMessage(error)).toBe(
      'The update is blocked by /Applications/.nocx-swap.app, which is not nocx’s own. Move or remove it, then try again.',
    )
  })

  it('still hides anything the backend said beyond the managed path', () => {
    const error = new Error(
      "reconcile: the backup at /Applications/.nocx-backup.app is not nocx's own bundle — move or remove it, then try again: read /private/var Errno 13",
    )

    expect(updateFailureMessage(error)).toBe(
      'The update is blocked by /Applications/.nocx-backup.app, which is not nocx’s own. Move or remove it, then try again.',
    )
  })

  it('falls back to the generic sentence when no managed path is named', () => {
    const error = new Error("reconcile: the backup at elsewhere is not nocx's own bundle")

    expect(updateFailureMessage(error)).toBe(
      'The update could not finish. Try again, or install the latest release manually.',
    )
  })
})
