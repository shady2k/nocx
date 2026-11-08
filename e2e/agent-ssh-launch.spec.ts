/**
 * e2e: a typed agent starts where the pane is (nocx-8gg8y).
 *
 * # What the owner hit, and what a person does here
 *
 * The owner opened an ssh session to a Linux host and typed `claude`. The pane
 * answered `nocx: not started — the local agent record could not be resolved`
 * in about a second, and the backend log held no line about the refusal. The
 * name of an agent is a WRAPPER the pane's shell carries, so on a host that
 * runs the helper the far shell asks this coordinator what that launch is —
 * and the coordinator answered a launch on somebody else's machine with a
 * LOCAL record question it could not even name the failure of.
 *
 * # Two panes, because the answer depends on where the shell is
 *
 * The remote half opens a saved profile against `cmd/e2e-sshd` with the
 * helper as its method, so the shell really runs on the far side of an ssh
 * connection and the wrapper really asks over that host's lifecycle channel.
 * The local half types the same name into this machine's own pane. Both must
 * start the agent, and neither may say "not started".
 *
 * # What the agent is
 *
 * A stand-in `claude` on PATH. The e2e container runs no model and this file
 * is not about one: the question is whether a typed name reaches an exec at
 * all, and the stand-in's own words are the proof — it prints the directory it
 * was started in, which for the remote pane is the far host's HOME.
 *
 * # No timing
 *
 * Every wait is on an observable state: the far host's fixture READY, a
 * prompt that reports ready, the consent question, and the block the command
 * opened. Nothing here waits out a duration.
 */
import { mkdirSync, mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { delimiter, join } from 'node:path'

import { expect, type Page } from '@playwright/test'

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

const TAB = '.nocx-tab'
const QUICK_CONNECT_SEARCH = '.quick-connect__search input'
const QUICK_CONNECT_ITEM = '.quick-connect__item'
const AGENT_APPROVAL_TITLE = "Allow this agent to use nocx's tools?"

const serverBin = () => readStand().server

/** One JSON-RPC call on a socket of the test's own, as ssh-stop.spec.ts does. */
async function ask<T>(ep: BackendEndpoint, method: string, params: unknown): Promise<T> {
  const wire = await openControlPlane(ep.port, ep.token)
  try {
    return (await wire.call(method, params)) as T
  } finally {
    wire.close()
  }
}

/** A `claude` on PATH that says where it ran. The remote pane's shell starts
 *  in the fixture's home, so the directory in its answer is the far host's. */
function writeStandInAgent(dir: string): void {
  mkdirSync(dir, { recursive: true })
  writeFileSync(join(dir, 'claude'), '#!/bin/sh\nprintf "STAND-IN-CLAUDE cwd=%s\\n" "$PWD"\n', {
    mode: 0o755,
  })
}

/** Answer the consent question a first launch raises, and fail loudly if the
 *  question never appears — a launch that skips it is a different defect. */
async function allowTheAgent(page: Page): Promise<void> {
  const dialog = page.getByRole('dialog').filter({ hasText: AGENT_APPROVAL_TITLE })
  await expect(dialog).toBeVisible({ timeout: 60_000 })
  await dialog.getByRole('button', { name: 'Allow', exact: true }).click()
  await expect(dialog).toHaveCount(0, { timeout: 15_000 })
}

/** The block the typed command opened, by the stand-in's own marker. */
function agentBlock(page: Page) {
  return page.locator('.pane.active .cmd-block', { hasText: 'STAND-IN-CLAUDE' })
}

/** What the pane must never say about a launch that started. */
async function expectNoRefusal(page: Page): Promise<void> {
  await expect(page.locator('.pane.active')).not.toContainText('nocx: not started', {
    timeout: 15_000,
  })
  await expect(page.locator('.pane.active')).not.toContainText('not orchestrated', {
    timeout: 15_000,
  })
}

test.describe('a typed agent starts where its pane is', () => {
  test('an agent typed in an ssh pane starts on the far host', async ({ page }) => {
    test.setTimeout(600_000)
    const root = mkdtempSync(join(tmpdir(), 'nocx-agent-ssh-'))
    const hostRoot = mkdtempSync(join(tmpdir(), 'nocx-agent-ssh-host-'))
    const remoteHome = join(hostRoot, 'home')
    mkdirSync(remoteHome, { recursive: true, mode: 0o700 })
    const fakeBin = join(root, 'bin')
    writeStandInAgent(fakeBin)
    // Both the coordinator (which resolves the name for the consent it asks)
    // and the far host (which execs it) read the same PATH: this container
    // holds both ends of the connection.
    const previousPath = process.env.PATH
    process.env.PATH = `${fakeBin}${delimiter}${previousPath ?? ''}`

    const backend = new VaultBackend(serverBin(), { root } satisfies DisposableRoot)
    let fixture: SshdFixture | null = null
    try {
      fixture = await startSshd({ home: remoteHome, cwd: remoteHome })
      const endpoint = await backend.start()
      const sshDir = join(backend.isolatedHome, '.ssh')
      mkdirSync(sshDir, { recursive: true, mode: 0o700 })
      writeFileSync(join(sshDir, 'known_hosts'), `${fixture.knownHosts}\n`)

      await bindEndpoint(page, endpoint)
      await page.goto('/')
      await expect(page.locator(TAB)).toHaveCount(1)
      await promptReady(page)

      const profileName = `e2e-agent-ssh-${Date.now()}`
      await ask(endpoint, 'profiles.create', {
        type: 'ssh',
        name: profileName,
        options: {
          host: fixture.host,
          port: fixture.port,
          user: 'e2e',
          keyPath: fixture.userKey,
          desiredMode: 'helper',
        },
      })
      await page.keyboard.press('Control+Shift+P')
      const search = page.locator(QUICK_CONNECT_SEARCH)
      await expect(search).toBeVisible({ timeout: 30_000 })
      await search.fill(profileName)
      await expect(page.locator(QUICK_CONNECT_ITEM, { hasText: profileName })).toBeVisible({
        timeout: 30_000,
      })
      await page.keyboard.press('Enter')
      const methodAsk = page.getByRole('dialog').filter({ hasText: 'Choose how nocx connects' })
      await expect(methodAsk).toBeVisible({ timeout: 30_000 })
      await methodAsk.getByRole('radio', { name: 'Helper' }).click()
      await methodAsk.getByRole('button', { name: 'Continue' }).click()
      await expect(page.locator(TAB)).toHaveCount(2, { timeout: 120_000 })
      await promptReady(page)

      await page.keyboard.type('claude')
      await page.keyboard.press('Enter')
      await allowTheAgent(page)

      const block = agentBlock(page)
      await expect(block).toBeVisible({ timeout: 120_000 })
      // THE FAR SHELL'S OWN CWD, not this machine's: the stand-in ran where
      // the pane is, which is the whole of what a remote launch means.
      await expect(block).toContainText(`cwd=${remoteHome}`, { timeout: 30_000 })
      await expect(block).not.toHaveClass(/cmd-block-running/, { timeout: 60_000 })
      await expectNoRefusal(page)
    } finally {
      fixture?.proc.kill('SIGTERM')
      backend.stop()
      process.env.PATH = previousPath
    }
  })

  test('an agent typed in a local pane starts here', async ({ page }) => {
    test.setTimeout(600_000)
    const root = mkdtempSync(join(tmpdir(), 'nocx-agent-local-'))
    const fakeBin = join(root, 'bin')
    writeStandInAgent(fakeBin)
    const previousPath = process.env.PATH
    process.env.PATH = `${fakeBin}${delimiter}${previousPath ?? ''}`

    const backend = new VaultBackend(serverBin(), { root } satisfies DisposableRoot)
    try {
      const endpoint = await backend.start()
      await bindEndpoint(page, endpoint)
      await page.goto('/')
      await expect(page.locator(TAB)).toHaveCount(1)
      await promptReady(page)

      await page.keyboard.type('claude')
      await page.keyboard.press('Enter')
      await allowTheAgent(page)

      const block = agentBlock(page)
      await expect(block).toBeVisible({ timeout: 120_000 })
      await expect(block).toContainText('cwd=', { timeout: 30_000 })
      await expect(block).not.toHaveClass(/cmd-block-running/, { timeout: 60_000 })
      await expectNoRefusal(page)
    } finally {
      backend.stop()
      process.env.PATH = previousPath
    }
  })
})
