/**
 * The records the set keeps in a project's tracker about how its work went,
 * as comments on the items they are about: one format, read by the backlog
 * gate and written and read by the run scripts, so the two can never disagree
 * about what a record says.
 *
 * A record is a comment whose first line is the marker and its kind, then one
 * `key: value` per line, then, for some kinds, one Markdown table:
 *
 *   [shady2k-time v1] receipt
 *   span: 3f9c1a2b
 *   from: 2026-09-25T10:15:00+03:00
 *   ...
 *
 *   | phase | model | tools | coord | answer | away | idle | total |
 *   |-------|------:|------:|------:|-------:|-----:|-----:|------:|
 *   | build |    40 |    55 |    10 |      0 |    0 |    0 |   105 |
 *   | total |    40 |    55 |    10 |      0 |    0 |    0 |   105 |
 *
 * Keys are the same in every project; only `note` is free text, in the
 * project's own language. Numbers are whole minutes, and a table adds up:
 * each row to its `total` cell, each column to the `total` row. A record that
 * does not is damaged, and a reader skips it rather than trust it.
 *
 * The kinds:
 *   claim     a session takes an item: the start of a span. Carries the
 *             forecast by phase and, for a run, the promised time.
 *   receipt   the end of a span: what the session spent on the item, by phase
 *             and by who spent it. Written by a script, never by hand.
 *   event     something that happened inside a span: a stop, a decision taken
 *             alone, a CI run.
 *   summary   a feature closed: the forecast against the work, by phase, and
 *             how long the work occupied, parallel sessions counted once.
 *   verdict   the owner's judgement of a run.
 *   recovery  how well a session picked up work another left.
 */

export const MARK = 'shady2k-time';
export const VERSION = 1;

export const PHASES = ['orient', 'explore', 'plan', 'build', 'debug', 'wrap', 'setup', 'unattributed'];
// Who spent the time: the model generating, tools running, the rest of an
// agent's turn (hooks, prompts, waiting on a subagent or worker), the owner
// answering, the owner away, and nobody.
export const BUCKETS = ['model', 'tools', 'coord', 'answer', 'away', 'idle'];
// What a forecast is of: the stretches an agent was working or waiting inside
// its turn, or the owner was answering. Not his absence, not nobody's gaps.
export const WORK = ['model', 'tools', 'coord', 'answer'];

export const ROLES = ['coordinator', 'worker', 'agent'];
export const ENDS = ['paused', 'finished', 'handed-over', 'stopped'];
export const REASONS = ['owner', 'missing', 'other'];
export const EVENTS = ['stop', 'decision', 'ci'];
export const RESULTS = ['pull-request', 'abandoned', 'stopped'];
export const ACCEPTED = ['as-is', 'after-changes', 'abandoned'];
export const GRADES = ['R0', 'R1', 'R2', 'R3'];

const TIME = 'time';
const INT = 'int';
const TEXT = 'text';
const oneOf = (list) => ({ list });

// Every key a kind may carry, and which it must.
export const SCHEMA = {
  claim: {
    need: { span: TEXT, at: TIME, session: TEXT, agent: TEXT, role: oneOf(ROLES) },
    may: { due: TIME, away: INT, tasks: INT, stages: INT, basis: TEXT, note: TEXT, recovered: TIME },
    table: { columns: ['forecast'], optional: true },
  },
  receipt: {
    need: { span: TEXT, from: TIME, to: TIME, end: oneOf(ENDS) },
    may: { reason: oneOf(REASONS), note: TEXT, recovered: TIME, cut: oneOf(['yes']) },
    table: { columns: [...BUCKETS, 'total'] },
  },
  event: {
    need: { span: TEXT, at: TIME, event: oneOf(EVENTS) },
    may: { reason: oneOf(REASONS), note: TEXT },
  },
  summary: {
    need: { at: TIME, result: oneOf(RESULTS), started: TIME, elapsed: INT, spans: INT, open: INT, missing: INT },
    may: { occupied: INT, 'occupied-partial': oneOf(['yes']), forecast: INT, pr: TEXT, note: TEXT },
    table: { columns: ['forecast', 'work', ...BUCKETS, 'total'], unknown: ['forecast'] },
  },
  verdict: {
    need: { at: TIME, accepted: oneOf(ACCEPTED) },
    may: { avoidable: INT, missed: INT, corrections: INT, rescues: INT, note: TEXT },
  },
  recovery: {
    need: { at: TIME, grade: oneOf(GRADES) },
    may: { from: TEXT, to: TEXT, note: TEXT },
  },
};

const HEAD = /^\[shady2k-time v(\d+)\][ \t]+([a-z-]+)[ \t]*$/;

/** A comment the set wrote, or meant to: its first line carries the marker. */
export const isMarked = (body) => typeof body === 'string' && body.trimStart().startsWith(`[${MARK}`);

const cellNumber = (s) => (/^\d+$/.test(s) ? Number(s) : s === '-' ? null : NaN);

/**
 * Reads a record. Never throws: what it cannot read is a problem in the list,
 * and a record with any problem is not to be counted.
 */
export function parseRecord(body) {
  const problems = [];
  const lines = String(body ?? '').replace(/\r\n?/g, '\n').trim().split('\n');
  const head = HEAD.exec(lines[0] || '');
  if (!head) return { kind: null, fields: {}, table: null, problems: ['the first line is not "[shady2k-time v1] <kind>"'] };
  const version = Number(head[1]);
  const kind = head[2];
  if (version !== VERSION) problems.push(`format version ${version} is not one this reader knows`);
  const schema = SCHEMA[kind];
  if (!schema) problems.push(`"${kind}" is not a kind of record`);
  const fields = {};
  const tableLines = [];
  for (const raw of lines.slice(1)) {
    const line = raw.trim();
    if (!line) continue;
    if (line.startsWith('|')) { tableLines.push(line); continue; }
    if (tableLines.length) { problems.push('text after the table'); continue; }
    const kv = /^([a-z][a-z-]*):[ \t]*(.*)$/.exec(line);
    if (!kv) { problems.push(`"${line.slice(0, 40)}" is not a "key: value" line`); continue; }
    if (kv[1] in fields) problems.push(`${kv[1]} appears twice`);
    fields[kv[1]] = kv[2].trim();
  }
  let table = null;
  if (tableLines.length) {
    const split = (l) => l.replace(/^\|/, '').replace(/\|$/, '').split('|').map((c) => c.trim());
    const columns = split(tableLines[0]);
    const rest = tableLines.slice(1).filter((l) => !/^\|[\s:|-]+\|$/.test(l));
    table = { columns: columns.slice(1), rows: {} };
    if (columns[0] !== 'phase') problems.push('the table\'s first column is not "phase"');
    for (const l of rest) {
      const cells = split(l);
      const name = cells[0];
      if (name in table.rows) problems.push(`the row ${name} appears twice`);
      if (cells.length !== columns.length) problems.push(`the row ${name} has ${cells.length} cells, the header ${columns.length}`);
      const row = {};
      table.columns.forEach((c, i) => { row[c] = cellNumber(cells[i + 1] ?? ''); });
      table.rows[name] = row;
    }
  }
  if (schema) problems.push(...validate(kind, fields, table, schema));
  return { kind, fields, table, problems };
}

function validate(kind, fields, table, schema) {
  const out = [];
  const known = { ...schema.need, ...schema.may };
  for (const k of Object.keys(schema.need)) if (!(k in fields) || fields[k] === '') out.push(`${k} is missing`);
  for (const [k, v] of Object.entries(fields)) {
    const type = known[k];
    if (!type) { out.push(`${k} is not a key of a ${kind}`); continue; }
    if (v === '' ) continue;
    if (type === TIME && !Number.isFinite(Date.parse(v))) out.push(`${k} is not a time: ${v}`);
    if (type === INT && !/^\d+$/.test(v)) out.push(`${k} is not a whole number of minutes: ${v}`);
    if (type.list && !type.list.includes(v)) out.push(`${k} is not one of ${type.list.join(', ')}: ${v}`);
  }
  if (kind === 'receipt' && Number.isFinite(Date.parse(fields.from)) && Number.isFinite(Date.parse(fields.to))
    && Date.parse(fields.to) < Date.parse(fields.from)) out.push('to is before from');
  if (kind === 'event' && fields.event === 'stop' && !fields.reason) out.push('a stop needs its reason');
  if (!schema.table) {
    if (table) out.push(`a ${kind} carries no table`);
    return out;
  }
  if (!table) {
    if (!schema.table.optional) out.push('the table is missing');
    return out;
  }
  const want = schema.table.columns;
  if (table.columns.join('|') !== want.join('|')) {
    out.push(`the table's columns are ${table.columns.join(', ')}, not ${want.join(', ')}`);
    return out;
  }
  const unknownOk = new Set(schema.table.unknown || []);
  for (const [name, row] of Object.entries(table.rows)) {
    if (name !== 'total' && !PHASES.includes(name)) out.push(`"${name}" is not a phase`);
    for (const c of want)
      if (Number.isNaN(row[c]) || (row[c] === null && !unknownOk.has(c))) out.push(`the row ${name} has no whole number under ${c}`);
  }
  if (!table.rows.total) { out.push('the table has no total row'); return out; }
  if (out.length) return out;
  const sumOf = (cols, row) => cols.reduce((n, c) => n + (row[c] ?? 0), 0);
  const phases = Object.keys(table.rows).filter((n) => n !== 'total');
  // Each column adds up to the total row.
  for (const c of want) {
    if (unknownOk.has(c) && table.rows.total[c] === null) continue;
    const s = phases.reduce((n, p) => n + (table.rows[p][c] ?? 0), 0);
    if (s !== table.rows.total[c]) out.push(`the column ${c} adds up to ${s}, its total says ${table.rows.total[c]}`);
  }
  // Each row adds up to its own total, and work to what it is made of.
  if (want.includes('total'))
    for (const n of Object.keys(table.rows)) {
      const s = sumOf(BUCKETS, table.rows[n]);
      if (s !== table.rows[n].total) out.push(`the row ${n} adds up to ${s}, its total says ${table.rows[n].total}`);
      if (want.includes('work')) {
        const w = sumOf(WORK, table.rows[n]);
        if (w !== table.rows[n].work) out.push(`the row ${n}'s work is ${table.rows[n].work}, its parts add up to ${w}`);
      }
    }
  // A receipt covers its own stretch of the clock and nothing else.
  if (kind === 'receipt') {
    const length = (Date.parse(fields.to) - Date.parse(fields.from)) / 60e3;
    if (Math.round(length) !== table.rows.total.total) out.push(`the table totals ${table.rows.total.total} min, the span from ${fields.from} to ${fields.to} is ${Math.round(length)}`);
  }
  return out;
}

/** Writes a record. The table is { columns, rows: { phase: { column: n } } }, total row included. */
export function formatRecord(kind, fields, table = null) {
  const lines = [`[${MARK} v${VERSION}] ${kind}`];
  const order = [...Object.keys(SCHEMA[kind].need), ...Object.keys(SCHEMA[kind].may)];
  for (const k of order) {
    const v = fields[k];
    if (v === undefined || v === null || v === '') continue;
    lines.push(`${k}: ${String(v).replace(/\s*\n\s*/g, ' ').trim()}`);
  }
  if (table) {
    const names = [...PHASES.filter((p) => table.rows[p]), ...(table.rows.total ? ['total'] : [])];
    const width = Math.max(5, ...names.map((n) => n.length));
    const cols = table.columns;
    const w = cols.map((c) => Math.max(c.length, ...names.map((n) => String(table.rows[n][c] ?? '-').length)));
    lines.push('');
    lines.push(`| ${'phase'.padEnd(width)} | ${cols.map((c, i) => c.padStart(w[i])).join(' | ')} |`);
    lines.push(`|${'-'.repeat(width + 2)}|${w.map((x) => `${'-'.repeat(x + 1)}:`).join('|')}|`);
    for (const n of names) lines.push(`| ${n.padEnd(width)} | ${cols.map((c, i) => String(table.rows[n][c] ?? '-').padStart(w[i])).join(' | ')} |`);
  }
  return lines.join('\n');
}

/**
 * Whole minutes that still add up. Every cell is rounded by the largest
 * remainder so that the cells together make the rounded total; rows and
 * columns are then the sums of the rounded cells, so nothing a reader adds
 * disagrees with what is printed.
 */
export function roundTable(cells, total) {
  // cells: { phase: { bucket: minutes } }
  const flat = [];
  for (const [p, row] of Object.entries(cells)) for (const b of BUCKETS) flat.push({ p, b, v: Math.max(0, row[b] || 0) });
  const target = Math.max(0, Math.round(total));
  const floors = flat.map((x) => Math.floor(x.v));
  let left = target - floors.reduce((a, b) => a + b, 0);
  const order = flat.map((x, i) => ({ i, r: x.v - Math.floor(x.v) })).sort((a, b) => b.r - a.r || a.i - b.i);
  for (let k = 0; left > 0 && order.length; k = (k + 1) % order.length, left--) floors[order[k].i]++;
  for (let k = order.length - 1; left < 0 && k >= 0; k--) if (floors[order[k].i] > 0) { floors[order[k].i]--; left++; }
  const rows = {};
  flat.forEach((x, i) => { (rows[x.p] ||= Object.fromEntries(BUCKETS.map((b) => [b, 0])))[x.b] = floors[i]; });
  for (const p of Object.keys(rows)) {
    rows[p].total = BUCKETS.reduce((n, b) => n + rows[p][b], 0);
    if (!rows[p].total) delete rows[p];
  }
  rows.total = Object.fromEntries([...BUCKETS, 'total'].map((c) => [c, Object.entries(rows).reduce((n, [, r]) => n + r[c], 0)]));
  return { columns: [...BUCKETS, 'total'], rows };
}

/**
 * Every record on every item of a normalized backlog, parsed, with where it
 * came from. A comment without the marker is not the set's and is left out.
 */
export function recordsOf(backlog) {
  const out = [];
  for (const issue of backlog.issues || [])
    for (const c of issue.comments || []) {
      if (!isMarked(c?.body)) continue;
      out.push({ item: issue.id, comment: c.id ?? null, author: c.author ?? null, postedAt: c.at ?? null, body: c.body, ...parseRecord(c.body) });
    }
  return out;
}

const canon = (body) => String(body).replace(/\r\n?/g, '\n').split('\n').map((l) => l.trim()).filter(Boolean).join('\n');

/**
 * The spans a backlog records, one per span id: its claim, its receipt, its
 * events. The same record posted twice (a retry) is one record; two different
 * records for one span's claim or receipt are a conflict, and a conflicted
 * span is not counted.
 */
export function spansOf(backlog) {
  const spans = new Map();
  const damaged = [];
  const get = (id) => {
    if (!spans.has(id)) spans.set(id, { id, claims: [], receipts: [], events: [], conflict: [] });
    return spans.get(id);
  };
  const other = { summaries: [], verdicts: [], recoveries: [] };
  for (const r of recordsOf(backlog)) {
    if (r.problems.length) { damaged.push(r); continue; }
    if (r.kind === 'claim') get(r.fields.span).claims.push(r);
    else if (r.kind === 'receipt') get(r.fields.span).receipts.push(r);
    else if (r.kind === 'event') get(r.fields.span).events.push(r);
    else other[{ summary: 'summaries', verdict: 'verdicts', recovery: 'recoveries' }[r.kind]].push(r);
  }
  // A retry posts the same record again: one record, however many copies.
  const once = (list) => {
    const seen = new Set();
    return list.filter((r) => { const k = `${r.item}\n${canon(r.body)}`; if (seen.has(k)) return false; seen.add(k); return true; });
  };
  for (const k of Object.keys(other)) other[k] = once(other[k]);
  for (const s of spans.values()) {
    for (const key of ['claims', 'receipts']) {
      const distinct = new Map();
      for (const r of s[key]) if (!distinct.has(canon(r.body))) distinct.set(canon(r.body), r);
      if (distinct.size > 1) s.conflict.push(key === 'claims' ? 'two different claims' : 'two different receipts');
      const items = new Set(s[key].map((r) => r.item));
      if (items.size > 1) s.conflict.push(`${key} on ${[...items].join(' and ')}`);
      s[key] = [...distinct.values()];
    }
    s.events = once(s.events);
    const where = new Set([...s.claims, ...s.receipts, ...s.events].map((r) => r.item));
    if (where.size > 1) s.conflict.push(`records of one span on ${[...where].join(' and ')}`);
    s.claim = s.claims[0] || null;
    s.receipt = s.receipts[0] || null;
    s.item = s.claim?.item ?? s.receipt?.item ?? s.events[0]?.item ?? null;
    s.session = s.claim?.fields.session ?? null;
    s.start = s.claim ? Date.parse(s.claim.fields.at) : null;
    s.end = s.receipt ? Date.parse(s.receipt.fields.to) : null;
  }
  // A session holds one span at a time: the next claim it makes ends the one
  // before, whether or not that one's receipt was written yet.
  const bySession = new Map();
  for (const s of spans.values()) if (s.session && s.claim) bySession.set(s.session, [...(bySession.get(s.session) || []), s]);
  for (const list of bySession.values()) {
    list.sort((a, b) => a.start - b.start);
    list.forEach((s, i) => { s.next = list[i + 1] || null; });
  }
  return { spans: [...spans.values()], damaged, ...other };
}

export const newSpanId = () => Array.from({ length: 8 }, () => '0123456789abcdef'[Math.floor(Math.random() * 16)]).join('');
