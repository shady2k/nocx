#!/usr/bin/env node
/**
 * The present-documents check: do the documents every agent acts on without
 * checking still name things that exist?
 *
 * Bought in a project whose architecture document, marked final and untouched
 * for two months, described a handful of modules while the code had grown to
 * more than ten times as many; it named a module whose job had moved to
 * another, and every agent that read it acted on it. The protocol's rule **What
 * describes the present is kept true** said to fix it in the pull request that
 * made it false; nothing noticed that nobody had.
 *
 * IT KNOWS NO PROJECT. Which documents describe the present is the project's
 * config (`presentDocuments`), never guessed here. It reads the base and the
 * head of a change from git and compares the two, the way reflexion models
 * compare a stated model with the source (Murphy, Notkin, Sullivan, FSE 1995).
 *
 * WHAT IT REFUSES, AND WHAT IT ONLY REPORTS:
 *
 *   dead-reference  a link in a present document whose target the head does
 *                   not have, or a code span the document named before the
 *                   change whose path the change removed. The change made the
 *                   document false: it is fixed in the same change. The only
 *                   verdict that fails.
 *   unknown-path    a code span naming a path the head lacks that the
 *                   document did not name before: a typo, a plan, "never
 *                   `docs/adr/`" or "`src/legacy/` was removed". Reported,
 *                   not refused, since a span may say a path is absent.
 *   old-dead        a dead reference already dead at the base. Drift that predates
 *                   the change: reported, and filed as debt by whoever runs
 *                   it, never fixed inside an unrelated change.
 *   uncovered-area  a code area no present document mentions. Often fine.
 *   aged-document   a document untouched while what it names changed in many
 *                   commits since. A reason to read it, not a verdict on it.
 *
 * WHAT IT CANNOT SEE:
 *
 *   - A CLAIM IS NOT A NAME. "Tokens are refreshed by the gateway" stays wrong
 *     after the gateway stops doing it; only a reader finds that. The report
 *     says which documents to read, and the reading is the agent's.
 *   - A REFERENCE IS WHAT IS WRITTEN AS ONE: a link, or a code span that
 *     looks like a path (it has a slash) and starts with a name the tree has
 *     had, since `application/json` and `internal/auth` look alike. A bare
 *     file name counts only once the tree has had a file of that name, since
 *     `Next.js` and `check.mjs` look alike. Prose, code blocks, placeholders
 *     and HTML comments are not read: they are where examples live.
 *   - A MODULE NAME IS NOT A PATH. `auth` in prose is not resolved against a
 *     package manager or a language; a document that names code by path is
 *     the one this can keep honest.
 *   - A RENAMED DOCUMENT CARRIES ITS OLD DEBT AS NEW. A reference is known by
 *     its document and its text, so moving a document with a broken link in
 *     it refuses the move until the link is fixed; its dead spans are
 *     reported as new.
 */

import { spawnSync } from 'node:child_process';
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, posix, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { pathMatcher } from './document-format.mjs';

export const RULES_VERSION = '0.42.0';
const HERE = dirname(fileURLToPath(import.meta.url));

class Misuse extends Error {}

// A refusal describes green: every verdict carries what the author has to reach.
const WHY = {
  'dead-reference': 'this change left a present document naming a path the tree no longer has (or never had): '
    + 'point it at what replaced it, or remove the statement, in this same change; a name that is not a path '
    + 'of this tree at all (a generated folder, an API route) goes into presentIgnores in the config',
  'unknown-path': 'names a path this tree does not have, newly: a typo, a plan, or a statement that it is absent; '
    + 'read it, and fix it or put it into presentIgnores',
  'old-dead': 'already dead before this change: file it as debt; it is not fixed inside unrelated work',
  'uncovered-area': 'no present document mentions this area: often fine; say so in the architecture if it matters',
  'aged-document': 'files were added, removed or renamed under what this document names in many commits since it was last edited: read it against the code',
};

// ---- what a document names ------------------------------------------------

const EXTERNAL = /^(?:[a-z][a-z0-9+.-]*:|\/\/|#|mailto:)/i;

function normal(path) {
  const parts = [];
  for (const part of path.split('/')) {
    if (!part || part === '.') continue;
    if (part === '..') { if (!parts.length) return null; parts.pop(); } else parts.push(part);
  }
  return parts.join('/');
}

// A code span is read as a path only when it could be nothing else.
function spanPath(span) {
  let s = span.trim();
  if (!s || /\s/.test(s) || /[<>*?{}$|`'"\\[\]()=,;!@%^&]/.test(s) || EXTERNAL.test(s)) return null;
  if (s.startsWith('-') || s.startsWith('/') || s.startsWith('~')) return null;
  s = s.replace(/(?::\d+(?:[-:]\d+)*|#L\d+(?:-L?\d+)?)$/, '');
  if (s.startsWith('./')) s = s.slice(2);
  if (!s || s === '.' || s === '..') return null;
  if (s.includes('/')) {
    // A slashed word that is not a path: a fraction, a version pair, a ratio.
    if (!/[a-zA-Z_]/.test(s)) return null;
    return { text: s.replace(/\/+$/, ''), bare: false };
  }
  return /^[\w.-]+\.[a-zA-Z][a-zA-Z0-9]{0,7}$/.test(s) ? { text: s, bare: true } : null;
}

const LIST_ITEM = /^\s*(?:[-*+]|\d+[.)])\s/;

/**
 * Every reference in a document's text. Fenced and indented code blocks and
 * HTML comments are examples and are not read; a code span is read as a path,
 * never searched for links, so `[see](x.md)` written as an example is not one.
 */
export function referencesIn(text) {
  const found = [];
  let fence = null, comment = false, indented = false, blank = true, listed = false;
  for (const raw of String(text).replace(/\r\n/g, '\n').split('\n')) {
    const mark = raw.match(/^\s{0,3}(`{3,}|~{3,})/);
    if (fence) { if (mark && mark[1][0] === fence[0] && mark[1].length >= fence.length) fence = null; continue; }
    if (mark && !comment) { fence = mark[1]; continue; }
    if (!raw.trim()) { blank = true; continue; }
    // An indented block is code only after a blank line outside a list, where
    // four spaces cannot be a list item's continuation.
    if (/^(?: {4}|\t)/.test(raw) && (indented || (blank && !listed))) { indented = true; blank = false; continue; }
    indented = false; blank = false;
    if (!/^\s/.test(raw)) listed = LIST_ITEM.test(raw);
    else if (LIST_ITEM.test(raw)) listed = true;
    let line = '';
    let rest = raw;
    while (rest) {
      if (comment) {
        const close = rest.indexOf('-->');
        if (close < 0) { rest = ''; break; }
        comment = false; rest = rest.slice(close + 3);
      } else {
        const open = rest.indexOf('<!--');
        if (open < 0) { line += rest; rest = ''; break; }
        line += `${rest.slice(0, open)} `; comment = true; rest = rest.slice(open + 4);
      }
    }
    // Code spans are taken out first and put back as tokens, so a link's
    // target is found only in text and a span inside a link's text is the link.
    const spans = [];
    line = line.replace(/(`+)([^`]+?)\1(?!`)/g, (_, _f, body) => { spans.push({ body, used: false }); return `\u0000${spans.length - 1}\u0000`; });
    const consume = (part) => { for (const m of part.matchAll(/\u0000(\d+)\u0000/g)) spans[Number(m[1])].used = true; };
    const definition = line.match(/^\s{0,3}\[(?!\^)[^\]]+\]:\s*<?([^\s>\u0000]+)>?/);
    if (definition) { found.push({ text: definition[1], kind: 'link' }); consume(line); }
    else line.replace(/!?\[((?:[^\]\\]|\\.)*)\]\(\s*<?([^)\s>\u0000]+)>?(?:\s+"[^"]*")?\s*\)/g, (whole, _t, target) => {
      found.push({ text: target, kind: 'link' });
      consume(whole);
      return ' ';
    });
    for (const span of spans) {
      if (span.used) continue;
      const p = spanPath(span.body);
      if (p) found.push({ text: p.text, kind: p.bare ? 'name' : 'path' });
    }
  }
  const seen = new Set();
  return found.filter((r) => {
    if (r.kind === 'link') {
      if (EXTERNAL.test(r.text)) return false;
      const bare = r.text.replace(/[#?].*$/, '');
      try { r.text = decodeURI(bare); } catch { r.text = bare; }
      r.text = r.text.replace(/\/+$/, '');
      if (!r.text) return false;
    }
    const key = `${r.kind}\t${r.text}`;
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
}

// ---- what a tree has --------------------------------------------------------

export function treeIndex(paths) {
  const files = new Set(paths);
  const dirs = new Set();
  const byName = new Map();
  const segments = new Set();
  for (const p of paths) {
    const name = p.slice(p.lastIndexOf('/') + 1);
    if (!byName.has(name)) byName.set(name, []);
    byName.get(name).push(p);
    segments.add(name);
    // Folders from the deepest up, stopping at one already known.
    for (let at = p.lastIndexOf('/'); at > 0; at = p.lastIndexOf('/', at - 1)) {
      const dir = p.slice(0, at);
      if (dirs.has(dir)) break;
      dirs.add(dir);
      segments.add(dir.slice(dir.lastIndexOf('/') + 1));
    }
  }
  // The tails of every folder, so `internal/auth` is found under `backend/`;
  // a file's tail is found through the files of its name instead of stored.
  const dirTails = new Set();
  for (const p of dirs) {
    let at = 0;
    for (;;) {
      dirTails.add(p.slice(at));
      at = p.indexOf('/', at) + 1;
      if (!at) break;
    }
  }
  const endsWith = (tail) => dirTails.has(tail)
    || (byName.get(tail.slice(tail.lastIndexOf('/') + 1)) || []).some((p) => p === tail || p.endsWith(`/${tail}`));
  return { files, dirs, names: byName, segments, endsWith, has: (p) => files.has(p) || dirs.has(p) };
}

const folderOf = (doc) => (posix.dirname(doc) === '.' ? '' : posix.dirname(doc));

/** The repository paths a reference could mean: beside its document, then from the root. */
export function placesOf(ref, doc) {
  if (ref.kind === 'name') return [];
  if (ref.kind === 'link') {
    const target = ref.text.startsWith('/') ? normal(ref.text) : normal(posix.join(folderOf(doc), ref.text));
    return target === null ? [] : [target];
  }
  return [...new Set([normal(posix.join(folderOf(doc), ref.text)), normal(ref.text)].filter((p) => p !== null))];
}

/**
 * How one reference in the document at `doc` resolves in `tree`: 'exact' where
 * it names a real path, 'tail' where it names only the end of one, false where
 * nothing matches, and null where it is not one this check can judge.
 */
export function resolves(ref, doc, tree, seen = tree) {
  if (ref.kind === 'link') {
    const [target] = placesOf(ref, doc);
    return target === undefined ? null : target === '' || tree.has(target) ? 'exact' : false;
  }
  if (ref.kind === 'name') return seen.names.has(ref.text) ? tree.names.has(ref.text) && 'exact' : null;
  // `application/json` and `origin/main` are not paths: a path starts with a
  // name the tree has had somewhere.
  const first = ref.text.split('/')[0];
  if (first !== '..' && !seen.segments.has(first)) return null;
  const places = placesOf(ref, doc);
  if (!places.length) return null;
  if (places.some((p) => tree.has(p))) return 'exact';
  const rooted = normal(ref.text);
  return rooted !== null && tree.endsWith(rooted) ? 'tail' : false;
}

// ---- the check --------------------------------------------------------------

/**
 * input: {
 *   base: { tree: [path], documents: [{path, text}] },
 *   head: { tree: [path], documents: [{path, text}] },
 *   areas: [path]            code areas at the head, from the config's roots
 *   churn: [{document, commits}]  commits that added, removed or renamed files
 *                                  under what each document names since it
 *                                  was last edited
 *   churnLimit: number
 *   untracked: [path]        repository paths git ignores: real on disk,
 *                             never in a tree
 *   ignores: [pattern]       what looks like a path and is not one of this
 *                             tree's: a generated folder, an API route
 * }
 */
export function checkPresent(input) {
  for (const side of ['base', 'head']) {
    const s = input?.[side];
    if (!s || !Array.isArray(s.tree) || !Array.isArray(s.documents)
      || s.documents.some((d) => typeof d?.path !== 'string' || typeof d?.text !== 'string'))
      throw new Misuse(`${side} needs a tree (a list of paths) and documents ({path, text})`);
  }
  if (!input.head.documents.length) throw new Misuse('no present document at the head: check presentDocuments in the config');
  const base = treeIndex(input.base.tree);
  const head = treeIndex(input.head.tree);
  // A bare name counts where either side has had a file of that name, and a
  // path where either side has had its first folder.
  const seen = { names: new Set([...base.names.keys(), ...head.names.keys()]), segments: new Set([...base.segments, ...head.segments]) };
  const ignored = (input.ignores || []).map(pathMatcher);
  const untracked = new Set(input.untracked || []);
  const skipped = (r, doc) => ignored.some((m) => m(r.text)) || placesOf(r, doc).some((p) => untracked.has(p));
  const judged = (side, tree) => {
    const all = new Map();
    for (const d of side.documents)
      for (const r of referencesIn(d.text)) {
        if (skipped(r, d.path)) continue;
        const how = resolves(r, d.path, tree, seen);
        if (how !== null) all.set(`${d.path}\t${r.kind}\t${r.text}`, { document: d.path, reference: r.text, ref: r, how });
      }
    return all;
  };
  const before = judged(input.base, base);
  const after = judged(input.head, head);
  const violations = [];
  for (const [key, v] of after) {
    const was = before.get(key);
    // A path named exactly before and only as the end of another one now (the
    // root `scripts/` gone, `pkg/b/scripts/` left) is gone as well.
    const dead = v.how === false || (was?.how === 'exact' && v.how === 'tail');
    if (!dead) continue;
    // What was already dead is older drift. A link says its target exists, so
    // a broken one is this change's error. A code span may say a path is
    // absent ("never `docs/adr/`", "`src/legacy/` was removed"), so a span is
    // this change's error only where the document named it before the change
    // and the path was there.
    const id = was && was.how === false ? 'old-dead'
      : v.ref.kind === 'link' || (was && was.how === 'exact') ? 'dead-reference' : 'unknown-path';
    violations.push({ id, document: v.document, reference: v.reference, why: WHY[id] });
  }
  const text = input.head.documents.map((d) => d.text).join('\n').toLowerCase();
  const word = (w) => new RegExp(`(?<![\\w-])${w.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}(?![\\w-])`).test(text);
  for (const area of input.areas || []) {
    const inside = input.head.documents.some((d) => d.path.startsWith(`${area}/`));
    if (!inside && !word(area.toLowerCase()) && !word(area.split('/').at(-1).toLowerCase()))
      violations.push({ id: 'uncovered-area', area, why: WHY['uncovered-area'] });
  }
  const limit = input.churnLimit ?? 20;
  for (const c of input.churn || [])
    if (limit > 0 && c.commits >= limit) violations.push({ id: 'aged-document', document: c.document, commits: c.commits, why: WHY['aged-document'] });
  return violations;
}

// ---- reading git ------------------------------------------------------------

function git(repo, args) {
  const r = spawnSync('git', ['-C', repo, ...args], { encoding: 'utf8', maxBuffer: 256 * 1024 * 1024 });
  if (r.error || r.status !== 0) throw new Misuse(`git ${args.join(' ')}: ${(r.stderr || r.error?.message || '').trim()}`);
  return r.stdout;
}

function side(repo, rev, matchers) {
  const tree = git(repo, ['ls-tree', '-r', '--name-only', '--full-tree', '-z', rev]).split('\0').filter(Boolean);
  const documents = tree.filter((p) => matchers.some((m) => m(p)))
    .map((path) => ({ path, text: git(repo, ['show', `${rev}:${path}`]) }));
  return { tree, documents };
}

// git check-ignore reads a working tree's rules, and the head's may differ from
// the checkout's, so they are copied into a scratch repository and asked there,
// with this machine's own git configuration kept out: the verdict is the
// repository's, not the machine's.
const ISOLATED = { ...process.env, GIT_CONFIG_GLOBAL: '/dev/null', GIT_CONFIG_NOSYSTEM: '1' };
function ignoredAt(repo, rev, paths) {
  const rules = git(repo, ['ls-tree', '-r', '--name-only', '--full-tree', rev]).split('\n')
    .filter((p) => p === '.gitignore' || p.endsWith('/.gitignore'));
  const dir = mkdtempSync(join(tmpdir(), 'present-ignore-'));
  try {
    const init = spawnSync('git', ['init', '-q', dir], { env: ISOLATED, encoding: 'utf8' });
    if (init.status !== 0) throw new Misuse(`git init for the ignore rules: ${(init.stderr || '').trim()}`);
    for (const p of rules) {
      mkdirSync(join(dir, posix.dirname(p)), { recursive: true });
      writeFileSync(join(dir, p), git(repo, ['show', `${rev}:${p}`]));
    }
    // Asked as a file and as a folder, since `/.cache/` ignores only the folder.
    const asked = paths.flatMap((p) => [p, `${p}/`]);
    const r = spawnSync('git', ['-C', dir, '-c', 'core.excludesFile=', 'check-ignore', '--no-index', '--stdin', '-z'],
      { input: asked.join('\0') + '\0', encoding: 'utf8', env: ISOLATED });
    if (r.status !== 0 && r.status !== 1) throw new Misuse(`git check-ignore: ${r.stderr.trim()}`);
    return [...new Set(r.stdout.split('\0').filter(Boolean).map((p) => p.replace(/\/$/, '')))];
  } finally { rmSync(dir, { recursive: true, force: true }); }
}

/** Code areas: the folders directly under each root, hidden ones left out. */
export function areasOf(tree, roots) {
  const areas = new Set();
  for (const root of roots) {
    const prefix = root ? `${root.replace(/\/+$/, '')}/` : '';
    for (const p of tree) {
      if (!p.startsWith(prefix)) continue;
      const parts = p.slice(prefix.length).split('/');
      if (parts.length > 1 && !parts[0].startsWith('.')) areas.add(prefix + parts[0]);
    }
  }
  return [...areas].sort();
}

export function checkedConfig(raw) {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) throw new Misuse('the config is not a JSON object');
  const list = (k, dflt) => {
    const v = raw[k] ?? dflt;
    if (!Array.isArray(v) || v.some((x) => typeof x !== 'string')) throw new Misuse(`${k} must be a list of strings`);
    return v;
  };
  const documents = list('presentDocuments', []);
  if (!documents.length) throw new Misuse('presentDocuments is empty: the config lists which documents describe the present');
  const churn = raw.presentChurnCommits ?? 20;
  if (!Number.isInteger(churn) || churn < 0) throw new Misuse('presentChurnCommits must be a whole number, zero or more');
  return { documents, areas: list('presentAreas', ['']), ignores: list('presentIgnores', []), churn };
}

export function fromGit(repo, baseRev, headRev, cfg) {
  const matchers = cfg.documents.map(pathMatcher);
  const base = side(repo, baseRev, matchers);
  const head = side(repo, headRev, matchers);
  const tree = treeIndex(head.tree);
  const churn = [];
  if (cfg.churn > 0) for (const d of head.documents) {
    const last = git(repo, ['log', '-1', '--format=%H', headRev, '--', d.path]).trim();
    const named = [...new Set(referencesIn(d.text).flatMap((r) => placesOf(r, d.path).filter((p) => p && tree.has(p)).slice(0, 1)))];
    if (!last || !named.length) continue;
    // What makes a document about structure stale is structure: files added,
    // removed or renamed under what it names, not edits inside them.
    const commits = git(repo, ['log', '--format=%H', '--diff-filter=ADR', `${last}..${headRev}`, '--', ...named])
      .split('\n').filter(Boolean).length;
    churn.push({ document: d.path, commits });
  }
  // A file git ignores (a generated config, a local store) is real and never
  // in a tree; the head's ignore rules say which, asked where each reference
  // points, never as written.
  const candidates = [...new Set(head.documents.concat(base.documents)
    .flatMap((d) => referencesIn(d.text).flatMap((r) => placesOf(r, d.path))).filter(Boolean))];
  const ignoredByGit = candidates.length ? ignoredAt(repo, headRev, candidates) : [];
  return { base, head, areas: areasOf(head.tree, cfg.areas), churn, churnLimit: cfg.churn, ignores: cfg.ignores, untracked: ignoredByGit };
}

// ---- report -----------------------------------------------------------------

export function report(violations, { documents, head }) {
  const of = (id) => violations.filter((v) => v.id === id);
  const lines = [`Present documents: ${documents} read at ${head}`];
  const fresh = of('dead-reference');
  lines.push(`new errors: ${fresh.length}${fresh.length ? ` — ${WHY['dead-reference']}` : ''}`);
  for (const v of fresh) lines.push(`  ${v.document}: \`${v.reference}\``);
  const unknown = of('unknown-path');
  if (unknown.length) {
    lines.push(`paths the head does not have, named newly (read, not refused): ${unknown.length} — ${WHY['unknown-path']}`);
    for (const v of unknown) lines.push(`  ${v.document}: \`${v.reference}\``);
  }
  const old = of('old-dead');
  if (old.length) {
    lines.push(`older dead references (debt, not refused): ${old.length} — ${WHY['old-dead']}`);
    for (const v of old) lines.push(`  ${v.document}: \`${v.reference}\``);
  }
  const areas = of('uncovered-area');
  if (areas.length) lines.push(`areas no present document mentions: ${areas.map((v) => v.area).join(', ')}`);
  for (const v of of('aged-document'))
    lines.push(`read against the code: ${v.document} (${v.commits} commits added, removed or renamed files under what it names since its last edit)`);
  return lines.join('\n');
}

// ---- selftest ---------------------------------------------------------------

export function selftest() {
  let failures = 0;
  const say = (ok, name, detail = '') => {
    console.log(`${ok ? 'PASS' : 'FAIL'}  present/${name}${detail}`);
    if (!ok) failures++;
  };
  const fixtures = JSON.parse(readFileSync(join(HERE, 'fixtures', 'present.json'), 'utf8'));
  for (const c of fixtures.cases) {
    let actual;
    try {
      actual = checkPresent(c.input).map((v) => [v.id, v.document ?? v.area, v.reference ?? v.commits ?? null]
        .filter((x) => x !== null && x !== undefined).join(' ')).sort();
    } catch (error) { actual = error instanceof Misuse ? ['invalid-input'] : [`crash ${error.message}`]; }
    const ok = JSON.stringify(actual) === JSON.stringify([...c.expected].sort());
    say(ok, c.name, ok ? '' : `: ${JSON.stringify(actual)}`);
  }
  for (const c of fixtures.references) {
    const actual = referencesIn(c.text).map((r) => `${r.kind} ${r.text}`);
    const ok = JSON.stringify(actual) === JSON.stringify(c.expected);
    say(ok, `references/${c.name}`, ok ? '' : `: ${JSON.stringify(actual)}`);
  }
  const unexplained = Object.keys(WHY).filter((k) => !WHY[k]);
  say(!unexplained.length, 'every verdict says what green looks like');
  return failures ? 1 : 0;
}

// ---- command line -----------------------------------------------------------

const USAGE = 'check-present.mjs --config CONFIG --base REV [--head REV] [--repo DIR] [--json]\n'
  + 'check-present.mjs --selftest | --version\n'
  + 'Exit: 0 no new dead reference, 1 this change left one, 2 invalid input or invocation.';

function main(args) {
  if (args.length === 1 && args[0] === '--version') { console.log(RULES_VERSION); return 0; }
  if (args.length === 1 && args[0] === '--selftest') return selftest();
  if (args.length === 1 && args[0] === '--help') { console.log(USAGE); return 0; }
  const options = {};
  for (let i = 0; i < args.length; i++) {
    const key = args[i];
    if (!['--config', '--base', '--head', '--repo', '--json'].includes(key) || Object.hasOwn(options, key))
      throw new Misuse(`unknown or repeated option: ${key}; see --help`);
    if (key === '--json') { options[key] = true; continue; }
    const value = args[++i];
    if (!value || value.startsWith('--')) throw new Misuse(`missing value: ${key}`);
    options[key] = value;
  }
  if (!options['--config'] || !options['--base']) throw new Misuse('--config and --base are required; see --help');
  let raw;
  try { raw = JSON.parse(readFileSync(options['--config'], 'utf8')); }
  catch (error) { throw new Misuse(`the config cannot be read: ${error.message}`); }
  const cfg = checkedConfig(raw);
  const repo = options['--repo'] || '.';
  const head = options['--head'] || 'HEAD';
  const input = fromGit(repo, options['--base'], head, cfg);
  const violations = checkPresent(input);
  const fresh = violations.filter((v) => v.id === 'dead-reference').length;
  console.log(options['--json']
    ? JSON.stringify({ version: RULES_VERSION, newErrors: fresh, violations })
    : report(violations, { documents: input.head.documents.length, head }));
  return fresh ? 1 : 0;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try { process.exitCode = main(process.argv.slice(2)); }
  catch (error) {
    console.error(`check-present.mjs: ${error.message}`);
    process.exitCode = 2;
  }
}
