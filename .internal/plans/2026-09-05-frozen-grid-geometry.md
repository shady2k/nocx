# Замороженная строка объявляет геометрию: план реализации ADR-0009

> **For agentic workers:** REQUIRED SUB-SKILL: Use beads-superpowers:subagent-driven-development (recommended) or beads-superpowers:executing-plans to implement this plan task-by-task. Each Task becomes a bead (`bd create -t task --parent <epic-id>`). Steps within tasks use checkbox (`- [ ]`) syntax for human readability.

**Goal:** Замороженная строка занимает ровно свои колонки и заливает их фоном целиком, потому что каждый прогон объявляет измеренный трекинг, а не потому что к строке применена одна угаданная дельта.

**Architecture:** Механизм взят у DOM-рендерера самого xterm.js и прочитан в его исходниках. Для каждой ячейки `spacing = колонки × cellWidth − измеренная продвижка(chars, bold, italic)`; соседние ячейки сливаются в один прогон только при совпадении атрибутов И `spacing`; прогон несёт этот `spacing` как `letter-spacing`, поэтому каждая ячейка вносит `продвижка + spacing = колонки × cellWidth` и сумма строки точна без единого объявления `width`. Прогон — `inline-block` полной высоты строки, отчего фон покрывает прямоугольник ячейки, а не content-box шрифта.

**Tech Stack:** TypeScript, xterm.js `IBufferLine`/`IBufferCell`, vitest (jsdom), Playwright (`e2e/run-in-container.sh`).

**Решение:** [ADR-0009](../../docs/decisions/0009-dom-scrollback-with-explicit-cell-geometry.md). **Спека:** `.internal/specs/2026-09-05-frozen-grid-renderer-design.md`.

## Global Constraints

- **Одна проходка ячеек (AD-8).** `collectRunsOf` в `frontend/src/scrollback/serializer.ts` остаётся единственным обходом ячеек.
- **Семантические эмиссии не меняются побайтово.** `serializeRangeSGR` и `serializeRangeText` зовут `collectRunsOf` без классификатора; проверяется тестом, который уже есть.
- **jsdom раскладку не считает.** Ширина, высота и базовая линия проверяются в браузере; разметка — в vitest.
- **Тайпчек по `tsconfig.test.json`, не по `tsconfig.json`.** Второй тестовые файлы не покрывает и остаётся зелёным при ошибке типа в тесте, а `pre-commit` гоняет первый. Бинарники называть точно: `./node_modules/.bin/tsc`, `./node_modules/.bin/vitest` — `npx` в свежем дереве может не разрешиться.
- **Корневые зависимости нужны хуку,** не коду: `pre-commit` требует корневой `node_modules` (prettier и корневой eslint). В свежем дереве `npm ci && cd frontend && npm ci`.
- **Замер снимается ровно в тех условиях, в каких лежит строка.** `font-family` объявлен на `.cmd-output`, а не на `.term-line` и не на контейнере скроллбэка; зонд обязан быть настоящим `.cmd-output` с `.term-line` внутри. Сигнатура кэша снимается с того же узла, в котором мерили.
- **Эталон (macOS, панель владельца, 2026-09-05):** ячейка 8px, строка 19px, content-box цветного прогона 16.5px; U+1F5D1 — 13.572, U+27F3 и U+27F2 — 9.087, U+2B22 — 8.903.

## Что уже есть и переиспользуется

Работа по `nocx-ec18` закрыта и лежит в `feat/agent-orchestration`. Из неё **сохраняется целиком**:

- `frontend/src/scrollback/cell-fit.ts` — пакетный замер (все записи, потом все чтения: одна принудительная раскладка вместо N), кэш с сигнатурой в ключе, настоящий LRU, отказ кэшировать неположительный замер, `dispose` со снятием слушателя `loadingdone`. Меняется только то, ЧТО он отдаёт наружу.
- `.term-cell-ink` и его `transform: scale()` — ужимание краски глифа, который шире своих колонок. Остаётся как есть.
- `e2e/frozen-line-grid.spec.ts` — восемь браузерных тестов. Дополняется, не переписывается.

**Ретируется:** `.term-cell` как коробка фиксированной ширины (её роль берёт `letter-spacing` прогона), глобальный `letter-spacing: var(--term-cell-delta)` на `.term-line`, публикация `--term-cell-delta` и зонд из букв `W` в `cell-metric.ts`.

## Осознанно вынесено за рамки

- **Восстановленный после перезапуска блок остаётся приблизительным.** `serializeRangeSGR`
  ширин ячеек не несёт и не должен: расширять его — значит связать парсер стилей с частным
  протоколом геометрии. Бид на sidecar заведён при закрытии `nocx-ec18` и в этот план не
  входит.
- **`nocx-4ff.26` не закрывается.** Блок сериализуется в момент `D`, а буфер xterm ограничен
  10 000 строк, поэтому длинный вывод уже потерял начало. Это про то, КОГДА делается копия,
  а не про то, как она разложена.
- **BiDi, Sixel, OSC 8 и стили подчёркивания** — из списка, названного в ADR-0009, здесь не
  трогаются. Явная геометрия делает их адресуемыми, но каждый остаётся своей задачей.

## Порядок и обратимость

После задачи 1 продукт не меняется (новый выход классификатора никем не читается). Задачи 2 и 3 **неделимы**: между «глобальный трекинг убран» и «по-прогонный поставлен» строка разъедется, поэтому они идут одним коммитом. Задача 4 — измерение штрафа, оно может дать основание откатить всё разом, поэтому идёт до закрытия эпика.

---

### Task 1: `cell-fit` отдаёт измеренную продвижку

**Files:**

- Modify: `frontend/src/scrollback/cell-fit.ts`
- Modify: `frontend/src/scrollback/cell-fit.test.ts`

**Interfaces:**

- Consumes: ничего нового.
- Produces: `advanceOf(chars: string, face: FitFace): number | null` вместо `boxOf`. `null` означает «не измерен» — вызывающий тогда ведёт себя как сегодня, без трекинга.
- `begin()`, `warm(candidates)`, `dispose()`, `size()` сохраняются без изменений. `FitCandidate` теряет поле `width`: продвижка кластера от числа колонок не зависит, колонки нужны только тому, кто считает `spacing`.

**Acceptance Criteria:**

- `advanceOf` возвращает то самое число, которое вернул измеритель, без порогов и без вердиктов.
- Быстрого пути по ASCII больше НЕТ: измеряется каждый различный кластер, включая латиницу. Кэш от этого растёт примерно на сотню записей на начертание и остаётся в границе.
- Ключ кэша по-прежнему включает начертание и сигнатуру, снятую с зонда.
- Неположительный замер не кэшируется и даёт `null`.
- `warm` по-прежнему делает РОВНО ОДИН вызов измерителя на пакет.

- [ ] **Step 1: Переписать тесты под новый выход**

В `frontend/src/scrollback/cell-fit.test.ts` заменить утверждения о вердиктах на утверждения о продвижке. Целиком заменяется блок `describe('createCellFit', ...)`; каркас (`containerWith`, `batch`, `REGULAR`, `BOLD`) остаётся.

```ts
it('отдаёт измеренную продвижку и ничего о ней не решает', () => {
  // Порог, колонки и вердикт «нужна ли коробка» переехали к тому, кто
  // считает spacing. Здесь остался кэш ширин — ровно то, чем он всегда и
  // был по существу.
  const fit = createCellFit(
    () => containerWith(8),
    batch(() => 13.572),
  )
  fit.begin()
  fit.warm([{ chars: '🗑', face: REGULAR }])
  expect(fit.advanceOf('🗑', REGULAR)).toBe(13.572)
})

it('меряет и латиницу тоже', () => {
  // Быстрого пути по ASCII больше нет. Он существовал, пока дельта была
  // одна на строку и латиница была тем подмножеством, на котором её
  // калибровали. Теперь spacing считается для каждого кластера, и «мы и
  // так знаем ответ для буквы» перестало быть правдой: ответ для буквы —
  // это её продвижка, и её надо померить.
  const measure = batch(() => 8.4287)
  const fit = createCellFit(() => containerWith(8), measure)
  fit.begin()
  fit.warm([{ chars: 'a', face: REGULAR }])
  expect(measure).toHaveBeenCalledTimes(1)
  expect(fit.advanceOf('a', REGULAR)).toBe(8.4287)
})

it('не отвечает про то, чего не мерил', () => {
  const fit = createCellFit(
    () => containerWith(8),
    batch(() => 13.572),
  )
  fit.begin()
  expect(fit.advanceOf('🗑', REGULAR)).toBeNull()
})

it('не кэширует нулевой замер', () => {
  // Погашенный зонд меряется в ноль. Ноль как продвижка дал бы spacing,
  // равный целой ячейке, то есть строку, разъехавшуюся вдвое.
  const fit = createCellFit(
    () => containerWith(8),
    batch(() => 0),
  )
  fit.begin()
  fit.warm([{ chars: '⬢', face: REGULAR }])
  expect(fit.advanceOf('⬢', REGULAR)).toBeNull()
  expect(fit.size()).toBe(0)
})
```

Тесты про начертания, сигнатуру, пакет, LRU, `dispose` и слушатель шрифтов **остаются как есть**, у них меняется только вызов `boxColumns`/`boxOf` на `advanceOf` и `FitCandidate` теряет `width`.

- [ ] **Step 2: Убедиться, что тесты падают**

Run: `cd frontend && ./node_modules/.bin/vitest run src/scrollback/cell-fit.test.ts`
Expected: FAIL — `advanceOf` не существует.

- [ ] **Step 3: Свести модуль к кэшу ширин**

В `frontend/src/scrollback/cell-fit.ts`:

```ts
export interface FitCandidate {
  chars: string
  face: FitFace
}

export interface CellFit {
  begin(): boolean
  warm(candidates: Iterable<FitCandidate>): void
  /** Измеренная продвижка кластера, либо null — не мерили. */
  advanceOf(chars: string, face: FitFace): number | null
  dispose(): void
  size(): number
}
```

Внутри: убрать `isCalibratedAscii` и всю ветку быстрого пути; `warm` кладёт в кэш сам замер; `advanceOf` — чтение с обновлением LRU. Порог `FIT_EPSILON_PX` из этого модуля уходит вместе с вердиктом — он нужен тому, кто сравнивает `spacing`.

Шапку модуля переписать: он больше не «владелец вопроса, ложится ли ячейка на сетку», он владелец ИЗМЕРЕННОЙ ПРОДВИЖКИ. Причина, по которой замер делается в настоящем `.cmd-output`, и причина, по которой сигнатура снимается с зонда, остаются дословно — они куплены двумя кругами разбора.

- [ ] **Step 4: Зелено**

Run: `cd frontend && ./node_modules/.bin/vitest run src/scrollback/cell-fit.test.ts && ./node_modules/.bin/tsc --noEmit -p tsconfig.test.json`
Expected: PASS; тайпчек ругнётся на `blocks.ts` и `serializer.ts`, которые ещё зовут старый метод — это ожидаемо и чинится задачей 2. Если хочется зелёного тайпчека на этом шаге, задачи 1 и 2 делаются одним воркером подряд.

- [ ] **Step 5: Коммит**

```bash
git add frontend/src/scrollback/cell-fit.ts frontend/src/scrollback/cell-fit.test.ts
git commit -m "refactor(terminal): cell-fit reports the advance it measured, and judges nothing (nocx-ec18, ADR-0009)"
```

---

### Task 2+3: по-прогонный трекинг, и строка, у которой фон заливает ячейку

**ЭТА ЗАДАЧА НЕДЕЛИМА.** Между «глобальный трекинг убран» и «по-прогонный поставлен» каждая строка в продукте разъезжается. Один коммит, красные браузерные тесты первыми.

**Files:**

- Modify: `frontend/src/scrollback/serializer.ts`
- Modify: `frontend/src/scrollback/serializer.test.ts`
- Modify: `frontend/src/scrollback/blocks.ts`
- Modify: `frontend/src/scrollback/cell-metric.ts`, `cell-metric.test.ts`
- Modify: `frontend/src/style.css`, `frontend/src/scrollback/cmd-output-wrap.test.ts`
- Modify: `e2e/frozen-line-grid.spec.ts`

**Interfaces:**

- Consumes: `advanceOf` из задачи 1.
- Produces: `collectRunsOf` считает `spacing` и не сливает прогоны с разным `spacing`; `GenericRun` меняет поле `box?: {cols, fit}` на `spacing?: number` плюс `ink?: number` (коэффициент ужимания, только когда `spacing < 0`).

**Acceptance Criteria:**

- Ячейка вносит `продвижка + spacing = колонки × cellWidth`; сумма строки точна без объявления `width`.
- Прогоны сливаются только при равных атрибутах И равном `spacing`.
- Ячейка с отрицательным `spacing` **никогда не сливается**: `transform` на прогоне из нескольких ячеек сдвинул бы каждую следующую влево от её колонки.
- `.term-line` не несёт `letter-spacing`; несёт точную `height` и `overflow: hidden`.
- Прогон — `inline-block; height: 100%; vertical-align: top`.
- `cell-metric.ts` больше не публикует `--term-cell-delta` и не держит зонд из букв `W`.
- Строка из N колонок с чужими глифами по ширине совпадает со строкой из N букв `W`.
- Фон цветного прогона по высоте равен высоте строки, а не 16.5px content-box.
- Ячейка двойной ширины занимает ровно две колонки.
- Число узлов на обычном выводе не выросло.

- [ ] **Step 1: Браузерный тест на фон — тот самый логотип**

Дописать в `e2e/frozen-line-grid.spec.ts`:

```ts
test('a coloured cell fills the row, not the font box', async ({ page }) => {
  // ФОРМА ЛОГОТИПА omp: пробелы с background-color. Фон СТРОЧНОГО элемента
  // красит content-box, высота которого выводится из метрик шрифта — на
  // панели владельца 16.5px в строке 19px, отчего между рядами оставались
  // тёмные полосы. Замеряется рект прогона против ректа строки.
  await page.goto('/')
  await promptReady(page)

  const marker = `BG-${Date.now().toString(36)}`
  // Три ряда цветного фона подряд: полосы видны только между рядами.
  await page.keyboard.type(
    `printf '\\033[41m      \\033[0m\\n\\033[41m      \\033[0m\\n\\033[41m      \\033[0m\\n' # ${marker}`,
  )
  await page.keyboard.press('Enter')
  await expect(page.locator('.cmd-block', { hasText: marker }).first()).toBeVisible({
    timeout: 15_000,
  })

  const gap = await page.evaluate((m) => {
    const b = Array.from(document.querySelectorAll('.cmd-block')).find((el) =>
      (el.textContent ?? '').includes(m),
    )
    const rows = Array.from(b?.querySelectorAll<HTMLElement>('.cmd-output > .term-line') ?? [])
    const painted = rows
      .map((r) => r.querySelector<HTMLElement>('span[style*="background"]'))
      .filter((s): s is HTMLElement => s !== null)
    if (painted.length < 2) return -1
    const rowH = rows[0].getBoundingClientRect().height
    const runH = painted[0].getBoundingClientRect().height
    return rowH - runH
  }, marker)

  // Ноль, а не «примерно»: фон обязан покрыть прямоугольник строки целиком,
  // иначе между рядами остаётся щель ровно этой величины.
  expect(gap).toBeGreaterThanOrEqual(0)
  expect(gap).toBeLessThan(0.5)
})
```

- [ ] **Step 2: Браузерный тест на двухколоночную ячейку**

```ts
test('a double-width cell occupies exactly two columns', async ({ page }) => {
  // Утверждение, которое прежний анализ считал недостижимым: браузер даёт
  // ОДНУ возможность трекинга там, где сетка даёт две колонки. Одной
  // достаточно, если она несёт правильное число — spacing считается от
  // колонок ЯЧЕЙКИ, а не от числа символов.
  await page.goto('/')
  await promptReady(page)

  const wide = `W-${Date.now().toString(36)}`
  await printLine(page, '漢'.repeat(10), wide)
  const wideRow = await rowShape(page, wide)

  const ref = `R-${Date.now().toString(36)}`
  await printLine(page, 'W'.repeat(20), ref)
  const refRow = await rowShape(page, ref)

  // Десять двухколоночных против двадцати одноколоночных — те же 20 колонок.
  expect(Math.abs(wideRow.width - refRow.width)).toBeLessThan(0.5)
})
```

- [ ] **Step 3: Прогнать и увидеть красное**

Run: `PW_PROJECTS=chromium e2e/run-in-container.sh e2e/frozen-line-grid.spec.ts`
Expected: два новых теста красные — фон на 2.5px ниже строки, двухколоночная строка не совпадает по ширине.

- [ ] **Step 4: Считать spacing в обходе ячеек**

В `frontend/src/scrollback/serializer.ts` заменить поле рана:

```ts
interface GenericRun<A> {
  chars: string
  attrs: A
  /** Трекинг прогона: колонки × cellWidth − измеренная продвижка. Прогоны с
   *  разным spacing не сливаются — иначе одна из ячеек внутри встанет не в
   *  свою колонку. undefined означает «продвижку не мерили», и тогда прогон
   *  ведёт себя как сегодняшний текст в потоке. */
  spacing?: number
  /** Во сколько ужать краску, когда spacing отрицателен: глиф шире своих
   *  колонок. Только для прогона ИЗ ОДНОЙ ячейки — см. ниже. */
  ink?: number
}
```

Классификатор меняет форму. Он возвращает ПАРУ, потому что оба числа выводятся
из одних и тех же двух величин — колонок и продвижки, — а `cellWidth` знает
только он: обход ячеек про метрику не знает и знать не должен.

```ts
/** Геометрия одной ячейки. `ink` присутствует только когда `spacing` < 0. */
export interface CellGeometry {
  /** колонки × cellWidth − измеренная продвижка */
  spacing: number
  /** колонки × cellWidth / продвижка — во сколько ужать краску */
  ink?: number
}

type GeometryOf<A> = (chars: string, columns: number, attrs: A) => CellGeometry | null
```

Считает её владелец метрики, рядом с врезкой в `blocks.ts`:

```ts
const geometryOf = (chars: string, columns: number, attrs: CellAttrs): CellGeometry | null => {
  const advance = this._cellFit.advanceOf(chars, { bold: attrs.bold, italic: attrs.italic })
  if (advance === null) return null
  const target = columns * cellWidth
  const spacing = Math.round((target - advance) * 1e4) / 1e4
  // Ужимать нужно только то, что не влезло. Глиф уже своих колонок оставляем
  // как есть: щель безвредна, растянутая буква заметна.
  return spacing < 0 ? { spacing, ink: Math.round((target / advance) * 1e4) / 1e4 } : { spacing }
}
```

Ветку непустой ячейки заменить:

```ts
const text = escape
  ? chars.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
  : chars

const columns = Math.max(1, width)
const geometry = geometryOf?.(chars, columns, attrs) ?? null
// ОТРИЦАТЕЛЬНЫЙ SPACING НЕ СЛИВАЕТСЯ, и это не осторожность. Краска
// такого глифа шире его колонок и ужимается transform'ом; transform на
// прогоне из k ячеек ужал бы и расстояния между ними, поставив каждую
// следующую левее её колонки. Ужимать можно только то, что стоит одно.
const solo = geometry !== null && geometry.ink !== undefined
const last = runs.length > 0 ? runs[runs.length - 1] : undefined
if (
  last !== undefined &&
  !solo &&
  last.ink === undefined &&
  last.spacing === geometry?.spacing &&
  equal(last.attrs, attrs)
) {
  last.chars += text
} else {
  const run: GenericRun<A> = { chars: text, attrs }
  if (geometry !== null) {
    run.spacing = geometry.spacing
    if (geometry.ink !== undefined) run.ink = geometry.ink
  }
  runs.push(run)
}
```

`last.spacing === geometry?.spacing` сравнивает `undefined` с `undefined`, когда
продвижку не мерили ни там, ни там, — и это верное поведение: два неизмеренных
прогона сливаются между собой ровно как сегодня.

- [ ] **Step 5: Эмиссия**

`serializeRange` ставит трекинг на прогон:

```ts
const style = attrsToStyle(snapshot, run.attrs)
const parts: string[] = []
if (style) parts.push(style)
if (run.spacing !== undefined && run.spacing !== 0) {
  parts.push(`letter-spacing:${run.spacing}px`)
}
const styleAttr = parts.length > 0 ? ` style="${parts.join(';')}"` : ''
const body =
  run.ink !== undefined
    ? `<span class="term-cell-ink" style="--cell-fit:${run.ink}">${run.chars}</span>`
    : run.chars
content += styleAttr ? `<span${styleAttr}>${body}</span>` : body
```

- [ ] **Step 6: Стили**

В `frontend/src/style.css` из блока `.term-line` **убрать** `letter-spacing: var(--term-cell-delta, 0px)` вместе с его комментарием (он описывает механизм, которого больше нет), `min-height` заменить на точную `height`, добавить `overflow: hidden`. Правило `.term-cell` удалить целиком. Добавить:

```css
/* КАЖДЫЙ ПРОГОН — ПРЯМОУГОЛЬНИК СТРОКИ (ADR-0009).
   Фон СТРОЧНОГО элемента красит content-box, высоту которого задают метрики
   шрифта: на панели владельца 16.5px в строке 19px, отчего логотип из
   пробелов с фоном шёл полосами. `height: 100%` на inline-block — это высота
   строки, и фон заливает ячейку целиком.
   Штраф известен и принят: в самом xterm рядом с этим правилом стоит
   `TODO: find workaround for inline-block (creates ~20% render penalty)`. */
.term-line > span {
  display: inline-block;
  height: 100%;
  vertical-align: top;
}
```

- [ ] **Step 7: Снять зонд из букв `W`**

В `frontend/src/scrollback/cell-metric.ts` удалить `measureNaturalAdvance`, `.cell-metric-probe` из `style.css` и публикацию `--term-cell-delta`; `publishCellMetric` оставляет только `--term-cell-width`. Тесты `cell-metric.test.ts` привести в соответствие.

Причина, записанная в шапке модуля: дельта была ОДНА на строку и калибровалась по одному образцу; теперь трекинг считается для каждого кластера из его собственной продвижки, и общий образец не нужен ни для чего.

- [ ] **Step 8: Врезка**

В `frontend/src/scrollback/blocks.ts` `_freezeVisual` меняет только имя и форму замыкания: сбор кандидатов теперь без `width`, а классификатор возвращает `spacing`.

- [ ] **Step 9: Зелено, и число узлов не выросло**

Run: `cd frontend && ./node_modules/.bin/vitest run src/scrollback src/terminal-links src/frame && ./node_modules/.bin/tsc --noEmit -p tsconfig.test.json`
Run: `PW_PROJECTS=chromium e2e/run-in-container.sh e2e/frozen-line-grid.spec.ts`
Expected: PASS, все десять браузерных.

Затем сравнить дробление на обычном выводе: в браузере на блоке из `ls -la` посчитать `document.querySelectorAll('.cmd-output > .term-line > span').length` до и после (до — на коммите перед этой задачей). Число обязано совпасть: обычный текст одного цвета имеет один `spacing` на всю строку и остаётся одним прогоном. Расхождение — это дефект, а не «так вышло».

- [ ] **Step 10: Коммит**

```bash
git add frontend/src/scrollback/serializer.ts frontend/src/scrollback/serializer.test.ts frontend/src/scrollback/blocks.ts frontend/src/scrollback/cell-metric.ts frontend/src/scrollback/cell-metric.test.ts frontend/src/scrollback/cmd-output-wrap.test.ts frontend/src/style.css e2e/frozen-line-grid.spec.ts
git commit -m "feat(terminal): a run declares its measured tracking, and fills the row (nocx-ec18, ADR-0009)"
```

---

### Task 4: измерить штраф `inline-block`

**Files:** изменений кода нет; результат идёт в тело эпика.

**Acceptance Criteria:**

- Названо число: во сколько обходится прокрутка блока на 5 000 строк до и после задачи 2+3, на одном и том же транскрипте и в одном и том же браузере.
- Если штраф делает прокрутку хуже кадрового бюджета — это основание откатить, и оно записывается, а не замалчивается.

- [ ] **Step 1: Взять транскрипт**

Захват из самой панели: `stty size`, затем `script -qec omp` — так уже делали для `nocx-ec18`, и файл лежит в истории бида. Годится любой вывод от пяти тысяч строк с цветом.

- [ ] **Step 2: Померить обе стороны**

На коммите ПЕРЕД задачей 2+3 и на коммите ПОСЛЕ, в контейнере, одним и тем же спеком: проиграть захват, дождаться заморозки, прокрутить блок от начала до конца через `page.mouse.wheel` и снять `performance.now()` вокруг, плюс число длинных кадров.

xterm называет свой штраф в ~20%. Наша величина может отличаться в обе стороны, потому что у нас прогонов на строку меньше, чем у него ячеек.

- [ ] **Step 3: Записать в эпик**

Числа, а не прилагательные: миллисекунды до, после, разница в процентах, число кадров дольше 16ms.

---

### Task 5: убрать инструмент дрейфа

**Files:**

- Delete: `frontend/src/scrollback/cell-drift.ts`, `frontend/src/scrollback/cell-drift.test.ts`
- Modify: `frontend/src/scrollback/blocks.ts` (врезка), `frontend/src/main.tsx` (установка ручки), `frontend/src/scrollback/serializer.ts` (out-параметр `colsOut`, если после задачи 2+3 его никто не читает)

**Acceptance Criteria:**

- `nocxCellDrift` больше нет ни в бандле, ни в `window`.
- `serializeRange` не несёт параметров, которых никто не читает.
- Рэтчет мёртвых экспортов зелёный без новых записей в baseline.

- [ ] **Step 1: Удалить и проверить, что ничего не отвалилось**

Run: `cd frontend && ./node_modules/.bin/vitest run && ./node_modules/.bin/tsc --noEmit -p tsconfig.test.json && ./node_modules/.bin/eslint . --max-warnings 0`

- [ ] **Step 2: Коммит**

```bash
git commit -m "chore(terminal): retire the drift instrument, its question is answered (nocx-mmn56)"
```

---

### Task 6: закрыть эпик числами

**Acceptance Criteria:**

- `make ci-full` зелёный на слитом дереве.
- В живой панели владельца: логотип omp без полос, рамка целая, корзина вписана.
- Число узлов на обычном выводе не выросло — с цифрами из задачи 2+3 шаг 9.
- Штраф из задачи 4 записан в тело эпика.

- [ ] **Step 1: Полный гейт**

Run: `make ci-full`
Локальный красный `backend` сверять со списком известных расхождений в AGENTS.md, прежде чем ему верить: на этой машине нет `bash 3.2`, и одиннадцать тестов `internal/shellintegration` падают по этой причине, а не по существу.

- [ ] **Step 2: Проверка руками у владельца**

`omp`, выход, посмотреть на логотип и на рамку. Выделить строку статуса мышью и вставить в редактор — должна совпасть с живой областью символ в символ.

- [ ] **Step 3: Закрыть**

```bash
bd close <epic-id> --reason "<числа: узлы до/после, штраф прокрутки, что проверено руками>"
bd dolt push
git push
```
