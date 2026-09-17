#!/usr/bin/env node
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
 * IT KNOWS NO TRACKER. Input is the normalized backlog described in model.md,
 * on stdin or as a path. Turning a tracker into that is an adapter's job and
 * the adapters are the only project-specific files here.
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

import { readFileSync } from 'node:fs';
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
              .map((b) => ({ id: i.id, note: `blocked by the idea ${b}` })),
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
              return age > cfg.staleDays ? [{ id: i.id, note: `blocked by ${b}, untouched ${age}d` }] : [];
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
              .map((b) => ({ id: i.id, note: `blocked by the deferred ${b}` })),
      ),
  },
  {
    id: 'off-milestone-open',
    severity: 'error',
    why: 'A milestone that is not current must be deferred, or the queue offers next quarter\'s feature as today\'s work. 802 issues were deferred in one pass for having been planned past that horizon.',
    fix: 'Defer it with its milestone label intact. It comes back whole when that milestone becomes current.',
    run: (m, cfg) =>
      m.issues.flatMap((i) => {
        if (!LIVE(i.status)) return [];
        const root = m.by.get(rootOf(m, i.id)) || i;
        const ls = root.labels || [];
        const other = ls.find((l) => cfg.milestoneLabels.includes(l) && l !== cfg.currentMilestone);
        return other && !ls.includes(cfg.currentMilestone)
          ? [{ id: i.id, note: `its root ${root.id} is ${other}, current is ${cfg.currentMilestone}` }]
          : [];
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
      ]);
      return m.issues
        .filter((i) => LIVE(i.status))
        .flatMap((i) =>
          (i.labels || [])
            .filter((l) => !known.has(l))
            .map((l) => ({ id: i.id, note: `label "${l}" is outside the vocabulary` })),
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

function evaluate(backlog, cfg, only) {
  const m = index(backlog);
  const now = Date.parse(backlog.generatedAt) || Date.now();
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
  };
}

function render(report) {
  const lines = [`gate — milestone ${report.milestone}, ${report.live} live of ${report.total}`, `source: ${report.source}`, ''];
  for (const r of report.results) {
    const mark = r.violations.length === 0 ? 'OK  ' : r.severity === 'error' ? 'FAIL' : 'WARN';
    lines.push(`${mark}  ${r.id}  (${r.violations.length})`);
    if (r.violations.length) {
      lines.push(`      why: ${r.why}`);
      lines.push(`      fix: ${r.fix}`);
      for (const v of r.violations.slice(0, 12)) lines.push(`      - ${v.id}: ${v.note}`);
      if (r.violations.length > 12) lines.push(`      … and ${r.violations.length - 12} more (--json for all)`);
    }
  }
  if (report.clusters.length) {
    lines.push('', 'AGES MAY BE CONTAMINATED — timestamp clusters found:');
    for (const c of report.clusters.slice(0, 5)) lines.push(`      ${c.count} live issues share ${c.minute}`);
    lines.push('      A bulk edit rewrites timestamps. Re-run the adapter against a revision from before it.');
  }
  return lines.join('\n');
}

/**
 * The portable half must not name what it happens to sit in. Twice during this
 * file's own authoring a project name reached the rules, so the guard is code
 * rather than a promise, and it draws two lines rather than one:
 *
 *   `projectWords`  never appear anywhere in the skill, SKILL.md included.
 *                   SKILL.md documents how to run the gate, never in which
 *                   repository it is running.
 *   `trackerWords`  never appear in the RULES — check.mjs, model.md, a
 *                   fixture — because a rule that knows a tracker has stopped
 *                   being portable. SKILL.md and adapters/ may name one: the
 *                   first documents the installed adapter, and naming a
 *                   tracker is the second's whole job.
 *
 * The project config is exempt from both. Describing one project is what it is.
 */
async function portability(projectConfigPath) {
  const { readdirSync, readFileSync: read } = await import('node:fs');
  let cfg;
  try {
    cfg = JSON.parse(read(projectConfigPath, 'utf8'));
  } catch {
    console.log('SKIP  portability: no project config to read projectWords from');
    return 0;
  }
  const rules = ['check.mjs', 'model.md', 'fixtures/config.json'];
  for (const kind of ['bad', 'good']) {
    for (const n of readdirSync(join(HERE, 'fixtures', kind))) rules.push(`fixtures/${kind}/${n}`);
  }
  const bands = [
    { words: cfg.projectWords || [], files: [...rules, 'SKILL.md'], what: 'the project' },
    { words: cfg.trackerWords || [], files: rules, what: 'a tracker' },
  ];
  if (!bands.some((b) => b.words.length)) {
    console.log('SKIP  portability: the project config declares no projectWords or trackerWords');
    return 0;
  }
  let failures = 0;
  let checked = 0;
  for (const band of bands) {
    for (const f of band.files) {
      let text;
      try {
        text = read(join(HERE, f), 'utf8').toLowerCase();
      } catch {
        continue;
      }
      for (const w of band.words) {
        checked++;
        if (text.includes(w.toLowerCase())) {
          console.log(`FAIL  portability: ${f} names ${band.what} ("${w}")`);
          failures++;
        }
      }
    }
  }
  if (!failures) console.log(`PASS  portability: nothing portable names the project or a tracker (${checked} checks)`);
  return failures;
}

async function runSelftest(cfg, projectConfigPath) {
  const { readdirSync } = await import('node:fs');
  const dir = join(HERE, 'fixtures');
  let failures = await portability(projectConfigPath);
  for (const name of readdirSync(join(dir, 'bad')).sort()) {
    const backlog = JSON.parse(readFileSync(join(dir, 'bad', name), 'utf8'));
    // A fixture fires the check it is named for, and MAY declare others it
    // necessarily fires with it: `idea-blocks-work` cannot be built without
    // also tripping `blocked-by-deferred`, because an idea belongs deferred.
    // Declaring the set is what keeps a rule from quietly widening — a check
    // that starts firing on a fixture it did not declare fails here.
    const expected = (backlog.expect || [name.replace(/\.json$/, '')]).slice().sort();
    const report = evaluate(backlog, cfg, null);
    const fired = report.results.filter((r) => r.violations.length).map((r) => r.id).sort();
    const ok = fired.join(',') === expected.join(',');
    console.log(`${ok ? 'PASS' : 'FAIL'}  bad/${name} → [${fired.join(', ') || 'nothing'}]${ok ? '' : `  expected [${expected.join(', ')}]`}`);
    if (!ok) failures++;
  }
  for (const name of readdirSync(join(dir, 'good')).sort()) {
    const backlog = JSON.parse(readFileSync(join(dir, 'good', name), 'utf8'));
    const report = evaluate(backlog, cfg, null);
    const fired = report.results.filter((r) => r.violations.length).map((r) => r.id);
    const ok = fired.length === 0;
    console.log(`${ok ? 'PASS' : 'FAIL'}  good/${name} → [${fired.join(', ') || 'nothing'}]`);
    if (!ok) failures++;
  }
  const covered = new Set(readdirSync(join(dir, 'bad')).map((n) => n.replace(/\.json$/, '')));
  for (const c of CHECKS) {
    if (!covered.has(c.id)) {
      console.log(`FAIL  no fixture for check ${c.id}`);
      failures++;
    }
  }
  console.log(failures ? `\n${failures} failure(s)` : '\nall fixtures pass');
  return failures ? 1 : 0;
}

async function main() {
  const argv = process.argv.slice(2);
  const args = { json: false, only: null, input: null, selftest: false, config: join(HERE, 'config.json') };
  for (let i = 0; i < argv.length; i++) {
    if (argv[i] === '--json') args.json = true;
    else if (argv[i] === '--only') args.only = argv[++i].split(',');
    else if (argv[i] === '--config') args.config = argv[++i];
    else if (argv[i] === '--selftest') args.selftest = true;
    else if (argv[i] === '--help' || argv[i] === '-h') args.help = true;
    else args.input = argv[i];
  }
  if (args.help) {
    console.log(
      'check.mjs [<normalized.json>|-] [--config <path>] [--only <id,id>] [--json]\n' +
        'check.mjs --selftest\n\n' +
        'Applies the backlog invariants to the normalized backlog on stdin or at <path>.\n' +
        'Exit 0 when no error-severity check fires, 1 otherwise.\n\n' +
        'Checks: ' + CHECKS.map((c) => c.id).join(', '),
    );
    return 0;
  }
  // The fixtures carry their own config: a self-test that used the project's
  // would pass or fail depending on which project the gate happens to sit in,
  // which is the opposite of what it is for.
  if (args.selftest)
    return runSelftest(JSON.parse(readFileSync(join(HERE, 'fixtures', 'config.json'), 'utf8')), args.config);
  const cfg = JSON.parse(readFileSync(args.config, 'utf8'));

  const raw = !args.input || args.input === '-' ? readFileSync(0, 'utf8') : readFileSync(args.input, 'utf8');
  const report = evaluate(JSON.parse(raw), cfg, args.only);
  console.log(args.json ? JSON.stringify(report, null, 2) : render(report));
  return report.results.some((r) => r.severity === 'error' && r.violations.length) ? 1 : 0;
}

process.exit(await main());
