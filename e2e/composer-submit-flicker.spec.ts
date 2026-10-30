import { appReadyForInput, test, expect } from './harness'
import type { Page, TestInfo } from '@playwright/test'

/**
 * nocx-1ebrs.40: ink fades, layout steps, and the restored tail settles.
 *
 * A compositor can skip an entire 140ms fade. Observe the product's animation
 * recipes and their completion, not a lucky rAF sample inside that interval.
 * Layout changes are recorded at DOM mutations and resize delivery. The two
 * closing frames are requested explicitly AFTER the block and its ink settle;
 * they are not the last two entries of a free-running sampler.
 */
const TITLE = '.nocx-tab-title'
const INPUT = '.pane.active .nocx-editor-input'
const BLOCK = '.pane.active .scrollback-inner > .cmd-block[data-block-kind="command"]'

interface State {
  display: string
  height: number
  opacity: number
  top: number
  below: number
  ghosts: number
  active: number
  generation: number
}

interface Motion {
  target: 'editor' | 'ghost' | 'inner' | 'layout'
  duration: number | string | undefined
  easing: string | undefined
  keyframes: Record<string, unknown>[]
  state: State
  outcome: 'running' | 'finished' | 'cancelled'
  end?: State
  departureTask?: State
}

interface Observation {
  fullHeight: number
  initial: State
  layouts: State[]
  motions: Motion[]
  cssGeometry: string[]
  read: () => State
}

declare global {
  interface Window {
    __handoff: Observation
  }
}

async function begin(page: Page, command: string, interruptDeparture = false) {
  await page.goto('/')
  await expect(page.locator(TITLE).first(), 'initial tab has a title').not.toHaveText('', {
    timeout: 15000,
  })
  await appReadyForInput(page)
  const input = page.locator(INPUT)
  await expect(input, 'composer owns a visible input before submit').toBeVisible({ timeout: 8000 })
  await input.fill(command)
  // Do not install the recorder inside an unrelated startup return fade.
  await expect
    .poll(
      () =>
        page.locator('.pane.active .nocx-editor').evaluate((editor) => ({
          opacity: Number(getComputedStyle(editor).opacity),
          active: editor.getAnimations().length,
        })),
      { message: 'initial composer has full ink and no return animation' },
    )
    .toEqual({ opacity: 1, active: 0 })

  await page.evaluate((interrupt) => {
    const pane = document.querySelector('.pane.active') as HTMLElement
    const editor = pane.querySelector('.nocx-editor') as HTMLElement
    const area = pane.querySelector('.scrollback-area') as HTMLElement
    const inner = pane.querySelector('.scrollback-inner') as HTMLElement
    const layout = new Set<Element>([editor, area])
    for (let el = editor.parentElement; el && pane.contains(el); el = el.parentElement)
      layout.add(el)
    const observation: Observation = {
      fullHeight: editor.getBoundingClientRect().height,
      initial: {} as State,
      layouts: [],
      motions: [],
      cssGeometry: [],
      read: () => ({
        display: getComputedStyle(editor).display,
        height: editor.getBoundingClientRect().height,
        opacity: Number(getComputedStyle(editor).opacity),
        top: inner.getBoundingClientRect().top,
        below: area.scrollHeight - area.scrollTop - area.clientHeight,
        ghosts: document.querySelectorAll('[data-nocx-motion-ghost]').length,
        active: [editor, inner].flatMap((el) => el.getAnimations()).length,
        generation: observation.motions.length,
      }),
    }
    window.__handoff = observation
    observation.initial = observation.read()
    const recordLayout = () => {
      observation.layouts.push(observation.read())
      for (const element of layout) {
        const style = getComputedStyle(element)
        // CSS transitions do not call Element.animate. Inspect their declared
        // geometry policy too, including transitions too brief to be sampled.
        const durations = style.transitionDuration.split(',')
        if (
          style.transitionProperty
            .split(',')
            .some(
              (property, index) =>
                !['none', 'opacity'].includes(property.trim()) &&
                parseFloat(durations[index % durations.length]) > 0,
            )
        ) {
          observation.cssGeometry.push(
            `${element.className}: ${style.transitionProperty} ${style.transitionDuration}`,
          )
        }
        for (const animation of element.getAnimations()) {
          if (!(animation instanceof CSSAnimation)) continue
          const keys = (animation.effect as KeyframeEffect).getKeyframes()
          if (
            keys.some((keyframe) =>
              Object.keys(keyframe).some(
                (key) =>
                  !['offset', 'computedOffset', 'easing', 'composite', 'opacity'].includes(key),
              ),
            )
          ) {
            observation.cssGeometry.push(`${element.className}: ${animation.animationName}`)
          }
        }
      }
    }
    // Layout milestones, not paint samples. Mutation delivery is batched;
    // the ghost's creation-task record below catches the immediate hide.
    new MutationObserver(recordLayout).observe(pane, {
      attributes: true,
      attributeFilter: ['style', 'class'],
      subtree: true,
    })
    const resize = new ResizeObserver(recordLayout)
    resize.observe(editor)
    const animate = Element.prototype.animate
    Element.prototype.animate = function (...args: Parameters<Element['animate']>): Animation {
      const animation = animate.apply(this, args)
      const target =
        this === editor
          ? 'editor'
          : this.hasAttribute('data-nocx-motion-ghost')
            ? 'ghost'
            : this === inner
              ? 'inner'
              : layout.has(this)
                ? 'layout'
                : null
      if (target) {
        const effect = animation.effect as KeyframeEffect
        const motion: Motion = {
          target,
          duration:
            typeof effect.getTiming().duration === 'number'
              ? Number(effect.getTiming().duration)
              : String(effect.getTiming().duration),
          easing: effect.getTiming().easing,
          keyframes: effect.getKeyframes(),
          state: observation.read(),
          outcome: 'running',
        }
        observation.motions.push(motion)
        if (target === 'ghost') {
          if (
            interrupt &&
            !observation.motions.some((other) => other !== motion && other.target === 'ghost')
          ) {
            // Construct the quick-return overlap even if the shell/backend is
            // slower than 140ms. Native WAAPI seeking is not page.clock:
            // preserve the recipe and let ONLY the product cancel this ghost.
            animation.pause()
            animation.currentTime = Number(motion.duration) / 2
          }
          queueMicrotask(() => {
            motion.departureTask = observation.read()
          })
        }
        void animation.finished.then(
          () => {
            motion.outcome = 'finished'
            // Let the product's own finished handlers remove the ghost first.
            queueMicrotask(() => {
              motion.end = observation.read()
            })
          },
          () => {
            motion.outcome = 'cancelled'
          },
        )
      }
      return animation
    }
  }, interruptDeparture)
  await page.keyboard.press('Enter')
}

async function settled(page: Page): Promise<State> {
  // Lifecycle closure precedes the stored body read. Wait for BOTH before
  // accepting an animation-free frame; otherwise the body can start a fresh
  // settle after the return, which caused nocx-atpw5.1.9.
  await expect(
    page.locator(BLOCK),
    'submitted command reached its success outcome',
  ).toHaveAttribute('data-outcome', 'success')
  await expect(
    page.locator(`${BLOCK} > .cmd-output, ${BLOCK} > [data-output-empty]`),
    'closed command has materialized its stored body',
  ).toHaveCount(1)
  await expect
    .poll(
      () =>
        page.evaluate(() => {
          const state = window.__handoff.read()
          return {
            display: state.display !== 'none',
            ink: state.opacity >= 0.95,
            height: state.height >= window.__handoff.fullHeight - 1,
            active: state.active,
            ghosts: state.ghosts,
          }
        }),
      {
        message:
          'materialized command restored full-height/full-ink composer with no active motion or ghost',
      },
    )
    .toEqual({ display: true, ink: true, height: true, active: 0, ghosts: 0 })
  return page.evaluate(() => window.__handoff.read())
}

async function finish(page: Page, testInfo: TestInfo) {
  const restored = await settled(page)
  // Construct exactly two frames after the observable settle. Do not retry
  // this pair: a new fade or motion here is evidence of a late hand-off, not
  // a reason to discard the failure and seek a luckier pair.
  const pair = await page.evaluate(async () => {
    const next = () =>
      new Promise<State>((resolve) => requestAnimationFrame(() => resolve(window.__handoff.read())))
    return [await next(), await next()]
  })
  const records = await page.evaluate(() => {
    const { fullHeight, initial, layouts, motions, cssGeometry } = window.__handoff
    return { fullHeight, initial, layouts, motions, cssGeometry }
  })
  const dump = JSON.stringify({ ...records, restored, pair })
  await testInfo.attach('handoff-records', { body: dump, contentType: 'application/json' })
  return { ...records, restored, pair, dump }
}

function assertMotion(records: Awaited<ReturnType<typeof finish>>) {
  const { fullHeight, initial, layouts, motions, pair, dump } = records
  expect(
    pair.every(
      (state) =>
        state.active === 0 &&
        state.ghosts === 0 &&
        state.opacity >= 0.95 &&
        state.display !== 'none',
    ),
    `wanted two full-ink, animation-free closing frames; saw ${dump}`,
  ).toBe(true)
  expect(pair[1].generation, `wanted no fresh motion between closing frames; saw ${dump}`).toBe(
    pair[0].generation,
  )
  expect(
    pair[0].generation,
    `wanted no fresh motion after the completed restore; saw ${dump}`,
  ).toBe(records.restored.generation)
  expect(initial.opacity, `wanted full ink before submit; saw ${dump}`).toBe(1)
  expect(initial.height, `wanted measurable initial composer; saw ${dump}`).toBeGreaterThan(1)
  expect(
    layouts.filter((s) => s.height > 1 && s.height < fullHeight - 1),
    `wanted layout height full or gone at each layout change; saw ${dump}`,
  ).toEqual([])

  // A recipe proves interpolation was requested, NOT that a throttled
  // compositor painted an intermediate opacity. page.clock does not drive
  // WAAPI in WebKit (see bead evidence); demanding such a paint is a lottery.
  const recipe = (target: Motion['target'], from: string, to: string) =>
    motions.some(
      (m) =>
        m.target === target &&
        typeof m.duration === 'number' &&
        m.duration === 140 &&
        m.easing === 'cubic-bezier(0.2, 0, 0, 1)' &&
        m.keyframes.every((frame) => frame.easing === 'linear') &&
        m.keyframes[0]?.opacity === from &&
        m.keyframes.at(-1)?.opacity === to,
    )
  expect(recipe('ghost', '1', '0'), `wanted a finite ghost ink fade 1 → 0; saw ${dump}`).toBe(true)
  expect(recipe('editor', '0', '1'), `wanted a finite composer ink fade 0 → 1; saw ${dump}`).toBe(
    true,
  )
  const firstDeparture = motions.find((motion) => motion.target === 'ghost')
  expect(
    firstDeparture?.departureTask?.display,
    `wanted composer display:none in the departure creation task, not after fading a live input; saw ${dump}`,
  ).toBe('none')
  expect(
    firstDeparture?.departureTask?.height,
    `wanted composer removed from layout in the departure creation task; saw ${dump}`,
  ).toBe(0)
  const completedReturn = motions.find(
    (m) => m.target === 'editor' && m.outcome === 'finished' && m.end?.opacity === 1,
  )
  expect(
    completedReturn,
    `wanted completed return with computed full ink, not the sampler's last frame; saw ${dump}`,
  ).toBeDefined()

  expect(
    records.cssGeometry,
    `wanted no CSS geometry transitions/animations on pane layout elements; saw ${dump}`,
  ).toEqual([])
  // Read every configured keyframe, not height at a handful of paint times.
  // Only opacity on the editor and translation on the stack may animate.
  const metadata = new Set(['offset', 'computedOffset', 'easing', 'composite'])
  const geometryMotion = motions.filter(
    (m) =>
      m.target !== 'ghost' &&
      m.keyframes.some((kf) =>
        Object.keys(kf).some(
          (key) =>
            !metadata.has(key) &&
            !(m.target === 'editor' && key === 'opacity') &&
            !(m.target === 'inner' && key === 'transform'),
        ),
      ),
  )
  expect(
    geometryMotion,
    `wanted ink/stack-only motion, no pane-level animated geometry; saw ${dump}`,
  ).toEqual([])
  // This bounded observation cannot promise that no future asynchronous
  // product event will ever move the pane. It proves two explicitly requested
  // frames AFTER materialization and animation completion agree.
  expect(
    Math.abs(pair[1].top - pair[0].top),
    `wanted stable post-restore stack position; saw ${dump}`,
  ).toBeLessThanOrEqual(1)
  expect(pair[1].below, `wanted tail in view after restore; saw ${dump}`).toBeLessThanOrEqual(2)
  expect(
    pair[1].height,
    `wanted full-height restored composer; saw ${dump}`,
  ).toBeGreaterThanOrEqual(fullHeight - 1)
}

test.describe('the submit hand-off (nocx-1ebrs.40)', () => {
  test('a quick command dips the panel and turns it around; the geometry never animates', async ({
    page,
  }, testInfo) => {
    await begin(page, 'true', true)
    const records = await finish(page, testInfo)
    assertMotion(records)
    expect(
      records.motions.find((motion) => motion.target === 'ghost')?.outcome,
      `wanted the real return to cancel the in-flight departure ghost; saw ${records.dump}`,
    ).toBe('cancelled')
  })

  test('a real command departs through a fade, and the tail never detaches', async ({
    page,
  }, testInfo) => {
    // Hold the real shell command open until its departure finishes. Unlike
    // sleep 0.4 this cannot finish before the browser observes the hidden box.
    await begin(page, "read -r nocx_handoff; printf 'handoff complete\\n'")
    await expect
      .poll(
        () =>
          page.evaluate(() => {
            const o = window.__handoff
            return {
              hidden: o.read().display === 'none',
              active: o.read().active,
              ghostFinished: o.motions.some(
                (m) => m.target === 'ghost' && m.outcome === 'finished',
              ),
            }
          }),
        { message: 'held command has a hidden composer and a completed departure fade' },
      )
      .toEqual({ hidden: true, ghostFinished: true, active: 0 })
    // A throttled compositor cannot prove an interval of painted tail
    // positions. Check the attached tail at the actual departure completion,
    // while PTY input still holds the command open, rather than counting
    // consecutive detached samples with an unknown time gap.
    const absent = await page.evaluate(() => window.__handoff.read())
    expect(
      absent.height,
      `wanted departed composer out of layout; saw ${JSON.stringify(absent)}`,
    ).toBe(0)
    expect(
      absent.below,
      `wanted tail attached after departure finished while command remains running; saw ${JSON.stringify(absent)}`,
    ).toBeLessThanOrEqual(2)
    await page.keyboard.press('Enter')
    const records = await finish(page, testInfo)
    assertMotion(records)
    const restoreGlides = records.motions.filter(
      (m) => m.target === 'inner' && m.state.display !== 'none',
    )
    // _glide cancels the previous animation before EVERY layout mutation.
    // Its replacement may have zero displacement and need no animation at
    // all. CI run 41014311723 created two valid restore recipes (74px and
    // 50px), then retargeted both: zero completions is not a failed restore.
    // Check every recipe that exists; full ink/height, attached tail and the
    // unchanged settled pair above remain mandatory even when none is needed.
    const invalidRestoreGlides = restoreGlides.filter((m) => {
      const offset = /^translateY\((-?[\d.]+)px\)$/.exec(String(m.keyframes[0]?.transform))
      return !(
        m.duration === 140 &&
        m.easing === 'cubic-bezier(0.2, 0, 0, 1)' &&
        m.keyframes.every((frame) => frame.easing === 'linear') &&
        offset !== null &&
        Math.abs(Number(offset[1])) >= 1 &&
        m.keyframes.at(-1)?.transform === 'translateY(0px)' &&
        (m.outcome === 'finished' || m.outcome === 'cancelled')
      )
    })
    expect(
      invalidRestoreGlides,
      `wanted each created restore glide to have a smooth 140ms nonzero recipe to identity; saw ${restoreGlides.length} created, ${restoreGlides.filter((m) => m.outcome === 'finished').length} finished, ${restoreGlides.filter((m) => m.outcome === 'cancelled').length} cancelled; records: ${records.dump}`,
    ).toEqual([])
  })
})
