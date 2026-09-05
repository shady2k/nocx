# Разреженные коробки по колонкам для замороженного блока (nocx-ec18)

> **For agentic workers:** REQUIRED SUB-SKILL: Use beads-superpowers:subagent-driven-development (recommended) or beads-superpowers:executing-plans to implement this plan task-by-task. Each Task becomes a bead (`bd create -t task --parent <epic-id>`). Steps within tasks use checkbox (`- [ ]`) syntax for human readability.

**Goal:** Строка замороженного блока занимает ровно столько же пикселей, сколько её колонок в сетке xterm, поэтому рамка TUI в блоке выглядит так же, как в живой области.

**Architecture:** Обычный текст течёт потоком с существующей коррекцией `letter-spacing` — она верна для одноколоночного кластера, чья натуральная продвижка равна измеренной зондом, и это подавляющее большинство вывода. Ячейка, чей измеренный шаг не сходится с `getWidth() × cellWidth`, заворачивается в атомарную inline-коробку фиксированной ширины. Классификатор — отдельный владелец поведения «ложится ли этот кластер на сетку»; заморозка сперва собирает у него ВСЕ вердикты одним пакетом, потом сериализует по тёплому кэшу.

**Tech Stack:** TypeScript, xterm.js `IBufferLine`/`IBufferCell`, vitest (jsdom), Playwright (`e2e/run-in-container.sh`).

**Ревизия 5 (2026-09-05).** Четыре круга состязательного разбора. Что было неверно и почему — раздел «История правок»; читать обязательно, иначе решения выглядят произвольными.

## Global Constraints

- **Одна проходка ячеек (AD-8).** `collectRunsOf` в `frontend/src/scrollback/serializer.ts` остаётся ЕДИНСТВЕННЫМ обходом ячеек. Он вызывается дважды за заморозку (сбор кандидатов, затем сериализация), но реализация одна — второй функции, знающей, как ходить по ячейкам, не появляется.
- **Семантические эмиссии не меняются побайтово.** `serializeRangeSGR` и `serializeRangeText` зовут `collectRunsOf` без классификатора (serializer.ts:578 и :611), поэтому `box` у них всегда `undefined`. Проверяется тестом.
- **Коробка содержит ровно исходный текст.** Ни `::before`-дублей, ни zero-width символов, ни второй скрытой копии: выделение, доступность и буфер обмена читают один и тот же узел.
- **Классификация по `cell.getChars()` целиком.** Ячейка не равна кодпоинту: U+1F5D1 — суррогатная пара в одной ячейке, база плюс VS16 — два кодпоинта в одной, ZWJ — много. И кодпоинт не определяет ни выбранный face, ни продвижку.
- **Ширина коробки берётся из `--term-cell-width`,** а не запекается в пиксели: блок обязан перепитчиваться при смене метрики, как это уже делает `letter-spacing`.
- **jsdom раскладку не считает.** Утверждение о ширине, высоте или базовой линии — только в браузере (`e2e/`). Утверждение о разметке — в vitest.
- **Измеренный эталон (macOS, панель владельца, 2026-09-05):** ячейка 8px; U+1F5D1 — 13.572, U+27F3 — 9.087, U+27F2 — 9.087, U+2B22 — 8.903. Сумма промахов 8.649px = 1.081 колонки на строке в 152 колонки; дрейф был на 1 строке из 25.

## История правок

Пять редакций и четыре разбора между ними. Ниже — только то, что было НЕВЕРНО, с причиной. Без этого следующий читатель повторит.

**Круг 1, блокирующее.**

1. **Зонд мерил бы не тем шрифтом.** Проверено по коду: `font-family: var(--font-family-mono)` стоит на `.cmd-output` (style.css:1300), `.term-line` даёт только `font-size` (style.css:1328), а `.scrollback-inner` шрифта не имеет вовсе (style.css:870). Зонд, повешенный на контейнер с классом `.term-line`, унаследовал бы UI-шрифт, и КАЖДЫЙ вердикт был бы недостоверен. Заодно вскрылось враньё в комментарии `cell-metric.ts`: он утверждает, что его зонд наследует шрифт от контейнера — не наследует, работает из-за явного `font-family` в правиле `.cell-metric-probe` (style.css:1503). Правится задачей 3.
2. **`overflow: hidden` уводит базовую линию.** Inline-block с не-`visible` overflow берёт базовую линию по нижнему краю margin-бокса — правило CSS. Заменено на `clip-path: inset(0)`, который режет краску на отрисовке и в вычислении базовой линии не участвует.
3. **Подсветка ссылок может расщепить коробку.** `decorateLinks` строит `Range` и делает `extractContents` (decorate.ts:49). Инструмент дрейфа этого не поймал бы: он меряет ДО декорации.
4. **Лигатуры и кернинг ломают быстрый путь по ASCII.** DOM волен сшить `->`, `==`, `ffi` иначе, чем xterm рисует по ячейкам. Глушатся явно.
5. **Начертание не входило в ключ кэша.** Кластер может ложиться в regular, не ложиться в bold, уйти в другой fallback в italic.

**Круг 2, блокирующее.**

6. **Сигнатура кэша снималась не с того узла.** Замер уже делался правильным шрифтом, а ключ описывал бы UI-шрифт контейнера. Тогда смена терминального шрифта не меняла бы ключ, и кэш оставался бы «верным по документам». Сигнатура теперь снимается с самого зонда.
7. **Тест базовой линии был вакуумным.** Он сравнивал верх коробки с ректом `Range` по ВСЕЙ строке — а этот рект сама коробка и определяет. Теперь сравнение с ректом конкретного соседнего текстового узла.
8. **Тест ссылок не выполнял собственных критериев** и содержал прямую ошибку: третий случай не содержал `tail`, а общая проверка требовала `tail`. Переписан на сравнение с эталонной строкой.
9. **`boxes > 0` был недетерминирован.** План сам говорил, что в контейнере глифы могут лечь на сетку, и тут же требовал наличия коробок. Разведено на два теста: механизм проверяется при заведомо промахивающейся метрике, геометрия — при настоящей.
10. **Зонд нарушал владение контейнером.** `BlockManager._own()` — единственный вход в `.scrollback-inner`, а `clearAll()` удаляет только своё. Зонд пережил бы очистку. Теперь у `CellFit` есть `dispose()`, а хост создаётся лениво, так что его удаление посторонним кодом безвредно.
11. **Нулевой замер объявлял бы коробку каждому кластеру.** Полноэкранный режим гасит прямых детей контейнера (style.css:1018), погашенный зонд меряется в ноль, а ноль отстоит от любой ячейки дальше порога. Найдено мной при перечитывании ревизии 2, подтверждено разбором. Две защиты: `display:block` инлайном и отказ кэшировать неположительный замер.
12. **Пакет вместо потолка.** Разбор настоял, и он прав: 64 последовательных «записать в DOM → прочитать рект» — это до 64 принудительных раскладок на критическом пути заморозки, а сверх потолка блок молча оставался бы неправильным. Пакет: все записи, потом все чтения — ОДНА раскладка. Второго обхода буфера это не требует в смысле AD-8 — тот же `collectRunsOf`, вызванный с дешёвым сборщиком.
13. **`LRU` был на деле FIFO** — чтение из `Map` порядка вставки не меняет. Сделан настоящим LRU.
14. **Сериализатор принимал ответ «1 колонка» для ячейки шириной 2.** Проверять надо `claimed === columns`.
15. **«Запас на три порядка» — арифметическая ошибка.** 0.9px против порога 0.05px — это 18 раз, а не 1000.
16. **Бид про переклассификацию был сформулирован слишком узко.** Это не только смена шрифта: `cellWidth` меняется при смене DPR уже сегодня. И старая лишняя коробка теперь может ОБРЕЗАТЬ глиф через `clip-path` — то есть результат способен стать хуже прежнего поведения, а не просто вернуться к нему.

**Где план сознательно расходится с разбором.**

- **`invalidate()` не возвращается.** Разбор предлагал звать инвалидацию после успешной публикации метрики; сигнатура в ключе строго лучше, потому что устаревшая запись просто перестаёт находиться и гонка невозможна по построению. Разбор с этим согласился, потребовав лишь снимать сигнатуру с правильного узла — принято (пункт 6).
- **Один кластер при замере, без повторов.** Разбор принял.
- **Перенос трекинга с `.term-line` на обёртки ранов отвергнут** — переписывает DOM всех строк ради проблемы на стыке коробки, которой может и не быть. Сначала замер, потом узкая компенсация. Разбор принял.
- **Переклассификация уже лежащих блоков — бид, не задача.** Разбор принял как follow-up, потребовав расширить формулировку — принято (пункт 16).

## Расхождение с записанными критериями nocx-ec18 — прочитать до начала

Тело бида требует: «letter-spacing как средство выравнивания сетки удалён вместе с зондом W и `--term-cell-delta`».

**Этот план делает обратное — намеренно.** Критерий писался, когда причина считалась одной. Замер показал две независимые оси:

- **(а) шаг сетки против натуральной продвижки шрифта** — xterm округляет ячейку до целых device-пикселей (8px), DOM кладёт 8.4287. `letter-spacing` гасит это точно для одноколоночного кластера с натуральной продвижкой, равной измеренной зондом. Это 24 строки из 25.
- **(б) чужая продвижка глифа, ушедшего в другой шрифт** — остаётся при любом идеально совпавшем базовом шаге. На замеренной строке её дали ровно четыре кластера, и сумма их промахов равна дрейфу строки.

Удалить `letter-spacing` — вернуть ось (а) на все строки, чтобы починить ось (б) на одной. **Критерий переписывается ДО начала работы** (задача 0).

## Осознанно вынесено за рамки

- **Восстановленный блок остаётся приблизительным.** `serializeRangeSGR` геометрию не выражает и не должен. Бид — задача 5, ДО закрытия дефекта.
- **`frame/mint.ts` теряет `getWidth()`.** Другая поверхность, бид — задача 5.
- **Уже лежащие в DOM блоки не переклассифицируются при смене полной сигнатуры.** Бид — задача 4. Это законный follow-up ИМЕННО ПОТОМУ, что клипа нет: устаревшая коробка держит верную ширину и ничего не прячет.
- **Инструмент дрейфа (`cell-drift.ts`) не удаляется.** Он — средство проверки задачи 6; его судьбу решает `nocx-mmn56`.

---

### Task 0: Привести критерии бида в соответствие с замером

**Files:** изменений кода нет.

**Acceptance Criteria:**

- Критерий про удаление `letter-spacing` заменён на критерий, отражающий две оси.
- Замер 2026-09-05 записан в тело как основание.
- Опубликовано.

- [ ] **Step 1: Прочитать текущее тело целиком**

```bash
bd show nocx-ec18
```

Правка описания ЗАМЕЩАЕТ, а не дописывает — прочитать старое до последней строки, иначе прошлые решения будут уничтожены.

- [ ] **Step 2: Переписать секцию критериев**

Заменить критерий «letter-spacing … удалён» на:

```
• Коррекция --term-cell-delta ОСТАЁТСЯ: она гасит расхождение шага сетки и
  натуральной продвижки шрифта и на замере 2026-09-05 закрывала 24 строки
  из 25. Удалять её — значит вернуть дефект на все строки ради одной.
• Ячейка, чья измеренная продвижка не сходится с getWidth() × cellWidth,
  занимает ровно свои колонки. Проверяется сравнением ШИРИН двух строк
  одинаковой длины в колонках, одна из которых состоит из букв W.
• Комментарии в cell-metric.ts и style.css, утверждающие, что коррекция
  делает N колонок ровно N × cellWidth, исправлены: единица CSS —
  типографский кластер, а не ячейка.
```

- [ ] **Step 3: Опубликовать**

```bash
bd dolt push
```

---

### Task 1: Классификатор «ложится ли ячейка на сетку»

**Files:**

- Create: `frontend/src/scrollback/cell-fit.ts`
- Create: `frontend/src/scrollback/cell-fit.test.ts`

**Interfaces:**

- Produces:
  - `export interface FitFace { bold: boolean; italic: boolean }`
  - `export interface FitCandidate { chars: string; width: number; face: FitFace }`
  - `export interface CellFit { begin(): boolean; warm(candidates: Iterable<FitCandidate>): void; boxColumns(chars: string, width: number, face: FitFace): number | null; dispose(): void; size(): number }`
  - `export function createCellFit(container: () => HTMLElement | null, measure?: BatchMeasure): CellFit`
  - `export type BatchMeasure = (host: HTMLElement, candidates: readonly FitCandidate[]) => number[]`
  - `export const FIT_EPSILON_PX = 0.05`, `export const MAX_CACHE_ENTRIES = 512`

`begin()` открывает заморозку: разрешает хост, снимает `cellWidth` и сигнатуру, возвращает `false`, если мерить негде. `warm()` меряет неизвестных кандидатов ОДНИМ пакетом. `boxColumns()` после этого — чистое чтение кэша.

**Acceptance Criteria:**

- ASCII шириной 1 никогда не попадает ни в кандидаты, ни в замер.
- Кластер с промахом ≥ `FIT_EPSILON_PX` даёт `width`; сходящийся — `null`.
- Двухколоночная ячейка судится по двум колонкам.
- Ключ включает начертание И сигнатуру, снятую С ЗОНДА, а не с контейнера: смена шрифта, размера, трекинга, набора faces или ширины ячейки делает старые вердикты ненаходимыми.
- `warm()` делает РОВНО ОДИН вызов измерителя на пакет, и в нём нет уже известных кандидатов.
- Неположительный замер не кэшируется и коробки не даёт.
- Кэш ограничен `MAX_CACHE_ENTRIES`, вытесняется по-настоящему давно не использованный.
- `boxColumns` без `begin()` возвращает `null`.
- `dispose()` убирает зонд из DOM.

- [ ] **Step 1: Написать падающие тесты**

Создать `frontend/src/scrollback/cell-fit.test.ts`:

```ts
// @vitest-environment jsdom
// Владелец вопроса «ложится ли эта ячейка на сетку» (nocx-ec18).

import { describe, it, expect, vi } from 'vitest'
import {
  createCellFit,
  FIT_EPSILON_PX,
  MAX_CACHE_ENTRIES,
  type FitCandidate,
  type FitFace,
} from './cell-fit'

const REGULAR: FitFace = { bold: false, italic: false }
const BOLD: FitFace = { bold: true, italic: false }

/** Контейнер несёт метрику; шрифт, как в продукте, живёт НЕ на нём, а на
 *  `.cmd-output`, которым будет зонд. jsdom раскладки не считает, поэтому
 *  getComputedStyle читает инлайн. */
function containerWith(cellWidth: number): HTMLElement {
  const el = document.createElement('div')
  el.style.setProperty('--term-cell-width', `${cellWidth}px`)
  document.body.appendChild(el)
  return el
}

/** Измеритель пакета: одно число на кандидата, по правилу. */
function batch(rule: (c: FitCandidate) => number) {
  return vi.fn((_host: HTMLElement, cands: readonly FitCandidate[]) => cands.map(rule))
}

describe('createCellFit', () => {
  it('меряет весь пакет одним вызовом и только неизвестных', () => {
    // Каждый замер — это запись в DOM и чтение ректа. Пакет делает все
    // записи, потом все чтения — одна принудительная раскладка вместо N на
    // критическом пути заморозки.
    const measure = batch(() => 13.572)
    const fit = createCellFit(() => containerWith(8), measure)
    expect(fit.begin()).toBe(true)
    fit.warm([
      { chars: '⬢', width: 1, face: REGULAR },
      { chars: '⟳', width: 1, face: REGULAR },
    ])
    expect(measure).toHaveBeenCalledTimes(1)
    expect(measure.mock.calls[0][1]).toHaveLength(2)

    fit.begin()
    fit.warm([
      { chars: '⬢', width: 1, face: REGULAR },
      { chars: '🗑', width: 1, face: REGULAR },
    ])
    expect(measure).toHaveBeenCalledTimes(2)
    // ⬢ уже известен — в пакет попадает только новый.
    expect(measure.mock.calls[1][1]).toEqual([{ chars: '🗑', width: 1, face: REGULAR }])
  })

  it('запирает кластер, чей шаг не сходится с его колонкой', () => {
    const fit = createCellFit(
      () => containerWith(8),
      batch(() => 13.572),
    )
    fit.begin()
    fit.warm([{ chars: '🗑', width: 1, face: REGULAR }])
    expect(fit.boxColumns('🗑', 1, REGULAR)).toBe(1)
  })

  it('оставляет в потоке кластер, который ложится сам', () => {
    // Псевдографика U+2500 в коробке не нуждается: рамка из неё выглядела
    // верно, а коробка на каждый символ границы — 150 узлов на строку без
    // единого исправленного пикселя.
    const fit = createCellFit(
      () => containerWith(8),
      batch(() => 8.02),
    )
    fit.begin()
    fit.warm([{ chars: '─', width: 1, face: REGULAR }])
    expect(fit.boxColumns('─', 1, REGULAR)).toBeNull()
  })

  it('судит двухколоночную ячейку по ДВУМ колонкам', () => {
    const ok = createCellFit(
      () => containerWith(8),
      batch(() => 16.01),
    )
    ok.begin()
    ok.warm([{ chars: '漢', width: 2, face: REGULAR }])
    expect(ok.boxColumns('漢', 2, REGULAR)).toBeNull()

    const off = createCellFit(
      () => containerWith(8),
      batch(() => 14),
    )
    off.begin()
    off.warm([{ chars: '漢', width: 2, face: REGULAR }])
    expect(off.boxColumns('漢', 2, REGULAR)).toBe(2)
  })

  it('судит начертания порознь', () => {
    // Один кластер может ложиться в regular и не ложиться в bold, а
    // attrsToStyle вешает font-weight на саму коробку — значит вердикт
    // обязан относиться к тому же начертанию.
    const measure = batch((c) => (c.face.bold ? 9.4 : 8.01))
    const fit = createCellFit(() => containerWith(8), measure)
    fit.begin()
    fit.warm([
      { chars: '⬢', width: 1, face: REGULAR },
      { chars: '⬢', width: 1, face: BOLD },
    ])
    expect(fit.boxColumns('⬢', 1, REGULAR)).toBeNull()
    expect(fit.boxColumns('⬢', 1, BOLD)).toBe(1)
  })

  it('не находит вердикт, снятый при другой ширине ячейки', () => {
    // Сигнатура в КЛЮЧЕ, а не отдельный invalidate(): устаревшая запись
    // просто не находится, и «метрику не опубликовали, а кэш уже сбросили»
    // невозможно по построению.
    const measure = batch(() => 13.572)
    const host = containerWith(8)
    const fit = createCellFit(() => host, measure)
    fit.begin()
    fit.warm([{ chars: '🗑', width: 1, face: REGULAR }])
    host.style.setProperty('--term-cell-width', '9px')
    fit.begin()
    fit.warm([{ chars: '🗑', width: 1, face: REGULAR }])
    expect(measure).toHaveBeenCalledTimes(2)
  })

  it('снимает сигнатуру С ЗОНДА, а не с контейнера', () => {
    // Это ошибка ревизии 2, и она была невидима: замер уже делался
    // правильным шрифтом, а ключ описывал бы UI-шрифт контейнера — значит
    // смена ТЕРМИНАЛЬНОГО шрифта не меняла бы ключ и кэш оставался бы
    // «верным по документам».
    const measure = batch(() => 13.572)
    const container = containerWith(8)
    const fit = createCellFit(() => container, measure)
    fit.begin()
    fit.warm([{ chars: '🗑', width: 1, face: REGULAR }])
    const probe = container.querySelector<HTMLElement>('.cell-fit-probe')
    expect(probe).not.toBeNull()
    probe!.style.fontFamily = 'Monaco'
    fit.begin()
    fit.warm([{ chars: '🗑', width: 1, face: REGULAR }])
    expect(measure).toHaveBeenCalledTimes(2)
  })

  it('не считает нулевой замер промахом на всю ячейку', () => {
    // Полноэкранный режим гасит прямых детей контейнера (style.css:1018).
    // Погашенный зонд меряется в ноль, а ноль отстоит от любой ячейки
    // дальше порога — коробку получил бы КАЖДЫЙ кластер. Ответ не
    // кэшируется: следующая заморозка спросит заново.
    const fit = createCellFit(
      () => containerWith(8),
      batch(() => 0),
    )
    fit.begin()
    fit.warm([{ chars: '⬢', width: 1, face: REGULAR }])
    expect(fit.boxColumns('⬢', 1, REGULAR)).toBeNull()
    expect(fit.size()).toBe(0)
  })

  it('никогда не спрашивает про одноколоночный ASCII', () => {
    const measure = batch(() => 999)
    const fit = createCellFit(() => containerWith(8), measure)
    fit.begin()
    fit.warm([
      { chars: 'a', width: 1, face: REGULAR },
      { chars: ' ', width: 1, face: REGULAR },
    ])
    expect(measure).not.toHaveBeenCalled()
    expect(fit.boxColumns('a', 1, REGULAR)).toBeNull()
  })

  it('молчит, пока мерить негде, вместо того чтобы гадать', () => {
    // Коробка по догадке хуже её отсутствия: она СОЗДАЁТ смещение там, где
    // его не было.
    expect(createCellFit(() => null).begin()).toBe(false)
    const noMetric = document.createElement('div')
    document.body.appendChild(noMetric)
    const fit = createCellFit(
      () => noMetric,
      batch(() => 13.572),
    )
    expect(fit.begin()).toBe(false)
    expect(fit.boxColumns('⬢', 1, REGULAR)).toBeNull()
  })

  it('без begin() не отвечает ничего', () => {
    const fit = createCellFit(
      () => containerWith(8),
      batch(() => 13.572),
    )
    expect(fit.boxColumns('🗑', 1, REGULAR)).toBeNull()
  })

  it('раздаёт вердикты по кандидатам, а не одним числом на всех', () => {
    // ЭТОТ ТЕСТ НЕ ЛОВИТ ту ловушку, ради которой он выглядит написанным, и
    // это надо знать: измеритель здесь подставной, а разница между
    // `display:block` и `display:inline-block` внутри хоста с
    // `width: max-content` — вопрос РАСКЛАДКИ, которой в jsdom нет. Тут
    // проверяется только разводка вердиктов по ключам. Настоящую ловушку
    // сторожит браузерный тест «the batch measures each cluster on its own».
    const fit = createCellFit(
      () => containerWith(8),
      (_h, cands) => cands.map((c) => (c.chars === '🗑' ? 13.572 : 8.01)),
    )
    fit.begin()
    fit.warm([
      { chars: '🗑', width: 1, face: REGULAR },
      { chars: '─', width: 1, face: REGULAR },
    ])
    expect(fit.boxColumns('🗑', 1, REGULAR)).toBe(1)
    expect(fit.boxColumns('─', 1, REGULAR)).toBeNull()
  })

  it('не вытесняет то, что положил в этой же заморозке', () => {
    // Один блок с очень пёстрым выводом может дать кандидатов больше, чем
    // граница кэша. Вытеснение внутри warm() съело бы вердикты, снятые
    // несколькими строками выше, и первые ячейки молча остались бы без
    // коробок. Поэтому чистка живёт в begin(), а кэш вправе временно
    // превысить границу ровно на один блок.
    const many = Array.from({ length: MAX_CACHE_ENTRIES + 1 }, (_, i) => ({
      chars: String.fromCodePoint(0x5000 + i),
      width: 1,
      face: REGULAR,
    }))
    const fit = createCellFit(
      () => containerWith(8),
      batch(() => 13.572),
    )
    fit.begin()
    fit.warm(many)
    expect(fit.size()).toBe(MAX_CACHE_ENTRIES + 1)
    expect(fit.boxColumns(many[0].chars, 1, REGULAR)).toBe(1)
    expect(fit.boxColumns(many[many.length - 1].chars, 1, REGULAR)).toBe(1)
    // Следующая заморозка приводит кэш в границы.
    fit.begin()
    expect(fit.size()).toBe(MAX_CACHE_ENTRIES)
  })

  it('снимает слушатель загрузки шрифтов по dispose', () => {
    // Анонимный callback на document.fonts копился бы при каждом
    // пересоздании контроллера. jsdom своего FontFaceSet не имеет, поэтому
    // подставляем свой и убираем после — иначе тест падал бы на undefined,
    // а не на отсутствии снятия.
    const added: string[] = []
    const removed: string[] = []
    const stub = {
      addEventListener: (t: string) => added.push(t),
      removeEventListener: (t: string) => removed.push(t),
    }
    const doc = document as Document & { fonts?: unknown }
    const had = Object.prototype.hasOwnProperty.call(doc, 'fonts')
    const prior = doc.fonts
    Object.defineProperty(doc, 'fonts', { value: stub, configurable: true })
    try {
      const fit = createCellFit(
        () => containerWith(8),
        batch(() => 13.572),
      )
      fit.begin()
      fit.dispose()
      expect(added).toContain('loadingdone')
      expect(removed).toContain('loadingdone')
    } finally {
      if (had) Object.defineProperty(doc, 'fonts', { value: prior, configurable: true })
      else delete doc.fonts
    }
  })

  it('вытесняет по-настоящему давно не использованное', () => {
    // Map хранит порядок ВСТАВКИ, поэтому наивное «удалить первый ключ» —
    // это FIFO, а не LRU: только что прочитанная запись вылетела бы первой.
    const fit = createCellFit(
      () => containerWith(8),
      batch(() => 13.572),
    )
    fit.begin()
    const first = { chars: '　', width: 1, face: REGULAR }
    fit.warm([first])
    for (let i = 1; i < MAX_CACHE_ENTRIES; i++) {
      fit.warm([{ chars: String.fromCodePoint(0x3000 + i), width: 1, face: REGULAR }])
    }
    // Трогаем самую старую — она обязана пережить следующее вытеснение.
    expect(fit.boxColumns('　', 1, REGULAR)).toBe(1)
    fit.warm([{ chars: '䀀', width: 1, face: REGULAR }])
    // warm() НЕ вытесняет: иначе он съел бы вердикты, снятые для этой же
    // заморозки. Размер выходит за границу и возвращается в неё на
    // следующем begin().
    expect(fit.size()).toBe(MAX_CACHE_ENTRIES + 1)
    fit.begin()
    expect(fit.size()).toBe(MAX_CACHE_ENTRIES)
    expect(fit.boxColumns('　', 1, REGULAR)).toBe(1)
  })

  it('убирает зонд из DOM по dispose', () => {
    // BlockManager объявляет _own() единственным входом в контейнер, а
    // clearAll() удаляет только своё. Зонд, оставленный там навсегда, —
    // посторонний прямой ребёнок среди опорных узлов.
    const container = containerWith(8)
    const fit = createCellFit(
      () => container,
      batch(() => 13.572),
    )
    fit.begin()
    fit.warm([{ chars: '⬢', width: 1, face: REGULAR }])
    expect(container.querySelector('.cell-fit-probe')).not.toBeNull()
    fit.dispose()
    expect(container.querySelector('.cell-fit-probe')).toBeNull()
  })

  it('порог назван и мал: коробка на сходящемся глифе безвредна, промах — нет', () => {
    const fit = createCellFit(
      () => containerWith(8),
      batch(() => 8 + FIT_EPSILON_PX * 2),
    )
    fit.begin()
    fit.warm([{ chars: '⬢', width: 1, face: REGULAR }])
    expect(fit.boxColumns('⬢', 1, REGULAR)).toBe(1)
  })
})
```

- [ ] **Step 2: Убедиться, что тесты падают**

Run: `cd frontend && ./node_modules/.bin/vitest run src/scrollback/cell-fit.test.ts`
Expected: FAIL — `Failed to resolve import "./cell-fit"`.

- [ ] **Step 3: Написать модуль**

Создать `frontend/src/scrollback/cell-fit.ts`:

```ts
// ВЛАДЕЛЕЦ ВОПРОСА «ложится ли эта ячейка на сетку» (nocx-ec18).
//
// xterm говорит, сколько колонок занимает ячейка (getWidth()); DOM кладёт
// глиф с продвижкой того шрифта, который для него выбрал браузер. Для
// одноколоночной латиницы эти числа сведены глобальной коррекцией
// --term-cell-delta, применяемой как letter-spacing ко всей строке. Для
// глифа, ушедшего в другой шрифт, единой дельты не существует: U+1F5D1
// меряется 13.572px против ячейки 8, U+27F3 и U+27F2 по 9.087, U+2B22 8.903
// (macOS, 2026-09-05). Сумма их промахов на одной строке дала 8.649px —
// ровно одну колонку, и это разъехавшийся угол рамки.
//
// Здесь решается ТОЛЬКО кого запирать в коробку. Как её нарисовать — вопрос
// стилей (.term-cell), кто её ставит — вопрос сериализатора.
//
// ПОЧЕМУ ПАКЕТОМ. Замер — это запись в DOM и чтение ректа, а чтение после
// записи форсирует раскладку. Поштучно на заморозке это N раскладок в тот
// самый момент, когда блок подменяет живую область. Пакет делает ВСЕ записи,
// потом ВСЕ чтения: одна раскладка независимо от N. Поэтому заморозка сперва
// собирает кандидатов (тем же обходом ячеек, с пустыми атрибутами), греет
// кэш, и только потом сериализует — на этом проходе boxColumns уже чистое
// чтение Map.
//
// ПОЧЕМУ ПО ЗАМЕРУ, А НЕ ПО КОДПОИНТУ. Кодпоинт не определяет ни выбранный
// face (VS15/VS16 переключают представление), ни продвижку: цепочка шрифтов
// различается по машине, обновление ОС меняет метрики.
//
// ПОЧЕМУ НЕ ЗАПЕРЕТЬ ВСЁ НЕ-ASCII БЕЗ ЗАМЕРА. Псевдографика в моноширинном
// шрифте есть и ложится точно; на строке рамки это 150 коробок без единого
// исправленного пикселя.
//
// ГДЕ МЕРЯЕТСЯ — И ЭТО НЕ МЕЛОЧЬ. font-family живёт на .cmd-output
// (style.css), а .term-line задаёт только font-size; контейнер скроллбэка
// шрифта не имеет вовсе. Зонд, повешенный на контейнер с одним классом
// .term-line, унаследовал бы UI-шрифт. Поэтому зонд — настоящий .cmd-output
// с .term-line внутри, и сигнатура кэша снимается С НЕГО, а не с контейнера:
// иначе замер делается правильным шрифтом, а ключ описывает другой.

export const FIT_EPSILON_PX = 0.05

/** Граница кэша. Ключ несёт сигнатуру, поэтому смена шрифта или метрики
 *  оставляет мёртвые записи — их вытесняет эта граница. */
export const MAX_CACHE_ENTRIES = 512

export interface FitFace {
  bold: boolean
  italic: boolean
}

export interface FitCandidate {
  chars: string
  width: number
  face: FitFace
}

export type BatchMeasure = (host: HTMLElement, candidates: readonly FitCandidate[]) => number[]

export interface CellFit {
  /** Открыть заморозку: разрешить зонд, снять ширину ячейки и сигнатуру.
   *  false — мерить негде, вся заморозка пройдёт как сегодня. */
  begin(): boolean
  /** Измерить неизвестных кандидатов одним пакетом. */
  warm(candidates: Iterable<FitCandidate>): void
  /** Чистое чтение кэша. */
  boxColumns(chars: string, width: number, face: FitFace): number | null
  /** Убрать зонд из DOM. */
  dispose(): void
  /** Размер кэша. Для тестов границы. */
  size(): number
}

const PROBE_CLASS = 'cell-fit-probe'
const SIGNATURE_CLASS = 'cell-fit-witness'

/** Одноколоночная латиница — ровно то подмножество, на котором
 *  --term-cell-delta откалиброван. Держится это только потому, что
 *  .term-line глушит лигатуры и кернинг (style.css): иначе DOM мог бы сшить
 *  `->` или `ffi` не так, как xterm рисует их по ячейкам. */
function isCalibratedAscii(chars: string, width: number): boolean {
  return width === 1 && chars.length === 1 && chars.charCodeAt(0) < 0x80
}

/** Зонд: НАСТОЯЩИЙ .cmd-output, чтобы шрифт пришёл из того же правила, что
 *  и у блока. `.cmd-output` вне `.cmd-block` не подпадает под правило
 *  переноса по настройке (:root[data-output-wrap='on'] требует предка
 *  .cmd-block), так что здесь всегда `white-space: pre` — что зонду и нужно. */
function makeProbe(container: HTMLElement): HTMLElement {
  const existing = container.querySelector<HTMLElement>(`:scope > .${PROBE_CLASS}`)
  if (existing) return existing
  const host = container.ownerDocument.createElement('div')
  host.className = `cmd-output ${PROBE_CLASS}`
  // Внутри хоста живёт постоянная `.term-line` — С НЕЁ снимается сигнатура.
  // letter-spacing, font-variant-ligatures и font-kerning объявлены именно
  // на .term-line, а не на .cmd-output: снимая их с хоста, сигнатура
  // описывала бы не тот shaping, которым сделан замер. Это та же ошибка
  // «мерим одним, ключуем другим», что была в ревизиях 1 и 2, только на
  // третьем уровне вложенности.
  // `display:block` ЯВНО, инлайном. Зонд — прямой ребёнок контейнера, а
  // полноэкранный режим гасит всех прямых детей кроме живой области
  // (style.css:1018). Погашенный зонд меряется в ноль, а ноль дальше по
  // коду выглядит как «промахнулся на всю ячейку». Инлайновое объявление
  // перебивает правило из таблицы стилей.
  host.style.cssText =
    'display:block;position:absolute;left:-99999px;top:0;width:max-content;padding:0;white-space:pre;visibility:hidden;pointer-events:none'
  const witness = container.ownerDocument.createElement('span')
  witness.className = `term-line ${SIGNATURE_CLASS}`
  witness.style.display = 'inline-block'
  host.appendChild(witness)
  container.appendChild(host)
  return host
}

/** Постоянная `.term-line` внутри зонда, с которой снимается сигнатура.
 *  Отдельная от измеряемых спанов, потому что те создаются и удаляются
 *  пакетом, а сигнатура нужна до и после. */
function signatureWitness(host: HTMLElement): HTMLElement {
  const found = host.querySelector<HTMLElement>(`.${SIGNATURE_CLASS}`)
  if (found) return found
  const witness = host.ownerDocument.createElement('span')
  witness.className = `term-line ${SIGNATURE_CLASS}`
  witness.style.display = 'inline-block'
  host.appendChild(witness)
  return witness
}

/** ВСЕ ЗАПИСИ, ПОТОМ ВСЕ ЧТЕНИЯ — в этом весь смысл пакета. Меряется ОДИН
 *  кластер на кандидата, не повтор: рект инлайнового бокса это сумма
 *  продвижек и он дробный, а сравнение идёт с порогом 0.05px против промаха
 *  в 0.9px и выше — запас примерно восемнадцатикратный. Повтор дал бы лишний
 *  знак и заодно позволил бы копиям шейпиться между собой, чего в настоящей
 *  строке не происходит. */
const domBatchMeasure: BatchMeasure = (host, candidates) => {
  const doc = host.ownerDocument
  const spans = candidates.map((c) => {
    const span = doc.createElement('span')
    span.className = 'term-line'
    // inline-block, И НЕ block. `display:block` с шириной auto растягивает
    // ребёнка до ширины содержащего блока, а у хоста она `max-content`, то
    // есть ширина САМОГО ШИРОКОГО кандидата — измеритель вернул бы одно и то
    // же число для всех, и весь пакет был бы ложью. inline-block
    // сжимается по содержимому, что и есть искомая продвижка.
    span.style.display = 'inline-block'
    if (c.face.bold) span.style.fontWeight = 'bold'
    if (c.face.italic) span.style.fontStyle = 'italic'
    span.textContent = c.chars
    return span
  })
  // Добавляем и убираем ТОЛЬКО свои спаны: replaceChildren снёс бы вместе с
  // ними постоянную .term-line, с которой снимается сигнатура.
  for (const span of spans) host.appendChild(span)
  const widths = spans.map((s) => s.getBoundingClientRect().width)
  for (const span of spans) span.remove()
  return widths
}

/** Всё, что меняет ответ и не является самим кластером. В КЛЮЧЕ, а не в
 *  отдельном invalidate(): устаревшая запись тогда просто не находится, и
 *  «метрику не опубликовали, а кэш уже сбросили» не может случиться.
 *  Снимается С ЗОНДА — см. шапку. */
function signatureOf(witness: HTMLElement, cellWidth: number, fontEpoch: number): string {
  const cs = getComputedStyle(witness)
  return [
    cellWidth,
    fontEpoch,
    cs.getPropertyValue('--term-cell-delta').trim(),
    cs.fontFamily,
    cs.fontSize,
    cs.fontStretch,
    cs.letterSpacing,
    cs.fontVariantLigatures,
    cs.fontKerning,
    cs.fontFeatureSettings,
  ].join('|')
}

function cellWidthOf(container: HTMLElement): number {
  const value = Number.parseFloat(getComputedStyle(container).getPropertyValue('--term-cell-width'))
  return Number.isFinite(value) && value > 0 ? value : 0
}

export function createCellFit(
  container: () => HTMLElement | null,
  measure: BatchMeasure = domBatchMeasure,
): CellFit {
  const cache = new Map<string, number>()
  let probe: HTMLElement | null = null
  let signature = ''
  let cellWidth = 0

  // ЗАГРУЗКА ШРИФТА НЕ МЕНЯЕТ НИ ОДНОЙ СТРОКИ ВЫЧИСЛЕННОГО СТИЛЯ: то же
  // семейство, тот же размер, другой файл. xterm ждёт document.fonts.ready
  // при монтировании (renderers/xterm.ts), поэтому начальное состояние
  // сигнатуры честное; всё, что грузится позже, ловится эпохой.
  let fontEpoch = 0
  const onFontsLoaded = (): void => {
    fontEpoch += 1
  }
  const fonts = (document as Document & { fonts?: FontFaceSet }).fonts
  fonts?.addEventListener?.('loadingdone', onFontsLoaded)

  const keyOf = (chars: string, width: number, face: FitFace): string =>
    `${signature}|${face.bold ? 'b' : ''}${face.italic ? 'i' : ''}|${width}|${chars}`

  /** Настоящий LRU: Map хранит порядок ВСТАВКИ, поэтому чтение обязано
   *  переставить запись в конец, иначе только что использованная вылетит
   *  первой — это FIFO под именем LRU. */
  const touch = (key: string): number | undefined => {
    const hit = cache.get(key)
    if (hit === undefined) return undefined
    cache.delete(key)
    cache.set(key, hit)
    return hit
  }

  return {
    begin() {
      const element = container()
      if (element === null) return false
      cellWidth = cellWidthOf(element)
      if (cellWidth === 0) {
        signature = ''
        return false
      }
      probe = makeProbe(element)
      signature = signatureOf(signatureWitness(probe), cellWidth, fontEpoch)
      // ВЫТЕСНЕНИЕ ЗДЕСЬ, А НЕ В warm(). Иначе очень пёстрый блок способен
      // вытеснить то, что warm() положил для него же несколькими строками
      // выше, и коробка не появится молча. Между заморозками кэш может
      // временно превышать границу ровно на один блок — это ограничено
      // числом РАЗЛИЧНЫХ кластеров в нём и несопоставимо дешевле, чем
      // потерянный вердикт.
      while (cache.size > MAX_CACHE_ENTRIES) {
        const oldest = cache.keys().next()
        if (oldest.done) break
        cache.delete(oldest.value)
      }
      return true
    },

    warm(candidates) {
      if (probe === null || signature === '') return
      const pending: FitCandidate[] = []
      const seen = new Set<string>()
      for (const c of candidates) {
        if (isCalibratedAscii(c.chars, c.width)) continue
        const key = keyOf(c.chars, c.width, c.face)
        if (seen.has(key) || touch(key) !== undefined) continue
        seen.add(key)
        pending.push(c)
      }
      if (pending.length === 0) return
      const widths = measure(probe, pending)
      for (let i = 0; i < pending.length; i++) {
        const advance = widths[i]
        // НОЛЬ — ЭТО НЕ ЗАМЕР, А ЕГО ОТСУТСТВИЕ, и разница стоит дорого:
        // ноль отстоит от любой ячейки дальше порога, поэтому наивная
        // проверка объявила бы коробку КАЖДОМУ кластеру всякий раз, когда
        // зонд не отрисовался. Не кэшируем — следующая заморозка спросит.
        if (!(advance > 0)) continue
        const c = pending[i]
        const miss = Math.abs(advance - c.width * cellWidth)
        cache.set(keyOf(c.chars, c.width, c.face), miss >= FIT_EPSILON_PX ? c.width : 0)
      }
    },

    boxColumns(chars, width, face) {
      if (signature === '' || isCalibratedAscii(chars, width)) return null
      const verdict = touch(keyOf(chars, width, face))
      // 0 в кэше означает «измерен и ложится сам»; отсутствие означает «не
      // измерен» — оба дают null, но по разным причинам, и обе законны.
      return verdict === undefined || verdict === 0 ? null : verdict
    },

    dispose() {
      // Слушатель снимается ИМЕНОВАННЫМ, иначе при каждом пересоздании
      // контроллера на документе оседает ещё один анонимный callback.
      fonts?.removeEventListener?.('loadingdone', onFontsLoaded)
      probe?.remove()
      probe = null
      signature = ''
      cache.clear()
    },

    size() {
      return cache.size
    },
  }
}
```

- [ ] **Step 4: Убедиться, что тесты проходят**

Run: `cd frontend && ./node_modules/.bin/vitest run src/scrollback/cell-fit.test.ts`
Expected: PASS, 17 тестов.

**Тайпчек — по `tsconfig.test.json`, а НЕ по `tsconfig.json`.** Первый воркер
поймал это ценой одного круга: `tsconfig.json` тестовые файлы не покрывает, так
что он зелен и при ошибке типа в тесте, а `pre-commit` гоняет `tsconfig.test.json`
и коммит не проходит. И бинарник называется точно: `npx tsc` в свежем дереве может
не разрешиться там, где `./node_modules/.bin/tsc` работает.

Run: `cd frontend && ./node_modules/.bin/tsc --noEmit -p tsconfig.test.json`
Expected: пусто.

- [ ] **Step 5: Коммит**

```bash
git add frontend/src/scrollback/cell-fit.ts frontend/src/scrollback/cell-fit.test.ts
git commit -m "feat(terminal): own the question of whether a cell lands on the grid (nocx-ec18)"
```

---

### Task 2: Сериализатор ставит коробку и умеет собирать кандидатов

**Files:**

- Modify: `frontend/src/scrollback/serializer.ts` (`GenericRun` — 317, `collectRunsOf` — 348, `collectRuns` — 410, `serializeRange` — 538)
- Modify: `frontend/src/scrollback/serializer.test.ts`

**Interfaces:**

- Produces:
  - `serializeRange(snapshot, getLine, startLine, endLine, colsOut?, boxColumns?)`, где `boxColumns?: (chars: string, width: number, attrs: CellAttrs) => number | null`.
  - `export function collectFitCandidates(getLine, startLine, endLine, sink: (chars: string, width: number, attrs: CellAttrs) => void): void` — сбор кандидатов ТЕМ ЖЕ обходом.
- Разметка: `<span class="term-cell" data-cols="1">X</span>`.

**Acceptance Criteria:**

- Ячейка, для которой классификатор вернул число, выезжает отдельным раном в `span.term-cell` с `data-cols`.
- Обычные раны склеиваются; с коробкой — никогда, коробки друг с другом — тоже.
- `data-cols` принимается ТОЛЬКО если равен колонкам ячейки: ответ «1» для ячейки шириной 2 отбрасывается.
- Классификатор получает `getChars()` целиком и вызывается один раз на ячейку, включая ячейку со спейсером.
- `collectFitCandidates` выдаёт те же ячейки в том же порядке, что увидит классификатор при сериализации.
- Хвостовые пробелы после коробки обрезаются, `cols` уменьшается; коробка не обрезается.
- Без классификатора вывод не меняется; `serializeRangeSGR`/`serializeRangeText` не меняются.

- [ ] **Step 1: Написать падающие тесты**

Дописать в `frontend/src/scrollback/serializer.test.ts`:

```ts
// ── Коробки по колонкам (nocx-ec18) ────────────────────────────────────────
//
// Коробка — единственный способ задать глифу продвижку: CSS не умеет
// назначить её текстовому кластеру без layout-объекта вокруг него. Здесь
// проверяется РАЗМЕТКА; что она даёт нужную ширину — в e2e.
describe('serializeRange cell boxes', () => {
  const boxEverything = () => 1
  const boxNothing = () => null

  it('оборачивает ячейку, которую классификатор не пропустил', () => {
    const lines = [makeLine('a\u{1F5D1}b')]
    const html = serializeRange(
      DEFAULT_SNAPSHOT,
      (y) => lines[y],
      0,
      0,
      undefined,
      (chars) => (chars === '\u{1F5D1}' ? 1 : null),
    )
    expect(html).toBe(
      '<span class="term-line">a<span class="term-cell" data-cols="1">\u{1F5D1}</span>b</span>',
    )
  })

  it('передаёт классификатору ячейку целиком и по одному разу', () => {
    // Ячейка не равна кодпоинту, а спейсер после широкой не должен породить
    // второй вызов.
    const seen: Array<[string, number]> = []
    const lines = [lineWith({ chars: '漢', width: 2 }, { chars: '', width: 0 }, { chars: 'x' })]
    const cols: number[] = []
    serializeRange(
      DEFAULT_SNAPSHOT,
      (y) => lines[y],
      0,
      0,
      cols,
      (chars, width) => {
        seen.push([chars, width])
        return width === 2 ? 2 : null
      },
    )
    expect(seen).toEqual([
      ['漢', 2],
      ['x', 1],
    ])
    expect(cols).toEqual([3])
  })

  it('отвергает вердикт, не равный колонкам ячейки', () => {
    // «Одна колонка» для ячейки шириной две — это сдвиг, которого в сетке
    // нет. Лучше сегодняшний поток, чем выдуманная геометрия.
    const lines = [lineWith({ chars: '漢', width: 2 }, { chars: '', width: 0 })]
    expect(
      serializeRange(
        DEFAULT_SNAPSHOT,
        (y) => lines[y],
        0,
        0,
        undefined,
        () => 1,
      ),
    ).not.toContain('term-cell')
    expect(
      serializeRange(
        DEFAULT_SNAPSHOT,
        (y) => lines[y],
        0,
        0,
        undefined,
        () => 3,
      ),
    ).not.toContain('term-cell')
    expect(
      serializeRange(
        DEFAULT_SNAPSHOT,
        (y) => lines[y],
        0,
        0,
        undefined,
        () => 2,
      ),
      // Стиль в ожидаемой строке НЕ лишний: lineWith по умолчанию ставит
      // fg: 7 в палитре P16, а это #a9b1d6 против дефолтного #c0caf5,
      // поэтому attrsToStyle выдаёт color и коробка несёт атрибут style.
      // Первая редакция этого утверждения его не ждала и пройти не могла.
    ).toContain('<span class="term-cell" data-cols="2" style="color:#a9b1d6">漢</span>')
  })

  it('никогда не склеивает коробки друг с другом', () => {
    const lines = [makeLine('⬢⬢')]
    const html = serializeRange(DEFAULT_SNAPSHOT, (y) => lines[y], 0, 0, undefined, boxEverything)
    expect(html).toBe(
      '<span class="term-line">' +
        '<span class="term-cell" data-cols="1">⬢</span>' +
        '<span class="term-cell" data-cols="1">⬢</span>' +
        '</span>',
    )
  })

  it('снова склеивает обычные раны после коробки', () => {
    const lines = [makeLine('⬢abc')]
    const html = serializeRange(
      DEFAULT_SNAPSHOT,
      (y) => lines[y],
      0,
      0,
      undefined,
      (chars) => (chars === '⬢' ? 1 : null),
    )
    expect(html).toBe(
      '<span class="term-line"><span class="term-cell" data-cols="1">⬢</span>abc</span>',
    )
  })

  it('обрезает хвостовой отступ после коробки, а саму коробку — нет', () => {
    const lines = [makeLine('⬢   ')]
    const cols: number[] = []
    const html = serializeRange(
      DEFAULT_SNAPSHOT,
      (y) => lines[y],
      0,
      0,
      cols,
      (chars) => (chars === '⬢' ? 1 : null),
    )
    expect(html).toBe(
      '<span class="term-line"><span class="term-cell" data-cols="1">⬢</span></span>',
    )
    expect(cols).toEqual([1])
  })

  it('несёт атрибуты ячейки на самой коробке и отдаёт их классификатору', () => {
    const faces: boolean[] = []
    const lines = [lineWith({ chars: '⬢', bold: true, fg: 1, fgMode: XTERM_CM_P16 })]
    const html = serializeRange(
      DEFAULT_SNAPSHOT,
      (y) => lines[y],
      0,
      0,
      undefined,
      (_c, _w, attrs) => {
        faces.push(attrs.bold)
        return 1
      },
    )
    expect(faces).toEqual([true])
    expect(html).toContain('class="term-cell" data-cols="1" style="')
    expect(html).toContain('font-weight:bold')
  })

  it('экранирует содержимое коробки', () => {
    const lines = [makeLine('<')]
    const html = serializeRange(DEFAULT_SNAPSHOT, (y) => lines[y], 0, 0, undefined, boxEverything)
    expect(html).toContain('<span class="term-cell" data-cols="1">&lt;</span>')
  })

  it('без классификатора даёт ровно сегодняшнюю строку', () => {
    const lines = [makeLine('a\u{1F5D1}b')]
    const plain = serializeRange(DEFAULT_SNAPSHOT, (y) => lines[y], 0, 0)
    expect(plain).toBe('<span class="term-line">a\u{1F5D1}b</span>')
    expect(serializeRange(DEFAULT_SNAPSHOT, (y) => lines[y], 0, 0, undefined, boxNothing)).toBe(
      plain,
    )
  })

  it('не доходит до семантических эмиссий', () => {
    const lines = [makeLine('a\u{1F5D1}b')]
    expect(serializeRangeText((y) => lines[y], 0, 0)).toBe('a\u{1F5D1}b')
    expect(serializeRangeSGR((y) => lines[y], 0, 0)).toBe('a\u{1F5D1}b')
  })
})

describe('collectFitCandidates', () => {
  it('видит ровно те ячейки, о которых потом спросят при сериализации', () => {
    // Если сбор и сериализация разойдутся хоть в одной ячейке, кэш будет
    // холодным именно там, и коробка не появится — молча.
    const lines = [makeLine('a\u{1F5D1}b'), new BufferLine('⬢x', true)]
    const collected: Array<[string, number]> = []
    collectFitCandidates(
      (y) => lines[y],
      0,
      1,
      (chars, width) => collected.push([chars, width]),
    )
    const asked: Array<[string, number]> = []
    serializeRange(
      DEFAULT_SNAPSHOT,
      (y) => lines[y],
      0,
      1,
      undefined,
      (chars, width) => {
        asked.push([chars, width])
        return null
      },
    )
    expect(collected).toEqual(asked)
  })
})
```

- [ ] **Step 2: Убедиться, что тесты падают**

Run: `cd frontend && ./node_modules/.bin/vitest run src/scrollback/serializer.test.ts -t "cell boxes"`
Expected: FAIL — шестой аргумент игнорируется, `collectFitCandidates` не экспортирован.

- [ ] **Step 3: Провести классификатор через обход**

В `frontend/src/scrollback/serializer.ts` заменить объявление рана (строка 317):

```ts
interface GenericRun<A> {
  chars: string
  attrs: A
  /** Колонки, в которые ячейку надо запереть, либо undefined — ран течёт
   *  потоком. Ран с этим полем НЕ склеивается ни с чем: он и есть одна
   *  ячейка, а слитая пара заняла бы одну колонку на двоих. */
  box?: number
}
```

`collectRunsOf` (строка 348) — новый параметр:

```ts
function collectRunsOf<A>(
  line: IBufferLine,
  attrsOf: (line: IBufferLine, i: number) => A,
  equal: (a: A, b: A) => boolean,
  escape: boolean,
  keepTrailingSpace: boolean,
  boxColumns?: (chars: string, width: number, attrs: A) => number | null,
): Walked<A> {
```

Ветка пустой ячейки — защитить приписывание пробела:

```ts
if (chars.length === 0) {
  const last = runs.length > 0 ? runs[runs.length - 1] : undefined
  if (last !== undefined && last.box === undefined && equal(last.attrs, attrs)) {
    last.chars += ' '
  } else {
    runs.push({ chars: ' ', attrs })
  }
  cols += Math.max(1, width)
  i += Math.max(1, width)
  continue
}
```

Ветка непустой ячейки — заменить целиком:

```ts
const text = escape
  ? chars.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
  : chars

// Коробка принимается ТОЛЬКО на колонки самой ячейки. «Одна колонка»
// для ячейки шириной две — это сдвиг, которого в сетке нет; лучше
// сегодняшний поток, чем выдуманная геометрия.
const columns = Math.max(1, width)
const box = boxColumns?.(chars, columns, attrs) === columns ? columns : null
const last = runs.length > 0 ? runs[runs.length - 1] : undefined
if (box !== null) {
  runs.push({ chars: text, attrs, box })
} else if (last !== undefined && last.box === undefined && equal(last.attrs, attrs)) {
  last.chars += text
} else {
  runs.push({ chars: text, attrs })
}
```

Обрезка хвостовых пробелов — коробку не трогать:

```ts
if (!keepTrailingSpace && runs.length > 0) {
  const last = runs[runs.length - 1]
  if (last.box === undefined) {
    const trimmed = last.chars.replace(/ +$/, '')
    cols -= last.chars.length - trimmed.length
    last.chars = trimmed
  }
}
```

- [ ] **Step 4: Провести классификатор через `collectRuns` и `serializeRange`**

`collectRuns` (строка 410):

```ts
function collectRuns(
  snapshot: TerminalSnapshot,
  line: IBufferLine,
  keepTrailingSpace = false,
  boxColumns?: (chars: string, width: number, attrs: CellAttrs) => number | null,
): Walked<CellAttrs> {
  return collectRunsOf(
    line,
    (l, i) => cellAttrs(snapshot, l, i),
    attrsEqual,
    true,
    keepTrailingSpace,
    boxColumns,
  )
}
```

`serializeRange` (строка 538):

```ts
export function serializeRange(
  snapshot: TerminalSnapshot,
  getLine: (y: number) => IBufferLine | undefined,
  startLine: number,
  endLine: number,
  colsOut?: number[],
  boxColumns?: (chars: string, width: number, attrs: CellAttrs) => number | null,
): string {
  const groups = walkRange(getLine, startLine, endLine, (line, keepTrailingSpace) => {
    const { runs, cols } = collectRuns(snapshot, line, keepTrailingSpace, boxColumns)
    let content = ''
    for (const run of runs) {
      if (run.chars.length === 0) continue
      const style = attrsToStyle(snapshot, run.attrs)
      if (run.box !== undefined) {
        const styleAttr = style ? ` style="${style}"` : ''
        content += `<span class="term-cell" data-cols="${run.box}"${styleAttr}>${run.chars}</span>`
      } else {
        content += style ? `<span style="${style}">${run.chars}</span>` : run.chars
      }
    }
    return { content, cols }
  })
  if (colsOut) {
    colsOut.length = 0
    for (const g of groups) colsOut.push(g.cols)
  }
  return groups.map((g) => `<span class="term-line">${g.content}</span>`).join('')
}
```

- [ ] **Step 5: Добавить сбор кандидатов — ТЕМ ЖЕ обходом**

Рядом с `serializeRange`:

```ts
/**
 * Пройти те же ячейки и назвать их, ничего не строя (nocx-ec18).
 *
 * Заморозка меряет ширины ПАКЕТОМ: чтение ректа после записи форсирует
 * раскладку, и поштучно это N раскладок в тот самый момент, когда блок
 * подменяет живую область. Значит кандидатов надо знать ДО сериализации.
 *
 * Это тот же collectRunsOf, а не второй обход в смысле AD-8: функция,
 * знающая, как ходить по ячейкам и как считать колонки, по-прежнему одна.
 * Здесь она вызывается с пустыми атрибутами — как это уже делает
 * serializeRangeText, — поэтому проход дешёвый: ни вывода цвета, ни
 * экранирования, ни склейки строк.
 *
 * ПОРЯДОК И СОСТАВ ОБЯЗАНЫ СОВПАДАТЬ с тем, что увидит классификатор при
 * сериализации, иначе кэш окажется холодным ровно там, где нужен, и коробка
 * не появится молча. Это утверждается тестом, сравнивающим два списка.
 */
export function collectFitCandidates(
  getLine: (y: number) => IBufferLine | undefined,
  startLine: number,
  endLine: number,
  sink: (chars: string, width: number, attrs: CellAttrs) => void,
): void {
  walkRange(getLine, startLine, endLine, (line, keepTrailingSpace) => {
    const { cols } = collectRunsOf<CellAttrs>(
      line,
      (l, i) => cellAttrs(DEFAULT_SNAPSHOT, l, i),
      attrsEqual,
      false,
      keepTrailingSpace,
      (chars, width, attrs) => {
        sink(chars, width, attrs)
        return null
      },
    )
    return { content: '', cols }
  })
}
```

Примечание для исполнителя: возвращаемое значение `walkRange` здесь
отбрасывается, и это не небрежность — у всех групп `content === ''`, так что
обрезка пустых их и вычистит. Кандидаты уже названы через `sink` во время
эмиссии, а колонки этой функции не нужны: их отдаёт `serializeRange` своим
`colsOut`. Если однажды понадобится и то и другое разом, брать надо оттуда.

`DEFAULT_SNAPSHOT` здесь годится, потому что классификатору из атрибутов нужны только `bold` и `italic`, а они от темы не зависят. Если тест «видит ровно те ячейки» покажет расхождение — искать причину в нём, а не подгонять тест.

- [ ] **Step 6: Убедиться, что тесты проходят**

Run: `cd frontend && ./node_modules/.bin/vitest run src/scrollback src/terminal-links src/frame && ./node_modules/.bin/tsc --noEmit -p tsconfig.test.json`
Expected: PASS.

- [ ] **Step 7: Коммит**

```bash
git add frontend/src/scrollback/serializer.ts frontend/src/scrollback/serializer.test.ts
git commit -m "feat(terminal): let the cell walk box a cell that does not land on the grid (nocx-ec18)"
```

---

### Task 3: Включить коробки и доказать геометрию, ссылки и выделение

**ЭТА ЗАДАЧА НЕДЕЛИМА.** Стиль, врезка, геометрия, сохранность коробок под подсветкой ссылок и выделение — один коммит. Между «коробки включены» и «доказано, что они целы» не должно быть состояния, в котором продукт хуже сегодняшнего. Красные браузерные тесты пишутся ПЕРВЫМИ.

**Files:**

- Create: `e2e/frozen-line-grid.spec.ts`
- Modify: `frontend/src/style.css` (`.term-line` около 1328; новое `.term-cell` за ним)
- Modify: `frontend/src/scrollback/blocks.ts` (поле рядом с `_getContainer` — 2092; `_freezeVisual` — 2333; `dispose`)
- Modify: `frontend/src/scrollback/cell-metric.ts` (комментарии на строках 25 и около 59)
- Modify: `frontend/src/scrollback/cmd-output-wrap.test.ts`
- Modify: `frontend/src/terminal-links/decorate.ts`, `frontend/src/terminal-links/decorate.test.ts`, `frontend/src/terminal-links/end-to-end.test.ts` — см. шаг 8а. Расщепление коробки диапазоном ссылки не «может случиться», а прямо следует из того, как работает `extractContents`; чинится в той же задаче, иначе она оставляет продукт с разрушенной геометрией на строках со ссылками. Тесты названы здесь потому, что оба утверждают ОДИН anchor на ссылку, идущую через несколько текстовых узлов: `decorate.test.ts:35` — косвенно, через полный `textContent` первого anchor, и `end-to-end.test.ts:145` — прямо, `block.querySelector('a').textContent` равен всей `AGENTS.md:84` на пути через два цветовых прогона. Правка это меняет, и менять их придётся осознанно, а не задним числом.

**Acceptance Criteria:**

- **Механизм (детерминированно):** при метрике, выведенной из замера в этом же браузере, на строке появляются `.term-cell` с верными `data-cols`. Не зависит от того, какие шрифты есть в среде.
- **Пакет меряет каждого кандидата отдельно:** при метрике, равной продвижке узкого кластера, коробку получает только широкий. Схлопнувшийся пакет закоробкует оба.
- **Геометрия:** строка из N колонок с символами по ширине совпадает со строкой из N букв `W` (расхождение меньше 0.5px).
- **Базовая линия:** ОДИН И ТОТ ЖЕ символ внутри коробки и рядом с ней даёт совпадающие ректы. Сравнивать разные глифы бессмысленно — у них разные ascent при одной базовой линии.
- **Перенос:** строка ровно во всю ширину терминала даёт один визуальный фрагмент при `data-output-wrap='on'`, поставленном тестом явно.
- **Ссылки:** ссылка КОНЧАЕТСЯ коробочным символом (частичный захват диапазона — единственный опасный случай); строка совпадает с эталонной по ширине, тексту и составу коробок; эталон коробки имеет и ссылкой не является; пустых `.term-cell` нет.
- **Выделение:** `Selection.toString()` по строке, в которой коробки ГАРАНТИРОВАННО есть, совпадает с её `textContent`.
- `.term-cell` и `.term-cell[data-cols='2']` поставляются в CSS, и у `.term-cell` НЕТ ни `overflow`, ни `clip-path`; `.term-line` глушит лигатуры и кернинг; тест читает каскад с атрибутными селекторами.
- Комментарии в `cell-metric.ts` больше не утверждают ни что коррекция делает N колонок ровно N × cellWidth, ни что зонд наследует шрифт блока.

- [ ] **Step 1: Расширить `shippedValue` до селекторов с атрибутами**

Прочитать `shippedValue` в `frontend/src/scrollback/cmd-output-wrap.test.ts`. Если он отбирает правила фильтром вида `/^\.[\w-]+$/` — это ровно тот дефект, из-за которого `nocx-juau` вернулся незамеченным: составной селектор не рассматривается, и тест утверждает про правило, которое в каскаде перебито. Расширить до модели элемента `{ classes: string[]; attrs?: Record<string, string> }`.

- [ ] **Step 2: Написать падающий тест на поставляемые правила**

```ts
// Коробка ячейки поставляется в CSS, а не живёт в инлайне (nocx-ec18).
it('ships a fixed-width box for a cell that cannot lay itself out', () => {
  expect(shippedValue({ classes: ['term-cell'] }, 'display')).toBe('inline-block')
  expect(shippedValue({ classes: ['term-cell'] }, 'width')).toBe('var(--term-cell-width, 1ch)')
  expect(shippedValue({ classes: ['term-cell'] }, 'letter-spacing')).toBe('0')
  // НИ overflow, НИ clip-path: коробка задаёт продвижку, а не прячет
  // краску. overflow увёл бы базовую линию к нижнему краю margin-бокса;
  // клип отрезал бы хвост глифу, попавшему в устаревшую коробку после
  // смены метрики. Оба утверждения — про ОТСУТСТВИЕ правила, потому что
  // добавить их обратно легко и незаметно.
  expect(shippedValue({ classes: ['term-cell'] }, 'overflow')).toBe('')
  expect(shippedValue({ classes: ['term-cell'] }, 'clip-path')).toBe('')
  expect(shippedValue({ classes: ['term-cell'], attrs: { 'data-cols': '2' } }, 'width')).toBe(
    'calc(var(--term-cell-width, 1ch) * 2)',
  )
})

it('shapes a frozen row cell by cell, the way the grid draws it', () => {
  // Иначе DOM волен сшить `->` или `ffi` не так, как xterm рисует их по
  // ячейкам, и предпосылка «ASCII всегда ложится» перестаёт быть верной.
  expect(shippedValue({ classes: ['term-line'] }, 'font-variant-ligatures')).toBe('none')
  expect(shippedValue({ classes: ['term-line'] }, 'font-kerning')).toBe('none')
})
```

- [ ] **Step 3: Написать падающие браузерные тесты**

Создать `e2e/frozen-line-grid.spec.ts`:

```ts
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
  await page.keyboard.type(`printf '%s\n' '${payload}' # ${marker}`)
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
  const [narrow] = await advancesOf(page, ['─'])
  await forceCellWidth(page, narrow)

  const marker = `BM-${Date.now().toString(36)}`
  await printLine(page, `──\u{1F5D1}──`, marker)
  const shape = await rowShape(page, marker)

  expect(shape.boxes.map((b) => b.text)).toEqual(['\u{1F5D1}'])
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
    `printf '%s%s\n' "$(printf 'W%.0s' $(seq 1 $(( $(tput cols) - 4 ))))" '${SYMBOLS}' # ${marker}`,
  )
  await page.keyboard.press('Enter')
  await expect(page.locator('.cmd-block', { hasText: marker }).first()).toBeVisible({
    timeout: 15_000,
  })

  // Число ВИЗУАЛЬНЫХ фрагментов. Округление высоты к шагу сетки пропустило
  // бы рост меньше половины строки; фрагменты не пропускают.
  const fragments = await page.evaluate((m) => {
    const b = Array.from(document.querySelectorAll('.cmd-block')).find((el) =>
      (el.textContent ?? '').includes(m),
    )
    const row = b?.querySelector<HTMLElement>('.cmd-output > .term-line')
    if (!row) return -1
    const range = document.createRange()
    range.selectNodeContents(row)
    return new Set(Array.from(range.getClientRects()).map((r) => Math.round(r.top))).size
  }, marker)

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
  const plain = `nolink!!example.com/x${SYMBOLS}`

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

  expect(linked.text).toBe(reference.text.replace('nolink!!', 'https://'))
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
```

- [ ] **Step 4: Убедиться, что всё падает**

Run: `cd frontend && ./node_modules/.bin/vitest run src/scrollback/cmd-output-wrap.test.ts`
Expected: FAIL — правил `.term-cell` в каскаде нет.

Run: `PW_PROJECTS=chromium e2e/run-in-container.sh e2e/frozen-line-grid.spec.ts`
Expected: FAIL — тесты, ожидающие коробок, находят ноль.

- [ ] **Step 5: Добавить стили**

В `frontend/src/style.css`, внутрь существующего блока `.term-line` (около 1328), рядом с `letter-spacing`:

```css
/* ОДНА ЯЧЕЙКА — ОДИН ГЛИФ, как рисует сетка. Без этого DOM волен сшить
     `->`, `==` или `ffi` в лигатуру и подвинуть кернингом то, что xterm
     кладёт по ячейкам, — и предпосылка «одноколоночная латиница ложится на
     сетку», на которой стоит вся коррекция выше, перестаёт быть верной. */
font-variant-ligatures: none;
font-kerning: none;
```

Сразу после блока `.term-line`:

```css
/* ОДНА ЯЧЕЙКА, КОТОРАЯ НЕ УМЕЕТ ЛЕЧЬ САМА (nocx-ec18).
   CSS не может задать продвижку текстовому кластеру без layout-объекта
   вокруг него — ни font-size-adjust, ни transform, ни font-feature-settings
   этого не делают. Поэтому глиф, чья продвижка разошлась с колонкой сетки
   (U+1F5D1 — 13.57px против ячейки в 8), запирается в коробку известной
   ширины. Кого запирать, решает scrollback/cell-fit.ts; сюда попадают
   единицы ячеек на блок, а не каждая: псевдографика и латиница ложатся
   сами. Ширина из переменной, а не из пикселей, чтобы блок перепитчивался
   вместе с сеткой, как это уже делает letter-spacing строки. */
.term-cell {
  display: inline-block;
  width: var(--term-cell-width, 1ch);
  /* Трекинг применяется К СИМВОЛУ, а ширина задана коробке. Оставить его
     внутри — значит двигать глиф внутри уже заданной ширины, ничего не
     выигрывая. */
  letter-spacing: 0;
  /* КЛИПА ЗДЕСЬ НЕТ, И ЭТО РЕШЕНИЕ, А НЕ УПУЩЕНИЕ.
     Коробка задаёт ПРОДВИЖКУ; краска пусть вылезает. Так было отвергнуто
     сразу два риска. Первый: `overflow: hidden` уводит базовую линию
     inline-block к нижнему краю его margin-бокса, и глиф уезжает по
     вертикали относительно соседей. Второй, найденный разбором и тонкий:
     классификатор меряет кластер в потоке, то есть С трекингом, а внутри
     коробки трекинг обнулён. Пока метрика та же, это безразлично — ширину
     задаёт коробка. Но после смены метрики состав коробок устаревает
     (переклассификация вынесена в follow-up), и глиф, который при новой
     метрике лёг бы сам, оказывается в коробке шириной с новую ячейку,
     рисуясь без δ: при отрицательной δ его натуральная продвижка ШИРЕ
     коробки, и клип отрезал бы у него хвост. Без клипа устаревшая коробка
     не может спрятать ничего — она лишь даёт наложение краски, ровно то
     самое, которое сегодня и так происходит.
     Цена названа: широкий глиф налезает на соседа вместо того, чтобы
     обрезаться. Эталон, на который равняется владелец, в этом месте рисует
     заглушку, то есть целого глифа тоже не показывает. Вернуть клип — одна
     строка, и делать это можно только вместе с переклассификацией. */
}

.term-cell[data-cols='2'] {
  width: calc(var(--term-cell-width, 1ch) * 2);
}
```

- [ ] **Step 6: Врезать классификатор в заморозку**

В `frontend/src/scrollback/blocks.ts` рядом с прочими импортами:

```ts
import { createCellFit, type CellFit, type FitCandidate } from './cell-fit'
import { collectFitCandidates } from './serializer'
```

Рядом с `_getContainer` (строка 2092):

```ts
  /** Кто решает, какой ячейке нужна коробка. Живёт при блоках, потому что
   *  меряет в ИХ контейнере: там опубликован --term-cell-width. */
  private _cellFit: CellFit = createCellFit(() => this._scrollbackInner)
```

В `_freezeVisual` (строка 2333):

```ts
const driftCols = driftEnabled() ? [] : undefined
// ДВА ПРОХОДА, ОДНА РАСКЛАДКА. Первый называет ячейки и греет кэш одним
// пакетным замером; второй сериализует, и там boxColumns уже чистое
// чтение Map. Поштучный замер во время сериализации был бы N
// принудительных раскладок в тот самый момент, когда блок подменяет
// живую область.
let boxColumns: Parameters<typeof serializeRange>[5]
if (this._cellFit.begin()) {
  const candidates: FitCandidate[] = []
  collectFitCandidates(getLine, rec.outputStart, endLine, (chars, width, attrs) =>
    candidates.push({ chars, width, face: { bold: attrs.bold, italic: attrs.italic } }),
  )
  this._cellFit.warm(candidates)
  boxColumns = (chars, width, attrs) =>
    this._cellFit.boxColumns(chars, width, { bold: attrs.bold, italic: attrs.italic })
}
const outputHtml = serializeRange(
  snapshot,
  getLine,
  rec.outputStart,
  endLine,
  driftCols,
  boxColumns,
)
```

И в `dispose()` этого класса — `this._cellFit.dispose()`. Зонд обязан уходить вместе с блоками: `_own()` объявлен единственным входом в `.scrollback-inner`, а `clearAll()` удаляет только своё, так что оставленный зонд был бы посторонним прямым ребёнком навсегда.

- [ ] **Step 7: Поправить два комментария, которые утверждают неверное**

В `frontend/src/scrollback/cell-metric.ts` заменить абзац на строке 25:

```ts
// The correction is per TYPOGRAPHIC CHARACTER, which is not the same unit as
// a terminal cell, and the difference is the whole of nocx-ec18. It cancels
// cellWidth - naturalAdvance exactly for a single-column cluster whose
// natural advance IS that naturalAdvance — which is most output, and why it
// is kept. It cannot do so for anything else: a two-column cell takes ONE
// tracking opportunity where the grid gives it two columns, and a glyph the
// browser resolved in another font has no single delta that fits it and a
// letter at once. Those cells are boxed instead (scrollback/cell-fit.ts).
```

И в комментарии к `measureNaturalAdvance` (около строки 59) заменить утверждение, что зонд «inherits the block's font … by living inside the scrollback container»:

```ts
/** The DOM's own natural per-character advance, measured from a hidden probe
 *  span. The font comes from `.cell-metric-probe` in the stylesheet, which
 *  declares it explicitly — NOT from living inside the scrollback container,
 *  which carries no font-family of its own (`.cmd-output` is where the block
 *  gets one). Believing the inheritance story is how a second probe came to
 *  be written against the UI font. 0 when the probe cannot be measured
 *  (jsdom has no layout) — the caller publishes nothing. */
```

- [ ] **Step 8а: Починить подсветку ссылок — она расщепляет коробку**

Тест `link decoration leaves the boxes and the geometry intact` красный с
пустой коробкой или с лишней коробкой. Это ожидаемый исход, а не случайность:
`decorateRow` строит `Range` от `charPos(flat, from)` до `charPos(flat, to-1)`
и делает `range.extractContents()` (decorate.ts, около 49). Когда конец
диапазона лежит ВНУТРИ текстового узла коробки, извлекается часть
`.term-cell`: браузер оставляет пустой оригинал, а непустой клон переносит
внутрь `<a>`. Ширина строки после этого ничем не гарантирована.

Направление правки — НЕ оборачивать диапазон целиком, а обернуть каждый
текстовый узел (или его отрезок) в свой anchor. Структура строки тогда не
пересобирается: коробка остаётся коробкой, внутри неё появляется anchor
вокруг её собственного текста.

**`href` тут ни при чём, и это важно.** Anchor создаётся БЕЗ `href`
намеренно (decorate.ts, около 56): реальный `href` на путь позволил бы клику
заменить документ приложения. Цель живёт в `data-*`, и общей у нескольких
anchor'ов должна быть именно она.

```ts
// В decorateRow, вместо одного extractContents на весь диапазон: пройти
// текстовые узлы диапазона и обернуть каждый отрезок отдельно, раздав всем
// один и тот же data-* target. `flat.chars[i].node` уже даёт узел каждого
// символа, так что группировка по узлу делается по тому же массиву,
// которым построен flat.
```

**Два следствия, которые надо решить, а не проглядеть.**

Первое: единственность anchor утверждают ДВА теста — `decorate.test.ts:35`
косвенно и `end-to-end.test.ts:145` прямо (`querySelector('a').textContent`
равен всей ссылке, идущей через два цветовых прогона). Оба переписываются
осознанно, и в комментарии каждого называется причина, иначе следующий
читатель решит, что инвариант просто размыли.

И вместе со вторым тестом идёт его предмет: клик. `attachLinkClicks`
(terminal-links/surface.ts) открывает цель по данным anchor'а, поэтому цель
обязана лежать на КАЖДОМ из них — иначе клик по второй половине ссылки не
откроет ничего. Это проверяется тем же end-to-end тестом: кликнуть по
anchor'у, который НЕ первый.

Второе: несколько anchor'ов НЕ равны одному для вспомогательных технологий
автоматически. Два честных выхода, и выбрать надо ЯВНО:

- либо ссылка получает семантику и порядок фокуса — один фокусируемый
  элемент, остальные `aria-hidden`; тогда фокусируемый обязан быть
  активируем с клавиатуры, а не только мышью, и в объём входят `surface.ts`
  с его тестом;
- либо утверждение об эквивалентности не делается вовсе, а ограничение
  записывается в бид как названный долг доступности.

Дешевле второе, и оно честнее: доступность ссылок в замороженном блоке — не
то, что чинят попутно внутри задачи про геометрию.

Точная форма принадлежит тому, кто увидит фактическую поломку в браузере;
тест выше её и описывает. Чего делать НЕЛЬЗЯ: ослаблять тест, исключать
коробки из детекции ссылок (ссылка с символом в пути — настоящая ссылка) или
переносить декорацию до сериализации.

- [ ] **Step 8: Довести до зелёного, разобрав развилку трекинга**

Run: `PW_PROJECTS=chromium e2e/run-in-container.sh e2e/frozen-line-grid.spec.ts`

**Если зелено** — трекинг после коробки не добавляется, идти на шаг 9.

**Если строка с символами уже эталона примерно на `k × |--term-cell-delta|`, где k — число коробок** — браузер считает атомарный inline-block типографской единицей и добавляет за ним унаследованный трекинг. ПРЕЖДЕ ЧЕМ КОМПЕНСИРОВАТЬ, убедиться, что он именно справа, а не размазан по границам: снять ширину одной коробки в трёх положениях — в начале строки, между обычными символами и в конце:

```js
const row = document.querySelector('.cmd-output > .term-line')
const b = row.querySelector('.term-cell')
const r = document.createRange()
r.selectNode(b)
console.log(
  b.getBoundingClientRect().width,
  r.getBoundingClientRect().width,
  getComputedStyle(b).width,
  getComputedStyle(row).letterSpacing,
)
```

Если подтвердилось — дописать в `.term-cell`:

```css
/* БРАУЗЕР СЧИТАЕТ КОРОБКУ ТИПОГРАФСКОЙ ЕДИНИЦЕЙ и добавляет за ней
     унаследованный трекинг — измерено, а не выведено. Ширина коробки уже
     точна, поэтому лишнюю дельту надо снять полем. Знак: delta
     отрицательна, значит margin положителен. */
margin-inline-end: calc(-1 * var(--term-cell-delta, 0px));
```

**Если расхождение иной величины** — это не трекинг. Разобраться по числам выше, прежде чем что-либо компенсировать.

- [ ] **Step 9: Прогнать всё затронутое**

Run: `cd frontend && ./node_modules/.bin/vitest run src/scrollback src/terminal-links && ./node_modules/.bin/tsc --noEmit -p tsconfig.test.json`
Run: `PW_PROJECTS=chromium e2e/run-in-container.sh e2e/frozen-line-grid.spec.ts`
Expected: PASS.

- [ ] **Step 10: Коммит**

```bash
git add frontend/src/style.css frontend/src/scrollback/blocks.ts frontend/src/scrollback/cell-metric.ts frontend/src/scrollback/cmd-output-wrap.test.ts frontend/src/terminal-links/decorate.ts frontend/src/terminal-links/decorate.test.ts frontend/src/terminal-links/end-to-end.test.ts e2e/frozen-line-grid.spec.ts
git commit -m "feat(terminal): box the cells that miss the grid, and prove the geometry (nocx-ec18)"
```

---

### Task 4: Завести биды на вынесенное — ДО закрытия дефекта

**Files:** изменений кода нет.

**Acceptance Criteria:** три бида заведены, связаны с `nocx-ec18` и опубликованы.

- [ ] **Step 1: Восстановленный блок**

```bash
bd create "Восстановленный блок теряет геометрию: ширины ячеек не переживают перезапуск" \
  -t bug -p 2 --label terminal -d "После nocx-ec18 живой замороженный блок кладёт не сходящиеся ячейки в коробки известной ширины, а восстановленный из SGR-тела — нет: serializeRangeSGR геометрию не выражает и не должен, это цветовая семантика. Рамка, целая до перезапуска, после него снова разъезжается.

Расширять SGR-тело нельзя — это связало бы парсер стилей с частным протоколом геометрии. Точный вариант один: versioned sidecar с границами ячеек и исключениями по ширинам, компактно (RLE), рядом с телом. Приблизительный дешёвый: восстановленный путь заново меряет кластеры и считает их шириной 1 — врёт на двухколоночных.

## Acceptance Criteria
- Блок, восстановленный после перезапуска, показывает ту же рамку, что и до. Проверено браузерным тестом, сравнивающим ШИРИНЫ строк до и после reload.
- SGR-тело осталось SGR-телом: парсер стилей не знает о геометрии."
```

- [ ] **Step 2: Кадр ассистента**

```bash
bd create "Кадр ассистента теряет getWidth(): двухколоночная ячейка даёт коробку плюс пустую" \
  -t bug -p 3 --label assistant -d "frontend/src/frame/mint.ts кладёт по одной ячейке на колонку идентичности, беря cell.getChars() и атрибуты, но НЕ getWidth(). frontend/src/frame/display.ts рисует span на ячейку фиксированной ширины. Значит ячейка двойной ширины даёт коробку с глифом и следом пустую коробку-продолжение вместо одной коробки в две колонки — CJK и широкие эмодзи смещают всё, что правее.

Замечено при работе над nocx-ec18; отдельная поверхность, отдельный владелец.

## Acceptance Criteria
- Строка кадра с CJK-символом занимает столько же, сколько строка из того же числа колонок латиницы. Проверено сравнением ширин в браузере.
- mintLiveFrame несёт ширину ячейки, а display её применяет."
```

- [ ] **Step 3: Переклассификация при смене полной сигнатуры**

```bash
bd create "Замороженные блоки не переклассифицируются при смене метрики или шрифта" \
  -t bug -p 2 --label terminal -d "Классификация ячеек происходит ОДИН раз, при заморозке. Ширины уже поставленных коробок следуют за --term-cell-width и перепитчиваются вместе с сеткой, но состав коробок замирает.

Это НЕ только про смену шрифта в настройках: cellWidth меняется при смене device pixel ratio и при повторном измерении рендерером — то есть уже сегодня, без участия пользователя.

Ущерб ограничен намеренно: у .term-cell НЕТ клипа. Устаревшая коробка держит ширину по текущей метрике и ничего не прячет — она лишь оставляет глиф в коробке там, где он лёг бы и сам. Если клип когда-нибудь вернут, эта задача становится его предусловием: коробка, поставленная при старой метрике, начнёт резать глиф, который при новой ложится сам (классификатор меряет С трекингом, а внутри коробки трекинг обнулён).

Чинится переклассификацией и повторной сериализацией блоков — что упирается в тот же отсутствующий sidecar геометрии.

## Acceptance Criteria
- После смены device pixel ratio или шрифта блок, замороженный до неё, показывает ту же рамку, что и блок, замороженный после.
- Ни один глиф не показан в коробке, которая при текущей метрике ему не нужна."
```

- [ ] **Step 4: Связать и опубликовать**

```bash
bd dep add <id> nocx-ec18 --type discovered-from   # для каждого из трёх
bd dolt push
```

Если `--type discovered-from` не поддержан этой версией `bd` — `bd dep add --help`; связь обязана быть, направление от находки к породившему биду.

---

### Task 5: Подтвердить на настоящей рамке и закрыть

**Files:** изменений кода не планируется; если понадобятся — они принадлежат задачам 1–3.

**Acceptance Criteria:**

- `make ci-full` зелёный на слитом дереве.
- В живой панели владельца на macOS: `omp`, выход, `nocxCellDrift.report()` даёт `wideByAColumn: 0` там, где было 1.
- Углы рамки сходятся при `terminal.wrapOutput` включённом и выключенном.
- Выделение мышью строки с коробками копирует ту же строку, что и живая область.
- `nocx-ec18` закрыт с числами в теле.

- [ ] **Step 1: Полный гейт на слитом дереве**

Run: `make ci-full`
Expected: четыре задания зелёные. Локальный красный `backend` сверять со списком известных расхождений в AGENTS.md, прежде чем ему верить.

- [ ] **Step 2: Замер в живой панели**

```js
nocxCellDrift.reset()
```

запустить `omp`, выйти, затем

```js
nocxCellDrift.report()
```

Expected: `wideByAColumn: 0`, `worstCols` меньше 0.1.

- [ ] **Step 3: Копирование руками**

Выделить мышью строку статуса `omp` в замороженном блоке, вставить в редактор — строка должна совпасть со строкой из живой области, символ в символ. Повторить при выключенном `terminal.wrapOutput`.

- [ ] **Step 4: Закрыть бид**

```bash
bd close nocx-ec18 --reason "<числа report() до и после, что проверено руками, коммиты>"
bd dolt push
```

- [ ] **Step 5: Пуш**

```bash
git push
```
