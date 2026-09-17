#!/usr/bin/env node
// VENDORED, NEVER EDITED. Copied byte for byte from the shady2k-skills plugin
// (github.com/shady2k/skills, 0.2.0, skills/backlog/setup-shady2k-skills) by
// its own installer. Delete these five lines and the rest is that file: the
// rules are the same everywhere, and what this project is lives in config.json
// and adapter.mjs beside it. To change a rule, change it there and re-vendor.
/**
 * The backlog gate — the checks that keep a "ready" queue an answer rather
 * than a dump.
 *
 * Measured on 2026-09-17 in the repository that bought these rules: 933 issues
 * open, 773 of them reported ready to work, spread over 665 independent roots;
 * 82 of the 83 in progress had not been touched in over two days; 399 open
 * items had not been touched in thirty. Nothing in that state can answer "what
 * do I work on next", so filing became cheaper than searching, and every issue
 * filed made the next search worse. Each check below is one rule that grooming
 * had to invent on the spot, and each carries the case that bought it, because
 * a rule whose reason is invisible is a rule somebody deletes.
 *
 * IT KNOWS NO TRACKER AND NO PROJECT. Input is the normalized backlog described
 * in model.md, on stdin or as a path, and a config naming the project's
 * vocabulary. Both the adapter that produces the first and the second itself
 * live in the project, never beside this file: a rule that travels with a
 * project's words has stopped being a rule and become that project's script.
 *
 * HOW HARD IT BINDS IS THE PROJECT'S CHOICE, one of three, and never silence:
 *
 *   block      any error-severity violation fails.
 *   block-new  only a violation ABSENT FROM THE BASELINE fails; the rest is
 *              debt, printed every time. Both sides are aged against the same
 *              clock, so an issue that merely grew a day older overnight is
 *              not something this commit did.
 *   report     nothing fails. The report is still printed: a gate may be
 *              switched off, its report may not, or in a month there is a red
 *              gate nobody remembers.
 *
 * WHAT IT CANNOT SEE:
 *
 *   - AGE IS A LIE AFTER A BULK EDIT, and this is the blind spot that bit
 *     hardest. Deferring 802 issues rewrites 802 timestamps, and the next
 *     analysis reported epics untouched for 45 days as active today, because
 *     they were — by a status change nobody would call work. So the report
 *     always names timestamp CLUSTERS and refuses to be quiet about them. It
 *     cannot tell a bulk edit from a busy hour; it can only say which minutes
 *     to distrust, and let the adapter be re-run against an earlier revision.
 *   - IT CANNOT SEE A MISSING FEATURE. Every check is a statement about issues
 *     that exist. An epic nobody filed, a stage with no issue, a criterion
 *     that holds but is unwritten: all invisible.
 *   - `epic-without-criterion` IS A TEXT SEARCH for a heading or a phrase. An
 *     epic stating its criterion in prose is reported; an epic with the
 *     heading and nothing under it is not. It reports a missing SIGNAL.
 *   - IT CANNOT FIND A DUPLICATE. That check belongs to whoever is about to
 *     file, because duplicates are rarely near-copies: a split-panes epic was
 *     filed twice because the searches were "split", "pane" and
 *     "panes" while the existing issue was titled "Drag one tab onto another
 *     and watch both at once".
 *   - IT JUDGES NO SCOPE. Whether the current milestone is the right one, and
 *     whether a criterion is a good criterion, belong to the owner.
 */

import { readdirSync, readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = dirname(fileURLToPath(import.meta.url));

const CRITERION_MARKERS = [
  '## success criteria',
  '## done when',
  'done when',
  'falsified if',
  '## acceptance criteria',
];

const LIVE = (s) => s === 'open' || s === 'active';

// ---------------------------------------------------------------- model

function index(backlog) {
  const by = new Map(backlog.issues.map((i) => [i.id, i]));
  const children = new Map();
  for (const i of backlog.issues) {
    if (!i.parent) continue;
    if (!children.has(i.parent)) children.set(i.parent, []);
    children.get(i.parent).push(i.id);
  }
  return { issues: backlog.issues, by, children, generatedAt: backlog.generatedAt };
}

function rootOf(m, id) {
  let cur = id;
  const seen = new Set();
  while (m.by.get(cur)?.parent && !seen.has(cur)) {
    seen.add(cur);
    cur = m.by.get(cur).parent;
  }
  return cur;
}

function subtree(m, id, seen = new Set()) {
  const out = [];
  for (const k of m.children.get(id) || []) {
    if (seen.has(k)) continue;
    seen.add(k);
    out.push(k, ...subtree(m, k, seen));
  }
  return out;
}

const isIdea = (cfg, i) =>
  (i.labels || []).some((l) => cfg.ideaLabels.includes(l)) ||
  cfg.ideaTitlePrefixes.some((p) => (i.title || '').startsWith(p));

const hasCriterion = (i) => {
  const b = (i.body || '').toLowerCase();
  return CRITERION_MARKERS.some((mk) => b.includes(mk));
};

// ---------------------------------------------------------------- checks

const CHECKS = [
  {
    id: 'idea-blocks-work',
    severity: 'error',
    why: 'An idea that blocks a build is not an idea, it is an undecided question, and it belongs in the work it is blocking. Five brainstorms were found holding live epics.',
    fix: 'Either the question is on the path — file it as work under the epic it gates — or it is not, and the edge goes.',
    run: (m, cfg) =>
      m.issues.flatMap((i) =>
        !LIVE(i.status) || isIdea(cfg, i)
          ? []
          : (i.blockedBy || [])
              .filter((b) => m.by.get(b) && isIdea(cfg, m.by.get(b)))
              .map((b) => ({ id: i.id, ref: b, note: `blocked by the idea ${b}` })),
      ),
  },
  {
    id: 'idea-in-queue',
    severity: 'error',
    why: 'An idea reachable as work is offered as work. Three reached the queue through provenance edges, which gate nothing and so never stopped them.',
    fix: 'Defer it. An idea costs nothing while it waits and is closed without regret when it stops being interesting.',
    run: (m, cfg) =>
      m.issues
        .filter((i) => LIVE(i.status) && isIdea(cfg, i))
        .map((i) => ({ id: i.id, note: `status ${i.status}; an idea belongs deferred` })),
  },
  {
    id: 'stale-edge',
    severity: 'warn',
    why: 'A blocking edge records a collision — two things touching the same code — never "later", because nothing expires one. One epic held live work for 45 days while sharing no file with it.',
    fix: 'Remove the edge, or move it onto the leaf that actually collides. If the blocker is real, it belongs in the slice.',
    run: (m, cfg, ctx) =>
      m.issues.flatMap((i) =>
        !LIVE(i.status)
          ? []
          : (i.blockedBy || []).flatMap((b) => {
              const blocker = m.by.get(b);
              if (!blocker || !LIVE(blocker.status)) return [];
              const age = ctx.age(blocker);
              return age > cfg.staleDays ? [{ id: i.id, ref: b, note: `blocked by ${b}, untouched ${age}d` }] : [];
            }),
      ),
  },
  {
    id: 'blocked-by-deferred',
    severity: 'error',
    why: 'An edge onto a deferred issue blocks for ever and silently: nothing will move the blocker, and the queue simply stops offering the blocked one.',
    fix: 'Pull the blocker into the slice, or accept that the blocked issue is out of it too and defer that as well.',
    run: (m) =>
      m.issues.flatMap((i) =>
        !LIVE(i.status)
          ? []
          : (i.blockedBy || [])
              .filter((b) => m.by.get(b)?.status === 'deferred')
              .map((b) => ({ id: i.id, ref: b, note: `blocked by the deferred ${b}` })),
      ),
  },
  {
    id: 'off-milestone-open',
    severity: 'error',
    why: 'Everything live belongs to the current milestone; whatever does not is deferred. Otherwise the queue offers next quarter\'s feature as today\'s work, and a parentless issue filed by hand is how a groomed backlog grows back into 665 roots while every other check stays green. 802 issues were deferred in one pass for having been planned past that horizon.',
    fix: 'Defer it, with its own milestone label intact if it has one, so it comes back whole. If it is on the current milestone\'s path, give it the parent it serves.',
    run: (m, cfg) =>
      m.issues.flatMap((i) => {
        if (!cfg.currentMilestone || !LIVE(i.status) || isIdea(cfg, i)) return [];
        const root = m.by.get(rootOf(m, i.id)) || i;
        const ls = root.labels || [];
        if (ls.includes(cfg.currentMilestone)) return [];
        const other = ls.find((l) => cfg.milestoneLabels.includes(l));
        return [{ id: i.id, note: `its root ${root.id} ${other ? `is ${other}` : 'carries no milestone'}, current is ${cfg.currentMilestone}` }];
      }),
  },
  {
    id: 'stale-hold',
    severity: 'error',
    why: 'Active means somebody is holding it now. An unheld issue left active is invisible to the ready queue and to every colleague looking for work; 82 of 83 were stale.',
    fix: 'Set it back to open the minute you stop holding it.',
    run: (m, cfg, ctx) =>
      m.issues.flatMap((i) => {
        if (i.status !== 'active') return [];
        const tree = [i.id, ...subtree(m, i.id)].map((id) => m.by.get(id)).filter(Boolean);
        const freshest = Math.min(...tree.map(ctx.age));
        return freshest > cfg.holdDays ? [{ id: i.id, note: `nothing in its tree moved for ${freshest}d` }] : [];
      }),
  },
  {
    id: 'epic-without-criterion',
    severity: 'warn',
    why: 'An epic with no criterion that stops being false exactly once becomes an area of code wearing an epic\'s clothes, and absorbs every new bug in its area until it can never finish.',
    fix: 'Name in one sentence what somebody can do that they could not before, and what would falsify it.',
    run: (m) =>
      m.issues
        .filter((i) => LIVE(i.status) && i.type === 'epic' && !hasCriterion(i))
        .map((i) => ({ id: i.id, note: 'no DONE WHEN / Success Criteria / Falsified if' })),
  },
  {
    id: 'label-vocabulary',
    severity: 'warn',
    why: 'A vocabulary enforced only by prose drifts. One repository declared a closed list in its contract and held about seventy labels in the tree.',
    fix: 'Map it onto a declared label, or add it to the config deliberately.',
    run: (m, cfg) => {
      const known = new Set([
        ...cfg.areaLabels,
        ...cfg.milestoneLabels,
        ...cfg.roadmapLabels,
        ...cfg.triageLabels,
        ...cfg.ideaLabels,
        ...(cfg.findingLabels || []),
      ]);
      return m.issues
        .filter((i) => LIVE(i.status))
        .flatMap((i) =>
          (i.labels || [])
            .filter((l) => !known.has(l))
            .map((l) => ({ id: i.id, ref: l, note: `label "${l}" is outside the vocabulary` })),
        );
    },
  },
  {
    id: 'area-label',
    severity: 'warn',
    why: 'Exactly one area label, by the area that OWNS the behaviour. None makes it unfindable; two usually means it is two issues.',
    fix: 'Label by the area that owns the behaviour, not every area it touches. If two genuinely own it, file two.',
    run: (m, cfg) =>
      m.issues.flatMap((i) => {
        if (!LIVE(i.status)) return [];
        const areas = (i.labels || []).filter((l) => cfg.areaLabels.includes(l));
        return areas.length === 1
          ? []
          : [{ id: i.id, note: areas.length ? `${areas.length} area labels: ${areas.join(', ')}` : 'no area label' }];
      }),
  },
  {
    id: 'finding-budget',
    severity: 'error',
    why: 'Bugs and debt found mid-milestone go to the front one at a time, each reasonable, and push the feature out by a fortnight nobody decided on. The charter declares how many the milestone absorbs.',
    fix: 'Take it to the owner: it goes to the next milestone, deferred, or it displaces something that is named and deferred with it. Raising the budget is a charter change, made on purpose.',
    run: (m, cfg) => {
      if (cfg.findingBudget == null) return [];
      const marks = cfg.findingLabels || [];
      const spent = m.issues.filter((i) => {
        if (i.status === 'deferred' || !(i.labels || []).some((l) => marks.includes(l))) return false;
        const root = m.by.get(rootOf(m, i.id)) || i;
        return [...(i.labels || []), ...(root.labels || [])].includes(cfg.currentMilestone);
      });
      return spent.length <= cfg.findingBudget
        ? []
        : spent.map((i) => ({ id: i.id, note: `one of ${spent.length} findings against a budget of ${cfg.findingBudget}` }));
    },
  },
  {
    id: 'parent-cycle',
    severity: 'error',
    why: 'A cycle in the parent chain makes every rollup and every ancestor walk non-terminating. Cheap to check and impossible to see by eye.',
    fix: 'Break the cycle: one of those issues is not the parent of the other.',
    run: (m) =>
      m.issues.flatMap((i) => {
        let cur = i.id;
        const seen = new Set([cur]);
        while (m.by.get(cur)?.parent) {
          cur = m.by.get(cur).parent;
          if (seen.has(cur)) return [{ id: i.id, note: `parent chain revisits ${cur}` }];
          seen.add(cur);
        }
        return [];
      }),
  },
];

function bulkClusters(m, threshold) {
  const byMinute = new Map();
  for (const i of m.issues) {
    if (i.status === 'closed') continue;
    const minute = (i.updatedAt || '').slice(0, 16);
    byMinute.set(minute, (byMinute.get(minute) || 0) + 1);
  }
  return [...byMinute.entries()]
    .filter(([, n]) => n >= threshold)
    .sort((a, b) => b[1] - a[1])
    .map(([minute, count]) => ({ minute, count }));
}

// ---------------------------------------------------------------- run

const STRENGTHS = ['block', 'block-new', 'report'];

/**
 * A bulk edit rewrites the timestamps it touches, so after one the backlog's
 * own ages describe the edit. `agesFrom` is a normalized backlog saved BEFORE
 * it: statuses, edges and labels are read from the current one, last-modified
 * from the old one wherever the issue already existed.
 */
function withAgesFrom(backlog, agesFrom) {
  if (!agesFrom) return backlog;
  const old = new Map(agesFrom.issues.map((i) => [i.id, i.updatedAt]));
  return {
    ...backlog,
    source: `${backlog.source || '(unnamed)'}; ages from ${agesFrom.source || 'a snapshot'}`,
    issues: backlog.issues.map((i) => (old.has(i.id) ? { ...i, updatedAt: old.get(i.id) } : i)),
  };
}

function evaluate(backlog, cfg, only, clock) {
  const m = index(backlog);
  const now = clock || Date.parse(backlog.generatedAt) || Date.now();
  const ctx = { age: (i) => Math.floor((now - Date.parse(i.updatedAt)) / 86400000) };
  const selected = only ? CHECKS.filter((c) => only.includes(c.id)) : CHECKS;
  return {
    milestone: cfg.currentMilestone,
    source: backlog.source || '(unnamed)',
    live: m.issues.filter((i) => LIVE(i.status)).length,
    total: m.issues.length,
    results: selected.map((c) => ({
      id: c.id,
      severity: c.severity,
      why: c.why,
      fix: c.fix,
      violations: c.run(m, cfg, ctx),
    })),
    clusters: bulkClusters(m, cfg.bulkCluster),
    now,
  };
}

/**
 * Marks each violation new or old against a baseline report, and decides the
 * exit. A violation is the same one when its check, its issue and the other
 * party to it (`ref`: the blocker, the label) are the same — never its note,
 * which carries an age and so changes every midnight.
 */
function judge(report, baseline, strength) {
  const known = new Set(
    (baseline?.results || []).flatMap((r) => r.violations.map((v) => `${r.id}|${v.id}|${v.ref || ''}`)),
  );
  for (const r of report.results)
    for (const v of r.violations) v.isNew = !baseline || !known.has(`${r.id}|${v.id}|${v.ref || ''}`);
  report.strength = strength;
  report.compared = Boolean(baseline);
  const failing = report.results
    .filter((r) => r.severity === 'error')
    .flatMap((r) => r.violations.filter((v) => strength === 'block' || v.isNew));
  // What the skills read. `failed` depends on the strength and is never true
  // under `report`; "did this change make the backlog worse" must not.
  report.newErrors = report.results
    .filter((r) => r.severity === 'error')
    .reduce((n, r) => n + r.violations.filter((v) => v.isNew).length, 0);
  report.failed = strength !== 'report' && failing.length > 0;
  return report;
}

function render(report) {
  const lines = [
    `gate (${report.strength}) — milestone ${report.milestone}, ${report.live} live of ${report.total}`,
    `source: ${report.source}`,
    '',
  ];
  for (const r of report.results) {
    const mark = r.violations.length === 0 ? 'OK  ' : r.severity === 'error' ? 'FAIL' : 'WARN';
    const fresh = r.violations.filter((v) => v.isNew).length;
    lines.push(`${mark}  ${r.id}  (${r.violations.length}${report.compared ? `, ${fresh} new` : ''})`);
    if (r.violations.length) {
      lines.push(`      why: ${r.why}`);
      lines.push(`      fix: ${r.fix}`);
      const ordered = [...r.violations].sort((a, b) => Number(b.isNew) - Number(a.isNew));
      for (const v of ordered.slice(0, 12)) lines.push(`      - ${report.compared && v.isNew ? 'NEW ' : ''}${v.id}: ${v.note}`);
      if (r.violations.length > 12) lines.push(`      … and ${r.violations.length - 12} more (--json for all)`);
    }
  }
  if (report.clusters.length) {
    lines.push('', 'AGES MAY BE CONTAMINATED — timestamp clusters found:');
    for (const c of report.clusters.slice(0, 5)) lines.push(`      ${c.count} live issues share ${c.minute}`);
    lines.push('      A bulk edit rewrites timestamps. Re-run the adapter against a revision from before it.');
  }
  lines.push('', `new errors: ${report.newErrors}${report.compared ? '' : ' (no baseline: every violation counts as new)'}`);
  if (report.strength === 'report') lines.push('strength is `report`: nothing here fails anything. It is still true.');
  else lines.push(report.failed ? 'RED' : 'green');
  return lines.join('\n');
}

/**
 * The skill must not name what it happens to sit in. Twice during this file's
 * own authoring a project name reached the rules, so the guard is code rather
 * than a promise. `projectWords` and `trackerWords` come from the PROJECT's
 * config — the skill cannot know them, which is the point — and neither may
 * appear in any file here: the rules, the fixtures, the model, SKILL.md. The
 * tracker is reached through the project's adapter and the project's own
 * tracker doc, never through anything that ships with the rules.
 *
 * Without a project config there is nothing to look for and it says so.
 */
function portability(projectConfigPath) {
  let cfg;
  try {
    cfg = JSON.parse(readFileSync(projectConfigPath, 'utf8'));
  } catch {
    console.log('SKIP  portability: no project config given (--config) to read projectWords from');
    return 0;
  }
  const words = [...(cfg.projectWords || []), ...(cfg.trackerWords || [])];
  if (!words.length) {
    console.log('SKIP  portability: the project config declares no projectWords or trackerWords');
    return 0;
  }
  const files = ['check.mjs', 'model.md', 'SKILL.md', 'backlog.md', 'fixtures/config.json'];
  for (const kind of ['bad', 'good'])
    for (const n of readdirSync(join(HERE, 'fixtures', kind))) files.push(`fixtures/${kind}/${n}`);
  let failures = 0;
  for (const f of files) {
    const text = readFileSync(join(HERE, f), 'utf8').toLowerCase();
    for (const w of words) {
      if (!text.includes(w.toLowerCase())) continue;
      console.log(`FAIL  portability: ${f} names the project or its tracker ("${w}")`);
      failures++;
    }
  }
  if (!failures) console.log(`PASS  portability: ${files.length} files name neither the project nor its tracker`);
  return failures;
}

function runSelftest(cfg, projectConfigPath) {
  const dir = join(HERE, 'fixtures');
  const load = (kind, name) => JSON.parse(readFileSync(join(dir, kind, name), 'utf8'));
  let failures = portability(projectConfigPath);
  const say = (ok, line) => {
    console.log(`${ok ? 'PASS' : 'FAIL'}  ${line}`);
    if (!ok) failures++;
  };
  for (const name of readdirSync(join(dir, 'bad')).sort()) {
    const backlog = load('bad', name);
    // A fixture fires the check it is named for, and MAY declare others it
    // necessarily fires with it: `idea-blocks-work` cannot be built without
    // also tripping `blocked-by-deferred`, because an idea belongs deferred.
    // Declaring the set is what keeps a rule from quietly widening — a check
    // that starts firing on a fixture it did not declare fails here.
    const expected = (backlog.expect || [name.replace(/\.json$/, '')]).slice().sort();
    const report = evaluate(backlog, cfg, null);
    const fired = report.results.filter((r) => r.violations.length).map((r) => r.id).sort();
    const ok = fired.join(',') === expected.join(',');
    say(ok, `bad/${name} → [${fired.join(', ') || 'nothing'}]${ok ? '' : `  expected [${expected.join(', ')}]`}`);
  }
  for (const name of readdirSync(join(dir, 'good')).sort()) {
    const report = evaluate(load('good', name), cfg, null);
    const fired = report.results.filter((r) => r.violations.length).map((r) => r.id);
    say(fired.length === 0, `good/${name} → [${fired.join(', ') || 'nothing'}]`);
  }
  const covered = new Set(readdirSync(join(dir, 'bad')).map((n) => n.replace(/\.json$/, '')));
  for (const c of CHECKS) if (!covered.has(c.id)) say(false, `no fixture for check ${c.id}`);

  // The three strengths, on one backlog that violates an error-severity rule.
  // Old debt is the same backlog as its own baseline; a new violation is that
  // backlog against a clean one. `report` is green in both and must still
  // have something to print.
  const dirty = load('bad', 'stale-hold.json');
  const clean = load('good', 'baseline.json');
  const verdict = (baseline, strength) => {
    const now = evaluate(dirty, cfg, null);
    return judge(now, baseline && evaluate(baseline, cfg, null, now.now), strength);
  };
  for (const [what, baseline, strength, red] of [
    ['old debt', dirty, 'block', true],
    ['old debt', dirty, 'block-new', false],
    ['a new violation', clean, 'block-new', true],
    ['a new violation', clean, 'report', false],
  ]) {
    const v = verdict(baseline, strength);
    const seen = v.results.some((r) => r.violations.length);
    say(v.failed === red && seen, `strength ${strength} on ${what} → ${v.failed ? 'red' : 'green'}, report ${seen ? 'kept' : 'LOST'}`);
  }
  // A bulk edit made every issue look touched today, which hides the stale
  // hold. Ages from the snapshot taken before it bring the violation back.
  const edited = { ...dirty, issues: dirty.issues.map((i) => ({ ...i, updatedAt: dirty.generatedAt })) };
  const fired = (b) => evaluate(b, cfg, ['stale-hold']).results[0].violations.length;
  say(fired(edited) === 0 && fired(withAgesFrom(edited, dirty)) > 0, 'ages-from: a stale hold hidden by a bulk edit is seen again through the snapshot');
  console.log(failures ? `\n${failures} failure(s)` : '\nall fixtures pass');
  return failures ? 1 : 0;
}

function main() {
  const argv = process.argv.slice(2);
  const args = { json: false, only: null, input: null, selftest: false, config: null, baseline: null, strength: null, agesFrom: null };
  for (let i = 0; i < argv.length; i++) {
    if (argv[i] === '--json') args.json = true;
    else if (argv[i] === '--only') args.only = argv[++i].split(',');
    else if (argv[i] === '--config') args.config = argv[++i];
    else if (argv[i] === '--baseline') args.baseline = argv[++i];
    else if (argv[i] === '--strength') args.strength = argv[++i];
    else if (argv[i] === '--ages-from') args.agesFrom = argv[++i];
    else if (argv[i] === '--selftest') args.selftest = true;
    else if (argv[i] === '--help' || argv[i] === '-h') args.help = true;
    else args.input = argv[i];
  }
  if (args.help) {
    console.log(
      'check.mjs --config <project config> [<normalized.json>|-] [--baseline <normalized.json>]\n' +
        '          [--strength block|block-new|report] [--ages-from <normalized.json>]\n' +
        '          [--only <id,id>] [--json]\n' +
        'check.mjs --selftest [--config <project config>]\n\n' +
        'Applies the backlog invariants to the normalized backlog on stdin or at <path>.\n' +
        'Strength comes from the config unless given; block-new needs --baseline.\n' +
        '--ages-from reads last-modified from a snapshot saved before a bulk edit.\n' +
        'Exit 0 green, 1 red, 2 misuse.\n\n' +
        'Checks: ' + CHECKS.map((c) => c.id).join(', '),
    );
    return 0;
  }
  // The fixtures carry their own config: a self-test that used the project's
  // would pass or fail depending on which project the gate happens to sit in,
  // which is the opposite of what it is for. The project's config is read for
  // one thing only — the words the skill may not contain.
  if (args.selftest)
    return runSelftest(JSON.parse(readFileSync(join(HERE, 'fixtures', 'config.json'), 'utf8')), args.config);

  const misuse = (why) => {
    console.error(`check.mjs: ${why}`);
    return 2;
  };
  if (!args.config) return misuse('--config <project config> is required; the rules carry no project of their own');
  const cfg = JSON.parse(readFileSync(args.config, 'utf8'));
  const strength = args.strength || cfg.strength;
  // No default. A strength nobody chose is a strength everybody has without
  // knowing it, and the quiet one of the three is indistinguishable from off.
  if (!STRENGTHS.includes(strength)) return misuse(`strength must be one of ${STRENGTHS.join(', ')}; got ${strength ?? 'none'}`);
  if (strength === 'block-new' && !args.baseline) return misuse('block-new needs --baseline: without one every violation is new');

  const read = (p) => JSON.parse(!p || p === '-' ? readFileSync(0, 'utf8') : readFileSync(p, 'utf8'));
  const ages = args.agesFrom ? read(args.agesFrom) : null;
  const report = evaluate(withAgesFrom(read(args.input), ages), cfg, args.only);
  const baseline = args.baseline ? evaluate(withAgesFrom(read(args.baseline), ages), cfg, args.only, report.now) : null;
  judge(report, baseline, strength);
  console.log(args.json ? JSON.stringify(report, null, 2) : render(report));
  return report.failed ? 1 : 0;
}

process.exit(main());
