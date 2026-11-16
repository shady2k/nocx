/**
 * e2e: an SSH tab says what its connection is doing, and a lost connection
 * takes no input (design §5.8, §5.6; nocx-oalo1.5.5 and nocx-oalo1.5.1).
 *
 * ## What has to be true before the person has a shell (Q1)
 *
 * A first open of a host where the helper is not installed is mostly an
 * upload. The pane used to be EMPTY for the whole of it, and the owner's
 * report (nocx-q9od5) is why the connection head exists: one line, painted
 * before any network, naming the stage and the time in it. This spec stages
 * a slow route and records the head's visible observations before Enter.
 * A pooled transport can truthfully start Connected; a cold one must name its
 * opening stage. Polling only the current state misses fast transitions.
 *
 * ## What a loss must not do to the person's keyboard (nocx-oalo1.5.1)
 *
 * Seen on 2026-11-13 with htop in an SSH tab and the lid closed: the pane knew
 * the session was gone and kept sending input to it, and every refusal raised
 * its own danger toast — one per keystroke, stacking until the window was
 * unreadable. The renderer now sends nothing when pane.connection is not
 * usable, so the fault this spec reproduces leaves NO input frame on the wire
 * after the cut and exactly ONE notice (the head). The program under the pane
 * reports mouse movement, so the flood is not a hypothetical: a pointer over
 * the grid produces intents on its own.
 *
 * ## Why the assertions are observable states, not waits
 *
 * The silence is checked after a same-socket request has been answered (the
 * quick-connect palette's own list), so every earlier frame had its turn; the
 * head is waited on by its data-state; the healthy half is a command whose
 * block appears. No assertion sleeps a duration and none depends on how fast
 * this machine is.
 */
import { mkdirSync, writeFileSync } from 'node:fs'
import path from 'node:path'

import { appReadyForInput, test, expect, resolveBackend, type Page } from './harness'
import { startFaultProxy, type FaultProxy } from './fault-proxy'
import { startSshd, rpc, type SshdFixture } from './sshd-fixture'
import { readStand } from './stand'

const TAB = '.nocx-tab'
const EDITOR = '.pane.active .nocx-editor-input'
const HEAD = '.pane.active [data-testid="connection-head"]'
const RECONNECT = `${HEAD} .ui-button`
const GRID = '.pane.active .terminal-live-grid'
const QUICK_CONNECT = '.quick-connect__search input'

function knownHostsFor(fixture: SshdFixture, port: number): string {
  const rest = fixture.knownHosts.slice(fixture.knownHosts.indexOf(' ') + 1)
  return `[127.0.0.1]:${port} ${rest}`
}

function trustHostKey(line: string): void {
  const sshDir = path.join(readStand().home, '.ssh')
  mkdirSync(sshDir, { recursive: true, mode: 0o700 })
  writeFileSync(path.join(sshDir, 'known_hosts'), line + '\n')
}

interface Wired {
  fixture: SshdFixture
  proxy: FaultProxy
  profileId: string
  profileName: string
}

async function wire(page: Page, home: string): Promise<Wired> {
  const fixture = await startSshd({ home, cwd: home })
  const proxy = await startFaultProxy(fixture.host, fixture.port)
  trustHostKey(knownHostsFor(fixture, proxy.port))

  await page.goto('/')
  await appReadyForInput(page)
  await expect(page.locator(TAB)).toHaveCount(1)

  const endpoint = await resolveBackend(page)
  const profileName = `e2e-foundation-${Date.now()}`
  const created = await rpc<{ id: string }>(page, endpoint, 'profiles.create', {
    type: 'ssh',
    name: profileName,
    options: {
      host: '127.0.0.1',
      port: proxy.port,
      user: 'e2e',
      keyPath: fixture.userKey,
      keepaliveInterval: 2000,
      desiredMode: 'script',
    },
  })
  return { fixture, proxy, profileId: created.id, profileName }
}

async function unwire(page: Page, w: Wired | null): Promise<void> {
  if (!w) return
  try {
    const endpoint = await resolveBackend(page)
    await rpc(page, endpoint, 'profiles.delete', { id: w.profileId })
  } catch {
    // A cleanup that throws would replace the real failure with its own.
  }
  await w.proxy.close()
  w.fixture.proc.kill('SIGKILL')
}

/** Put the profile in the palette and press Enter, WITHOUT waiting for a
 *  shell: the wait is the thing the Q1 test is about. */
async function startConnection(page: Page, profileName: string): Promise<void> {
  await page.keyboard.press('Control+Shift+P')
  const search = page.locator(QUICK_CONNECT)
  await expect(search).toBeVisible()
  await search.fill(profileName)
  const savedRow = page.locator('.quick-connect__item:not(.quick-connect__item--system)', {
    hasText: profileName,
  })
  await expect(savedRow).toBeVisible({ timeout: 10_000 })
  await expect(savedRow).toHaveAttribute('aria-selected', 'true')
  await page.keyboard.press('Enter')
}

async function openConnection(page: Page, profileName: string): Promise<void> {
  await startConnection(page, profileName)
  await expect(page.locator(EDITOR)).toBeVisible({ timeout: 45_000 })
}

async function runCommand(page: Page, command: string): Promise<void> {
  const editor = page.locator(EDITOR)
  await editor.click()
  await editor.pressSequentially(command)
  await editor.press('Enter')
  await expect(
    page
      .locator('.pane.active .cmd-block', { hasText: command.split(' ').at(-1) ?? command })
      .first(),
  ).toBeVisible({ timeout: 30_000 })
}

/** Every control-plane frame this window sent, in order. A `session.intent`
 *  here is a person's input on its way to a shell. */
function captureFrames(page: Page): string[] {
  const frames: string[] = []
  page.on('websocket', (ws) => {
    if (!ws.url().includes('/session')) return
    ws.on('framesent', (frame) => {
      frames.push(typeof frame === 'string' ? frame : String(frame.payload))
    })
  })
  return frames
}

test('an SSH tab shows a stage within a second and never an empty screen (Q1)', async ({
  page,
}, testInfo) => {
  test.setTimeout(150_000)
  const home = testInfo.outputPath('foundation-head-home')
  mkdirSync(home, { recursive: true })
  let w: Wired | null = null
  try {
    w = await wire(page, home)
    // WARM THE HELPER FIRST. The first SSH open of a cold stand starts the
    // helper process, and that one-time cost is not the connection's dial; Q1
    // is about the tab's first second on a stand that is already serving.
    w.proxy.pass()
    await openConnection(page, w.profileName)

    // Another pane can reuse the warm transport. Delay the relay without
    // assuming that a second pane must dial it again. Its truthful first
    // observation can be Connected: the shell attempt is a separate axis.
    w.proxy.slow(1500)
    // Record the head before Enter. A stage can finish before Playwright's
    // next round trip; its visible sentence is still the statement Q1 owes.
    await page.evaluate(() => {
      const selector = '[data-testid="connection-head"]'
      const existing = new Set(document.querySelectorAll(selector))
      const observations: { state: string; stage: string; text: string; visible: boolean }[] = []
      Object.assign(window, { __foundationHeadObservations: observations })
      const record = (): void => {
        for (const head of document.querySelectorAll<HTMLElement>(selector)) {
          if (existing.has(head)) continue
          observations.push({
            state: head.dataset.state ?? '',
            stage: head.dataset.stage ?? '',
            text: head.textContent ?? '',
            visible:
              head.getClientRects().length > 0 && getComputedStyle(head).visibility !== 'hidden',
          })
        }
      }
      const observer = new MutationObserver(record)
      observer.observe(document.body, {
        subtree: true,
        childList: true,
        attributes: true,
        characterData: true,
      })
      Object.assign(window, { __foundationHeadObserver: observer })
    })
    await startConnection(page, w.profileName)

    const head = page.locator(HEAD)
    await expect(head).toBeVisible({ timeout: 1000 })
    await expect
      .poll(
        () =>
          page.evaluate(() => {
            const observations = (
              window as unknown as {
                __foundationHeadObservations: {
                  state: string
                  stage: string
                  text: string
                  visible: boolean
                }[]
              }
            ).__foundationHeadObservations
            return observations.some(
              (entry) =>
                entry.visible &&
                ((entry.state === 'opening' &&
                  /^(resolve|tcp|ssh-version|key-exchange|sign-in|shell)$/.test(entry.stage) &&
                  entry.text.includes('Connecting') &&
                  /resolving the route|reaching the host|waiting for the SSH answer|agreeing encryption keys|signing in|opening a shell/.test(
                    entry.text,
                  )) ||
                  (entry.state === 'connected' && entry.text.includes('Connected'))),
            )
          }),
        { timeout: 1000 },
      )
      .toBe(true)

    // The link recovers and THE SAME tab reaches its shell: a healthy open on
    // a healthy host ends at Connected, with a shell that runs a command.
    w.proxy.pass()
    await expect(head).toHaveAttribute('data-state', 'connected', { timeout: 60_000 })
    await expect(page.locator(EDITOR)).toBeVisible({ timeout: 45_000 })
    await runCommand(page, 'echo after-the-open')
    await expect(head).toContainText('Connected')
  } finally {
    await page.evaluate(() => {
      const observed = window as unknown as { __foundationHeadObserver?: MutationObserver }
      observed.__foundationHeadObserver?.disconnect()
    })
    await unwire(page, w)
  }
})

test('a lost connection sends nothing more and says so once (nocx-oalo1.5.1)', async ({
  page,
}, testInfo) => {
  test.setTimeout(180_000)
  const home = testInfo.outputPath('foundation-input-home')
  mkdirSync(home, { recursive: true })
  const frames = captureFrames(page)
  let w: Wired | null = null
  try {
    w = await wire(page, home)
    await openConnection(page, w.profileName)

    // THE HEALTHY HALF, first: typed input reaches a real shell.
    await runCommand(page, 'echo healthy-input-ok')

    // A program that asks the terminal for mouse reports. `sleep` keeps it in
    // the foreground; the DECSET sequences are what make the pane send pointer
    // intents WITHOUT anyone typing — the flood the owner saw.
    const editor = page.locator(EDITOR)
    await editor.click()
    await editor.pressSequentially("printf '\\033[?1000h\\033[?1006h'; sleep 300")
    await editor.press('Enter')
    await expect(page.locator('.pane.active .cmd-block-running').first()).toBeVisible({
      timeout: 30_000,
    })

    const grid = page.locator(GRID)
    await expect(grid).toBeVisible()
    const box = (await grid.boundingBox())!
    const moveMouse = async (): Promise<void> => {
      await page.mouse.move(box.x + box.width / 3, box.y + box.height / 3)
      await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2, { steps: 4 })
      await page.mouse.click(box.x + box.width / 2, box.y + box.height / 2)
    }

    // THE HEALTHY HALF, in the same place the loss is measured: input frames
    // reach the backend while the connection is usable. The command typed
    // above is that proof on the wire, so the after-loss assertion has a
    // measured baseline rather than a hope.
    await expect
      .poll(() => frames.filter((f) => f.includes('"method":"session.intent"')).length, {
        timeout: 10_000,
      })
      .toBeGreaterThan(0)
    // Pointer reports are staged too. Whether the emulator is in mouse mode at
    // this instant is the program's business, not the gate's; the loss below is
    // checked with the same gestures plus the keyboard.
    await moveMouse()

    // THE LOUD DEATH: every socket is destroyed, the backend ends the session
    // and offers the one action.
    w.proxy.cut()
    await expect
      .poll(
        async () => {
          const state = await page
            .locator(HEAD)
            .getAttribute('data-state')
            .catch(() => null)
          return state === 'failed' || state === 'ended' || state === 'reconnecting'
        },
        { timeout: 60_000 },
      )
      .toBe(true)
    await expect(page.locator(RECONNECT)).toBeVisible()

    // From this instant the pane is not usable. Anything the pointer or the
    // keyboard produces belongs to a connection that is gone.
    const before = frames.length
    await moveMouse()
    await page.keyboard.type('must-not-be-sent')
    await page.keyboard.press('Enter')

    // A SAME-SOCKET REQUEST IS ANSWERED AFTER THOSE FRAMES: the palette's
    // provider list travels the pane's own socket, so when the palette shows
    // its rows every input frame that was going to be written has been.
    await page.keyboard.press('Control+Shift+P')
    await expect(page.locator(QUICK_CONNECT)).toBeVisible({ timeout: 10_000 })
    await page.keyboard.press('Escape')

    const after = frames.slice(before)
    expect(
      after.filter((f) => f.includes('"method":"session.intent"')),
      'no input may reach the backend after the loss',
    ).toEqual([])

    // ONE NOTICE, and it is the loss itself: no "Input was refused" toast, and
    // no stack of them.
    await expect(page.getByText(/Input was refused/)).toHaveCount(0)
    await expect(page.locator(HEAD)).toHaveCount(1)
    await expect(page.getByText(/^The connection/)).toHaveCount(0)
  } finally {
    await unwire(page, w)
  }
})
