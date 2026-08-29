import { test, expect } from '@playwright/test'
import { execSync } from 'node:child_process'
import { existsSync, mkdtempSync, statSync } from 'node:fs'
import { homedir, tmpdir } from 'node:os'
import { join } from 'node:path'

import { VaultBackend } from './harness'
import { readStand } from './stand'
import { assertResolvedIsolatedHome, createHomeIsolation } from './home-isolation'

/**
 * The assertion the rest of this work is for: a launched backend does not merely
 * RECEIVE an isolated home, it resolves it.
 *
 * Building an environment proves what was handed over. A binary that reads the
 * home some other way — a hard-coded path, a cached value, a library that calls
 * getpwuid instead of reading $HOME — satisfies that and still writes the
 * developer's real directory. nocx-ti8w was exactly this shape one level up:
 * correct isolation existed in this file and three specs out of twenty-five
 * used it, and every gate stayed green while a run reset the developer's theme.
 *
 * The signal is ~/.nocx, and it is chosen because it is the write that was
 * doing the damage. internal/app.Start() installs the shell integration into
 * os.UserHomeDir() on every single launch, before the socket is listening. So
 * its appearance inside the disposable home is proof the backend resolved that
 * home, and its absence from the real one is proof it did not resolve that.
 */

/** Lazily, not at module scope: the stand is started by globalSetup, which
 *  runs after Playwright has collected this file. */
const serverBin = () => readStand().server

/** mtime of the real ~/.nocx, or null when it does not exist. */
function realShellIntegrationStamp(): number | null {
  try {
    return statSync(join(homedir(), '.nocx')).mtimeMs
  } catch {
    return null
  }
}

test.describe('the e2e home boundary is obeyed, not just handed over', () => {
  // No "is the binary there" guard any more. It was asked at collection time,
  // which is before the stand exists, and it was guarding against a binary
  // nobody built — the failure mode of the old arrangement, where the runner
  // and CI each had to remember. The stand builds nocx-server on its way up, so
  // a missing binary now means the stand failed, and that fails the run rather
  // than quietly skipping this file (nocx-z9s9.7).

  test('a backend given the boundary installs into it and leaves the real home alone', async () => {
    const root = mkdtempSync(join(tmpdir(), 'nocx-e2e-live-home-'))
    const before = realShellIntegrationStamp()

    const backend = new VaultBackend(serverBin(), { root })
    try {
      await backend.start()

      // Resolved, not merely supplied.
      const installed = join(backend.isolatedHome, '.nocx')
      expect(existsSync(installed), `expected shell integration in ${installed}`).toBe(true)

      // And the module's own guard agrees about which home that is.
      const reference = createHomeIsolation({ inheritedEnv: {}, root })
      assertResolvedIsolatedHome(backend.isolatedHome, reference)

      // A tripwire, and deliberately labelled as one rather than dressed up as
      // the proof. Measured while checking this test could fail at all: running
      // a backend with NO boundary does not move this mtime, because
      // EnsureInstalled is version-gated and a reinstall of the same version is
      // a no-op. So it catches a real home that had no ~/.nocx yet, or one at a
      // different version, and it is blind to the common case.
      //
      // The assertion above is the one with teeth: on a root created by
      // mkdtemp seconds ago, ~/.nocx cannot appear inside it unless this
      // backend resolved that home and wrote there.
      expect(realShellIntegrationStamp()).toBe(before)
    } finally {
      backend.stop()
      execSync(`rm -rf ${JSON.stringify(root)}`)
    }
  })
})
