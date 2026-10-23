import { appReadyForInput, test, expect } from './harness'

/**
 * nocx-1ebrs.40 — the composer flickers on submit.
 *
 * The submit hand-off must not blink the input panel. From painted frames
 * (one sample per animation frame across the hand-off) this asserts the
 * shape the fix is:
 *
 *  1. the pane-level geometry the grid and the pty are sized from never
 *     animates — the box's own height is always its full height or gone
 *     (display:none), never anything between;
 *  2. the INK fades — the departure's ink passes through intermediate
 *     opacities on the ghost that replaces the box on screen (the box itself
 *     leaves the layout in one frame — that is what keeps every fit number
 *     and every visible-waiting consumer honest), and the return fades in on
 *     the panel;
 *  3. the tail never detaches — while the person is following, content does
 *     not stay below the fold across frames, so no late pin lands as a jump.
 */

const TITLE = '.nocx-tab-title'
const INPUT = '.pane.active .nocx-editor-input'

interface Frame {
  t: number
  disp: string
  eH: number
  op: number
  st: number
  ch: number
  sh: number
  iT: number
  gl: number
  /** Active inner-element Web Animations API durations, in milliseconds. */
  glDurations: number[]
  /** The departure ghost's opacity, or -1 where there is no ghost this
   *  frame. The box's own departure is one frame (that is the fix); the
   *  ink the person sees fade is on this copy. */
  gOp: number
}

async function sampleAcrossSubmit(page: import('@playwright/test').Page, command: string) {
  await page.goto('/')
  await expect(page.locator(TITLE).first()).not.toHaveText('', { timeout: 15000 })
  await appReadyForInput(page)
  const input = page.locator(INPUT)
  await expect(input).toBeVisible({ timeout: 8000 })
  await input.fill(command)

  await page.evaluate(() => {
    const pane = document.querySelector('.pane.active') as HTMLElement
    const editor = pane.querySelector<HTMLElement>('.nocx-editor') as HTMLElement
    const area = pane.querySelector<HTMLElement>('.scrollback-area') as HTMLElement
    const inner = pane.querySelector<HTMLElement>('.scrollback-inner') as HTMLElement
    const w = window as unknown as { __frames: unknown[]; __t0: number; __stop?: boolean }
    w.__frames = []
    w.__t0 = performance.now()
    const sample = (): void => {
      if (w.__stop) return
      const ghost = document.querySelector('[data-nocx-motion-ghost]')
      w.__frames.push({
        t: +(performance.now() - w.__t0).toFixed(1),
        disp: getComputedStyle(editor).display,
        eH: +editor.getBoundingClientRect().height.toFixed(1),
        op: +getComputedStyle(editor).opacity.slice(0, 5),
        st: +area.scrollTop.toFixed(1),
        ch: area.clientHeight,
        sh: area.scrollHeight,
        iT: +inner.getBoundingClientRect().top.toFixed(1),
        gl: inner.getAnimations().length,
        glDurations: inner
          .getAnimations()
          .filter((animation) => animation.playState === 'running')
          .map((animation) => animation.effect?.getComputedTiming().duration ?? Number.NaN),
        gOp: ghost ? +getComputedStyle(ghost).opacity.slice(0, 5) : -1,
      })
      requestAnimationFrame(sample)
    }
    requestAnimationFrame(sample)
  })

  // The panel's resting height and ink, read before anything moves.
  const fullH = await page.evaluate(
    () => document.querySelector('.pane.active .nocx-editor')?.getBoundingClientRect().height ?? 0,
  )

  // An idle page paints on its own clock: without this wait the sampler's
  // first tick can land after the submit (measured: a first frame already
  // display:none, t=21ms), and the visible phase the assertions read from
  // never existed in `frames` at all.
  await page.waitForFunction(
    () =>
      ((window as unknown as { __frames: Frame[] }).__frames ?? []).some(
        (f) => f.disp !== 'none' && f.eH > 1,
      ),
    undefined,
    { timeout: 15_000 },
  )

  await page.keyboard.press('Enter')

  // The hand-back: the box is back at its full height and its full ink. NOT
  // toBeVisible — a mid-fade box is non-empty and Playwright-visible.
  await page.waitForFunction(
    (full) => {
      const ed = document.querySelector('.pane.active .nocx-editor')
      return (
        ed !== null &&
        getComputedStyle(ed).display !== 'none' &&
        ed.getBoundingClientRect().height >= full * 0.9 &&
        Number(getComputedStyle(ed).opacity) >= 0.95
      )
    },
    fullH,
    { timeout: 15000 },
  )
  await page.waitForFunction(
    () =>
      (document.querySelector('.pane.active .scrollback-inner')?.getAnimations().length ?? 0) === 0,
  )
  await page.evaluate(() => {
    const w = window as unknown as { __stop?: boolean }
    requestAnimationFrame(() => {
      w.__stop = true
    })
  })
  await page.waitForTimeout(120)
  return {
    fullH,
    frames: (await page.evaluate(
      () => (window as unknown as { __frames: unknown[] }).__frames,
    )) as Frame[],
  }
}

test.describe('the submit hand-off (nocx-1ebrs.40)', () => {
  test('a quick command dips the panel and turns it around; the geometry never animates', async ({
    page,
  }, testInfo) => {
    // The owner's case: a command that finishes while the departure is still
    // on screen. The panel must dim and come back — never vanish, never pop.
    const { fullH, frames } = await sampleAcrossSubmit(page, 'true')
    const dump = JSON.stringify(frames)
    await testInfo.attach('frames', { body: dump, contentType: 'application/json' })
    expect(frames.length, `frames sampled: ${dump}`).toBeGreaterThan(6)

    // 1. The box's own height is full or gone, never between: the pane-level
    // geometry the grid and the pty are fitted from does not animate.
    const midHeight = frames.filter((f) => f.eH > 1 && f.eH < fullH - 1)
    expect(
      midHeight,
      `box at intermediate heights: ${JSON.stringify(midHeight)}; all: ${dump}`,
    ).toEqual([])

    // 2. The ink moved gradually and came back. On a THROTTLED clock
    // (headless WebKit paints every ~80-150ms) a 140ms fade yields one or
    // two samples, so no single sampled threshold is honest; what holds on
    // every frame budget is: a mid-fade ink state EXISTS somewhere in the
    // hand-off — on the panel or on the ghost — where the old code painted
    // full ink or nothing at all. The ghost's decay across samples is the
    // second witness; either settles it.
    const ghostOps = frames.filter((f) => f.gOp >= 0).map((f) => f.gOp)
    const ghostDecayed = ghostOps.length >= 2 && Math.max(...ghostOps) - Math.min(...ghostOps) > 0.3
    const dipped =
      frames.some((f) => f.disp !== 'none' && f.op < 1) ||
      frames.some((f) => f.gOp > 0 && f.gOp < 1) ||
      ghostDecayed
    expect(dipped, `panel ink dipped during the hand-off: ${dump}`).toBe(true)
    const last = frames[frames.length - 1]
    expect(last.op, `panel back at full ink: ${dump}`).toBeGreaterThanOrEqual(0.95)
    expect(last.eH, `panel back at full height: ${dump}`).toBeGreaterThanOrEqual(fullH - 1)
  })

  test('a real command departs through a fade, and the tail never detaches', async ({
    page,
  }, testInfo) => {
    // Long enough that the departure completes and the panel is genuinely
    // gone for a while — the shape the owner sees on any ordinary command.
    const { fullH, frames } = await sampleAcrossSubmit(page, 'sleep 0.4')
    const dump = JSON.stringify(frames)
    await testInfo.attach('frames', { body: dump, contentType: 'application/json' })
    expect(frames.length, `frames sampled: ${dump}`).toBeGreaterThan(6)

    // 1. Two-valued layout height, all the way through.
    const midHeight = frames.filter((f) => f.eH > 1 && f.eH < fullH - 1)
    expect(
      midHeight,
      `box at intermediate heights: ${JSON.stringify(midHeight)}; all: ${dump}`,
    ).toEqual([])

    // 2. The departure: the box leaves the layout in ONE frame — that is the
    // fix, it is what keeps every fit number and every consumer honest — and
    // the INK the person sees fade belongs to the ghost that replaces it on
    // screen. The first cut faded the box itself and kept it in the flow for
    // 140ms; a submit helper then typed into that dying window and the keys
    // fell through to the pty, unrecorded (nocxify-journey, run 39592338852).
    // So: the box steps visible → gone (asserted by `absent` below landing
    // with no intermediate-height, mid-ink running state), and the ghost
    // passes through intermediate ink before it is gone.
    let submitAt = frames.findIndex((f) => f.disp === 'none' || f.op < 0.95 || f.eH < fullH - 1)
    if (submitAt < 0) submitAt = frames.length - 1
    const absent = frames.map((f, i) => ({ f, i })).filter(({ f }) => f.disp === 'none')
    expect(absent.length, `panel fully departs: ${dump}`).toBeGreaterThan(0)
    const lastFullInk = frames
      .slice(0, absent[0].i)
      .map((f, i) => ({ f, i }))
      .filter(({ f }) => f.op >= 0.95 && f.eH >= fullH - 1)
      .pop()
    expect(lastFullInk, `panel was full before it left: ${dump}`).toBeTruthy()
    const boxSteppedOut = frames.slice(lastFullInk!.i + 1, absent[0].i + 1).every((f) => f.eH < 1)
    expect(boxSteppedOut, `box leaves in one frame, not a shrink: ${dump}`).toBe(true)
    // Same throttle rule as test 1: a sampled ghost strictly between full
    // ink and none, or two ghost samples that decayed — the fast-out easing
    // spends almost no time mid-range on a slow clock, so a strict window
    // would flake on the sample that caught the fade's tail.
    const ghostOps2 = frames.filter((f) => f.gOp >= 0).map((f) => f.gOp)
    const ghostDecayed2 =
      ghostOps2.length >= 2 && Math.max(...ghostOps2) - Math.min(...ghostOps2) > 0.3
    const ghostFaded = frames.some((f) => f.gOp > 0 && f.gOp < 1) || ghostDecayed2
    expect(ghostFaded, `departure passes through intermediate ghost ink: ${dump}`).toBe(true)

    // And the return fades back in from nothing rather than popping.
    const lastAbsent = absent[absent.length - 1].i
    const restoredAt = frames.findIndex(
      (f, i) => i > lastAbsent && f.eH >= fullH - 1 && f.op >= 0.95,
    )
    expect(restoredAt, `panel returns at full ink: ${dump}`).toBeGreaterThan(0)
    // And the ghost does not outlive the hand-off: once the box is back at
    // its full ink, no later frame still carries one (show() cancels it).
    const ghostAfterReturn = frames.some((f, i) => i > restoredAt && f.gOp >= 0)
    expect(ghostAfterReturn, `ghost outlived the return: ${dump}`).toBe(false)
    // The return's mid-flight ink: on a throttled clock the window between
    // the last absent frame and the restored one can hold no sample at all,
    // so the witness is page-wide — some visible frame at LESS than full
    // ink (impossible without a fade; the old code painted exactly 1 or was
    // gone) — while `restoredAt` stays the strict arrival assertion.
    const fadeIn =
      frames.slice(lastAbsent + 1, restoredAt).some((f) => f.disp !== 'none' && f.op < 1) ||
      frames.some((f) => f.disp !== 'none' && f.op < 1)
    expect(fadeIn, `return passes through intermediate ink: ${dump}`).toBe(true)

    // 3. The tail never detaches while the panel is gone: content below the
    // fold on the ABSENT frames. The old code let ~130px accumulate there
    // (echo + process bar, nothing re-pinning) and sat on it for frames
    // before a late pin snapped scrollTop 98px in one frame. After the
    // panel is back the hand-off settle owns the transition and the idle
    // re-fit drains the content glided — the fold metric decays without a
    // scroll — so the drain is asserted by its painted glide below, not
    // here. (A per-frame bound would fail on main itself; see assertion 4.)
    const belowAbsent = frames
      .slice(submitAt + 1)
      .map((f) => ({ disp: f.disp, below: +(f.sh - f.st - f.ch).toFixed(1) }))
      .filter(({ disp }) => disp === 'none')
    const stuck = belowAbsent.filter(({ below: b }, i) => {
      if (b <= 2) return false
      const next = belowAbsent[i + 1]
      return next !== undefined && next.below > 2
    })
    expect(
      stuck,
      `fold stayed detached while the panel was gone (px): ${JSON.stringify(stuck)}; all: ${dump}`,
    ).toEqual([])

    // 4. Scroll stability is a user-visible end state, not a count of how
    // many intermediate positions happened to land on the sampler's clock.
    // CI Chromium shard 2 (run 39830472460, job 120005304954) sampled only
    // two distinct positions over eleven frames. The animation can still
    // reach its settled state correctly; requiring four samples made frame
    // scheduling part of the contract. sampleAcrossSubmit waits until the
    // inner element has no active animations, then records another frame.
    // Its last two painted positions must therefore agree: no late scroll
    // jump after the hand-off has settled.
    // The composer can become fully opaque before the settle glide finishes.
    // Keep sampling through animation completion, not only through restoredAt.
    const restoredGlideFrames = frames
      .slice(lastAbsent + 1)
      .filter((frame) =>
        frame.glDurations.some((duration) => Number.isFinite(duration) && duration > 0),
      )
    expect(
      restoredGlideFrames.length,
      `scrollbackInner restore glide ran with finite nonzero duration: ${dump}`,
    ).toBeGreaterThan(0)
    const restoredPositions = new Set(restoredGlideFrames.map((frame) => Math.round(frame.iT)))
    expect(
      restoredPositions.size,
      `restore painted changing positions while the glide was running: ${dump}`,
    ).toBeGreaterThan(1)
    const settledPair = frames.slice(-2)
    expect(settledPair.length, `two settled frames were recorded: ${dump}`).toBe(2)
    expect(
      Math.abs(settledPair[1].iT - settledPair[0].iT),
      `scroll position changed after the hand-off settled: ${JSON.stringify(settledPair)}; all: ${dump}`,
    ).toBeLessThanOrEqual(1)
    // And the hand-off ends with the tail where the reader can see it.
    const lastBelow = +(
      frames[frames.length - 1].sh -
      frames[frames.length - 1].st -
      frames[frames.length - 1].ch
    ).toFixed(1)
    expect(lastBelow, `fold at rest: ${dump}`).toBeLessThanOrEqual(2)
  })
})
