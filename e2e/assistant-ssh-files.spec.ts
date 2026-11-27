/**
 * e2e: on an ssh pane the assistant reads, lists, searches and edits THAT
 * server's files, and never this machine's same-named ones (nocx-a62cl.11.4,
 * stage 10's DONE WHEN).
 *
 * # The stand
 *
 * A real backend with its own disposable home (this machine, "L"), the
 * `sshd-fixture` host with its own home (the far machine, "R"), a host record
 * in Helper mode, `FakeOpenAI` and real pi. The two filesystems are two temp
 * trees, and every fixture file has a same-NAMED twin under each home:
 *
 *   R/proj/{permit.conf, ask.conf, src/a.go: "needle-remote"}
 *   L/proj/{permit.conf, ask.conf, src/a.go: "needle-local", local-only.txt}
 *
 * The absolute paths therefore differ (R is not L), which is what the stand can
 * do; the assertion that the tools never reach L is what it must do, and it is
 * made on L's bytes and modification times after every scenario.
 *
 * # Why this file's job cannot be done by a Go test alone
 *
 * A Go test can hand the tools a fake provider and call it an ssh pane. What
 * it cannot do is put a REAL helper on a real ssh connection, a real pane on
 * the screen, a real person in front of the approval prompt, and a real `pi`
 * behind the model. Every scenario below is a person's gesture and its
 * observable result; the transport acceptance test
 * (internal/transport/assistant_files_remote_test.go) is the proof about the
 * local disk, and this file is the proof about the product.
 *
 * # Policy
 *
 * `policy.set`, the product's own control-plane write, before the first ask:
 * `observe` permit on R/proj, `mutate-reversible` permit on R/proj/permit.conf
 * alone, everything else ask. So a read anywhere under R/proj runs, an edit of
 * permit.conf runs, and an edit of ask.conf is a question — which is scenario
 * 4's prompt and scenario 5's window.
 *
 * # No timing
 *
 * Every wait is on an observable state: a frame on the wire, a request the fake
 * recorded, an element on screen, an attribute on a block. Nothing waits out a
 * duration.
 */
import { mkdirSync, mkdtempSync, readFileSync, readdirSync, statSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { expect, type Page } from '@playwright/test'

import {
  standalone as base,
  appReadyForInput,
  bindEndpoint,
  promptReady,
  VaultBackend,
  type BackendEndpoint,
} from './harness'
import { FakeOpenAI } from './fake-openai'
import { configurePiHome } from './pi-home'
import { rpc, startSshd, type SshdFixture } from './sshd-fixture'
import { readStand } from './stand'
import { askFromPrompt, toolResults } from './ask-gesture'

const test = base

const TAB = '.nocx-tab'
const QUICK_CONNECT_SEARCH = '.quick-connect__search input'
const QUICK_CONNECT_ITEM = '.quick-connect__item'
const METHOD_ASK = 'Choose how nocx connects'
const APPROVAL_TITLE = 'This action needs your approval'
const ANSWER_BODY = '.cmd-output[data-answer-body]'

const serverBin = () => readStand().server

let fake: FakeOpenAI
let backend: VaultBackend
let endpoint: BackendEndpoint
let fixture: SshdFixture
/** The far machine's home — what the pane's shell calls `~`. */
let remoteHome: string
/** This machine's home for this backend: the disposable one. */
let localHome: string
/** R's project directory, the absolute path the model is given. */
let remoteProj: string
/** L's twin of it. */
let localProj: string

/** L's bytes as seeded, and what "untouched" is measured against. */
const seededLocal = new Map<string, { body: string; size: number; mtimeMs: number }>()

test.describe.configure({ mode: 'serial' })

function seed(path: string, body: string): void {
  mkdirSync(join(path, '..'), { recursive: true })
  writeFileSync(path, body)
}

/** Every file under L/proj as it stands now: bytes, length and mtime. */
function localState(dir: string): Map<string, { body: string; size: number; mtimeMs: number }> {
  const out = new Map<string, { body: string; size: number; mtimeMs: number }>()
  const walk = (at: string): void => {
    for (const entry of readdirSync(at, { withFileTypes: true })) {
      const child = join(at, entry.name)
      if (entry.isDirectory()) {
        walk(child)
        continue
      }
      const stat = statSync(child)
      out.set(child, {
        body: readFileSync(child, 'utf8'),
        size: stat.size,
        mtimeMs: stat.mtimeMs,
      })
    }
  }
  walk(dir)
  return out
}

test.beforeAll(async () => {
  fake = new FakeOpenAI()
  await fake.start()

  remoteHome = mkdtempSync(join(tmpdir(), 'nocx-ssh-files-host-'))
  // mkdtempSync creates the leaf; home-isolation creates `<root>/home` itself,
  // so the local twin is seeded after the backend has started (start() is what
  // derives it), from the path the boundary will use.
  remoteProj = join(remoteHome, 'proj')
  seed(join(remoteProj, 'permit.conf'), 'permit remote\n')
  seed(join(remoteProj, 'ask.conf'), 'ask remote\n')
  seed(join(remoteProj, 'src', 'a.go'), 'package a\n// needle-remote\n')

  fixture = await startSshd({ home: remoteHome, cwd: remoteHome })

  const root = mkdtempSync(join(tmpdir(), 'nocx-ssh-files-'))
  backend = new VaultBackend(serverBin(), { root })
  endpoint = await backend.start(configurePiHome(root, fake.baseUrl()))

  localHome = join(root, 'home')
  localProj = join(localHome, 'proj')
  seed(join(localProj, 'permit.conf'), 'permit local\n')
  seed(join(localProj, 'ask.conf'), 'ask local\n')
  seed(join(localProj, 'src', 'a.go'), 'package a\n// needle-local\n')
  seed(join(localProj, 'local-only.txt'), 'only here\n')
  for (const [path, state] of localState(localProj)) seededLocal.set(path, state)

  const sshDir = join(backend.isolatedHome, '.ssh')
  mkdirSync(sshDir, { recursive: true, mode: 0o700 })
  writeFileSync(join(sshDir, 'known_hosts'), `${fixture.knownHosts}\n`)
})

test.afterAll(async () => {
  fixture?.proc.kill('SIGTERM')
  backend?.stop()
  await fake?.stop()
})

/** The one policy write this file needs: reads under R/proj, writes to
 *  permit.conf alone, and a question for everything else. */
async function permitReadsAndOneWrite(page: Page): Promise<void> {
  const ask = { decision: 'ask', scopes: [] }
  const res = await rpc<{ applied: boolean }>(page, endpoint, 'policy.set', {
    policy: {
      observe: { decision: 'permit', scopes: [{ kind: 'path', id: remoteProj }] },
      'mutate-reversible': {
        decision: 'permit',
        scopes: [{ kind: 'path', id: join(remoteProj, 'permit.conf') }],
      },
      'mutate-destructive': ask,
      'privilege-change': ask,
      disclose: ask,
      'cross-boundary': ask,
      delegate: ask,
    },
    runs: 'ask',
  })
  expect(res.applied).toBe(true)
}

/** Open a pane to the fixture host in Helper mode, through the product's own
 *  quick-connect and connect-time ask. */
async function openHelperPane(page: Page, profileName: string): Promise<void> {
  await page.keyboard.press('Control+Shift+P')
  const search = page.locator(QUICK_CONNECT_SEARCH)
  await expect(search).toBeVisible({ timeout: 30_000 })
  await search.fill(profileName)
  await expect(page.locator(QUICK_CONNECT_ITEM, { hasText: profileName })).toBeVisible({
    timeout: 30_000,
  })
  await page.keyboard.press('Enter')
  const methodAsk = page.getByRole('dialog').filter({ hasText: METHOD_ASK })
  await expect(methodAsk).toBeVisible({ timeout: 30_000 })
  await methodAsk.getByRole('radio', { name: 'Helper' }).click()
  await methodAsk.getByRole('button', { name: 'Continue' }).click()
  await expect(page.locator(TAB)).toHaveCount(2, { timeout: 120_000 })
  await promptReady(page)
}

/** Create the saved connection this file opens, once per test that needs it:
 *  the profile name carries the test's own nonce so two tests cannot collide
 *  in the palette. */
async function createProfile(page: Page, name: string): Promise<string> {
  await rpc(page, endpoint, 'profiles.create', {
    type: 'ssh',
    name,
    options: {
      host: fixture.host,
      port: fixture.port,
      user: fixture.user,
      keyPath: fixture.userKey,
    },
  })
  return name
}

/** A fresh page on this backend with its first tab ready. */
async function openApp(page: Page): Promise<void> {
  await bindEndpoint(page, endpoint)
  await page.goto('/')
  await expect(page.locator(TAB)).toHaveCount(1)
  await appReadyForInput(page)
}

function answerBlock(page: Page, question: string) {
  return page.locator('.cmd-block').filter({ hasText: question })
}

/** Success is silent (2026-09-14 §3.1): the outcome attribute is the
 *  observable, the word `completed` never reaches the DOM. */
async function answerFinished(page: Page, question: string): Promise<void> {
  await expect(answerBlock(page, question)).toHaveAttribute('data-outcome', 'success', {
    timeout: 60_000,
  })
}

/** L/proj is exactly as it was seeded: every byte, every length, every
 *  modification time. A tool that read or wrote this machine's twin instead
 *  of the host's file would move one of them. */
function expectLocalUntouched(): void {
  const now = localState(localProj)
  expect([...now.keys()].sort()).toEqual([...seededLocal.keys()].sort())
  for (const [path, was] of seededLocal) {
    expect(
      now.get(path),
      `${path} was touched by a call that should have reached the host`,
    ).toEqual(was)
  }
}

test.describe('the assistant acts on the ssh pane’s own files', () => {
  test.use({ viewport: { width: 1280, height: 900 } })

  test('files.list and files.search answer from the host, not from here', async ({ page }) => {
    test.setTimeout(600_000)
    await openApp(page)
    await permitReadsAndOneWrite(page)
    await openHelperPane(page, await createProfile(page, `e2e-ssh-files-list-${Date.now()}`))

    // Both calls in ONE turn: the listing proves the host's names, and the
    // search proves its bytes. The answer is DERIVED from what the model was
    // sent, so the person's answer is evidence about the product rather than
    // about the fake.
    fake.setScript({
      chunks: [],
      toolCalls: [
        { name: 'files.list', arguments: { path: remoteProj } },
        { name: 'files.search', arguments: { path: remoteProj, pattern: 'needle' } },
      ],
    })
    fake.setScript({ chunks: (body) => answerFrom(body) })

    const QUESTION = 'List the project and search it for the needle.'
    const before = fake.requests().length
    await askFromPrompt(page, QUESTION)
    await answerFinished(page, QUESTION)

    const answered = fake
      .requests()
      .slice(before)
      .map((request) => request.body)
    const results = answered.flatMap(toolResults)
    const listing =
      results.find(
        (result) => result.includes('local-only.txt') || result.includes('permit.conf'),
      ) ?? ''
    const search = results.find((result) => result.includes('needle')) ?? ''

    // 1. THE LISTING IS THE HOST'S: its own names, and none of the file only
    //    this machine has.
    expect(listing).toContain('permit.conf')
    expect(listing).toContain('ask.conf')
    expect(listing).not.toContain('local-only.txt')

    // 2. THE SEARCH SAW THE HOST'S BYTES, and never this machine's twin.
    expect(search).toContain('needle-remote')
    expect(search).not.toContain('needle-local')

    // And the person's answer repeats it, so the result really reached the
    // model and the surface really showed it.
    await expect(answerBlock(page, QUESTION).locator(ANSWER_BODY)).toContainText('needle-remote')

    expectLocalUntouched()
  })

  test('files.edit under a permit row changes the host’s file and not this machine’s', async ({
    page,
  }) => {
    test.setTimeout(600_000)
    await openApp(page)
    await permitReadsAndOneWrite(page)
    await openHelperPane(page, await createProfile(page, `e2e-ssh-files-permit-${Date.now()}`))

    const target = join(remoteProj, 'permit.conf')
    fake.setScript({
      chunks: [],
      toolCalls: [{ name: 'files.read', arguments: { path: target } }],
    })
    // The revision is what the READ actually answered, never one this spec
    // invented: a made-up token would be refused and the scenario would pass
    // for the wrong reason.
    fake.setScript({
      chunks: [],
      toolCalls: (body) => [
        {
          name: 'files.edit',
          arguments: {
            path: target,
            revision: revisionOf(body),
            patch: 'PUT 1.=1:\n+permit remote, edited',
          },
        },
      ],
    })
    fake.setScript({ chunks: (body) => answerFrom(body) })

    const QUESTION = 'Read permit.conf and change its first line.'
    await askFromPrompt(page, QUESTION)
    await answerFinished(page, QUESTION)

    // 3. THE HOST'S FILE CHANGED, with no question asked: the row permits this
    //    one path.
    expect(readFileSync(target, 'utf8')).toBe('permit remote, edited\n')
    // And this machine's twin is byte for byte, mtime for mtime, as it was.
    expectLocalUntouched()
  })

  test('an edit the policy asks about changes the host’s file after the answer', async ({
    page,
  }) => {
    test.setTimeout(600_000)
    await openApp(page)
    await permitReadsAndOneWrite(page)
    await openHelperPane(page, await createProfile(page, `e2e-ssh-files-ask-${Date.now()}`))

    const target = join(remoteProj, 'ask.conf')
    fake.setScript({
      chunks: [],
      toolCalls: [{ name: 'files.read', arguments: { path: target } }],
    })
    fake.setScript({
      chunks: [],
      toolCalls: (body) => [
        {
          name: 'files.edit',
          arguments: {
            path: target,
            revision: revisionOf(body),
            patch: 'PUT 1.=1:\n+ask remote, edited',
          },
        },
      ],
    })
    fake.setScript({ chunks: (body) => answerFrom(body) })

    const QUESTION = 'Read ask.conf and change its first line.'
    await askFromPrompt(page, QUESTION)

    // 4. THE QUESTION, and then the edit: the person's yes is what authorises
    //    a write outside the permit scope.
    const prompt = page.getByRole('dialog', { name: APPROVAL_TITLE })
    await expect(prompt).toBeVisible({ timeout: 60_000 })
    await expect(prompt).toContainText('files.edit')
    await prompt.getByRole('button', { name: /^Allow once/ }).click()
    await expect(prompt).toHaveCount(0)

    await answerFinished(page, QUESTION)
    expect(readFileSync(target, 'utf8')).toBe('ask remote, edited\n')
    expectLocalUntouched()
  })

  test('a change made while the question is open is refused and nothing is installed', async ({
    page,
  }) => {
    test.setTimeout(600_000)
    await openApp(page)
    await permitReadsAndOneWrite(page)
    await openHelperPane(page, await createProfile(page, `e2e-ssh-files-changed-${Date.now()}`))

    const target = join(remoteProj, 'ask.conf')
    fake.setScript({
      chunks: [],
      toolCalls: [{ name: 'files.read', arguments: { path: target } }],
    })
    fake.setScript({
      chunks: [],
      toolCalls: (body) => [
        {
          name: 'files.edit',
          arguments: {
            path: target,
            revision: revisionOf(body),
            patch: 'PUT 1.=1:\n+ask remote, edited',
          },
        },
      ],
    })
    fake.setScript({ chunks: (body) => answerFrom(body) })

    const QUESTION = 'Read ask.conf and change its first line, again.'
    const before = fake.requests().length
    await askFromPrompt(page, QUESTION)

    const prompt = page.getByRole('dialog', { name: APPROVAL_TITLE })
    await expect(prompt).toBeVisible({ timeout: 60_000 })
    // 5. THE WINDOW: while the question is open, somebody else writes the file
    //    on the host. The approval the person is about to give covers bytes
    //    that no longer exist, and the call must refuse rather than install.
    writeFileSync(target, 'third-party\n')
    await prompt.getByRole('button', { name: /^Allow once/ }).click()
    await expect(prompt).toHaveCount(0)
    await answerFinished(page, QUESTION)

    // The model was told, and the refusal names the change rather than a
    // missing binding.
    const results = fake
      .requests()
      .slice(before)
      .flatMap((request) => toolResults(request.body))
    const refusal = results.find((result) => result.startsWith('REFUSED')) ?? ''
    expect(refusal).toContain('files.edit')
    expect(refusal).toContain('changed since approval')

    // The outsider's bytes are what remains on the host, and this machine's
    // twin was never involved.
    expect(readFileSync(target, 'utf8')).toBe('third-party\n')
    expectLocalUntouched()
  })

  test('a host whose helper is not serving refuses files.* with its reason', async ({ page }) => {
    test.setTimeout(600_000)
    await openApp(page)
    // A raw host runs no helper at all, and the reason the panel shows for
    // such a connection is `mode-raw`. Reads are permitted outright here so
    // that what stops the call is the axis and never the policy.
    const ask = { decision: 'ask', scopes: [] }
    const applied = await rpc<{ applied: boolean }>(page, endpoint, 'policy.set', {
      policy: {
        observe: { decision: 'permit', scopes: [] },
        'mutate-reversible': ask,
        'mutate-destructive': ask,
        'privilege-change': ask,
        disclose: ask,
        'cross-boundary': ask,
        delegate: ask,
      },
      runs: 'ask',
    })
    expect(applied.applied).toBe(true)
    const name = await createProfile(page, `e2e-ssh-files-raw-${Date.now()}`)
    await page.keyboard.press('Control+Shift+P')
    const search = page.locator(QUICK_CONNECT_SEARCH)
    await expect(search).toBeVisible({ timeout: 30_000 })
    await search.fill(name)
    await expect(page.locator(QUICK_CONNECT_ITEM, { hasText: name })).toBeVisible({
      timeout: 30_000,
    })
    await page.keyboard.press('Enter')
    const methodAsk = page.getByRole('dialog').filter({ hasText: METHOD_ASK })
    await expect(methodAsk).toBeVisible({ timeout: 30_000 })
    await methodAsk.getByRole('radio', { name: 'Raw' }).click()
    await methodAsk.getByRole('button', { name: 'Continue' }).click()
    await expect(page.locator(TAB)).toHaveCount(2, { timeout: 120_000 })

    fake.setScript({
      chunks: [],
      toolCalls: [{ name: 'files.read', arguments: { path: join(remoteProj, 'permit.conf') } }],
    })
    // 6. THE SCREEN READ STILL WORKS: its content does not come through the
    //    helper, so a host with no helper loses its FILES and not the pane.
    fake.setScript({
      chunks: [],
      toolCalls: [{ name: 'session.read', arguments: {} }],
    })
    fake.setScript({ chunks: (body) => answerFrom(body) })

    const QUESTION = 'Read permit.conf on this host, then read the screen.'
    const before = fake.requests().length
    await askFromRawPane(page, QUESTION)
    await answerFinished(page, QUESTION)

    const results = fake
      .requests()
      .slice(before)
      .flatMap((request) => toolResults(request.body))
    const refused = results.find((result) => result.startsWith('REFUSED')) ?? ''
    expect(refused).toContain('files.read')
    expect(refused).toContain('mode-raw')
    // And the run went on: the screen read ran in the same turn, so both calls
    // are blocks under the answer and only one of them was refused.
    const turn = answerBlock(page, QUESTION)
    await expect(turn.locator('.cmd-block[data-tool="files.read"]')).toHaveCount(1)
    await expect(turn.locator('.cmd-block[data-tool="session.read"]')).toHaveCount(1)

    expectLocalUntouched()
  })
})

/** The revision the read actually answered, taken from the tool result the
 *  model was sent. A missing token throws rather than silently proposing an
 *  edit the backend would refuse. */
function revisionOf(body: string): string {
  const result = toolResults(body).find((text) => text.includes('"revision"'))
  const match = result?.match(/"revision":"([^"]+)"/)
  if (!match) throw new Error('the read answered no revision to edit under')
  return match[1]
}

/** The model's answer, derived from what it was actually sent: the markers the
 *  scenarios assert on can only be there if the tool results reached it. */
function answerFrom(body: string): string[] {
  const results = toolResults(body)
  if (results.length === 0) return ['I was given no result.']
  return ['Here is what I found: ', results.join(' ')]
}

/** Ask from a pane whose shell owns the keys — a raw ssh pane — where the
 *  composer is the reserved box the terminal mode leaves in place. The chord
 *  is the product's own target switch, and what follows waits for the editor
 *  the switch makes live. */
async function askFromRawPane(page: Page, question: string): Promise<void> {
  await page.locator('.pane.active .terminal-live-container').click()
  await page.keyboard.press('ControlOrMeta+Enter')
  const input = page.locator('.pane.active .nocx-editor-input')
  await expect(input).toBeVisible({ timeout: 15_000 })
  await input.fill(question)
  await page.keyboard.press('Enter')
}
