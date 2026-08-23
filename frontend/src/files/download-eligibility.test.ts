// The download rule, all four combinations. The absences are the
// security-shaped half of this feature — a Download offered where the file
// is already on the person's own disk is `Show in Finder` wearing the wrong
// name — and an untested absence is not a rule.
import { describe, expect, it } from 'vitest'

import { downloadReachesTheBytes } from './download-eligibility'

describe('where a download reaches bytes the person cannot otherwise get at', () => {
  it('refuses exactly one combination: the desktop window on a local tab', () => {
    // The file is on the machine the window is running on. `Show in Finder`
    // is the action for it, and it is in the same menu on exactly this
    // combination.
    expect(downloadReachesTheBytes({ native: true, kind: 'local' })).toBe(false)
  })

  it('allows the desktop window on an ssh tab', () => {
    expect(downloadReachesTheBytes({ native: true, kind: 'ssh' })).toBe(true)
  })

  it('allows a browser on a LOCAL tab — the row that is easy to get wrong', () => {
    // "Local" names the BACKEND's machine, not the browser's. The person is
    // sitting somewhere else, so the file is as far away as any remote one
    // and `Show in Finder` would open a window nobody is looking at.
    expect(downloadReachesTheBytes({ native: false, kind: 'local' })).toBe(true)
  })

  it('allows a browser on an ssh tab', () => {
    expect(downloadReachesTheBytes({ native: false, kind: 'ssh' })).toBe(true)
  })
})
