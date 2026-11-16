/**
 * e2e: A SAVED PROFILE WHOSE KEY IS IN THE VAULT OPENS A PANE OVER THE HELPER
 * (nocx-1ebrs.17), and it does so with an RSA key against a host that refuses
 * SHA-1 signatures.
 *
 * # What a person can do that they could not before
 *
 * A saved ssh connection whose private key is stored in nocx's vault opens a
 * pane — the path every existing user with a remote host takes, and the one
 * 0.6 broke while 0.4 connected.
 *
 * # The gap this closes, which is why it is a spec rather than a unit test
 *
 * The one helper-path ssh spec (ssh-helper-happy-path.spec.ts) seeds a stored
 * PASSWORD. No e2e exercised a pane whose credential is a KEY IN THE VAULT, and
 * that is precisely the configuration that shipped broken: the key was right,
 * the vault was unlocked, the helper offered it, the coordinator signed it —
 * with `ssh-rsa`, SHA-1, because the helper's reverse signer could not be asked
 * for a negotiated algorithm. The server's own journal said
 *
 *     userauth_pubkey: signature algorithm ssh-rsa not in PubkeyAcceptedAlgorithms
 *
 * # Two keys, one host, and why both
 *
 * `cmd/e2e-sshd` accepts a modern sshd's algorithm list (ssh-rsa EXCLUDED, as
 * OpenSSH 8.8 and later default to) and, with `-rsa-key`, mints an RSA key
 * beside its ed25519 one. BOTH are stored in the vault and BOTH open a pane:
 * the ed25519 case is what passed all along — its type IS its algorithm — so it
 * proves the transport while the RSA case isolates the algorithm. A report from
 * one machine saying "it works here" was exactly this half.
 *
 * # No timing
 *
 * Every wait is on an observable state: a session that did not exist before the
 * profile was asked for, bound to the pane the renderer built, and the far
 * host's own `AUTH=` counter for a connection that really authenticated.
 */
import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'

import { expect, type Browser, type Page } from '@playwright/test'

import { BASE_URL } from './base-url'
import {
  standalone as base,
  bindEndpoint,
  openControlPlane,
  promptReady,
  type BackendEndpoint,
  type DisposableRoot,
  VaultBackend,
} from './harness'
import { startSshd, type SshdFixture } from './sshd-fixture'
import { readStand } from './stand'

const test = base

const QUICK_CONNECT_SEARCH = '.quick-connect__search input'
const QUICK_CONNECT_ITEM = '.quick-connect__item'

/**
 * The vault passphrase for THIS spec's own backend. Not the harness's shared
 * constant: that one is the shared stand's, and this one is disposable with the
 * backend (harness.ts says so where it declares the other).
 */
const VAULT_PASSPHRASE = 'e2e-vault-key-passphrase'

const RSA_PROFILE = 'Vault RSA key'
const ED25519_PROFILE = 'Vault ed25519 key'

interface LiveSession {
  sessionId: string
  instanceId: string
  sessionEpoch: number
  paneId: string | null
  attached: boolean
}

/** One JSON-RPC call on a socket of the test's own. It never attaches to a
 *  session, so asking a question cannot change the state it is about. */
async function ask<T>(ep: BackendEndpoint, method: string, params: unknown): Promise<T> {
  const wire = await openControlPlane(ep.port, ep.token)
  try {
    return (await wire.call(method, params)) as T
  } finally {
    wire.close()
  }
}

async function liveSessions(ep: BackendEndpoint): Promise<LiveSession[]> {
  const answer = await ask<{ sessions: LiveSession[] }>(ep, 'sessions.live', {})
  return answer.sessions
}

/**
 * The fixture's own count of authenticated connections, read off its `AUTH=`
 * line: the LAST value, because the line carries a running count.
 */
function authenticatedConnections(fixture: SshdFixture): number {
  const printed = fixture.lines().filter((line) => line.startsWith('AUTH='))
  if (printed.length === 0) return 0
  const last = printed[printed.length - 1]
  return Number(last.slice('AUTH='.length))
}

/** Open a page on a context of its own, bound to this backend. */
async function freshClient(browser: Browser, ep: BackendEndpoint): Promise<Page> {
  // baseURL explicitly: a context made by hand inherits nothing from the
  // config's `use`, so `goto('/')` would have no origin to resolve against.
  const context = await browser.newContext({ baseURL: BASE_URL })
  const page = await context.newPage()
  await bindEndpoint(page, ep)
  await page.goto('/')
  return page
}

/** Open a saved profile the way a person does: quick connect finds it and
 *  Enter opens it. (ssh-helper-happy-path.spec.ts holds the same three presses;
 *  it does not export them, and a fourth private copy is what this file would
 *  be if it re-derived them.) */
async function openSavedProfile(page: Page, profileName: string): Promise<void> {
  await page.keyboard.press('Control+Shift+P')
  const search = page.locator(QUICK_CONNECT_SEARCH)
  await expect(search).toBeVisible({ timeout: 30_000 })
  await search.fill(profileName)
  // The row is the observable that says Enter will open the SAVED profile
  // rather than the ad-hoc "Connect to <host>" fallback (nocx-k1691).
  await expect(page.locator(QUICK_CONNECT_ITEM, { hasText: profileName })).toBeVisible({
    timeout: 30_000,
  })
  await page.keyboard.press('Enter')
}

/**
 * Store one private key in the vault and save a profile bound to it.
 *
 * The key text comes from a file the FIXTURE minted, which is the only way a
 * spec can hold the private half of a key the far host accepts. It travels
 * over `secrets.saveKeyMaterial` — the control plane the product itself uses —
 * and what the profile names is the returned ROW, never the material
 * (ADR-0011 §2).
 */
async function seedVaultKeyProfile(
  ep: BackendEndpoint,
  fixture: SshdFixture,
  name: string,
  keyPath: string,
): Promise<void> {
  const minted = await ask<{ row: string }>(ep, 'secrets.saveKeyMaterial', {
    keyText: readFileSync(keyPath, 'utf8'),
    name,
  })
  await ask(ep, 'profiles.create', {
    type: 'ssh',
    name,
    options: {
      host: fixture.host,
      port: fixture.port,
      user: 'e2e',
      auth: 'publicKey',
      keySecret: minted.row,
    },
  })
}

/**
 * Open the named profile and answer with the session that did not exist
 * before — read off the pane element the renderer built, not off the cwd chip,
 * which carries a placeholder from construction and says nothing about whether
 * a shell ever started.
 */
async function openProfileAndWaitForItsPane(
  browser: Browser,
  ep: BackendEndpoint,
  profileName: string,
  /** Whether this open still owes the connect-time ask. The answer is keyed by
   *  the MACHINE's host-key fingerprint (ADR-0034), so two connections to one
   *  host ask once between them — the second open here is silent, and the pane
   *  it produces is the proof that nothing was asked. */
  answerAsk: boolean,
): Promise<string> {
  const page = await freshClient(browser, ep)
  await promptReady(page)
  const before = (await liveSessions(ep)).map((s) => s.sessionId)
  await openSavedProfile(page, profileName)
  if (answerAsk) {
    // A helper-mode connection is asked even though it names the helper as its
    // METHOD: the deployment is the machine's permission, not the
    // connection's choice (ADR-0105, nocx-1kejz.3).
    const helperAsk = page.getByRole('dialog').filter({ hasText: 'Choose how nocx connects' })
    await expect(helperAsk).toBeVisible({ timeout: 30_000 })
    await helperAsk.getByRole('radio', { name: 'Helper' }).click()
    await helperAsk.getByRole('button', { name: 'Continue' }).click()
    await expect(helperAsk).not.toBeVisible()
  }

  const activePane = page.locator('.pane.active')
  let opened = ''
  await expect
    .poll(
      async () => {
        const fresh = (await liveSessions(ep)).find(
          (session) => !before.includes(session.sessionId),
        )
        if (fresh === undefined) return false
        if ((await activePane.getAttribute('data-session-id')) !== fresh.sessionId) return false
        opened = fresh.sessionId
        return true
      },
      { timeout: 120_000, message: `opening ${profileName} never produced a pane of its own` },
    )
    .toBe(true)
  return opened
}

test.describe('a vault-held key opens a pane through the helper', () => {
  test('an RSA key signs the algorithm a modern host negotiates, and an ed25519 key still works beside it', async ({
    browser,
  }) => {
    // A go build of the fixture, two real SSH handshakes through the helper,
    // and two panes. Every wait inside is on an observable state.
    test.setTimeout(600_000)

    const home: DisposableRoot = { root: mkdtempSync(path.join(tmpdir(), 'nocx-vault-key-')) }
    const hostRoot = mkdtempSync(path.join(tmpdir(), 'nocx-vault-key-host-'))
    const remoteHome = path.join(hostRoot, 'home')
    mkdirSync(remoteHome, { recursive: true, mode: 0o700 })

    const backend = new VaultBackend(readStand().server, home)
    let fixture: SshdFixture | null = null

    try {
      // `-rsa-key`: the fixture accepts an RSA key beside its ed25519 one, and
      // its default algorithm list is a modern sshd's — which EXCLUDES ssh-rsa
      // (SHA-1). Without that exclusion the RSA case would pass with the
      // defect still in place.
      fixture = await startSshd({
        home: remoteHome,
        cwd: remoteHome,
        args: ['-rsa-key'],
      })
      const rsaKey = fixture.rsaKey
      expect(rsaKey, 'the fixture minted no RSA key: startSshd did not pass -rsa-key').toBeTruthy()

      const endpoint = await backend.start()

      // The far host's key, trusted in the home this backend runs under: the
      // helper asks the COORDINATOR whether to trust it, and the coordinator
      // reads its own known_hosts. REPLACED, never appended — every fixture
      // spawn mints a fresh key.
      const sshDir = path.join(backend.isolatedHome, '.ssh')
      mkdirSync(sshDir, { recursive: true, mode: 0o700 })
      writeFileSync(path.join(sshDir, 'known_hosts'), `${fixture.knownHosts}\n`)

      await ask(endpoint, 'vault.setup', { passphrase: VAULT_PASSPHRASE })
      await seedVaultKeyProfile(endpoint, fixture, RSA_PROFILE, rsaKey!)
      await seedVaultKeyProfile(endpoint, fixture, ED25519_PROFILE, fixture.userKey)

      // ── THE RSA KEY: the defect ──────────────────────────────────────────
      //
      // A pane exists only if the shell channel opened, which happens only
      // after the far host accepted a signature made with rsa-sha2-256 or
      // -512. With the helper's signer unable to be asked for an algorithm,
      // this was `ssh-rsa`, the host refused it, and no pane appeared.
      const beforeRsa = authenticatedConnections(fixture)
      await openProfileAndWaitForItsPane(browser, endpoint, RSA_PROFILE, true)
      expect(
        authenticatedConnections(fixture),
        'the far host did not authenticate the RSA-keyed pane',
      ).toBeGreaterThan(beforeRsa)

      // ── THE ED25519 KEY: the half that always worked ─────────────────────
      //
      // Its type IS its algorithm, so it proves the transport while the RSA
      // case isolates the algorithm. A green RSA with a red ed25519 would mean
      // the harness broke rather than the product.
      const beforeEd = authenticatedConnections(fixture)
      // The same machine, a second connection, and NOTHING is asked: the
      // first open's answer was recorded against this host's fingerprint, which
      // is what "asked once per machine" means. A dialog here would fail the
      // wait below, so the pane is the assertion.
      await openProfileAndWaitForItsPane(browser, endpoint, ED25519_PROFILE, false)
      expect(
        authenticatedConnections(fixture),
        'the far host did not authenticate the ed25519-keyed pane',
      ).toBeGreaterThan(beforeEd)

      // AND NOTHING WAS TYPED: both panes authenticated with the stored key,
      // not with a password prompt somebody answered.
      expect(fixture.lines().some((line) => line.startsWith('CONN='))).toBe(true)
    } finally {
      if (fixture !== null) {
        fixture.proc.kill('SIGTERM')
      }
      backend.stop()
    }
  })
})
