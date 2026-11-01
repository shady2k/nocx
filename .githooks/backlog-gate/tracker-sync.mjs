#!/usr/bin/env node
/**
 * The tracker-neutral sync mechanism: one store per machine, kept in step on a
 * ref no branch contains. This file names no tracker; a recipe does.
 *
 * The store is a linked worktree of the project's own repository, detached at
 * the ref (default refs/tracker/state), parked at the machine's state directory
 * outside every checkout. Inside it:
 *
 *   <the recipe's stateFiles>                  the tracker's committed state
 *   .tracker-sync/recordings/<revision>.jsonl  a reference to the store commit
 *                                              whose tree is the state that
 *                                              revision had, written once and
 *                                              never moved
 *
 * THE STORE REF'S HISTORY IS APPEND-ONLY, and a recording depends on it: nothing
 * here force-pushes the ref, resets it to a commit it did not build on, or
 * deletes a published commit, and a commit a recording names is an ancestor of
 * the recording's own commit. Records of the layout this replaced held the state
 * itself - a full copy per revision, 5 GB beside 10 MB of items in one store - and
 * are converted to references the first time a command meets a store that still
 * holds them; a copy no store commit holds is kept and reported, never guessed.
 * A copy is matched against a commit on the state files a commit can hold - the
 * recipe's declared files the store's own git does not ignore - because a
 * recording written from the working tree may carry a file the tracker keeps
 * locally and no commit ever holds.
 * A referenced commit that is missing stops the read loudly: the state of that
 * revision is not taken from a later one.
 *
 * The tracker's database is machine-local, rebuilt from the state files, and is
 * never committed, so two machines never collide on the ref over a database.
 * Everything of the mechanism that is not state (the lock, an empty hooks
 * directory, the watch pid file, the resolved configuration) lives beside the
 * worktree, at <machine dir>, never inside a checkout.
 *
 * A field changed on two sides takes the tracker's own order for that field;
 * where that cannot separate two values, the position of the commit that
 * published the value on the remote's first-parent chain decides, oldest first,
 * and a value not yet on the remote ranks as the tip being published now. A
 * machine's clock is never consulted.
 *
 * The machine's state folder is named after the MAIN checkout - the parent of
 * `git rev-parse --git-common-dir` - so every linked worktree of this machine
 * reaches the one store, not a store of its own.
 *
 * The record hooks this installs are composed onto the checkout's hooks, never
 * written into a folder inside the repository: with `core.hooksPath` pointing
 * into the tree, the line would carry this machine's absolute paths into every
 * machine's checkout, so connect refuses and prints the line to add by hand.
 * The hooks it installs are post-commit, post-merge and post-checkout; the
 * post-checkout one relinks a checkout whose pointer a pre-move branch replaced
 * with a real folder.
 *
 * Exit statuses, each with a message naming what is missing or who must fix it:
 *   0  ok
 *   1  a refusal the caller can fix: usage, no recipe, a pointer absent, a
 *      checkout that is not a repository, a hooks folder inside the repository, a
 *      failed recipe command, a malformed export, two different recordings of one
 *      revision. A recording that already exists is not one of these: it is the
 *      state the step exists to reach, and the command says so and exits 0.
 *   2  the runtime is missing: git, or a command the recipe needs
 *   3  a claim or a transition was lost: said out loud, nothing left behind
 *   4  the remote is unreachable and another machine's state was needed; the
 *      store here is this machine's only, and the command says so
 *   5  the remote refused this ref: a hosting policy, never counted with 4
 *
 * BOUNDED LATENCY, and what it costs: publish waits for the debounce of quiet or
 * the maximum batch age, whichever comes first, so a stream of writes is still
 * published within the maximum age. Every network git operation is killed after
 * its timeout (10 s for a push, 20 s for a fetch) and reported as unreachable or
 * refused rather than hanging. A recipe command is killed after the recipe
 * timeout (--recipe-timeout, recorded by connect, 120 s by default). Network
 * operations run outside the store mutation lock. A conditional operation
 * snapshots under the local lock (a copy of the live database where the recipe
 * can seed one, an import otherwise), then pushes its isolated candidate;
 * unrelated live writes survive timeout or retry.
 *
 * WORK IN PROPORTION TO THE CHANGE: a merge where only one side moved the state
 * merges nothing, and the database takes in only what changed where the recipe
 * declares importChanged; a whole-store import runs only where the database's
 * state is not known (a refused flush, a new machine) or the recipe asks for it.
 *
 * WHY A REF OUTSIDE THE BRANCHES STARTS NO CODE CI, and how far that goes: a
 * push event's ref is a branch or a tag. GitHub documents github.ref as "the
 * fully-formed ref of the branch or tag that triggered the workflow run. For
 * workflows triggered by push, this is the branch or tag ref that was pushed"
 * and github.ref_type as "valid values are branch or tag"
 * (https://docs.github.com/en/actions/reference/workflows-and-actions/contexts),
 * and the push event as "Runs your workflow when you push a commit or tag"
 * (https://docs.github.com/en/actions/reference/events-that-trigger-workflows).
 * That is the documented behaviour, and it is an inference about a provider, not
 * a proof: the proof is the project's own run list after the first push, read by
 * the owner. A host that refuses a ref outside refs/heads/ and refs/tags/ is
 * unsupported for this layout, and this mechanism says so by name (exit 5).
 *
 * No git command here sees the project's hooks, and the project's hooks never
 * see a tracker commit: every git call runs with core.hooksPath set to an empty
 * directory. The record hooks installed in code checkouts are composed onto
 * whatever hook is already there, and they only record a revision that is cut
 * or landed (post-commit, post-merge) — never a checkout.
 */
import { execFile } from 'node:child_process';
import { createHash, randomUUID } from 'node:crypto';
import { existsSync, mkdirSync, readFileSync, writeFileSync, writeSync, rmSync, statSync, readdirSync, openSync, closeSync, unlinkSync, readSync, renameSync, rmdirSync, realpathSync, cpSync, lstatSync } from 'node:fs';
import { dirname, join, resolve, basename, sep } from 'node:path';
import { homedir } from 'node:os';
import { pathToFileURL, fileURLToPath } from 'node:url';
import { AsyncLocalStorage } from 'node:async_hooks';
import { ValueNeeded, inlineValue, valueOf } from './argv.mjs';
import { notRegularFile, notRegularFileSaid, requireRegularFile } from './regular-file.mjs';

const HERE = dirname(new URL(import.meta.url).pathname);
const DEFAULT_REF = 'refs/tracker/state';
const FETCH_REF = 'refs/tracker-sync/remote';
const VERSION = readVersion();

function readVersion() {
  try {
    const f = join(HERE, 'tracker-sync.version');
    requireRegularFile(f, 'the version file');
    return readFileSync(f, 'utf8').trim();
  } catch { return 'unknown'; }
}
let THROW_ERRORS = false;
const die = (code, msg) => { if (THROW_ERRORS) throw Object.assign(new Error(msg), {exitCode:code}); process.stderr.write(`tracker-sync: ${msg}\n`); process.exit(code); };
const say = (msg) => process.stdout.write(`${msg}\n`);
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const nowIso = () => new Date().toISOString();

/** Parse --flag value, --flag=value and positional arguments. */
function parseArgs(argv) {
  const flags = {}; const pos = [];
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    const eq = a.match(/^--([A-Za-z][A-Za-z-]*)=(.*)$/);
    if (eq) {
      // `--at=` is an option given no value, not an option absent: read it the
      // one way the set reads a value (argv.mjs), so an emptied variable in the
      // caller's command cannot turn a historical export into today's state.
      try { flags[eq[1]] = inlineValue(eq[2], `--${eq[1]}`); } catch (e) {
        if (e instanceof ValueNeeded) die(2, e.message);
        throw e;
      }
      continue;
    }
    const bare = a.match(/^--([A-Za-z][A-Za-z-]*)$/);
    if (bare) {
      if (i + 1 < argv.length && !argv[i + 1].startsWith('--')) {
        // A word the caller wrote after the option is its value, and a value
        // that is empty, blank or shaped like another option is misuse.
        try { flags[bare[1]] = valueOf(argv, i, a); } catch (e) {
          if (e instanceof ValueNeeded) die(2, e.message);
          throw e;
        }
        i++;
      } else flags[bare[1]] = true;
      continue;
    }
    pos.push(a);
  }
  return { flags, pos };
}
const list = (v) => (v === undefined ? [] : Array.isArray(v) ? v : [v]);

// The variables git exports to a hook it runs. Under `post-commit` and
// `post-merge` this mechanism IS such a hook, and git sets GIT_DIR,
// GIT_INDEX_FILE, GIT_WORK_TREE and the rest to the code checkout's repository:
// a `git -C <store> ...` then reads and writes the CODE repository instead of
// the store worktree, and the first recording after a commit failed for exactly
// that reason. Nothing here needs them - every command names its own checkout
// and its own cwd - so every child runs without them. The tracker's own command
// is a child too, and it inherits the cleaned environment.
const HOOK_ENV = ['GIT_DIR', 'GIT_INDEX_FILE', 'GIT_WORK_TREE', 'GIT_PREFIX', 'GIT_OBJECT_DIRECTORY',
  'GIT_ALTERNATE_OBJECT_DIRECTORIES', 'GIT_COMMON_DIR', 'GIT_QUARANTINE_PATH', 'GIT_NAMESPACE', 'GIT_INDEX_VERSION'];
function childEnv(env) {
  const e = { ...process.env, ...(env || {}) };
  for (const k of HOOK_ENV) delete e[k];
  for (const k of Object.keys(e)) if (e[k] === null) delete e[k];
  return e;
}

/** Run a program, bounded in time. Never throws: the caller reads the status. */
function run(cmd, args, { cwd, env, timeoutMs = 20000, input } = {}) {
  return new Promise((res) => {
    const child = execFile(cmd, args, { cwd, env: childEnv(env), timeout: timeoutMs, maxBuffer: 64 * 1024 * 1024, killSignal: 'SIGKILL' },
      (err, stdout, stderr) => {
        const timedOut = !!err && (err.killed || err.signal) && !err.code;
        res({ code: err ? (typeof err.code === 'number' ? err.code : 1) : 0, stdout: stdout || '', stderr: stderr || '', timedOut, missing: !!err && err.code === 'ENOENT' });
      });
    if (input !== undefined && child.stdin) { child.stdin.end(input); }
  });
}
const sh = (command, opts) => run('/bin/sh', ['-c', command], opts);
const text = (r) => `${r.stdout}${r.stderr}`.trim();

// ---- the machine's own directory, the lock, the pid file --------------------

function machineDir(flags, repoRoot) {
  if (flags.state) return resolve(String(flags.state));
  const base = process.env.XDG_STATE_HOME || join(homedir(), '.local', 'state');
  // ONE STORE PER MACHINE, and every checkout and worktree of the machine
  // reaches it: the name is the MAIN checkout's, never the checkout the command
  // was run in. `git rev-parse --show-toplevel` differs per linked worktree, so
  // keying on it gave one machine a store per worktree; the parent of the common
  // git directory is the main checkout of every worktree of this repository, and
  // it is the same string from all of them. The value is resolved once, before
  // anything runs (main()), because resolving it needs git and every git call
  // here passes through machineDir for the empty hooks path.
  return base && CURRENT.machineKey ? join(base, CURRENT.machineKey) : join(base, basename(repoRoot));
}
const storePathOf = (flags, repoRoot) => join(machineDir(flags, repoRoot), 'tracker');
// macOS /tmp and symlinked checkouts must name the same physical directory.
// Hooks may not exist yet, so resolve their closest existing ancestor.
function canonicalPath(path) {
  try {return realpathSync(path);} catch (e) {
    const parent=dirname(path);if(parent===path) throw e;
    return join(canonicalPath(parent),basename(path));
  }
}

const configPath = (flags, repoRoot) => join(machineDir(flags, repoRoot), 'config.json');
const lockPath = (flags, repoRoot) => join(machineDir(flags, repoRoot), 'lock');
const pidPath = (flags, repoRoot) => join(machineDir(flags, repoRoot), 'watch.pid');
const noHooksPath = (flags, repoRoot) => join(machineDir(flags, repoRoot), 'nohooks');

const readConfig = (flags, repoRoot) => {
  try {
    const f = configPath(flags, repoRoot);
    requireRegularFile(f, 'the machine config');
    return JSON.parse(readFileSync(f, 'utf8'));
  } catch { return {}; }
};
const writeConfig = (flags, repoRoot, patch) => {
  const merged = { ...readConfig(flags, repoRoot), ...patch };
  mkdirSync(machineDir(flags, repoRoot), { recursive: true });
  writeFileSync(configPath(flags, repoRoot), `${JSON.stringify(merged, null, 2)}\n`);
  return merged;
};

// A command that dies inside a locked section still leaves the store usable: the
// lock names its process, and a lock whose process is gone is broken at once.
let LOCK_HELD = null;
process.on('exit', () => { LOCK_HELD?.release(); });

/** Publish a fully written, nonempty owner directory atomically. A reaper
 * removes only the dead owner's unique file; only its successful remover may
 * remove the now-empty directory. A successor is nonempty, so rmdir cannot
 * remove it. A live process is never evicted on timestamp alone.
 *
 * A contender that meets a lock on its way out tries again rather than failing:
 * the name may already be gone when it looks, and a holder is briefly between
 * unlinking its token and removing the directory. What still fails closed is an
 * owner nobody can verify: a malformed owner file, or several entries that stay.
 * The token carries no mtime anyone reads, so nothing refreshes one. */
export function takeLock(flags, repoRoot) {
  const p = lockPath(flags, repoRoot);
  mkdirSync(dirname(p), { recursive: true });
  const token = `owner-${process.pid}-${randomUUID()}.json`;
  const candidate = `${p}.${token}`;
  mkdirSync(candidate);
  writeFileSync(join(candidate, token), `${JSON.stringify({ pid: process.pid, at: nowIso(), token })}\n`, { flag: 'wx' });
  try {
    for (let attempt = 0; attempt < 3; attempt++) {
      try {
        renameSync(candidate, p);
        const lease = { release: null };
        let released = false;
        lease.release = () => {
          if (released) return;
          released = true;
          // Never unlink/rmdir a successor's name, even after an external
          // recovery or a repeated exit/release callback.
          try {
            unlinkSync(join(p, token));
            try {
              rmdirSync(p);
            } catch {}
          } catch {}
          if (LOCK_HELD === lease) LOCK_HELD = null;
        };
        LOCK_HELD = lease;
        return lease.release;
      } catch (e) {
        if (!['EEXIST', 'ENOTEMPTY', 'ENOTDIR'].includes(e.code)) throw e;
        // The holder may have released between the failed rename and this stat.
        let directory;
        try {
          directory = statSync(p).isDirectory();
        } catch (err) {
          if (err.code === 'ENOENT') continue;
          throw err;
        }
        let held, ownerFile;
        try {
          const names = directory ? readdirSync(p) : [];
          // A holder is between unlinking its token and removing the directory.
          if (directory && names.length === 0) continue;
          if (directory && names.length !== 1) throw new Error('unknown owner');
          ownerFile = directory ? join(p, names[0]) : p;
          requireRegularFile(ownerFile, "the store lock's owner file");
          held = JSON.parse(readFileSync(ownerFile, 'utf8'));
          if (!Number.isInteger(held.pid) || held.pid <= 0 || (directory && basename(ownerFile) !== held.token)) throw new Error('invalid owner');
        } catch { throw new Error(`the store lock ${p} has no verifiable owner; nothing was written; inspect it before manual recovery`); }
        let alive = true;
        try {
          process.kill(held.pid, 0);
        } catch (err) {
          alive = err.code !== 'ESRCH';
        }
        if (alive) throw new Error(`the store is locked by pid ${held.pid} since ${held.at} (${p}); another tracker-sync command is running`);
        say(`tracker-sync: breaking a lock whose process is gone (${p}, pid ${held.pid})`);
        // Unlink of an obsolete unique token fails rather than removing a new
        // holder. Only a successful remover attempts rmdir (which also refuses
        // a nonempty successor). Legacy file locks cannot replace a directory.
        try {
          unlinkSync(ownerFile);
          if (directory) {
            try {
              rmdirSync(p);
            } catch {}
          }
        } catch {}
      }
    }
    throw new Error(`could not take the store lock ${p}`);
  } finally {
    rmSync(candidate, { recursive: true, force: true });
  }
}

// ---- git, always without the project's hooks --------------------------------

function git(args, { cwd, timeoutMs } = {}) {
  const flags = CURRENT.flags; const repoRoot = CURRENT.repoRoot;
  // The mechanism commits to the store with its OWN identity, always: a CI runner,
  // a container, a supervisor running as another user and a fresh machine have no
  // user.name and user.email, and a store commit that depended on them would fail
  // everywhere the mechanism is meant to run unattended. The machine's own name
  // is in its config, so two machines' commits are told apart in the ref's log.
  return run('git', [
    '-c', `core.hooksPath=${noHooksPath(flags, repoRoot)}`,
    '-c', 'user.name=tracker-sync',
    '-c', `user.email=tracker-sync@${ourMachine() || 'machine'}`,
    ...args,
  ], { cwd, timeoutMs });
}
const gitOk = async (args, opts) => {
  const r = await git(args, opts);
  if (r.code !== 0) die(1, `git ${args.join(' ')} failed: ${text(r) || 'no output'}`);
  return r.stdout.trim();
};

const CURRENT = { flags: {}, repoRoot: null, recipe: null, store: null, ref: DEFAULT_REF, checkouts: [], machineKey: null };

/** The name of the machine's state folder: the main checkout of this repository.
 *
 * The parent of the common git directory answers for every worktree, linked ones
 * included, and for a checkout inside the main worktree alike; a repository git
 * cannot answer about falls back to this checkout's own name, which at least
 * keeps the command working.
 */
async function machineKeyOf(checkout) {
  const r = await run('git', ['-C', checkout, 'rev-parse', '--git-common-dir'], {});
  if (r.code !== 0 || !r.stdout.trim()) return basename(checkout);
  const common = resolve(checkout, r.stdout.trim());
  const main = dirname(common);
  return basename(main) || basename(checkout);
}

async function repoRootOf(checkout) {
  const r = await run('git', ['-C', checkout, 'rev-parse', '--show-toplevel'], {});
  if (r.missing) die(2, 'git is not on PATH');
  if (r.code !== 0) die(1, `${checkout} is not a git repository: ${text(r)}`);
  return r.stdout.trim();
}

// ---- the recipe: the port to one tracker ------------------------------------

const placeholders = (cmd, map) => cmd.replace(/<([A-Za-z][A-Za-z-]*)>/g, (m, k) => (k in map ? map[k] : m));
const quote = (s) => `'${String(s).split("'").join(`'\\''`)}'`;

async function loadRecipes() {
  const dir = join(HERE, 'trackers');
  let names = [];
  try { names = readdirSync(dir).filter((n) => n.endsWith('.mjs')).sort(); } catch {}
  const out = [];
  for (const n of names) {
    const p = join(dir, n);
    try {
      requireRegularFile(p, 'a shipped recipe');
      out.push({ path: p, ...((await import(pathToFileURL(p).href)).default || {}) });
    }
    catch (e) { out.push({ path: p, name: n.replace(/\.mjs$/, ''), broken: e.message }); }
  }
  return out;
}

async function resolveRecipe() {
  const { flags, repoRoot } = CURRENT;
  const named = flags.recipe || process.env.TRACKER_RECIPE || readConfig(flags, repoRoot).recipe;
  if (named) {
    const p = resolve(String(named));
    if (!existsSync(p)) die(1, `the recipe ${p} does not exist`);
    requireRegularFile(p, 'the recipe');
    try {
      const mod = (await import(pathToFileURL(p).href)).default;
      if (!mod || !mod.stateFiles) die(1, `the recipe ${p} declares no stateFiles`);
      return { path: p, ...mod };
    } catch (e) { die(1, `the recipe ${p} could not be loaded: ${e.message}`); }
  }
  const all = await loadRecipes();
  const usable = all.filter((r) => !r.broken);
  die(1, `no recipe: name the one this project's tracker needs with --recipe <file> or TRACKER_RECIPE. `
    + `This copy ships ${all.length ? all.map((r) => `${r.path}${r.broken ? ' (broken)' : ` (declares tracker ${r.tracker || r.name})`}`).join(', ') : 'none'}`);
}

/** How long one recipe command may run, in ms: --recipe-timeout, else the value
 * connect recorded for this machine, else 120 s.
 *
 * Bounded on purpose: a tracker command that hangs (its own store lock, a slow
 * disk) must not hold the store's lock with it, or the next session's claim
 * would wait on it. But the bound is the store's, not the mechanism's: a whole-store
 * import of thousands of items measured 32-38 s on an idle machine, and the
 * fixed 20 s this replaced killed it under load (#52). It is recorded beside the
 * other bounds (debounce, batch age, sync interval) so a project with a larger
 * store raises it without editing the mechanism. */
const RECIPE_TIMEOUT_S = 120;
function recipeTimeoutMs() {
  const v = CURRENT.flags['recipe-timeout'] ?? readConfig(CURRENT.flags, CURRENT.repoRoot).recipeTimeout ?? RECIPE_TIMEOUT_S;
  const n = Number(v);
  if (!Number.isFinite(n) || n <= 0) die(2, `the recipe timeout must be a number of seconds above 0, not ${JSON.stringify(v)} (--recipe-timeout, or recipeTimeout in ${configPath(CURRENT.flags, CURRENT.repoRoot)})`);
  return n * 1000;
}

/** Run one of the recipe's declared commands in the store. */
async function recipe(decl, map, { cwd, timeoutMs = recipeTimeoutMs() } = {}) {
  const r = CURRENT.recipe;
  const raw = decl.split('.').reduce((o, k) => (o === null || o === undefined ? o : o[k]), r);
  if (!raw) die(1, `the recipe ${r.path} declares no ${decl}`);
  const cmd = placeholders(typeof raw === 'string' ? raw : raw.command || '', {
    recipeFile: quote(r.path), mechanismFile: quote(join(HERE, 'tracker-sync.mjs')),
    store: quote(CURRENT.store), ref: CURRENT.ref, ...map,
  });
  if(CURRENT.isolatedFrom && cmd.includes(CURRENT.isolatedFrom)) throw new Error('cannot isolate conditional recipe: command names live store');
  const res = await sh(cmd, { cwd: cwd || CURRENT.store, timeoutMs, env: CURRENT.recipeEnv });
  return res;
}
async function recipeOk(decl, map, opts) {
  const r = await recipe(decl, map, opts);
  if (r.code !== 0) die(r.code === 127 || r.missing ? 2 : 1,
    r.timedOut
      ? `the recipe's ${decl} command was stopped after ${(opts?.timeoutMs ?? recipeTimeoutMs()) / 1000} s, the recipe timeout (--recipe-timeout, recorded by connect): ${text(r) || 'no output'}`
      : `the recipe's ${decl} command failed (${r.code}): ${text(r) || 'no output'}`);
  return r.stdout;
}

// ---- a flush the tracker refuses -------------------------------------------
/**
 * The tracker's own state, put into the state files before anything else reads
 * them. A write the tracker accepted lives in its database until the tracker
 * flushes it, and a fast-forward, a merge or a rebuild from the state files
 * would drop it: so the flush is either done here or understood, never assumed
 * harmless.
 *
 * The mechanism knows no tracker, so it knows no refusal either. The recipe
 * declares them, each with the meaning it has:
 *
 *   refusals: [{ code, match, meaning: 'nothing-unexported' | 'merge-database' }]
 *   exportSide: 'node <recipeFile> export-side --out <out>'
 *   candidateFiles: ['<a local file a conditional write needs>', ...]
 *   prune: '<a command that keeps the tracker's own backups within its bound>'
 *
 * `candidateFiles` is the only way a path the store's git ignores reaches a
 * candidate: a tracker that keeps a setting outside the committed state names it
 * there, and everything else the store holds - its backups, its history, the
 * recordings - stays out of the copy a conditional write makes. `prune` is run
 * once when an installation migrates and after a record or a sync it repairs, so
 * the tracker's own backups never grow without bound inside the store; a recipe
 * that declares none is left alone, and a prune that fails is said and never
 * turns into a failed write.
 *
 * 'nothing-unexported' is a refusal that proves no write sits only in the
 * database (a store that has not imported the ref yet: an empty database over a
 * filled file). 'merge-database' is everything else: the database and the state
 * files may disagree, so the database is exported to a side file and merged per
 * item with the state files before anything is fast-forwarded or merged. A
 * refusal the recipe does not classify is treated as 'merge-database', and one
 * that cannot be merged stops this command with exit 1: never continue.
 */
function refusalMeaning(r) {
  const said = text(r);
  for (const d of CURRENT.recipe.refusals || []) {
    if (!d || !d.meaning) continue;
    if (d.code !== undefined && Number(d.code) !== r.code) continue;
    if (d.match !== undefined && !said.includes(d.match)) continue;
    if (d.meaning === 'nothing-unexported' || d.meaning === 'merge-database') return d.meaning;
  }
  return 'merge-database';
}

/** The state files the recipe's forced export wrote into the side folder, in
 * the store's own paths. Only a regular file is read: a named pipe in there is
 * named, never opened. */
function sideParts(side) {
  const found = [];
  const walk = (dir) => {
    let names = [];
    try { names = readdirSync(dir); } catch { return; }
    for (const n of names.sort()) {
      const p = join(dir, n);
      const st = lstatSync(p, { throwIfNoEntry: false });
      if (!st) continue;
      if (st.isDirectory()) { walk(p); continue; }
      if (!st.isFile()) { say(`tracker-sync: the forced export left ${p}, which is not a regular file; it is not read`); continue; }
      const rel = p.slice(side.length + 1).split(sep).join('/');
      if (!stateFiles().includes(rel)) continue;
      found.push([rel, readFileSync(p, 'utf8')]);
    }
  };
  walk(side);
  return found;
}

/** Put what the tracker's database holds into the state files, and say so.
 * Caller holds the lock. */
async function mergeDatabaseIntoState({ code, said }) {
  const recipeFile = CURRENT.recipe.path;
  const atRisk = () => `the state files (${stateFiles().join(', ')}) may lack writes the tracker's database holds`;
  if (!CURRENT.recipe.exportSide)
    die(1, `the tracker refused to flush its database (${code}): ${said}. ${atRisk()}, and the recipe ${recipeFile} declares no forced export to a side file (exportSide), so nothing is merged and nothing is written: this store stops rather than take the state files as they are`);
  const side = join(machineDir(CURRENT.flags, CURRENT.repoRoot), 'database-export');
  rmSync(side, { recursive: true, force: true });
  mkdirSync(side, { recursive: true });
  const forced = await recipe('exportSide', { out: quote(side) }, { timeoutMs: Math.max(120000, recipeTimeoutMs()) });
  if (forced.code !== 0 || forced.missing)
    die(2, `the tracker refused to flush its database (${code}): ${said}. Its forced export to a side file failed (${forced.code}): ${text(forced).split('\n')[0] || 'no output'}. ${atRisk()}, so nothing is merged and nothing is written`);
  const fromDatabase = sideParts(side);
  if (!fromDatabase.length)
    die(1, `the tracker refused to flush its database (${code}): ${said}. Its forced export to ${side} wrote no state file (${stateFiles().join(', ')}), so ${atRisk()} and nothing was merged`);
  // Per item, with no ancestor: an item or a record that only one side holds is
  // kept whole from that side, so neither the database's write nor an item the
  // database has not imported yet can be dropped by this merge. The tracker's
  // own order decides a field both sides moved (updated_at, or the recipe's
  // equivalent), exactly as the sync's merge does.
  const ours = stateFiles().map((p) => {
    const fromSide = fromDatabase.find(([q]) => q === p);
    return fromSide || [p, null];
  });
  const theirs = await storeParts();
  const withoutAncestor = stateFiles().map((p) => [p, '']);
  const merged = await mergeParts({ ours, theirs, base: withoutAncestor });
  await writeState(merged);
  rmSync(side, { recursive: true, force: true });
  say(`tracker-sync: the tracker refused to flush its database (${code}): ${said}; its state was exported to a side file and merged per item with the state files, so no write the tracker accepted is left in the database alone`);
  return { merged: true };
}

/** The tracker's state in the state files, or the write saved and merged. */
async function flushDatabase() {
  const flushed = await recipe('export', {});
  if (flushed.code === 0) return { ok: true };
  if (flushed.missing || flushed.code === 127)
    die(2, `the recipe's export command could not run (${flushed.code}): ${text(flushed) || 'no output'}`);
  const said = text(flushed).split('\n')[0] || 'no output';
  if (refusalMeaning(flushed) === 'nothing-unexported') {
    say(`tracker-sync: the tracker refused to flush its database (${flushed.code}): ${said}; it holds no unexported write, and the state files are taken as they are`);
    return { ok: true, nothing: true };
  }
  return mergeDatabaseIntoState({ code: flushed.code, said });
}

// ---- the store: a worktree detached at the ref ------------------------------

const headOid = async () => (await gitOk(['rev-parse', 'HEAD'], { cwd: CURRENT.store }));

async function ensureStore({ create = false } = {}) {
  const { flags, repoRoot } = CURRENT;
  const store = storePathOf(flags, repoRoot);
  if (!create && !existsSync(store)) {
    CURRENT.store = store;
    return { store, absent: true };
  }
  mkdirSync(machineDir(flags, repoRoot), { recursive: true });
  mkdirSync(noHooksPath(flags, repoRoot), { recursive: true });
  let fresh = false;
  if (!existsSync(store)) {
    fresh = true;
    let have = (await git(['rev-parse', '--verify', '--quiet', CURRENT.ref], { cwd: repoRoot })).code === 0;
    if (!have) {
      await git(['fetch', 'origin', `+${CURRENT.ref}:${CURRENT.ref}`], { cwd: repoRoot, timeoutMs: 20000 });
      have = (await git(['rev-parse', '--verify', '--quiet', CURRENT.ref], { cwd: repoRoot })).code === 0;
    }
    if (!have) {
      const emptyTree = (await gitOk(['hash-object', '-t', 'tree', '/dev/null'], { cwd: repoRoot })).trim();
      const c = await git(['commit-tree', emptyTree, '-m', 'tracker store: the first state'], { cwd: repoRoot });
      if (c.code !== 0) die(1, `could not create the store ref ${CURRENT.ref}: ${text(c)}`);
      await gitOk(['update-ref', CURRENT.ref, c.stdout.trim()], { cwd: repoRoot });
      say(`tracker-sync: created ${CURRENT.ref} (no branch contains it)`);
    }
    await git(['worktree', 'prune'], { cwd: repoRoot });
    const add = await git(['worktree', 'add', '--detach', store, CURRENT.ref], { cwd: repoRoot });
    if (add.code !== 0) die(1, `could not create the store worktree at ${store}: ${text(add)}`);
    say(`tracker-sync: the store is ${store}`);
  } else if ((await git(['rev-parse', '--is-inside-work-tree'], { cwd: store })).code !== 0) {
    die(1, `${store} exists and is not the store worktree: move it aside or point --state elsewhere`);
  }
  CURRENT.store = canonicalPath(store);
  mkdirSync(join(store, '.tracker-sync', 'recordings'), { recursive: true });
  return { store, fresh };
}

const stateFiles = () => CURRENT.recipe.stateFiles;

/** The state files a store commit can hold: the recipe's declared files that the
 * store's own git does not ignore.
 *
 * A recording written from the store's working tree can carry a file the tracker
 * keeps locally - a project's own `.gitignore` can ignore the database's mirror -
 * so no commit of any revision holds it, and requiring it made every recording a
 * store written by 0.121.0 holds unmatchable (#49).
 *
 * Asked of git, never of a file name: a project's ignore rules are the only
 * authority on what its commits can hold, they differ per project, and a rule that
 * named a tracker's files here would be wrong in the next project.
 *
 * A file a commit cannot hold can neither be part of a recorded state nor tell two
 * states apart, so it is left out of both sides of the comparison. */
let comparableFiles = null;
async function comparableStateFiles() {
  if (comparableFiles) return comparableFiles;
  const all = stateFiles();
  // No --no-index: git does not ignore a file it already tracks, and such a file IS
  // in every commit. Asking with --no-index would drop it from what is compared while
  // a commit still holds it, and then a recording written from a digest over the rest
  // would not read back (#49 review).
  const r = await git(['check-ignore', '--', ...all], { cwd: CURRENT.store });
  // 0 says at least one of them is ignored, 1 that none is. Anything else is git
  // unable to answer, and then every declared file is compared whole - the rule
  // before this one - said out loud: a kept recording must never look like its own
  // verdict, and a silent narrowing is how #49 happened the first time.
  if (r.code !== 0 && r.code !== 1) {
    say(`tracker-sync: the store's git could not say which state files it ignores (${text(r).split('\n')[0] || `exit ${r.code}`}); every declared state file is compared whole, so a recording that carries a file no commit holds is kept`);
    comparableFiles = all.slice();
    return comparableFiles;
  }
  const ignored = new Set(r.stdout.split('\n').map((l) => l.trim()).filter(Boolean));
  comparableFiles = all.filter((p) => !ignored.has(p));
  return comparableFiles;
}

/** Write one state file into the store from a revision's tree, or from nowhere. */
async function stateFrom(rev) {
  const out = [];
  for (const p of stateFiles()) {
    const r = await git(['show', `${rev}:${p}`], { cwd: CURRENT.store });
    out.push([p, r.code === 0 ? r.stdout : null]);
  }
  return out;
}
const sideText = (parts) => parts.filter(([, c]) => c !== null).map(([p, c]) => `=== ${p}\n${c}`).join('\n');
function parseSide(txt) {
  const out = [];
  const re = /^=== (.*)\n/gm;
  let m; const marks = [];
  while ((m = re.exec(txt))) marks.push([m[1], m.index, m.index + m[0].length]);
  for (let i = 0; i < marks.length; i++) {
    const [p, , start] = marks[i];
    const end = i + 1 < marks.length ? marks[i + 1][1] : txt.length;
    out.push([p, txt.slice(start, end)]);
  }
  return out;
}
/** Write exactly these state files: a state that omits a file must not inherit
 * the one standing in the worktree. Used where a commit's tree IS a recorded
 * state, which a recording's digest then has to match byte for byte. */
async function writeStateExact(parts) {
  await writeState(parts);
  for (const [p, c] of parts) if (c === null) rmSync(join(CURRENT.store, p), { force: true });
}

async function writeState(parts) {
  for (const [p, c] of parts) {
    if (c === null) continue;
    const f = join(CURRENT.store, p);
    mkdirSync(dirname(f), { recursive: true });
    writeFileSync(f, c);
  }
}

/** Stage the state files and the mechanism's own files, and commit a tree. */
async function commitState(message, parents) {
  const existing = stateFiles().filter((p) => existsSync(join(CURRENT.store, p)));
  if (existing.length) await git(['add', '--', ...existing], { cwd: CURRENT.store });
  const tracked = (await git(['ls-files', '--', ...stateFiles()], { cwd: CURRENT.store })).stdout.split('\n').filter(Boolean);
  const gone = tracked.filter((p) => !existsSync(join(CURRENT.store, p)));
  if (gone.length) await git(['rm', '--cached', '-q', '--', ...gone], { cwd: CURRENT.store });
  // --sparse: a candidate leaves the recordings out of its worktree and keeps them
  // in its index, and the one recording a record run writes is still staged.
  await git(['-c', 'advice.updateSparsePath=false', 'add', '-A', '--sparse', '--', '.tracker-sync'], { cwd: CURRENT.store });
  const tree = (await gitOk(['write-tree'], { cwd: CURRENT.store })).trim();
  const args = ['commit-tree', tree];
  for (const p of parents) args.push('-p', p);
  args.push('-m', message);
  const c = await git(args, { cwd: CURRENT.store });
  if (c.code !== 0) die(1, `could not write the store commit: ${text(c)}`);
  const oid = c.stdout.trim();
  await gitOk(['reset', '--soft', oid], { cwd: CURRENT.store });
  return oid;
}

/** Fetch the ref into a private ref: never into the one the worktree holds. */
async function fetchRemote() {
  // A fetch that fails while two machines and their watchers are working is far
  // more often a moment's contention - or a slow disk under load - than an
  // unreachable remote, and calling it unreachable is what makes a session refuse
  // work it could do. Three tries over a few seconds, then it is said.
  for (let attempt = 0; attempt < 3; attempt++) {
    const r = await git(['fetch', 'origin', `+${CURRENT.ref}:${FETCH_REF}`], { cwd: CURRENT.store, timeoutMs: 20000 });
    if (r.code === 0) return { ok: true, tip: (await gitOk(['rev-parse', FETCH_REF], { cwd: CURRENT.store })).trim() };
    const t = text(r);
    if (/couldn't find remote ref|Could not find remote ref|no such ref/i.test(t)) return { ok: true, noRef: true };
    if (attempt < 2 && !isPolicy(r)) { await sleep(1000 * (attempt + 1)); continue; }
    return { ok: false, unreachable: true, said: t };
  }
  return { ok: false, unreachable: true, said: 'the fetch did not answer' };
}
const isUnreachable = (r) => r.timedOut || /could not resolve host|unable to access|Connection (refused|reset|timed out)|Network is unreachable|Could not read from remote|Operation timed out|no route to host|terminal prompts disabled|Permission denied \(publickey\)/i.test(text(r));
const isRefused = (r) => /rejected|non-fast-forward|fetch first|stale info/i.test(text(r));
// A refusal the remote's own policy makes (a protected ref, a denied user) is
// its own class and is never retried; a push a pre-receive hook merely declined
// this once is an ordinary refusal, and the fetch-merge-push round retries it.
// `cannot lock ref` is not a policy: it is a ref momentarily held by another
// process, and the fetch-merge-push round retries it. A policy refusal is one a
// person must change something for.
const isPolicy = (r) => /denied|protected ref|not allowed|refusing to allow|forbidden/i.test(text(r));

/** Which machine this store belongs to, and keeping that true.
 *
 * The machine ordinals travel on the ref so that record ids never collide, but
 * which machine THIS one is must not travel: taking the remote's file wholesale
 * (a fast-forward, a merge, a rollback) would hand this machine the other's
 * ordinal, and the two would mint the same record ids. The name is in the
 * machine's own config, written by connect. */
function ourMachine() {
  return readConfig(CURRENT.flags, CURRENT.repoRoot).machine || null;
}
function keepIdentity() {
  const me = ourMachine();
  if (!me || !CURRENT.store) return;
  const f = join(CURRENT.store, '.tracker-sync', 'machines.json');
  if (!existsSync(f)) return;
  try {
    requireRegularFile(f, 'the machine ordinals');
    const d = JSON.parse(readFileSync(f, 'utf8'));
    if (d.thisMachine !== me) writeFileSync(f, `${JSON.stringify({ ...d, thisMachine: me }, null, 2)}\n`);
  } catch {}
}

/** The state files as they stand in the store's working tree. A state file that
 * is not a regular file is named instead of opened: a named pipe with no writer
 * would block the read for ever. */
async function storeParts() {
  return stateFiles().map((p) => {
    const f = join(CURRENT.store, p);
    requireRegularFile(f, 'a state file');
    return [p, existsSync(f) ? readFileSync(f, 'utf8') : null];
  });
}

/** One per-item merge through the recipe, whoever the three sides are: the
 * sync's merge of the remote's tip, and the merge that saves a refused flush.
 * The recipe compares the sides; nothing here text-merges a state file. */
async function mergeParts({ ours, theirs, base, order = [], orderLocal = [] }) {
  const work = join(machineDir(CURRENT.flags, CURRENT.repoRoot), 'merge');
  rmSync(work, { recursive: true, force: true });
  mkdirSync(work, { recursive: true });
  const paths = {
    ours: join(work, 'ours.txt'), theirs: join(work, 'theirs.txt'), base: join(work, 'base.txt'),
    out: join(work, 'merged.txt'), order: join(work, 'order.txt'), orderLocal: join(work, 'order-local.txt'),
  };
  writeFileSync(paths.ours, sideText(ours));
  writeFileSync(paths.theirs, sideText(theirs));
  writeFileSync(paths.base, sideText(base));
  writeFileSync(paths.order, `${order.join('\n')}\n`);
  writeFileSync(paths.orderLocal, `${orderLocal.join('\n')}\n`);
  await recipeOk('merge', { ours: quote(paths.ours), theirs: quote(paths.theirs), base: quote(paths.base), out: quote(paths.out), order: quote(paths.order), orderLocal: quote(paths.orderLocal) });
  return parseSide(readFileSync(paths.out, 'utf8'));
}

/** Bring the tracker's database to the state files.
 *
 * `before` is the state the database last held, when the caller knows it: the
 * state files right after a flush. A recipe that declares `importChanged` is then
 * handed that state and imports only what differs from it, and says so; one that
 * does not, or a caller that does not know, imports the whole store. A tracker's
 * whole-store import grows with the store, not with the change: on thousands of
 * items it measured tens of seconds and hundreds of megabytes per run, once per
 * merge (#52). */
async function importState(before) {
  if (!before || !CURRENT.recipe.importChanged) return recipeOk('import', {});
  const f = join(machineDir(CURRENT.flags, CURRENT.repoRoot), 'import-before.txt');
  // Written part by part: a state of thousands of items is not copied into one
  // more string to be written.
  const fd = openSync(f, 'w');
  try {
    let first = true;
    for (const [p, c] of before) {
      if (c === null) continue;
      writeSync(fd, `${first ? '' : '\n'}=== ${p}\n`); writeSync(fd, c);
      first = false;
    }
  } finally { closeSync(fd); }
  try { return await recipeOk('importChanged', { before: quote(f) }); } finally { rmSync(f, { force: true }); }
}

/** The state files as the database holds them after a flush, or null where the
 * flush does not prove that (see flushDatabase): only then may an import take in
 * just the difference. */
const heldByDatabase = (flushed, parts) => (flushed && flushed.ok && !flushed.nothing && !flushed.merged ? parts : null);

/** The blob of each state file a commit can hold, as it stands in the store's
 * working tree; a file that is not there is absent from the map. */
async function worktreeStateBlobs() {
  const out = new Map();
  for (const p of await comparableStateFiles()) {
    const f = join(CURRENT.store, p);
    requireRegularFile(f, 'a state file');
    if (!existsSync(f)) continue;
    out.set(p, (await gitOk(['hash-object', '--', p], { cwd: CURRENT.store })).trim());
  }
  return out;
}
const sameBlobs = (a, b) => a.size === b.size && [...a].every(([p, oid]) => b.get(p) === oid);

/** Merge the remote's tip into ours, per item, through the recipe, per F3.
 * Caller holds the lock: this is local work only.
 *
 * The tracker's database is flushed first, so `ours` is the state it holds and a
 * write that reached it after the last commit is merged, not overwritten. Then the
 * work follows the change, not the store (#52): where the remote moved no state
 * file since the merge base, ours IS the merge and nothing is merged or imported;
 * where we moved none, theirs is, and only what it changed is imported; only where
 * both sides moved does the recipe merge per item. */
async function mergeAndCommit(tip, { flushed: done } = {}) {
  // A caller that flushed a moment ago under the same lock hands its result on:
  // a refused flush is merged from the database once, not twice.
  const flushed = done || await flushDatabase();
  const head = await headOid();
  const base = (await git(['merge-base', head, tip], { cwd: CURRENT.store })).stdout.trim();
  const oursParts = await storeParts();
  const baseBlobs = base ? await stateBlobsAt(base) : null;
  // The state the database holds, when it is known to be ours: a flush the
  // tracker refused leaves state files that are not the database's (a merge of
  // its side export, or a filled file over an empty database), and then only a
  // whole import brings the database to the merged state.
  const held = heldByDatabase(flushed, oursParts);
  // Whether the database must take in the merged state, and from what.
  let mustImport = !held;
  if (baseBlobs && sameBlobs(await stateBlobsAt(tip), baseBlobs)) {
    // Nothing of the tracker's moved on the remote: our state stands as it is.
  } else if (baseBlobs && sameBlobs(await worktreeStateBlobs(), baseBlobs)) {
    // Nothing of ours moved: the remote's state is the merge, exactly as a
    // fast-forward takes it. A file the store's git ignores is never in a commit,
    // so it is the machine's own and is left where it is.
    const comparable = await comparableStateFiles();
    const theirs = (await stateFrom(tip)).filter(([p]) => comparable.includes(p));
    await writeStateExact(theirs);
    mustImport = true;
  } else {
    const theirsParts = await stateFrom(tip);
    const baseParts = base ? await stateFrom(base) : stateFiles().map((p) => [p, null]);
    const order = (await git(['log', '--first-parent', '--format=%H', base ? `${base}..${tip}` : tip], { cwd: CURRENT.store })).stdout.split('\n').filter(Boolean).reverse();
    const orderLocal = (await git(['log', '--first-parent', '--format=%H', head], { cwd: CURRENT.store })).stdout.split('\n').filter(Boolean);
    const merged = await mergeParts({ ours: oursParts, theirs: theirsParts, base: baseParts, order, orderLocal });
    await writeState(merged);
    mustImport = true;
  }
  // recordings travel beside the tracker items and are immutable: two different
  // states for one revision are a refusal, never a newer-wins choice.
  const listIn = async (rev) => (await git(['ls-tree', '-r', '--name-only', rev, '--', '.tracker-sync/recordings'], { cwd: CURRENT.store })).stdout.split('\n').filter(Boolean);
  const [a, b] = [await listIn(head), await listIn(tip)];
  for (const p of new Set([...a, ...b])) {
    const ca = a.includes(p) ? (await git(['show', `${head}:${p}`], { cwd: CURRENT.store })).stdout : null;
    const cb = b.includes(p) ? (await git(['show', `${tip}:${p}`], { cwd: CURRENT.store })).stdout : null;
    // A recording holds a reference, so the two sides are compared as the STATES
    // they stand for: two references to commits with the same state are one
    // recording, and only states that differ are a refusal for a person.
    const sa = ca === null ? null : (await recordingState(ca, `the recording of ${basename(p)} in ${head.slice(0, 12)}`)).text;
    const sb = cb === null ? null : (await recordingState(cb, `the recording of ${basename(p)} in ${tip.slice(0, 12)}`)).text;
    if (sa !== null && sb !== null && sa !== sb)
      die(1, `two different recordings for ${basename(p)}: ${head.slice(0, 12)} and ${tip.slice(0, 12)} recorded different states for one revision, so neither is moved; a person must decide`);
    if (ca === null) { mkdirSync(dirname(join(CURRENT.store, p)), { recursive: true }); writeFileSync(join(CURRENT.store, p), cb); }
  }
  await recipeIsland(head, tip);
  keepIdentity();
  if (mustImport) await importState(held);
  return commitState(`tracker store: merge ${head.slice(0, 12)} and ${tip.slice(0, 12)} per item`, [head, tip]);
}
/** The mechanism's own files under .tracker-sync travel beside the state. */
async function recipeIsland(head, tip) {
  const names = (await git(['ls-tree', '-r', '--name-only', tip, '--', '.tracker-sync'], { cwd: CURRENT.store }))
    .stdout.split('\n').filter((n) => n && !n.startsWith('.tracker-sync/recordings/'));
  for (const n of names) {
    const theirs = (await git(['show', `${tip}:${n}`], { cwd: CURRENT.store })).stdout;
    requireRegularFile(join(CURRENT.store, n), 'a file the mechanism carries beside the state');
    const mine = existsSync(join(CURRENT.store, n)) ? readFileSync(join(CURRENT.store, n), 'utf8') : null;
    if (mine === null) { mkdirSync(dirname(join(CURRENT.store, n)), { recursive: true }); writeFileSync(join(CURRENT.store, n), theirs); continue; }
    if (mine === theirs) continue;
    if (n.endsWith('/machines.json')) {
      // One machine ordinal per machine: the two sides are unioned, and a lost
      // race (two machines taking the same ordinal) is named so connect runs again.
      const a = JSON.parse(mine); const b = JSON.parse(theirs);
      const ordinals = { ...a.ordinals };
      for (const [machine, ordinal] of Object.entries(b.ordinals || {})) {
        const taken = Object.entries(ordinals).find(([m, o]) => o === ordinal && m !== machine);
        if (taken) die(1, `two machines claimed ordinal ${ordinal} (${taken[0]} and ${machine}): run connect again on this machine, which takes the next free one`);
        if (ordinals[machine] === undefined) ordinals[machine] = ordinal;
      }
      writeFileSync(join(CURRENT.store, n), `${JSON.stringify({ ...a, ...b, ordinals, thisMachine: a.thisMachine || b.thisMachine }, null, 2)}\n`);
      continue;
    }
    die(1, `${n} differs between ${head.slice(0, 12)} and ${tip.slice(0, 12)} and is neither a recording nor the machine ordinals: a person must decide`);
  }
}

/** Bring our store to the remote's state, or merge it; never text-merge files. */
/** Bring our store to the remote's state, or merge it; never text-merge files.
 *
 * The network is never inside the lock: the fetch, the push and a refused push's
 * second fetch happen outside it, and only the store's own mutations (export,
 * merge, import, commit) are inside. A write that reached the tracker's database
 * is exported first, so neither a fast-forward nor a merge can lose it. */
async function syncOnce({ needsRemote, push, quiet, skipOnTimeout = false } = {}) {
  const remote = await fetchRemote();
  if (!remote.ok) { if (needsRemote) die(4, `the remote is unreachable (${remote.said}); this store is this machine's only`); return { unreachable: true }; }
  if (remote.noRef) return { noRef: true };
  let fast=false;
  // The store's own work, in one critical section. A watcher round that cannot
  // take the lock skips this tick instead of exiting: the supervisor would only
  // restart it into the same wait.
  const section = async () => {
    // What a killed process left, and what an older version of this mechanism
    // left, before anything reads the store: the migration commits, and the head
    // read below then includes its commit, which the resume paths must not drop.
    const migrated = await migrateStore();
    // HEAD and ancestry belong to the same critical section as reset/merge.
    // A local publisher may have advanced HEAD while the fetch was in flight.
    const head=await headOid();
    // A candidate an earlier run could not confirm may be proven accepted now.
    await pruneSettledCandidates(remote.tip);
    if (head===remote.tip) return head;
    fast=(await git(['merge-base','--is-ancestor',head,remote.tip],{cwd:CURRENT.store})).code===0;
    // The live database becomes state files first, so a write that only reached
    // it is not lost to a fast-forward or a merge. A tracker that refuses the
    // flush is understood through the recipe: a refusal that proves nothing is
    // unexported is said and the state files stand, and every other refusal is
    // merged in per item, or stops here - never read as "nothing unexported".
    const flushed = await flushDatabase();
    if (fast) {
      // Our own unpublished writes are a commit of their own; the push that
      // follows merges the remote in rather than dropping them.
      // Our own unpublished writes are merged with the remote's state per item
      // and committed, never merely committed and left as they are: a state that
      // is stale against the remote is exactly what makes a claim land twice,
      // because the recipe is asked about the state in front of it.
      if (migrated.migrated || await hasUncommittedState()) return mergeAndCommit(remote.tip, { flushed });
      // Nothing of ours is unpublished: the remote's state replaces this store's,
      // and the database, which holds exactly the state being replaced, takes in
      // only what the remote changed.
      const held = heldByDatabase(flushed, await storeParts());
      await gitOk(['reset', '--hard', remote.tip], { cwd: CURRENT.store });
      keepIdentity();
      await importState(held);
      return remote.tip;
    }
    return mergeAndCommit(remote.tip, { flushed });
  };
  let oid;
  try {
    oid = await locked(section, 120000, { skipOnTimeout });
  } catch (e) {
    // The watcher's round is not its to take: it is skipped and tried again on
    // the next tick. A one-shot command's locked() refused with exit 1 already.
    if (e.lockTimeout) return { skipped: e.message };
    throw e;
  }
  if (!quiet) say(fast ? `tracker-sync: took the remote's state ${remote.tip.slice(0, 12)}` : `tracker-sync: merged the remote's ${remote.tip.slice(0, 12)} into ${oid.slice(0, 12)} per item`);
  const pushed = push === false ? null : await pushOid(oid, { quiet, skipOnTimeout });
  return { oid, pushed };
}
/** Push an immutable oid; a refused push fetches, merges and pushes again. */
/** Push an immutable oid; a refused push fetches, merges and pushes again.
 * The push itself is a network call and runs outside the lock; the merge is local
 * work and takes it. */
async function pushOid(oid, { quiet, attempts = 3, skipOnTimeout = false } = {}) {
  // The last refusal, kept for the line that gives up: "three pushes were refused"
  // is a verdict, and what the remote answered is the cause (#49's addendum).
  let lastSaid = '';
  for (let i = 0; i < attempts; i++) {
    const r = await git(['push', 'origin', `${oid}:${CURRENT.ref}`], { cwd: CURRENT.store, timeoutMs: 10000 });
    if (r.code === 0) return { ok: true, oid };
    if (isUnreachable(r)) { if (!quiet) say(`tracker-sync: the remote is unreachable; the write is kept here and published when it answers (${text(r).split('\n')[0]})`); return { ok: false, unreachable: true }; }
    // A hosting policy that refuses this ref is its own class (exit 5): named,
    // never retried as if the remote had merely moved.
    if (isPolicy(r)) die(5, `the remote refused ${CURRENT.ref}: ${text(r)}`);
    lastSaid = text(r).split('\n')[0] || `exit ${r.code}`;
    if (isRefused(r)) {
      const remote = await fetchRemote();
      if (!remote.ok) { if (!quiet) say('tracker-sync: the push was refused and the remote is unreachable; the write is kept here'); return { ok: false, unreachable: true }; }
      if (remote.noRef) { if (!quiet) say('tracker-sync: the push was refused and the remote has no store ref yet'); return { ok: false, refused: true }; }
      // A caller whose own call already holds the store's lock (connect, or a
      // section that reached here) merges directly: taking it twice in one call
      // would wait for itself. The question is asked of THIS call's context, not
      // of the process: another loop of a watcher may hold the lock, and a merge
      // outside it would write the store beside that loop's work.
      try {
        oid = LOCK_CONTEXT.getStore() ? await mergeAndCommit(remote.tip) : await locked(() => mergeAndCommit(remote.tip), 120000, { skipOnTimeout });
      } catch (e) {
        if (e.lockTimeout) return { ok: false, skipped: e.message };
        throw e;
      }
      if (!quiet) say(`tracker-sync: the push was refused; merged the remote's ${remote.tip.slice(0, 12)} and pushing again`);
      continue;
    }
    die(1, `the push failed: ${text(r)}`);
  }
  say(`tracker-sync: the push did not land after ${attempts} attempts (last: ${lastSaid || 'no answer'}); the write is kept here and the next tick tries again`);
  return { ok: false, refused: true };
}
// ---- publish, sync, claim, transition, record -------------------------------

const stateDigest = () => {
  const h = createHash('sha256');
  for (const p of stateFiles()) {
    const f = join(CURRENT.store, p);
    h.update(p);
    requireRegularFile(f, 'a state file');
    h.update(existsSync(f) ? readFileSync(f) : Buffer.from(''));
  }
  return h.digest('hex');
};
async function hasUncommittedState() {
  const r = await git(['status', '--porcelain', '--', ...stateFiles(), '.tracker-sync'], { cwd: CURRENT.store });
  return r.stdout.trim().length > 0;
}

/** The store's file lock, waited for. Past the deadline this throws a tagged
 * error rather than exiting: a one-shot command's locked() refuses with exit 1,
 * and a loop of the watcher skips the round. */
async function takeLockWait(ms) {
  const deadline = Date.now() + (ms || 0);
  let said = false;
  for (;;) {
    try { return takeLock(CURRENT.flags, CURRENT.repoRoot); } catch (e) {
      if (Date.now() > deadline)
        throw Object.assign(new Error(`${e.message}; nothing was written, and the command that held it is named above`), { lockTimeout: true });
      if (!said) {
        said = true;
        say(`tracker-sync: waiting for the store lock (${e.message.split(';')[0]})`);
      }
      await sleep(200);
    }
  }
}

/** One process's own critical sections queue HERE, never on the file lock.
 *
 * The file lock is not re-entrant in-process: while one section of this process
 * holds it, another section of the same process that calls takeLockWait waits
 * for a lock its own process holds, reaches the deadline and dies. The watcher
 * runs two loops - publish and sync - and that is exactly what made it restart
 * for ever. A nested call (pushOid's merge inside a section that already holds
 * the lock) runs in the section that holds it, as it did before.
 */
let LOCK_QUEUE = Promise.resolve();
// Whether THIS call is already inside a critical section. The two loops of one
// process are both "this process" but never nested, so the answer has to travel
// with the call that asked, not with the process: an async context says exactly
// that, and the watcher's second loop queues instead of running unlocked.
const LOCK_CONTEXT = new AsyncLocalStorage();
async function inProcessLock(fn) {
  if (LOCK_CONTEXT.getStore()) return fn();
  const previous = LOCK_QUEUE;
  let handOn;
  LOCK_QUEUE = new Promise((r) => { handOn = r; });
  await previous;
  try { return await LOCK_CONTEXT.run(true, fn); } finally { handOn(); }
}

/** Local store work only: never a network call inside it.
 *
 * A one-shot command (record, a sync run by hand) waits for the lock and then
 * refuses with exit 1 naming the holder: a person must know why nothing was
 * written. A loop inside the watcher passes skipOnTimeout: the round is not its
 * to take, so it is skipped, said once in the log, and tried again on the next
 * tick - never an exit, which the supervisor would answer with a restart. */
async function locked(fn, waitMs = 120000, { skipOnTimeout = false } = {}) {
  return inProcessLock(async () => {
    let release;
    try {
      release = await takeLockWait(waitMs);
    } catch (e) {
      if (skipOnTimeout && e.lockTimeout) throw e;
      die(1, e.message);
    }
    try { return await fn(); } finally { release(); }
  });
}
/** One publish. `publish` with `--watch` starts the watcher with this first round
 * and then keeps looping, so that round skips a lock it cannot take instead of
 * ending the command before the watcher it was asked to start. */
async function publishOnce({ quiet, skipOnTimeout = false } = {}) {
  let oid;
  try {
    oid = await locked(async () => {
      const migrated = await migrateStore({ announce: true });
      await flushDatabase();
      if (!migrated.migrated && !(await hasUncommittedState())) return migrated.oid;
      return commitState(`tracker store: ${nowIso()}`, [await headOid()]);
    }, 120000, { skipOnTimeout });
  } catch (e) {
    if (e.lockTimeout) return { ok: false, skipped: e.message };
    throw e;
  }
  if (oid === null) return { ok: true, nothing: true };
  const res = await pushOid(oid, { quiet, skipOnTimeout });
  writeConfig(CURRENT.flags, CURRENT.repoRoot, { lastPublish: nowIso(), lastPublishOk: !!res.ok });
  if (res.ok && !quiet) say(`tracker-sync: published ${oid.slice(0, 12)} to ${CURRENT.ref}`);
  return res;
}
async function pushOnce(oid) {
  const r = await git(['push', 'origin', `${oid}:${CURRENT.ref}`], { cwd: CURRENT.store, timeoutMs: 10000 });
  if (r.code === 0) return { ok: true };
  if (isUnreachable(r)) return { unreachable: true, said: text(r) };
  if (isPolicy(r)) die(5, `the remote refused ${CURRENT.ref}: ${text(r)}`);
  if (isRefused(r)) return { refused: true, said: text(r) };
  die(1, `the push failed: ${text(r)}`);
}

/** A conditional write: it counts only once it is on the remote (claim, transition).
 *
 * The recipe is what knows the tracker's own state, so the condition lives
 * there: its claim and transition commands refuse by exiting 3 when the item is
 * already held, or the transition has already moved, and write nothing. This
 * wrapper makes the remote the only place the write counts: fetch and merge
 * first, write, push an immutable oid, and, when the remote refuses the push,
 * discard everything written, rebuild on top of the remote's new state and ask
 * the recipe again before writing a second time. */
/** A settled outcome needs no person: the diagnostic of a discarded candidate
 * goes with it. An error, or an outcome nobody could confirm yet, keeps it,
 * because someone must reconcile that. */
function settleDiagnostic(file) {
  if (!file) return;
  try {
    rmSync(file, { force: true });
  } catch {}
}

/** A candidate an earlier run retained may be proven accepted later: once the
 * remote's tip contains it, the write did reach the remote, so its ref and its
 * diagnostic are settled and go. A candidate the remote does not hold is left
 * for a person. */
async function pruneSettledCandidates(remoteTip) {
  if (!remoteTip) return;
  const listed = await git(['for-each-ref', '--format=%(refname) %(objectname)', 'refs/tracker-sync/candidates/'], { cwd: CURRENT.store });
  if (listed.code !== 0) return;
  for (const line of listed.stdout.split('\n')) {
    const [ref, oid] = line.trim().split(/\s+/);
    if (!ref || !oid) continue;
    if ((await git(['merge-base', '--is-ancestor', oid, remoteTip], { cwd: CURRENT.store })).code !== 0) continue;
    await git(['update-ref', '-d', ref, oid], { cwd: CURRENT.store });
    settleDiagnostic(join(machineDir(CURRENT.flags, CURRENT.repoRoot), `${basename(ref)}.result.json`));
  }
}


// ---- what the mechanism leaves beside the store, and cleaning it ------------
//
// The mechanism writes beside the store, in the machine's own directory: a
// candidate per conditional write, a diagnostic beside each candidate, a merge
// scratch folder, the lock, the watcher's pid file. Until this sweep existed each
// of them was removed only in a `finally`, and a process killed before its last
// step never runs one: a hook that timed out, a session that ended, a machine
// that reclaimed memory. Four dead candidates held 35 GB and the disk reached
// 98%, and the write that then failed was a commit's tracker state (issue #45).
// A `finally` is not a cleanup a person can rely on; what a kill leaves behind
// must be found and removed by the next process, and that is this sweep.

/** The phases a candidate ends in without needing a person: its outcome was
 * reported to whoever asked, so a diagnostic still lying beside it was left by a
 * process that was killed before it could take its own rubbish out. */
const SETTLED_PHASES = new Set(['accepted', 'accepted-reconciled', 'already-recorded', 'lost', 'refused']);
/** The phases that need a person: the write may be on the remote and nobody
 * proved it, or a command the recipe runs failed. The diagnostic names the ref
 * that holds the candidate commit, and a reconcile reads both, so neither is
 * swept: that is the evidence, not rubbish. */
const RETAINED_PHASES = new Set(['unknown', 'error', 'recipe-failed']);

const humanBytes = (n) => {
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let v = Number(n) || 0; let i = 0;
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++; }
  return `${i === 0 ? v : v.toFixed(1)} ${units[i]}`;
};

/** How much a folder holds, without following a link out of it and without
 * failing on a file that vanished under the walk: a number for a person to read,
 * never a proof. */
function dirSize(dir) {
  let bytes = 0; let files = 0;
  const walk = (d) => {
    let entries;
    try { entries = readdirSync(d, { withFileTypes: true }); } catch { return; }
    for (const e of entries) {
      const p = join(d, e.name);
      try {
        const st = lstatSync(p);
        if (st.isSymbolicLink()) continue;
        if (st.isDirectory()) walk(p);
        else if (st.isFile()) { bytes += st.size; files++; }
      } catch {}
    }
  };
  walk(dir);
  return { bytes, files };
}

/** When the process that is running now started, in milliseconds since the epoch,
 * or null where the machine does not say. A pid is reused, so "alive" is not the
 * same question as "the process that wrote this diagnostic is still running":
 * /proc/<pid>/stat carries the start time in clock ticks since boot and
 * /proc/stat carries the boot time, and the two place it on the same clock as
 * the diagnostic's own timestamp. USER_HZ is 100 on every Linux the mechanism
 * runs on. Where /proc is absent (macOS, Windows) this returns null and a reused
 * pid keeps a dead candidate for a person to remove: the safe direction, because
 * a live candidate is never swept. */
function procStartMs(pid) {
  try {
    const stat = readFileSync(`/proc/${pid}/stat`, 'utf8');
    const rest = stat.slice(stat.lastIndexOf(')') + 2).split(' ');
    const ticks = Number(rest[19]);
    const boot = /^btime (\d+)$/m.exec(readFileSync('/proc/stat', 'utf8'));
    if (!Number.isFinite(ticks) || !boot) return null;
    return Number(boot[1]) * 1000 + ticks * 10;
  } catch { return null; }
}

/** Is the process named by a diagnostic still the one that wrote it? */
function pidAlive(pid, createdAt) {
  if (!Number.isInteger(pid) || pid <= 0) return false;
  try { process.kill(pid, 0); } catch { return false; }
  const started = procStartMs(pid);
  const made = Date.parse(createdAt || '');
  // Five seconds of slack: the diagnostic is written by the process itself, a
  // forked child is not, and a clock that steps back a little is still one clock.
  if (started !== null && Number.isFinite(made) && started > made + 5000) return false;
  return true;
}

/** Every candidate this machine's directory holds, with what its diagnostic says
 * and whether its process is still there. `self` is the caller's own candidate
 * folder, which is never a stranger to be removed. */
function listCandidates(self) {
  const dir = machineDir(CURRENT.flags, CURRENT.repoRoot);
  const out = { dir, created: [], results: [], orphans: [] };
  let entries = [];
  try { entries = readdirSync(dir); } catch { return out; }
  const results = new Map();
  for (const name of entries) {
    if (!name.endsWith('.result.json')) continue;
    const candidate = name.slice(0, -'.result.json'.length);
    let diagnostic = null;
    try { diagnostic = JSON.parse(readFileSync(join(dir, name), 'utf8')); } catch {}
    results.set(candidate, { name, path: join(dir, name), candidate, diagnostic });
    out.results.push(results.get(candidate));
  }
  for (const name of entries) {
    if (!name.startsWith('candidate-') || name.endsWith('.result.json')) continue;
    const path = join(dir, name);
    try { if (!lstatSync(path).isDirectory()) continue; } catch { continue; }
    const entry = { name, path, result: results.get(name) || null, self: name === self };
    out.created.push(entry);
  }
  // A candidate folder named by no diagnostic: the process was killed before it
  // wrote one, or after another sweep removed the diagnostic. Only a live process
  // writes its diagnostic before it makes its folder, so a folder nobody names is
  // a stranger unless it is the caller's own.
  out.orphans = out.created.filter((c) => !c.result && !c.self);
  out.stale = out.created.filter((c) => !c.self && (c.result ? !pidAlive(c.result.diagnostic?.pid, c.result.diagnostic?.createdAt) : true));
  return out;
}

/** Remove what a killed process left beside the store. Runs under the store lock,
 * so no live command is using the merge scratch folder it also removes, and a
 * candidate whose process is alive is never touched: a conditional write in
 * another worktree keeps its candidate while its push is on the wire.
 *
 * What is NOT swept: a retained ref (`refs/tracker-sync/candidates/*`, the
 * "unknown" outcome) and the diagnostic that names it. The ref is the proof a
 * reconcile needs, and it is dropped only when the remote's tip is shown to
 * contain the commit (pruneSettledCandidates). */
async function sweepMachineDir({ self = null } = {}) {
  const found = listCandidates(self);
  const swept = { candidates: 0, worktrees: 0, results: 0, bytes: 0, kept: [] };
  let pruned = false;
  for (const c of found.stale) {
    const size = dirSize(c.path).bytes;
    // The registration goes first, from the live store that made it: a folder
    // removed under a live registration is a registration git reports for ever.
    const removed = await git(['worktree', 'remove', '--force', c.path], { cwd: CURRENT.store });
    if (removed.code === 0) swept.worktrees++;
    rmSync(c.path, { recursive: true, force: true });
    pruned = true;
    swept.candidates++; swept.bytes += size;
    // A retained outcome keeps its diagnostic even though its copy goes: the
    // evidence a reconcile reads is the ref and the file that names it, and a
    // commit is already on the ref, so the folder is dead weight. Removing it with
    // the copy would leave the ref with nothing naming it.
    if (c.result && RETAINED_PHASES.has(c.result.diagnostic?.phase)) { swept.kept.push({ name: c.result.name, phase: c.result.diagnostic.phase }); continue; }
    if (c.result) { rmSync(c.result.path, { force: true }); swept.results++; }
  }
  // Diagnostics of dead candidates whose folder is already gone, and the ones a
  // process was killed too late to remove. A settled outcome needs no person, so
  // it goes; an unknown or failed one stays, because a person must reconcile it.
  for (const r of found.results) {
    const phase = r.diagnostic?.phase;
    // A process that is alive owns its diagnostic for as long as it runs, whatever
    // its phase says: it removes it itself when the outcome is reported, and a
    // sweep that took it first would leave its own candidate looking like an orphan
    // to the next sweep, which would then remove a live candidate's folder.
    if (pidAlive(r.diagnostic?.pid, r.diagnostic?.createdAt)) continue;
    if (SETTLED_PHASES.has(phase)) { rmSync(r.path, { force: true }); swept.results++; continue; }
    if (RETAINED_PHASES.has(phase)) { swept.kept.push({ name: r.name, phase }); continue; }
    // Still preparing or pushing: only a dead process can be here within a lock,
    // and that candidate was removed with its folder above.
    if (!pidAlive(r.diagnostic?.pid, r.diagnostic?.createdAt)) { rmSync(r.path, { force: true }); swept.results++; }
  }
  // The same class, not only candidates: the merge scratch folder and the index a
  // referenced state is written from. Under the lock nothing living uses them.
  rmSync(join(found.dir, 'merge'), { recursive: true, force: true });
  // The side export a refused flush writes, before it merges it: a kill between the
  // export and the merge leaves a whole copy of the tracker's state there.
  rmSync(join(found.dir, 'database-export'), { recursive: true, force: true });
  try {
    for (const name of readdirSync(found.dir).filter((n) => n.startsWith('recording-index-'))) rmSync(join(found.dir, name), { force: true });
  } catch {}
  if (pruned) await git(['worktree', 'prune'], { cwd: CURRENT.store });
  return swept;
}

// ---- recordings: a reference to a store commit, not a copy of its state ------
//
// A recording binds a code revision to the state the store had then. It used to
// hold that state whole: a full copy per revision, written on every commit in
// every worktree, about 6 MB each and 5 GB in one store against 10 MB of items
// (issue #45). The state is already in the store's history, so a recording holds
// a reference: the commit whose tree is exactly that state, and the digest of the
// bytes it produces. `export --at` reads the state from that commit and answers
// with the same bytes a snapshot would have.
//
// THE STORE REF'S HISTORY IS APPEND-ONLY, and this is the invariant a reference
// depends on: nothing here force-pushes the ref, resets it to a commit it did not
// build on, or deletes a commit it published. A commit a recording names is an
// ancestor of the recording's own commit, so publishing the recording publishes
// what it names. A referenced commit that is missing is a loud refusal, never a
// guess from a later state.
const RECORDING_FORMAT = 'tracker-sync-recording/1';
const sha256 = (text) => createHash('sha256').update(text, 'utf8').digest('hex');

/** The reference a recording file holds, or null when the file is a snapshot
 * written before references existed (a store not yet migrated). */
function parseRecording(text) {
  const trimmed = text.trim();
  if (!trimmed.startsWith('{')) return null;
  let d;
  try { d = JSON.parse(trimmed); } catch { return null; }
  if (!d || d.format !== RECORDING_FORMAT || typeof d.store !== 'string' || !/^[0-9a-f]{40,64}$/.test(d.store)) return null;
  return { store: d.store, digest: typeof d.digest === 'string' ? d.digest : null };
}
const recordingText = (oid, state) => `${JSON.stringify({ format: RECORDING_FORMAT, store: oid, digest: `sha256:${sha256(state)}` }, null, 2)}\n`;

/** The state files as a commit holds them, in the shape a snapshot writes: the
 * one shape both kinds of recording produce, so `export --at` is one read. */
async function stateTextAt(oid, where = 'the recording') {
  if ((await git(['cat-file', '-e', `${oid}^{commit}`], { cwd: CURRENT.store })).code !== 0)
    die(1, `${where} names store commit ${String(oid).slice(0, 12)}, which this store does not hold. The store ref's history is append-only and is never rewritten, so a referenced commit is either here or the ref was rewritten outside this mechanism: the state of that revision is not guessed from a later one`);
  return sideText(await stateFrom(oid));
}

/** What a recording file stands for, as state text: a reference is read through
 * its commit and checked against the digest it wrote, a snapshot is its own
 * bytes. */
async function recordingState(text, where) {
  const ref = parseRecording(text);
  if (!ref) {
    // A whole copy written before references existed is read on the files a commit
    // can hold, exactly as a reference is. Without this, a copy and the reference
    // its own store migrated it to read as two different recordings of one revision:
    // one machine still on the old form and one that has migrated would refuse the
    // revision as "recorded twice with different states".
    const parts = snapshotParts(text);
    if (!parts) return { text };
    const files = await comparableStateFiles();
    const comparable = parts.filter(([p]) => files.includes(p));
    return { text: comparable.length < parts.length ? sideText(comparable) : text };
  }
  const state = await stateTextAt(ref.store, where);
  if (ref.digest && ref.digest !== `sha256:${sha256(state)}`)
    die(1, `${where} binds store commit ${ref.store.slice(0, 12)} to a state that commit no longer holds: the store ref's history was rewritten outside this mechanism, and the state of that revision is not guessed`);
  return { text: state, store: ref.store };
}

/** The blob each of the recipe's state files a commit can hold has at a commit.
 * Comparing blob ids answers "does this commit hold this state" without reading
 * the content twice, which is what makes a migration over a thousand recordings
 * cheap. A declared file the store's git ignores is not asked for: no commit of
 * any revision holds it, and listing it would answer nothing. */
async function stateBlobsAt(oid) {
  const r = await git(['ls-tree', '-r', oid, '--', ...(await comparableStateFiles())], { cwd: CURRENT.store });
  const out = new Map();
  if (r.code !== 0) return out;
  for (const line of r.stdout.split('\n')) {
    const m = /^\d+ blob ([0-9a-f]+)\t(.*)$/.exec(line);
    if (m) out.set(m[2], m[1]);
  }
  return out;
}

/** The blob id a text has in this store, without writing it: the state a
 * snapshot holds, compared with a commit's tree. */
async function blobOf(text) {
  const r = await run('git', ['-c', `core.hooksPath=${noHooksPath(CURRENT.flags, CURRENT.repoRoot)}`, 'hash-object', '--stdin'], { cwd: CURRENT.store, input: text });
  if (r.code !== 0) die(1, `could not hash a recorded state: ${text(r) || 'no output'}`);
  return r.stdout.trim();
}

/** Is this commit the state a snapshot holds? Every state file the snapshot
 * printed that a commit can hold must be in this commit, byte for byte: blob ids
 * are content, so equal ids are equal bytes.
 *
 * What may differ, and why it can never make two different tracker states one
 * state:
 *
 *   - a file the store's git ignores. No commit of any revision holds it, so it
 *     is in no state a recording can be compared with; the copy the recording
 *     printed is the working tree's, which the tracker rebuilds from the files
 *     commits do hold. It cannot distinguish two states, because no commit ever
 *     carried either version of it.
 *   - a declared file the commit holds and the recording did not print. The
 *     recording is silent about it, so nothing is claimed about it, and it cannot
 *     make two states one either: a state is told from another by a file some
 *     recording printed. What it can do is leave two candidate commits, and then
 *     the earliest - the commit that added the recording, which is the state the
 *     record run saw - is named, never a later one.
 *
 * A file the recording printed that the commit lacks is never a match: the
 * recording holds tracker data no commit holds, and a conversion that dropped it
 * would lose that data. */
function sameState(commitBlobs, snapshotBlobs) {
  if (!snapshotBlobs.size) return false;
  for (const [p, oid] of snapshotBlobs) if (commitBlobs.get(p) !== oid) return false;
  return true;
}

/** The parts of a snapshot, recovered exactly or not at all: the text is
 * re-generated from what was read back, and a text that does not come back byte
 * for byte is not something this mechanism wrote, so nothing is guessed from it. */
function snapshotParts(text) {
  const marks = [...text.matchAll(/^=== ([^\n]+)\n/gm)];
  if (!marks.length) return null;
  const parts = [];
  for (let i = 0; i < marks.length; i++) {
    const start = marks[i].index + marks[i][0].length;
    const end = i + 1 < marks.length ? marks[i + 1].index : text.length;
    // sideText joins two parts with one newline, which is the byte right before
    // the next header line; the body ends in front of it.
    parts.push([marks[i][1], i + 1 < marks.length ? text.slice(start, end - 1) : text.slice(start, end)]);
  }
  return sideText(parts) === text ? parts : null;
}

/** Every state the ref's first-parent history held, as a blob-set key to the
 * newest commit that held it: one git call for the whole chain, because a
 * migration that walked commits one by one would take minutes inside a hook. */
async function storeHistoryStates() {
  const history = { byKey: new Map(), byPair: null };
  const r = await git(['log', '--first-parent', '--raw', '--no-abbrev', '--format=%H', '--', ...(await comparableStateFiles())], { cwd: CURRENT.store });
  const commits = [];
  let cur = null;
  for (const line of r.stdout.split('\n')) {
    const t = line.trim();
    if (!t) continue;
    if (/^[0-9a-f]{40,64}$/.test(t)) { cur = { oid: t, entries: [] }; commits.push(cur); continue; }
    const m = /^:\d+ \d+ ([0-9a-f]+) [0-9a-f]+ \w+\t(.*)$/.exec(line);
    if (m && cur) cur.entries.push({ old: m[1], path: m[2] });
  }
  if (!commits.length) {
    // A history too large for the output bound is said, never read as "no commit
    // holds this state": nothing is converted from a guess.
    if (r.code !== 0) say(`tracker-sync: the store's state history could not be read in one call (${text(r).split('\n')[0] || 'no output'}); a recording no commit of the log holds is kept, not converted`);
    return history;
  }
  let state = await stateBlobsAt(commits[0].oid);
  const keyOf = (m) => [...m.entries()].sort().map(([p, o]) => `${p}:${o}`).join('|');
  for (const c of commits) {
    const key = keyOf(state);
    if (!history.byKey.has(key)) history.byKey.set(key, c.oid);
    for (const e of c.entries) {
      if (/^0+$/.test(e.old)) state.delete(e.path); else state.set(e.path, e.old);
    }
  }
  return history;
}

/** The state the ref's history held that this recording holds: the newest one
 * carrying every file the recording printed, in the same bytes. A commit may hold
 * a declared file the recording did not print (see sameState), so the newest exact
 * key is asked for first and the rest is answered from an index built on demand:
 * the pairs a recording prints point at the states that hold them, so a store of a
 * thousand recordings never walks the whole history per recording. */
function historyStateFor(history, blobs) {
  const pairs = [...blobs].map(([p, o]) => `${p}:${o}`).sort();
  if (!pairs.length) return null;
  const exact = history.byKey.get(pairs.join('|'));
  if (exact) return exact;
  if (!history.byPair) {
    history.byPair = new Map();
    for (const key of history.byKey.keys()) {
      for (const pair of key.split('|')) {
        if (!history.byPair.has(pair)) history.byPair.set(pair, []);
        history.byPair.get(pair).push(key);
      }
    }
  }
  let shortest = null;
  for (const pair of pairs) {
    const keys = history.byPair.get(pair);
    if (!keys) return null;
    if (!shortest || keys.length < shortest.length) shortest = keys;
  }
  const found = [];
  for (const key of shortest) {
    const have = new Set(key.split('|'));
    let holds = true;
    for (const pair of pairs) if (!have.has(pair)) { holds = false; break; }
    if (holds) found.push(key);
  }
  if (!found.length) return null;
  // More than one state carries everything the recording printed, and they differ in
  // a file it is silent about: naming one of them would be a guess between two
  // tracker states, so none is named and the recording is kept with that cause. The
  // commit that ADDED a recording is still its first candidate, and there the answer
  // is the state the record run saw.
  if (new Set(found).size > 1) return { ambiguous: [...new Set(found)].sort() };
  return history.byKey.get(found[0]) || null;
}

/** Turn every snapshot recording into a reference, and keep the ones no store
 * commit holds so a person can see them. Runs under the store lock, replaces each
 * file with an atomic rename (a kill leaves the snapshot or the reference, never
 * neither, and never half of one), and is idempotent: a reference is left alone.
 *
 * Bounded memory, whatever the store holds (#49): the first pass keeps the NAMES
 * of the snapshots, the second reads one, converts it, writes it and drops it, and
 * the only things that grow with the number of recordings are names and oids -
 * nothing grows with the size of one. What it converts it commits in batches of
 * MIGRATION_BATCH, so a run killed in the middle keeps its progress on the ref.
 *
 * A recording it keeps says WHY, in the words of that recording's own case: which
 * state file differs, which file the commit lacks, which commit it was compared
 * with. "No store commit holds that state" is a verdict, not a cause, and the
 * reporter of #49 had to add a debug print to learn what it meant. */
/** How many converted recordings are committed at a time. A migration over a
 * thousand snapshots spends minutes in git, and a run killed in the middle must
 * keep its progress on the ref, not only in the store's working tree. */
const MIGRATION_BATCH = 100;

/** The migration's own rule, as a version: what the machine remembers about a
 * pass is only valid for the rule that produced it, so a new rule reads the store
 * again instead of trusting an answer it would no longer give. */
const MIGRATION_RULE = 'comparable-state-1';

/** Every cause a recording is kept for, in plain words: what status groups them
 * by, and what the one line after a run names. The list of all of them, with the
 * file and the commit each was compared with, is in the machine's own folder. */
const KEEP_CAUSES = {
  'not-a-regular-file': 'not a regular file',
  'not-a-recording': 'its text does not read back as a recording this mechanism wrote',
  'text-not-recoverable': 'its text does not come back byte for byte, so no state file could be read from it',
  'no-comparable-file': "every state file it prints is one the store's git ignores, so no commit holds any of it",
  'no-candidate': "no state the store's history held could be compared with it",
  'states-agree': 'several states the history held carry what it prints and differ in a file it is silent about',
  'file-not-held': 'a state file it prints is in no commit the store holds',
  'file-differs': 'a state file differs from the nearest commit',
};
const keepRecording = (out, name, cause, said) => out.kept.push({ name, cause, said });

const migrationMemoryPath = (flags, repoRoot) => join(machineDir(flags, repoRoot), 'migration.json');
const migrationListPath = (flags, repoRoot) => join(machineDir(flags, repoRoot), 'migration-kept.txt');

/** Write one of the machine's own files atomically: a kill leaves the old file or
 * the new one, never half of one. */
function writeAtomic(file, body) {
  mkdirSync(dirname(file), { recursive: true });
  const tmp = `${file}.${process.pid}.tmp`;
  writeFileSync(tmp, body);
  renameSync(tmp, file);
}

/** Commit the recordings the migration has converted and not committed yet. The
 * message says how many this commit carried, so a run interrupted before the last
 * batch leaves commits that read on their own. */
async function flushConverted(out) {
  const n = out.pending;
  out.pending = 0;
  out.oid = await commitState(`tracker store: ${n} recording${n === 1 ? ' is a reference' : 's are references'} to the state commits they name`, [await headOid()]);
  out.commits++;
  return out.oid;
}

/** Recordings the store's working tree holds changed and no commit does: what a
 * migration killed between a rename and its commit left behind. Counted from
 * `git status`, so nothing is read, and a temporary file a kill left is not
 * counted as a recording. */
async function recordingsWaitingForCommit() {
  const r = await git(['status', '--porcelain', '--', '.tracker-sync/recordings'], { cwd: CURRENT.store });
  if (r.code !== 0) return 0;
  return r.stdout.split('\n').filter((l) => l.trim() && !l.includes('.migrating-') && /\.[a-z]{2,}$/.test(l.trim().slice(3).trim())).length;
}

/** What a pass can depend on, as one digest: this rule's version, the state files
 * a commit can hold (a project's ignore rules can change), the ref's head (a state
 * a recording needs can only arrive or go with a commit), and every whole-state
 * recording's name, size and mtime. The pass over a thousand recordings no commit
 * holds took about a minute, and it ran on every record, sync and publish; the
 * answer changes only when one of these does (#49). */
async function migrationMark(names, recDir, historyDigest = null) {
  const h = createHash('sha256');
  h.update(`rule\t${MIGRATION_RULE}\n`);
  h.update(`files\t${(await comparableStateFiles()).join('\t')}\n`);
  // The STATES the ref's history holds, not the head's oid: a publish commits the
  // state after the migration ran, so an oid would make every command that
  // published read a thousand recordings again. A commit that carries a state no
  // commit held before can make a kept recording convertible, and it adds a key
  // here; a commit that commits a state the history already holds adds none, and
  // cannot change the answer.
  // The history of the pass that answered for these recordings, never a newer one: a
  // batch commit can add a state while the pass runs, and a block of recordings
  // compared before it landed must not be remembered as compared against it.
  const history = historyDigest || createHash('sha256').update([...(await storeHistoryStates()).byKey.keys()].sort().join('\n')).digest('hex');
  h.update(`history\t${history}\n`);
  let whole = 0;
  for (const n of names) {
    let st = null;
    try { st = statSync(join(recDir, n)); } catch { /* gone since it was listed */ }
    if (!st) continue;
    whole++;
    h.update(`${n}\t${st.size}\t${st.mtimeMs}\n`);
  }
  return { digest: h.digest('hex'), whole, history };
}

/** The memory a pass left, or null when there is none or it is unreadable: an
 * unreadable one is not a reason to read a thousand recordings again by itself, so
 * it is simply not a match. */
function readMigrationMemory() {
  try {
    const f = migrationMemoryPath(CURRENT.flags, CURRENT.repoRoot);
    requireRegularFile(f, "the migration's memory");
    const d = JSON.parse(readFileSync(f, 'utf8'));
    return d && typeof d.digest === 'string' ? d : null;
  } catch { return null; }
}

/** What this pass kept, by cause, with one example of each, and every kept
 * recording on its own line with what it was compared with - the list a person
 * reads instead of adding a debug print (#49). Both files are the machine's own,
 * never the ref's, and both are rewritten by a pass, so neither grows with the
 * store. */
/** What a pass did, as two numbers a person and this mechanism's own proof can
 * read without timing anything: how many times the recordings were read, and how
 * many commands reused that answer instead of repeating it (#49). A pass that
 * converted nothing is not a failure - it is an answer - but a pass repeated on
 * every command is. */
function rememberPass(field) {
  const mem = readMigrationMemory() || {};
  const next = { ...mem, [field]: (Number(mem[field]) || 0) + 1 };
  if (field === 'passes') next.at = nowIso(); else next.lastSkip = nowIso();
  writeAtomic(migrationMemoryPath(CURRENT.flags, CURRENT.repoRoot), `${JSON.stringify(next, null, 2)}\n`);
}

async function writeMigrationMemory(recDir, rest, out, historyDigest = null) {
  const counts = {}; const examples = {}; const rows = [];
  for (const k of out.kept) {
    counts[k.cause] = (counts[k.cause] || 0) + 1;
    if (!examples[k.cause]) examples[k.cause] = k.name;
    rows.push(`${k.name}\t${k.cause}\t${k.said}`);
  }
  const list = migrationListPath(CURRENT.flags, CURRENT.repoRoot);
  writeAtomic(list, rows.length ? `${rows.join('\n')}\n` : '');
  const mark = await migrationMark(rest, recDir, historyDigest);
  const prev = readMigrationMemory() || {};
  writeAtomic(migrationMemoryPath(CURRENT.flags, CURRENT.repoRoot), `${JSON.stringify({
    ...prev, rule: MIGRATION_RULE, digest: mark.digest, whole: mark.whole,
    kept: counts, examples, list: rows.length ? list : null, at: nowIso(),
    passes: (Number(prev.passes) || 0) + 1,
  }, null, 2)}\n`);
  out.keptCounts = counts;
  out.keptExamples = examples;
  out.listPath = rows.length ? list : null;
}

/** The kept recordings in plain words, biggest group first: the number, the cause,
 * and - with `examples` - the recording to look at. */
function keptSaid(counts, examples = null) {
  return Object.entries(counts || {})
    .sort((a, b) => b[1] - a[1])
    .map(([cause, n]) => `${n} × ${KEEP_CAUSES[cause] || cause}${examples && examples[cause] ? ` (for example ${examples[cause]})` : ''}`);
}

/** Why this snapshot is not the state of this commit, in one sentence: the first
 * state file the commit lacks, or the first one whose bytes differ, named with the
 * commit it was compared with. null when every file the recording prints is the
 * commit's. */
function stateDifference(atParts, compare, oid) {
  const at = new Map(atParts || []);
  for (const [p, body] of compare) {
    if (!at.has(p)) return { cause: 'file-not-held', said: `store commit ${String(oid).slice(0, 12)} holds no ${p}, which this recording prints` };
    const there = at.get(p);
    if (there !== body) {
      return { cause: 'file-differs', said: `${p} differs from store commit ${String(oid).slice(0, 12)}'s (the commit's is ${Buffer.byteLength(there, 'utf8')} bytes, this recording's ${Buffer.byteLength(body, 'utf8')})` };
    }
  }
  return null;
}

/** What the migration kept and why, as the last pass recorded it, checked against
 * the recordings as they stand now: a memory that does not describe this store is
 * not read as this store's answer, and `status` then says the states are new
 * rather than naming a cause that may no longer hold. Reads no recording. */
async function migrationReport(names) {
  if (!names || !names.length) return null;
  const mem = readMigrationMemory();
  if (!mem || mem.rule !== MIGRATION_RULE) return { known: false };
  const mark = await migrationMark(names, join(CURRENT.store, '.tracker-sync', 'recordings'));
  if (mark.digest !== mem.digest) return { known: false };
  return { known: true, kept: mem.kept || {}, examples: mem.examples || {}, list: mem.list || null, whole: mark.whole,
    passes: Number(mem.passes) || 0, skips: Number(mem.skips) || 0, at: mem.at || null };
}

async function migrateRecordings() {
  const recDir = join(CURRENT.store, '.tracker-sync', 'recordings');
  const out = { converted: 0, kept: [], freed: 0, pending: 0, commits: 0, oid: null, skipped: false, remembered: null, examined: 0 };
  let names = [];
  try { names = readdirSync(recDir).filter((n) => n.endsWith('.jsonl')).sort(); } catch { return out; }
  // Names only. A snapshot is a whole state, a few megabytes, and a store can hold
  // a thousand of them: keeping their text here was 4-6 GB of live strings and the
  // migration died of it before converting one recording (#49). `isReferenceFile()`
  // reads the first 64 bytes, which is what `status` already counts with.
  const snapshots = [];
  for (const n of names) {
    const file = join(recDir, n);
    const notRegular = notRegularFileSaid(file);
    if (notRegular !== null) { keepRecording(out, n, 'not-a-regular-file', notRegular); continue; }
    if (!isReferenceFile(file)) { snapshots.push(n); continue; }
    // A file that looks like a reference and is not one is reported, as it always
    // was: nothing is converted from it and nothing is hidden about it. A
    // reference is a line or two, so reading this one is not the leak.
    let t = null;
    try { t = readFileSync(file, 'utf8'); } catch { keepRecording(out, n, 'not-a-regular-file', `${file} could not be read`); continue; }
    if (!parseRecording(t)) keepRecording(out, n, 'not-a-recording', `it starts ${JSON.stringify(t.trim().slice(0, 40))} and names no store commit the way this mechanism writes one`);
  }
  if (!snapshots.length) return out;
  // The same store, the same rule, the same recordings, the same head, the same
  // ignore rules: the pass would say exactly what the last one said, so it is not
  // repeated. A recording that changed, a commit that arrived or went, a project
  // that started or stopped ignoring a state file, and a new rule all change this
  // digest, and each of them can change the answer.
  // A project's ignore rules can change while a watch runs, and a pass is the one
  // place that decides what may differ: the answer is asked of git again here.
  comparableFiles = null;
  const before = await migrationMark(snapshots, recDir);
  const remembered = readMigrationMemory();
  if (remembered && remembered.rule === MIGRATION_RULE && remembered.digest === before.digest) {
    out.skipped = true;
    out.remembered = remembered;
    rememberPass('skips');
    out.keptCounts = remembered.kept || {};
    out.keptExamples = remembered.examples || {};
    out.listPath = remembered.list || null;
    return out;
  }
  const files = await comparableStateFiles();
  // The commit that added a recording file is its first candidate: that commit's
  // tree is the state the record run captured. The ref's whole history answers
  // only for the rest, and is built once for all of them.
  const added = new Map();
  const log = await git(['log', '--first-parent', '--format=%H', '--diff-filter=A', '--name-only', '--', '.tracker-sync/recordings'], { cwd: CURRENT.store });
  let cur = null;
  // The list of commits that added a recording is names and oids only - one line
  // per recording, never its text - so this grows with the number of recordings and
  // not with their size. A log this machine cannot read whole is said, and the
  // state history below still answers for every snapshot.
  if (log.code !== 0) say(`tracker-sync: the commits that added the recordings could not be read in one call (${text(log).split('\n')[0] || 'no output'}); the migration answers from the state the ref's history holds`);
  for (const line of log.stdout.split('\n')) {
    const t = line.trim();
    if (!t) continue;
    if (/^[0-9a-f]{40,64}$/.test(t)) { cur = t; continue; }
    if (!cur) continue;
    const name = basename(t);
    if (!added.has(name)) added.set(name, []);
    added.get(name).push(cur);
  }
  let history = null;
  // One recording at a time, and its text dropped before the next is read: the
  // migration's memory is one state, whatever the store holds. Only names, oids
  // and the states the ref's history held stay, and those do not grow with the
  // size of a recording.
  for (let i = 0; i < snapshots.length; i++) {
    const name = snapshots[i];
    out.examined++;
    let whole;
    try { whole = readFileSync(join(recDir, name), 'utf8'); } catch { keepRecording(out, name, 'not-a-regular-file', `${join(recDir, name)} could not be read`); continue; }
    const parts = snapshotParts(whole);
    if (!parts) {
      keepRecording(out, name, /^=== [^\n]+$/m.test(whole) ? 'text-not-recoverable' : 'not-a-recording',
        /^=== [^\n]+$/m.test(whole)
          ? `its ${Buffer.byteLength(whole, 'utf8')} bytes do not come back byte for byte from the state files they mark, so no state could be read from it`
          : 'it prints no state file header ("=== <path>"), so it holds no state this mechanism knows how to read');
      continue;
    }
    // Only the files a commit can hold are compared; the rest are named, so a change
    // in a project's ignore rules is visible rather than silent.
    const compare = parts.filter(([p]) => files.includes(p));
    const local = parts.filter(([p]) => !files.includes(p)).map(([p]) => p);
    if (!compare.length) {
      keepRecording(out, name, 'no-comparable-file', `every state file it prints (${local.join(', ')}) is one the store's git ignores, so no commit holds any of it`);
      continue;
    }
    const blobs = new Map();
    for (const [p, body] of compare) blobs.set(p, await blobOf(body));
    const near = (added.get(name) || [])[0] || null;
    let oid = null;
    for (const c of added.get(name) || []) {
      if (sameState(await stateBlobsAt(c), blobs)) { oid = c; break; }
    }
    if (!oid) {
      if (!history) history = await storeHistoryStates();
      const found = historyStateFor(history, blobs);
      if (found && found.ambiguous) {
        keepRecording(out, name, 'states-agree', `the store's history held ${found.ambiguous.length} states that carry every file it prints and differ in the ones it does not, so none is named: ${found.ambiguous.slice(0, 3).map((k) => k).join(' / ')}`);
        continue;
      }
      oid = found;
    }
    if (!oid) {
      // No commit holds it as a whole. The concrete cause is how its files differ
      // from the nearest state there is - the commit that added it, and with none
      // the state the store's head holds - because "no store commit holds that
      // state" is a verdict that sent the reporter of #49 to a debug print.
      const nearOid = near || await headOid();
      const nearText = await stateTextAt(nearOid);
      const nearParts = snapshotParts(nearText);
      const difference = nearParts ? stateDifference(nearParts, compare, nearOid) : null;
      keepRecording(out, name, difference ? difference.cause : 'no-candidate', difference ? difference.said
        : `${near ? `the commit that added it, ${String(near).slice(0, 12)},` : 'no commit added it,'} and none of the ${history.byKey.size} state${history.byKey.size === 1 ? '' : 's'} the ref's history held carries every file it prints (${compare.map(([p]) => p).join(', ')})`);
      continue;
    }
    // Byte for byte before anything is replaced, on the files being compared: the
    // reference must export the state the snapshot printed, or it is not the same
    // recording and nothing is converted from a guess.
    const atText = await stateTextAt(oid);
    const atParts = snapshotParts(atText);
    const difference = atParts ? stateDifference(atParts, compare, oid) : { cause: 'file-differs', said: `store commit ${String(oid).slice(0, 12)} could not be read back as the state files it holds` };
    if (difference) { keepRecording(out, name, difference.cause, difference.said); continue; }
    // The reference names the commit and digests the state that commit exports, so
    // a reader checks the digest against the same bytes a snapshot returned.
    const next = recordingText(oid, atText);
    const tmp = join(recDir, `.${name}.migrating-${process.pid}`);
    writeFileSync(tmp, next);
    renameSync(tmp, join(recDir, name));
    out.converted++;
    out.pending++;
    out.freed += Math.max(0, Buffer.byteLength(whole, 'utf8') - Buffer.byteLength(next, 'utf8'));
    snapshots[i] = null;
    // Dropped before the next recording is read, so the peak is one state and not
    // the whole store's worth of them.
    whole = null;
    if (out.pending >= MIGRATION_BATCH) await flushConverted(out);
  }
  // What is still a whole state, and why, remembered for the next command: a store
  // whose recordings no commit can hold is not read again on every record, sync and
  // publish, and its owner reads the causes here instead of adding a print (#49).
  await writeMigrationMemory(recDir, snapshots.filter(Boolean), out, before.history);
  return out;
}

/** What an installation of an older version needs, done by the mechanism itself
 * and said in one line: the rubbish a killed process left, snapshots turned into
 * references, the tracker's own backups pruned to the recipe's bound. No owner
 * action, no flag: a store that grew for months is repaired the first time any
 * command with the store in its hands runs. */
async function migrateStore({ announce = false, force = false } = {}) {
  const swept = await sweepMachineDir();
  const conv = await migrateRecordings();
  let pruned = null;
  if (force || conv.converted || swept.candidates) {
    // A tracker's backups are its own: the recipe keeps the bound, and a recipe
    // that declares none is left alone. A prune that fails is said, never fatal:
    // it is housekeeping, and a failed conditional write is not what came of it.
    const r = CURRENT.recipe.prune ? await recipe('prune', {}) : null;
    if (r) pruned = { code: r.code, said: text(r).split('\n')[0] || null, failed: r.code !== 0 };
  }
  let oid = conv.oid || null;
  // What the migration converted but did not commit reaches the ref here: the tail
  // of a run killed between its last batch and its commit, or the whole of a run
  // killed before any batch. It is counted from the store's own status, so a run
  // that converts nothing new still publishes what an interruption left.
  if (!conv.pending) conv.pending = conv.converted ? 0 : await recordingsWaitingForCommit();
  if (conv.pending) oid = await flushConverted(conv);
  // One line after a run that changed something, and the top cause when it kept
  // anything: "1110 kept for a person" is a verdict, and the cause and the list are
  // what an owner needs. A pass that converted nothing and kept everything for the
  // same reason said all this once already, so it says nothing here.
  if (announce && (swept.candidates || conv.converted || conv.commits || pruned?.failed || (conv.kept.length && !conv.skipped)))
    say(`tracker-sync: cleaned ${swept.candidates} dead candidate${swept.candidates === 1 ? '' : 's'} (${humanBytes(swept.bytes)}), ${conv.converted} recording${conv.converted === 1 ? '' : 's'} now reference their state instead of copying it (${humanBytes(conv.freed)}), ${conv.kept.length} kept: ${keptSaid(conv.keptCounts).slice(0, 2).join('; ') || 'no cause said'} - every one is listed in ${conv.listPath || machineDir(CURRENT.flags, CURRENT.repoRoot)}${pruned ? `; backup prune exit ${pruned.code}` : ''}`);
  return { swept, ...conv, pruned, oid, migrated: !!(swept.candidates || conv.converted || conv.commits) };
}


/** The local files a candidate needs beyond the store's own tracked files: only
 * the ones the recipe declares, never a path the store's git ignores for its own
 * reasons. A tracker that keeps a setting outside its committed state names it in
 * `candidateFiles`; the mechanism names no tracker and copies nothing else. */
async function copyCandidateFiles(live, candidate, before) {
  const declared = CURRENT.recipe.candidateFiles || [];
  let files = 0; let bytes = 0;
  if (!declared.length) return { files, bytes };
  const tracked = new Set((await gitOk(['ls-tree', '-r', '--name-only', before], { cwd: live })).split('\n').filter(Boolean));
  const expand = (rel) => {
    if (rel.startsWith('/') || rel.split('/').includes('..'))
      die(1, `the recipe declares ${rel} as a local file a candidate needs, and a path outside the store is not one`);
    const base = basename(rel);
    if (!base.includes('*')) return [rel];
    const dir = dirname(rel);
    let names = [];
    try { names = readdirSync(dir === '.' ? live : join(live, dir)); } catch { return []; }
    const re = new RegExp(`^${base.split('*').map((x) => x.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')).join('.*')}$`);
    return names.filter((n) => re.test(n)).map((n) => (dir === '.' ? n : join(dir, n)));
  };
  for (const pattern of declared) {
    for (const rel of expand(String(pattern))) {
      // A declared path the store tracks is already in the candidate: the
      // check-out of `before` wrote it, and reading it twice would count it twice.
      if (tracked.has(rel)) continue;
      const from = join(live, rel);
      const st = lstatSync(from, { throwIfNoEntry: false });
      if (!st) continue;
      if (st.isSymbolicLink()) throw new Error(`cannot isolate conditional recipe: ${rel} is a symbolic link; nothing conditional was written`);
      if (!st.isFile()) throw new Error(`cannot isolate conditional recipe: ${rel} is not a file, and a candidate copies only the files a recipe declares`);
      if (/(?:\.(?:json|yaml|yml|toml|ini|conf|env)|\/config)$/.test(from) && readFileSync(from).includes(Buffer.from(live)))
        throw new Error('cannot isolate conditional recipe: a local file names the live store');
      const to = join(candidate, rel);
      mkdirSync(dirname(to), { recursive: true });
      cpSync(from, to);
      files++; bytes += st.size;
    }
  }
  return { files, bytes };
}

/** A pending condition never becomes the live worktree's HEAD or database.
 * Local writers and publishers keep using the live store while the immutable
 * candidate is on the wire. Only acceptance integrates it under the local lock.
 * Copy local recipe configuration too, but never follow a pointer to live data. */
async function isolatedCandidate(kind, item, prepare) {
  const live = CURRENT.store;
  const previousEnv = CURRENT.recipeEnv;
  const previousThrow = THROW_ERRORS;
  const previousIsolation = CURRENT.isolatedFrom;
  const candidate = join(machineDir(CURRENT.flags, CURRENT.repoRoot), `candidate-${randomUUID()}`);
  const diagnostic = { kind, item, pid: process.pid, createdAt: nowIso(), phase: 'preparing', files: 0, bytes: 0, snapshotMs: 0, importMs: 0 };
  const resultFile = `${candidate}.result.json`;
  const save = () => writeFileSync(resultFile, `${JSON.stringify(diagnostic, null, 2)}\n`);
  try {
    THROW_ERRORS = true;
    save();
    const step = await locked(async () => {
      await recipeOk('export', {});
      if (await hasUncommittedState()) await commitState(`tracker store: before ${kind}`, [await headOid()]);
      const before = await headOid();
      await gitOk(['worktree', 'add', '--detach', candidate, before], { cwd: live });
      // What a conditional write reads is the tracker's state, never a recording:
      // the recordings stay in the candidate's index - a commit made here must
      // never delete one - and are left out of its worktree, so a store that still
      // holds whole snapshots does not copy gigabytes into a candidate. A recording
      // written here is added with --sparse, which is what puts it back in the tree.
      await git(['-c', 'advice.updateSparsePath=false', 'sparse-checkout', 'set', '--no-cone', '/*', '!/.tracker-sync/recordings/*'], { cwd: candidate });
      const snapshotStart = Date.now();
      const copied = await copyCandidateFiles(live, candidate, before);
      diagnostic.files = copied.files;
      diagnostic.bytes = copied.bytes;
      diagnostic.snapshotMs = Date.now() - snapshotStart;
      save();
      // The candidate's database, copied from the live one where the recipe can:
      // the live database was flushed into `before` a moment ago, so a copy of it
      // is the candidate's state without a whole-store import (#52). Run before
      // the isolation below, because it is the one command that must name the
      // live store; one that declines is not a failure, the import follows.
      let seeded = false;
      if (CURRENT.recipe.seed) {
        const seedStart = Date.now();
        const sd = await recipe('seed', { live: quote(live) }, { cwd: candidate });
        seeded = sd.code === 0;
        diagnostic.seeded = seeded;
        diagnostic.seedMs = Date.now() - seedStart;
        if (!seeded) diagnostic.seedSaid = text(sd).split('\n')[0] || `exit ${sd.code}`;
        save();
      }
      CURRENT.store = canonicalPath(candidate);
      CURRENT.isolatedFrom = live;
      // pointer.env is trusted recipe-produced launcher shell text. Preserve
      // its existing shell quoting/expansion contract and keep values in memory.
      const route = await recipe('pointer.env', {});
      if (route.code !== 0) return { failed: route, decl: 'pointer.env' };
      const bound = await sh(`{ set -a;\n${route.stdout}\nset +a; } >/dev/null\n${quote(process.execPath)} -e 'process.stdout.write(JSON.stringify(process.env))'`, { cwd: CURRENT.store, env: previousEnv });
      if (bound.code !== 0) throw new Error('cannot isolate conditional recipe: pointer.env launcher failed');
      try {
        CURRENT.recipeEnv = JSON.parse(bound.stdout);
        for (const key of Object.keys(process.env)) if (!(key in CURRENT.recipeEnv)) CURRENT.recipeEnv[key] = null;
        for (const value of Object.values(CURRENT.recipeEnv)) if (typeof value === 'string' && value.includes(live)) throw new Error('live routing');
      } catch {
        throw new Error('cannot isolate conditional recipe: pointer.env launcher returned invalid environment');
      }
      keepIdentity();
      if (!seeded) {
        const importStart = Date.now();
        const imported = await recipe('import', {});
        diagnostic.importMs = Date.now() - importStart;
        save();
        if (imported.code !== 0) return { failed: imported, decl: 'import' };
      }
      return prepare(before);
    });
    if (!step.oid) {
      diagnostic.phase = step.already ? 'already-recorded' : step.lost ? 'lost' : 'recipe-failed';
      save();
      return { ...step, diagnostic: resultFile };
    }
    diagnostic.oid = step.oid;
    diagnostic.phase = 'pushing';
    save();
    // Network outside the mutation lock. A refused/unreachable candidate is
    // discarded, not rolled back over another local operation's completed work.
    const pushed = await pushOnce(step.oid);
    diagnostic.phase = pushed.ok ? 'accepted' : pushed.unreachable ? 'unknown' : 'refused';
    if (pushed.unreachable) {
      diagnostic.retainedRef = `refs/tracker-sync/candidates/${basename(candidate)}`;
      await gitOk(['update-ref', diagnostic.retainedRef, step.oid], { cwd: live });
    }
    save();
    return { ...step, pushed, diagnostic: resultFile };
  } catch (error) {
    diagnostic.phase = 'error';
    diagnostic.error = 'candidate preparation or push failed';
    save();
    throw error;
  } finally {
    CURRENT.store = live;
    CURRENT.recipeEnv = previousEnv;
    THROW_ERRORS = previousThrow;
    CURRENT.isolatedFrom = previousIsolation;
    if (existsSync(candidate)) {
      await git(['worktree', 'remove', '--force', candidate], { cwd: live });
      rmSync(candidate, { recursive: true, force: true });
    }
  }
}

async function conditionalCandidate(kind, decl, map, item) {
  return isolatedCandidate(kind, item, async (before) => {
    const w = await recipe(decl, map);
    if (w.code === 3) return { lost: text(w) };
    if (w.code !== 0) return { failed: w, decl };
    const exported = await recipe('export', {});
    if (exported.code !== 0) return { failed: exported, decl: 'export' };
    return { oid: await commitState(`tracker store: ${kind} ${item}`, [before]) };
  });
}

/** An unreachable push may have reached the remote after all: fetch, and check
 * whether the remote's tip contains this exact candidate. A run that proves it
 * accepts the write; anything else stays unknown.
 *
 * Another command may have settled and pruned this candidate first. That is the
 * same proof, so a diagnostic that is already gone is not an error, and the
 * ref is dropped from the diagnostic's own name when there is no file left to
 * read it from. */
async function reconcileCandidate(step) {
  if (!step.pushed?.unreachable) return;
  const observed = await fetchRemote();
  if (!observed.ok || !observed.tip) return;
  if ((await git(['merge-base', '--is-ancestor', step.oid, observed.tip], { cwd: CURRENT.store })).code !== 0) return;
  step.pushed = { ok: true, reconciled: true };
  let diagnostic = null;
  try {
    requireRegularFile(step.diagnostic, 'a retained candidate diagnostic');
    diagnostic = JSON.parse(readFileSync(step.diagnostic, 'utf8'));
  } catch {}
  const retained = diagnostic?.retainedRef || `refs/tracker-sync/candidates/${basename(step.diagnostic).replace(/\.result\.json$/, '')}`;
  if (diagnostic) {
    diagnostic.phase = 'accepted-reconciled';
    delete diagnostic.retainedRef;
    try { writeFileSync(step.diagnostic, `${JSON.stringify(diagnostic, null, 2)}\n`); } catch {}
  }
  // The ref is dropped with the old value: a ref that moved since is not ours.
  await git(['update-ref', '-d', retained, step.oid], { cwd: CURRENT.store });
}

async function integrateCandidate(step) {
  await locked(async () => {
    await mergeAndCommit(step.oid);
  });
}

async function conditional(kind, decl, map, item) {
  for (let attempt = 0; attempt < 3; attempt++) {
    await syncOnce({ needsRemote: true, quiet: true });
    const step = await conditionalCandidate(kind, decl, map, item);
    await reconcileCandidate(step);
    if (step.lost) {
      settleDiagnostic(step.diagnostic);
      die(3, `${kind} lost: ${step.lost.split('\n')[0] || 'the condition no longer holds'}; read again and take other work`);
    }
    // A recipe command that failed is an error: its diagnostic stays for a person.
    if (step.failed) die(step.failed.code === 127 || step.failed.missing ? 2 : 1, `the recipe's ${step.decl} command failed (${step.failed.code}): ${text(step.failed) || 'no output'}`);
    if (step.pushed.ok) {
      await integrateCandidate(step);
      settleDiagnostic(step.diagnostic);
      writeConfig(CURRENT.flags, CURRENT.repoRoot, { lastPublish: nowIso(), lastPublishOk: true });
      say(`${kind} landed on the remote: ${item}`);
      return;
    }
    if (step.pushed.unreachable) {
      // Unknown: the diagnostic and the retained ref stay until a run proves
      // what the remote holds.
      writeConfig(CURRENT.flags, CURRENT.repoRoot, { lastPublishOk: false });
      die(4, `the remote is unreachable, the ${kind} outcome is unknown: the remote may have accepted it before the connection failed. Reconcile remote state before retrying; candidate ${step.oid}`);
    }
    settleDiagnostic(step.diagnostic);
    say(`${kind}: the push was refused; the pending candidate was discarded, and the condition is asked again after synchronization`);
  }
  die(3, `${kind} lost: three pushes were refused; read again and take other work`);
}



/** Is this recording file a reference, or a snapshot written before references
 * existed? Only the first bytes are read: a store migrated today may still hold a
 * thousand snapshots of several megabytes each, and a status that read them all
 * would take as long as the leak did. */
function isReferenceFile(file) {
  try {
    const fd = openSync(file, 'r');
    try { const b = Buffer.alloc(64); const n = readSync(fd, b, 0, 64, 0); return b.slice(0, n).toString('utf8').startsWith('{'); } finally { closeSync(fd); }
  } catch { return false; }
}

/** What the machine's own directory and the store hold, and what dead runs left
 * in it. No command reported these numbers while 35 GB of dead candidates and 92
 * leftover result files sat beside one store, so an owner saw the leak only when
 * a write failed on a full disk (#45). */
function diskReport() {
  const dir = machineDir(CURRENT.flags, CURRENT.repoRoot);
  const found = listCandidates(null);
  const stale = found.stale.map((c) => ({ name: c.name, phase: c.result?.diagnostic?.phase || null, bytes: dirSize(c.path).bytes }));
  const retained = found.results.filter((r) => RETAINED_PHASES.has(r.diagnostic?.phase))
    .map((r) => ({ name: r.name, phase: r.diagnostic?.phase }));
  const machine = dirSize(dir);
  const store = dirSize(CURRENT.store);
  const recs = join(CURRENT.store, '.tracker-sync', 'recordings');
  let recordings = 0; let snapshots = 0; const snapshotNames = [];
  try {
    for (const name of readdirSync(recs)) {
      if (!name.endsWith('.jsonl')) continue;
      recordings++;
      // A directory or a pipe named like a recording is not a state: it is kept and
      // named by the migration, and counting it here would make the machine's own
      // memory of the pass disagree with the pass itself.
      if (notRegularFile(join(recs, name)) === null && !isReferenceFile(join(recs, name))) { snapshots++; snapshotNames.push(name); }
    }
  } catch {}
  return {
    machineDir: { path: dir, bytes: machine.bytes, files: machine.files },
    store: { path: CURRENT.store, bytes: store.bytes, files: store.files },
    recordings: { total: recordings, snapshots, snapshotNames },
    candidates: {
      total: found.created.length, stale: stale.length, staleBytes: stale.reduce((n, c) => n + c.bytes, 0),
      staleList: stale, retained: retained.length, retainedList: retained,
    },
  };
}

/** The store commit whose tree is exactly the state a recording records. The
 * candidate was checked out at `before`, and its state files are that commit's —
 * so the common case names `before` and writes no commit at all. A state taken
 * from a code revision (`--from-tree`) is no store commit yet: it becomes one,
 * because a reference must name a commit that holds it, and the store's own state
 * is put back before anything else runs so no later work is lost. A published
 * state commit is an ancestor of everything after it, which is the invariant a
 * reference rests on; a commit that is not (a ref rewritten outside this
 * mechanism) is not named, and the exact state is committed instead. */
async function recordedStateCommit(parts, oid, rev) {
  const head = await headOid();
  if (oid && (await git(['merge-base', '--is-ancestor', oid, head], { cwd: CURRENT.store })).code === 0) return oid;
  const held = await storeParts();
  // Exact: a state file the revision did not carry is removed for the commit, so the
  // tree holds that state and nothing of the store's own that would make the
  // recorded digest describe other bytes.
  await writeStateExact(parts);
  const made = await commitState(`tracker store: the state at code revision ${String(rev).slice(0, 12)}`, [await headOid()]);
  await writeStateExact(held);
  return made;
}

async function recordRun({ revision, checkout, fromTree }) {
  const ck = checkout ? resolve(checkout) : CURRENT.repoRoot;
  let captured;
  for (let attempt = 0; attempt < 3; attempt++) {
    const remote = await fetchRemote();
    if (!remote.ok) die(4, 'the remote is unreachable, so recording identity cannot be checked; nothing new was recorded');
    // A record run proves acceptance of an earlier candidate too: a recording
    // that was reported unknown and did land is settled here, so its ref and
    // diagnostic go before this run looks at the revision.
    await locked(async () => {
      // A record runs from every worktree's post-commit hook, so it is the command
      // that meets an old installation first: the sweep and the migration happen
      // here, before the candidate is prepared, and the candidate carries the
      // migration commit to the remote with its own.
      await migrateStore();
      await pruneSettledCandidates(remote.tip);
    });
    const step = await isolatedCandidate('record', revision || 'HEAD', async (before) => {
      const ck2 = ck;
      let rev = revision;
      let parts;
      if (!captured) {
        if (fromTree) {
          rev = rev || (await gitOk(['rev-parse', 'HEAD'], { cwd: ck2 })).trim();
          if ((await git(['cat-file', '-e', `${rev}^{commit}`], { cwd: CURRENT.store })).code !== 0)
            die(1, `${rev} is not a revision of this repository`);
          parts = await stateFrom(rev);
          if (parts.every(([, c]) => c === null))
            die(1, `${rev} carries none of the state files (${stateFiles().join(', ')}), so it holds no state to record`);
        } else {
          const head = (await gitOk(['rev-parse', 'HEAD'], { cwd: ck2 })).trim();
          rev = rev || head;
          if (rev !== head)
            die(1, `revision ${String(rev).slice(0, 12)} is not ${ck2}'s HEAD (${head.slice(0, 12)}): only a revision cut or landed now is recorded, because a later state is not that revision's`);
          // Only the files a commit can hold: the recording digests the state a
          // reader gets back from the commit, and a file the store's git ignores is
          // in no commit, so a digest that covered it would never verify (#49).
          const files = await comparableStateFiles();
          parts = (await storeParts()).filter(([p]) => files.includes(p));
        }
        // The commit the candidate's own state files come from: the state this
        // revision has is already in the store's history, and the recording names
        // it instead of copying it. A retry keeps the commit the first attempt
        // captured, so the recording is the state that was captured, not a later
        // one; a state taken from a code revision has no such commit yet.
        captured = { rev, parts, oid: fromTree ? null : before };
      }
      ({ rev, parts } = captured);
      const file = join(CURRENT.store, '.tracker-sync', 'recordings', `${rev}.jsonl`);
      // The candidate leaves the recordings out of its worktree (they are not what a
      // write reads), so a recording this machine already holds is read from the
      // live store: the candidate's own state files are that store's at `before`.
      const held = CURRENT.isolatedFrom ? join(CURRENT.isolatedFrom, '.tracker-sync', 'recordings', `${rev}.jsonl`) : file;
      // What the remote holds for this revision, read before anything is said about
      // "already recorded": a recording this machine kept while the remote's publish
      // of the same revision was refused is exactly where two different states for
      // one revision meet, and that is a refusal for a person, never a success.
      const there = remote.ok && !remote.noRef
        ? (await git(['show', `${remote.tip}:.tracker-sync/recordings/${rev}.jsonl`], { cwd: CURRENT.store })).stdout
        : '';
      // A recording is a reference, and two references to commits holding the SAME
      // state are not two different recordings: what a revision's state was is the
      // question, and a state is what the two files are compared as.
      const where = `the recording of ${String(rev).slice(0, 12)}`;
      const thereState = there ? (await recordingState(there, `${where} on the remote`)).text : null;
      // A recording that already exists is SUCCESS, not a failure: the record hook
      // runs again after a merge, and a person reruns the step after an interrupted
      // one; "this revision is recorded" is the state the step exists to reach, and
      // reporting it as an error made a session believe the revision never was
      // recorded, and made a hook fail on a revision it had recorded itself.
      // Nothing is written and nothing is pushed.
      if (existsSync(held)) {
        requireRegularFile(held, 'the recording');
        const mine = (await recordingState(readFileSync(held, 'utf8'), `${where} here`)).text;
        if (thereState !== null && thereState !== mine)
          die(1, `${String(rev).slice(0, 12)} is recorded here with a state the remote does not carry, and the remote holds one this machine does not: two different recordings of one revision, so neither is moved. A person must decide which state that revision had`);
        return { already: String(rev).slice(0, 12), where: 'here' };
      }
      if (thereState !== null && thereState !== sideText(parts))
        die(1, `${String(rev).slice(0, 12)} is already recorded on the remote with a different state; neither is moved, a person must decide`);
      if (thereState !== null) return { already: String(rev).slice(0, 12), where: 'on the remote' };
      // Capture the revision's state once, before merging remote changes.
      // Retries preserve that exact recording rather than recapturing later work.
      const stateOid = await recordedStateCommit(parts, captured.oid, rev);
      captured.oid = stateOid;
      if (!remote.noRef) await mergeAndCommit(remote.tip);
      mkdirSync(dirname(file), { recursive: true });
      // A reference, not a copy: the store's history already holds this state, and
      // a copy per revision grew one store to 5 GB against 10 MB of items (#45).
      // The digest covers the state the commit exports, which is what every reader
      // computes: a projection of the working tree's parts would not read back when
      // the commit holds a declared file the working tree does not (#49 review).
      writeFileSync(file, recordingText(stateOid, await stateTextAt(stateOid)));
      return { oid: await commitState(`tracker store: record ${String(rev).slice(0, 12)}`, [await headOid()]) };
    });
    if (step.already) {
      settleDiagnostic(step.diagnostic);
      writeConfig(CURRENT.flags, CURRENT.repoRoot, { lastRecord: nowIso() });
      say(`tracker-sync: ${step.already} is already recorded ${step.where}; a recording is written once and never moved, so nothing was written`);
      return;
    }
    // A recipe command that failed is an error: its diagnostic stays for a person.
    if (step.failed) die(step.failed.code === 127 || step.failed.missing ? 2 : 1, `the recipe's ${step.decl} command failed (${step.failed.code}): ${text(step.failed) || 'no output'}`);
    await reconcileCandidate(step);
    if (step.pushed.ok) {
      await integrateCandidate(step);
      settleDiagnostic(step.diagnostic);
      writeConfig(CURRENT.flags, CURRENT.repoRoot, { lastRecord: nowIso() });
      say(`tracker-sync: recorded the store at ${String(captured.rev).slice(0, 12)}`);
      return;
    }
    if (step.pushed.unreachable) die(4, `recording outcome is unknown: the remote may have accepted candidate ${step.oid}; reconcile remote state before retrying`);
    // A refused candidate is discarded and asked again: its diagnostic goes.
    settleDiagnostic(step.diagnostic);
  }
  die(4, 'the remote refused three recording candidates; live work was preserved; reconcile remote state before retrying');
}
// ---- connect, status, export, watch ----------------------------------------

/** The record step a checkout's own hook needs, said so a person can put it in a
 * hook this mechanism must not write to itself. */
const recordLine = (flags, repoRoot, checkout) =>
  `node ${quote(join(HERE, 'tracker-sync.mjs'))} record --revision "$(git rev-parse HEAD)" --checkout "$(git rev-parse --show-toplevel)" --state ${quote(machineDir(flags, repoRoot))} --recipe ${quote(CURRENT.recipe.path)}`;

/** The line that puts a checkout back on the store's pointer, for the hook that
 * runs when a branch is checked out. A branch from before the move carries its
 * own tracker folder, so checking it out replaces the pointer with a real
 * folder; switching away leaves that folder behind, and a tracker started there
 * would be a second one. This relinks it, and it says so when it cannot. */
const relinkLine = (flags, repoRoot) =>
  `node ${quote(join(HERE, 'tracker-sync.mjs'))} relink --checkout "$(git rev-parse --show-toplevel)" --state ${quote(machineDir(flags, repoRoot))} --recipe ${quote(CURRENT.recipe.path)}`;

const HOOK_MARKS = {
  'post-commit': '# shady2k tracker-sync record: this line records the revision this hook just cut or landed',
  'post-merge': '# shady2k tracker-sync record: this line records the revision this hook just cut or landed',
  'post-checkout': '# shady2k tracker-sync relink: this line puts a checkout back on the store when a branch left a folder there',
};

/** Where a checkout's hooks are, the line each one needs, and whether that
 * folder is the repository's own (core.hooksPath inside the tree), where a
 * machine's absolute paths must never be written. Asked with plain git, never
 * through the mechanism's own core.hooksPath override: that override would make
 * git report the mechanism's empty directory instead of the project's hooks. */
async function hookPlan(checkout) {
  const flags = CURRENT.flags; const repoRoot = CURRENT.repoRoot;
  const hp = await run('git', ['-C', checkout, 'rev-parse', '--git-path', 'hooks'], {});
  if (hp.code !== 0) die(1, `could not find the hooks directory of ${checkout}: ${text(hp)}`);
  const dir = resolve(checkout, hp.stdout.trim());
  const cfg = await run('git', ['-C', checkout, 'config', '--get', 'core.hooksPath'], {});
  const inRepo = cfg.code === 0 && cfg.stdout.trim() !== '' ? await hooksInsideRepository(checkout, dir) : false;
  const files = {
    'post-commit': `${recordLine(flags, repoRoot, checkout)} >/dev/null 2>&1 || true\n`,
    'post-merge': `${recordLine(flags, repoRoot, checkout)} >/dev/null 2>&1 || true\n`,
    'post-checkout': `${relinkLine(flags, repoRoot)} || true\n`,
  };
  const wanted = Object.keys(HOOK_MARKS).map((name) => {
    const file = join(dir, name);
    // A hook that is not a regular file is refused like one without the marker:
    // opening a named pipe there would block the connect for ever.
    const text = existsSync(file) && notRegularFile(file) === null ? readFileSync(file, 'utf8') : null;
    return { name, file, line: files[name], mark: `${HOOK_MARKS[name]}\n`, text, present: text !== null && text.includes(HOOK_MARKS[name]) };
  });
  return { dir, inRepo, wanted, missing: wanted.filter((h) => !h.present) };
}

/** Whether this hooks folder is inside the checkout's working tree. Git's own
 * hooks folder is <git dir>/hooks and is not the repository's file; a folder the
 * project configured with core.hooksPath that lies in the tree is. */
async function hooksInsideRepository(checkout, dir) {
  const top = await run('git', ['-C', checkout, 'rev-parse', '--show-toplevel'], {});
  if (top.code !== 0) return false;
  const inside = (p, root) => p === root || p.startsWith(root.endsWith('/') ? root : `${root}/`);
  dir=canonicalPath(dir);
  if (!inside(dir, canonicalPath(resolve(top.stdout.trim())))) return false;
  // Git's own metadata directory is not the repository's tracked files:
  // `core.hooksPath` naming it (`.git/hooks`) is the folder git uses by default,
  // nothing there is committed, and refusing it would take away a configuration
  // that worked before this check existed. The common directory is asked too, so a
  // linked worktree's hooks (which live in the main repository's git directory)
  // are recognised as git's own.
  for (const what of ['--git-dir', '--git-common-dir']) {
    const g = await run('git', ['-C', checkout, 'rev-parse', what], {});
    if (g.code === 0 && g.stdout.trim() && inside(dir, canonicalPath(resolve(checkout, g.stdout.trim())))) return false;
  }
  return true;
}

async function installHooks(checkout) {
  const plan = await hookPlan(checkout);
  mkdirSync(plan.dir, { recursive: true });
  for (const h of plan.wanted) {
    const { file: f, mark, line } = h;
    if (h.present) continue;
    if (h.text !== null) {
      // A hook that is not a POSIX shell script cannot carry this line: shell
      // appended to another language is a syntax error that stops every commit.
      const shebang = (h.text.split('\n')[0] || '').trim();
      if (shebang.startsWith('#!') && !/\/(sh|bash|dash|zsh|ksh)\b/.test(shebang)) {
        die(1, `${f} is not a shell hook (${shebang}): tracker-sync will not append shell to it. Call the step from that hook yourself: ${line.trim()}`);
      }
      // A hook that ends in `exit 0` would end before an appended step runs, so
      // the step goes in front of that line, and the project's own hook is kept.
      const tail = h.text.match(/\n[ \t]*exit[ \t]+0[ \t]*\n?[ \t]*$/);
      writeFileSync(f, tail && tail.index !== undefined
        ? `${h.text.slice(0, tail.index)}\n${mark}${line}${h.text.slice(tail.index).replace(/^\n/, '')}`
        : `${h.text.replace(/\n*$/, '\n')}${mark}${line}`);
      say(`tracker-sync: composed the ${h.name} step onto the existing ${f}${tail ? ' (before its own exit 0)' : ''}`);
    } else {
      writeFileSync(f, `#!/bin/sh\n${mark}${line}`);
      say(`tracker-sync: installed ${f}`);
    }
    await run('chmod', ['755', f], {});
  }
}

/** Put a checkout back on the store's pointer, and say so only when it moves.
 *
 * This is what the post-checkout hook runs. A branch that predates the move
 * tracks its own tracker folder, so checking it out replaces the pointer with a
 * real folder; switching away leaves that folder behind, and a tracker started
 * there would be a second tracker beside the machine's store. Whether such a
 * folder may be relinked or must be refused is the recipe's to say: it knows its
 * own tracker's files. Nothing of the store is written here, so no lock is taken.
 */
async function relinkRun(flags) {
  const checkouts = list(flags.checkout).map((c) => resolve(String(c)));
  if (!checkouts.length) checkouts.push(CURRENT.repoRoot);
  // Never for the store itself, nor for a worktree parked beside it: the checkout
  // a hook reports has to be the PROJECT's, and a git worktree of the store (the
  // mechanism makes one, and that very `worktree add` runs the project's hooks)
  // has the store as its own top level, whose pointer cannot be a link to itself.
  const mine = resolve(machineDir(flags, CURRENT.repoRoot));
  const isOurs = (c) => c === mine || c.startsWith(mine.endsWith('/') ? mine : `${mine}/`);
  for (const c of checkouts) {
    if (isOurs(c)) continue;
    const r = await recipe('pointer.install', { checkout: quote(c) }, { cwd: CURRENT.store });
    if (r.code !== 0)
      die(r.code === 127 || r.missing ? 2 : 1, `${c} is not on the store's pointer at ${CURRENT.store}: ${text(r)}`);
    const said = text(r);
    if (/relinked|installed|added/.test(said)) say(`tracker-sync: ${c} was put back on the store at ${CURRENT.store}: ${said.split('\n')[0]}`);
  }
}

async function connectRun(flags) {
  const repoRoot = CURRENT.repoRoot;
  const checkouts = list(flags.checkout).map((c) => resolve(String(c)));
  if (flags.all) {
    const wt = (await git(['worktree', 'list', '--porcelain'], { cwd: repoRoot })).stdout;
    for (const line of wt.split('\n')) if (line.startsWith('worktree ')) checkouts.push(resolve(line.slice(9).trim()));
    checkouts.push(repoRoot);
  }
  if (!checkouts.length) checkouts.push(repoRoot);
  const unique = [...new Set(checkouts)];
  // preflight: nothing is written before every runtime and destination answers
  const missing = [];
  if ((await run('git', ['--version'], {})).missing) missing.push('git is not on PATH');
  if (await recipe('prove', {}, { cwd: repoRoot }).then((r) => r.missing || r.code === 127)) missing.push(`the tracker's own command is missing (${CURRENT.recipe.prove})`);
  for (const c of unique) {
    if (!existsSync(c)) { missing.push(`${c} does not exist`); continue; }
    const isRepo = await run('git', ['-C', c, 'rev-parse', '--git-dir'], {});
    if (isRepo.code !== 0) missing.push(`${c} is not a git repository`);
  }
  if (missing.length) die(2, `connect cannot start: ${missing.join('; ')}`);
  // A hooks folder inside the repository must never take this machine's own
  // state path: the record line names an absolute state directory and the
  // recipe's own path, and it would be committed for every machine's checkout to
  // read. Asked before anything is written, and refused with the line that fixes
  // it, which the project puts in its own hook. A hook that already carries the
  // step is left alone: nothing is written there, so nothing is refused.
  for (const c of unique) {
    const plan = await hookPlan(c);
    if (!plan.inRepo || !plan.missing.length) continue;
    // Every missing hook is printed whole - the marker line and the command - so
    // the project can paste it into its own hook and rerun connect: the check reads
    // the marker, so a command without it would be refused again and the recovery
    // the message recommends would never complete.
    const add = plan.missing.map((h) => `  ${h.name}:\n    ${h.mark.trim()}\n    ${h.line.trim()}`).join('\n');
    die(1, `the hooks folder ${plan.dir} of ${c} is inside the repository (core.hooksPath names a folder in the tree), so these steps would commit this machine's own paths - its state directory ${machineDir(CURRENT.flags, repoRoot)} and the recipe ${CURRENT.recipe.path} - into every checkout of every machine. Nothing was written. Put them in the project's own hooks there yourself, marker line and command together, or point core.hooksPath at a folder outside the working tree and rerun connect:\n${add}`);
  }
  if (!flags.offline) {
    // Reachability is whether the remote answers at all. `--exit-code` makes git
    // exit 2 for "no matching ref", which says nothing about the remote: a bare
    // remote whose HEAD names a branch that does not exist (git init --bare
    // still defaulting to master while the project pushes main) answers
    // perfectly, and reading that as unreachable refused a remote connect can
    // use. The store ref's own absence is not unreachability either: connect
    // makes the store, and a remote with no store ref yet is the normal first
    // connect on a fresh remote.
    const any = await git(['ls-remote', 'origin'], { cwd: repoRoot, timeoutMs: 20000 });
    if (any.missing || any.timedOut || any.code !== 0)
      die(4, `the remote is unreachable (${text(any).split('\n')[0] || 'no answer'}); the store cannot be brought up to date. Pass --offline to connect anyway, and it will publish when the remote answers`);
  }
  // Everything connect writes happens under the store's one lock: nothing of its
  // transaction can interleave with a watcher's publish or another session's claim.
  const release = await takeLockWait(5000);
  try {
  // This call owns the store's lock for its whole transaction, and the async
  // context says so: a push that is refused inside it merges in this section
  // instead of waiting for a lock the same call already holds.
  await LOCK_CONTEXT.run(true, async () => {
    const { fresh } = await ensureStore({ create: true });
    // A store that came from the ref carries the other machines' state files. The
    // tracker's database here is this machine's own and must be rebuilt from them
    // BEFORE anything writes, or the first write is made against an empty database
    // and the export that follows is refused (a tracker refuses to flush an empty
    // database over a filled one) or the two sides' prefixes disagree.
    if (!fresh && CURRENT.recipe.import) {
      await recipeOk('import', {});
      say('tracker-sync: this machine\'s database was rebuilt from the state the ref carries');
    }
    // A store the mechanism has just created carries no tracker state. The
    // tracker's own "make me a store" command is the recipe's, and it runs here
    // and only here: never in a checkout, so a checkout can never conjure up a
    // second, empty tracker beside the machine's store.
    if (fresh && CURRENT.recipe.init) {
      await recipeOk('init', { prefix: quote(String(flags.prefix || '')) });
      say('tracker-sync: initialized the tracker inside the new store');
    } else if (fresh && !CURRENT.recipe.init) {
      say(`tracker-sync: the store is new; the recipe declares no init, so the tracker's state must be put there by the project (see the integration's Tracker layout field)`);
    }
    // The in-branch store's state is taken in BEFORE this machine claims its
    // ordinal and before anything of ours is pushed: the copy is allowed only
    // while the ref holds no store, and our own first commit would make it look
    // as if it already did.
    const from = flags['from-checkout'] ? resolve(String(flags['from-checkout'])) : null;
    if (from) {
      const remote = await fetchRemote();
      const hasRemoteStore = remote.ok && !remote.noRef;
      if (hasRemoteStore)
        die(1, `the ref ${CURRENT.ref} already holds a store (${remote.tip.slice(0, 12)}): a byte copy would overwrite records. Import and merge the checkout's remaining writes per item instead`);
      for (const p of stateFiles()) {
        const src = join(from, p);
        requireRegularFile(src, 'a state file in the checkout');
        if (!existsSync(src)) continue;
        const dest = join(CURRENT.store, p);
        mkdirSync(dirname(dest), { recursive: true });
        writeFileSync(dest, readFileSync(src));
        say(`tracker-sync: copied ${p} from ${from} into the store`);
      }
      await recipeOk('import', {});
    }
    const machineName = String(flags.machine || (await run('hostname', [], {})).stdout.trim() || 'machine');
    writeConfig(flags, repoRoot, { machine: machineName });
    // The recipe timeout is this machine's bound, read by every later command;
    // the watcher's own bounds are on its launch line.
    if (flags['recipe-timeout'] !== undefined) writeConfig(flags, repoRoot, { recipeTimeout: recipeTimeoutMs() / 1000 });
    if (CURRENT.recipe.ordinal) {
      const name = machineName;
      await recipeOk('ordinal', { machine: quote(name) });
      const oid = await commitState(`tracker store: this machine takes its ordinal`, [await headOid()]);
      const res = await pushOid(oid, { quiet: true });
      say(`tracker-sync: this machine is named in the store's machine ordinals${res.ok ? '' : ' (kept here, published when the remote answers)'}`);
    }
    const envLine = (await recipe('pointer.env', {}, { cwd: CURRENT.store })).stdout.trim();
    // NOT `--env-file`: node itself consumes that option (`--env-file=<path>`), so
    // the mechanism would never see it and node would fail on a missing file.
    const envFile = flags['launcher-file'] ? resolve(String(flags['launcher-file'])) : readConfig(flags, repoRoot).envFile;
    const says = [];
    for (const c of unique) {
      const install = await recipe('pointer.install', { checkout: quote(c) }, { cwd: CURRENT.store });
      if (install.code !== 0) {
        const done = says.map((s) => s.checkout).join(', ') || 'none';
        if (install.code === 127 || install.missing) die(2, `the pointer command is missing for ${c}; checkouts already pointed: ${done}. connect is safe to rerun`);
        die(1, `${c} could not be pointed at the store: ${text(install)}. Checkouts already pointed: ${done}. connect is safe to rerun`);
      }
      const check = await recipe('pointer.check', { checkout: quote(c) }, { cwd: CURRENT.store });
      const said = text(check);
      // The check must name the store this mechanism created: a tracker that
      // answers with some other store is not pointing at the machine's one.
      if (check.code !== 0 || !said.includes(CURRENT.store))
        die(1, `${c} does not reach the store at ${CURRENT.store}: the pointer check said "${said || 'nothing'}" (exit ${check.code})`);
      const variableOnly = /variable-only/.test(said);
      if (variableOnly && !envFile)
        die(1, `${c} reaches ${CURRENT.store} only through a variable and no launcher file was named: pass --launcher-file <file> (or put the checkout's pointer in place) so the checkout is pointed where its agents start, not in an agent's memory`);
      says.push({ checkout: c, variableOnly });
      say(`tracker-sync: ${c} reaches ${CURRENT.store}${variableOnly ? ' (through the variable the launcher carries)' : ' through its own pointer'}`);
      await installHooks(c);
    }
    // An installation that ran an older mechanism keeps what that version left:
    // dead candidates and their worktree registrations, a whole copy of the state
    // per revision, the tracker's own backups beyond the recipe's bound. Setup's
    // recheck runs connect, so this is where an existing project is repaired
    // without an action of its owner, and the line says what came back.
    const migrated = await migrateStore({ announce: true, force: true });
    if (migrated.oid) {
      const res = await pushOid(migrated.oid, { quiet: true });
      say(`tracker-sync: recordings this store held as whole copies now reference the state they record${res.ok ? '' : ' (kept here, published when the remote answers)'}`);
    }
    if (envFile && envLine) {
      requireRegularFile(envFile, 'the launcher file');
      const lines = existsSync(envFile) ? readFileSync(envFile, 'utf8') : '';
      if (!lines.includes(envLine)) writeFileSync(envFile, `${lines.replace(/\n*$/, '\n')}${envLine}\n`);
      say(`tracker-sync: the launcher file ${envFile} carries ${envLine.split('=')[0]}=... for checkouts outside the product's folder`);
    } else if (envLine) {
      say(`tracker-sync: checkouts outside the product's folder need this line in the agents' launcher: ${envLine}`);
    }
    writeConfig(flags, repoRoot, {
      recipe: CURRENT.recipe.path, tracker: CURRENT.recipe.tracker, ref: CURRENT.ref, repo: repoRoot,
      store: CURRENT.store, checkouts: unique, envFile: envFile || null, connectedAt: nowIso(),
      points: says, mechanismVersion: VERSION, mechanism: join(HERE, 'tracker-sync.mjs'),
    });
    say(`tracker-sync: connected ${unique.length} checkout(s) to ${CURRENT.store} on ${CURRENT.ref} (mechanism ${VERSION})`);
  });
  } finally { release(); }
}

async function statusRun(flags) {
  const repoRoot = CURRENT.repoRoot;
  const release = await takeLockWait(5000);
  try {
  // Status holds the store lock, so it is one of the commands that finds what a
  // killed process left: an owner who only ever asks how things stand must not be
  // the one who never gets it cleaned.
  const swept = await sweepMachineDir();
  const cfg = readConfig(flags, repoRoot);
  const out = { recipe: CURRENT.recipe.path, tracker: CURRENT.recipe.tracker, ref: CURRENT.ref, store: CURRENT.store,
    mechanism: { path: join(HERE, 'tracker-sync.mjs'), version: VERSION }, recordings: 0, checks: [], queued: {}, errorClass: null };
  try {
    out.head = await headOid();
    const disk = diskReport();
    out.recordings = disk.recordings.total;
    out.snapshots = disk.recordings.snapshots;
    out.migration = await migrationReport(disk.recordings.snapshotNames);
    out.disk = disk;
    const remote = await fetchRemote();
    out.unreachable = !remote.ok;
    out.queued = { unpublished: null, behind: null };
    if (!remote.ok) { out.errorClass = 4; out.remote = null; out.said = remote.said.split('\n')[0]; }
    else if (remote.noRef) { out.remote = null; out.errorClass = 0; out.said = 'the remote has no store ref yet'; }
    else {
      out.remote = remote.tip;
      const ahead = (await git(['rev-list', '--count', `${remote.tip}..HEAD`], { cwd: CURRENT.store })).stdout.trim();
      const behind = (await git(['rev-list', '--count', `HEAD..${remote.tip}`], { cwd: CURRENT.store })).stdout.trim();
      out.ahead = Number(ahead); out.behind = Number(behind);
      out.queued = { unpublished: Number(ahead), behind: Number(behind) };
    }
    for (const c of cfg.checkouts || []) {
      const r = await recipe('pointer.check', { checkout: quote(c) }, { cwd: CURRENT.store });
      out.checks.push({ checkout: c, pointsAt: text(r).split('\n')[0] || null, ok: r.code === 0,
        variableOnly: /variable-only/.test(text(r)), said: text(r) });
    }
    const pid = (() => {
      try {
        const f = pidPath(flags, repoRoot);
        requireRegularFile(f, 'the watch pid file');
        return Number(readFileSync(f, 'utf8').trim());
      } catch { return null; }
    })();
    const alive = pid ? (() => { try { process.kill(pid, 0); return true; } catch { return false; } })() : false;
    out.supervisor = { configured: !!cfg.supervisor || !!pid, alive, pid, lastTick: pid ? new Date(statSync(pidPath(flags, repoRoot)).mtimeMs).toISOString() : null };
    out.lastPublish = cfg.lastPublish || null; out.lastPublishOk = cfg.lastPublishOk ?? null;
    out.lastFetch = remote.ok ? nowIso() : cfg.lastFetch || null;
    out.lastSync = cfg.lastFetch || null;
    // How a project learns that its installed copy is older than the set's: the
    // copy reports the version it was installed from, and whoever has the set
    // compares it with the set's own (setup does this on every run, byte for
    // byte, and rewrites the copy and this version file).
    out.mechanism.installedAt = cfg.connectedAt || null;
    const expected = flags['expect-version'] ? String(flags['expect-version']) : cfg.expectedVersion;
    out.mechanism.update = !expected ? 'the set version is not named here'
      : expected === VERSION ? 'up to date with the set'
        : `older than the set (${expected}): rerun setup to refresh the copy`;
    out.points = cfg.points || null;
    out.config = { recipe: cfg.recipe, ref: cfg.ref, store: cfg.store, connectedAt: cfg.connectedAt || null };
    out.recipes = (await loadRecipes()).map((r) => ({ path: r.path, tracker: r.tracker || null, broken: r.broken || null }));
  } catch (e) { out.errorClass = 1; out.said = e.message; }
  if (flags.json) { say(JSON.stringify(out, null, 2)); return; }
  say(`recipe      ${basename(out.recipe)} (${out.tracker || 'tracker unnamed'})`);
  say(`ref         ${out.ref}`);
  say(`store       ${out.store}`);
  say(`head        ${out.head ? out.head.slice(0, 12) : 'none'}${out.remote ? `  remote ${out.remote.slice(0, 12)}` : '  remote none'}${out.unreachable ? '  UNREACHABLE' : ''}`);
  say(`queued      ${out.queued.unpublished ?? 0} local, ${out.queued.behind ?? 0} to take`);
  say(`recordings  ${out.recordings}${out.snapshots ? `  (${out.snapshots} still whole states${out.migration && out.migration.known ? '' : ': the next record, sync or publish converts them, or says which cannot be'})` : ''}`);
  // What the last pass kept and why, grouped by cause, with one example each: an
  // owner must not have to add a debug print to learn what a kept recording means
  // (#49). A memory that does not describe this store is not read as its answer.
  if (out.snapshots && out.migration && out.migration.known) {
    say(`kept        ${Object.keys(out.migration.kept).length ? keptSaid(out.migration.kept, out.migration.examples).join('; ') : 'nothing: these whole states are newer than the last pass'}`);
    say(`migration   the recordings were read ${out.migration.passes} time${out.migration.passes === 1 ? '' : 's'}, and ${out.migration.skips} command${out.migration.skips === 1 ? '' : 's'} reused that answer instead of reading ${out.migration.whole} of them again`);
    if (out.migration.list) say(`list        ${out.migration.list}`);
  }
  say(`disk        machine folder ${humanBytes(out.disk?.machineDir.bytes)} (${out.disk?.machineDir.files} files), store ${humanBytes(out.disk?.store.bytes)} (${out.disk?.store.files} files)`);
  say(`leftovers   ${swept.candidates} dead candidate${swept.candidates === 1 ? '' : 's'} removed now (${humanBytes(swept.bytes)}, ${swept.results} result file${swept.results === 1 ? '' : 's'})`
    + `${out.disk?.candidates.retained ? `; ${out.disk.candidates.retained} unknown outcome${out.disk.candidates.retained === 1 ? '' : 's'} kept for a reconcile` : ''}`);
  say(`supervisor  ${out.supervisor?.alive ? `alive (pid ${out.supervisor.pid})` : 'not running'}`);
  say(`mechanism   ${out.mechanism.version}${out.mechanism.update ? `  (${out.mechanism.update})` : ''}`);
  for (const c of out.checks) say(`checkout    ${c.ok ? (c.variableOnly ? 'ok via variable' : 'ok') : 'NOT POINTED'}  ${c.checkout}  ${c.pointedAt}`);
  } finally { release(); }
}

async function exportRun(flags, pos) {
  const at = flags.at ? String(flags.at) : pos[0];
  if (at) {
    const f = join(CURRENT.store, '.tracker-sync', 'recordings', `${at}.jsonl`);
    requireRegularFile(f, 'the recording');
    if (!existsSync(f))
      die(1, `no recording for ${at}: the store never recorded that revision, so its state is unknown and is not guessed from a later state`);
    // A recording holds a reference to the store commit whose state it records, so
    // the state is read from that commit: the bytes are the ones a snapshot of the
    // same state returned, and a baseline read at a revision changes by nothing.
    const { text: state } = await recordingState(readFileSync(f, 'utf8'), `the recording of ${at}`);
    process.stdout.write(state);
    return;
  }
  if (!flags.offline) {
    // syncOnce takes the store lock itself, around its local work only.
    const r = await syncOnce({ needsRemote: true, quiet: true });
    writeConfig(CURRENT.flags, CURRENT.repoRoot, { lastFetch: nowIso(), lastUnreachable: !!(r && r.unreachable) });
  } else {
    process.stderr.write("tracker-sync: --offline: this store is this machine's only and the other machines' state is not in it\n");
  }
  for (const p of stateFiles()) {
    const f = join(CURRENT.store, p);
    requireRegularFile(f, 'a state file');
    if (existsSync(f)) {
      const body = readFileSync(f, 'utf8');
      process.stdout.write(`=== ${p}\n${body}${body.endsWith('\n') ? '' : '\n'}`);
    }
  }
}

async function watchRun(flags) {
  const debounce = Number(flags.debounce ?? 2);
  const maxAge = Number(flags['max-age'] ?? 5);
  const interval = Number(flags.interval ?? 10);
  mkdirSync(machineDir(flags, CURRENT.repoRoot), { recursive: true });
  writeFileSync(pidPath(flags, CURRENT.repoRoot), String(process.pid));
  let stop = false;
  const onStop = () => { stop = true; };
  process.on('SIGINT', onStop); process.on('SIGTERM', onStop);
  say(`tracker-sync: watching ${CURRENT.store} (publish after ${debounce}s of quiet or ${maxAge}s at most, sync every ${interval}s)`);
  let firstChange = null; let lastChange = null; let lastDigest = '';
  // The pid file is touched every tick: its age is how a reader tells a live
  // watcher from a stale one.
  const heartbeat = setInterval(() => { try { const p = pidPath(flags, CURRENT.repoRoot); writeFileSync(p, String(process.pid)); } catch {} }, 5000);
  heartbeat.unref();
  // A round the watcher cannot take the lock for is skipped, not fatal: the two
  // loops of one process queue on each other in-process now, so a timeout means
  // another command (a record from a worktree, a sync by hand) holds the store,
  // and it is said once here rather than once per tick.
  let lockTimeoutSaid = false;
  const onLockTimeout = (e) => {
    if (lockTimeoutSaid) return;
    lockTimeoutSaid = true;
    say(`tracker-sync: another command holds the store lock (${e.message.split(';')[0]}); this round is skipped and tried again, the watcher stays up`);
  };
  const publishLoop = async () => {
    while (!stop) {
      // The lock covers the store's own work only; the push is a network call
      // and happens with the lock released, so a claim or a record from another
      // session is never made to wait on the watcher's network.
      let digest = null;
      try {
        digest = await locked(async () => {
          await flushDatabase();
          if (!(await hasUncommittedState())) return null;
          return stateDigest();
        }, 20000, { skipOnTimeout: true });
      } catch (e) {
        if (e.lockTimeout) { onLockTimeout(e); await sleep(1000); continue; }
        say(`tracker-sync: the export failed: ${e.message}`);
      }
      if (digest === null) { firstChange = null; lastChange = null; lastDigest = ''; await sleep(1000); continue; }
      const t = Date.now();
      if (digest !== lastDigest) { lastDigest = digest; lastChange = t; if (firstChange === null) firstChange = t; }
      const ready = (t - lastChange) / 1000 >= debounce || (t - firstChange) / 1000 >= maxAge;
      if (ready) {
        let oid;
        try {
          oid = await locked(async () => commitState(`tracker store: ${nowIso()}`, [await headOid()]), 20000, { skipOnTimeout: true });
        } catch (e) {
          if (e.lockTimeout) { onLockTimeout(e); await sleep(1000); continue; }
          throw e;
        }
        const res = await pushOid(oid, { quiet: true, skipOnTimeout: true });
        if (res.skipped) onLockTimeout(Object.assign(new Error(res.skipped), { lockTimeout: true }));
        writeConfig(flags, CURRENT.repoRoot, { lastPublish: nowIso(), lastPublishOk: !!res.ok });
        if (res.ok) {
          say(`tracker-sync: published ${oid.slice(0, 12)} (${((Date.now() - firstChange) / 1000).toFixed(1)}s after the first unpublished change)`);
          firstChange = null; lastChange = null; lastDigest = '';
        }
      }
      await sleep(1000);
    }
  };
  const syncLoop = async () => {
    while (!stop) {
      try {
        const r = await syncOnce({ quiet: true, skipOnTimeout: true });
        const skipped = (r && r.skipped) || (r && r.pushed && r.pushed.skipped);
        if (skipped) onLockTimeout(Object.assign(new Error(skipped), { lockTimeout: true }));
      } catch (e) { say(`tracker-sync: the sync failed: ${e.message}`); }
      await sleep(interval * 1000);
    }
  };
  await Promise.all([publishLoop(), syncLoop()]);
  clearInterval(heartbeat);
  try { unlinkSync(pidPath(flags, CURRENT.repoRoot)); } catch {}
  say('tracker-sync: watch stopped');
}
// ---- the command line -------------------------------------------------------

function usage(code = 1) {
  const t = [
    'usage: tracker-sync.mjs <command> [options]',
    '',
    'commands:',
    '  connect [--checkout <path>]... [--all] [--from-checkout <path>] [--launcher-file <path>] [--machine <name>] [--prefix <id prefix>] [--offline]',
    '  relink [--checkout <path>]...',
    '  publish [--watch] [--debounce <s>] [--max-age <s>]',
    '  sync [--interval <s>]',
    '  claim --item <id> --holder <holder>',
    '  transition --item <id> --to <status>',
    '  record --revision <rev> [--checkout <path>] [--from-tree]',
    '  export [--at <revision>] [--now] [--offline]',
    '  status [--json]',
    '  watch [--interval <s>] [--debounce <s>] [--max-age <s>]',
    '  recipes | version | help',
    '',
    'options:  --recipe <file>  --state <dir>  --repo <checkout>  --recipe-timeout <s> (connect records it)',
    'The recipe names the tracker; this mechanism never does. TRACKER_RECIPE is read too.',
  ].join('\n');
  (code === 0 ? say : (s) => process.stderr.write(`${s}\n`))(t);
  process.exit(code);
}

async function main() {
  const argv = process.argv.slice(2);
  const cmd = argv[0];
  const { flags, pos } = parseArgs(argv.slice(1));
  CURRENT.flags = { ...flags };
  if (!cmd || cmd === 'help' || cmd === '--help') usage(cmd ? 0 : 1);
  if (cmd === 'version') { say(VERSION); return; }
  if (cmd === 'recipes') {
    const all = await loadRecipes();
    if (!all.length) { say('this copy ships no recipe'); return; }
    for (const r of all) say(`${r.broken ? 'broken' : (r.tracker || r.name)} ${r.path}${r.broken ? ` — ${r.broken}` : ''}`);
    return;
  }
  CURRENT.repoRoot = await repoRootOf(flags.repo ? String(flags.repo) : process.cwd());
  CURRENT.machineKey = await machineKeyOf(CURRENT.repoRoot);
  CURRENT.recipe = await resolveRecipe();
  CURRENT.ref = CURRENT.recipe.ref || DEFAULT_REF;
  // A ref a branch or a tag contains would put the tracker back inside the code
  // branches, which is what the layout exists to avoid, and would start code CI.
  if (/^refs\/(heads|tags)\//.test(CURRENT.ref))
    die(1, `the recipe names ${CURRENT.ref} as the store's ref, and a branch or a tag carries the store back into the code branches: name a ref no branch contains (refs/tracker/state by default)`);
  // Only connect creates the store: every other command attaches to it and
  // fails loudly, naming connect, where there is none.
  if (cmd !== 'connect') {
    const s = await ensureStore({ create: false });
    if (s.absent) die(1, `there is no store at ${s.store}: run connect first (it creates the store, points the checkouts at it and starts the sync)`);
  }
  switch (cmd) {
    case 'connect':
      await connectRun(flags);
      break;
    case 'relink':
      await relinkRun(flags);
      break;
    case 'publish': {
      // publishOnce takes the store lock itself, for its local work only. Under
      // --watch this first round is the watcher's first tick, so a lock it cannot
      // take is skipped and said once, exactly as the loop says it.
      const r = await publishOnce({ quiet: false, skipOnTimeout: !!flags.watch });
      if (flags.watch) {
        if (r.skipped) say(`tracker-sync: another command holds the store lock (${r.skipped.split(';')[0]}); this round is skipped and tried again, the watcher stays up`);
        await watchRun(flags);
        break;
      }
      if (r.nothing) say('tracker-sync: nothing to publish');
      else if (r.ok) say(`tracker-sync: published ${String(r.oid).slice(0, 12)}`);
      else say('tracker-sync: the write is kept here and is published when the remote answers');
      break;
    }
    case 'sync': {
      if (flags.interval) { await watchRun(flags); break; }
      // syncOnce takes the store lock itself, around its local work only.
      const r = await syncOnce({ needsRemote: true, quiet: false });
      if (r.upToDate) say('tracker-sync: already in step with the remote');
      break;
    }
    case 'claim': {
      if (!flags.item || !flags.holder) usage(1);
      await conditional('claim', 'claim', { item: quote(String(flags.item)), holder: quote(String(flags.holder)) }, String(flags.item));
      break;
    }
    case 'transition': {
      if (!flags.item || !flags.to) usage(1);
      await conditional('transition', 'transition', {
        item: quote(String(flags.item)), to: quote(String(flags.to)),
        expect: quote(flags.expect ? String(flags.expect) : ''),
      }, String(flags.item));
      break;
    }
    case 'record':
      await recordRun({ revision: flags.revision ? String(flags.revision) : null, checkout: flags.checkout ? String(flags.checkout) : null, fromTree: !!flags['from-tree'] });
      break;
    case 'export':
      await exportRun(flags, pos);
      break;
    case 'status':
      await statusRun(flags);
      break;
    case 'watch':
      await watchRun(flags);
      break;
    default:
      process.stderr.write(`tracker-sync: unknown command ${cmd}\n`);
      usage(1);
  }
}

if (process.argv[1] && realpathSync(process.argv[1]) === realpathSync(fileURLToPath(import.meta.url)))
  main().catch((e) => die(e.exitCode || 1, e && e.stack ? e.stack.split('\n')[0] : String(e)));
