// Замороженная строка занимает ровно свои колонки (nocx-ec18).
//
// Сравниваются ШИРИНЫ, а не строки CSS: 6240 jsdom-тестов были зелёными,
// пока продукт был сломан, потому что jsdom не считает раскладку.
//
// ДВА РАЗНЫХ ВОПРОСА, ДВА РАЗНЫХ СПОСОБА. «Механизм срабатывает» нельзя
// проверять на настоящих символах при настоящей метрике: попадут ли
// ⬢ ⟳ ⟲ 🗑 мимо ячейки, решает шрифт платформы, а контейнерный прогон на
// Linux — не macOS владельца. Всё, что должно УВИДЕТЬ коробку, работает на
// принуждённой метрике, выведенной из замера в этом же браузере, — тогда
// промах гарантирован по построению. «Геометрия верна» проверяется при
// настоящей метрике и держится независимо от того, появились коробки или нет.

import { test, expect, promptReady } from './harness'
import type { Page } from '@playwright/test'

const COLS = 40
const SYMBOLS = '⬢⟳⟲\u{1F5D1}'

interface RowShape {
  width: number
  boxes: Array<{ cols: string; text: string }>
  text: string
  links: number
}

/** Продвижка каждого кластера в настоящих условиях блока: внутри
 *  `.cmd-output > .term-line`, то есть с тем шрифтом, размером и трекингом,
 *  которые получит строка. Зонд живёт и умирает внутри одного evaluate. */
async function advancesOf(page: Page, clusters: string[]): Promise<number[]> {
  return await page.evaluate((list) => {
    const inner = document.querySelector<HTMLElement>('.scrollback-inner')!
    const host = document.createElement('div')
    host.className = 'cmd-output'
    host.style.cssText =
      'display:block;position:absolute;left:-99999px;top:0;width:max-content;padding:0;white-space:pre;visibility:hidden'
    inner.appendChild(host)
    const out = list.map((c) => {
      const span = document.createElement('span')
      span.className = 'term-line'
      span.style.display = 'inline-block'
      span.textContent = c
      host.appendChild(span)
      const w = span.getBoundingClientRect().width
      span.remove()
      return w
    })
    host.remove()
    return out
  }, clusters)
}

/** Подменить ширину ячейки ПРАВИЛОМ С !important, а не инлайном:
 *  publishCellMetric пишет ту же переменную инлайном на тот же элемент
 *  (scrollback/cell-metric.ts), и републикация посреди теста стёрла бы
 *  подмену — тест краснел бы от чужого таймера. Правило переживает инлайн
 *  без !important. Живёт до конца страницы; следующий тест приходит после
 *  resetStand() и своего page.goto('/'), так что стенд не заражается. */
async function forceCellWidth(page: Page, px: number): Promise<void> {
  await page.addStyleTag({
    content: `.scrollback-inner { --term-cell-width: ${px}px !important; }`,
  })
}

async function rowShape(page: Page, marker: string): Promise<RowShape> {
  return await page.evaluate((m) => {
    const empty = { width: -1, boxes: [], text: '', links: -1 }
    const block = Array.from(document.querySelectorAll('.cmd-block')).find((b) =>
      (b.textContent ?? '').includes(m),
    )
    const row = block?.querySelector<HTMLElement>('.cmd-output > .term-line')
    const out = row?.parentElement
    if (!row || !out) return empty

    // Интринсик-ширина. Собственный рект `.term-line` не годится: она
    // display:block, её бокс равен ширине контейнера и одинаков у всех строк
    // — читая его, тест мерил бы константу. Клон кладётся ВНУТРЬ
    // `.cmd-output`, иначе теряет шрифт: font-family живёт там, а не на
    // `.term-line`.
    const host = document.createElement('div')
    host.style.cssText =
      'position:absolute;left:-99999px;top:0;width:max-content;white-space:pre;visibility:hidden'
    out.appendChild(host)
    const clone = row.cloneNode(true) as HTMLElement
    host.appendChild(clone)
    const width = clone.getBoundingClientRect().width
    host.remove()

    return {
      width,
      boxes: Array.from(row.querySelectorAll('.term-cell')).map((b) => ({
        cols: b.getAttribute('data-cols') ?? '?',
        text: b.textContent ?? '',
      })),
      text: row.textContent ?? '',
      links: row.querySelectorAll('.term-link').length,
    }
  }, marker)
}

async function printLine(page: Page, payload: string, marker: string): Promise<void> {
  // `\\n` и не `\n`: keyboard.type отправляет НАСТОЯЩИЙ перевод строки как
  // Enter, так что литеральный перенос в шаблоне выполнил бы половину
  // команды и напечатал бы остаток в следующий промпт.
  await page.keyboard.type(`printf '%s\\n' '${payload}' # ${marker}`)
  await page.keyboard.press('Enter')
  await expect(page.locator('.cmd-block', { hasText: marker }).first()).toBeVisible({
    timeout: 15_000,
  })
  await expect
    .poll(async () => (await rowShape(page, marker)).width, { timeout: 10_000 })
    .toBeGreaterThan(0)
}

test('a box sits on the same baseline as the text beside it', async ({ page }) => {
  await page.goto('/')
  await promptReady(page)

  // ОДИН И ТОТ ЖЕ СИМВОЛ внутри коробки и рядом с ней. Сравнивать разные
  // глифы бессмысленно: у них разные ascent и разные границы краски при
  // одной и той же базовой линии, и расхождение ректов ничего не сказало бы
  // про базовую линию. Проверяется свойство САМОЙ КОРОБКИ, поэтому строится
  // прямо в DOM и от заморозки не зависит.
  const delta = await page.evaluate(() => {
    const inner = document.querySelector<HTMLElement>('.scrollback-inner')!
    const host = document.createElement('div')
    host.className = 'cmd-output'
    host.style.cssText =
      'display:block;position:absolute;left:-99999px;top:0;width:max-content;padding:0;white-space:pre;visibility:hidden'
    const row = document.createElement('span')
    row.className = 'term-line'
    row.innerHTML = 'W<span class="term-cell" data-cols="1">W</span>W'
    host.appendChild(row)
    inner.appendChild(host)
    const boxed = document.createRange()
    boxed.selectNodeContents(row.querySelector('.term-cell')!)
    const plain = document.createRange()
    plain.selectNodeContents(row.firstChild!)
    const d = Math.abs(boxed.getBoundingClientRect().top - plain.getBoundingClientRect().top)
    host.remove()
    return d
  })

  expect(delta).toBeLessThan(0.5)
})

// Кластеры, которые xterm считает ОДНОКОЛОНОЧНЫМИ на любой платформе (все —
// BMP, ни один не East-Asian Wide), и не ASCII, поэтому классификатор их
// действительно меряет, а не отсеивает как откалиброванную латиницу. Пара для
// теста ниже выбирается из них ЗАМЕРОМ, а не назначается: контейнерный
// Chromium кладёт `─` в 14px при `W` в 7, то есть «узкий» символ с macOS тут
// оказывается широким. Замерено 2026-09-05 в этом контейнере:
// ° 5.59 · » 5.59 · ¶ 7.55 · ± × ÷ § 8.41 · ⬢ 11 · ─ │ ┌ 14 · ⟳ ⟲ 15.
const SINGLE_COLUMN = ['°', '»', '¶', '±', '×', '÷', '§', '⬢', '─', '│', '┌', '⟳', '⟲']

test('the batch measures each cluster on its own', async ({ page }) => {
  await page.goto('/')
  await promptReady(page)

  // ЛОВУШКА, КОТОРУЮ ЭТОТ ТЕСТ СТОРОЖИТ: если измерители внутри хоста с
  // `width: max-content` объявлены `display:block`, каждый растягивается до
  // ширины хоста, то есть до самого широкого кандидата, и пакет возвращает
  // одно число для всех. Тогда узкий кластер получил бы вердикт широкого.
  //
  // Метрика ставится РОВНО в продвижку узкого кластера: при ней он ложится
  // (промах 0) и коробки получить не должен, а широкий промахивается на
  // несколько пикселей и обязан её получить. Схлопнувшийся пакет закоробкует
  // оба — и тест это увидит.
  const advances = await advancesOf(page, SINGLE_COLUMN)
  let narrow = 0
  let wide = 0
  for (let i = 0; i < advances.length; i++) {
    if (advances[i] < advances[narrow]) narrow = i
    if (advances[i] > advances[wide]) wide = i
  }
  // Пара обязана РАЗЛИЧАТЬСЯ, иначе тест ничего не сторожит. Если шрифт среды
  // кладёт все эти кластеры в одну продвижку — это отчёт о среде, а не флейк:
  // подобрать кластеры, а не ослаблять проверку.
  expect(advances[wide] - advances[narrow]).toBeGreaterThan(1)
  await forceCellWidth(page, advances[narrow])

  const marker = `BM-${Date.now().toString(36)}`
  await printLine(
    page,
    `${SINGLE_COLUMN[narrow].repeat(2)}${SINGLE_COLUMN[wide]}${SINGLE_COLUMN[narrow].repeat(2)}`,
    marker,
  )
  const shape = await rowShape(page, marker)

  expect(shape.boxes.map((b) => b.text)).toEqual([SINGLE_COLUMN[wide]])
})

test('the box mechanism fires when a cell misses its column', async ({ page }) => {
  await page.goto('/')
  await promptReady(page)

  // ШИРИНА ВЫВЕДЕНА ИЗ ЗАМЕРА, А НЕ ВЗЯТА С ПОТОЛКА. «Плюс три пикселя»
  // ничего не доказывало: продвижка конкретного fallback-шрифта могла бы
  // совпасть с новой метрикой, и тест краснел бы на исправном коде. Здесь
  // метрика ставится заведомо дальше самого широкого из символов — тогда
  // промахиваются все, на любом шрифте.
  const advances = await advancesOf(page, [...SYMBOLS])
  await forceCellWidth(page, Math.max(...advances) + 5)

  const marker = `GM-${Date.now().toString(36)}`
  await printLine(page, `WW${SYMBOLS}WW`, marker)
  const shape = await rowShape(page, marker)

  expect(shape.boxes.length).toBe([...SYMBOLS].length)
  expect(shape.boxes.every((b) => b.cols === '1' || b.cols === '2')).toBe(true)
  expect(shape.boxes.every((b) => b.text !== '')).toBe(true)
})

test('the ink of a glyph wider than its cell is scaled to fit inside it', async ({ page }) => {
  await page.goto('/')
  await promptReady(page)

  // МЕТРИКА СТАВИТСЯ В ПРОДВИЖКУ УЗКОГО КЛАСТЕРА, а не шире всех, как в
  // тесте выше. Там коробка нужна была ДЛИННЕЕ глифа, и ужимать было нечего;
  // здесь нужен обратный промах — глиф ШИРЕ своих колонок, потому что именно
  // он рисуется за краем коробки и именно его половину закрашивает сосед со
  // своим фоном. У владельца это 14px краски в ячейке 8px.
  const advances = await advancesOf(page, SINGLE_COLUMN)
  let narrow = 0
  let wide = 0
  for (let i = 0; i < advances.length; i++) {
    if (advances[i] < advances[narrow]) narrow = i
    if (advances[i] > advances[wide]) wide = i
  }
  // Как и в тесте про пакет: пара обязана различаться, иначе утверждать
  // нечего. Схождение продвижек — отчёт о шрифте среды, а не флейк.
  expect(advances[wide] - advances[narrow]).toBeGreaterThan(1)
  await forceCellWidth(page, advances[narrow])

  const marker = `FS-${Date.now().toString(36)}`
  await printLine(page, `WW${SINGLE_COLUMN[wide]}WW`, marker)

  const fitted = await page.evaluate((m) => {
    const block = Array.from(document.querySelectorAll('.cmd-block')).find((b) =>
      (b.textContent ?? '').includes(m),
    )
    const box = block?.querySelector<HTMLElement>('.cmd-output > .term-line .term-cell')
    if (!box) return null
    const ink = box.querySelector<HTMLElement>('.term-cell-ink')
    if (!ink) return { fit: Number.NaN, boxWidth: box.getBoundingClientRect().width, inkWidth: -1 }
    return {
      // Объявленный множитель — тот, что сериализатор положил в разметку.
      fit: Number.parseFloat(ink.style.getPropertyValue('--cell-fit')),
      boxWidth: box.getBoundingClientRect().width,
      // getBoundingClientRect отдаёт УЖЕ ТРАНСФОРМИРОВАННЫЙ прямоугольник,
      // поэтому это прямая проверка «глиф поместился», а не пересчёт по
      // множителю, который тест взял из той же разметки.
      inkWidth: ink.getBoundingClientRect().width,
    }
  }, marker)

  expect(fitted).not.toBeNull()
  expect(fitted!.fit).toBeGreaterThan(0)
  expect(fitted!.fit).toBeLessThan(1)
  // Полпикселя — шум округления раскладки, ровно тот же допуск, что у
  // геометрии строки ниже.
  expect(fitted!.inkWidth).toBeLessThanOrEqual(fitted!.boxWidth + 0.5)
})

test('a row of symbols occupies exactly the columns the grid gave it', async ({ page }) => {
  await page.goto('/')
  await promptReady(page)

  const ref = `GR-${Date.now().toString(36)}`
  await printLine(page, 'W'.repeat(COLS), ref)
  const reference = await rowShape(page, ref)

  const sym = `GS-${Date.now().toString(36)}`
  await printLine(page, `${'W'.repeat(COLS - 4)}${SYMBOLS}`, sym)
  const symbols = await rowShape(page, sym)

  // Полпикселя — шум округления раскладки; целая колонка — разъехавшийся
  // угол рамки. Утверждение держится и когда коробок не появилось: если эти
  // четыре глифа на этой платформе ложатся сами, ширины и так совпадут.
  expect(Math.abs(symbols.width - reference.width)).toBeLessThan(0.5)
})

test('a full-width row does not fold while output wrapping is on', async ({ page }) => {
  await page.goto('/')
  await promptReady(page)
  // Явно, а не в надежде на умолчание: тест обязан утверждать про то
  // состояние, которое он описывает.
  await page.evaluate(() => document.documentElement.setAttribute('data-output-wrap', 'on'))

  const marker = `GW-${Date.now().toString(36)}`
  await page.keyboard.type(
    `printf '%s%s\\n' "$(printf 'W%.0s' $(seq 1 $(( $(tput cols) - 4 ))))" '${SYMBOLS}' # ${marker}`,
  )
  await page.keyboard.press('Enter')
  await expect(page.locator('.cmd-block', { hasText: marker }).first()).toBeVisible({
    timeout: 15_000,
  })

  // Число ВИЗУАЛЬНЫХ СТРОК, а не ректов. Округление высоты к шагу сетки
  // пропустило бы рост меньше половины строки; ректы одни, без группировки,
  // врут в другую сторону — `.term-cell` это inline-block, и его рект
  // отсчитывается от границы бокса, а текстовый — от верха шрифта, так что
  // на НЕсложенной строке они уже дают два разных `top`. Замерено в этом
  // контейнере 2026-09-05: строка ровно в ширину сетки даёт top 511.2 и
  // 512.2 (разница 1.0px), сложенная — 598.2 и 618.2 (разница 20.0px) при
  // line-height 20px. Поэтому ректы группируются с допуском в полстроки:
  // разница внутри строки — это разница шрифтовых ascent, разница между
  // строками — целый шаг сетки, и между ними десятикратный запас.
  const linesOf = async (): Promise<number> =>
    await page.evaluate((m) => {
      const b = Array.from(document.querySelectorAll('.cmd-block')).find((el) =>
        (el.textContent ?? '').includes(m),
      )
      const row = b?.querySelector<HTMLElement>('.cmd-output > .term-line')
      if (!row) return -1
      const range = document.createRange()
      range.selectNodeContents(row)
      const tops = Array.from(range.getClientRects())
        .map((r) => r.top)
        .sort((x, y) => x - y)
      const step = Number.parseFloat(getComputedStyle(row).lineHeight)
      const tolerance = (Number.isFinite(step) && step > 0 ? step : 16) / 2
      let lines = 0
      let previous = Number.NEGATIVE_INFINITY
      for (const top of tops) {
        if (top - previous > tolerance) lines++
        previous = top
      }
      return lines
    }, marker)

  // Строка появляется не в тот же тик, что и блок: до заморозки `.cmd-output`
  // пуст, и ноль означал бы «ещё не отрисовано», а не «не сложилось».
  await expect.poll(linesOf, { timeout: 10_000 }).toBeGreaterThan(0)
  const fragments = await linesOf()

  expect(fragments).toBe(1)
})

test('link decoration leaves the boxes and the geometry intact', async ({ page }) => {
  // decorateLinks строит Range по текстовым узлам и делает extractContents
  // (terminal-links/decorate.ts). Опасен ЧАСТИЧНЫЙ захват: если конец
  // диапазона попадает ВНУТРЬ `.term-cell`, браузер вправе расщепить её,
  // оставив пустой оригинал или клон внутри anchor.
  //
  // Поэтому ссылка КОНЧАЕТСЯ коробочным символом, а метрика принуждена —
  // иначе на этой платформе коробок могло бы не быть вовсе, и тест
  // проверял бы, что ничего не сломалось там, где ничего не происходит.
  //
  // Промежуточное состояние снаружи не наблюдаемо, поэтому сравнение — с
  // ЭТАЛОННОЙ строкой тех же колонок, отличающейся только тем, что она не
  // ссылка.
  await page.goto('/')
  await promptReady(page)
  const advances = await advancesOf(page, [...SYMBOLS])
  await forceCellWidth(page, Math.max(...advances) + 5)

  const url = `https://example.com/x${SYMBOLS}`
  // Ровно те же колонки, и НИ ОДНОГО токена, который грамматика признаёт
  // путём. План предлагал `nolink!!example.com/x`, и это эталон, который
  // сам является ссылкой: `example.com/x` содержит слэш и не состоит из
  // одних цифр, то есть проходит правило `nested` в terminal-links/detect.ts
  // — префикс тут ни при чём. Слэши заменены на `!`, которого нет в наборе
  // символов пути, поэтому строка распадается на токены без слэшей и
  // ссылкой не становится. Длина совпадает посимвольно.
  const plain = `nolink!!example!com!x${SYMBOLS}`

  const refMarker = `LR-${Date.now().toString(36)}`
  await printLine(page, `${plain} tail`, refMarker)
  const reference = await rowShape(page, refMarker)

  const linkMarker = `LL-${Date.now().toString(36)}`
  await printLine(page, `${url} tail`, linkMarker)
  const linked = await rowShape(page, linkMarker)

  // Эталон обязан БЫТЬ строкой с коробками и НЕ быть ссылкой, иначе тест не
  // сторожит ничего. Если детектор всё же признаёт его ссылкой — подобрать
  // другой префикс, а не ослаблять проверку.
  expect(reference.boxes.length).toBeGreaterThan(0)
  expect(reference.links).toBe(0)
  expect(linked.links).toBeGreaterThan(0)

  expect(linked.text).toBe(reference.text.replace('nolink!!example!com!x', 'https://example.com/x'))
  expect(linked.boxes).toEqual(reference.boxes)
  expect(Math.abs(linked.width - reference.width)).toBeLessThan(0.5)
  // Пустая коробка — след расщепления.
  expect(linked.boxes.every((b) => b.text !== '')).toBe(true)
})

test('selecting a row with boxes copies the row', async ({ page }) => {
  await page.goto('/')
  await promptReady(page)
  const advances = await advancesOf(page, [...SYMBOLS])
  await forceCellWidth(page, Math.max(...advances) + 5)

  const marker = `LS-${Date.now().toString(36)}`
  await printLine(page, `WW${SYMBOLS}WW`, marker)
  const shape = await rowShape(page, marker)
  expect(shape.boxes.length).toBeGreaterThan(0)

  const { selected, text } = await page.evaluate((m) => {
    const b = Array.from(document.querySelectorAll('.cmd-block')).find((el) =>
      (el.textContent ?? '').includes(m),
    )
    const row = b!.querySelector<HTMLElement>('.cmd-output > .term-line')!
    const range = document.createRange()
    range.selectNodeContents(row)
    const sel = window.getSelection()!
    sel.removeAllRanges()
    sel.addRange(range)
    return { selected: sel.toString(), text: row.textContent ?? '' }
  }, marker)

  // Коробка содержит ровно исходный текстовый узел, поэтому выделение через
  // её границу не теряет и не добавляет символов.
  expect(selected).toBe(text)
})
