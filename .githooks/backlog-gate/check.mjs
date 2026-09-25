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
 *     epic stating its criterion in prose is reported; an empty heading is
 *     also reported. A few words are a signal, not a proof of a good criterion.
 *   - IT CANNOT FIND A DUPLICATE. That check belongs to whoever is about to
 *     file, because duplicates are rarely near-copies: a split-panes epic was
 *     filed twice because the searches were "split", "pane" and
 *     "panes" while the existing issue was titled "Drag one tab onto another
 *     and watch both at once".
 *   - `stale-edge` MEASURES THE BLOCKER, NOT THE EDGE. Few trackers date an
 *     edge, so the model carries none, and a brand-new dependency on an old
 *     issue reads the same as an old one. It is a warning for that reason.
 *   - A RENAMED ID IS A NEW ISSUE. `block-new` matches a violation by check,
 *     issue id and the other party to it. A tracker that renumbers turns old
 *     debt into new errors once, and nothing here can know better.
 *   - IT JUDGES NO SCOPE. Whether the current milestone is the right one, and
 *     whether a criterion is a good criterion, belong to the owner.
 */

import { mkdtempSync, readdirSync, readFileSync, writeFileSync } from 'node:fs';
import { spawnSync } from 'node:child_process';
import { tmpdir } from 'node:os';
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

const LIVE = (s) => ['open', 'active', 'submitted', 'implemented'].includes(s);

// What an edge may not name. An epic is one by its kind; a parent is one while
// work is still open under it, because an edge onto it would serialize that
// work behind something it never needed. A leaf whose sub-task has closed is a
// leaf again: what remains is in the issue itself, so an edge onto it is as
// concrete as an edge ever was, and refusing it leaves the prerequisite in
// prose that nothing queries.
const isContainer = (m, i) =>
  !!i && (i.type === 'epic' || (m.children.get(i.id) || []).some((c) => LIVE(m.by.get(c)?.status)));

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

// A marker alone is a heading somebody meant to fill in. It counts when words
// follow it before the next heading.
const hasCriterion = (i) => {
  const b = (i.body || '').toLowerCase();
  return CRITERION_MARKERS.some((mk) => {
    const at = b.indexOf(mk);
    if (at < 0) return false;
    const after = b.slice(at + mk.length).split(/\n\s*#/)[0];
    return (after.match(/[\p{L}\p{N}]{2,}/gu) || []).length >= 3;
  });
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
    why: 'A dependency needs a required result or a conflict, not list order. Its age prompts a review but does not disprove a real prerequisite.',
    fix: 'Verify the required result or conflict. Remove an obsolete edge, or move it onto the actual consuming and producing leaves.',
    run: (m, cfg, ctx) =>
      m.issues.flatMap((i) =>
        !LIVE(i.status)
          ? []
          : (i.blockedBy || []).flatMap((b) => {
              const blocker = m.by.get(b);
              if (!blocker) return [{ id: i.id, ref: b, note: `blocked by ${b}, which does not exist` }];
              if (!LIVE(blocker.status)) return [];
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
        return [{ id: i.id, ref: root.id, note: `its root ${root.id} ${other ? `is ${other}` : 'carries no milestone'}, current is ${cfg.currentMilestone}` }];
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
        // `holder` is optional in the model. An adapter that emits it lets
        // this say so outright instead of inferring it from age.
        if (i.holder === null || i.holder === '') return [{ id: i.id, note: 'active, and nobody holds it' }];
        const tree = [i.id, ...subtree(m, i.id)].map((id) => m.by.get(id)).filter(Boolean);
        const freshest = Math.min(...tree.map(ctx.age));
        return freshest > cfg.holdDays ? [{ id: i.id, note: `nothing in its tree moved for ${freshest}d` }] : [];
      }),
  },
  {
    id: 'epic-without-criterion',
    severity: 'error',
    why: 'An epic with no criterion that stops being false exactly once becomes an area of code wearing an epic\'s clothes, and absorbs every new bug in its area until it can never finish.',
    fix: 'Name in one sentence what somebody can do that they could not before, and what would falsify it.',
    run: (m) =>
      m.issues
        .filter((i) => LIVE(i.status) && i.type === 'epic' && !hasCriterion(i))
        .map((i) => ({ id: i.id, note: 'no DONE WHEN / Success Criteria / Falsified if' })),
  },
  {
    id: 'label-vocabulary',
    severity: 'error',
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
    severity: 'error',
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
    fix: 'The ways that do the work are the owner\'s to give: displace named planned work and raise the budget in config and the charter decision, open the next milestone for it, or hold it knowingly with a review date. Deferring it yourself to make the number fit, dropping its milestone label or narrowing it shrinks the count instead, and leaves a record saying the fault is not there. A repair a required check forced on the way to a merge is not intake at all: file it under the work whose merge it blocked, with no finding label. Deferring planned work alone does not change this count.',
    run: (m, cfg) => {
      if (cfg.findingBudget == null) return [];
      const marks = cfg.findingLabels || [];
      const spent = m.issues.filter((i) => {
        if (i.status === 'deferred' || !(i.labels || []).some((l) => marks.includes(l))) return false;
        // Its OWN milestone label wins: a feature carried into the next
        // milestone must not charge the findings it already closed to the new
        // budget. Only a finding with no milestone of its own reads its root's.
        const own = (i.labels || []).filter((l) => cfg.milestoneLabels.includes(l));
        if (own.length) return own.includes(cfg.currentMilestone);
        return ((m.by.get(rootOf(m, i.id)) || i).labels || []).includes(cfg.currentMilestone);
      });
      // Only the overage is in violation, oldest findings first, so crossing
      // the budget by one is one new error and not the whole list.
      const order = (i) => `${i.createdAt || ''}|${i.id}`;
      return spent
        .sort((a, b) => (order(a) < order(b) ? -1 : 1))
        .slice(cfg.findingBudget)
        .map((i, n) => ({ id: i.id, note: `finding ${cfg.findingBudget + n + 1} of ${spent.length}, against a budget of ${cfg.findingBudget}` }));
    },
  },
  {
    id: 'dependency-cycle',
    severity: 'error',
    why: 'Live tasks that transitively wait for themselves can never become ready, even when the parent tree is valid.',
    fix: 'Correct the required-result or conflict edges; independent tasks and features must not be chained by list order.',
    run: (m) => m.issues.filter((i) => LIVE(i.status)).flatMap((i) => {
      const pending = [...(i.blockedBy || [])];
      const seen = new Set();
      while (pending.length) {
        const id = pending.pop();
        if (id === i.id) return [{ id: i.id, note: 'live dependencies lead back to this issue' }];
        if (seen.has(id)) continue;
        seen.add(id);
        const blocker = m.by.get(id);
        if (blocker && LIVE(blocker.status)) pending.push(...(blocker.blockedBy || []));
      }
      return [];
    }),
  },
  {
    id: 'nonleaf-dependency',
    severity: 'error',
    why: 'Blocking entire containers hides the concrete prerequisite and serializes unrelated work.',
    fix: 'Point it at the leaf whose result releases it, with the result or conflict it represents. Where a whole outcome is awaited, that leaf is the one putting the awaited thing in the person\'s hands; where it does not exist yet, write it first as that outcome\'s acceptance and point at it. Leaving the prerequisite in a comment instead is not the cheaper answer: what ready does not read, the next run does not know.',
    run: (m) => m.issues.filter((i) => LIVE(i.status)).flatMap((i) =>
      (i.blockedBy || []).filter((id) => isContainer(m, i) || isContainer(m, m.by.get(id)))
        .map((id) => ({ id: i.id, ref: id, note: 'dependency uses a container rather than a leaf' }))),
  },
  {
    id: 'submitted-without-evidence',
    severity: 'error',
    why: 'A worker result awaiting integration must remain recoverable without being offered for implementation again.',
    fix: 'Record the submitted result revision/location and local-check evidence, or keep genuinely unfinished work open.',
    run: (m) => m.issues.filter((i) => i.status === 'submitted' &&
      (i.type === 'epic' || m.children.has(i.id) || m.by.get(i.parent)?.type !== 'epic' ||
       typeof i.delivery?.revision !== 'string' || !i.delivery.revision.trim() ||
       typeof i.delivery?.evidence !== 'string' || !i.delivery.evidence.trim()))
      .map((i) => ({ id: i.id, note: 'submitted requires a leaf under a stage, result revision/location and local evidence' })),
  },
  {
    id: 'implemented-without-evidence',
    severity: 'error',
    why: 'Implemented work is unavailable to acceptance or downstream tasks without an integrated revision and related-check evidence.',
    fix: 'Record the integrated revision and local-check evidence under its stage, or reopen work that has not actually been integrated.',
    run: (m) => m.issues.filter((i) => i.status === 'implemented' &&
      (i.type === 'epic' || m.children.has(i.id) || m.by.get(i.parent)?.type !== 'epic' ||
       typeof i.integration?.revision !== 'string' || !i.integration.revision.trim() ||
       typeof i.integration?.evidence !== 'string' || !i.integration.evidence.trim()))
      .map((i) => ({ id: i.id, note: 'implemented requires a leaf under a stage, integrated revision and related-check evidence' })),
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

// The version of the set these rules shipped with. A project holds a COPY of
// this file, and this is how anybody tells that the copy has fallen behind.
const RULES_VERSION = '0.26.0';

const STRENGTHS = ['block', 'block-new', 'report'];

/** Thrown for anything that is the caller's mistake; main turns it into exit 2. */
class Misuse extends Error {}

const LIST_KEYS = ['areaLabels', 'milestoneLabels', 'roadmapLabels', 'triageLabels', 'ideaLabels', 'ideaTitlePrefixes', 'findingLabels'];
const NUMBER_KEYS = { staleDays: 14, holdDays: 2, bulkCluster: 20 };

/**
 * A gate that cannot read its input is red, never green, and says why in one
 * line. A missing list is an empty list; a missing threshold is its default;
 * anything of the wrong type is refused rather than guessed at.
 */
function checkedConfig(raw, where) {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) throw new Misuse(`${where}: the config is not a JSON object`);
  const cfg = { ...raw };
  for (const k of LIST_KEYS) {
    if (cfg[k] == null) cfg[k] = [];
    if (!Array.isArray(cfg[k]) || cfg[k].some((x) => typeof x !== 'string')) throw new Misuse(`${where}: ${k} must be a list of strings`);
  }
  for (const [k, dflt] of Object.entries(NUMBER_KEYS)) {
    if (cfg[k] == null) cfg[k] = dflt;
    if (!Number.isFinite(cfg[k]) || cfg[k] < 0) throw new Misuse(`${where}: ${k} must be a number, zero or more`);
  }
  if (cfg.findingBudget != null && !(Number.isInteger(cfg.findingBudget) && cfg.findingBudget >= 0))
    throw new Misuse(`${where}: findingBudget must be a whole number or null`);
  if (cfg.currentMilestone != null && !cfg.milestoneLabels.includes(cfg.currentMilestone))
    throw new Misuse(`${where}: currentMilestone "${cfg.currentMilestone}" is not in milestoneLabels`);
  return cfg;
}

function checkedBacklog(raw, where) {
  if (!raw || !Array.isArray(raw.issues)) throw new Misuse(`${where}: not a normalized backlog (no "issues" list); see model.md`);
  const seen = new Set();
  for (const i of raw.issues) {
    if (!i || typeof i.id !== 'string' || !i.id) throw new Misuse(`${where}: an issue has no id`);
    if (seen.has(i.id)) throw new Misuse(`${where}: the id ${i.id} appears twice`);
    seen.add(i.id);
    if (!['open', 'active', 'submitted', 'implemented', 'deferred', 'closed'].includes(i.status)) throw new Misuse(`${where}: ${i.id} has the status "${i.status}"; see model.md`);
    if (i.blockedBy != null && (!Array.isArray(i.blockedBy) || i.blockedBy.some((id) => typeof id !== 'string' || !id)))
      throw new Misuse(`${where}: ${i.id} blockedBy must be a list of ids`);
    // An unreadable date makes every age comparison false, which reads as "fresh".
    if (LIVE(i.status) && !Number.isFinite(Date.parse(i.updatedAt))) throw new Misuse(`${where}: ${i.id} has no readable updatedAt`);
  }
  return raw;
}

function readJson(path, what) {
  let text;
  try {
    text = !path || path === '-' ? readFileSync(0, 'utf8') : readFileSync(path, 'utf8');
  } catch (e) {
    throw new Misuse(`cannot read ${what} (${path || 'stdin'}): ${e.code || e.message}`);
  }
  try {
    return JSON.parse(text);
  } catch {
    throw new Misuse(`${what} (${path || 'stdin'}) is not JSON${text.trim() ? '' : ': it is empty'}`);
  }
}

/**
 * A bulk edit rewrites the timestamps it touches, so after one the backlog's
 * own ages describe the edit. `agesFrom` is a normalized backlog saved BEFORE
 * it: statuses, edges and labels are read from the current one, last-modified
 * from the old one only while its timestamp still matches the AFTER snapshot.
 * Later work keeps its actual timestamp rather than being frozen in the past.
 */
function withAgesFrom(backlog, agesFrom, agesThrough) {
  if (!agesFrom) return backlog;
  const old = new Map(agesFrom.issues.map((i) => [i.id, i.updatedAt]));
  const after = new Map(agesThrough.issues.map((i) => [i.id, i.updatedAt]));
  return {
    ...backlog,
    source: `${backlog.source || '(unnamed)'}; ages from ${agesFrom.source || 'a snapshot'}`,
    issues: backlog.issues.map((i) => (old.has(i.id) && after.get(i.id) === i.updatedAt
      ? { ...i, updatedAt: old.get(i.id) } : i)),
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
    `gate ${RULES_VERSION} (${report.strength}) — milestone ${report.milestone}, ${report.live} live of ${report.total}`,
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
 * appear in any file here: the rules, the fixtures, the model, the protocol,
 * the seed, SKILL.md. The tracker is reached through the project's adapter and
 * the project's own tracker doc, never through anything that ships with the
 * rules.
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
  const files = ['check.mjs', 'check-commits.mjs', 'model.md', 'SKILL.md', 'integration.md', 'protocol.md', 'fixtures/config.json'];
  for (const kind of ['bad', 'good', 'commits'])
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
    const expected = (backlog.expect || [name.split('.')[0]]).slice().sort();
    const report = evaluate(backlog, cfg, null);
    const fired = report.results.filter((r) => r.violations.length).map((r) => r.id).sort();
    let ok = fired.join(',') === expected.join(',');
    let detail = ok ? '' : `  expected [${expected.join(', ')}]`;
    // Naming the check is not enough for a rule with several branches: a
    // fixture may list the exact violations, as check|issue|ref, and then
    // finding only some of them fails.
    if (ok && backlog.expectViolations) {
      const got = report.results.flatMap((r) => r.violations.map((v) => `${r.id}|${v.id}|${v.ref || ''}`)).sort();
      const want = backlog.expectViolations.slice().sort();
      ok = got.join(' ') === want.join(' ');
      if (!ok) detail = `  violations [${got.join(' ')}]  expected [${want.join(' ')}]`;
    }
    say(ok, `bad/${name} → [${fired.join(', ') || 'nothing'}]${detail}`);
  }
  for (const name of readdirSync(join(dir, 'good')).sort()) {
    const report = evaluate(load('good', name), cfg, null);
    const fired = report.results.filter((r) => r.violations.length).map((r) => r.id);
    say(fired.length === 0, `good/${name} → [${fired.join(', ') || 'nothing'}]`);
  }
  const covered = new Set(readdirSync(join(dir, 'bad')).map((n) => n.split('.')[0]));
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
  say(fired(edited) === 0 && fired(withAgesFrom(edited, dirty, edited)) > 0, 'ages-from: a stale hold hidden by a bulk edit is seen again through paired snapshots');
  const resumed = { ...edited, issues: edited.issues.map((i) => ({ ...i, updatedAt: '2026-02-01T00:00:00Z' })) };
  say(fired(withAgesFrom(resumed, dirty, edited)) === 0, 'ages-from: later real work retains its timestamp');
  // The command line, run for real. A gate that cannot read its input must
  // exit 2 and never 0, and none of these may pass by accident.
  const tmp = mkdtempSync(join(tmpdir(), 'gate-selftest-'));
  const put = (name, value) => {
    const p = join(tmp, name);
    writeFileSync(p, typeof value === 'string' ? value : JSON.stringify(value));
    return p;
  };
  const run = (...argv) => spawnSync(process.execPath, [fileURLToPath(import.meta.url), ...argv], { encoding: 'utf8' });
  const fixtureCfg = join(dir, 'config.json');
  const overBudget = join(dir, 'bad', 'finding-budget.json');
  const stale = join(dir, 'bad', 'stale-hold.json');
  for (const [what, want, argv] of [
    ['an unknown --only', 2, ['--config', fixtureCfg, '--strength', 'block', '--only', 'no-such-check', stale]],
    ['an empty backlog file', 2, ['--config', fixtureCfg, '--strength', 'block', put('empty.json', '')]],
    ['a config that is not an object', 2, ['--config', put('cfg.json', '[]'), '--strength', 'block', stale]],
    ['a live issue with an unreadable date', 2, ['--config', fixtureCfg, '--strength', 'block', put('nodate.json', { issues: [{ id: 'A', status: 'active', updatedAt: 'not-a-date' }] })]],
    ['no strength chosen anywhere', 2, ['--config', fixtureCfg, stale]],
    ['an unpaired age snapshot', 2, ['--config', fixtureCfg, '--strength', 'block', '--ages-from', stale, stale]],
    ['a config with only a strength, on a violating backlog', 1, ['--config', put('min.json', { strength: 'block' }), stale]],
  ]) {
    const got = run(...argv).status;
    say(got === want, `cli: ${what} → exit ${got}${got === want ? '' : `, expected ${want}`}`);
  }
  // Lowering the budget creates violations. Judged by today's config the
  // baseline has them too and block-new is green; judged by its own it is not.
  const roomy = put('roomy.json', { ...cfg, findingBudget: 9, strength: 'block-new' });
  const tight = put('tight.json', { ...cfg, strength: 'block-new' });
  const blind = run('--config', tight, '--baseline', overBudget, overBudget).status;
  const sighted = run('--config', tight, '--baseline', overBudget, '--baseline-config', roomy, overBudget).status;
  say(blind === 0 && sighted === 1, `cli: a lowered budget is old debt without --baseline-config (exit ${blind}) and a new error with it (exit ${sighted})`);

  console.log(failures ? `\n${failures} failure(s)` : '\nall fixtures pass');
  return failures ? 1 : 0;
}

function main() {
  const argv = process.argv.slice(2);
  const args = { json: false, only: null, input: null, selftest: false, config: null, baseline: null, baselineConfig: null, strength: null, agesFrom: null, agesThrough: null };
  for (let i = 0; i < argv.length; i++) {
    if (argv[i] === '--json') args.json = true;
    else if (argv[i] === '--only') args.only = argv[++i].split(',');
    else if (argv[i] === '--config') args.config = argv[++i];
    else if (argv[i] === '--baseline') args.baseline = argv[++i];
    else if (argv[i] === '--baseline-config') args.baselineConfig = argv[++i];
    else if (argv[i] === '--strength') args.strength = argv[++i];
    else if (argv[i] === '--ages-from') args.agesFrom = argv[++i];
    else if (argv[i] === '--ages-through') args.agesThrough = argv[++i];
    else if (argv[i] === '--selftest') args.selftest = true;
    else if (argv[i] === '--help' || argv[i] === '-h') args.help = true;
    else if (argv[i] === '--version') args.version = true;
    else args.input = argv[i];
  }
  if (args.version) {
    console.log(RULES_VERSION);
    return 0;
  }
  if (args.help) {
    console.log(
      'check.mjs --config <project config> [<normalized.json>|-]\n' +
        '          [--baseline <normalized.json> [--baseline-config <config as it was then>]]\n' +
        '          [--strength block|block-new|report] [--ages-from <before.json> --ages-through <after.json>]\n' +
        '          [--only <id,id>] [--json]\n' +
        'check.mjs --selftest [--config <project config>]\n' +
        'check.mjs --version\n\n' +
        'Applies the backlog invariants to the normalized backlog on stdin or at <path>.\n' +
        'Strength comes from the config unless given; block-new needs --baseline.\n' +
        '--ages-from and --ages-through correct only timestamps still matching the bulk edit.\n' +
        '--baseline-config judges the baseline by the rules of its own day, so a\n' +
        'config change that creates violations shows up as new errors.\n' +
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
    return runSelftest(checkedConfig(readJson(join(HERE, 'fixtures', 'config.json'), 'the fixture config'), 'fixture config'), args.config);

  if (!args.config) throw new Misuse('--config <project config> is required; the rules carry no project of their own');
  const cfg = checkedConfig(readJson(args.config, 'the config'), 'config');
  const strength = args.strength || cfg.strength;
  // No default. A strength nobody chose is a strength everybody has without
  // knowing it, and the quiet one of the three is indistinguishable from off.
  if (!STRENGTHS.includes(strength)) throw new Misuse(`strength must be one of ${STRENGTHS.join(', ')}; got ${strength ?? 'none'}`);
  if (strength === 'block-new' && !args.baseline) throw new Misuse('block-new needs --baseline: without one every violation is new');
  // A typo here used to select no check at all and exit green.
  const unknown = (args.only || []).filter((id) => !CHECKS.some((c) => c.id === id));
  if (unknown.length || (args.only && !args.only.length))
    throw new Misuse(`--only names no such check: ${unknown.join(', ') || '(nothing)'}. Checks: ${CHECKS.map((c) => c.id).join(', ')}`);

  const ages = args.agesFrom ? checkedBacklog(readJson(args.agesFrom, 'the ages snapshot'), 'ages snapshot') : null;
  if (Boolean(args.agesFrom) !== Boolean(args.agesThrough)) throw new Misuse('--ages-from and --ages-through must be supplied together');
  const after = args.agesThrough ? checkedBacklog(readJson(args.agesThrough, 'the after snapshot'), 'after snapshot') : null;
  const report = evaluate(withAgesFrom(checkedBacklog(readJson(args.input, 'the backlog'), 'backlog'), ages, after), cfg, args.only);
  // The baseline is judged by the config of ITS day when one is given. With
  // the current config, lowering a budget or renaming the milestone creates
  // violations that look like old debt, and block-new waves them through.
  const thenCfg = args.baselineConfig ? checkedConfig(readJson(args.baselineConfig, 'the baseline config'), 'baseline config') : cfg;
  const baseline = args.baseline
    ? evaluate(withAgesFrom(checkedBacklog(readJson(args.baseline, 'the baseline'), 'baseline'), ages, after), thenCfg, args.only, report.now)
    : null;
  judge(report, baseline, strength);
  console.log(args.json ? JSON.stringify(report, null, 2) : render(report));
  return report.failed ? 1 : 0;
}

try {
  process.exitCode = main();
} catch (e) {
  // Anything unforeseen is misuse too: exit 1 means "the backlog is red", and
  // a crash has established no such thing.
  console.error(`check.mjs: ${e instanceof Misuse ? e.message : e.stack}`);
  process.exitCode = 2;
}
