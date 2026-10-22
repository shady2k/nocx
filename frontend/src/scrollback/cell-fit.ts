// THE OWNER of "does this grapheme land on the grid?" (nocx-ec18).
//
// The terminal grid reports cell columns, while the DOM lays out glyphs with
// browser font advances. `--term-cell-delta` corrects ordinary single-column
// Latin text. A glyph that uses a different font has no shared correction:
// on macOS (2026-09-05), measured U+1F5D1 was 13.572px against an 8px cell,
// while U+27F3, U+27F2, and U+2B22 missed by 9.087px, 9.087px, and 8.903px.
// Their combined error moved a box-drawing corner by one column.
//
// This module decides only which glyphs need a fitted box. CSS draws the box;
// the serializer supplies its DOM wrapper. Measurements run as one batch so
// a freeze causes one layout rather than one layout per glyph. Actual shaped
// glyph advances are measured because code points alone do not determine font
// face or width (VS15/VS16 and platform font fallback change both).
//
// The probe uses the actual `.cmd-output` font context, with a `.term-line`
// inside it. Measuring only on the scrollback container would inherit the UI
// font and cache a width that does not match frozen output.

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

/** Что классификатор говорит про ячейку, которая не ложится сама. */
export interface CellBox {
  /** Колонки, в которые заперта ячейка. */
  cols: number
  /** Во сколько раз ужать НАРИСОВАННЫЙ глиф, чтобы он поместился в свои
   *  колонки. Никогда больше 1: глиф уже своей ячейки не растягиваем — щель
   *  безвредна, а растянутая буква заметна. 1 означает «ужимать не надо». */
  fit: number
}

export interface CellFit {
  /** Открыть заморозку: разрешить зонд, снять ширину ячейки и сигнатуру.
   *  false — мерить негде, вся заморозка пройдёт как сегодня. */
  begin(): boolean
  /** Измерить неизвестных кандидатов одним пакетом. */
  warm(candidates: Iterable<FitCandidate>): void
  /** Чистое чтение кэша. */
  boxOf(chars: string, width: number, face: FitFace): CellBox | null
  /** The measured advance of a cluster — the same cache entry boxOf
   *  derives its verdict from (nocx-zg3k3.7: a run carries its spacing,
   *  and spacing is derived from the advance). null means not measured.
   *  Calibrated ASCII always answers null: its advance is naturalAdvance,
   *  which the published row correction already accounts for. */
  advanceOf(chars: string, width: number, face: FitFace): number | null
  /** The row context begin() took: the cell width and the published
   *  correction (--term-cell-delta) — the default spacing for runs whose
   *  ink nobody measured. null — there is nowhere to measure. */
  geometry(): {
    cellWidth: number
    rowDelta: number
    signature: string
    padY?: number
    pitch?: number
  } | null
  /** Убрать зонд из DOM. */
  dispose(): void
  /** Размер кэша. Для тестов границы. */
  size(): number
}

/** До четырёх знаков — та же точность, что у опубликованной дельты
 *  (cell-metric.ts): 8/14 это 0.5714285714285714, а множитель в миллидолях
 *  уже мельче любой сетки раскладки, и в разметке он читается. */
function round4(value: number): number {
  return Math.round(value * 1e4) / 1e4
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

/** Rule 4's vertical padding (ADR-0009, run-geometry RunMetric.padY): half
 *  the difference between the published row pitch (--term-cell-height) and
 *  the content box an inline run of this font actually gets. A background
 *  painted on an inline run covers the content box only, so without it every
 *  coloured row stopped short of the next and full-screen programs that fill
 *  rows with colour (mc's panels) were striped (nocx-zg3k3.21). Both absent
 *  while the pitch is not published yet: the background then covers the
 *  content box, the old degrade. The pitch itself rides out beside the
 *  padding (nocx-1ebrs.15): it is the vertical half of the cell metric, and
 *  the procedural box/block ink fills the cell top to bottom with it. */
function verticalMetricsOf(host: HTMLElement): { padY?: number; pitch?: number } {
  const witness = signatureWitness(host)
  const pitch = Number.parseFloat(getComputedStyle(witness).getPropertyValue('--term-cell-height'))
  if (!Number.isFinite(pitch) || pitch <= 0) return {}
  const run = host.ownerDocument.createElement('span')
  run.textContent = 'M'
  witness.appendChild(run)
  const content = run.getBoundingClientRect().height
  run.remove()
  if (!Number.isFinite(content) || content <= 0) return { pitch }
  return {
    pitch,
    padY: Math.max(0, Math.round(((pitch - content) / 2) * 1e4) / 1e4),
  }
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
    // THE GLYPH'S OWN ADVANCE, NOT ITS WIDTH UNDER THE ROW'S TRACKING
    // (nocx-zg3k3.29). The span is a .term-line for its type context, and a
    // .term-line also carries --term-cell-delta as letter-spacing; measured
    // with it, every glyph read one delta wider than it is, and the spacing
    // derived from that painted it one delta short of its cell — a run of
    // dozens of `─` ended a cell before mc's border did.
    span.style.letterSpacing = '0px'
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
  // КЭШ ХРАНИТ ЗАМЕР, А НЕ ВЕРДИКТ. Оба вывода — нужна ли коробка и на
  // сколько ужимать краску — считаются из продвижки, поэтому вторая
  // величина не может разойтись с первой. Раньше здесь лежали колонки либо
  // ноль как «измерен и ложится сам»: ноль был вердиктом, притворявшимся
  // замером, и второго вывода из него не сделать.
  const cache = new Map<string, number>()
  let probe: HTMLElement | null = null
  let signature = ''
  let cellWidth = 0
  let rowDelta = 0
  let vertical: { padY?: number; pitch?: number } = {}

  // A loaded font face can change glyph advances without changing the CSS
  // signature. Record the font-loading epoch and invalidate measurements when
  // the browser reports a completed load.
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
      const published = Number.parseFloat(
        getComputedStyle(signatureWitness(probe)).getPropertyValue('--term-cell-delta'),
      )
      rowDelta = Number.isFinite(published) ? published : 0
      vertical = verticalMetricsOf(probe)
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
        cache.set(keyOf(c.chars, c.width, c.face), advance)
      }
    },

    boxOf(chars, width, face) {
      if (signature === '' || isCalibratedAscii(chars, width)) return null
      const advance = touch(keyOf(chars, width, face))
      // Отсутствие означает «не измерен» — коробки нет, следующая заморозка
      // спросит. Измеренная продвижка отвечает на оба вопроса сразу.
      if (advance === undefined) return null
      const target = width * cellWidth
      // Lands on its own when it lands under the row's own tracking, as the
      // calibrated ASCII beside it does: the same verdict the in-flow width
      // gave before the measurement stopped carrying the tracking.
      if (Math.abs(advance + rowDelta - target) < FIT_EPSILON_PX) return null
      // МЕНЬШЕ ЕДИНИЦЫ ИЛИ РОВНО ЕДИНИЦА, никогда больше. Глиф у́же своей
      // ячейки уже помещается: растянуть его — значит поменять начертание
      // ради щели, которой никто не видит, а искажённая буква видна сразу.
      return { cols: width, fit: Math.min(1, round4(target / advance)) }
    },

    advanceOf(chars, width, face) {
      if (signature === '' || isCalibratedAscii(chars, width)) return null
      return touch(keyOf(chars, width, face)) ?? null
    },

    geometry() {
      if (signature === '' || cellWidth === 0) return null
      return {
        cellWidth,
        rowDelta,
        signature,
        ...(vertical.padY === undefined ? {} : { padY: vertical.padY }),
        ...(vertical.pitch === undefined ? {} : { pitch: vertical.pitch }),
      }
    },

    dispose() {
      // Слушатель снимается ИМЕНОВАННЫМ, иначе при каждом пересоздании
      // контроллера на документе оседает ещё один анонимный callback.
      fonts?.removeEventListener?.('loadingdone', onFontsLoaded)
      probe?.remove()
      probe = null
      signature = ''
      rowDelta = 0
      vertical = {}
      cache.clear()
    },

    size() {
      return cache.size
    },
  }
}
