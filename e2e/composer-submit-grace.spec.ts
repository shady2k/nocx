import { appReadyForInput, promptReady, test, expect } from './harness'
import type { Page, TestInfo } from '@playwright/test'

/**
 * The composer leaves only when the submitted command's own block outlived the
 * grace (nocx-j9v81, ADR-tqjt5rkk).
 *
 * The keyboard is the program's from the submit itself (ADR-0004 §1: the
 * hand-off releases it synchronously), so the only thing that waits is what is
 * DRAWN. These cases read that off a record: every DOM mutation and every
 * animation frame between the keystroke and the settled prompt contributes a
 * state — display, ink, height, inertness, the departure ghosts, and the
 * composer's own text.
 *
 * THE THRESHOLD IS THE OWNER'S 40 ms AND A MACHINE IS NOT ALLOWED TO ARGUE WITH
 * IT. So the quick case does not assert an absolute outcome; it reads the
 * length the block itself printed — the figure the product decided on — and
 * asserts the rule's two implications: a block at or over the grace always
 * leaves, and a composer that stayed means its block did not outlive the grace.
 * The third combination — a quick block whose completion reached the renderer
 * after the grace, so the composer left and came back — is the accepted price
 * the record states, and it is the machine's call, not the rule's. The case
 * that must never depend on any of that is `sleep 0.2`: a fifth of a second
 * cannot be a quick command on any machine.
 */
const TITLE = '.nocx-tab-title'
const COMPOSER = '.pane.active .nocx-editor'
const INPUT = '.pane.active .nocx-editor-input'
const BLOCKS = '.pane.active .scrollback-inner > .cmd-block[data-block-kind="command"]'

/** The owner's threshold, named here because this spec is the second reader of
 *  one decision: the product's own `SUBMIT_GRACE_MS` lives in
 *  `frontend/src/terminal-content.ts`, which an e2e cannot import (it pulls the
 *  renderer in), and the unit tests pin that constant to 40. */
const GRACE_MS = 40

interface State {
  /** Milliseconds since the recording started. */
  at: number
  from: 'mutation' | 'frame' | 'key'
  /** The key a `key` record is about, '' otherwise. */
  key: string
  /** How many command blocks existed when this state was read. */
  blocks: number
  /** The last block's outcome attribute, '-' while it is still running. */
  outcome: string
  display: string
  opacity: number
  height: number
  inert: boolean
  ghosts: number
  /** The composer's own text — what a leaked keystroke would appear in. */
  doc: string
  /** The last block's printed length, exact (`title` of its duration meta), or
   *  null while it is still running — so this is also the moment the block's
   *  LOGICAL freeze landed, which is the completion fact. */
  printed: string | null
  /** Whether the composer was on screen when the state was read. */
  drawn: boolean
}

interface Recording {
  states: State[]
  read: () => State
}

declare global {
  interface Window {
    __grace: Recording
  }
}

/**
 * Put the page in front of a settled prompt, with a WARM shell and no fade in
 * flight, and answer how many command blocks were already there.
 *
 * The warm-up is part of the measurement rather than convenience: the first
 * command in a fresh page pays the shell's own start, and this spec is about
 * what happens after a command that returns quickly. The composer's own startup
 * return fade is waited out for the same reason: an opacity assertion taken
 * during it would mean nothing.
 */
async function openWarmPrompt(page: Page): Promise<number> {
  await page.goto('/')
  await expect(page.locator(TITLE).first(), 'initial tab has a title').not.toHaveText('', {
    timeout: 15000,
  })
  await appReadyForInput(page)
  await expect(page.locator(INPUT), 'composer owns the prompt').toBeVisible({ timeout: 8000 })
  await expect
    .poll(
      () =>
        page.locator(COMPOSER).evaluate((editor) => ({
          opacity: Number(getComputedStyle(editor).opacity),
          active: editor.getAnimations().length,
        })),
      { message: 'the composer is at full ink with nothing animating before the submit' },
    )
    .toEqual({ opacity: 1, active: 0 })

  const count = await page.locator(BLOCKS).count()
  for (let i = 0; i < 2; i++) {
    await page.locator(INPUT).fill('true')
    await page.keyboard.press('Enter')
    await expect(page.locator(BLOCKS), 'the warm-up command opened its block').toHaveCount(
      count + i + 1,
      { timeout: 30000 },
    )
    await expect(page.locator(BLOCKS).last(), 'the warm-up command settled').toHaveAttribute(
      'data-outcome',
      'success',
      { timeout: 30000 },
    )
    await promptReady(page)
  }
  // A returning composer fades in over 140 ms, and a record taken inside that
  // fade would report a dip the measured command had nothing to do with: the
  // LAST warm-up's return has to finish before anything is measured.
  await expect
    .poll(
      () =>
        page.locator(COMPOSER).evaluate((editor) => ({
          opacity: Number(getComputedStyle(editor).opacity),
          active: editor.getAnimations().length,
        })),
      { message: 'the composer is at full ink with nothing animating before the measurement' },
    )
    .toEqual({ opacity: 1, active: 0 })
  return count + 2
}

/** Start recording the composer's presentation and the command blocks beside
 *  it. Only a change is stored, so a settle that repeats one state for a
 *  hundred frames costs one entry; a key is always stored. */
async function record(page: Page): Promise<void> {
  await page.evaluate(() => {
    const pane = document.querySelector('.pane.active') as HTMLElement
    const editor = pane.querySelector('.nocx-editor') as HTMLElement
    const content = pane.querySelector('.nocx-editor-input') as HTMLElement
    const started = performance.now()
    const states: State[] = []
    let last = ''
    const read = (): State => {
      const blocks = pane.querySelectorAll(
        '.scrollback-inner > .cmd-block[data-block-kind="command"]',
      )
      const block = blocks[blocks.length - 1] as HTMLElement | undefined
      const duration = block?.querySelector('.cmd-header-right [data-column="duration"]')
      return {
        at: Math.round((performance.now() - started) * 10) / 10,
        from: 'frame',
        key: '',
        blocks: blocks.length,
        outcome: block?.dataset.outcome ?? '-',
        printed: duration?.getAttribute('title') ?? null,
        display: getComputedStyle(editor).display,
        opacity: Number(getComputedStyle(editor).opacity),
        height: editor.getBoundingClientRect().height,
        inert: editor.hasAttribute('inert'),
        ghosts: document.querySelectorAll('[data-nocx-motion-ghost]').length,
        doc: content.textContent ?? '',
        drawn: getComputedStyle(editor).display !== 'none',
      }
    }
    const snap = (from: State['from'], key = ''): void => {
      const state = { ...read(), from, key }
      const identity = JSON.stringify({ ...state, at: 0, from: '', key: '' })
      if (from !== 'key' && identity === last) return
      last = identity
      states.push(state)
    }
    window.__grace = { states, read: () => read() }
    document.addEventListener('keydown', (event) => snap('key', event.key), true)
    new MutationObserver(() => snap('mutation')).observe(pane, {
      attributes: true,
      subtree: true,
      childList: true,
      characterData: true,
    })
    const frame = (): void => {
      snap('frame')
      requestAnimationFrame(frame)
    }
    requestAnimationFrame(frame)
  })
}

/** The length the last command block prints for itself, in milliseconds — the
 *  figure the header shows, and the same number the product decides on. Null
 *  while the block has not frozen (a running block's title ticks). */
async function printedBlockMs(page: Page): Promise<number | null> {
  return page.evaluate(() => {
    const blocks = document.querySelectorAll(
      '.pane.active .scrollback-inner > .cmd-block[data-block-kind="command"]',
    )
    const block = blocks[blocks.length - 1] as HTMLElement | undefined
    const meta = block?.querySelector('.cmd-header-right [data-column="duration"]')
    const match = /^(\d+)ms$/.exec(meta?.getAttribute('title') ?? '')
    return match === null ? null : Number(match[1])
  })
}

async function states(page: Page, testInfo: TestInfo): Promise<State[]> {
  const recorded = await page.evaluate(() => window.__grace.states)
  await testInfo.attach('composer-grace-records', {
    body: JSON.stringify(recorded),
    contentType: 'application/json',
  })
  return recorded
}

/** Every state in which the composer was not fully drawn where it belongs:
 *  removed from the layout, faded below full ink, or an ink ghost of itself
 *  left behind. */
function absent(recorded: State[]): State[] {
  return recorded.filter(
    (state) =>
      state.display === 'none' || state.height < 1 || state.opacity < 0.99 || state.ghosts > 0,
  )
}

function timeline(recorded: State[]): string {
  return JSON.stringify(
    recorded.map(
      (state) =>
        `${state.at} ${state.from}${state.key ? `:${state.key}` : ''} ${state.display} o=${state.opacity} h=${Math.round(state.height)} i=${state.inert ? 'I' : '-'} g=${state.ghosts} n=${state.blocks}/${state.outcome} doc=${JSON.stringify(state.doc)}`,
    ),
  )
}

test.describe('the submit grace (nocx-j9v81, ADR-tqjt5rkk)', () => {
  test('a quick command: the composer follows the length the block itself printed', async ({
    page,
  }, testInfo) => {
    const settled = await openWarmPrompt(page)
    await record(page)
    await page.keyboard.type('true')
    await page.keyboard.press('Enter')

    await expect(page.locator(BLOCKS), 'the command opened its block').toHaveCount(settled + 1, {
      timeout: 30000,
    })
    await expect(
      page.locator(BLOCKS).last(),
      'the command reached its success outcome',
    ).toHaveAttribute('data-outcome', 'success', { timeout: 30000 })
    await expect
      .poll(() => printedBlockMs(page), { message: 'the block prints its own length' })
      .not.toBeNull()
    const blockMs = await printedBlockMs(page)
    await promptReady(page)

    const recorded = await states(page, testInfo)
    const dump = timeline(recorded)
    // The record has to have watched the command: its block existed while the
    // composer had already given its keys up. Without that, an empty record
    // would satisfy every assertion below.
    expect(
      recorded.some((state) => state.blocks > settled && state.inert),
      `wanted the command's block on screen with the composer's keys given up; saw ${dump}`,
    ).toBe(true)

    // THE RULE, read off the record and off the length the block printed. Two
    // implications, both the product's own, and neither needs a clock:
    //
    //   - a block at or over the grace always leaves: the grace is the
    //     deadline, and a completion that long departs too;
    //   - a composer that STAYED means the block did not outlive the grace.
    //
    // The other direction — a quick block whose completion arrived after the
    // grace leaves and comes back — is the accepted price (ADR-tqjt5rkk) and
    // is the machine's call, so it is not asserted here.
    const stayed = absent(recorded).length === 0
    expect(blockMs, `wanted the block's own length; saw ${dump}`).not.toBeNull()
    if ((blockMs as number) >= GRACE_MS + 1) {
      expect(
        recorded.some((state) => state.display === 'none'),
        `the block printed ${blockMs} ms — over the ${GRACE_MS} ms grace — so the composer must leave; saw ${dump}`,
      ).toBe(true)
    }
    if (stayed) {
      expect(
        (blockMs as number) <= GRACE_MS - 1,
        `the composer stayed in every frame, so its block must not have outlived the ${GRACE_MS} ms grace; saw ${dump}`,
      ).toBe(true)
    }
  })

  test('a command that outlives the grace always takes the composer down', async ({
    page,
  }, testInfo) => {
    const settled = await openWarmPrompt(page)
    await record(page)
    await page.locator(INPUT).fill('sleep 0.2')
    await page.keyboard.press('Enter')

    // The hide is the observable, read off the RECORD rather than off the live
    // element: the record only grows, so a hide that the prompt has already
    // undone is still evidence that it happened.
    await expect
      .poll(
        () => page.evaluate(() => window.__grace.states.some((state) => state.display === 'none')),
        { message: 'a command of a fifth of a second takes the composer down', timeout: 30000 },
      )
      .toBe(true)

    await expect(page.locator(BLOCKS), 'the command opened its block').toHaveCount(settled + 1, {
      timeout: 30000,
    })
    await expect(
      page.locator(BLOCKS).last(),
      'the command reached its success outcome',
    ).toHaveAttribute('data-outcome', 'success', { timeout: 30000 })
    const blockMs = await printedBlockMs(page)

    const recorded = await states(page, testInfo)
    const dump = timeline(recorded)
    expect(
      blockMs,
      `wanted a block clearly longer than the grace; saw ${dump}`,
    ).toBeGreaterThanOrEqual(GRACE_MS + 1)
    const given = recorded.findIndex((state) => state.inert)
    const hidden = recorded.findIndex((state) => state.display === 'none')
    expect(given, `wanted the submit to give the keys up; saw ${dump}`).toBeGreaterThanOrEqual(0)
    expect(hidden, `wanted the departure recorded; saw ${dump}`).toBeGreaterThan(given)
    // The box was drawn while the command was already running: the departure
    // waits for the command's own length rather than for the keystroke.
    expect(
      recorded.some((state) => state.blocks > settled && state.display !== 'none'),
      `wanted the composer drawn while the command ran; saw ${dump}`,
    ).toBe(true)

    // And the prompt brings it back, focused.
    await promptReady(page)
  })

  test('a key typed while the composer waits reaches the program and never the composer', async ({
    page,
  }, testInfo) => {
    await openWarmPrompt(page)
    await record(page)
    await page.locator(INPUT).fill('read v; echo grace-$v')
    await page.keyboard.press('Enter')
    // Immediately, while the box is still on screen and the program has owned
    // the keyboard since the submit: the shape of a leak is a letter in the
    // composer, and the shape of the fix is the letter in `read`'s variable.
    await page.keyboard.type('q')
    await page.keyboard.press('Enter')

    await expect(
      page.locator(BLOCKS).last().locator('.cmd-output'),
      'the program read the key typed while the composer waited',
    ).toContainText('grace-q', { timeout: 30000 })
    await promptReady(page)

    const recorded = await states(page, testInfo)
    const dump = timeline(recorded)
    // The command's own characters are typed before Enter; only what follows
    // the submit can be a leak — and the read's answering Enter is the second
    // one, so the letters after the FIRST are the ones to read.
    const enters = recorded
      .map((state, index) => ({ state, index }))
      .filter((entry) => entry.state.from === 'key' && entry.state.key === 'Enter')
    expect(
      enters.length,
      `wanted the submit and the answer Enter; saw ${dump}`,
    ).toBeGreaterThanOrEqual(2)
    const afterSubmit = recorded.slice(enters[0].index + 1)
    const typed = afterSubmit.filter((state) => state.from === 'key' && state.key === 'q')
    expect(typed.length, `wanted the one typed key recorded; saw ${dump}`).toBe(1)
    // THE KEY'S OWN MOMENT, and what must be true of it either way: the box was
    // still drawn (the grace had not run out — the case this is about) or the
    // grace had already taken it. A key typed in a 40 ms window is a race the
    // machine can lose; a key that landed in the COMPOSER is the defect.
    const drawn = typed[0].drawn
    const departed = recorded.some((state) => state.display === 'none' && state.at <= typed[0].at)
    expect(
      drawn || departed,
      `wanted the key at a moment the composer was drawn or already gone — never in between; saw ${dump}`,
    ).toBe(true)
    expect(typed[0].doc, `wanted the key to reach nothing in the composer; saw ${dump}`).toBe('')
    expect(
      afterSubmit.filter((state) => state.doc.includes('q')),
      `wanted no keystroke typed while the composer waited to land in it; saw ${dump}`,
    ).toEqual([])
  })
})
