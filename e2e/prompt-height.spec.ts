/**
 * A prompt fits the screen and keeps its answers reachable (nocx-ck02x) —
 * verified where layout resolves.
 *
 * The owner hit this in the running app: an approval prompt for a long
 * `session.run` command ran past the bottom of the window with `Allow once`
 * the last thing visible and `Deny` off-screen entirely. A person cannot
 * answer a question they are required to answer, and the run stays suspended
 * until they do.
 *
 * The cause was in the kit, not in the approval surface. `.ui-prompt` capped
 * its WIDTH twice (560px floating, 760px top-sheet) and its height not at
 * all, while `.ui-prompt-overlay` is `position: fixed; inset: 0` with
 * `align-items: flex-start` — so a panel taller than the viewport simply grew
 * out of the bottom edge, taking its actions with it. Every top-sheet prompt
 * had this (vault unlock, connection password, key material, approval); the
 * approval prompt is only the one whose content has no bound.
 *
 * WHY THIS SPEC EXISTS AT ALL. The unit tests (frontend/src/ui/prompt.test.tsx)
 * assert the overflow arithmetic and that the measurement is wired to the
 * element the CSS paints. They run in jsdom, which computes no layout: there,
 * every element is 0×0, "the button is inside the viewport" is not a question
 * that can be asked, and a test asserting a class name would be a test of a
 * class name. The height cap is a flex chain — a `max-height`, a `min-height:
 * 0` middle and non-shrinking ends — and a flex chain is exactly what jsdom
 * cannot resolve.
 *
 * THE CSS IS READ FROM THE REAL STYLESHEET, never copied here, for the reason
 * e2e/scroll-ownership.spec.ts gives: a duplicated rule drifts silently and
 * the one test meant to prove the contract stops testing it.
 *
 * THE MARKUP, unlike the CSS, IS reproduced — this spec renders no Solid, so
 * it builds `.ui-prompt-overlay > .ui-prompt > (title, body, actions)` by
 * hand. That shape is pinned from the other side: prompt.test.tsx's "renders
 * the panel as title, body and actions, in that order" fails if the component
 * stops producing what is built below. A synthetic subject is also what
 * scroll-ownership.spec.ts uses, and for the same reason — the subject here
 * is the kit contract, not any one surface that happens to use it.
 *
 * WHAT THIS SPEC CANNOT PROVE, stated rather than dressed up: it does not run
 * the real approval prompt, so it cannot show that the real body's real
 * numbers reach `data-overflow-*` in the running app. It shows that the CSS
 * caps the panel, that the body is what scrolls, that the actions stay in the
 * viewport and reachable by keyboard, and that the fade paints from the
 * attribute the component sets.
 */

import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { test, expect, type Page } from './harness'

const dir = '../frontend/src/styles/'
const TOKENS_CSS = readFileSync(resolve(__dirname, dir + 'tokens.css'), 'utf8')
const THEME_CSS = readFileSync(resolve(__dirname, dir + 'themes/tokyo-night.css'), 'utf8')
const PROMPT_CSS = readFileSync(resolve(__dirname, dir + 'components/prompt.css'), 'utf8')

/** Only the framing the app normally supplies: no margin around the fixed
 *  overlay, and a readable base size for the paragraphs that make the body
 *  tall. Everything that governs the panel's box comes from prompt.css. */
const TESTBED_CSS = `
  body { margin: 0; font: 14px/1.4 system-ui, sans-serif; }
`

type Placement = 'floating' | 'top-sheet'

/** Paragraphs enough to run any viewport off the bottom several times over. */
function paragraphs(count: number): string {
  const out: string[] = []
  for (let i = 0; i < count; i++) {
    out.push(
      `<p id="p${i + 1}" style="margin:0">Fact ${i + 1} — what this command would reach, and where, spelled out at the length a real proposal reaches.</p>`,
    )
  }
  return out.join('\n')
}

/**
 * The panel as `frontend/src/ui/prompt.tsx` renders it.
 *
 * `capped` carries the child that made the shrink rule necessary: a CodeBlock
 * is `max-height: 200px; overflow: auto`, and a flex item with its own scroll
 * region has an automatic minimum size of zero — so inside a body that is now
 * height-constrained it would collapse to nothing instead of the body
 * scrolling. It is here so that collapse is a test failure.
 */
function promptHtml(opts: {
  placement: Placement
  facts: number
  capped?: boolean
  overflowMarks?: boolean
}): string {
  const marks = opts.overflowMarks === true ? ' data-overflow-start data-overflow-end' : ''
  const capped =
    opts.capped === true
      ? `<div id="capped" style="max-height:200px;overflow:auto;background:#222">${paragraphs(30)}</div>`
      : ''
  return `
    <html><head><style>
      ${TOKENS_CSS}
      ${THEME_CSS}
      ${PROMPT_CSS}
      ${TESTBED_CSS}
    </style></head><body>
      <div class="ui-prompt-overlay" data-placement="${opts.placement}">
        <section
          class="ui-prompt"
          data-placement="${opts.placement}"
          data-density="compact"
          role="dialog"
          aria-modal="true"
          aria-label="This action needs your approval"
        >
          <h2 class="ui-prompt__title" id="title">This action needs your approval</h2>
          <div class="ui-prompt__body" id="body"${marks}>
            ${capped}
            ${paragraphs(opts.facts)}
          </div>
          <div class="ui-prompt__actions" data-layout="stacked">
            <button type="button" id="allow">Allow once</button>
            <button type="button" id="deny">Deny once</button>
          </div>
        </section>
      </div>
    </body></html>
  `
}

type Box = { top: number; bottom: number; height: number }

async function boxOf(page: Page, selector: string): Promise<Box> {
  return page.evaluate((sel) => {
    const r = (document.querySelector(sel) as HTMLElement).getBoundingClientRect()
    return { top: r.top, bottom: r.bottom, height: r.height }
  }, selector)
}

async function viewportHeight(page: Page): Promise<number> {
  return page.evaluate(() => window.innerHeight)
}

/**
 * Wait for the panel's entrance animation to finish before measuring it.
 *
 * A top-sheet slides down from `translateY(-100%)` over 160ms, and a
 * transform moves what getBoundingClientRect reports. Measured while writing
 * this spec: WebKit read the top-sheet panel's bottom edge at y=0 and `Deny`
 * at y=-13 — the whole panel translated a full height up, which looks exactly
 * like the defect being fixed and is not it. Chromium happened to measure
 * after the animation had finished and agreed with the layout. So the wait is
 * on an OBSERVABLE state — every animation on the panel is finished — never on
 * a duration; a spec that slept 200ms here would be the timing dependency
 * AGENTS.md refuses.
 */
async function settled(page: Page): Promise<void> {
  await page.waitForFunction(() => {
    const panel = document.querySelector('.ui-prompt')
    if (!panel) return false
    if (typeof panel.getAnimations !== 'function') return true
    return panel.getAnimations().every((a) => a.playState === 'finished')
  })
}

/** Both placements get the same assertions: a spec that covered only
 *  `top-sheet` would leave half the kit broken, since `floating` has (had)
 *  the identical missing cap. */
const PLACEMENTS: Placement[] = ['top-sheet', 'floating']

test.describe('a prompt taller than the window (nocx-ck02x)', () => {
  test.use({ viewport: { width: 900, height: 600 } })

  for (const placement of PLACEMENTS) {
    test(`${placement}: the actions stay inside the viewport`, async ({ page }) => {
      await page.setContent(promptHtml({ placement, facts: 60 }))
      await settled(page)

      const height = await viewportHeight(page)
      const panel = await boxOf(page, '.ui-prompt')
      const actions = await boxOf(page, '.ui-prompt__actions')
      const deny = await boxOf(page, '#deny')

      // Against the viewport, never against a pixel count: the cap is
      // expressed in the overlay's own content box and must hold at whatever
      // size the window happens to be.
      expect(panel.bottom).toBeLessThanOrEqual(height + 1)
      expect(actions.bottom).toBeLessThanOrEqual(height + 1)
      expect(deny.bottom).toBeLessThanOrEqual(height + 1)
      expect(deny.top).toBeGreaterThanOrEqual(0)
      // The panel really is the tall case — otherwise the assertions above
      // would pass on a prompt that never needed capping.
      expect(panel.height).toBeGreaterThan(height / 2)
    })

    test(`${placement}: the body is what scrolls and the title stays put`, async ({ page }) => {
      await page.setContent(promptHtml({ placement, facts: 60 }))
      await settled(page)

      const overflow = await page.evaluate(() => {
        const body = document.getElementById('body') as HTMLElement
        return {
          overflowY: getComputedStyle(body).overflowY,
          scrollHeight: body.scrollHeight,
          clientHeight: body.clientHeight,
        }
      })
      expect(overflow.overflowY).toBe('auto')
      expect(overflow.scrollHeight).toBeGreaterThan(overflow.clientHeight)

      const titleBefore = await boxOf(page, '#title')
      const actionsBefore = await boxOf(page, '.ui-prompt__actions')

      const scrolled = await page.evaluate(() => {
        const body = document.getElementById('body') as HTMLElement
        body.scrollTop = body.scrollHeight
        return {
          bodyScrollTop: body.scrollTop,
          overlayScrollTop: (document.querySelector('.ui-prompt-overlay') as HTMLElement).scrollTop,
          documentScrollTop: document.scrollingElement?.scrollTop ?? 0,
        }
      })

      // The body moved, and nothing else did — a panel that scrolls the
      // document instead takes its title off the top of the screen.
      expect(scrolled.bodyScrollTop).toBeGreaterThan(0)
      expect(scrolled.overlayScrollTop).toBe(0)
      expect(scrolled.documentScrollTop).toBe(0)

      const titleAfter = await boxOf(page, '#title')
      const actionsAfter = await boxOf(page, '.ui-prompt__actions')
      expect(titleAfter.top).toBeCloseTo(titleBefore.top, 0)
      expect(actionsAfter.top).toBeCloseTo(actionsBefore.top, 0)

      // And the last fact can actually be brought into view — "the body has a
      // scrollbar" and "the reader can reach the end" are different claims.
      const lastVisible = await page.evaluate(() => {
        const body = document.getElementById('body') as HTMLElement
        const last = document.getElementById('p60') as HTMLElement
        return last.getBoundingClientRect().bottom <= body.getBoundingClientRect().bottom + 1
      })
      expect(lastVisible).toBe(true)
    })

    test(`${placement}: a scroll-capped child is not collapsed by the cap`, async ({ page }) => {
      await page.setContent(promptHtml({ placement, facts: 40, capped: true }))
      await settled(page)

      const capped = await boxOf(page, '#capped')
      // Its own max-height, not whatever was left over. A flex item with
      // `overflow: auto` has an automatic minimum size of zero, so without
      // the body's non-shrinking children this reads a few pixels.
      expect(capped.height).toBeGreaterThan(150)
    })

    test(`${placement}: a short prompt is unchanged`, async ({ page }) => {
      await page.setContent(promptHtml({ placement, facts: 3 }))
      await settled(page)

      const before = await page.evaluate(() => {
        const body = document.getElementById('body') as HTMLElement
        const panel = document.querySelector('.ui-prompt') as HTMLElement
        return {
          panelHeight: panel.getBoundingClientRect().height,
          scrollHeight: body.scrollHeight,
          clientHeight: body.clientHeight,
        }
      })

      // Nothing to scroll, so no scrollbar and no fade …
      expect(before.scrollHeight).toBeLessThanOrEqual(before.clientHeight + 1)

      // … and the panel is exactly as tall as it would be with no cap at all.
      const uncapped = await page.evaluate(() => {
        const panel = document.querySelector('.ui-prompt') as HTMLElement
        panel.style.maxHeight = 'none'
        return panel.getBoundingClientRect().height
      })
      expect(before.panelHeight).toBeCloseTo(uncapped, 0)
    })

    test(`${placement}: the last action is reachable by keyboard`, async ({ page }) => {
      await page.setContent(promptHtml({ placement, facts: 60 }))
      await settled(page)

      // Prompt puts the caret on the first enabled action (prompt.tsx
      // focusInitial, asserted in prompt.test.tsx); this spec starts where
      // that leaves the user and asks whether the rest is reachable without
      // a mouse.
      await page.evaluate(() => {
        ;(document.getElementById('allow') as HTMLButtonElement).focus()
        ;(document.getElementById('deny') as HTMLButtonElement).addEventListener('click', () => {
          ;(window as unknown as { denied?: boolean }).denied = true
        })
      })
      await page.keyboard.press('Tab')
      expect(await page.evaluate(() => document.activeElement?.id)).toBe('deny')

      // Focused AND on screen: a control the browser will focus but scroll
      // out of sight is the same defect wearing a keyboard.
      const deny = await boxOf(page, '#deny')
      const height = await viewportHeight(page)
      expect(deny.bottom).toBeLessThanOrEqual(height + 1)
      expect(deny.top).toBeGreaterThanOrEqual(0)

      await page.keyboard.press('Enter')
      expect(await page.evaluate(() => (window as unknown as { denied?: boolean }).denied)).toBe(
        true,
      )
    })
  }

  // ── Discoverability ───────────────────────────────────────────────────
  // The fade is painted from the attributes prompt.tsx measures, the way the
  // tab strip's is (tab-strip.css): an edge that says "there is more" when
  // there is not cannot be checked by looking, so it must be measured. The
  // arithmetic and the wiring are unit-tested; what a browser adds is that
  // the rule matches and resolves to a real mask.

  test('the fade is painted only where the body is actually cut', async ({ page }) => {
    await page.setContent(promptHtml({ placement: 'top-sheet', facts: 60, overflowMarks: true }))
    await settled(page)

    const masked = await page.evaluate(() => {
      const body = document.getElementById('body') as HTMLElement
      const both = getComputedStyle(body).maskImage
      body.removeAttribute('data-overflow-start')
      const endOnly = getComputedStyle(body).maskImage
      body.removeAttribute('data-overflow-end')
      const none = getComputedStyle(body).maskImage
      return { both, endOnly, none }
    })

    expect(masked.both).toContain('gradient')
    expect(masked.endOnly).toContain('gradient')
    expect(masked.both).not.toBe(masked.endOnly)
    expect(masked.none).toBe('none')
  })
})
