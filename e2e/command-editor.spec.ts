import { test, expect } from './harness' // shared Wails WS-port shim for headless CI

const TITLE = '.nocx-tab-title'
const EDITOR = '.nocx-editor'
const INPUT = '.nocx-editor-input'

async function waitForPrompt(page: import('@playwright/test').Page) {
  await page.goto('/')
  await expect(page.locator(TITLE).first()).not.toHaveText('', {
    timeout: 15000,
  })
}

/**
 * Where a word of the command line actually is, in page coordinates.
 *
 * The editor's content is text inside a contenteditable, so a word is not an
 * element and no locator can name one. A Range can: walk the text nodes CM6
 * rendered, map the word's offset into whichever node holds it, and measure it.
 * That is the anchor — the coordinate is derived from it rather than assumed.
 *
 * CM6 splits a line across several text nodes when it decorates the syntax, so
 * this walks nodes and accumulates rather than reading `textContent` once.
 */
async function wordCenter(
  page: import('@playwright/test').Page,
  word: string,
): Promise<{ x: number; y: number }> {
  const rect = await page.evaluate((needle) => {
    const input = document.querySelector('.nocx-editor-input')
    if (input === null) throw new Error('editor input not in the document')

    const walker = document.createTreeWalker(input, NodeFilter.SHOW_TEXT)
    let consumed = 0
    const start = (input.textContent ?? '').indexOf(needle)
    if (start < 0) throw new Error(`the editor does not contain ${needle}`)

    for (let node = walker.nextNode(); node !== null; node = walker.nextNode()) {
      const len = node.textContent?.length ?? 0
      // The word has to sit inside ONE text node for a Range to measure it;
      // a word split across two would mean CM6 decorated part of it, and
      // silently measuring the fragment is how a fragile test comes back.
      if (start >= consumed && start + needle.length <= consumed + len) {
        const range = document.createRange()
        range.setStart(node, start - consumed)
        range.setEnd(node, start - consumed + needle.length)
        const r = range.getBoundingClientRect()
        return { x: r.x, y: r.y, width: r.width, height: r.height }
      }
      consumed += len
    }
    throw new Error(`${needle} spans more than one text node — cannot measure it`)
  }, word)

  if (rect.width === 0 || rect.height === 0) {
    throw new Error(`${word} measured zero — the editor is not laid out yet`)
  }
  return { x: rect.x + rect.width / 2, y: rect.y + rect.height / 2 }
}

test.describe('command editor (nocx-4ff)', () => {
  // A clean local prompt owns input immediately — the editor must not wait for a
  // command to run first. Regression for the spurious OSC 133 C emitted while
  // nocx.bash was being sourced, which left the first prompt untrusted.
  test('editor is visible at the first prompt', async ({ page }) => {
    await waitForPrompt(page)
    await expect(page.locator(EDITOR)).toBeVisible({ timeout: 8000 })
  })

  // Regression for the WebGL link-layer canvas (z-index:2) that won hit-testing
  // over the editor, so every click, caret move and word-select landed on the
  // terminal canvas.
  //
  // This comment used to say "the editor sits at z-index:20 above every xterm
  // layer". It does not, and never did in this era: .nocx-editor has no z-index
  // at all, and nothing in the project sets 20. What actually keeps the two
  // apart is geometry — the link canvas lives inside .xterm-live-container, a
  // separate flex row with overflow:hidden that is zero pixels tall when idle,
  // so the two never overlap (nocx-0oc, and
  // .internal/reports/2026-08-01-editor-stacking-and-test-surface.md).
  //
  // The test is kept anyway, and asserts the property rather than the mechanism:
  // the point over the input surface belongs to the editor. That stays true if
  // the layout changes again, which a z-index assertion would not.
  test('mouse hit-tests the editor surface, not the terminal canvas', async ({ page }) => {
    await waitForPrompt(page)
    await expect(page.locator(EDITOR)).toBeVisible({ timeout: 8000 })
    await page.locator(INPUT).fill('echo hello world foobar')

    // The input surface is CM6's contenteditable contentDOM now (ADR-0010),
    // not a textarea. What the regression is about is the EDITOR winning the
    // point — the link layer stealing it made the editor unclickable — so
    // assert the hit lands inside .nocx-editor rather than asserting a tag.
    const hitInsideEditor = await page.evaluate(() => {
      const editor = document.querySelector('.nocx-editor') as HTMLElement
      const el = document.querySelector('.nocx-editor-input') as HTMLElement
      const r = el.getBoundingClientRect()
      const hit = document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2)
      return hit !== null && editor.contains(hit)
    })
    expect(hitInsideEditor).toBe(true)
  })

  test('double-click selects a word in the editor', async ({ page }) => {
    await waitForPrompt(page)
    await expect(page.locator(EDITOR)).toBeVisible({ timeout: 8000 })
    await page.locator(INPUT).fill('echo hello world foobar')

    // Aim at the word, not at a number.
    //
    // This used to double-click `box.x + 120` and accept any of the four words
    // back. 120px from the left edge is a claim about the terminal FONT: on the
    // author's Mac it lands inside `world`, and in the e2e container it lands on
    // the space before it, so the selection came back as " " and the test failed
    // on a product that was working (nocx-z9s9.10).
    //
    // A double-click is inherently positional — a user does put the pointer
    // somewhere — so the coordinate stays. What changes is where it comes from:
    // a Range over the word itself, measured in the page at run time. Whatever
    // the font, the point is inside `world`, and the assertion can then be the
    // one the product actually owes — that word and no other.
    const target = await wordCenter(page, 'world')
    await page.mouse.dblclick(target.x, target.y)

    // CM6 keeps the native DOM selection in sync with the editor selection,
    // so the picked word is observable via getSelection(). The textarea's
    // selectionStart/selectionEnd have no equivalent on a contenteditable.
    const sel = await page.evaluate(() => {
      const input = document.querySelector('.nocx-editor-input') as HTMLElement
      const s = window.getSelection()
      return {
        text: s?.toString() ?? '',
        insideEditor: s !== null && s.anchorNode !== null && input.contains(s.anchorNode),
      }
    })
    // The selection must live in the editor, not in the terminal behind it.
    expect(sel.insideEditor).toBe(true)
    // And it is exactly the word that was clicked — never a partial range, a
    // neighbouring word, or the whitespace between two.
    expect(sel.text).toBe('world')
  })

  /**
   * The DONE WHEN of the CM6 epic (nocx-2gf): shell token highlighting is
   * visible in the running app.
   *
   * `editor.test.ts` already asserts what the grammar produces, thoroughly and
   * in jsdom — which is where the gap was. jsdom loads no stylesheet, so a
   * token class there proves the tokenizer ran and says nothing about whether
   * anything is painted: a build that dropped style.css, or renamed the
   * classes on one side only, keeps every one of those tests green while the
   * user looks at one flat colour.
   *
   * So this asserts the two halves that only a real browser can: the classes
   * appear on the live line, and the roles are painted APART. Distinctness is
   * the honest form of "visible" — comparing against a hard-coded hex would
   * assert the current theme (there are several, and they are user-chosen),
   * while a role that shares its neighbour's colour is exactly the failure a
   * person would report.
   */
  test('shell tokens are classed and painted apart in the running app', async ({ page }) => {
    await waitForPrompt(page)
    await expect(page.locator(EDITOR)).toBeVisible({ timeout: 8000 })
    await page.locator(INPUT).fill('ls -la | grep foo')

    const roles = await page.evaluate(() => {
      const input = document.querySelector('.nocx-editor-input')
      if (input === null) throw new Error('editor input not in the document')
      const seen: Record<string, { text: string; color: string }> = {}
      for (const span of input.querySelectorAll<HTMLElement>('[class*="tok-"]')) {
        for (const cls of span.className.split(/\s+/)) {
          if (cls.startsWith('tok-') && !(cls in seen)) {
            seen[cls] = { text: span.textContent ?? '', color: getComputedStyle(span).color }
          }
        }
      }
      return seen
    })

    // The grammar is running against the live line, not only in a unit test.
    expect(roles['tok-flag']?.text).toBe('-la')
    expect(roles['tok-operator']?.text).toBe('|')

    // And the roles do not share a colour. `tok-command` is deliberately left
    // out of the comparison: a command word the session cannot resolve gives
    // up the command colour by design (.tok-command.tok-unresolved in
    // style.css), so its paint depends on what the shell reported, which is
    // not what this test is about.
    expect(roles['tok-flag']?.color).not.toBe(roles['tok-operator']?.color)
    expect(roles['tok-path']?.color).not.toBe(roles['tok-operator']?.color)
  })

  test('submit gives the composer box to the command, and takes it back', async ({ page }) => {
    await waitForPrompt(page)
    await expect(page.locator(EDITOR)).toBeVisible({ timeout: 8000 })
    await page.locator(INPUT).fill("printf 'A\\nB\\nC\\n\\e]0;JIT\\a';read x")

    // Everything this test asserts, in one read: the composer's box, the
    // scroller's box, and where the block's header sits inside it. The header
    // is measured against the scroller rather than the viewport so a scroll
    // does not read as a move.
    const geometry = () =>
      page.evaluate(() => {
        const editor = document.querySelector<HTMLElement>('.pane.active .nocx-editor')
        const area = document.querySelector<HTMLElement>('.pane.active .scrollback-area')
        const block = document.querySelector<HTMLElement>('.pane.active .cmd-block')
        const inner = document.querySelector<HTMLElement>('.pane.active .scrollback-inner')
        if (editor === null || area === null || inner === null) {
          throw new Error('terminal layout is incomplete')
        }
        const areaRect = area.getBoundingClientRect()
        return {
          editorHeight: Math.round(editor.getBoundingClientRect().height),
          areaHeight: area.clientHeight,
          blockTop: block ? Math.round(block.getBoundingClientRect().top - areaRect.top) : -1,
          display: editor.style.display,
          visibility: editor.style.visibility,
          // The grid's row pitch, published by scrollback/cell-metric.ts. The
          // tolerance below is stated in ROWS, so it must come from the same
          // number the layout is built on rather than from a literal.
          cell: Math.round(
            parseFloat(getComputedStyle(inner).getPropertyValue('--term-cell-height')) || 0,
          ),
        }
      })

    const before = await geometry()
    await page.keyboard.press('Enter')
    // The composer LEAVES the layout at submit — `display:none`, box and all.
    // It kept its box once, so the scrollback would not jump by that height at
    // every Enter; the settle glide answers that better, and the box is what an
    // inline TUI on the normal buffer needs (nocx-g6hnk).
    await expect(page.locator(EDITOR)).toBeHidden()

    // The OSC title is emitted after all three rows. Waiting for it makes the
    // running sample content-ordered rather than a timeout/height surrogate.
    await expect(page.locator(TITLE)).toContainText('JIT')
    await page.waitForFunction(() => {
      const clip =
        document
          .querySelector<HTMLElement>('.pane.active .xterm-live-viewport')
          ?.getBoundingClientRect().height ?? 0
      const live =
        document
          .querySelector<HTMLElement>('.pane.active .xterm-live-container')
          ?.getBoundingClientRect().height ?? 0
      // The flow box is the clip plus the body padding both halves share, so
      // it is never smaller — and the rows are in by the time it passes 50px.
      return clip >= 50 && live >= clip
    })
    const running = await geometry()

    // The composer is gone and its height has gone to the scroller — the
    // whole point: `top` on the normal buffer gets the rows `htop` gets on the
    // alternate one. One pixel of rounding between a rect and a clientHeight.
    expect(running.display).toBe('none')
    expect(running.editorHeight).toBe(0)
    expect(
      Math.abs(running.areaHeight - (before.areaHeight + before.editorHeight)),
    ).toBeLessThanOrEqual(1)
    // A short command starts where the prompt was, not at the top of the pane.
    expect(running.blockTop).toBeGreaterThan(running.areaHeight / 2)

    // Release the shell-side hold only after observing the running geometry.
    await page.keyboard.press('Enter')

    await expect(page.locator(EDITOR)).toBeVisible({ timeout: 5000 })
    await page.waitForFunction(
      () =>
        (document
          .querySelector<HTMLElement>('.pane.active .xterm-live-container')
          ?.getBoundingClientRect().height ?? -1) < 0.5,
    )
    // And wait for the settle to finish. The pane MOVES to its new geometry
    // rather than jumping to it, so a measurement taken mid-glide reads the
    // transform, not the layout (nocx-i4h04.2).
    await page.waitForFunction(
      () =>
        (document.querySelector<HTMLElement>('.pane.active .scrollback-inner')?.getAnimations()
          .length ?? 0) === 0,
    )
    // And the prompt's return gives the box back, exactly.
    const after = await geometry()
    expect(after.display).toBe('')
    expect(after.editorHeight).toBe(before.editorHeight)
    expect(after.areaHeight).toBe(before.areaHeight)

    // THE FREEZE MUST NOT PUSH THE HEADER BACK DOWN — not by a row, not at
    // all. The frozen body is the same box as the live region it replaces:
    // same row pitch, same body padding, and the same rows, because the live
    // region stopped reserving the one the cursor moved to (it is below the
    // fence the block ends at, and belongs to no block). Upward movement is
    // unbounded on purpose: output arriving pushes the header up, exactly as
    // a terminal scrolls. One pixel of rounding is all that is allowed back.
    expect(after.cell).toBeGreaterThan(0)
    expect(after.blockTop - running.blockTop).toBeLessThanOrEqual(1)
  })

  // THE COMPOSER IS ONE HEIGHT, EMPTY OR NOT. Typing the first character
  // changed it: the card was 10px taller while the draft was empty and
  // snapped down at the first keystroke, taking the chip row, the Run token
  // and the scrollback that hangs above it along (nocx-6c546).
  //
  // base.css styles `::-webkit-scrollbar` at 10px, which makes every
  // scrollbar in this app a CLASSIC one — it takes layout space rather than
  // floating over the content. CM6's `.cm-scroller` carries
  // `overflow-x: auto`, and WebKit reserves that 10px horizontal gutter for
  // the empty document and does not re-evaluate the decision until the
  // document changes. So the jump was the reservation being dropped.
  //
  // It reproduces on the `webkit` project and NOT on `chromium` — which is
  // the whole reason webkit stays in the matrix, WKWebView being what the
  // packaged app runs. The assertion is a comparison of the same element in
  // two states rather than an absolute pixel count, so it says the same
  // thing whatever the viewport, font or engine.
  test('the composer keeps its height when the first character is typed', async ({ page }) => {
    await waitForPrompt(page)
    await expect(page.locator(EDITOR)).toBeVisible({ timeout: 8000 })

    const cardHeight = () =>
      page.evaluate(
        () =>
          document.querySelector<HTMLElement>('.pane.active .nocx-editor')!.getBoundingClientRect()
            .height,
      )

    /**
     * What the person has typed — NOT what `.cm-content` renders.
     *
     * The completion's ghost tail is a `span.nocx-editor-ghost` decoration
     * INSIDE the content DOM (suggest/controller.ts), so `textContent` is the
     * typed prefix plus whatever shell history completes it to. On a stand
     * where an earlier spec has run a command, one `e` renders as `echo
     * nocx-journey-warmup` — which is the product working, and is what a
     * `toHaveText('e')` here reported as a failure in the full suite while
     * passing when this file ran alone.
     */
    const draft = () =>
      page.evaluate(() => {
        const content = document.querySelector<HTMLElement>('.pane.active .nocx-editor-input')
        if (content === null) return null
        const copy = content.cloneNode(true) as HTMLElement
        for (const ghost of copy.querySelectorAll('.nocx-editor-ghost')) ghost.remove()
        return copy.textContent ?? ''
      })

    // The precondition, stated rather than assumed: the baseline is the height
    // of an EMPTY composer, so a draft left behind by another spec would make
    // every number below measure something else.
    await expect.poll(draft).toBe('')
    const empty = await cardHeight()
    expect(empty).toBeGreaterThan(0)

    await page.locator('.pane.active .nocx-editor-input').click()
    await page.keyboard.type('e')
    // Wait on the state, never on a duration: the character is in the
    // document before the height is worth reading.
    await expect.poll(draft).toBe('e')
    const typed = await cardHeight()

    // LESS THAN A PIXEL, not equal, and the slack is measured rather than
    // guessed. When the completion has a candidate it draws the ghost as a CM6
    // WIDGET, and CM6 puts an `<img class="cm-widgetBuffer">` beside every
    // widget — `height: 1em`, `vertical-align: text-top` — which lifts the line
    // box from 16.796875 to 16.890625. That is 0.09375px, it belongs to CM6
    // rather than to this card, and whether it is there at all depends on what
    // the stand's shell history holds, which is whatever spec ran before.
    //
    // The defect this case guards is 10px — a whole scrollbar gutter — so a
    // pixel of slack still fails on it by a factor of ten, and refusing the
    // slack only buys a test that reports the suite's file order.
    expect(Math.abs(typed - empty)).toBeLessThan(1)

    // And back again when the draft is emptied — the height belongs to the
    // box, not to whether anything is in it.
    await page.keyboard.press('Backspace')
    await expect.poll(draft).toBe('')
    expect(Math.abs((await cardHeight()) - empty)).toBeLessThan(1)
  })
})
