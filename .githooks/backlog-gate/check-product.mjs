#!/usr/bin/env node
/**
 * The product-documents check: is every product document in its form, and does
 * every id a document names exist?
 *
 * The forms are the protocol's (references/product.md): a vision, sources,
 * hypotheses, user stories, use cases, product requirements, open questions,
 * prototypes and their results, each a Markdown file under docs/ whose first
 * line is its id and title, then its fields, then its sections. A repository
 * with no product document has nothing to check here and passes.
 *
 * IT KNOWS NO PROJECT AND NO WIKI. It reads files and says what is wrong with
 * them, file and line, in words a contributor who never heard of the set can
 * act on. A product wiki that accepts changes runs this same check.
 *
 * WHAT IT REFUSES:
 *
 *   document-form       the identity line, a field or a section is missing,
 *                       unknown, repeated or of the wrong shape
 *   unfinished-content  a placeholder (TODO, TBD, angle brackets,
 *                       [NEEDS CLARIFICATION]): an unknown is a question
 *   duplicate-id        two documents claim one id
 *   dangling-reference  a document under docs/ names an id no document holds,
 *                       or a prototype's code folder that does not exist
 *   wrong-link          a link field names a kind it may not grow from
 *   unknown-step        a use case's extension names no step of its scenario
 *   written-status      a Status: field: status is computed from the links
 *   system-part-in-requirement
 *                       a product requirement names code: it says what a user
 *                       observes, and the capability requirement says how
 *   unverified-quote    a quote is not at the place it names in a kept source
 *
 * WHAT IT ONLY REPORTS:
 *
 *   outside-quote       a quote from a source kept outside the repository,
 *                       which nothing here can read
 *
 * WHAT IT CANNOT SEE: whether a requirement is true, small or wanted. A
 * requirement can name no code and still describe a database; that is found
 * by reading. Ids are found as written (a prefix, a dash, three digits or
 * more), outside code blocks, code spans and HTML comments.
 */

import { spawnSync } from 'node:child_process';
import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs';
import { dirname, join, relative, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';

export const RULES_VERSION = '0.40.0';
const HERE = dirname(fileURLToPath(import.meta.url));

class Misuse extends Error {}

const WHY = {
  'document-form': 'a product document is its identity line "# <ID> — <title>", its fields as "Name: value", then its own sections; see the protocol\'s product documents',
  'unfinished-content': 'a placeholder is not content: write what is known, and make what is not an open question (Q-…) with who can answer it',
  'duplicate-id': 'each id belongs to one document and is never reused: give one of them the next free number',
  'dangling-reference': 'the id or folder named here does not exist: write that document, or correct the reference',
  'wrong-link': 'this field links only the kinds a document of this kind grows from',
  'unknown-step': 'an extension names the step of the main success scenario it alters, as 3a for step 3',
  'written-status': 'status is computed from the links and the checks: remove the Status: field',
  'system-part-in-requirement': 'a product requirement says what a user can observe and names no part of the system; how the system does it is a capability requirement that serves this one',
  'unverified-quote': 'the quote must stand, as written, at the lines or times the citation names in that source: correct the place or the quote',
  'outside-quote': 'the source is kept outside the repository, so this quote cannot be verified here',
};
const NOTES = new Set(['outside-quote']);

const PREFIXES = ['S', 'H', 'US', 'UC', 'FR', 'Q', 'P', 'R'];
const ID = /^(S|H|US|UC|FR|Q|P|R)-\d{3,}$/;
const MENTION = /(?<![\w-])(?:S|H|US|UC|FR|Q|P|R)-\d{3,}(?![\w-])/g;
const PLACEHOLDER = /\b(TODO|TBD|TKTK)\b|\[NEEDS CLARIFICATION|<[^>\n]+>/i;

const list = (...values) => ({ list: values });
const links = (...kinds) => ({ links: kinds });
const date = { date: true };
const text = { text: true };

// Each kind: its fields (required unless marked) and its sections.
const KINDS = {
  S: {
    name: 'source',
    fields: { Kind: list('conversation', 'interview', 'transcript', 'document', 'outside'), Date: date, With: text,
      Reference: { ...text, optional: true }, Redacted: { ...text, optional: true } },
    sections: { Record: { optional: true } },
  },
  H: {
    name: 'hypothesis',
    fields: { 'Grew from': links('VISION', 'S', 'Q') },
    sections: { Belief: {}, Test: {}, Metric: {}, Threshold: {}, Result: { optional: true }, Sources: { optional: true, sources: true } },
  },
  US: {
    name: 'user story',
    fields: { 'Grew from': links('VISION', 'H') },
    sections: { Story: { story: true }, Acceptance: { items: true }, Sources: { optional: true, sources: true } },
  },
  UC: {
    name: 'use case',
    fields: { Serves: links('US'), Level: list('summary', 'user goal', 'subfunction'), 'Primary actor': text, Scope: text,
      Scenario: { ...text, optional: true }, Trigger: { ...text, optional: true } },
    sections: { Preconditions: {}, 'Success end condition': {}, 'Failed end condition': {}, 'Main success scenario': { steps: true },
      Extensions: { optional: true, extensions: true }, 'Open issues': { optional: true, items: true },
      'Stakeholders and interests': { optional: true }, 'Minimal guarantee': { optional: true }, Sources: { optional: true, sources: true } },
  },
  FR: {
    name: 'product requirement',
    fields: { Serves: links('UC', 'US'), 'Verified by': list('test', 'demonstration', 'inspection', 'analysis') },
    sections: { Requirement: { noCode: true }, Rationale: { optional: true }, Sources: { optional: true, sources: true } },
  },
  Q: {
    name: 'open question',
    fields: { Concerns: links('VISION', ...PREFIXES), 'Who can answer': text },
    sections: { Question: {}, 'What settles it': {}, Answer: { optional: true, mayBeEmpty: true } },
  },
  P: {
    name: 'prototype',
    fields: { Tests: links('H'), Code: { path: true } },
    sections: { 'What it tries': {}, 'How to run': {} },
  },
  R: {
    name: 'result',
    fields: { Of: links('P'), Date: date },
    sections: { Prediction: {}, Observed: {}, Verdict: { verdict: true } },
  },
};
const COMMON = { Supersedes: { supersedes: true, optional: true } };

// ---- where the files come from -------------------------------------------

// A repository read three ways: its working tree, its staged files (what a
// commit hook checks) or a revision (what CI checks). Paths are POSIX,
// relative to the root.
function tree(root, { staged = false, rev = null } = {}) {
  if (!staged && !rev) {
    const all = [];
    const walk = (dir) => {
      for (const entry of readdirSync(dir)) {
        if (entry === '.git' || entry === 'node_modules') continue;
        const path = join(dir, entry);
        let st;
        try { st = statSync(path); } catch { continue; }
        if (st.isDirectory()) walk(path); else all.push(relative(root, path).split(sep).join('/'));
      }
    };
    if (!existsSync(root)) throw new Misuse(`no such folder: ${root}`);
    walk(root);
    return { files: all, read: (p) => readFileSync(join(root, p), 'utf8') };
  }
  const git = (...args) => {
    const r = spawnSync('git', ['-C', root, ...args], { encoding: 'utf8', maxBuffer: 1 << 28 });
    if (r.status !== 0) throw new Misuse(`git ${args.join(' ')} failed: ${r.stderr.trim()}`);
    return r.stdout;
  };
  const files = (staged ? git('ls-files', '-z', '--cached') : git('ls-tree', '-r', '-z', '--name-only', rev)).split('\0').filter(Boolean);
  return { files, read: (p) => git('show', staged ? `:${p}` : `${rev}:${p}`) };
}

// ---- reading one document -------------------------------------------------

// The lines of a Markdown text, with what is example rather than content
// (fenced code, HTML comments) marked so nothing is read from it.
function lines(textOf) {
  const out = [];
  let fence = null, comment = false;
  textOf.replace(/\r\n/g, '\n').split('\n').forEach((raw, i) => {
    const mark = raw.match(/^\s{0,3}(`{3,}|~{3,})(.*)$/);
    let structural = true;
    if (fence) {
      structural = false;
      if (mark && mark[1][0] === fence[0] && mark[1].length >= fence.length && !mark[2].trim()) fence = null;
    } else if (mark) { fence = mark[1]; structural = false; }
    else if (comment || raw.trimStart().startsWith('<!--')) {
      structural = false;
      comment = !raw.includes('-->');
    }
    out.push({ raw, line: i + 1, structural });
  });
  return out;
}

// The ids a line names, outside code spans.
const mentions = (raw) => [...raw.replace(/`[^`]*`/g, '').matchAll(MENTION)].map((m) => m[0]);

function parse(path, body, finding) {
  const all = lines(body);
  const first = all.find((l) => l.raw.trim());
  const head = first && first.structural && first.raw.match(/^# (\S+) — (.+)$/);
  if (!head || !ID.test(head[1])) {
    // A first line that starts with an id is a product document out of form.
    const near = first && first.structural && first.raw.match(/^#\s*((?:S|H|US|UC|FR|Q|P|R)-\d{3,})(?![\w-])/);
    if (near) finding('document-form', path, first.line, `the identity line is "# ${near[1]} — <title>", with a dash between the id and the title`);
    return near ? { broken: first.line } : null;
  }
  const id = head[1];
  const prefix = id.split('-')[0];
  const kind = KINDS[prefix];
  const doc = { id, prefix, kind, path, line: first.line, title: head[2].trim(), fields: {}, sections: {}, lines: all };
  const at = (line, rule, what) => finding(rule, path, line, what);
  if (PLACEHOLDER.test(doc.title)) at(first.line, 'unfinished-content', 'the title is a placeholder');
  let section = null;
  for (const l of all.slice(all.indexOf(first) + 1)) {
    if (!l.structural) { if (section) section.lines.push(l); continue; }
    const h2 = l.raw.match(/^## (.+)$/);
    if (h2) {
      const name = h2[1].trim();
      if (!Object.hasOwn(kind.sections, name)) at(l.line, 'document-form', `a ${kind.name} has no section "${name}"; its sections are ${Object.keys(kind.sections).join(', ')}`);
      else if (doc.sections[name]) at(l.line, 'document-form', `the section "${name}" is repeated`);
      section = { name, line: l.line, lines: [] };
      if (Object.hasOwn(kind.sections, name) && !doc.sections[name]) doc.sections[name] = section;
      continue;
    }
    if (/^#{1,6}\s/.test(l.raw) && !section) { at(l.line, 'document-form', 'a heading before the first section'); continue; }
    if (section) { section.lines.push(l); continue; }
    if (!l.raw.trim()) continue;
    const f = l.raw.match(/^([A-Z][A-Za-z ]*?):\s*(.*)$/);
    if (!f) { at(l.line, 'document-form', 'between the identity line and the first section only fields stand, one per line as "Name: value"'); continue; }
    const [, name, value] = f;
    if (name === 'Status') { at(l.line, 'written-status', 'status is not written'); continue; }
    if (!Object.hasOwn(kind.fields, name) && !Object.hasOwn(COMMON, name)) {
      at(l.line, 'document-form', `a ${kind.name} has no field "${name}"; its fields are ${Object.keys(kind.fields).join(', ')}`); continue;
    }
    if (doc.fields[name]) { at(l.line, 'document-form', `the field "${name}" is repeated`); continue; }
    doc.fields[name] = { value: value.trim(), line: l.line };
  }
  return doc;
}

// ---- the rules -------------------------------------------------------------

const content = (s) => s.lines.filter((l) => l.structural).map((l) => l.raw).join('\n').trim();
const items = (s) => s.lines.filter((l) => l.structural && /^\s*[-*+]\s+\S/.test(l.raw));

function inspect(doc, finding, ctx) {
  const { kind, path } = doc;
  const at = (line, rule, what) => finding(rule, path, line, what);
  const rules = { ...kind.fields, ...COMMON };
  for (const [name, rule] of Object.entries(rules)) {
    const f = doc.fields[name];
    if (!f || !f.value) {
      if (!rule.optional) at(f ? f.line : doc.line, 'document-form', `a ${kind.name} needs the field "${name}:"`);
      continue;
    }
    if (PLACEHOLDER.test(f.value)) { at(f.line, 'unfinished-content', `the field "${name}" is a placeholder`); continue; }
    if (rule.list && !rule.list.includes(f.value)) at(f.line, 'document-form', `"${name}" is one of ${rule.list.join(', ')}`);
    if (rule.date && !/^\d{4}-\d{2}-\d{2}$/.test(f.value)) at(f.line, 'document-form', `"${name}" is a date, YYYY-MM-DD`);
    if (rule.links || rule.supersedes) {
      const named = f.value.split(',').map((s) => s.trim()).filter(Boolean);
      for (const n of named) {
        if (n !== 'VISION' && !ID.test(n)) { at(f.line, 'document-form', `"${n}" is not an id`); continue; }
        const of = n === 'VISION' ? 'VISION' : n.split('-')[0];
        if (rule.links && !rule.links.includes(of)) at(f.line, 'wrong-link', `"${name}" of a ${kind.name} links ${rule.links.join(', ')}, not ${n}`);
        if (rule.supersedes && of !== doc.prefix) at(f.line, 'wrong-link', `a ${kind.name} supersedes another ${kind.name}, not ${n}`);
      }
    }
    if (rule.path) {
      const folder = f.value.replace(/^\.\//, '').replace(/\/+$/, '');
      if (!folder.startsWith('prototypes/') || !ctx.files.some((p) => p.startsWith(`${folder}/`)))
        at(f.line, 'dangling-reference', `"${name}" names ${f.value}, and prototypes/ holds no such folder with files in it`);
    }
  }
  if (doc.prefix === 'S') {
    const outside = doc.fields.Kind?.value === 'outside';
    if (outside && !doc.fields.Reference?.value) at(doc.fields.Kind.line, 'document-form', 'an outside source says where it is kept, in "Reference:"');
    if (!outside && doc.fields.Reference) at(doc.fields.Reference.line, 'document-form', 'only an outside source has a "Reference:"; a kept one holds its record');
    if (outside && doc.sections.Record) at(doc.sections.Record.line, 'document-form', 'an outside source holds no record');
    if (!outside && !doc.sections.Record) at(doc.line, 'document-form', 'a kept source needs the section "Record"');
  }
  for (const [name, rule] of Object.entries(kind.sections)) {
    const s = doc.sections[name];
    if (!s) { if (!rule.optional && doc.prefix !== 'S') at(doc.line, 'document-form', `a ${kind.name} needs the section "## ${name}"`); continue; }
    const body = content(s);
    if (!body) { if (!rule.mayBeEmpty) at(s.line, 'document-form', `the section "${name}" is empty`); continue; }
    for (const l of s.lines) if (l.structural && PLACEHOLDER.test(l.raw.replace(/`[^`]*`/g, ''))) at(l.line, 'unfinished-content', `a placeholder in "${name}"`);
    if (rule.story) {
      const parts = Object.fromEntries(items(s).map((l) => l.raw.match(/^\s*[-*+]\s+(As|I want|So that):\s*(.*)$/)).filter(Boolean).map((m) => [m[1], m[2].trim()]));
      for (const p of ['As', 'I want', 'So that']) if (!parts[p]) at(s.line, 'document-form', `a story is three items, "- As:", "- I want:" and "- So that:"; "${p}" is missing`);
    }
    if (rule.items && !items(s).length) at(s.line, 'document-form', `"${name}" is a list`);
    if (rule.steps) {
      const steps = s.lines.filter((l) => l.structural).map((l) => l.raw.match(/^(\d+)\.\s+\S/)).filter(Boolean).map((m) => Number(m[1]));
      if (!steps.length) at(s.line, 'document-form', 'the main success scenario is a numbered list of steps');
      else doc.steps = new Set(steps);
    }
    if (rule.extensions) for (const l of items(s)) {
      const m = l.raw.match(/^\s*[-*+]\s+(\d+)([a-z])\s+(.+?):\s*\S/);
      if (!m) { at(l.line, 'document-form', 'an extension is "- 3a <condition>: <what happens>"'); continue; }
      if (doc.steps && !doc.steps.has(Number(m[1]))) at(l.line, 'unknown-step', `the main success scenario has no step ${m[1]}`);
    }
    if (rule.verdict && !/^(confirmed|refuted|unclear)\b/i.test(body)) at(s.line, 'document-form', 'a verdict starts with confirmed, refuted or unclear');
    if (rule.noCode) for (const l of s.lines) if (l.structural && /`[^`]+`/.test(l.raw)) at(l.line, 'system-part-in-requirement', 'it names code');
    if (rule.sources) for (const l of items(s)) cite(l, doc, finding, ctx);
  }
}

const words = (s) => s.replace(/\s+/g, ' ').trim().toLowerCase();
const seconds = (t) => t.split(':').reduce((a, b) => a * 60 + Number(b), 0);

// A citation: S-001 L12-18 "quote", or S-001 00:12:30-00:13:05 "quote".
function cite(l, doc, finding, ctx) {
  const at = (rule, what) => finding(rule, doc.path, l.line, what);
  const m = l.raw.match(/^\s*[-*+]\s+(S-\d{3,})\s+(?:L(\d+)(?:-(\d+))?|(\d{1,2}:\d{2}:\d{2})-(\d{1,2}:\d{2}:\d{2}))\s+"(.+)"\s*$/)
    || l.raw.match(/^\s*[-*+]\s+(S-\d{3,})\s+()()()()"(.+)"\s*$/);
  if (!m) { at('document-form', 'a citation is "- S-001 L12-18 "the quote"" or "- S-001 00:12:30-00:13:05 "the quote""'); return; }
  const [, sid, from, to, start, end, quote] = m;
  const source = ctx.byId.get(sid);
  if (!source) return; // the reference rule names it
  if (source.prefix !== 'S') { at('wrong-link', `${sid} is not a source`); return; }
  if (source.fields.Kind?.value === 'outside') { at('outside-quote', `${sid} is kept outside the repository`); return; }
  if (!from && !start) { at('document-form', `a quote from a kept source names its place, as ${sid} L12-18`); return; }
  let place;
  if (from) {
    const a = Number(from), b = Number(to || from);
    place = source.lines.filter((x) => x.line >= a && x.line <= b);
    if (!place.length || b < a) { at('unverified-quote', `${sid} has no lines ${from}${to ? `-${to}` : ''}`); return; }
  } else {
    const a = seconds(start), b = seconds(end);
    let inside = false;
    place = [];
    for (const x of source.lines) {
      const t = x.raw.match(/^\s*\[(\d{1,2}:\d{2}:\d{2})\]/);
      if (t) inside = seconds(t[1]) >= a && seconds(t[1]) <= b;
      if (inside) place.push(x);
    }
    if (!place.length) { at('unverified-quote', `${sid} marks no time between ${start} and ${end}`); return; }
  }
  if (!words(place.map((x) => x.raw).join(' ')).includes(words(quote))) at('unverified-quote', `the quote is not at ${from ? `L${from}${to ? `-${to}` : ''}` : `${start}-${end}`} of ${sid}`);
}

/**
 * Every finding in a repository's product documents: { rule, file, line,
 * what, why, note }. A finding marked note is reported and refuses nothing.
 */
export function checkProduct(source) {
  const found = [];
  const finding = (rule, file, line, what) => found.push({ rule, file, line, what, why: WHY[rule] ?? null, note: NOTES.has(rule) });
  const markdown = source.files.filter((p) => p.startsWith('docs/') && p.endsWith('.md')).sort();
  const docs = [];
  const bodies = new Map();
  const broken = new Map();
  for (const path of markdown) {
    const body = source.read(path);
    bodies.set(path, body);
    const doc = parse(path, body, finding);
    if (doc?.broken) broken.set(path, doc.broken);
    else if (doc) docs.push(doc);
  }
  const byId = new Map();
  for (const d of docs) {
    if (byId.has(d.id)) finding('duplicate-id', d.path, d.line, `${d.id} is also ${byId.get(d.id).path}`);
    else byId.set(d.id, d);
  }
  if (!docs.length) return found.sort(order);
  const hasVision = source.files.includes('docs/vision.md');
  const ctx = { files: source.files, byId };
  for (const d of docs) inspect(d, finding, ctx);
  // Every id named anywhere under docs/ resolves, the specs' included.
  for (const [path, body] of bodies) for (const l of lines(body)) {
    if (!l.structural || broken.get(path) === l.line) continue;
    for (const n of new Set(mentions(l.raw))) if (!byId.has(n)) finding('dangling-reference', path, l.line, `no document is ${n}`);
    if (/(?<![\w-])VISION(?![\w-])/.test(l.raw.replace(/`[^`]*`/g, '')) && !hasVision && docs.some((d) => d.path === path))
      finding('dangling-reference', path, l.line, 'VISION is docs/vision.md, which does not exist');
  }
  return found.sort(order);
}
const order = (a, b) => a.file.localeCompare(b.file) || a.line - b.line || a.rule.localeCompare(b.rule);

// ---- the fixtures: one complete product, and one break per rule -------------

export function fixtureRepository() {
  const root = join(HERE, 'fixtures', 'product', 'good');
  return tree(root);
}

// A case edits the complete product in memory: replace text in a file, add a
// file or remove one; expected is every finding as "rule file:line".
export function fixtureCases() {
  const { cases } = JSON.parse(readFileSync(join(HERE, 'fixtures', 'product', 'cases.json'), 'utf8'));
  return cases.map((c) => {
    const base = fixtureRepository();
    const files = new Map(base.files.map((p) => [p, null]));
    const read = (p) => (files.get(p) ?? base.read(p));
    for (const e of c.edits) {
      if (e.remove) { files.delete(e.remove); continue; }
      if (e.add) { files.set(e.add, e.text); continue; }
      const now = read(e.file);
      if (!now.includes(e.replace)) throw new Error(`fixture ${c.name}: ${e.file} has no "${e.replace}"`);
      files.set(e.file, now.replace(e.replace, e.with));
    }
    return { ...c, source: { files: [...files.keys()], read } };
  });
}

export function selftest() {
  let failures = 0;
  const show = (ok, label) => { console.log(`${ok ? 'PASS' : 'FAIL'}  ${label}`); if (!ok) failures++; };
  const clean = checkProduct(fixtureRepository());
  show(clean.length === 0, `product/the complete example is clean${clean.length ? `: ${clean.map((f) => `${f.rule} ${f.file}:${f.line} ${f.what}`).join('; ')}` : ''}`);
  const fired = new Set();
  for (const c of fixtureCases()) {
    const got = checkProduct(c.source).map((f) => `${f.rule} ${f.file}:${f.line}`).sort();
    for (const g of got) fired.add(g.split(' ')[0]);
    const ok = JSON.stringify(got) === JSON.stringify([...c.expected].sort());
    show(ok, `product/${c.name}${ok ? '' : `: got ${JSON.stringify(got)}`}`);
  }
  const silent = Object.keys(WHY).filter((r) => !fired.has(r));
  show(!silent.length, `product/every rule has a case that fires it${silent.length ? `: ${silent.join(', ')}` : ''}`);
  show(checkProduct({ files: ['docs/notes.md'], read: () => '# Notes\n\nUS-East is a region, FR-12 a form.\n' }).length === 0,
    'product/a repository with no product document passes, whatever its text says');
  // Each template names exactly its kind's fields and sections, so a document
  // written from one is never refused for its shape.
  const folder = join(HERE, 'templates', 'product');
  const templates = readdirSync(folder).filter((f) => f.endsWith('.md'));
  const covered = new Set();
  for (const name of templates) {
    const t = readFileSync(join(folder, name), 'utf8').split('\n');
    const prefix = t[0].match(/^# ([A-Z]+)-<NNN> — /)?.[1];
    const kind = KINDS[prefix];
    if (!kind) { show(false, `product/template ${name} opens with a kind's identity line`); continue; }
    covered.add(prefix);
    const firstSection = t.findIndex((l) => l.startsWith('## '));
    const fields = t.slice(1, firstSection < 0 ? t.length : firstSection).map((l) => l.match(/^([A-Z][A-Za-z ]*?):/)?.[1]).filter(Boolean);
    const sections = t.map((l) => l.match(/^## (.+)$/)?.[1]).filter(Boolean);
    const same = (a, b) => JSON.stringify([...a].sort()) === JSON.stringify([...b].sort());
    show(same(fields, Object.keys(kind.fields)) && same(sections, Object.keys(kind.sections)),
      `product/template ${name} has the ${kind.name}'s fields and sections`);
  }
  show(Object.keys(KINDS).every((k) => covered.has(k)), 'product/every kind has its template');
  return failures ? 1 : 0;
}

// ---- the command line ------------------------------------------------------

function main(args) {
  if (args.length === 1 && args[0] === '--version') { console.log(RULES_VERSION); return 0; }
  if (args.length === 1 && args[0] === '--selftest') return selftest();
  if (args.length === 1 && args[0] === '--help') {
    console.log('check-product.mjs [--root <repository>] [--staged | --rev <revision>] [--json]\n'
      + 'Checks the product documents under docs/. Exit: 0 clean, 1 findings that refuse, 2 misuse or unreadable input.');
    return 0;
  }
  const opts = { root: '.', staged: false, rev: null, json: false };
  for (let i = 0; i < args.length; i++) {
    const a = args[i];
    if (a === '--json' && !opts.jsonSeen) { opts.json = true; opts.jsonSeen = true; }
    else if (a === '--staged' && !opts.staged) opts.staged = true;
    else if ((a === '--root' || a === '--rev') && args[i + 1] && !args[i + 1].startsWith('--')) {
      const key = a.slice(2);
      if (opts[`${key}Seen`]) throw new Misuse(`${a} is given twice`);
      opts[key] = args[++i]; opts[`${key}Seen`] = true;
    } else throw new Misuse(`unknown, repeated or incomplete option: ${a}`);
  }
  if (opts.staged && opts.rev) throw new Misuse('--staged and --rev read different trees: give one');
  const found = checkProduct(tree(resolve(opts.root), { staged: opts.staged, rev: opts.rev }));
  const refusing = found.filter((f) => !f.note);
  if (opts.json) console.log(JSON.stringify({ version: RULES_VERSION, findings: found }));
  else if (!found.length) console.log('Product documents: clean');
  else {
    for (const f of found) console.log(`${f.file}:${f.line}: ${f.note ? 'note' : 'error'} ${f.rule}: ${f.what}${f.why ? ` — ${f.why}` : ''}`);
    console.log(`${refusing.length} error(s), ${found.length - refusing.length} note(s)`);
  }
  return refusing.length ? 1 : 0;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try { process.exitCode = main(process.argv.slice(2)); }
  catch (error) { console.error(`check-product: ${error.message}`); process.exitCode = 2; }
}
