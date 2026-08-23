// All four combinations, because the defect this predicate replaces was one
// of them: browser + local said "no uploader here" while the drop on the
// same tab uploaded. A test that checks only the three easy answers cannot
// report that, and the absent case is the one worth guarding — a menu item
// that leaks onto the wrong tab is a promise the product cannot keep.
import { describe, expect, it } from 'vitest'

import { uploadMovesTheFile } from './upload-eligibility'

describe('uploadMovesTheFile', () => {
  it('moves a file from a browser onto the machine a local tab is on', () => {
    // The bug: a `File` is bytes on the BROWSER's machine and the tab's
    // shell is on the backend's, so there is somewhere to send it.
    expect(uploadMovesTheFile({ native: false, kind: 'local' })).toBe(true)
  })

  it('moves a file from a browser onto a remote host', () => {
    expect(uploadMovesTheFile({ native: false, kind: 'ssh' })).toBe(true)
  })

  it('moves a file from the Wails window onto a remote host', () => {
    // The ticket names a file on the backend's machine; the shell is
    // elsewhere, so the transfer is real.
    expect(uploadMovesTheFile({ native: true, kind: 'ssh' })).toBe(true)
  })

  it('moves nothing in the Wails window on a local tab', () => {
    // The one false: the file is already on the machine it would be sent
    // to. D9's other half applies — the path is inserted instead.
    expect(uploadMovesTheFile({ native: true, kind: 'local' })).toBe(false)
  })
})
