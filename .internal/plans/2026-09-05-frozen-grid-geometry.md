# Замороженная строка объявляет геометрию: план реализации ADR-0009

> **For agentic workers:** REQUIRED SUB-SKILL: Use beads-superpowers:subagent-driven-development (recommended) or beads-superpowers:executing-plans to implement this plan task-by-task. Each Task becomes a bead (`bd create -t task --parent <epic-id>`). Steps within tasks use checkbox (`- [ ]`) syntax for human readability.

**Goal:** Замороженная строка занимает ровно свои колонки и заливает их фоном целиком, потому что каждая ячейка объявляет измеренную геометрию, а не потому что ко всей строке применена одна угаданная дельта.

**Architecture:** `spacing = колонки × cellWidth − измеренная продвижка(chars, bold, italic)` на ячейку; прогоны сливаются при равенстве атрибутов И `spacing`; у строки есть трекинг по умолчанию, а отличающиеся прогоны его переопределяют. Фон заливает ячейку измеренным вертикальным полем, которое красится на каждом фрагменте перенесённой строки и раскладку не двигает.

**Решение:** [ADR-0009](../../docs/decisions/0009-dom-scrollback-with-explicit-cell-geometry.md). **Спека:** `.internal/specs/2026-09-05-frozen-grid-renderer-design.md`.

**Ревизия 2.** Первая редакция копировала у xterm `inline-block; height: 100%` и точную высоту строки с `overflow: hidden`. Разбор показал, что это ломает перенос и горизонтальную прокрутку, а замер в обоих движках показал, что копировать это не нужно вовсе. Что было неверно — в разделе «Что отменил замер».

## Что отменил замер

Замерено 2026-09-05 в обоих движках: перенесённый прогон из 800 символов с вертикальным полем 8px.

|                              | строка | content-box | фрагментов | высота каждого |
| ---------------------------- | ------ | ----------- | ---------- | -------------- |
| WKWebView (панель владельца) | 19     | 16.5        | 6          | 32.5           |
| Linux-контейнер              | 20     | 17          | 6          | 33             |

**Вертикальное поле красится на КАЖДОМ фрагменте перенесённой строки, и `box-decoration-break: clone` для этого не нужен** — числа одинаковы с ним и без него. Поле у строчного элемента не влияет на высоту строки: оно только красит.

Отсюда отменены три вещи из первой редакции:

- **`inline-block` на каждом прогоне** — вместе с ним двадцатипроцентный штраф, о котором xterm пишет в своём TODO, смена единицы переноса и изменение топологии `Range.getClientRects()`, на котором стоят существующие тесты. xterm может себе это позволить, потому что **его строки не переносятся никогда**; наши переносятся.
- **Точная высота строки** — не нужна.
- **`overflow: hidden`** — не нужен и вреден: он отрезал бы горизонтальное переполнение, которым живёт широкая строка (`.cmd-output` достаёт до неё через `overflow-x: auto`), и всё ниже сгиба при включённом переносе.

Величина поля — половина разницы «строка минус content-box»: 1.25px на macOS, 1.5px в контейнере. **Разная, поэтому публикуется из замера, а не пишется в стилях.**

## Global Constraints

- **Одна проходка ячеек (AD-8).** `collectRunsOf` остаётся единственным обходом.
- **Семантические эмиссии не меняются побайтово.** `serializeRangeSGR` и `serializeRangeText` — тест есть.
- **jsdom раскладку не считает.** Ширина, высота и заливка — только в браузере; разметка — в vitest.
- **Тайпчек по `tsconfig.test.json`.** `tsconfig.json` тестовые файлы не покрывает и остаётся зелёным при ошибке типа в тесте. Бинарники точно: `./node_modules/.bin/tsc`, `./node_modules/.bin/vitest`.
- **Корневые зависимости нужны хуку:** `npm ci && cd frontend && npm ci` в свежем дереве.
- **Каждый коммит зелёный.** Промежуточного состояния с красным тайпчеком быть не должно; если задача не собирается сама по себе, она слита с соседней.
- **Геометрия обещана строке, которая помещается в панель.** Строка, которую пользователь попросил перенести, — текст за сгибом, и выравнивание колонок там не обещано.

## Осознанно вынесено за рамки

- **Восстановленный блок.** `serializeRangeSGR` ширин не несёт; sidecar — отдельный бид.
- **`nocx-4ff.26`** — блок сериализуется в момент `D` при буфере в 10 000 строк. Это про то, КОГДА делается копия.
- **BiDi, Sixel, OSC 8, стили подчёркивания** — явная геометрия делает их адресуемыми, но каждый остаётся своей задачей.

---

### Task 1: у замороженной строки появляется свой класс

**Files:**

- Modify: `frontend/src/scrollback/serializer.ts`, `serializer.test.ts`
- Modify: `frontend/src/scrollback/restored-block.ts`, `restored-block.test.ts`
- Modify: `frontend/src/style.css`, `frontend/src/scrollback/cmd-output-wrap.test.ts`

**Interfaces:** produces — замороженная строка эмитится как `<span class="term-line term-grid">`.

**Почему первой.** `.term-line` сейчас несёт два смысла: строку сетки и строку прозы ассистента (`answer-body.ts:259` рисует этим же классом переносимый markdown, таблицы и огороженный код). Любое правило геометрии, написанное на `.term-line`, ударит по ним. Класс **добавляется**, а не заменяется, поэтому ни один существующий селектор не ломается и задача зелёная сама по себе.

**Acceptance Criteria:**

- `serializeRange` и `restored-block.bodyToHTML` эмитят `class="term-line term-grid"`.
- `answer-body.ts` НЕ трогается: проза остаётся с одним `.term-line`.
- Существующие тесты, утверждающие разметку дословно, обновлены; ни один не ослаблен.
- Правил на `.term-grid` пока нет — класс появляется пустым, под задачи 2 и 3.

- [ ] **Step 1: Обновить тесты разметки**

Найти все дословные утверждения: `grep -rn '<span class="term-line"' frontend/src e2e | grep -v node_modules`. В тестах, относящихся к ЗАМОРОЖЕННОМУ выводу и к восстановленному, ожидание становится `<span class="term-line term-grid">`. В тестах ответа ассистента — остаётся как есть.

- [ ] **Step 2: Убедиться, что падают**

Run: `cd frontend && ./node_modules/.bin/vitest run src/scrollback src/terminal-links`
Expected: FAIL на обновлённых ожиданиях.

- [ ] **Step 3: Эмитить класс**

В `serializer.ts` — везде, где строится `<span class="term-line">`, включая три ранних возврата в `serializeLine`. В `restored-block.ts` — в `bodyToHTML` и в строке `cmd-output-evicted`.

- [ ] **Step 4: Зелено и коммит**

```bash
cd frontend && ./node_modules/.bin/vitest run && ./node_modules/.bin/tsc --noEmit -p tsconfig.test.json
git commit -am "refactor(terminal): the frozen row gets a class of its own (ADR-0009)"
```

---

### Task 2: фон заливает ячейку — полосы уходят

**Files:**

- Modify: `frontend/src/scrollback/cell-metric.ts`, `cell-metric.test.ts`
- Modify: `frontend/src/style.css`, `frontend/src/scrollback/cmd-output-wrap.test.ts`
- Modify: `e2e/frozen-line-grid.spec.ts`

**Interfaces:** produces — `publishRunPadding(container, cellHeight, measure?)` публикует `--term-run-pad`.

**Почему отдельно.** Это чинит видимый дефект — полосы на логотипе — и не зависит ни от чего в задаче 3. Владелец получает результат раньше.

**Acceptance Criteria:**

- `--term-run-pad` публикуется как `(высота строки − content-box) / 2`, измеренная зондом в том же контексте, что и ширина ячейки.
- Прогон под `.term-grid`, несущий фон, получает это поле; раскладка не двигается.
- Фон цветного прогона по высоте равен высоте строки (расхождение меньше 0.5px).
- Утверждение держится и на ПЕРЕНЕСЁННОЙ строке: каждый фрагмент залит.
- Проза ассистента не задета.

- [ ] **Step 1: Браузерный тест**

Дописать в `e2e/frozen-line-grid.spec.ts`:

```ts
test('a coloured cell fills the row, and keeps filling it after a fold', async ({ page }) => {
  // Форма логотипа omp: пробелы с background-color. Фон СТРОЧНОГО элемента
  // красит content-box, высоту которого задают метрики шрифта — 16.5px в
  // строке 19px на панели владельца, отчего между рядами оставались полосы.
  await page.goto('/')
  await promptReady(page)

  const marker = `BG-${Date.now().toString(36)}`
  // Три коротких ряда — проверяют заливку; один длинный — что она переживает
  // перенос, потому что вертикальное поле красится на каждом фрагменте.
  await page.keyboard.type(
    `printf '\\033[41m      \\033[0m\\n\\033[41m      \\033[0m\\n\\033[41m%s\\033[0m\\n' ` +
      `"$(printf ' %.0s' $(seq 1 $(( $(tput cols) * 3 ))))" # ${marker}`,
  )
  await page.keyboard.press('Enter')
  await expect(page.locator('.cmd-block', { hasText: marker }).first()).toBeVisible({
    timeout: 15_000,
  })

  const m = await page.evaluate((k) => {
    const b = Array.from(document.querySelectorAll('.cmd-block')).find((el) =>
      (el.textContent ?? '').includes(k),
    )
    const rows = Array.from(b?.querySelectorAll<HTMLElement>('.cmd-output > .term-grid') ?? [])
    const painted = rows
      .map((r) => r.querySelector<HTMLElement>('span[style*="background"]'))
      .filter((s): s is HTMLElement => s !== null)
    if (painted.length < 3) return { gap: -1, foldedGaps: [] as number[] }
    const rowH = parseFloat(getComputedStyle(rows[0]).lineHeight)
    const gap = rowH - painted[0].getBoundingClientRect().height
    // Последний прогон перенесён: у него несколько фрагментов, и каждый
    // обязан быть залит на всю высоту строки.
    const folded = Array.from(painted[painted.length - 1].getClientRects())
    return { gap, foldedGaps: folded.map((r) => +(rowH - r.height).toFixed(2)) }
  }, marker)

  expect(m.gap).toBeGreaterThanOrEqual(0)
  expect(m.gap).toBeLessThan(0.5)
  expect(m.foldedGaps.length).toBeGreaterThan(1)
  for (const g of m.foldedGaps) expect(Math.abs(g)).toBeLessThan(0.5)
})
```

- [ ] **Step 2: Красное**

Run: `PW_PROJECTS=chromium e2e/run-in-container.sh e2e/frozen-line-grid.spec.ts`
Expected: FAIL, `gap` около 3px в контейнере.

- [ ] **Step 3: Мерить и публиковать**

В `frontend/src/scrollback/cell-metric.ts` рядом с `publishRowPitch`:

```ts
/** Вертикальное поле, которым фон прогона дотягивается до границ строки.
 *
 *  Фон СТРОЧНОГО элемента красит content-box, высоту которого задают метрики
 *  шрифта, а не line-height: измерено 16.5px в строке 19px под WKWebView и
 *  17 в 20 в контейнере. Разница делится пополам, потому что content-box
 *  центрирован в строке полулидингом — тогда краска дотягивается ровно до
 *  обеих границ, а соседние ряды сходятся встык.
 *
 *  ПОЛЕ У СТРОЧНОГО ЭЛЕМЕНТА НЕ ВЛИЯЕТ НА ВЫСОТУ СТРОКИ — оно только красит,
 *  и красится на КАЖДОМ фрагменте перенесённой строки. Это и есть причина,
 *  по которой xterm-овский `inline-block; height: 100%` сюда не переносится:
 *  он даёт тот же фон ценой атомарных инлайнов, а его строки не переносятся
 *  никогда, наши переносятся. */
export function publishRunPadding(
  container: HTMLElement,
  cellHeight: number,
  measure: (container: HTMLElement) => number = measureContentBox,
): number | null {
  if (!Number.isFinite(cellHeight) || cellHeight <= 0) return null
  const content = measure(container)
  if (!Number.isFinite(content) || content <= 0) return null
  const pad = Math.max(0, Math.round(((cellHeight - content) / 2) * 1e4) / 1e4)
  container.style.setProperty('--term-run-pad', `${pad}px`)
  return pad
}
```

`measureContentBox` — зонд той же формы, что уже есть в этом модуле: скрытый `.cell-metric-probe` с одним символом, читается высота его рект. Ноль означает «мерить негде» — тогда не публикуем ничего, и блок ведёт себя как сегодня.

Позвать из `controller.ts` рядом с `publishRowPitch`.

- [ ] **Step 4: Стиль**

```css
/* ФОН ЗАЛИВАЕТ ЯЧЕЙКУ, А НЕ КОРОБКУ ШРИФТА (ADR-0009).
   Фон строчного элемента красит content-box, который ниже строки на 2.5px
   под WKWebView и на 3 в контейнере, отчего логотип из пробелов с фоном шёл
   полосами. Поле замерено и опубликовано (cell-metric.ts); у строчного
   элемента оно не влияет на раскладку и красится на КАЖДОМ фрагменте
   перенесённой строки — проверено в обоих движках.
   Правило висит на .term-grid, а не на .term-line: вторым классом рисуется
   проза ассистента, и поле там ни к чему. */
.term-grid > span {
  padding-block: var(--term-run-pad, 0px);
}
```

- [ ] **Step 5: Зелено и коммит**

Run: оба набора плюс контейнерный спек.

```bash
git commit -am "fix(terminal): a frozen cell's background fills the row, not the font box (ADR-0009)"
```

---

### Task 3: трекинг по ячейке, с умолчанием на строке

**Files:**

- Modify: `frontend/src/scrollback/cell-fit.ts`, `cell-fit.test.ts`
- Modify: `frontend/src/scrollback/serializer.ts`, `serializer.test.ts`
- Modify: `frontend/src/scrollback/blocks.ts`
- Modify: `frontend/src/scrollback/cell-metric.ts`, `cell-metric.test.ts`, `controller.test.ts`
- Modify: `frontend/src/style.css`, `cmd-output-wrap.test.ts`
- Modify: `e2e/frozen-line-grid.spec.ts`
- Modify: `frontend/src/scrollback/sgr-read.test.ts` (равенство живой и восстановленной разметки)

**Interfaces:**

- `cell-fit`: `advanceOf(chars, face): number | null` вместо `boxOf`; `FitCandidate` теряет `width`.
- `serializer`: `geometryOf(chars, columns, attrs) => { spacing: number; ink?: number } | null`.

**Acceptance Criteria:**

- Ячейка вносит `продвижка + spacing = колонки × cellWidth`.
- **Пустая ячейка проходит через классификатор так же, как непустая.** Логотип — это пробелы с фоном; без этого он не выровняется.
- У строки есть **умолчание трекинга**: самый частый `spacing` строки ставится на неё, и только отличающиеся прогоны переопределяют его. Иначе неокрашенный прогон, сегодня голый текстовый узел, станет `<span>` на каждой строке.
- Ячейка с отрицательным `spacing` не сливается ни с чем: `transform` на прогоне из k ячеек ужал бы и расстояния между ними.
- `advance` проверяется на `Number.isFinite`, а не только на `> 0`: иначе `letter-spacing:-Infinitypx`.
- `.term-cell` и глобальный `--term-cell-delta` удалены; зонд из 64 букв `W` удалён.
- **Восстановленный блок получает явный запасной вариант**, а не молча теряет коррекцию, которая у него есть сегодня: пока нет sidecar, он рендерится БЕЗ трекинга, и это утверждается тестом, а не подразумевается.
- Восемь существующих браузерных тестов мигрированы с `.term-cell` на прогоны; их пользовательские утверждения сохранены дословно.
- Число узлов на детерминированной фикстуре обычного текста не выросло.

- [ ] **Step 1: Тест на пустую ячейку — первым, он важнее прочих**

```ts
it('спрашивает геометрию и у пустой ячейки', () => {
  // Логотип omp — пробелы с background-color. Ветка chars.length === 0
  // классификатор не звала вовсе, поэтому такие ячейки лежали натуральной
  // шириной пробела, а не шириной колонки. Без этого выравнивание цветных
  // блоков не чинится никаким фоном.
  const seen: Array<[string, number]> = []
  const lines = [lineWith({ chars: '', width: 1 }, { chars: '', width: 1 })]
  serializeRange(
    DEFAULT_SNAPSHOT,
    (y) => lines[y],
    0,
    0,
    undefined,
    (chars, columns) => {
      seen.push([chars, columns])
      return { spacing: 0.5 }
    },
  )
  expect(seen).toEqual([
    [' ', 1],
    [' ', 1],
  ])
})
```

Пустая ячейка предъявляется классификатору как `' '` — то, чем она и рисуется. Обрезка хвостовых пробелов при этом не меняется: она работает по символам уже собранного рана.

- [ ] **Step 2: Остальные тесты — умолчание строки, отрицательный spacing, Infinity, восстановление**

```ts
it('ставит умолчание на строку и переопределяет только отличающееся', () => {
  // Иначе каждая строка обычного текста получает лишний <span>. xterm держит
  // общий default ровно поэтому и оценивает выигрыш примерно в 10%.
  const lines = [makeLine('aaaa⬢aaaa')]
  const html = serializeRange(
    DEFAULT_SNAPSHOT,
    (y) => lines[y],
    0,
    0,
    undefined,
    (chars) => (chars === '⬢' ? { spacing: -1.3, ink: 0.86 } : { spacing: 0.43 }),
  )
  expect(html).toContain('class="term-line term-grid" style="letter-spacing:0.43px"')
  // Латиница вокруг остаётся голым текстом, обёрнут только отличающийся.
  expect(html).toMatch(/>aaaa<span[^>]*letter-spacing:-1\.3px/)
})

it('не сливает ячейку, чья краска шире её колонок', () => {
  const lines = [makeLine('⬢⬢')]
  const html = serializeRange(
    DEFAULT_SNAPSHOT,
    (y) => lines[y],
    0,
    0,
    undefined,
    () => ({
      spacing: -1.3,
      ink: 0.86,
    }),
  )
  expect(html.match(/term-cell-ink/g)).toHaveLength(2)
})

it('отвергает нечисловой замер', () => {
  // Бесконечность прошла бы проверку `> 0` и дала бы
  // letter-spacing:-Infinitypx.
  const fit = createCellFit(
    () => containerWith(8),
    batch(() => Infinity),
  )
  fit.begin()
  fit.warm([{ chars: '⬢', face: REGULAR }])
  expect(fit.advanceOf('⬢', REGULAR)).toBeNull()
})
```

- [ ] **Step 3: Красное**

Run: `cd frontend && ./node_modules/.bin/vitest run src/scrollback`

- [ ] **Step 4: Реализация**

`cell-fit` сводится к кэшу ширин: `advanceOf` отдаёт замер, вердиктов нет, быстрый путь по ASCII удаляется (он существовал, пока дельта была одна на строку; теперь ответ для буквы — это её продвижка, и её надо померить). Проверка становится `Number.isFinite(advance) && advance > 0`. Из сигнатуры кэша уходит `--term-cell-delta`.

`collectRunsOf` зовёт `geometryOf` в ОБЕИХ ветках — и для пустой ячейки, предъявляя `' '`. Слияние требует равенства атрибутов и `spacing`; ячейка с `ink` не сливается никогда.

`serializeRange` считает самый частый `spacing` строки, ставит его на `.term-grid` как `letter-spacing` и переопределяет только прогоны, чей `spacing` отличается. Прогон без переопределения и без атрибутов остаётся голым текстом, как сегодня.

`blocks.ts` собирает кандидатов без `width` и передаёт замыкание, считающее `spacing` и `ink` из `cellWidth`.

`cell-metric.ts` перестаёт публиковать `--term-cell-delta` и теряет зонд из 64 букв `W`: общий образец больше ни для чего не нужен.

`restored-block.ts` рендерит без трекинга и **говорит об этом в комментарии**, называя бид на sidecar.

- [ ] **Step 5: Мигрировать восемь браузерных тестов**

`rowShape` собирает не `.term-cell`, а прогоны с переопределённым трекингом; тест базовой линии строит прогон вместо коробки; тест краски ищет `.term-cell-ink` без родителя `.term-cell`. **Утверждения не ослабляются**: что именно должно быть верно, остаётся дословно, меняется только то, у чего это спрашивают.

- [ ] **Step 6: Посчитать узлы**

На детерминированной фикстуре — не на `ls -la`, который зависит от содержимого каталога:

```bash
printf 'plain ascii line %s\n' $(seq 1 200)
```

В браузере до и после: `document.querySelectorAll('.cmd-output *').length` плюс число текстовых узлов. Обязано совпасть. Расхождение — дефект, а не следствие.

- [ ] **Step 7: Зелено и коммит**

---

### Task 4: измерить, чего это стоило

**Acceptance Criteria:** названы числа — холодная заморозка, тёплая, блок высокой кардинальности, и прокрутка. Если хоть одно вышло за кадровый бюджет, это записано как основание, а не замолчано.

Первая заморозка меряет около сотни кластеров латиницы на начертание одним пакетом — быстрый путь по ASCII удалён. Мерить надо **заморозку**, а не только прокрутку: раскладка происходит там.

- [ ] **Step 1: Спек, который остаётся в репозитории**

`e2e/frozen-grid-perf.spec.ts`, не одноразовый скрипт: иначе число в теле бида невоспроизводимо. Фиксированный viewport, прогрев, три прогона, интервалы кадров через `requestAnimationFrame`, а не один `performance.now()` вокруг `page.mouse.wheel`.

- [ ] **Step 2: Четыре замера**

Холодная заморозка обычного ASCII; повторная с тёплым кэшем; блок с высокой кардинальностью (CJK плюс эмодзи); прокрутка блока на 5000 строк.

- [ ] **Step 3: Записать в эпик** — миллисекунды, а не прилагательные.

---

### Task 5: убрать инструмент дрейфа

**Files:** delete `cell-drift.ts`, `cell-drift.test.ts`; modify `blocks.ts`, `main.tsx`, `serializer.ts` (out-параметр `colsOut`, если его больше никто не читает).

**Не раньше задачи 4:** пока не измерено, инструмент — единственный способ увидеть дрейф на живой панели.

- [ ] **Step 1: Удалить, прогнать `vitest`, `tsc`, `eslint --max-warnings 0`.**
- [ ] **Step 2: Коммит.**

---

### Task 6: закрыть эпик числами

- [ ] **Step 1:** `make ci-full`. Локальный красный `backend` сверять со списком известных расхождений: на этой машине нет `bash 3.2`.
- [ ] **Step 2:** У владельца: логотип omp без полос, рамка целая, корзина вписана, выделение строки статуса совпадает с живой областью символ в символ.
- [ ] **Step 3:** Закрыть с числами из задач 3 и 4.
