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
 *   5  the remote refused this ref: a hosting policy or a clone without write
 *      rights, never counted with 4. A clone that cannot publish is a legitimate
 *      clone: it keeps its hooks and its reads, connect says so in one sentence
 *      and exits 0, and every later command keeps the local work and makes no
 *      promise that a later attempt publishes it
 *
 * BOUNDED LATENCY, and what it costs: publish waits for the debounce of quiet or
 * the maximum batch age, whichever comes first, so a stream of writes is still
 * published within the maximum age. Every network git operation is killed after
 * its timeout (10 s for a push, 20 s for a sync fetch, and the store's whole
 * history in connect at the recipe timeout's floor of 120 s, because a large
 * store's first fetch outlasts a moment's bound) and reported as unreachable or
 * refused rather than hanging. A failed push is read by its OUTCOME, not by more
 * words: text that is only about the network is unreachable; text that may be an
 * authorization answer is asked about once with a bounded read; a remote that
 * answers its reads and refuses the write is a refusal, never an unreachable
 * remote and never a promise to publish later. A recipe command is killed after the recipe
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
/** A stop that arrives while this process is reading the store. The supervisor
 * signals the whole group (systemd's default KillMode=control-group), so the git
 * children of a merge are killed with it; that is a stop, not a failure and not a
 * fact about the store. Nothing here has committed anything by then, so the round
 * is abandoned and the next start continues (#skills-by6c). */
let STOPPING = false;
function stopNow(what) {
  die(0, `stopped while ${what}; nothing was committed by that round and the next start continues`);
}
/** One read of a path in a commit that is over: a stopped process exits as a
 * stop, and anything else is named as a failed read, never taken for a file the
 * commit does not hold. */
function readFailed(what, r) {
  if (STOPPING) stopNow(`reading ${what}`);
  die(1, `could not read ${what}: ${text(r).split('\n')[0] || `git exited ${r.code}`}; this is a failed read, not a file the commit does not hold, so nothing is decided from it`);
}
/** Git's own answer for an object it does not have, and the only answer that is a
 * commit this store does not hold: a `cat-file` that was killed says nothing
 * about whether the object is here (#skills-by6c). */
const MISSING_OBJECT = /not a valid object name|invalid object name|does not exist|bad object|Needed a single revision/i;
async function commitHere(oid, where) {
  const said = where || `store commit ${String(oid).slice(0, 12)}`;
  for (let attempt = 0; attempt < 2; attempt++) {
    const r = await git(['cat-file', '-e', `${oid}^{commit}`], { cwd: CURRENT.store });
    if (r.code === 0) return true;
    if (STOPPING) stopNow(`reading ${said}`);
    if (attempt === 0) continue;
    if (MISSING_OBJECT.test(text(r))) return false;
    readFailed(said, r);
  }
}
/** The names a commit holds under a path, in one read. A listing that failed is a
 * failed read, never a commit that holds nothing underneath (#skills-by6c). */
async function treeNames(rev, path, where) {
  const said = where || `the files of ${String(rev).slice(0, 12)} under ${path}`;
  for (let attempt = 0; attempt < 2; attempt++) {
    const r = await git(['ls-tree', '-r', '--name-only', rev, '--', path], { cwd: CURRENT.store });
    if (r.code === 0) return r.stdout.split('\n').map((l) => l.trim()).filter(Boolean);
    if (STOPPING) stopNow(`reading ${said}`);
    if (attempt === 1) readFailed(said, r);
  }
}
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
 *   signal: ['<a store-relative path the database writes>', ...]
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

const hasOrigin = async (cwd) => (await git(['remote', 'get-url', 'origin'], { cwd })).code === 0;

/** Take the store's ref from the remote into this clone, or say why it was not
 * taken - and never take an empty store in its place (#62).
 *
 * A ref absent here is not the same as a ref the remote never had. The first
 * store's fetch is a whole history, so it gets the recipe timeout's floor of
 * 120 s rather than the 20 s a sync's fetch is bounded by. Its result is READ,
 * and the question "does the remote hold this ref?" is asked of the remote when
 * it failed: holds it means stop, naming the ref and the failed fetch; answers
 * without it means this is genuinely a first store; no answer means stop too,
 * because an empty commit under the ref would present an empty tracker as the
 * real one. `--offline` with the ref absent here would be exactly that guess,
 * so it refuses before anything is written. A repository that names no remote
 * still gets its first store: there is nothing to ask and nothing to wait for.
 */
async function storeRefFromRemote() {
  const { repoRoot, flags } = CURRENT;
  const ref = CURRENT.ref;
  if (!(await hasOrigin(repoRoot))) return { have: false, noRemote: true };
  if (flags.offline)
    return { stop: true, code: 1, said: `--offline was given, ${ref} is not held in this clone and the remote is not read, so connect cannot tell a first store from a store it could not fetch. Nothing was written. Run connect without --offline when the remote answers, or fetch the ref first: git fetch origin +${ref}:${ref}` };
  const f = await git(['fetch', 'origin', `+${ref}:${ref}`], { cwd: repoRoot, timeoutMs: Math.max(120000, recipeTimeoutMs()) });
  if (f.code === 0) return { have: (await git(['rev-parse', '--verify', '--quiet', ref], { cwd: repoRoot })).code === 0 };
  const said = text(f).split('\n').filter(Boolean)[0] || `exit ${f.code}`;
  const ls = await git(['ls-remote', 'origin', ref], { cwd: repoRoot, timeoutMs: 20000 });
  if (ls.missing || ls.timedOut || ls.code !== 0)
    return { stop: true, code: 4, said: `the remote did not answer a read of ${ref} (${said}), so connect cannot tell a first store from a store it could not fetch. Nothing was written` };
  const holds = ls.stdout.split('\n').map((l) => l.trim().split(/\s+/)).some(([, name]) => name === ref);
  if (holds)
    return { stop: true, code: 4, said: `the remote holds ${ref} and this clone could not fetch it (${said}). Nothing was written: an empty store under that ref would present an empty tracker as the real one. Retry the fetch when the remote is back, then connect` };
  return { have: false };
}

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
      const taken = await storeRefFromRemote();
      if (taken.stop) die(taken.code, taken.said);
      have = taken.have;
      if (!taken.noRemote && !have) say(`tracker-sync: the remote answers and holds no ${CURRENT.ref}`);
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

/** The text of a path in a commit, or null where that commit does not hold the
 * path. A `git show` that fails and a commit that does not hold a file are two
 * different answers that both leave no output: stateFrom took the first for the
 * second, so a read killed when a supervisor stopped the watcher - or one that
 * failed for lack of memory - gave a state missing a file, and the digest check
 * then reported that the store ref's history had been rewritten (#skills-by6c).
 * An answer is asked of the commit's own tree: only a tree without the path is an
 * absent file. Anything else is one bounded retry and then a failed read. */
async function showAt(rev, path, where) {
  const r = await git(['show', `${rev}:${path}`], { cwd: CURRENT.store });
  if (r.code === 0) return r.stdout;
  // A stop that reached this read is a stop, not a fact about the commit: the
  // supervisor signals the whole group, so the git child died with this process.
  if (STOPPING) stopNow(`reading ${where || `${path} of ${String(rev).slice(0, 12)}`}`);
  const tree = await git(['ls-tree', '-r', '--name-only', rev, '--', path], { cwd: CURRENT.store });
  if (tree.code !== 0) readFailed(where || `${path} of ${String(rev).slice(0, 12)}`, r);
  if (!tree.stdout.split('\n').map((l) => l.trim()).includes(path)) return null;
  const again = await git(['show', `${rev}:${path}`], { cwd: CURRENT.store });
  if (again.code === 0) return again.stdout;
  readFailed(where || `${path} of ${String(rev).slice(0, 12)}`, again);
}

/** Write one state file into the store from a revision's tree, or from nowhere. */
/** `where` names the read in a failure: a recording's state read says which
 * recording it was, never only a store commit a person cannot place. */
async function stateFrom(rev, where = null) {
  const out = [];
  for (const p of stateFiles())
    out.push([p, await showAt(rev, p, where ? `${where} (${p})` : `the state file ${p} of ${String(rev).slice(0, 12)}`)]);
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

/** Stage the state files and the mechanism's own files, and commit a tree.
 *
 * `signal` is the database's change signal an export took immediately before it
 * read the database: this commit is then the state that signal describes, and
 * the pair is persisted so a hook that times out can prove what the head is
 * (see readStatePair). Every commit without one moves the head away from the
 * state the persisted pair describes, so the pair is dropped: a stale pair that
 * still read as valid would vouch for a state the database no longer has. */
async function commitState(message, parents, { signal = null } = {}) {
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
  pairExportState(signal, oid);
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
const isRefused = (r) => /rejected|non-fast-forward|fetch first|stale info/i.test(text(r));
// A refusal the remote's own policy makes (a protected ref, a denied user) is
// its own class and is never retried; a push a pre-receive hook merely declined
// this once is an ordinary refusal, and the fetch-merge-push round retries it.
// `cannot lock ref` is not a policy: it is a ref momentarily held by another
// process, and the fetch-merge-push round retries it. A policy refusal is one a
// person must change something for.
const isPolicy = (r) => /denied|protected ref|not allowed|refusing to allow|forbidden/i.test(text(r));

/** What a failed network call means, by OUTCOME rather than by more words.
 *
 * Three answers only, and each is decided by what the remote DID:
 *
 *   refused     the remote answered and will not take the write. Named, never
 *               retried, and never reported as a remote that answers later.
 *   unreachable nobody answered; nothing about rights is known.
 *   failed      neither: an answer this mechanism does not recognise, which is
 *               a failure for a person and is never retried as a moved ref.
 *
 * A host's text mixes a refusal with a transport error in exactly the lines that
 * matter (a read-only deploy key, a clone with no rights, `terminal prompts
 * disabled`), so the words alone cannot be the test. A text that only names the
 * network is unreachable with no probe: asking a remote that did not answer
 * costs its own timeout. A text that MAY be an authorization answer is decided by
 * one bounded read of the remote, and a remote that answers its reads while
 * refusing the write is refusing. An explicit policy word needs no probe.
 *
 * HTTP statuses are matched only in git's own `error: 403` form: a bare number
 * can sit inside an object id, and an object id is not a refusal.
 */
const NETWORK_ANSWER = /could not resolve host|failed to connect|connection refused|connection reset|connection (?:timed out|closed)|network is unreachable|no route to host|operation timed out|remote end hung up|early EOF|unexpected disconnect|error: (?:5\d\d|429)\b/i;
const AUTHORIZATION_ANSWER = /terminal prompts disabled|authentication failed|permission to \S+ denied|permission denied|no anonymous write access|unable to access|could not read from remote repository|marked as read only|invalid username or password|error: (?:401|403)\b|requested url returned error/i;
const POLICY_ANSWER = /protected ref|not allowed|refusing to allow|forbidden|pre-receive hook declined|not permitted/i;

/** Whether the remote answers at all, asked once: the read that separates a
 * refusal from an outage, and the only thing it decides. */
async function remoteAnswersReads(cwd = CURRENT.store) {
  const r = await git(['ls-remote', 'origin'], { cwd, timeoutMs: 20000 });
  return !(r.missing || r.timedOut || r.code !== 0);
}

async function pushFailureKind(r) {
  const t = text(r);
  // The network first: a text that names only the network is an outage whatever
  // else it says, and probing a remote that did not answer costs its timeout.
  if (r.timedOut || NETWORK_ANSWER.test(t)) return 'unreachable';
  if (AUTHORIZATION_ANSWER.test(t)) {
    // The remote's own last answer about THIS clone's writes is kept in the
    // machine's config, and it is used before the remote is asked again: a record
    // started by every commit must not spend a network round trip learning what
    // this clone already knows, or the hook is slower than it was (#62). A
    // refusal is about rights and does not lapse between two commits, and a push
    // that lands clears the mark.
    if (readConfig(CURRENT.flags, CURRENT.repoRoot).publishRefused) return 'refused';
    return (await remoteAnswersReads(CURRENT.store)) ? 'refused' : 'unreachable';
  }
  // A policy the text names outright needs no probe. `denied` is kept here, as it
  // always was: the answers that MIX a denial with a transport error are matched
  // above, so what is left is a remote naming a rule of its own.
  if (POLICY_ANSWER.test(t) || isPolicy(r)) return 'refused';
  return 'failed';
}

// ---- a remote that refuses this clone's writes ------------------------------
/** The one sentence a clone without write rights is owed, and the state that
 * keeps it from being said again per tick or per commit. A refusal is a fact
 * about rights, not a moment: it is remembered in the machine's own config so
 * that status can show it, it is said the first time and not after, and the
 * sentence names what does happen: the write stays here, and a push that lands
 * carries it - rights granted later, or a credential renewed, heal by themselves,
 * and a push that lands clears the mark. A read-only clone is legitimate: its
 * local work, its hooks and its reads are kept. */
let PUSH_REFUSED = null;
function refusalLine(said) {
  const line = String(said || '').split('\n').map((s) => s.trim()).filter(Boolean)[0] || 'no answer';
  return `the remote refuses this clone's writes (${line}); this clone cannot publish now, so the write stays here, and the next push that lands - a sync, or any later run - carries it`;
}
function noteRefusal(r) {
  const line = refusalLine(text(r));
  PUSH_REFUSED = line;
  const known = readConfig(CURRENT.flags, CURRENT.repoRoot).publishRefused;
  const first = !(known && known.said === line);
  writeConfig(CURRENT.flags, CURRENT.repoRoot, { lastPublishOk: false, publishRefused: { said: line, at: nowIso() } });
  return { said: line, first };
}
/** A push that lands clears the mark: this clone can publish again. */
function clearRefusal() {
  PUSH_REFUSED = null;
  if (readConfig(CURRENT.flags, CURRENT.repoRoot).publishRefused)
    writeConfig(CURRENT.flags, CURRENT.repoRoot, { publishRefused: null, lastPublishOk: true });
}

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
  //
  // A merge costs what it changes (#skills-rntd): the two sides are listed once,
  // with the blob id of every recording, so a recording that is the same file on
  // both sides is one lookup and reads no state, and one held on one side only is
  // copied without a state read. Two different files that both hold a reference
  // are compared by the blobs of the commits they name - a tree, never a file -
  // and only a real difference reads the two states, to refuse with the sentence
  // it refused with before. Reading a state per recording on both sides is what
  // made one commit on a store of 216 recordings cost about 170 s of CPU and
  // 1.49 GB, and it grew by one recording per commit.
  const recordingsIn = async (rev) => {
    const said = `the recordings of ${String(rev).slice(0, 12)}`;
    for (let attempt = 0; attempt < 2; attempt++) {
      const r = await git(['ls-tree', '-r', rev, '--', '.tracker-sync/recordings'], { cwd: CURRENT.store });
      if (r.code !== 0) {
        if (STOPPING) stopNow(`reading ${said}`);
        if (attempt === 1) readFailed(said, r);
        continue;
      }
      const out = new Map();
      for (const line of r.stdout.split('\n')) {
        const m = /^\d+ blob ([0-9a-f]+)\t(.*)$/.exec(line);
        if (m) out.set(m[2], m[1]);
      }
      return out;
    }
    return new Map();
  };
  const [a, b] = [await recordingsIn(head), await recordingsIn(tip)];
  // A referenced commit's state blobs, read once per merge however many
  // recordings name it.
  const stateBlobsOf = new Map();
  const blobsAt = async (oid) => {
    const key = String(oid);
    if (!stateBlobsOf.has(key)) stateBlobsOf.set(key, await stateBlobsAt(key));
    return stateBlobsOf.get(key);
  };
  for (const p of new Set([...a.keys(), ...b.keys()])) {
    const [oa, ob] = [a.get(p), b.get(p)];
    const whereA = `the recording of ${basename(p)} in ${String(head).slice(0, 12)}`;
    const whereB = `the recording of ${basename(p)} in ${String(tip).slice(0, 12)}`;
    if (oa && ob && oa === ob) continue; // the same file on both sides: one state, and nothing to read
    if (!oa) {
      // Held on the remote's side only: the file travels as it is, and no state
      // is read for it.
      const cb = await showAt(tip, p, whereB);
      if (cb !== null) { mkdirSync(dirname(join(CURRENT.store, p)), { recursive: true }); writeFileSync(join(CURRENT.store, p), cb); }
      continue;
    }
    if (!ob) continue; // ours only: ours stays, as it always did
    // Two different files. A recording holds a reference, so the two sides are
    // compared as the STATES they stand for: two references whose commits hold
    // the same state are one recording, and the trees answer that without either
    // state being read.
    const [ca, cb] = [await showAt(head, p, whereA), await showAt(tip, p, whereB)];
    if (ca === null || cb === null) continue;
    const [ra, rb] = [parseRecording(ca), parseRecording(cb)];
    // Equal trees prove one state only where both trees were read. A tree read
    // that answered nothing proves nothing: stateBlobsAt answers an empty map both
    // for a commit this store does not hold and for a commit that holds no state
    // file, and two empty maps are not one state. An empty pair therefore takes
    // the state-reading path below, where recordingState names the commit this
    // store does not hold (#skills-ppvr).
    const [blobsA, blobsB] = ra && rb ? [await blobsAt(ra.store), await blobsAt(rb.store)] : [null, null];
    if (blobsA && blobsA.size && sameBlobs(blobsA, blobsB)) continue;
    const sa = (await recordingState(ca, whereA)).text;
    const sb = (await recordingState(cb, whereB)).text;
    if (sa !== sb)
      die(1, `two different recordings for ${basename(p)}: ${head.slice(0, 12)} and ${tip.slice(0, 12)} recorded different states for one revision, so neither is moved; a person must decide`);
  }
  await recipeIsland(head, tip);
  keepIdentity();
  if (mustImport) await importState(held);
  // A merge that changes nothing is not a commit - unless the head does not hold
  // the remote's tip yet, because this commit is also what makes the push a
  // fast-forward of that tip: skipping it there left a head that could never be
  // pushed, and a write that never left the machine. With the tip already in the
  // head, a tick of a watcher in a clone that cannot publish would otherwise add
  // one merge commit of the same two states per tick, for ever (#62).
  const containsTip = (await git(['merge-base', '--is-ancestor', tip, head], { cwd: CURRENT.store })).code === 0;
  if (containsTip && !(await hasUncommittedState())) return head;
  return commitState(`tracker store: merge ${head.slice(0, 12)} and ${tip.slice(0, 12)} per item`, [head, tip]);
}
/** The mechanism's own files under .tracker-sync travel beside the state. */
async function recipeIsland(head, tip) {
  const names = (await treeNames(tip, '.tracker-sync', `the mechanism's own files of ${String(tip).slice(0, 12)}`))
    .filter((n) => n && !n.startsWith('.tracker-sync/recordings/'));
  for (const n of names) {
    // A read that failed is named, never taken for an empty file the two sides
    // disagree about (#skills-by6c).
    const theirs = (await showAt(tip, n, `${n} of ${String(tip).slice(0, 12)}`)) ?? '';
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
  // A sync is a READ that may also publish: a remote that refuses this clone's
  // writes must not turn the read into an exit, or a clone without write rights
  // could not read the other machines' state at all.
  // pushOid says a refusal itself when this call is not quiet; a quiet caller
  // (the watcher, export) says it in its own place, once.
  const pushed = push === false ? null : await pushOid(oid, { quiet, skipOnTimeout, allowRefusal: true });
  return { oid, pushed };
}
/** Push an immutable oid; a refused push fetches, merges and pushes again.
 * The push itself is a network call and runs outside the lock; the merge is local
 * work and takes it.
 *
 * `allowRefusal` is for the callers that must finish their own job whatever the
 * remote says about writing - connect, a watcher tick, a record started by a
 * commit hook: a remote that answers and refuses is then an OUTCOME they carry,
 * never an exit that leaves the rest of their work undone. Without it a refusal
 * is exit 5, the class it has always been. */
async function pushOid(oid, { quiet, attempts = 3, skipOnTimeout = false, allowRefusal = false } = {}) {
  // A refusal already met in this run is not asked again: a watcher tick that
  // retried it would be retrying a fact about rights, not a moment's failure.
  if (allowRefusal && PUSH_REFUSED) return { ok: false, refused: true, stopped: true, said: PUSH_REFUSED, first: false };
  // The last refusal, kept for the line that gives up: "three pushes were refused"
  // is a verdict, and what the remote answered is the cause (#49's addendum).
  let lastSaid = '';
  for (let i = 0; i < attempts; i++) {
    const r = await git(['push', 'origin', `${oid}:${CURRENT.ref}`], { cwd: CURRENT.store, timeoutMs: 10000 });
    if (r.code === 0) { clearRefusal(); return { ok: true, oid }; }
    const kind = await pushFailureKind(r);
    lastSaid = text(r).split('\n')[0] || `exit ${r.code}`;
    if (kind === 'unreachable') {
      if (!quiet) say(`tracker-sync: the remote is unreachable; the write is kept here (${lastSaid})`);
      return { ok: false, unreachable: true, said: text(r) };
    }
    // A remote that answers and refuses this write is a refusal, not a remote
    // that may answer later: named, never retried as if it had merely moved, and
    // never a promise to publish the write when something changes. A clone
    // without write rights is a legitimate clone and is told so once.
    if (kind === 'refused') {
      const refusal = noteRefusal(r);
      if (allowRefusal) {
        if (!quiet && refusal.first) say(`tracker-sync: ${refusal.said}`);
        return { ok: false, refused: true, said: refusal.said, first: refusal.first };
      }
      die(5, `${refusal.said} (${CURRENT.ref})`);
    }
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

/** The paths the recipe says move when its database does, or null.
 *
 *   signal: ['<a path the database writes>', ...]    store-relative
 *
 * A tracker's database is machine-local and its writes never touch the state
 * files until they are flushed, so "did anything change?" is a question about
 * the database, and the cheap answer is the stats of the files it writes: its
 * own file, its write-ahead log and their sidecars. Declared as paths, never as
 * a command, because the point of the signal is that asking costs no process
 * and no store lock: the mechanism stats them itself. A recipe that declares
 * none keeps the behaviour of a watcher that exports on every tick. */
function changeSignalPaths() {
  const decl = CURRENT.recipe.signal;
  if (!decl) return null;
  const paths = Array.isArray(decl) ? decl : decl.paths || [];
  const list = paths.map(String).filter((p) => p);
  return list.length ? list : null;
}

/** One tick's signal: each declared path's size and mtime, in nanoseconds.
 *
 * Read WITHOUT the store lock on purpose: it must not make a record wait. A
 * read that fails for any reason other than "the file is not there" answers
 * null, and null is never a match for anything, so the tick exports: a signal
 * nobody could read must never become a skip. */
function readChangeSignal(paths) {
  const parts = [];
  for (const p of paths) {
    try {
      const s = statSync(join(CURRENT.store, p), { bigint: true });
      parts.push(`${p}:${s.mtimeNs}:${s.size}`);
    } catch (e) {
      if (e && e.code === 'ENOENT') { parts.push(`${p}:-`); continue; }
      return null;
    }
  }
  return parts.join('|');
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
// How long an unqualified lock wait may take. A record started by a hook lowers
// it for its own run: a commit is already made and nothing may wait on it for
// minutes, so the hook's record asks a bounded few times instead (see
// recordWithRetries).
let LOCK_WAIT_MS = 120000;
// Whether this run is a record a checkout's hook started: its lock timeouts are
// thrown to that caller rather than ending the process on the spot.
let HOOK_RECORD = false;
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
async function locked(fn, waitMs = LOCK_WAIT_MS, { skipOnTimeout = false } = {}) {
  return inProcessLock(async () => {
    let release;
    try {
      release = await takeLockWait(waitMs);
    } catch (e) {
      // A loop skips the round; a record started by a hook is retried by its
      // caller, which is the one that knows how long a commit may wait (#57).
      if ((skipOnTimeout || HOOK_RECORD) && e.lockTimeout) throw e;
      die(1, e.message);
    }
    try { return await fn(); } finally { release(); }
  });
}
/** One publish. `publish` with `--watch` starts the watcher with this first round
 * and then keeps looping, so that round skips a lock it cannot take instead of
 * ending the command before the watcher it was asked to start. */
async function publishOnce({ quiet, skipOnTimeout = false, allowRefusal = false } = {}) {
  let oid;
  try {
    oid = await locked(async () => {
      // The database's signal, read under the store's own lock immediately before
      // the flush reads the database, is what this publish proves about the head.
      const signal = exportSignal();
      const migrated = await migrateStore({ announce: true });
      const flushed = await flushDatabase();
      // Only a flush the tracker answered cleanly proves the state files are the
      // database's: a refusal it calls "nothing unexported" leaves the files as
      // they are - which is another machine's merge, not this database - and a
      // refusal merged from the database's side file carries what that merge kept.
      const clean = !!(flushed && flushed.ok && !flushed.nothing && !flushed.merged);
      if (!migrated.migrated && !(await hasUncommittedState())) {
        pairExportState(clean ? signal : null, await headOid());
        return migrated.oid;
      }
      return commitState(`tracker store: ${nowIso()}`, [await headOid()], { signal: clean ? signal : null });
    }, 120000, { skipOnTimeout });
  } catch (e) {
    if (e.lockTimeout) return { ok: false, skipped: e.message };
    throw e;
  }
  if (oid === null) return { ok: true, nothing: true };
  const res = await pushOid(oid, { quiet, skipOnTimeout, allowRefusal });
  writeConfig(CURRENT.flags, CURRENT.repoRoot, { lastPublish: nowIso(), lastPublishOk: !!res.ok });
  if (res.ok && !quiet) say(`tracker-sync: published ${oid.slice(0, 12)} to ${CURRENT.ref}`);
  return res;
}
/** One push of one candidate: what the remote made of it, for a caller that
 * decides. A remote that refuses this clone's writes is `refusedByHost` - the
 * recording a candidate carries was made here and stays possible, while a claim
 * counts only on the remote - and is never read as an unreachable remote. */
async function pushOnce(oid) {
  const r = await git(['push', 'origin', `${oid}:${CURRENT.ref}`], { cwd: CURRENT.store, timeoutMs: 10000 });
  if (r.code === 0) { clearRefusal(); return { ok: true }; }
  const kind = await pushFailureKind(r);
  if (kind === 'unreachable') return { unreachable: true, said: text(r) };
  if (kind === 'refused') { const refusal = noteRefusal(r); return { refusedByHost: true, said: refusal.said, first: refusal.first }; }
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
  if (!(await commitHere(oid, `the store commit ${String(oid).slice(0, 12)} that ${where} names`)))
    die(1, `${where} names store commit ${String(oid).slice(0, 12)}, which this store does not hold. The store ref's history is append-only and is never rewritten, so a referenced commit is either here or the ref was rewritten outside this mechanism: the state of that revision is not guessed from a later one`);
  return sideText(await stateFrom(oid, where));
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
  const files = await comparableStateFiles();
  const read = () => git(['ls-tree', '-r', oid, '--', ...files], { cwd: CURRENT.store });
  let r = await read();
  if (r.code !== 0) {
    // A tree this store could not read is not a commit holding no state files: the
    // merge reads it to decide whether the remote moved anything, and an empty map
    // there would say "nothing moved" and skip the merge whole. A commit this store
    // does not hold is answered the way it always was - by the reads that name it -
    // so only a commit that IS here is a failed read, retried once (#skills-by6c).
    if (STOPPING) stopNow(`reading the state tree of ${String(oid).slice(0, 12)}`);
    if (!(await commitHere(oid, `the state tree of ${String(oid).slice(0, 12)}`))) return new Map();
    r = await read();
    if (r.code !== 0) {
      if (!(await commitHere(oid, `the state tree of ${String(oid).slice(0, 12)}`))) return new Map();
      readFailed(`the state tree of ${String(oid).slice(0, 12)}`, r);
    }
  }
  const out = new Map();
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
    diagnostic.phase = pushed.ok ? 'accepted' : pushed.unreachable ? 'unknown' : pushed.refusedByHost ? 'refused-by-host' : 'refused';
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
    // A claim or a transition counts only on the remote, so a remote that refuses
    // this clone's writes did not make it: named, exit 5, and the candidate goes.
    if (step.pushed.refusedByHost) {
      settleDiagnostic(step.diagnostic);
      die(5, `${step.pushed.said}: the ${kind} did not count, because a ${kind} counts only once the remote holds it`);
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

async function recordRun({ revision, checkout, fromTree, resolvedRevision }) {
  const ck = checkout ? resolve(checkout) : CURRENT.repoRoot;
  // The revision is resolved once, in the checkout the person asked about, and
  // carried through every attempt: a hook waits for the lock, HEAD may move
  // meanwhile, and the marker, the recording and the refusal must all be about
  // the same revision.
  const requested = resolvedRevision || await canonicalRevision(await recordedRevision({ revision, checkout }), ck);
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
    const step = await isolatedCandidate('record', requested || 'HEAD', async (before) => {
      const ck2 = ck;
      // A revision named by abbreviation keeps the name it was asked with: a
      // recording already made under it must be replayed, not written again
      // under the full id.
      let rev = revision || requested;
      let parts;
      if (!captured) {
        // A timed-out hook's marker names the store commit this revision's state
        // had when the hook gave up: that state IS the revision's history, and a
        // redo records it instead of the state standing now.
        const pinned = await timeoutPinnedState(CURRENT.flags, CURRENT.repoRoot, requested);
        if (pinned) {
          parts = await stateFrom(pinned);
          captured = { rev, parts, oid: pinned };
        } else if (fromTree) {
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
      // A recording the remote does not hold is an empty read, and a read that
      // failed is neither: a killed show is retried once, then named as a failed
      // read, so a stop cannot look like "the remote never recorded this revision"
      // and cannot overwrite the remote's recording with ours (#skills-by6c).
      const there = remote.ok && !remote.noRef
        ? (await showAt(remote.tip, `.tracker-sync/recordings/${rev}.jsonl`, `the remote's recording of ${String(rev).slice(0, 12)}`)) ?? ''
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
      // Check at the write, under the candidate's own lock: a hook's record of
      // this revision may have given up while this run fetched or waited. A
      // marker that names a store commit is that revision's state, and it is
      // recorded instead of this run's own, later capture; a marker that names
      // none is a state that was never captured, and today's state must not
      // become that revision's history.
      const arrival = requested ? findRecordTimeout(CURRENT.flags, CURRENT.repoRoot, requested) : null;
      let written = stateOid;
      if (arrival) {
        const pinned = await timeoutPinnedState(CURRENT.flags, CURRENT.repoRoot, requested);
        if (!pinned) die(1, `the recording of ${String(rev).slice(0, 12)} timed out at commit time and ${noSnapshotWaysOut(rev, arrival.commit)}`);
        if (pinned !== stateOid) captured.oid = pinned;
        written = pinned;
      }
      mkdirSync(dirname(file), { recursive: true });
      // A reference, not a copy: the store's history already holds this state, and
      // a copy per revision grew one store to 5 GB against 10 MB of items (#45).
      // The digest covers the state the commit exports, which is what every reader
      // computes: a projection of the working tree's parts would not read back when
      // the commit holds a declared file the working tree does not (#49 review).
      writeFileSync(file, recordingText(written, await stateTextAt(written)));
      // A marker can be written in the instant between the check above and this
      // write: a hook that gave up takes no store lock. It is still the state at
      // commit time and still earlier than this run's capture, so the recording
      // is rewritten to it before anything is committed.
      const late = requested ? findRecordTimeout(CURRENT.flags, CURRENT.repoRoot, requested) : null;
      if (late) {
        const pinnedLate = await timeoutPinnedState(CURRENT.flags, CURRENT.repoRoot, requested);
        if (!pinnedLate) die(1, `the recording of ${String(rev).slice(0, 12)} timed out at commit time and ${noSnapshotWaysOut(rev, late.commit)}`);
        if (pinnedLate !== written) {
          captured.oid = pinnedLate;
          writeFileSync(file, recordingText(pinnedLate, await stateTextAt(pinnedLate)));
        }
      }
      return { oid: await commitState(`tracker store: record ${String(rev).slice(0, 12)}`, [await headOid()]) };
    });
    if (step.already) {
      settleDiagnostic(step.diagnostic);
      // This revision IS recorded now: a marker a hook's timed-out record left
      // must not outlive it, or the next push would name a timeout that is over.
      clearRecordTimeout(CURRENT.flags, CURRENT.repoRoot, requested);
      writeConfig(CURRENT.flags, CURRENT.repoRoot, { lastRecord: nowIso() });
      say(`tracker-sync: ${step.already} is already recorded ${step.where}; a recording is written once and never moved, so nothing was written`);
      return;
    }
    // A recipe command that failed is an error: its diagnostic stays for a person.
    if (step.failed) die(step.failed.code === 127 || step.failed.missing ? 2 : 1, `the recipe's ${step.decl} command failed (${step.failed.code}): ${text(step.failed) || 'no output'}`);
    await reconcileCandidate(step);
    // A recording is this machine's own work, and a remote that refuses this
    // clone's writes does not take it away: the candidate is merged into the
    // store here, where every reader of a recording looks, and the one sentence
    // says that this clone cannot publish it. Keep it, never retry it as a
    // momentary refusal (#62).
    if (step.pushed.refusedByHost) {
      await integrateCandidate(step);
      settleDiagnostic(step.diagnostic);
      clearRecordTimeout(CURRENT.flags, CURRENT.repoRoot, requested);
      writeConfig(CURRENT.flags, CURRENT.repoRoot, { lastRecord: nowIso() });
      const at = String(captured.rev).slice(0, 12);
      // Once: a clone that already heard it does not hear it again per commit. A
      // record a person ran is their own action, and it says where the recording is.
      if (step.pushed.first) say(`tracker-sync: the recording of ${at} is kept in this store; ${step.pushed.said}`);
      else if (!CURRENT.flags.hook) say(`tracker-sync: the recording of ${at} is kept in this store, and this clone cannot publish it`);
      return;
    }
    if (step.pushed.ok) {
      await integrateCandidate(step);
      settleDiagnostic(step.diagnostic);
      clearRecordTimeout(CURRENT.flags, CURRENT.repoRoot, requested);
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
// ---- a recording that timed out at commit time ----------------------------
/**
 * The marker a hook's record leaves when the store lock could not be had.
 *
 * A post-commit hook has nothing to fail: the commit exists, its output is
 * discarded, and the recording it could not write is invisible until a push
 * refuses the revision and says "no recording for <rev>: the store never
 * recorded that revision" - which names the wrong cause (#57). So a record run
 * started by a hook that still cannot take the lock keeps a marker per revision
 * in the machine's own state (never in the tree): the push that reads the
 * missing recording names the timeout, and a later successful record of that
 * revision removes the marker.
 *
 * The marker also keeps the store commit whose tree holds the state that
 * revision had when the hook gave up. Retrying the record later would otherwise
 * record the tracker AS IT IS THEN under the old revision, and a recording is
 * the one history that revision will ever have (the gate reads it with --at, and
 * the baseline under block-new compares against it): a write made after the
 * commit would become that revision's permanent history and could make a new
 * violation look like old debt. So the redo records exactly that commit, and a
 * marker written before this field existed - a state that was never captured -
 * refuses a redo instead of inventing one.
 */
const recordTimeoutDir = (flags, repoRoot) => join(machineDir(flags, repoRoot), 'record-timeouts');
/** The recording of a revision in the live store, whoever wrote it. */
const recordingPathOf = (rev) => join(CURRENT.store, '.tracker-sync', 'recordings', `${rev}.jsonl`);
/** A revision's full object id, or the name as given when it does not resolve.
 *
 * A marker is keyed by the full id, so a hook that writes `rev-parse HEAD` and a
 * reader that asks with an abbreviation name the same marker, and two revisions
 * that begin alike can never be confused: one marker file per revision, matched
 * exactly. */
async function canonicalRevision(rev, ck = CURRENT.repoRoot || CURRENT.store) {
  const name = String(rev || '');
  if (!name) return null;
  const r = await git(['rev-parse', '--verify', `${name}^{commit}`], { cwd: ck });
  const oid = r.code === 0 ? r.stdout.trim() : '';
  return /^[0-9a-f]{40,64}$/.test(oid) ? oid : name;
}

/** The store commit whose tree is the state this machine has published, read
 * without the store lock: a ref read takes none, so a hook that cannot take the
 * lock can still name it.
 *
 * That commit is the one a recording references (`recordedStateCommit` names the
 * candidate's base, which is this head), so it is exactly what the state at a
 * revision would have been recorded against. What it cannot hold is a write the
 * watcher had not exported yet at the moment it was read: the watcher publishes
 * after `--debounce` seconds of quiet or `--max-age` seconds at most (2 and 5 by
 * default), so a tracker write made within that window before the read is in no
 * commit yet. A write the lock holder was exporting at that moment lands in a
 * LATER store commit, and a recording may not name that one either: a state
 * later than the revision's is what this whole mechanism must not invent. */
async function storeStateCommit() {
  const r = await git(['rev-parse', '--verify', 'HEAD^{commit}'], { cwd: CURRENT.store });
  const oid = r.code === 0 ? r.stdout.trim() : '';
  return /^[0-9a-f]{40,64}$/.test(oid) ? oid : null;
}

/** The signal an export took, and the store commit that export made.
 *
 * The head is the last PUBLISHED state, and a tracker write the watcher had not
 * exported yet is in no commit at all - so naming the head as a revision's state
 * is exact only while nothing is waiting to be exported (#skills-v0cn). An
 * export therefore persists the database's change signal, read immediately
 * before it reads the database, with the commit it made from it, or with the
 * unchanged head when it made none: after a flush that left no uncommitted
 * state, the head IS the state the database holds.
 *
 * A reader that finds the same signal standing now, and that commit at the head,
 * has proved the head is exactly the state the database had; the redo then
 * records it. Anything else is not proved: a signal that moved, a commit the
 * head has left behind, a pair an older release never wrote, or a path that
 * changed the head without a signal of its own - which drops the pair, because a
 * pair left standing reads as a valid claim about a state that is gone.
 *
 * The pair is this machine's own, so it lives in the machine's state directory
 * (never the tree), and it carries the store it describes, so a state directory
 * reused for another store cannot lend it one.
 */
const statePairPath = (flags, repoRoot) => join(machineDir(flags, repoRoot), 'export-state.json');
function readStatePair() {
  try {
    const f = statePairPath(CURRENT.flags, CURRENT.repoRoot);
    requireRegularFile(f, 'the last exportʼs state pair');
    const p = JSON.parse(readFileSync(f, 'utf8'));
    if (typeof p?.signal !== 'string' || !p.signal) return null;
    if (typeof p?.commit !== 'string' || !/^[0-9a-f]{40,64}$/.test(p.commit)) return null;
    // A pair written for another store proves nothing about this one.
    if (typeof p?.store === 'string' && p.store && canonicalPath(p.store) !== canonicalPath(CURRENT.store)) return null;
    // When it was written: a pair that came after the reader's own look at the
    // database proves nothing about the state the reader found (see exactStateCommit).
    const at = Date.parse(p.at || '');
    if (!Number.isFinite(at)) return null;
    return { signal: p.signal, commit: p.commit, at };
  } catch { return null; }
}
function writeStatePair(signal, commit) {
  writeAtomic(statePairPath(CURRENT.flags, CURRENT.repoRoot), `${JSON.stringify({
    signal, commit, store: CURRENT.store, at: nowIso(),
  }, null, 2)}\n`);
}
function clearStatePair() {
  try { rmSync(statePairPath(CURRENT.flags, CURRENT.repoRoot), { force: true }); } catch {}
}
/** What an export proved, or that it proved nothing: a signal that could not be
 * read is never a pair, and a pair is never invented from a later read. */
function pairExportState(signal, commit) {
  if (typeof signal === 'string' && signal && /^[0-9a-f]{40,64}$/.test(String(commit || ''))) writeStatePair(signal, commit);
  else clearStatePair();
}
/** The signal an export takes immediately before it reads the database, under
 * the store lock, so no other export can run between the two. */
const exportSignal = () => {
  const paths = changeSignalPaths();
  return paths ? readChangeSignal(paths) : null;
};
/** The store commit that holds exactly the state the database had when
 * `signalAtStart` was read, or null when that is not proved.
 *
 * Three things have to hold, and together they are the whole proof:
 *
 *   the pair's signal IS the signal read here - so the database did not move
 *   between the export's own read of it and this one; the pair's commit IS the
 *   store head; and the pair was written BEFORE this reader looked at the
 *   database. The last is what makes the state exact and not merely close: an
 *   export reads the database a moment AFTER it reads the signal, so a pair
 *   written after this reader's look could hold a tracker write made after the
 *   reader looked - a later state, which this mechanism must never record. A pair
 *   that was already there cannot: whatever the export read, it read it before
 *   the pair was written, which was before this reader's look, and a write in
 *   between would have moved the signal this reader compares.
 *
 * A signal that could not be read proves nothing, and neither does a recipe that
 * declares no signal at all: a tracker whose database is not observed cannot be
 * said to have stood still. */
async function exactStateCommit(signalAtStart, readAt) {
  if (typeof signalAtStart !== 'string' || !signalAtStart) return null;
  const pair = readStatePair();
  if (!pair || pair.signal !== signalAtStart) return null;
  if (!(Number.isFinite(readAt) && pair.at <= readAt)) return null;
  const head = await storeStateCommit();
  return head && pair.commit === head ? pair : null;
}

/** The store commit a timeout marker pinned, verified in this store.
 *
 * A recording may only name a commit the store still holds and that the store's
 * head builds on - the store ref's history is append-only - so a pinned commit
 * is checked both ways before it is used. A marker without a commit (one this
 * release's predecessor wrote) has no state to give: null, never a guess. */
async function timeoutPinnedState(flags, repoRoot, revision) {
  const marker = revision ? findRecordTimeout(flags, repoRoot, revision) : null;
  // A marker names a state only when it carries the proof that the state IS the
  // database's at that commit: the export pair, the commit it named being the one
  // the same marker names. A marker from 0.132.0 kept none; a marker from 0.133.0
  // kept the last published head, which a tracker write the watcher had not
  // exported is missing - so neither is a state to record, and both are refused
  // with the ways out instead (skills-v0cn).
  const proved = marker && marker.pair && typeof marker.pair === 'object'
    && typeof marker.pair.signal === 'string' && marker.pair.signal
    && typeof marker.pair.commit === 'string' && marker.pair.commit === marker.commit;
  const oid = proved && typeof marker.commit === 'string' && /^[0-9a-f]{40,64}$/.test(marker.commit) ? marker.commit : null;
  if (!oid) return null;
  if ((await git(['cat-file', '-e', `${oid}^{commit}`], { cwd: CURRENT.store })).code !== 0)
    die(1, `the timeout marker of ${String(revision).slice(0, 12)} names store commit ${oid.slice(0, 12)}, which this store does not hold: the store ref's history is append-only, so the state that revision had is not guessed from a later one`);
  if ((await git(['merge-base', '--is-ancestor', oid, await headOid()], { cwd: CURRENT.store })).code !== 0)
    die(1, `the timeout marker of ${String(revision).slice(0, 12)} names store commit ${oid.slice(0, 12)}, which is not in the store head's history, so that state is not that revision's`);
  return oid;
}

/** What a person can do about a marker that holds no state, in plain words. */
const noSnapshotWaysOut = (rev, unproved = null) => `${unproved
  ? `the marker kept store commit ${String(unproved).slice(0, 12)}, which an earlier release wrote from the last published state: whether that is exactly this revision's state was never proved, and a state that is not proved is not recorded, so`
  : 'its state at that commit was never captured, and a later state is not guessed to be it:'} make a new commit (or amend this one) and its hook records that fresh revision, or sync, if another machine has already recorded ${String(rev || 'that revision').slice(0, 12)}`;
// A revision's full name and a short one name the same marker: the hook writes
// the name git gives it, and a reader may ask with either.
function findRecordTimeout(flags, repoRoot, at) {
  const dir = recordTimeoutDir(flags, repoRoot);
  const want = String(at || '');
  if (!want || want.includes('/') || want.includes('..')) return null;
  let names = [];
  try { names = readdirSync(dir); } catch { return null; }
  // Exact first: the marker's name is the revision's full id, and `at` is read
  // exactly as the caller wrote it. An abbreviation that resolves to nothing is
  // the one case a prefix is allowed, and only when exactly one marker carries
  // it: two revisions that begin alike must never lend each other a marker.
  const ids = names.filter((n) => n.endsWith('.json')).map((n) => n.slice(0, -5));
  const exact = ids.filter((id) => id === want);
  const loose = exact.length ? exact : ids.filter((id) => id.startsWith(want) || want.startsWith(id));
  if (loose.length !== 1) return null;
  const name = `${loose[0]}.json`;
  try { return { file: join(dir, name), ...JSON.parse(readFileSync(join(dir, name), 'utf8')) }; } catch { return { file: join(dir, name), revision: loose[0] }; }
}
function writeRecordTimeout(flags, repoRoot, revision, said, commit, proof = null) {
  if (!revision) return null;
  const file = join(recordTimeoutDir(flags, repoRoot), `${revision}.json`);
  const pinned = /^[0-9a-f]{40,64}$/.test(String(commit || '')) ? String(commit) : null;
  const body = { revision, at: nowIso(), said: String(said || '').split('\n')[0] };
  // The state this revision had when the hook gave up, as the store commit whose
  // tree holds it, WITH the evidence that proved it: the export pair - the
  // database's signal, the commit it paired with that signal, and when - that was
  // standing before the hook looked at the database. A marker without that
  // evidence is a state that was never proved to be this revision's, whichever
  // release wrote it, and no later state may be recorded as it either. The
  // command is only a way back to a state that exists.
  const proved = pinned && proof && proof.commit === pinned && typeof proof.signal === 'string' && proof.signal;
  if (proved) {
    body.commit = pinned;
    body.pair = { signal: proof.signal, commit: pinned, at: new Date(proof.at).toISOString() };
    body.command = rerunRecordLine(flags, repoRoot, revision);
  }
  writeAtomic(file, `${JSON.stringify(body, null, 2)}\n`);
  return file;
}
function clearRecordTimeout(flags, repoRoot, revision) {
  const found = findRecordTimeout(flags, repoRoot, revision);
  if (!found) return false;
  try { rmSync(found.file, { force: true }); return true; } catch { return false; }
}
/** The command that redoes a recording a hook could not write. */
const rerunRecordLine = (flags, repoRoot, revision) =>
  `node ${quote(join(HERE, 'tracker-sync.mjs'))} record --revision ${revision} --checkout ${quote(CURRENT.repoRoot)} --state ${quote(machineDir(flags, repoRoot))} --recipe ${quote(CURRENT.recipe.path)}`;

/** A record a checkout's hook started: bounded, and never silent.
 *
 * Three attempts of at most HOOK_LOCK_WAIT_MS each, with a short pause between
 * them: about half a minute at worst, and never the two minutes a person's own
 * record may wait. A commit is not held by it in any case - the hook runs after
 * the commit exists - but a hook that waits minutes is a hook that runs into the
 * next one, so the bound is short and the outcome is a marker, not silence. */
const HOOK_LOCK_WAIT_MS = 8000;
const HOOK_RECORD_ATTEMPTS = 3;
// The timeout path does NOT wait for the pair an export under way is about to
// write, though it could: the export reads the database a moment after it reads
// the signal, so a pair written after the hook's own look can hold a tracker
// write made after it, and a state later than the commit's own is exactly what
// this mechanism must never record. Measured on the sequence in
// test/tracker-recording-recovery ('a blocked watcher's export ...'): the export
// a blocked watcher owes lands 0.4-2 s after the store is released, and the wait
// would catch it - but only by giving up the proof, so the marker keeps no commit
// instead and the redo says how to get out.
async function recordWithRetries(opts) {
  HOOK_RECORD = true;
  LOCK_WAIT_MS = HOOK_LOCK_WAIT_MS;
  // Both read once, before any wait, and both lock-free: the revision this hook
  // is about, and the store commit whose tree is the state it had when the commit
  // was cut. A ref read takes no lock, and this is the closest lock-free point to
  // the commit, so a marker written below can never name a state later than the
  // commit's own.
  const rev = await canonicalRevision(await recordedRevision(opts), opts.checkout ? resolve(opts.checkout) : CURRENT.repoRoot);
  // The database's own change signal, read before any lock wait and lock-free,
  // beside the revision: the marker names a store commit only when the head is
  // exactly the state this value describes (see exactStateCommit). A recipe that
  // declares no signal, and a read that failed, both land here as null, and null
  // proves nothing for ever.
  const readAt = Date.now();
  const signalPaths = changeSignalPaths();
  const signalAtStart = signalPaths ? readChangeSignal(signalPaths) : null;
  const pinned = { ...opts, resolvedRevision: rev };
  let last = null;
  for (let attempt = 1; attempt <= HOOK_RECORD_ATTEMPTS; attempt++) {
    try {
      await recordRun(pinned);
      return;
    } catch (e) {
      if (!e.lockTimeout) throw e;
      last = e;
      if (attempt < HOOK_RECORD_ATTEMPTS) {
        say(`tracker-sync: the store is locked (${e.message.split(';')[0]}); attempt ${attempt} of ${HOOK_RECORD_ATTEMPTS} for this recording, trying again`);
        await sleep(2000);
      }
    }
  }
  // Two things a marker must never do: replace an older marker of the same
  // revision - the first one is the evidence closest to the commit, and a later
  // timeout could only name a later state - or outlive a recording another run
  // has already made. Both are checked here and again after the write, because a
  // marker is written without the store lock the record holds.
  // What the head is exactly, or null: the commit the last export paired with the
  // signal this hook found standing, when that pair was already there before this
  // hook looked (see exactStateCommit).
  const exact = await exactStateCommit(signalAtStart, readAt);
  const storeCommit = exact ? exact.commit : null;
  const before = rev ? findRecordTimeout(CURRENT.flags, CURRENT.repoRoot, rev) : null;
  const recorded = rev ? existsSync(recordingPathOf(rev)) : false;
  const file = before ? before.file : recorded ? null : writeRecordTimeout(CURRENT.flags, CURRENT.repoRoot, rev, last.message, storeCommit, exact);
  const kept = before && typeof before.commit === 'string' ? before.commit : storeCommit;
  if (file && existsSync(recordingPathOf(rev))) rmSync(file, { force: true });
  die(1, `the recording of ${rev ? String(rev).slice(0, 12) : 'this revision'} timed out on the store lock after ${HOOK_RECORD_ATTEMPTS} attempts (${last.message.split(';')[0]}); `
    + `${file || before
      ? `a marker is kept at ${file || before.file}, so the next push names the timeout instead of a missing recording; ${kept
        ? `it names store commit ${kept.slice(0, 12)}, the state this revision had when it was cut, and running record again records exactly that state`
        : 'the state this revision had was not captured, so record will refuse to write a later state as it'}`
      : recorded
        ? 'it is recorded now, so nothing is missing'
        : `${kept ? `the state was at store commit ${kept.slice(0, 12)}; a person must rerun record` : 'a person must rerun record'}`}`);
}
/** The revision this run is about, read the way record reads it. */
async function recordedRevision(opts) {
  if (opts.revision) return String(opts.revision);
  const ck = opts.checkout ? resolve(opts.checkout) : CURRENT.repoRoot;
  const r = await git(['rev-parse', 'HEAD'], { cwd: ck });
  return r.code === 0 ? r.stdout.trim() : null;
}

// ---- connect, status, export, watch ----------------------------------------

/** The record step a checkout's own hook needs, said so a person can put it in a
 * hook this mechanism must not write to itself. */
const recordLine = (flags, repoRoot, checkout) =>
  `node ${quote(join(HERE, 'tracker-sync.mjs'))} record --hook --revision "$(git rev-parse HEAD)" --checkout "$(git rev-parse --show-toplevel)" --state ${quote(machineDir(flags, repoRoot))} --recipe ${quote(CURRENT.recipe.path)}`;

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
    // The bytes are kept beside the decoded text: an upgrade of an older
    // generated command splices bytes, because a hook may carry comments or
    // commands that are not valid UTF-8 and a decode-and-rewrite would replace them.
    const bytes = existsSync(file) && notRegularFile(file) === null ? readFileSync(file) : null;
    const text = bytes === null ? null : bytes.toString('utf8');
    return { name, file, line: files[name], mark: `${HOOK_MARKS[name]}\n`, bytes, text, present: text !== null && text.includes(HOOK_MARKS[name]) };
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
    let upgraded = null;
    if (h.present) {
      // A hook that already carries the marker is normally left alone. The one
      // exception is the exact command an older release generated, installed
      // before `record --hook` existed: the marker says that line is ours, so
      // rerunning setup can bring it up to date. A marker alone never grants
      // ownership of code: only the exact old line, immediately after its own
      // marker, is replaced, and every other byte is kept.
      // A hooks folder inside the tree is the project's own files, and
      // post-checkout carries the relink line, which never changed.
      if (plan.inRepo || h.name === 'post-checkout') continue;
      // Matched and spliced as bytes, never as decoded shell text: a comment or
      // another command in the same hook may hold bytes that are not valid UTF-8,
      // and only the generated line may change.
      const legacy = Buffer.from(mark + line.replace(' record --hook --revision ', ' record --revision ').trimEnd());
      const replacement = Buffer.from(mark + line.trimEnd());
      const parts = [];
      let copied = 0;
      let at = h.bytes.indexOf(legacy);
      while (at !== -1) {
        const end = at + legacy.length;
        // Whole lines only: the same text inside a longer command is not a
        // generated line, and is not ours to change.
        if ((at === 0 || h.bytes[at - 1] === 10) && (end === h.bytes.length || h.bytes[end] === 10)) {
          parts.push(h.bytes.subarray(copied, at), replacement);
          copied = end;
        }
        at = h.bytes.indexOf(legacy, end);
      }
      if (!parts.length) continue;
      // A machine hook can be a symlink into the project's tracked files even
      // when its folder is git metadata: the file itself is resolved, and a
      // tracked target is never rewritten.
      if (await hooksInsideRepository(checkout, f)) continue;
      parts.push(h.bytes.subarray(copied));
      upgraded = Buffer.concat(parts);
    }
    if (h.text !== null) {
      // A hook that is not a POSIX shell script cannot carry this line: shell
      // appended to another language is a syntax error that stops every commit.
      const shebang = (h.text.split('\n')[0] || '').trim();
      if (shebang.startsWith('#!') && !/\/(sh|bash|dash|zsh|ksh)\b/.test(shebang)) {
        die(1, `${f} is not a shell hook (${shebang}): tracker-sync will not append shell to it. Call the step from that hook yourself: ${line.trim()}`);
      }
      if (upgraded !== null) {
        writeFileSync(f, upgraded);
        say(`tracker-sync: upgraded the generated ${h.name} record step in ${f}`);
        continue;
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
  const remoteNamed = await hasOrigin(repoRoot);
  if (!flags.offline && remoteNamed) {
    // Reachability is whether the remote answers at all. `--exit-code` makes git
    // exit 2 for "no matching ref", which says nothing about the remote: a bare
    // remote whose HEAD names a branch that does not exist (git init --bare
    // still defaulting to master while the project pushes main) answers
    // perfectly, and reading that as unreachable refused a remote connect can
    // use. The store ref's own absence is not unreachability either: connect
    // makes the store, and a remote with no store ref yet is the normal first
    // connect on a fresh remote.
    const any = await git(['ls-remote', 'origin'], { cwd: repoRoot, timeoutMs: 20000 });
    if (any.missing || any.timedOut || any.code !== 0) {
      // Nothing has been written yet, and nothing is: connect cannot tell a remote
      // that holds no store ref from one it cannot read, and guessing is how an
      // empty tracker appeared in a clone that could not reach the real one (#62).
      // --offline is offered only where it is honest: the clone already holds the
      // store's ref, so connecting it asks the remote for nothing it needs.
      const held = (await git(['rev-parse', '--verify', '--quiet', CURRENT.ref], { cwd: repoRoot })).code === 0;
      die(4, `the remote is unreachable (${text(any).split('\n')[0] || 'no answer'}), so connect cannot tell whether it holds ${CURRENT.ref} yet: nothing was written. Retry when the remote answers${held ? `, or pass --offline now: this clone already holds ${CURRENT.ref}, and --offline connects it without reading the remote` : ''}`);
    }
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
    // The ordinal is taken HERE, locally, and published last: nothing between this
    // commit and the push asks the remote for it, because nothing between them is
    // a push or a merge, and a push that fails must never leave the checkout
    // without its pointer, its hooks and its config (#62).
    let ordinalOid = null;
    if (CURRENT.recipe.ordinal) {
      await recipeOk('ordinal', { machine: quote(machineName) });
      ordinalOid = await commitState(`tracker store: this machine takes its ordinal`, [await headOid()]);
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
    // Only now the writes that leave this machine, and only after every local step
    // above: whether a checkout has its pointer, its hooks and its config never
    // depends on a push (#62).
    const leaving = [];
    if (ordinalOid) leaving.push([`this machine is named in the store's machine ordinals`, ordinalOid]);
    if (migrated.oid) leaving.push([`recordings this store held as whole copies now reference the state they record`, migrated.oid]);
    if (leaving.length) {
      if (!remoteNamed) {
        say('tracker-sync: this repository names no remote, so what connect wrote stays in this store until one is added');
      } else if (flags.offline) {
        say(`tracker-sync: --offline: ${leaving.length === 1 ? 'the store\'s new commit stays' : 'the store\'s new commits stay'} here, and the next publish carries ${leaving.length === 1 ? 'it' : 'them'} to the remote`);
      } else {
        let refused = null;
        let unreachable = null;
        for (const [sentence, oid] of leaving) {
          const res = await pushOid(oid, { quiet: true, allowRefusal: true });
          if (res.ok) say(`tracker-sync: ${sentence}`);
          else if (res.refused) refused = refused || res.said;
          else unreachable = unreachable || res.said || res.skipped || 'no answer';
        }
        // A remote that answers and refuses this clone's writes leaves a complete
        // connection: a clone without write rights is a legitimate clone, and it
        // is told once, in one sentence (#62).
        if (refused) say(`tracker-sync: ${refused}`);
        // Any other failure is still a failure, said after those local steps.
        else if (unreachable) die(4, `the remote stopped answering while connect published what it wrote (${String(unreachable).split('\n')[0]}): this checkout is connected, its hooks and its config are in place, and the store's new commits stay here`);
      }
    }
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
    // A clone whose writes the remote refuses is a legitimate clone, and status is
    // where a person asks. The mark connect and every later push leave is read
    // here and never re-probed: asking the host again would say the same thing.
    out.publishRefused = cfg.publishRefused || null;
    out.cannotPublish = !!cfg.publishRefused;
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
  say(`publish     ${out.publishRefused ? out.publishRefused.said
    : out.lastPublishOk === false ? 'the last publish did not land (the remote was unreachable)'
      : out.lastPublish ? `ok, ${out.lastPublish}` : 'nothing published yet'}`);
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
    if (!existsSync(f)) {
      // A hook's record that timed out on the store lock left a marker: the push
      // that finds no recording must name the timeout and the command that redoes
      // it, never "the store never recorded that revision", which is not what
      // happened and leaves the reader with no way to see it was a timeout (#57).
      const timedOut = findRecordTimeout(CURRENT.flags, CURRENT.repoRoot, await canonicalRevision(at));
      if (timedOut) {
        const when = `the recording of ${at} timed out at commit time: the store was locked (${timedOut.said || 'the lock was held elsewhere'}), and record gave up after its bounds at ${timedOut.at || 'commit time'}`;
        const pinned = await timeoutPinnedState(CURRENT.flags, CURRENT.repoRoot, await canonicalRevision(at));
        die(1, pinned
          ? `${when}; the state that revision had then is kept in store commit ${pinned.slice(0, 12)}, and no later state is recorded as it. Redo it with: ${timedOut.command || rerunRecordLine(CURRENT.flags, CURRENT.repoRoot, at)}`
          : `${when}; ${noSnapshotWaysOut(at, timedOut.commit)}`);
      }
      die(1, `no recording for ${at}: the store never recorded that revision, so its state is unknown and is not guessed from a later state`);
    }
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
  // The stop is also visible to the reads a tick is in: a git child signalled with
  // this process leaves a read that failed, and that is a stop - said as one, with
  // nothing committed - never a fact about the store (#skills-by6c).
  const onStop = () => { stop = true; STOPPING = true; };
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
  // The tracker's own change signal, for a recipe that declares one: with it,
  // a tick whose signal equals the one taken before the last export skips both
  // the store lock and the export, which is what makes an idle watcher cost
  // almost nothing and lets a record have the lock at once (#57).
  const sigPaths = changeSignalPaths();
  let lastSignal = null;      // the signal taken BEFORE the export of that tick
  let signalKnown = false;
  let idleMs = 1000;
  // How long the last tick waited before looking: the change it may have missed
  // by that much is aged from before it was seen, so the idle back-off cannot
  // push a publication past the bound a project set.
  let idleWaited = 0;
  const IDLE_MAX_MS = 5000;
  const publishLoop = async () => {
    while (!stop) {
      // A tick's signal is taken BEFORE anything is exported, and that value -
      // never the one after - is what the next tick is compared with. An outside
      // write that lands at any point of the export, even after the export read
      // the database and before the tick ended, is therefore part of the store's
      // next signal, which cannot equal the recorded one, so the next tick
      // exports again: such a write is delayed by one tick and never lost.
      // A tracker whose flush moves its own database on every run then exports
      // on every tick, which is what a watcher without a signal does: never
      // worse than before, and never a write folded away. One whose flush with
      // nothing to export moves nothing - measured on the shipped recipe,
      // "Nothing to export (no dirty issues)" - records the value it found and
      // goes idle from the next tick.
      const sig = sigPaths ? readChangeSignal(sigPaths) : null;
      if (sigPaths && sig !== null && signalKnown && sig === lastSignal && firstChange === null
          && !(await hasUncommittedState())) {
        idleMs = Math.min(IDLE_MAX_MS, idleMs + 1000);
        idleWaited = idleMs;
        await sleep(idleMs);
        continue;
      }
      idleMs = 1000;
      // The lock covers the store's own work only; the push is a network call
      // and happens with the lock released, so a claim or a record from another
      // session is never made to wait on the watcher's network.
      let digest = null;
      // What this tick's flush proved: the signal read under the lock immediately
      // before the flush, and either nothing to commit - so the head is that
      // state already - or the digest that is about to be committed as it.
      let exportSignalNow = null;
      let flushProved = false;
      let exported = false;
      try {
        digest = await locked(async () => {
          exportSignalNow = exportSignal();
          const flushed = await flushDatabase();
          // A flush the tracker refused does not prove the state files are the
          // database's, so this tick proves nothing even when it commits nothing.
          flushProved = !!(flushed && flushed.ok && !flushed.nothing && !flushed.merged);
          if (!(await hasUncommittedState())) {
            pairExportState(flushProved ? exportSignalNow : null, await headOid());
            return null;
          }
          return stateDigest();
        }, 20000, { skipOnTimeout: true });
        exported = true;
      } catch (e) {
        if (e.lockTimeout) { onLockTimeout(e); await sleep(1000); continue; }
        say(`tracker-sync: the export failed: ${e.message}`);
      }
      // What the next tick is compared with: the value taken BEFORE this export,
      // and no other. A round that could not take the lock records nothing, so it
      // tries again instead of believing it exported.
      if (sigPaths && exported && sig !== null) { lastSignal = sig; signalKnown = true; }
      if (digest === null) { firstChange = null; lastChange = null; lastDigest = ''; idleWaited = 0; await sleep(1000); continue; }
      const t = Date.now();
      if (digest !== lastDigest) {
        lastDigest = digest;
        lastChange = t;
        // The change may have landed up to one idle poll before this tick looked,
        // so the age that --max-age bounds starts where the poll last saw the
        // store unchanged: the bound covers the idle wait, never less than it.
        if (firstChange === null) firstChange = t - idleWaited;
      }
      const ready = (t - lastChange) / 1000 >= debounce || (t - firstChange) / 1000 >= maxAge;
      if (ready) {
        let oid;
        try {
          oid = await locked(async () => {
            // The commit is a second section, so the lock was free in between: the
            // pair is proved again under THIS one - the database has not moved
            // since the flush read it, and the state about to be committed is the
            // state that flush produced - and a tick that cannot prove both
            // commits without a claim instead of a false one.
            const proved = flushProved && exportSignalNow !== null && exportSignal() === exportSignalNow && stateDigest() === digest;
            return commitState(`tracker store: ${nowIso()}`, [await headOid()], { signal: proved ? exportSignalNow : null });
          }, 20000, { skipOnTimeout: true });
        } catch (e) {
          if (e.lockTimeout) { onLockTimeout(e); await sleep(1000); continue; }
          throw e;
        }
        const res = await pushOid(oid, { quiet: true, skipOnTimeout: true, allowRefusal: true });
        if (res.skipped) onLockTimeout(Object.assign(new Error(res.skipped), { lockTimeout: true }));
        writeConfig(flags, CURRENT.repoRoot, { lastPublish: nowIso(), lastPublishOk: !!res.ok });
        // A remote that refuses this clone's writes is said once, kept in the
        // config for status, and never asked again by this watcher: a retry
        // cannot change rights, and the loop stays up for the reads and records.
        if (res.refused && res.first) say(`tracker-sync: ${res.said} (the watcher keeps its reads and its records)`);
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
        if (r && r.pushed && r.pushed.refused && r.pushed.first) say(`tracker-sync: ${r.pushed.said} (the watcher keeps its reads and its records)`);
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
    '  record --revision <rev> [--checkout <path>] [--from-tree] [--hook]',
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
      // --watch: this round is the watcher's first tick, and a tick carries a
      // refusal instead of ending the watcher it was asked to start.
      const r = await publishOnce({ quiet: false, skipOnTimeout: !!flags.watch, allowRefusal: !!flags.watch });
      if (flags.watch) {
        if (r.skipped) say(`tracker-sync: another command holds the store lock (${r.skipped.split(';')[0]}); this round is skipped and tried again, the watcher stays up`);
        await watchRun(flags);
        break;
      }
      if (r.nothing) say('tracker-sync: nothing to publish');
      else if (r.ok) say(`tracker-sync: published ${String(r.oid).slice(0, 12)}`);
      else if (r.refused) say(`tracker-sync: ${r.said}`);
      else say('tracker-sync: the remote is unreachable; the write is kept here');
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
    case 'record': {
      const opts = { revision: flags.revision ? String(flags.revision) : null, checkout: flags.checkout ? String(flags.checkout) : null, fromTree: !!flags['from-tree'] };
      // --hook: the checkout's own post-commit and post-merge line. It is
      // bounded and it never fails silently (see recordWithRetries).
      if (flags.hook) await recordWithRetries(opts); else await recordRun(opts);
      break;
    }
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
