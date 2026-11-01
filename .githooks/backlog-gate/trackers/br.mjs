// beads_rust (br) recipe for the tracker-neutral mechanism (tracker-sync.mjs).
// This file is the only file in the skills tree allowed to name br.
// Plain node; no dependencies beyond node and git. Run as:
//   node trackers/br.mjs <subcommand> [options]
// Commands that read or write the store are run by the mechanism with
// cwd = the store worktree (the worktree holding this store's .beads), so
// every br command here discovers that store by walking up from cwd.
//
// Facts this recipe relies on (measured on br 0.7.0):
// - The tracker's settings (config.yaml, exclusive claims among them) and its
//   workflow policy (policy.yaml, custom statuses and what entering one
//   requires) are state like the items: they are declared as state files, so they travel on the ref
//   and a second machine's connect rebuilds the same behaviour instead of the
//   tracker's own default.
// - A fresh store needs ONLY .beads/issues.jsonl to rebuild the database with
//   the same item ids and comment ids (br init --prefix <p>, then
//   br sync --import-only --rebuild). beads.base.jsonl is not read for item
//   state; it is kept only as the merge ancestor so br's own `br sync --merge`
//   stays clean (a missing base makes it exit 6).
// - A prefix mismatch is silently tolerated by br (renaming is an opt-in
//   --rename-prefix), so this recipe verifies the store's prefix itself and
//   refuses with a named message when the sides disagree.
// - br's auto-import can absorb a hand-written issues.jsonl before a merge
//   runs, so every br command here passes --no-auto-import and imports
//   explicitly when the DB must follow the file.
// - br assigns per-item comment ids, so two machines both give the first
//   comment on an item id 1, and br's own per-item merge renumbers one of
//   them. A record (a br comment that is a decision's evidence) must keep the
//   same id in every export, so `post` gives every record its id at post time
//   from the store's machine ordinal (ordinal * 1000000 + br's local id),
//   and this recipe's merge never renumbers an existing record id.
// - br's database rebuild (br sync --import-only --rebuild) lays a database
//   backup per run into .beads/.br_recovery before it touches the database and
//   keeps every one: measured ~17-18 files per run on br 0.7.0 (database, WAL,
//   shm, WAL certificate, namespace sidecars; plain and vacuum each), while
//   `br update`, `br close`, `br sync --flush-only` and a plain
//   `br sync --import-only` (no --rebuild) measured zero. Because this recipe's
//   import runs on every conditional write, the folder grows without bound
//   (measured in one real store: 9553 files, 5.5 GB in 11 days). br gives no
//   way to skip or bound those backups: `br config schema` lists no such key,
//   and `br history prune` manages only .beads/.br_history (in this fixture it
//   pruned 0 backups while .br_recovery held 35). So the recipe bounds the
//   folder itself after a successful import (pruneRecovery below).

import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { pathToFileURL } from 'node:url';
import { ValueNeeded, valueOf } from '../argv.mjs';

const BR_GLOBAL = ['--no-color', '--no-auto-import'];

function fail(message) {
  process.stderr.write(`br recipe: ${message}\n`);
  process.exit(1);
}

// A conditional write (a claim, a transition) counts only once it is on the
// remote, and the state the mechanism just merged is the remote's. When the
// condition no longer holds, the write is not made and this is exit 3, so the
// mechanism says the write was lost instead of publishing it again.
function refuse(message) {
  process.stderr.write(`br recipe: ${message}\n`);
  process.exit(3);
}

function itemIn(cwd, id) {
  const found = readIssues(cwd).find((i) => i.id === id);
  if (!found) fail(`no item ${id} in the store (${path.join(storeDir(cwd), 'issues.jsonl')})`);
  return found;
}

function run(cmd, args, cwd, opts = {}) {
  const r = spawnSync(cmd, args, { cwd, encoding: 'utf8' });
  if (r.error) fail(`could not run ${cmd}: ${r.error.message}`);
  if (r.status !== 0 && !opts.allowFailure) {
    fail(`${cmd} ${args.join(' ')} failed with exit status ${r.status}\n${r.stderr || r.stdout || ''}`);
  }
  return r;
}

function br(args, cwd, opts = {}) {
  return run('br', [...BR_GLOBAL, ...args], cwd, opts);
}

function readJSON(file, what) {
  if (!fs.existsSync(file)) fail(`missing ${what}: ${file}`);
  try {
    return JSON.parse(fs.readFileSync(file, 'utf8'));
  } catch (e) {
    fail(`unreadable ${what} (${file}): ${e.message}`);
  }
}

function storeDir(cwd) {
  return path.join(cwd, '.beads');
}

// The tracker's own settings file: this tracker keeps settings that change its
// behaviour in a file beside its state (exclusive claims among them), and it is
// state like the items are. Left out of the move, the setting silently changes
// on every machine that connects. It travels on the ref and is read through the
// same merge, never parsed as a JSON line.
const CONFIG_PATH = '.beads/config.yaml';
// The tracker's workflow policy (custom statuses, what entering one requires) is
// the same kind of state: a store that moved without it silently drops the
// project's workflow on every machine that connects, and a project once carried
// it by hand beside the mechanism's own files.
const POLICY_PATH = '.beads/policy.yaml';
const SETTINGS_PATHS = [CONFIG_PATH, POLICY_PATH];
const ISSUES_PATH = '.beads/issues.jsonl';
const BASE_PATH = '.beads/beads.base.jsonl';

// ---- machine ordinals -------------------------------------------------------

// .tracker-sync/machines.json, written by the mechanism's connect through the
// recipe's `ordinal` command. Two machines never share an ordinal because the
// file is claimed through the ref (read, take the smallest unused ordinal,
// write, push; the mechanism refuses on a lost race).
function machinesFile(cwd) {
  return path.join(cwd, '.tracker-sync', 'machines.json');
}

function readOrdinals(cwd) {
  const f = machinesFile(cwd);
  if (!fs.existsSync(f)) {
    fail(`no machine ordinal in ${f}: run the mechanism's connect first, it claims this machine's ordinal`);
  }
  const data = readJSON(f, 'machine ordinals');
  if (!data || typeof data !== 'object' || !data.ordinals) {
    fail(`malformed machine ordinals in ${f}`);
  }
  return data;
}

function thisOrdinal(cwd) {
  const data = readOrdinals(cwd);
  const me = data.thisMachine || data.machine;
  if (me === undefined || data.ordinals[me] === undefined) {
    fail(`the machine ordinals in ${machinesFile(cwd)} do not name this machine`);
  }
  return data.ordinals[me];
}

// ---- state files ------------------------------------------------------------

function readIssues(cwd) {
  const f = path.join(storeDir(cwd), 'issues.jsonl');
  if (!fs.existsSync(f)) fail(`missing store file: ${f}`);
  return fs.readFileSync(f, 'utf8').split('\n').filter((l) => l.trim() !== '').map((l) => JSON.parse(l));
}

function writeIssues(cwd, items) {
  fs.writeFileSync(
    path.join(storeDir(cwd), 'issues.jsonl'),
    items.map((i) => JSON.stringify(i)).join('\n') + (items.length ? '\n' : ''),
  );
  // Keep br's own merge clean: base == the file just written (facts round 2 §5).
  fs.writeFileSync(path.join(storeDir(cwd), 'beads.base.jsonl'), fs.readFileSync(path.join(storeDir(cwd), 'issues.jsonl')));
}

// The prefix is verified here because br tolerates a mismatch silently. The
// live prefix lives in the database, not config.yaml, so it is read from the
// item ids themselves: every id is `<prefix>-<id>`.
function prefixesOf(items) {
  const found = new Set();
  for (const it of items) {
    if (typeof it.id !== 'string' || !it.id.includes('-')) fail(`item with unusable id ${JSON.stringify(it.id)}`);
    found.add(it.id.slice(0, it.id.indexOf('-')));
  }
  return found;
}

function checkPrefix(cwd, items) {
  const found = prefixesOf(items);
  if (found.size > 1) {
    fail(`store prefix mismatch: the store at ${storeDir(cwd)} holds items with more than one prefix (${[...found].join(', ')})`);
  }
  return [...found][0];
}

// ---- merge: per-item three-way ---------------------------------------------

// Sides arrive as one file each: the state files concatenated with
// `=== <path>` markers. --order is the remote's first-parent commits from the
// merge base to the remote tip, oldest first; --order-local names the local
// tip, which ranks as the newest because it is being published now.
//
// An item is kept as its line of text until something has to read it. In a
// store of thousands of items a sync changes a few, and parsing every item of
// three sides held several hundred megabytes for a one-comment change (#52):
// an item whose line is the same on both sides is the merge's answer as it
// stands, and only an item the sides disagree on is parsed. The ancestor file
// (beads.base.jsonl) is a copy of the items the merge writes itself, so its
// sides are never read.
function parseSides(file, name) {
  if (!fs.existsSync(file)) fail(`missing ${name} side file: ${file}`);
  return parseSideText(fs.readFileSync(file, 'utf8'));
}
function parseSideText(txt) {
  // Sections are found by their markers and read in place: a line is a slice of
  // the one string read, never a copy, and the ancestor's section is skipped.
  const marks = [];
  const re = /^=== (.+)$/gm;
  let m;
  while ((m = re.exec(txt))) marks.push([m[1].trim(), m.index, m.index + m[0].length + 1]);
  const out = {};
  for (let i = 0; i < marks.length; i++) {
    const [p, , start] = marks[i];
    const end = i + 1 < marks.length ? marks[i + 1][1] : txt.length;
    if (p === BASE_PATH) { out[p] = new Map(); continue; }
    if (SETTINGS_PATHS.includes(p)) { out[p] = txt.slice(Math.min(start, end), end).replace(/\n+$/, ''); continue; }
    const items = new Map();
    let at = Math.min(start, end);
    while (at < end) {
      let nl = txt.indexOf('\n', at);
      if (nl === -1 || nl > end) nl = end;
      const line = txt.slice(at, nl);
      if (line.trim() !== '') items.set(idOfLine(line), line);
      at = nl + 1;
    }
    out[p] = items;
  }
  return out;
}

// One item per line, keyed by its id without parsing the line where the id
// opens it, as this tracker's export writes it; any other line is parsed.
const LEADING_ID = /^\{"id":"((?:[^"\\]|\\.)*)"/;
function idOfLine(line) {
  const m = line.match(LEADING_ID);
  if (m && !m[1].includes('\\')) return m[1];
  return JSON.parse(line).id;
}
// Fields compared for the three-way rule. `updated_at` is the tracker's own
// order for every field; the machine clock is never consulted beyond it.
const FIELD_ORDER = 'updated_at';
const SKIP_FIELDS = new Set(['id', 'comments', FIELD_ORDER]);

function newerByOrder(a, b) {
  // a and b are items; returns the item whose FIELD_ORDER value is newer.
  // Where the field cannot separate the two values, ours (the local,
  // unpublished side) ranks as the tip being published now.
  if (a && b) {
    if (a[FIELD_ORDER] > b[FIELD_ORDER]) return 'ours';
    if (b[FIELD_ORDER] > a[FIELD_ORDER]) return 'theirs';
  }
  return 'ours';
}

function prefixesOfIds(ids) {
  const found = new Set();
  for (const id of ids) {
    if (typeof id !== 'string' || !id.includes('-')) fail(`item with unusable id ${JSON.stringify(id)}`);
    found.add(id.slice(0, id.indexOf('-')));
  }
  return found;
}

function cmdMerge(argv) {
  const want = {};
  for (const k of ['ours', 'theirs', 'base', 'out', 'order', 'store']) {
    const one = opt(argv, `--${k}`);
    if (one === undefined) fail(`merge: missing --${k}`);
    want[k] = one;
  }
  const orderLocal = opt(argv, '--order-local');

  const sides = parseSides(want.ours, 'ours');
  const theirsSides = parseSides(want.theirs, 'theirs');
  const baseSides = parseSides(want.base, 'base');

  for (const s of [sides, theirsSides, baseSides]) {
    if (!s[ISSUES_PATH]) fail(`merge: a side file does not hold ${ISSUES_PATH}`);
  }

  const ours = sides[ISSUES_PATH];
  const theirs = theirsSides[ISSUES_PATH];
  const base = baseSides[ISSUES_PATH];

  const op = prefixesOfIds(ours.keys());
  const tp = prefixesOfIds(theirs.keys());
  const overlap = [...op].filter((x) => tp.has(x));
  if (overlap.length === 0 && (op.size || tp.size)) {
    fail(`store prefix mismatch between the merged sides: ours has prefix ${[...op].join(', ') || '(none)'}, theirs has ${[...tp].join(', ') || '(none)'}`);
  }
  if (op.size > 1 || tp.size > 1) fail(`store prefix mismatch inside a merged side (${op.size > 1 ? [...op].join(', ') : [...tp].join(', ')})`);

  // The unique-id space for records and recorded overwrites comes from this
  // store's machine ordinal, so ids never collide across machines. Which ids are
  // taken is read only when a fresh one is first needed: most merges need none.
  const ordinal = thisOrdinal(want.store);
  let nextSeq = 500000;
  let taken = null;
  function freshId() {
    if (!taken) {
      taken = new Set();
      for (const side of [ours, theirs]) {
        for (const line of side.values()) for (const c of JSON.parse(line).comments || []) taken.add(String(c.id));
      }
    }
    let id;
    do {
      id = ordinal * 1000000 + nextSeq++;
    } while (taken.has(String(id)));
    taken.add(String(id));
    return id;
  }

  const parse = (line) => (line === undefined ? undefined : JSON.parse(line));
  const ids = new Set([...base.keys(), ...ours.keys(), ...theirs.keys()]);
  const merged = [];
  for (const id of ids) {
    const ol = ours.get(id), tl = theirs.get(id);
    // The same line on both sides is the merge's answer for that item.
    if (ol !== undefined && ol === tl) { merged.push([id, ol]); continue; }
    const b = parse(base.get(id)), o = parse(ol), t = parse(tl);
    if (b && !o && !t) continue; // deleted on both sides
    if (b && !o && t) {
      if (JSON.stringify(strip(t)) === JSON.stringify(strip(b))) continue; // deleted on ours, untouched on theirs
      merged.push([id, JSON.stringify({ ...t, comments: mergeComments(b, undefined, t, freshId, id, `deleted on ours`) })]);
      continue;
    }
    if (b && o && !t) {
      if (JSON.stringify(strip(o)) === JSON.stringify(strip(b))) continue;
      merged.push([id, JSON.stringify({ ...o, comments: mergeComments(b, o, undefined, freshId, id, `deleted on theirs`) })]);
      continue;
    }
    if (!b) {
      // Only on one side: keep it whole (a record that only appends).
      if (o && t) merged.push([id, JSON.stringify(pickSide(b, o, t, freshId, id))]);
      else merged.push([id, o ? ol : tl]);
      continue;
    }
    merged.push([id, JSON.stringify(pickSide(b, o, t, freshId, id))]);
  }
  merged.sort((a, b2) => (a[0] < b2[0] ? -1 : 1));

  // One item, field by field, against the common ancestor: a field only one
  // side moved away from the base takes that side, so two independent edits to
  // one item both survive. A field both sides moved takes the tracker's own
  // order, and where that cannot separate the two, the order they were
  // published on the ref - and the value that lost is recorded on the item.
  // Taking a whole item from the newer side would revert the other's field.
  function pickSide(b, o, t, freshId, id) {
    const out = { ...o };
    const notes = [];
    for (const f of new Set([...Object.keys(o), ...Object.keys(t)])) {
      if (SKIP_FIELDS.has(f)) continue;
      const bv = b ? b[f] : undefined, ov = o[f], tv = t[f];
      if (JSON.stringify(ov) === JSON.stringify(tv)) { out[f] = ov; continue; }
      const oursMoved = JSON.stringify(ov) !== JSON.stringify(bv);
      const theirsMoved = JSON.stringify(tv) !== JSON.stringify(bv);
      if (oursMoved && !theirsMoved) { out[f] = ov; continue; }
      if (theirsMoved && !oursMoved) { out[f] = tv; continue; }
      const side = newerByOrder(o, t);
      const win = side === 'ours' ? ov : tv;
      const lose = side === 'ours' ? tv : ov;
      out[f] = win;
      notes.push(`tracker-sync: field "${f}" set to ${JSON.stringify(win)} (from side ${side}); replaced ${JSON.stringify(lose)} from side ${side === 'ours' ? 'theirs' : 'ours'}`);
    }
    // The tracker's own field: take the newer of the two.
    out[FIELD_ORDER] = newerByOrder(o, t) === 'ours' ? (o[FIELD_ORDER] ?? t[FIELD_ORDER]) : (t[FIELD_ORDER] ?? o[FIELD_ORDER]);
    out.comments = mergeComments(b, o, t, freshId, id, null);
    for (const n of notes) {
      out.comments.push({ id: freshId(), issue_id: id, author: 'tracker-sync', text: n, created_at: out[FIELD_ORDER] });
    }
    // An item with no comments on either side keeps the tracker's own shape: an
    // empty list it never wrote would make the item's line differ on every merge.
    if (!out.comments.length && !('comments' in o) && !('comments' in t)) delete out.comments;
    return out;
  }

  // Comments are records that only append: union by comment id, both sides
  // kept. An existing record id is never renumbered. When two sides used the
  // same id for different comments (br counts per item per machine), the one
  // from theirs keeps the id and ours gets a fresh unique id, so a record
  // posted through `post` (which already holds a globally unique id) never
  // moves.
  function mergeComments(b, o, t, freshId, id, extraNote) {
    const out = [];
    const seen = new Map();
    const sides = [];
    if (b) sides.push(['base', b.comments || []]);
    if (o) sides.push(['ours', o.comments || []]);
    if (t) sides.push(['theirs', t.comments || []]);
    // Deterministic order: base comments first, then ours, then theirs, each
    // in their own stored order.
    for (const [sideName, list] of sides) {
      for (const c of list) {
        const key = String(c.id);
        if (seen.has(key)) {
          const prev = seen.get(key);
          if (JSON.stringify(prev) === JSON.stringify(c)) continue;
          // Same id, different content: renumber only the newcomer, never the
          // already-recorded one, and never a `post`ed record (its id is
          // above the ordinal boundary and cannot collide with a plain id).
          if (Number(c.id) < 1000000 && Number(prev.id) >= 1000000) {
            const c2 = { ...c, id: freshId() };
            out.push(c2);
            seen.set(String(c2.id), c2);
            continue;
          }
          if (Number(prev.id) < 1000000 && Number(c.id) >= 1000000) {
            // Replace the low-id one: it was not posted through the recipe.
            const idx = out.indexOf(prev);
            if (idx !== -1) out.splice(idx, 1);
            out.push(c);
            seen.set(key, c);
            continue;
          }
          // Two records that both came from the machine-id bands, with one id
          // and different content, cannot be resolved by moving either: a void
          // that names that id would then retire the wrong record. This is the
          // case the bands exist to prevent, so it is refused and named rather
          // than quietly renumbered.
          if (Number(c.id) >= 1000000 && Number(prev.id) >= 1000000) {
            fail(`two different records claim the id ${c.id} on one item: no machine may reuse one, and moving either would move what a void names. A person must decide`);
          }
          const c2 = { ...c, id: freshId() };
          out.push(c2);
          seen.set(String(c2.id), c2);
          continue;
        }
        seen.set(key, c);
        out.push(c);
      }
    }
    out.sort((a, b2) => Number(a.id) - Number(b2.id));
    return out;
  }

  function strip(it) {
    const { [FIELD_ORDER]: _, comments, ...rest } = it;
    return { ...rest, comments: (comments || []).map((c) => `${c.author}\u0000${c.text}`) };
  }

  // The tracker's own settings travel with the items, three-way against the merged
  // ancestor, and ABSENCE is one of the three values: a machine that deleted the
  // file did so to bring the tracker's own defaults back, and a merge that restored
  // the ancestor's file would silently re-enable a setting such as exclusive claims.
  // The side that moved away from the ancestor takes the merge; two sides that moved
  // differently are a refusal for a person, exactly as two different recordings of
  // one revision are.
  let settingsText = '';
  for (const p of SETTINGS_PATHS) {
    const [oc, tc, bc] = [sides[p], theirsSides[p], baseSides[p]];
    let settings; // undefined: the merged state carries no such file at all
    if (oc === tc) settings = oc;
    else if (bc === oc) settings = tc;
    else if (bc === tc) settings = oc;
    else fail(`the tracker's own settings (${p}) differ between the two sides and neither is the merged ancestor's: a person must decide which setting this store keeps`);
    // The mechanism writes what --out names and leaves anything else as it is, so a
    // merged deletion is applied here, in the store this merge runs in.
    if (settings === undefined) fs.rmSync(path.join(process.cwd(), p), { force: true });
    else settingsText += `=== ${p}\n${settings}${settings.endsWith('\n') ? '' : '\n'}`;
  }

  // Write the merged state files to --out: the merged issues.jsonl, the base
  // file as the merged ancestor, and the tracker's settings when either side has
  // them.
  const fd = fs.openSync(want.out, 'w');
  try {
    for (const p of [ISSUES_PATH, BASE_PATH]) {
      fs.writeSync(fd, `=== ${p}\n`);
      for (const [, line] of merged) fs.writeSync(fd, `${line}\n`);
    }
    fs.writeSync(fd, settingsText);
  } finally { fs.closeSync(fd); }
  process.stdout.write(`merged ${merged.length} items\n`);
}

// ---- the recovery backups a rebuild import leaves ----------------------------

// One successful rebuild import leaves a backup of the pre-import database
// family in .beads/.br_recovery (br 0.7.0), and br keeps every one. The backup
// is for br's own recovery: if the import dies mid-run, br restores the
// database family from the run's backups. Once the import has succeeded, br
// reads nothing of an older run again - and no restore can reference a backup
// run br has already superseded - so keeping every past run is dead weight that
// grows with every conditional write.
//
// What a prune loses. This recipe keeps the backups of the three newest backup
// runs (three distinct run timestamps in the file names) and deletes only
// older ones. After a successful import that is: the run just finished (kept),
// the two before it (kept), and everything older (deleted) - the evidence of
// pre-import states older than three conditional writes ago. The bound is not
// a concern about never having a backup: the newest run's complete family is
// always kept.
//
// Why this cannot delete a backup another process is still writing. br writes
// each backup by writing a private temp file and renaming it into its final,
// timestamped name, so a name this folder lists is fully written and is
// renamed, never appended to. This runs only after a rebuild import that the
// mechanism ran while holding the store's own write lock, so no other import
// was in flight; and the current import's own backups carry the newest
// timestamps, which are always kept. A concurrent process that starts later
// writes files whose names do not exist until it renames them, and this run
// never lists what it has not seen.
//
// Failure is not a write failure. The conditional write already happened; a
// prune that fails (a permission, a name that vanished between list and
// unlink) leaves the folder one run larger and the write still succeeds.
// Idempotence and cost: it reads one directory listing and makes no br call
// when fewer than three runs are present.
const RECOVERY_BACKUP_STAMP = /\d{8}_\d{6}_\d{9}/;
const RECOVERY_RUNS_KEPT = 3;

function pruneRecovery(cwd) {
  const dir = path.join(storeDir(cwd), '.br_recovery');
  let entries;
  try {
    entries = fs.readdirSync(dir);
  } catch {
    return; // no recovery folder yet: nothing to bound
  }
  const stamps = [...new Set(entries.map((n) => (n.match(RECOVERY_BACKUP_STAMP) || [])[0]).filter(Boolean))]
    .sort()
    .reverse();
  const keep = new Set(stamps.slice(0, RECOVERY_RUNS_KEPT));
  for (const name of entries) {
    const m = name.match(RECOVERY_BACKUP_STAMP);
    if (!m || keep.has(m[0])) continue;
    try {
      fs.unlinkSync(path.join(dir, name));
    } catch {
      // A name that vanished (or cannot be removed) is left where it lies; the
      // next successful import prunes again.
    }
  }
}

// Rebuild the database from the state files, then bound the recovery backups
// the rebuild left. This is the recipe's declared import command, run by the
// mechanism on every conditional write. Nothing it does here changes what the
// import wrote: pruning happens only after br exited with status 0.
//
// With --before (the state files as the database last held them, in the side
// format the merge reads), only what changed is imported. A rebuild of a store of
// thousands of items measured 32-38 s and ~440 MB on br 0.7.0, and the mechanism
// ran one on every merge, so two or three agents writing at once ran a machine
// out of memory (#52). What br offers instead, measured on the same store:
// - a plain import (no --rebuild) of a file holding only the changed items
//   updates those and leaves every other item and the item count alone, ~7 s
//   and ~190 MB, most of it br's own cache rebuild over the whole store;
// - it SKIPS an item whose updated_at did not move, so a comment that arrived
//   without moving it, or a comment id rewritten on an existing item, is not
//   imported ("Updated: 0");
// - a forced flush after it writes the whole state from the database again
//   (~3 s), which is what br certifies the state file against afterwards.
// So the partial import is checked rather than trusted: every changed item, as
// br exports it, must equal the item it was given, or the state file is put back
// and the store is rebuilt from it. A deleted item always rebuilds, because only
// a rebuild removes one. Nothing changed means nothing is run at all.
function cmdImport(argv = []) {
  const before = opt(argv, '--before');
  const cwd = process.cwd();
  let why = '';
  if (before !== undefined) {
    const r = importChanged(cwd, before);
    if (r.done) { process.stdout.write(`${r.done}\n`); return; }
    why = ` (${r.rebuild})`;
  }
  br(['sync', '--import-only', '--rebuild'], cwd);
  pruneRecovery(cwd);
  process.stdout.write(`imported from JSONL and rebuilt${why}\n`);
}

// br writes no key for an empty list or an absent value, and this recipe's merge
// may, so the two are one item: a key holding [] or null is no key.
const canonical = (v) => (Array.isArray(v) ? v.map(canonical)
  : v && typeof v === 'object' ? Object.fromEntries(Object.keys(v).sort()
    .filter((k) => v[k] !== null && !(Array.isArray(v[k]) && v[k].length === 0)).map((k) => [k, canonical(v[k])])) : v);
const sameItem = (a, b) => JSON.stringify(canonical(JSON.parse(a))) === JSON.stringify(canonical(JSON.parse(b)));

// Import the changed items only: { done: what was done } or { rebuild: why not }.
const PARTIAL_IMPORT_FLOOR = 50;
function importChanged(cwd, beforeFile) {
  if (!fs.existsSync(beforeFile)) return { rebuild: `no prior state at ${beforeFile}` };
  const was = parseSides(beforeFile, 'before')[ISSUES_PATH];
  if (!was) return { rebuild: `the prior state holds no ${ISSUES_PATH}` };
  const file = path.join(storeDir(cwd), 'issues.jsonl');
  const full = fs.readFileSync(file, 'utf8');
  const now = parseSideText(`=== ${ISSUES_PATH}\n${full}`)[ISSUES_PATH];
  for (const id of was.keys()) if (!now.has(id)) return { rebuild: `${id} was deleted, and only a rebuild removes an item` };
  const changed = [...now].filter(([id, line]) => was.get(id) !== line);
  if (!changed.length) return { done: 'nothing to import: the database already holds this state' };
  // br's partial import costs per item what its rebuild costs for the whole
  // store at about a tenth of it (measured: thousands of changed items ran past
  // two minutes, a rebuild of the same store 32-38 s). A change that touches
  // most of the store - an export format that moved with a tracker upgrade - is
  // rebuilt.
  if (changed.length > Math.max(PARTIAL_IMPORT_FLOOR, now.size / 10)) {
    return { rebuild: `${changed.length} of ${now.size} items changed, which a rebuild takes in faster` };
  }
  // br skips an item whose updated_at is not newer than the one it holds
  // (measured: equal, and earlier, both "Skipped ... up-to-date"), so such a
  // change is known not to land before br is asked: a comment renumbered by the
  // merge (two machines' comments share br's per-store counter) and an edit from
  // a machine whose clock runs behind are the usual ones.
  for (const [id, line] of changed) {
    const prior = was.get(id);
    if (prior !== undefined && !(String(JSON.parse(line)[FIELD_ORDER] ?? '') > String(JSON.parse(prior)[FIELD_ORDER] ?? ''))) {
      return { rebuild: `${id} changed without its ${FIELD_ORDER} moving forward, which br's partial import skips` };
    }
  }
  const putBack = (why) => { fs.writeFileSync(file, full); return { rebuild: why }; };
  try {
    fs.writeFileSync(file, changed.map(([, line]) => line).join('\n') + '\n');
    const imp = br(['sync', '--import-only'], cwd, { allowFailure: true });
    if (imp.status !== 0) return putBack(`the partial import failed (${imp.status}): ${(imp.stderr || imp.stdout || '').trim().split('\n')[0]}`);
    const fl = br(['sync', '--flush-only', '--force'], cwd, { allowFailure: true });
    if (fl.status !== 0) return putBack(`the export after the partial import failed (${fl.status}): ${(fl.stderr || fl.stdout || '').trim().split('\n')[0]}`);
    const exported = parseSideText(`=== ${ISSUES_PATH}\n${fs.readFileSync(file, 'utf8')}`)[ISSUES_PATH];
    for (const id of now.keys()) if (!exported.has(id)) return putBack(`${id} is missing from the export after the partial import`);
    for (const [id, line] of changed) if (!sameItem(exported.get(id), line)) return putBack(`br did not take ${id} as given`);
  } catch (e) {
    return putBack(`the partial import stopped: ${e.message}`);
  }
  // Keep br's own merge clean: base == the file just written (facts round 2 §5).
  fs.copyFileSync(file, path.join(storeDir(cwd), 'beads.base.jsonl'));
  pruneRecovery(cwd);
  return { done: `imported ${changed.length} changed item${changed.length === 1 ? '' : 's'}` };
}

// The same bound as a command of its own: the mechanism runs it when it repairs an
// installation whose backups already piled up, where no import is due.
function cmdPrune() {
  pruneRecovery(process.cwd());
}

// ---- claim / transition / post ----------------------------------------------

function cmdClaim(argv) {
  const item = opt(argv, '--item');
  const holder = opt(argv, '--holder', '');
  if (!item) fail('claim: missing --item <id>');
  // br update refuses terminal states and never closes anything here; the
  // recipe's claim only writes the holder. An empty holder clears it.
  const held = itemIn(process.cwd(), item).assignee || '';
  if (holder !== '' && held !== '' && held !== holder) {
    refuse(`claim lost: ${item} is held by ${held} in the merged state, so no claim by ${holder} is written`);
  }
  if (holder !== '' && held === holder) {
    process.stdout.write(`${item} is already held by ${holder}\n`);
    return;
  }
  if (holder === '') {
    br(['update', item, '--assignee', ''], process.cwd());
    process.stdout.write(`cleared assignee on ${item}\n`);
  } else {
    br(['update', item, '--assignee', holder], process.cwd());
    process.stdout.write(`set assignee of ${item} to ${holder}\n`);
  }
}

function cmdTransition(argv) {
  const item = opt(argv, '--item');
  const to = opt(argv, '--to');
  if (!item || !to) fail('transition: missing --item <id> --to <status>');
  const expect = opt(argv, '--expect');
  const now = itemIn(process.cwd(), item);
  if (expect && now.status !== expect) {
    refuse(`transition lost: ${item} is ${now.status} in the merged state, not ${expect}`);
  }
  if (now.status === to) refuse(`transition lost: ${item} is already ${to}`);
  if (now.status === 'closed' || now.status === 'tombstone') {
    refuse(`transition lost: ${item} is ${now.status}, and a transition does not reopen a terminal state`);
  }
  // br refuses `br update -s closed` by design; the dedicated command closes.
  if (to === 'closed') {
    br(['close', item], process.cwd());
  } else {
    br(['update', item, '-s', to], process.cwd());
  }
  process.stdout.write(`set status of ${item} to ${to}\n`);
}

function cmdPost(argv) {
  const item = opt(argv, '--item');
  const text = opt(argv, '--text');
  if (!item || text === undefined) fail('post: missing --item <id> --text <text>');
  const cwd = process.cwd();
  const ordinal = thisOrdinal(cwd);

  // Post the text EXACTLY as given; --json gives the local comment id.
  const r = br(['comments', 'add', item, '--json', '-m', text], cwd);
  const comment = JSON.parse(r.stdout.trim());
  const localId = comment.id;

  // Bring the database\'s export onto disk, then rewrite the record\'s id to
  // the machine-unique one and re-import, so br itself shows that id and a
  // later comment by br gets a fresh unused id. The record band is this
  // machine\'s ordinal * 1e6, on a grid of 1000: each record takes the next
  // free grid point and the counter jumps past it, so br\'s own per-item
  // counter (which continues from the last id on the item) never reaches the
  // next record\'s id until a thousand plain comments land between two posts.
  const mf = machinesFile(cwd);
  const data = readJSON(mf, 'machine ordinals');
  let counter = data.recordsNext && Number.isInteger(data.recordsNext) ? data.recordsNext : 1000;
  br(['sync', '--flush-only'], cwd);
  const items = readIssues(cwd);
  checkPrefix(cwd, items);
  const taken = new Set();
  for (const it of items) for (const c of it.comments || []) taken.add(Number(c.id));
  while (taken.has(ordinal * 1000000 + counter)) counter += 1000;
  const recordId = ordinal * 1000000 + counter;
  data.recordsNext = counter + 1000;
  fs.writeFileSync(mf, JSON.stringify(data, null, 2) + '\n');

  const target = items.find((it) => it.id === (comment.issue_id || item));
  if (!target) fail(`post: exported store does not hold item ${item}`);
  target.comments = target.comments || [];
  const rec = target.comments.find((c) => Number(c.id) === Number(localId) && c.text === text);
  if (!rec) fail(`post: exported comment (id ${localId}) on ${item} not found with the exact text`);
  rec.id = recordId;
  writeIssues(cwd, items);
  // Plain --import-only does not update comment ids on an existing item
  // (measured: the DB kept the old id); --rebuild replaces the rows from the
  // file, so the record id lands in the database and br's own next comment id
  // continues above it (max + 1).
  cmdImport();
  const back = readIssues(cwd).flatMap((it) => it.comments || []).find((c) => Number(c.id) === recordId);
  if (!back || back.text !== text) {
    fail(`post: record id ${recordId} did not survive the re-import with its text`);
  }
  process.stdout.write(`${recordId}\n`);
}

// An option's value is read the one way the set reads one (argv.mjs): a
// missing, empty, blank or option-shaped word is misuse that names the option,
// never a value the recipe goes on without -- `--expect ''` once dropped the
// precondition a transition must hold, and wrote as if it had held.
//
// One word is a value only because the command means it: `--holder ''` clears
// the holder (see cmdClaim), so an empty holder is accepted there and nowhere.
function opt(argv, flag, dflt) {
  const i = argv.indexOf(flag);
  if (i === -1) return dflt;
  if (flag === '--holder' && argv[i + 1] === '') return '';
  try {
    return valueOf(argv, i, flag);
  } catch (e) {
    if (!(e instanceof ValueNeeded)) throw e;
    process.stderr.write(`br recipe: ${e.message}\n`);
    process.exit(2);
  }
}

// ---- export a refused flush to a side file ----------------------------------
// A flush the tracker refuses is a flush whose state the mechanism still needs:
// the database may hold a write the state file lacks, and a merge or a rebuild
// from that file would drop it. This command puts the database's own state in a
// side folder, so the mechanism can merge it per item and write nothing into
// the store from a forced export.
//
// br's export guards are bypassed with --force, and --force is exactly what
// must not run in the store: it replaces the state file from the database,
// which is the loss the guards exist to prevent. So the database and the state
// files are copied beside the store and the forced export runs there.
//
// A copy taken while another session is writing the database can be a state
// that is older than the live one, and an export of it would silently lack a
// write accepted in between. So the store's own files are fingerprinted before
// the copy and again after the export, and the whole attempt is repeated while
// they changed: a store being written is retried, and one that will not stand
// still is refused by name rather than exported as an older state.
// What a store's own regular files are, so a copy taken between two writes is
// told apart from one taken while nothing was writing.
function storeFingerprint(store) {
  const seen = [];
  for (const n of fs.readdirSync(store).sort()) {
    if (/\.lock$/.test(n)) continue;
    const f = path.join(store, n);
    let st;
    try { st = fs.lstatSync(f); } catch { continue; }
    if (st.isFile()) seen.push(`${n}:${st.size}:${st.mtimeMs}`);
  }
  return seen.join('|');
}

function cmdExportSide(argv) {
  const out = opt(argv, '--out');
  if (!out) fail('export-side: missing --out <folder>');
  const cwd = process.cwd();
  const store = storeDir(cwd);
  if (!fs.existsSync(store)) fail(`export-side: no store at ${store}`);
  const copy = path.join(out, '.beads');
  const fingerprint = () => storeFingerprint(store);
  const state = path.join(copy, 'issues.jsonl');
  let exported = false;
  for (let attempt = 0; attempt < 3 && !exported; attempt++) {
    const before = fingerprint();
    fs.rmSync(copy, { recursive: true, force: true });
    fs.mkdirSync(copy, { recursive: true });
    try {
      fs.cpSync(store, copy, {
        recursive: true,
        dereference: false,
        // A lock is not state, a link is not state, and a named pipe or a socket
        // is never opened: a read of one would block here for ever.
        filter: (from) => {
          if (/\.lock$/.test(path.basename(from))) return false;
          let st;
          try { st = fs.lstatSync(from); } catch { return false; }
          return st.isDirectory() || st.isFile();
        },
      });
    } catch (e) {
      fail(`export-side: could not copy the store to ${copy}: ${e.message}`);
    }
    const db = path.join(copy, 'beads.db');
    if (!fs.existsSync(db)) fail(`export-side: the copy holds no database (${db}), so there is nothing to export`);
    const r = br(['--db', db, 'sync', '--flush-only', '--force'], out, { allowFailure: true });
    if (r.status !== 0) fail(`export-side: the forced export failed (${r.status}): ${(r.stderr || r.stdout || '').trim()}`);
    if (!fs.existsSync(state)) fail(`export-side: the forced export wrote no ${state}`);
    // Nothing of the store moved while the copy was taken and exported: the side
    // file is the state of the database this command was asked about.
    exported = fingerprint() === before;
  }
  if (!exported)
    fail(`export-side: the store at ${store} changed while its database was being exported three times over, so the copy may lack a write another command accepted; nothing is exported. Let the running command finish and try again`);
  process.stdout.write(`${state}\n`);
}

// ---- seed a candidate from the live database ---------------------------------
// A conditional write runs in a candidate: a checkout of the store's last
// commit, beside the live one, with a database of its own. Rebuilding that
// database from the state file is the same whole-store rebuild the import
// avoids (32-38 s and ~440 MB on thousands of items, #52), and the candidate is
// cut from a commit the live database was just flushed into. So the live
// database is copied instead (measured: a claim on the copy, 0.4 s), with the
// same care export-side takes: the copy is repeated while the live store moved,
// and refused when the live state file is not the candidate's - the database
// then holds a write the candidate's state lacks, and br would refuse the
// candidate's export. A refusal is not a failure: the mechanism imports instead.
// cwd = the candidate; --from names the live store's worktree.
const SEED_SKIP = new Set(['issues.jsonl', 'beads.base.jsonl', 'config.yaml', 'policy.yaml', '.gitignore']);
function cmdSeed(argv) {
  const from = opt(argv, '--from');
  if (!from) fail('seed: missing --from <the live store>');
  const live = storeDir(from);
  const here = storeDir(process.cwd());
  if (!fs.existsSync(path.join(live, 'beads.db'))) fail(`seed: no database in ${live} to copy`);
  const same = () => {
    try { return fs.readFileSync(path.join(live, 'issues.jsonl')).equals(fs.readFileSync(path.join(here, 'issues.jsonl'))); } catch { return false; }
  };
  fs.mkdirSync(here, { recursive: true });
  for (let attempt = 0; attempt < 3; attempt++) {
    const before = storeFingerprint(live);
    if (!same()) fail(`seed: the live state file differs from the candidate's, so its database holds a write the candidate's state lacks; import instead`);
    const copied = [];
    for (const n of fs.readdirSync(live)) {
      if (/\.lock$/.test(n) || SEED_SKIP.has(n)) continue;
      const f = path.join(live, n);
      let st;
      try { st = fs.lstatSync(f); } catch { continue; }
      // A lock is not state, a folder is br's own history or backups, and a named
      // pipe or a socket is never opened.
      if (!st.isFile()) continue;
      fs.copyFileSync(f, path.join(here, n));
      copied.push(n);
    }
    if (storeFingerprint(live) === before && same()) {
      // A copied file that names the live store would route the candidate's
      // writes back to it; br's own files name the database relatively.
      for (const n of copied) {
        const f = path.join(here, n);
        if (fs.statSync(f).size < 1024 * 1024 && fs.readFileSync(f).includes(Buffer.from(path.resolve(from)))) {
          fail(`seed: ${n} names the live store, so the copy is not isolated; import instead`);
        }
      }
      process.stdout.write(`seeded the candidate's database from ${live}\n`);
      return;
    }
  }
  fail(`seed: the live store at ${live} changed while its database was being copied three times over; import instead`);
}

// ---- pointer ----------------------------------------------------------------

function cmdPointerInstall(argv) {
  const checkout = opt(argv, '--checkout');
  if (!checkout) fail('pointer-install: missing --checkout <path>');
  const cwd = process.cwd();
  const store = storeDir(cwd);
  if (!fs.existsSync(store)) fail(`pointer-install: no store at ${store}`);
  const target = path.join(checkout, '.beads');
  const st = fs.lstatSync(target, { throwIfNoEntry: false });
  let relinked = false;
  if (st) {
    if (st.isSymbolicLink()) {
      const points = fs.readlinkSync(target);
      if (path.resolve(checkout, points) !== path.resolve(store)) {
        fail(`pointer-install: ${target} is a symlink to ${points}, not to the store ${store}`);
      }
    } else if (st.isDirectory()) {
      // A branch from before the move tracks its own .beads folder, so checking
      // it out replaces the pointer with a real folder; switching away leaves
      // that folder behind holding the checkout's locks and nothing of the
      // tracker. Leaving it there is what makes a tracker started in the
      // checkout a second tracker, so it is relinked. Anything of the tracker's
      // own in that folder makes it a tracker, and a tracker is refused by name,
      // never overwritten.
      // The locks br leaves behind are the leftover; anything else is state.
      const held = fs.readdirSync(target).filter((n) => !/\.lock$/.test(n) && n !== 'locks');
      if (held.length) {
        fail(`pointer-install: a real .beads folder stands at ${target} and holds ${held.join(', ')}, which is a tracker of its own, not the pointer to ${store}; refusing to overwrite it. Move it aside (keeping it is this checkout's decision) and run connect again`);
      }
      fs.rmSync(target, { recursive: true, force: true });
      fs.symlinkSync(store, target);
      relinked = true;
      process.stdout.write(`relinked .beads in ${checkout} to ${store} (the folder there held nothing but locks)\n`);
    } else {
      fail(`pointer-install: a real .beads file stands at ${target}; refusing. A careless \"br init --force\" there would write into the shared store ${store} and rewrite its prefix, so nothing is overwritten`);
    }
  }
  if (!relinked && !fs.existsSync(target)) fs.symlinkSync(store, target);
  const gi = path.join(checkout, '.gitignore');
  const line = '.beads';
  const cur = fs.existsSync(gi) ? fs.readFileSync(gi, 'utf8') : '';
  if (!cur.split('\n').some((l) => l.trim() === line)) {
    fs.writeFileSync(gi, cur + (cur && !cur.endsWith('\n') ? '\n' : '') + line + '\n');
    process.stdout.write(`added ${line} to ${gi}\n`);
  }
  process.stdout.write(`.beads in ${checkout} points at ${store}\n`);
}

function cmdPointerCheck(argv) {
  const checkout = opt(argv, '--checkout');
  if (!checkout) fail('pointer-check: missing --checkout <path>');
  // A real .beads folder where the pointer belongs is not a pointer: br walking
  // up from the checkout finds that folder's own (possibly empty) database and
  // answers with it, so a second tracker started beside the machine's store here
  // would be certified as reaching it. It is refused by name instead, and the
  // mechanism says which checkout is not pointed at the store.
  const here = path.join(checkout, '.beads');
  const st = fs.lstatSync(here, { throwIfNoEntry: false });
  if (st && !st.isSymbolicLink()) {
    fail(`${here} is a real ${st.isDirectory() ? 'folder' : 'file'}, not the link to the store ${storeDir(process.cwd())}. A tracker started there is a second tracker, not this machine's store. Run the mechanism's connect in this checkout to relink it: a folder holding nothing but locks is relinked, and a folder holding a tracker is refused and named`);
  }
  // The proof is the checkout's OWN pointer: `br` is run in that checkout with
  // the environment it has, and this command never invents a variable of its
  // own. A checkout that reaches the store only because some private variable
  // was injected for the check is not pointed at the store, and saying it is
  // would certify exactly the state the layout exists to prevent.
  const r = spawnSync('br', [...BR_GLOBAL, 'where'], { cwd: checkout, encoding: 'utf8' });
  if (r.stdout) process.stdout.write(r.stdout);
  if (r.status !== 0) {
    if (r.stderr) process.stderr.write(r.stderr);
    process.stderr.write(`${checkout} does not reach a store: run the mechanism's connect for this checkout, or give its agents the launcher line the integration records\n`);
    process.exit(r.status ?? 1);
  }
  process.exit(0);
}

function cmdPointerEnv() {
  const db = path.join(storeDir(process.cwd()), 'beads.db');
  process.stdout.write(`BEADS_DB=${db}\n`);
}

// ---- init -------------------------------------------------------------------

// The store's own first state, and only that: the mechanism calls this when it
// has just created an empty store, never in a checkout. br refuses to lay a
// store over one that is already there ("Already initialized"), which is the
// loud failure we want; `br init --force` would write into whatever store the
// .beads symlink points at and rewrite its prefix, so this recipe never passes it.
function cmdInit(argv) {
  const prefix = opt(argv, '--prefix', '');
  if (!prefix) fail('init: missing --prefix <prefix> (the item id prefix this store keeps, from the project)');
  const cwd = process.cwd();
  const r = br(['init', '--prefix', prefix], cwd, { allowFailure: true });
  if (r.status !== 0) {
    fail(`could not initialize the store's tracker (${r.status}): ${(r.stderr || r.stdout || '').trim()}\n`
      + `A .beads that already exists is refused on purpose: this recipe never passes --force, which would write into whatever store the symlink points at.`);
  }
  process.stdout.write((r.stdout || '').trim() + '\n');
}

// ---- ordinal ----------------------------------------------------------------

function cmdOrdinal(argv) {
  const machine = opt(argv, '--machine');
  if (!machine) fail('ordinal: missing --machine <name>');
  const cwd = process.cwd();
  const f = machinesFile(cwd);
  let data = { ordinals: {}, thisMachine: undefined };
  if (fs.existsSync(f)) data = readJSON(f, 'machine ordinals');
  data.ordinals = data.ordinals || {};
  if (data.ordinals[machine] === undefined) {
    const used = new Set(Object.values(data.ordinals));
    let n = 1;
    while (used.has(n)) n++;
    data.ordinals[machine] = n;
  }
  data.thisMachine = machine;
  fs.mkdirSync(path.dirname(f), { recursive: true });
  fs.writeFileSync(f, JSON.stringify(data, null, 2) + '\n');
  process.stdout.write(`${data.ordinals[machine]}\n`);
}

// ---- entry ------------------------------------------------------------------

const commands = {
  init: cmdInit,
  import: cmdImport,
  merge: cmdMerge,
  'export-side': cmdExportSide,
  claim: cmdClaim,
  transition: cmdTransition,
  post: cmdPost,
  'pointer-install': cmdPointerInstall,
  'pointer-check': cmdPointerCheck,
  'pointer-env': cmdPointerEnv,
  ordinal: cmdOrdinal,
  prune: cmdPrune,
  seed: cmdSeed,
};
const isMain = process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href;
if (isMain) {
  const [cmd, ...argv] = process.argv.slice(2);
  if (!cmd || !commands[cmd]) {
    process.stderr.write(`usage: node trackers/br.mjs <init|import|prune|merge|export-side|seed|claim|transition|post|pointer-install|pointer-check|pointer-env|ordinal> [options]\n`);
    process.exit(cmd ? 1 : 0);
  }
  commands[cmd](argv);
}

export default {
  name: 'br',
  tracker: 'beads_rust',
  ref: 'refs/tracker/state',
  storePath: '.beads',
  stateFiles: ['.beads/issues.jsonl', '.beads/beads.base.jsonl', '.beads/config.yaml', '.beads/policy.yaml'],
  export: 'br sync --flush-only',
  // How this tracker refuses a flush, and what each refusal means. The mechanism
  // knows no tracker: a refusal it does not find here is the dangerous one -
  // the database may hold writes the state file lacks - and is merged from the
  // database or stopped, never read as "nothing unexported".
  // Measured on br 0.7.0, each exit status 7:
  refusals: [
    // A store that has not imported the ref yet: an empty database over the
    // filled state file. Nothing is unexported, and the state files stand.
    { code: 7, match: 'Refusing to export empty database over non-empty JSONL', meaning: 'nothing-unexported' },
    // The state file changed since the database's last certified export, so the
    // database holds writes the file lacks.
    { code: 7, match: 'Refusing a no-op flush because the JSONL changed since its last certified sync', meaning: 'merge-database' },
    // The state file holds items the database never imported: exporting would
    // lose them, and importing would lose a write the database holds.
    { code: 7, match: 'Refusing to export stale database that would lose issues', meaning: 'merge-database' },
  ],
  // The database's own state in a side file, for a flush the tracker refuses.
  exportSide: 'node <recipeFile> export-side --out <out>',
  import: 'node <recipeFile> import',
  // The import that takes the state the database last held, and imports only
  // what differs from it; the mechanism uses it wherever it knows that state.
  importChanged: 'node <recipeFile> import --before <before>',
  // A candidate's database, copied from the live store's instead of rebuilt.
  seed: 'node <recipeFile> seed --from <live>',
  // Bounds the tracker's own database backups inside the store; the import runs it
  // too, and the mechanism runs it when it repairs an installation.
  prune: 'node <recipeFile> prune',
  init: 'node <recipeFile> init --prefix <prefix>',
  merge: 'node <recipeFile> merge --ours <ours> --theirs <theirs> --base <base> --out <out> --order <order> --order-local <orderLocal> --store <store>',
  claim: 'node <recipeFile> claim --item <item> --holder <holder>',
  transition: 'node <recipeFile> transition --item <item> --to <to> --expect <expect>',
  post: 'node <recipeFile> post --item <item> --text <text>',
  ordinal: 'node <recipeFile> ordinal --machine <machine>',
  pointer: {
    install: 'node <recipeFile> pointer-install --checkout <checkout>',
    check: 'node <recipeFile> pointer-check --checkout <checkout>',
    env: 'node <recipeFile> pointer-env',
  },
  prove: 'br where',
  launch: 'node <mechanismFile> watch --interval 10 --debounce 2',
};
