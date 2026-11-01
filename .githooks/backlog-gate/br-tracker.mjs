// beads_rust (br) recipe for the tracker-neutral mechanism (tracker-sync.mjs).
// This file is the only file in the skills tree allowed to name br.
// Plain node; no dependencies beyond node and git. Run as:
//   node trackers/br.mjs <subcommand> [options]
// Commands that read or write the store are run by the mechanism with
// cwd = the store worktree (the worktree holding this store's .beads), so
// every br command here discovers that store by walking up from cwd.
//
// Facts this recipe relies on (measured on br 0.7.0):
// - The tracker's settings (config.yaml, exclusive claims among them) are state
//   like the items: they are declared as a state file, so they travel on the ref
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
function parseSides(file, name) {
  if (!fs.existsSync(file)) fail(`missing ${name} side file: ${file}`);
  const sides = {};
  let current = null;
  for (const line of fs.readFileSync(file, 'utf8').split('\n')) {
    const m = line.match(/^=== (.+)$/);
    if (m) {
      current = m[1].trim();
      sides[current] = [];
    } else if (current) {
      sides[current].push(line);
    }
  }
  const out = {};
  for (const [p, lines] of Object.entries(sides)) {
    out[p] = p === CONFIG_PATH
      ? lines.join('\n').replace(/\n+$/, '')
      : lines.filter((l) => l.trim() !== '').map((l) => JSON.parse(l));
  }
  return out;
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

  const issuesPath = '.beads/issues.jsonl';
  const basePath = '.beads/beads.base.jsonl';
  for (const s of [sides, theirsSides, baseSides]) {
    if (!s[issuesPath]) fail(`merge: a side file does not hold ${issuesPath}`);
  }

  const ours = Object.fromEntries(sides[issuesPath].map((i) => [i.id, i]));
  const theirs = Object.fromEntries(theirsSides[issuesPath].map((i) => [i.id, i]));
  const base = Object.fromEntries(baseSides[issuesPath].map((i) => [i.id, i]));

  const op = prefixesOf(Object.values(ours));
  const tp = prefixesOf(Object.values(theirs));
  const overlap = [...op].filter((x) => tp.has(x));
  if (overlap.length === 0 && (op.size || tp.size)) {
    fail(`store prefix mismatch between the merged sides: ours has prefix ${[...op].join(', ') || '(none)'}, theirs has ${[...tp].join(', ') || '(none)'}`);
  }
  if (op.size > 1 || tp.size > 1) fail(`store prefix mismatch inside a merged side (${op.size > 1 ? [...op].join(', ') : [...tp].join(', ')})`);

  // The unique-id space for records and recorded overwrites comes from this
  // store's machine ordinal, so ids never collide across machines.
  const ordinal = thisOrdinal(want.store);
  let nextSeq = 500000;
  const taken = new Set();
  for (const it of [...Object.values(ours), ...Object.values(theirs)]) {
    for (const c of it.comments || []) taken.add(String(c.id));
  }
  function freshId() {
    let id;
    do {
      id = ordinal * 1000000 + nextSeq++;
    } while (taken.has(String(id)));
    taken.add(String(id));
    return id;
  }

  const ids = new Set([...Object.keys(base), ...Object.keys(ours), ...Object.keys(theirs)]);
  const merged = [];
  for (const id of ids) {
    const b = base[id], o = ours[id], t = theirs[id];
    if (b && !o && !t) continue; // deleted on both sides
    if (b && !o && t) {
      if (JSON.stringify(strip(t)) === JSON.stringify(strip(b))) continue; // deleted on ours, untouched on theirs
      merged.push({ ...t, comments: mergeComments(b, undefined, t, freshId, id, `deleted on ours`) });
      continue;
    }
    if (b && o && !t) {
      if (JSON.stringify(strip(o)) === JSON.stringify(strip(b))) continue;
      merged.push({ ...o, comments: mergeComments(b, o, undefined, freshId, id, `deleted on theirs`) });
      continue;
    }
    if (!b) {
      // Only on one side: keep it whole (a record that only appends).
      const it = o && t ? pickSide(b, o, t, freshId, id) : (o || t);
      merged.push(it);
      continue;
    }
    merged.push(pickSide(b, o, t, freshId, id));
  }
  merged.sort((a, b2) => (a.id < b2.id ? -1 : 1));

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
  const [oc, tc, bc] = [sides[CONFIG_PATH], theirsSides[CONFIG_PATH], baseSides[CONFIG_PATH]];
  let settings; // undefined: the merged state carries no settings file at all
  if (oc === tc) settings = oc;
  else if (bc === oc) settings = tc;
  else if (bc === tc) settings = oc;
  else fail(`the tracker's own settings (${CONFIG_PATH}) differ between the two sides and neither is the merged ancestor's: a person must decide which setting this store keeps`);
  // The mechanism writes what --out names and leaves anything else as it is, so a
  // merged deletion is applied here, in the store this merge runs in.
  if (settings === undefined) fs.rmSync(path.join(process.cwd(), CONFIG_PATH), { force: true });

  // Write the merged state files to --out: the merged issues.jsonl, the base
  // file as the merged ancestor, and the tracker's settings when either side has
  // them.
  const mergedText = merged.map((i) => JSON.stringify(i)).join('\n') + (merged.length ? '\n' : '');
  const settingsText = settings === undefined ? '' : `=== ${CONFIG_PATH}\n${settings}${settings.endsWith('\n') ? '' : '\n'}`;
  fs.writeFileSync(want.out, `=== ${issuesPath}\n${mergedText}=== ${basePath}\n${mergedText}${settingsText}`);
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
function cmdImport() {
  br(['sync', '--import-only', '--rebuild'], process.cwd());
  pruneRecovery(process.cwd());
  process.stdout.write('imported from JSONL and rebuilt\n');
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
function cmdExportSide(argv) {
  const out = opt(argv, '--out');
  if (!out) fail('export-side: missing --out <folder>');
  const cwd = process.cwd();
  const store = storeDir(cwd);
  if (!fs.existsSync(store)) fail(`export-side: no store at ${store}`);
  const copy = path.join(out, '.beads');
  // What the store's own regular files are, so a copy taken between two writes
  // is told apart from one taken while nothing was writing.
  const fingerprint = () => {
    const seen = [];
    for (const n of fs.readdirSync(store).sort()) {
      if (/\.lock$/.test(n)) continue;
      const f = path.join(store, n);
      let st;
      try { st = fs.lstatSync(f); } catch { continue; }
      if (st.isFile()) seen.push(`${n}:${st.size}:${st.mtimeMs}`);
    }
    return seen.join('|');
  };
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
};
const isMain = process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href;
if (isMain) {
  const [cmd, ...argv] = process.argv.slice(2);
  if (!cmd || !commands[cmd]) {
    process.stderr.write(`usage: node trackers/br.mjs <init|import|prune|merge|export-side|claim|transition|post|pointer-install|pointer-check|pointer-env|ordinal> [options]\n`);
    process.exit(cmd ? 1 : 0);
  }
  commands[cmd](argv);
}

export default {
  name: 'br',
  tracker: 'beads_rust',
  ref: 'refs/tracker/state',
  storePath: '.beads',
  stateFiles: ['.beads/issues.jsonl', '.beads/beads.base.jsonl', '.beads/config.yaml'],
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
