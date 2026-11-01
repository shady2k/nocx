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

import { createHash } from 'node:crypto';
import { mkdtempSync, readdirSync, readFileSync, writeFileSync } from 'node:fs';
import { notRegularFileSaid, requireRegularFile } from './regular-file.mjs';
import { spawnSync } from 'node:child_process';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { spansOf } from './time-format.mjs';
import { ValueNeeded, valueOf } from './argv.mjs';

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

// A stage is a container of work: it either carries leaves under it, or it is
// the one and only child of its feature. A leaf that is one of several
// children of a feature is one step of the feature's work, not the place its
// outcome is reachable; letting one serve an outcome is how a stray task
// quietly replaces the person's path.
const carries = (m, i) => (m.children.get(i?.id) || []).length > 0;
const isStage = (m, i) => {
  if (!i || !i.parent) return false;
  const feature = m.by.get(i.parent);
  if (feature?.type !== 'epic') return false;
  // Under a feature, the stages are the children that carry work. Where no
  // child carries any, the children themselves are the breakdown, so a feature
  // cut into two layer leaves is still judged.
  const siblings = (m.children.get(i.parent) || []).map((id) => m.by.get(id)).filter(Boolean);
  return carries(m, i) || !siblings.some((s) => carries(m, s));
};

// ---------------------------------------------------------------- model

function index(backlog) {
  const by = new Map(backlog.issues.map((i) => [i.id, i]));
  const children = new Map();
  // Issue id -> the commit ids naming it, each list sorted so the violation's
  // ref — the earliest of them — does not depend on the adapter's order.
  const landedBy = new Map();
  if (backlog.landed) for (const c of backlog.landed)
    for (const tid of c.taskIds)
      landedBy.set(tid, [...(landedBy.get(tid) || []), c.id].sort());
  for (const i of backlog.issues) {
    if (!i.parent) continue;
    if (!children.has(i.parent)) children.set(i.parent, []);
    children.get(i.parent).push(i.id);
  }
  return {
    issues: backlog.issues, by, children, outcomes: backlog.outcomes || [],
    vision: backlog.vision || null, forecasts: backlog.forecasts || [],
    // The forecast rules are turned on by the export carrying the records at all:
    // a project whose adapter emits none has not adopted them, and its existing
    // dates are not judged by a rule it never agreed to.
    // The main line's commits, in check-commits.mjs's own shape, that the
    // adapter supplies when it can read the line at the revision the gate
    // judges. Absent means the adapter has no such read yet, and the merge
    // rule below is judged by none of it.
    landed: backlog.landed || null, landedBy,
    hasForecasts: backlog.forecasts != null, generatedAt: backlog.generatedAt,
  };
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
// Body syntax the gate itself reads, one line per fact, no adapter parsing:
//
//   Serves: O1, O2          (a stage — the outcome ids it serves)
//   Seen at: signup, receipt  (a feature — the places a person reaches its outcome)
//   Preparatory: <reason>   (a stage — prefactoring, and why)
//
// Each is matched case-insensitively, at the start of a line, with an optional
// leading `-`, `*` or `>`, exactly like the DONE WHEN recognition above: a
// marker at the top of a file that somebody meant and never filled counts as a
// declaration, and an empty one does not.
const afterMarker = (i, marker) => {
  const re = new RegExp(`^\\s*[-*>]?\\s*${marker}\\s*:(.*)$`, 'im');
  const at = re.exec(i.body || '');
  // One fact, one line: the value is the rest of that line and nothing after
  // it. Prose on the next line is not a value, so a `Serves: O1` above a
  // sentence does not turn that sentence into outcome ids.
  return at ? at[1].trim() : '';
};

// A value that is only a placeholder declares nothing: TODO, TBD, TKTK and
// anything between angle brackets is somebody's intent to fill it in later,
// not the fact itself.
const isPlaceholder = (t) =>
  !t || /(?:\W|^)(?:todo|tbd|tktk)(?:\W|$)|<[^>]*>/.test(t.toLowerCase());

const wordIds = (t) => t.match(/[\p{L}\p{N}][\p{L}\p{N}-]*/gu) || [];
const servesOf = (i) => (isPlaceholder(afterMarker(i, 'serves')) ? [] : wordIds(afterMarker(i, 'serves')));
const seenAtOf = (i) =>
  isPlaceholder(afterMarker(i, 'seen at')) ? [] : wordIds(afterMarker(i, 'seen at')).map((h) => h.toLowerCase());
// A date or a duration a document promises: one line, in the same shape as the
// facts above. What the gate refuses is a promise nothing measured stands
// behind, whether it is a target date on a charter or a feature or a duration a
// status reports.
const targetOf = (i) => (isPlaceholder(afterMarker(i, 'target')) ? '' : afterMarker(i, 'target'));
const forecastOf = (i) => (isPlaceholder(afterMarker(i, 'forecast')) ? '' : afterMarker(i, 'forecast'));
const statedOf = (i) => [{ kind: 'Target', value: targetOf(i) }, { kind: 'Forecast', value: forecastOf(i) }]
  .filter((x) => x.value);

// Three word characters is the least a reason can carry and still be one;
// an empty or bracketed placeholder declares nothing.
const preparatoryOf = (i) =>
  !isPlaceholder(afterMarker(i, 'preparatory')) &&
  (afterMarker(i, 'preparatory').match(/[\p{L}\p{N}]/gu) || []).length >= 3;

// The DONE WHEN text of one issue, lowercased, from the first marker to the
// next heading, with this syntax's own lines stripped first. The same slice
// hasCriterion has always read, kept honest by one owner.
const criterionTextOf = (i) => {
  const b = (i.body || '').toLowerCase();
  for (const mk of CRITERION_MARKERS) {
    const at = b.indexOf(mk);
    if (at >= 0) return b.slice(at + mk.length).split(/\n\s*#/)[0]
      .split('\n').filter((l) => !/^\s*[-*>]?\s*(?:serves|seen at|preparatory)\s*:/i.test(l)).join('\n');
  }
  return '';
};

export const hasCriterion = (i) => (criterionTextOf(i).match(/[\p{L}\p{N}]{2,}/gu) || []).length >= 3;

// ------------------------------------------------ the vision, and the chain
// The owner's journeys and principles, each with a stable id, as the vision
// record (or the decision record it points to) keeps them. Everything under the
// vision names these ids and nothing copies their wording: the text is read from
// the record at the moment it is needed, so a changed vision changes the answer
// and no copy rots.
const low = (t) => String(t).toLowerCase();
function visionItems(v) {
  const items = new Map();
  for (const [kind, list] of [['journey', v?.journeys], ['principle', v?.principles]])
    for (const item of list || [])
      if (item && typeof item.id === 'string' && item.id) items.set(low(item.id), { id: item.id, kind, title: item.title });
  return items;
}

// The exact task set covering the outcomes a document serves: the leaves under
// the live and closed stages that serve one of them. A document that serves
// nothing is its own scope, and the leaves under it are its set.
function taskSetOf(m, scope) {
  const wanted = new Set(servesOf(scope).map(low));
  const roots = wanted.size
    ? m.issues.filter((x) => (LIVE(x.status) || x.status === 'closed') && isStage(m, x)
      && servesOf(x).some((s) => wanted.has(low(s)))).map((x) => x.id)
    : [scope.id];
  const leaves = new Set();
  for (const r of roots)
    for (const id of [r, ...subtree(m, r)]) {
      const x = m.by.get(id);
      if (x && x.type !== 'epic' && !(m.children.get(id) || []).length) leaves.add(id);
    }
  leaves.delete(scope.id);
  return [...leaves].sort();
}

// One definition of a task set's digest, shared with the run script that writes
// the record: sha256 over the sorted ids, one per line.
const setDigest = (ids) => createHash('sha256').update([...ids].sort().join('\n')).digest('hex');

// Every promise of a date or a duration a live or closed issue carries, with the
// set the gate computes for it and the record it names.
function statements(m) {
  return m.issues
    .filter((i) => LIVE(i.status) || i.status === 'closed')
    .flatMap((issue) => statedOf(issue).map((s) => ({
      issue, kind: s.kind, stated: s.value, tasks: taskSetOf(m, issue),
      record: (m.forecasts || []).find((r) => r.scope === issue.id && r.stated === s.value),
    })));
}
const recordMismatch = (s, r) => r.digest !== setDigest(r.tasks) ||
  r.tasks.slice().sort().join(' ') !== s.tasks.join(' ');
// A record that carries no measurement is not a forecast: the run script writes
// how many finished runs it had, and the range when it had enough of them. Three
// is the least a pace can be measured from, the same bound the run script uses.
const recordComputed = (r) => typeof r.enough === 'boolean' && Number.isFinite(r.runs)
  && (r.enough ? Number.isFinite(r.low) && Number.isFinite(r.high) && r.runs >= 3 : r.runs < 3);

/**
 * The purpose of one task, for the brief a worker is handed: what it serves, in
 * the vision's current wording, whom it serves and where a person sees it. Ids
 * travel down the chain and are expanded here, at read time — a reference, never
 * a copy.
 */
export function purposeOf(backlog, taskId) {
  const m = index(backlog);
  const task = m.by.get(taskId);
  if (!task) throw new Misuse(`purpose: the backlog holds no issue ${taskId}`);
  const items = visionItems(m.vision);
  const outcomes = new Map(m.outcomes.map((o) => [low(o.id), o]));
  const chain = [];
  for (let cur = task, seen = new Set(); cur && !seen.has(cur.id); cur = m.by.get(cur.parent)) {
    seen.add(cur.id);
    chain.push(cur);
  }
  const served = new Map();
  const vision = new Map();
  const take = (id) => {
    if (served.has(low(id)) || vision.has(low(id))) return;
    const outcome = outcomes.get(low(id));
    if (!outcome) {
      vision.set(low(id), items.get(low(id)) || { id, kind: 'unknown', title: 'not declared in the vision record' });
      return;
    }
    served.set(low(id), outcome);
    for (const x of outcome.serves || []) take(x);
  };
  for (const node of chain) for (const id of servesOf(node)) take(id);
  const byId = (a, b) => (a.id < b.id ? -1 : 1);
  const of = (kind) => [...vision.values()].filter((x) => x.kind === kind).sort(byId);
  const feature = chain.find((x) => x.type === 'epic') || null;
  return {
    id: task.id, title: task.title,
    serves: [...served.values()].sort(byId),
    journeys: of('journey'), principles: of('principle'), unknown: of('unknown'),
    // The stage the task sits in, beside the feature it belongs to: the purpose
    // descends to the task through both, and a brief that names only the feature
    // leaves the stage out.
    stage: chain[1] && chain[1].type !== 'epic' ? chain[1] : null,
    under: feature, seenAt: feature ? seenAtOf(feature) : [],
  };
}

/** The one fixed shape the brief carries: no prose, one line per fact. */
export function describePurpose(p) {
  const lines = [`${p.id}: ${p.title}`];
  for (const o of p.serves) lines.push(`serves ${o.id}: ${o.title}`);
  for (const j of p.journeys) lines.push(`serves ${j.id}: ${j.title}`);
  for (const k of p.principles) lines.push(`principle ${k.id}: ${k.title}`);
  for (const u of p.unknown) lines.push(`serves ${u.id}: declared in no vision record this backlog carries`);
  if (!p.serves.length && !p.journeys.length && !p.principles.length && !p.unknown.length)
    lines.push(`serves nothing: no stage or feature above ${p.id} names what it is for`);
  if (p.stage) lines.push(`stage ${p.stage.id}: ${p.stage.title}`);
  if (p.under) lines.push(`under ${p.under.id}: ${p.under.title}`);
  lines.push(p.seenAt.length ? `seen at ${p.seenAt.join(', ')}` : 'seen at: nowhere declared, so no person reaches it yet');
  return lines.join('\n');
}


// A span that has ended with no receipt: its session claimed something else,
// its item was claimed again, or its item is no longer being worked on. One
// still active and claimed by nobody since may simply still be running.
function unreceipted(m) {
  const { spans } = spansOf(m);
  return spans.filter((s) => s.claim && !s.receipt && !s.conflict.length).flatMap((s) => {
    const item = m.by.get(s.item);
    const later = spans.find((o) => o !== s && o.claim && o.item === s.item && o.start > s.start);
    const why = s.next ? `the same session claimed ${s.next.item} at ${s.next.claim.fields.at}`
      : later ? `${s.item} was claimed again at ${later.claim.fields.at}`
      : item && item.status !== 'active' ? `${s.item} is ${item.status}` : null;
    return why ? [{ id: s.item, ref: `span:${s.id}`, note: `span ${s.id} has ended with no receipt: ${why}` }] : [];
  });
}

// Work in flight when time records were adopted was begun by sessions that
// could not write a claim. Setup lists it, and the exemption follows the task
// tree, so a task split afterwards keeps it.
function exemptFromRecords(m, cfg, id) {
  const listed = new Set(cfg.timeRecordsExempt || []);
  const seen = new Set();
  for (let cur = id; cur && !seen.has(cur); cur = m.by.get(cur)?.parent) {
    if (listed.has(cur)) return true;
    seen.add(cur);
  }
  return false;
}

// A run's time is its coordinator's clock: a claim on the feature or stage
// covers every leaf its workers hand in under it. A worker's own claim adds
// its effort by phase, and nothing requires it.
function claimedAbove(m, claimed, id) {
  const seen = new Set();
  for (let cur = id; cur && !seen.has(cur); cur = m.by.get(cur)?.parent) {
    if (claimed.has(cur)) return true;
    seen.add(cur);
  }
  return false;
}

function unclaimedWork(m, cfg) {
  const claimed = new Set(spansOf(m).spans.filter((s) => s.claim).map((s) => s.item));
  return m.issues.filter((i) => ['submitted', 'implemented'].includes(i.status) && i.type !== 'epic' && !m.children.has(i.id) && !claimedAbove(m, claimed, i.id) && !exemptFromRecords(m, cfg, i.id))
    .map((i) => ({ id: i.id, note: `${i.status} with no claim recorded on it` }));
}

// ---------------------------------------------------------------- checks

const CHECKS = [
  {
    id: 'idea-blocks-work',
    severity: 'error',
    // Its verdict reads a config value: a config that changed elsewhere changes it.
    config: true,
    why: 'An idea that blocks a build is not an idea, it is an undecided question, and it belongs in the work it is blocking.',
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
    // Its verdict reads a config value: a config that changed elsewhere changes it.
    config: true,
    why: 'An idea reachable as work is offered as work. An edge that only records where it came from gates nothing, so it does not stop that.',
    fix: 'Defer it. An idea costs nothing while it waits and is closed without regret when it stops being interesting.',
    run: (m, cfg) =>
      m.issues
        .filter((i) => LIVE(i.status) && isIdea(cfg, i))
        .map((i) => ({ id: i.id, note: `status ${i.status}; an idea belongs deferred` })),
  },
  {
    id: 'stale-edge',
    severity: 'warn',
    // Its verdict reads a config value: a config that changed elsewhere changes it.
    config: true,
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
    // Its verdict reads a config value: a config that changed elsewhere changes it.
    config: true,
    why: 'Everything live belongs to the current milestone; whatever does not is deferred. Otherwise the queue offers next quarter\'s feature as today\'s work, and a parentless issue filed by hand is how a groomed backlog grows back into a heap of roots while every other check stays green.',
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
    // Its verdict reads a config value: a config that changed elsewhere changes it.
    config: true,
    why: 'Active means somebody is holding it now. An unheld issue left active is invisible to the ready queue and to every colleague looking for work.',
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
    id: 'merged-but-open',
    severity: 'error',
    why: 'The work is already on the main line: a commit on it names this leaf. A worker is offered it a second time, and the owner is told work remains that exists.',
    fix: 'Close the leaf with the revision that delivered it and the checks the merge ran, by the protocol\'s rules "A leaf with no stage closes at its merge" and "Remaining work is counted from what landed", or by close-out\'s evidence rule where the leaf carries a stage. Where the commit did not deliver it, the change or the link is what is wrong, not the status.',
    // No `ref`: the party to this violation is the issue itself, and a commit
    // is evidence in the note, not identity. A baseline keyed on a commit would
    // call the same forgotten close new again the day another commit named it.
    run: (m) => !m.landed ? [] : m.issues
      .filter((i) => (i.status === 'open' || i.status === 'active') && i.type !== 'epic' && !carries(m, i))
      .flatMap((i) => {
        const namedBy = m.landedBy.get(i.id);
        if (!namedBy) return [];
        const others = namedBy.length - 1;
        return [{
          id: i.id,
          note: `still ${i.status}, and the commit ${namedBy[0]} on the main line names it${others ? ` (and ${others} more)` : ''}`,
        }];
      }),
  },
  {
    id: 'orphaned-outcome',
    severity: 'error',
    why: 'A milestone outcome no stage serves is a promise made on paper. Deferred stages serve nothing, so a milestone whose gains sit beyond every live and closed stage is headed nowhere while looking planned.',
    fix: 'Green looks like this: every outcome id the charter declares is named in the Serves line of at least one stage that is live or closed, and stages name only ids the charter declares. A milestone that has not got there yet is best honest about it: defer the milestone\'s work knowingly rather than stage layers underneath a person who never reaches anything.',
    run: (m) => {
      if (!m.outcomes.length) return [];
      const declared = new Set(m.outcomes.map((o) => o.id.toLowerCase()));
      const stageServes = m.issues
        .filter((i) => (LIVE(i.status) || i.status === 'closed') && isStage(m, i))
        .map((i) => servesOf(i).map((x) => ({ id: x, key: x.toLowerCase() })));
      const low = (t) => t.toLowerCase();
      return [
        ...m.outcomes.flatMap((o) =>
          stageServes.some((ids) => ids.some((x) => x.key === low(o.id)))
            ? []
            : [{ id: o.id, note: `the outcome ${o.id} (${o.title}) is what the charter says a person gains, and no live or closed stage serves it; deferred work serves nothing` }]),
        ...[...new Set(stageServes.flat().filter((x) => !declared.has(x.key)).map((x) => x.id))]
          .map((id) => ({ id, note: `the stage serves the outcome ${id}, but the charter declares no such outcome id, so what it names is nobody's planned gain` })),
      ];
    },
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
    id: 'unobservable-first-stage',
    severity: 'error',
    why: 'A feature whose first work is architecture shows a person nothing at any step until the last one. One such step is real prefactoring and is allowed with its reason; after it, a stage that still hides the feature from the person means the milestone said where they would see something and the breakdown does not pass through it. A feature that names no place in its body is not judged here — a known limit: without the place, the rule has nothing to bind to.',
    fix: 'Green looks like this: the feature names in its Seen at line the place a person reaches its outcome, and the stage first in line has a DONE WHEN that names that place too — a person does the thing there — or that first stage says Preparatory with its reason, and the stage after it reaches the person. More than one preparatory stage before anything observable, or none at all, is the breakdown to re-cut.',
    run: (m) =>
      m.issues.flatMap((f) => {
        if (f.type !== 'epic' || !(m.children.get(f.id) || []).length) return [];
        const handles = seenAtOf(f);
        if (!handles.length) return [];
        const stages = (m.children.get(f.id) || []).map((id) => m.by.get(id)).filter((i) => isStage(m, i))
          .sort((a, b) => ((Date.parse(a.createdAt) || Infinity) - (Date.parse(b.createdAt) || Infinity) || (a.id < b.id ? -1 : 1)));
        // A criterion that is only a placeholder names nothing a person sees,
        // even when the declared handle appears in it.
        const observable = (i) => {
          const t = criterionTextOf(i);
          return !isPlaceholder(t) && handles.some((h) => t.includes(h));
        };
        const note = (i) => `the stage ${i.id} shows a person nothing: DONE WHEN names none of ${handles.join(', ')}, which the feature ${f.id} declares as the places a person reaches the outcome of ${f.id}`;
        let allowed = true;
        for (let k = 0; k < stages.length; k++) {
          const s = stages[k];
          if (preparatoryOf(s)) {
            if (allowed && k === stages.length - 1)
              return [{ id: s.id, ref: f.id, note: `${note(s)}; the stage is declared Preparatory, but the feature has no later stage, so the person would reach those places nowhere` }];
            if (!allowed)
              return [{ id: s.id, ref: f.id, note: `${note(s)}; and it is the second preparatory stage, and the first was already allowed once and must be followed by the observable one` }];
            allowed = false;
            continue;
          }
          if (observable(s)) return [];
          return [{ id: s.id, ref: f.id, note: note(s) }];
        }
        return [];
      }),
  },
  {
    id: 'unserved-outcome',
    severity: 'error',
    why: 'An outcome that serves nothing the vision declares is work nobody asked for, and an id the vision does not declare is a link to something that does not exist. A milestone whose outcomes name no journey and no principle has stopped being the owner\'s milestone: what reaches the plan is the breakdown somebody invented, not the purpose he gave.',
    fix: 'Green looks like this: each outcome line of the charter carries a Serves: line naming the vision journeys and principles it serves, and every id it names is declared in the vision record. The ids are added to a charter that already exists without changing its text.',
    run: (m) => {
      const v = visionItems(m.vision);
      if (!v.size || !m.outcomes.length) return [];
      const declared = [...v.values()].map((x) => x.id).join(', ');
      return m.outcomes.flatMap((o) => {
        const ids = (o.serves || []).filter((x) => typeof x === 'string' && x);
        if (!ids.length)
          return [{ id: o.id, note: `the outcome ${o.id} (${o.title}) serves nothing: the vision declares ${declared}, and this outcome names none of them` }];
        return ids.filter((x) => !v.has(low(x)))
          .map((x) => ({ id: o.id, ref: x, note: `the outcome ${o.id} serves ${x}, and the vision declares no such journey or principle` }));
      });
    },
  },
  {
    id: 'unforecast-target',
    severity: 'error',
    why: 'A date that arrives with the work — in a brief, from a parent session, out of habit — or headroom chosen instead of an estimate, is not a forecast: nothing measured stands behind it, and the plan is fitted to it instead of to the work. A date whose forecast no longer covers the work it serves is the same promise with a stale number under it.',
    fix: 'Green looks like this: one record stands beside the date or the duration, computed by the run script from the measured pace over the exact task set covering the outcomes the document serves, pinned to that set\'s digest. Where the work grew or shrank, compute the record again rather than restating the date.',
    run: (m) => m.hasForecasts ? statements(m).flatMap((s) => {
      if (!s.record)
        return [{ id: s.issue.id, note: `the ${s.stated} it promises has no forecast record: compute one over the ${s.tasks.length} task(s) covering the outcomes it serves` }];
      if (recordMismatch(s, s.record))
        return [{ id: s.issue.id, note: `the forecast record for ${s.stated} covers ${s.record.tasks.length} task(s) and the outcomes it serves cover ${s.tasks.length} today: compute the record again` }];
      // The set is right; the record must also carry what was measured, or it is
      // a task list wearing a forecast's name.
      return recordComputed(s.record) ? []
        : [{ id: s.issue.id, note: `the forecast record for ${s.stated} carries no measured pace: the run script writes how many finished runs it had and, once there are enough, the range it measured; a record without the measurement is not a forecast` }];
    }) : [],
  },
  {
    id: 'unmeasured-forecast',
    severity: 'warn',
    why: 'A project with too little history has no pace to measure, and a date computed from nothing would be a guess wearing a number. Saying so is not a refusal: it is the record being honest, and the owner being told which date stands on a guess.',
    fix: 'Green looks like this: the record stands beside the date and says how many finished runs it had; once three runs have finished work comparable to this, compute the record again and the warning goes.',
    run: (m) => m.hasForecasts ? statements(m)
      .filter((s) => s.record && !recordMismatch(s, s.record) && s.record.enough === false)
      .map((s) => ({ id: s.issue.id, note: `the forecast record for ${s.stated} is computed from ${s.record.runs ?? 0} finished run(s): too little history for a measured pace, so the date stands on a guess` })) : [],
  },
  {
    id: 'label-vocabulary',
    severity: 'error',
    // Its verdict reads a config value: a config that changed elsewhere changes it.
    config: true,
    why: 'A vocabulary enforced only by prose drifts. A closed list declared only in a document is soon outgrown by the labels actually used.',
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
    // Its verdict reads a config value: a config that changed elsewhere changes it.
    config: true,
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
    // Its verdict reads a config value: a config that changed elsewhere changes it.
    config: true,
    why: 'Bugs and debt found mid-milestone go to the front one at a time, each reasonable, and push the feature out by a fortnight nobody decided on. The charter declares how many the milestone absorbs.',
    fix: 'The ways that do the work are the owner\'s to give: displace named planned work and raise the budget in config and the charter decision, open the next milestone for it, or hold it knowingly with a review date. Deferring it yourself to make the number fit, dropping its milestone label or narrowing it shrinks the count instead, and leaves a record saying the fault is not there. A repair a required check forced on the way to a merge, and upkeep of the set\'s own checks, hooks and their CI wiring, are not intake at all: file them with the work whose merge they block, or with the setup task (under it where it is a container, beside it where it is a leaf), with no finding label. Deferring planned work alone does not change this count.',
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
  {
    id: 'time-record-damaged',
    severity: 'error',
    why: 'A record of how the work went that does not parse, or whose table does not add up, is not counted by anyone, so the time it held is lost while the item looks recorded.',
    fix: 'Void it with the run script\'s `void`, naming its comment, and post what it prints. Where the record still matters, write it again with the run script, never by hand, and post it exactly as printed: a check that then names a missing claim or receipt says how. The numbers come from the script; a hand-edited table is what this catches.',
    run: (m) => spansOf(m).damaged.map((r) => ({ id: r.item, ref: `comment:${r.comment}`, note: r.problems.slice(0, 2).join('; ') })),
  },
  {
    id: 'time-void-idle',
    severity: 'warn',
    why: 'A void that is damaged or names no record of the set on its item retires nothing, so the record it was meant for still counts. It does no harm by itself, so it does not block.',
    fix: 'Where a record still needs voiding, void it again with the run script\'s `void`, naming its comment. The idle void can stay.',
    run: (m) => spansOf(m).idle.map((r) => ({ id: r.item, ref: `comment:${r.comment}`, note: r.why })),
  },
  {
    id: 'time-span-conflict',
    severity: 'error',
    why: 'One span has one claim and one receipt. Two different ones mean the same minutes are recorded twice, or a receipt was written for someone else\'s span, and no total built on them can be trusted.',
    fix: 'Keep the record the script printed for this span and void the other with the run script\'s `void`, naming its comment. Where the one to keep is wrong too, void both and write it again with the run script. The same record posted twice by a retry is not a conflict and needs nothing.',
    run: (m) => spansOf(m).spans.filter((s) => s.conflict.length || (s.claim && s.receipt && Date.parse(s.receipt.fields.from) !== s.start))
      .map((s) => ({ id: s.item, ref: `span:${s.id}`, note: s.conflict.length ? `span ${s.id}: ${s.conflict.join('; ')} (comments ${s.conflicting.join(', ')})` : `span ${s.id}: its receipt (comment ${s.receipt.comment}) starts at ${s.receipt.fields.from}, its claim (comment ${s.claim.comment}) at ${s.claim.fields.at}` })),
  },
  {
    id: 'time-span-unclaimed',
    severity: 'error',
    why: 'A receipt or event whose span was never claimed belongs to no session and no item start, so nothing can say whose time it is or whether it overlaps other work.',
    fix: 'Post the span\'s claim where it was taken, or void a record written against a span id that does not exist with the run script\'s `void`, naming its comment.',
    run: (m) => spansOf(m).spans.filter((s) => !s.claim && !s.conflict.length).map((s) => ({ id: s.item, ref: `span:${s.id}`, note: `span ${s.id} has records but no claim (comments ${[...s.receipts, ...s.events].map((r) => r.comment).join(', ')})` })),
  },
  {
    id: 'time-span-overlap',
    severity: 'error',
    why: 'A session works on one item at a time: taking the next ends the one before. A receipt reaching past the session\'s next claim counts the same minutes on two items.',
    fix: 'Void the receipt that overlaps with the run script\'s `void`, naming its comment, then write it again with the run script, which ends the span where the session\'s next claim begins.',
    run: (m) => spansOf(m).spans.filter((s) => s.receipt && s.next && s.end > s.next.start + 60e3)
      .map((s) => ({ id: s.item, ref: `span:${s.id}`, note: `span ${s.id} ends ${s.receipt.fields.to} (comment ${s.receipt.comment}), after the same session claimed ${s.next.item} at ${s.next.claim.fields.at}` })),
  },
  {
    id: 'time-span-unreceipted',
    severity: 'error',
    why: 'A span that has ended with no receipt is time spent and never recorded. Reports then give a total that looks complete and is not.',
    fix: 'The run script\'s `gaps` prints the receipt of every ended span whose transcript is on this machine: post what it prints unchanged. Where no machine has the transcript, close the span with the run script\'s receipt of unknown time, the reason in its note, and say so to the owner: the time stays unknown, not zero.',
    run: (m) => unreceipted(m),
  },
  {
    id: 'time-work-unclaimed',
    severity: 'error',
    // Its verdict reads a config value: a config that changed elsewhere changes it.
    config: true,
    why: 'A result handed in with no claim on its task, or on the feature or stage it belongs to, was worked on by nobody the record knows: its time cannot be found, and the gate cannot tell it from work that took none.',
    fix: 'Work a coordinator ran is covered by its claim on the feature or stage: where that claim is missing, recover it. Otherwise, on the machine that holds the session\'s transcript, recover its claim with the run script, at the start the transcript shows, and then its receipt. Where no machine has it, recover the claim at the start another record shows, naming that record, close it with a receipt of unknown time, and say so to the owner. Work in flight when time records were adopted is listed as exempt by setup at that moment, never later to quiet this. Closing the task does not fill the gap.',
    run: (m, cfg) => unclaimedWork(m, cfg),
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
const RULES_VERSION = '0.59.0';

const STRENGTHS = ['block', 'block-new', 'report'];

/** Thrown for anything that is the caller's mistake; main turns it into exit 2. */
class Misuse extends Error {}

const LIST_KEYS = ['areaLabels', 'milestoneLabels', 'roadmapLabels', 'triageLabels', 'ideaLabels', 'ideaTitlePrefixes', 'findingLabels', 'timeRecordsExempt'];
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
  const comments = new Set();
  for (const i of raw.issues) {
    if (!i || typeof i.id !== 'string' || !i.id) throw new Misuse(`${where}: an issue has no id`);
    if (seen.has(i.id)) throw new Misuse(`${where}: the id ${i.id} appears twice`);
    seen.add(i.id);
    if (!['open', 'active', 'submitted', 'implemented', 'deferred', 'closed'].includes(i.status)) throw new Misuse(`${where}: ${i.id} has the status "${i.status}"; see model.md`);
    if (i.blockedBy != null && (!Array.isArray(i.blockedBy) || i.blockedBy.some((id) => typeof id !== 'string' || !id)))
      throw new Misuse(`${where}: ${i.id} blockedBy must be a list of ids`);
    // An unreadable date makes every age comparison false, which reads as "fresh".
    if (LIVE(i.status) && !Number.isFinite(Date.parse(i.updatedAt))) throw new Misuse(`${where}: ${i.id} has no readable updatedAt`);
    // A record's violations are known by its comment's id, so a new damaged
    // record is never mistaken for an old one: every comment carries its own.
    if (i.comments != null && (!Array.isArray(i.comments) || i.comments.some((c) => !c || typeof c.body !== 'string'
      || typeof c.id !== 'string' || !c.id || typeof c.author !== 'string' || !Number.isFinite(Date.parse(c.at)))))
      throw new Misuse(`${where}: ${i.id} comments must be a list of { id, at, author, body }, each with its tracker's id, time and author`);
    for (const c of i.comments || []) {
      if (comments.has(c.id)) throw new Misuse(`${where}: the comment id ${c.id} appears twice`);
      comments.add(c.id);
    }
  }
  // The charter's outcomes are how the milestone's gains get ids. No list is
  // the adoption path: a project mid-run that has not put ids on its charter
  // is not stopped, and the rules that read them report nothing.
  // The vision record the rest of the chain names by id: the owner's journeys
  // and principles, each with its own wording. Its presence is what turns the
  // chain's rules on; a project that keeps no such record yet is not judged by
  // them, and the ids are added at setup with the owner's agreement.
  if (raw.vision != null) {
    const v = raw.vision;
    if (typeof v !== 'object' || Array.isArray(v)) throw new Misuse(`${where}: vision must be an object holding journeys and principles`);
    for (const k of Object.keys(v)) if (!['journeys', 'principles'].includes(k))
      throw new Misuse(`${where}: vision.${k} is not part of the vision record, which holds journeys and principles`);
    for (const k of ['journeys', 'principles']) {
      if (v[k] == null) continue;
      if (!Array.isArray(v[k]) || v[k].some((x) => !x || typeof x.id !== 'string' || !x.id || typeof x.title !== 'string'))
        throw new Misuse(`${where}: vision.${k} must be a list of { id, title }, each with a non-empty string id and a string title`);
    }
    // One namespace, matched the way the rules match it: an id that differs only
    // in case would silently overwrite the other in the chain, so it is refused
    // as a duplicate of the id it stands for.
    const ids = new Map();
    for (const k of ['journeys', 'principles'])
      for (const x of v[k] || []) {
        const key = x.id.toLowerCase();
        if (ids.has(key)) throw new Misuse(`${where}: the vision id ${x.id} appears twice (${ids.get(key)} and ${k}), and the rules read them as one id`);
        ids.set(key, k);
      }
  }
  // A forecast record: what the run script computed, over which tasks, beside
  // which promise. The gate recomputes the set and its digest itself.
  if (raw.forecasts != null) {
    if (!Array.isArray(raw.forecasts) || raw.forecasts.some((f) => !f || typeof f.scope !== 'string' || !f.scope
      || typeof f.stated !== 'string' || !f.stated || typeof f.digest !== 'string' || !f.digest
      || !Array.isArray(f.tasks) || f.tasks.some((t) => typeof t !== 'string' || !t)))
      throw new Misuse(`${where}: forecasts must be a list of { scope, stated, tasks, digest }: the issue it stands beside, the date or duration it was computed for, the ids of the task set it covers, and that set's digest`);
  }
  if (raw.outcomes != null) {
    if (!Array.isArray(raw.outcomes) || raw.outcomes.some((o) => !o || typeof o.id !== 'string' || !o.id || typeof o.title !== 'string'
      || (o.serves != null && (!Array.isArray(o.serves) || o.serves.some((x) => typeof x !== 'string' || !x)))))
      throw new Misuse(`${where}: outcomes must be a list of { id, title }, with a non-empty string id and a string title, and an optional serves list of vision ids`);
    const ids = new Set();
    for (const o of raw.outcomes) {
      if (ids.has(o.id)) throw new Misuse(`${where}: the outcome id ${o.id} appears twice`);
      ids.add(o.id);
    }
  }
  // The main line's commits, in the same shape check-commits.mjs takes its
  // `commits` list in, so one parser feeds both. No list is the adoption path:
  // a project whose adapter does not supply the main line yet is judged by none
  // of the merge rule, and its existing queue is not errored on rules it never
  // agreed to.
  if (raw.landed != null) {
    if (!Array.isArray(raw.landed) || raw.landed.some((c) => !c
      || typeof c.id !== 'string' || !c.id.trim()
      || !Array.isArray(c.taskIds) || c.taskIds.some((id) => typeof id !== 'string' || !id.trim())))
      throw new Misuse(`${where}: landed must be a list of { id, taskIds }: each a main-line commit as check-commits.mjs takes it, with a non-empty string id and the non-empty string ids of the issues its message names`);
    const ids = new Set();
    for (const c of raw.landed) {
      if (ids.has(c.id)) throw new Misuse(`${where}: the landed commit id ${c.id} appears twice`);
      ids.add(c.id);
    }
  }
  return raw;
}

function readJson(path, what) {
  let text;
  try {
    // A named file is never opened before it is known to be a regular file; a
    // named pipe with no writer would wait for ever. '-' is the caller's own
    // stdin and is read as it comes.
    if (path && path !== '-') requireRegularFile(path, what);
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

/**
 * A read at a code revision must say which revision it stands for. The store
 * lives outside the code branches, so "the tracker at this revision" is a read
 * of the newest snapshot published at or before it, and nothing in the input's
 * own tree can be checked against the revision. What CAN be checked is that the
 * state being judged was read for the revision that was asked for: without it a
 * stale export, or one from another revision, passes silently as this one, and
 * the gate reports on a state nobody asked about.
 */
function atRevision(backlog, at, where) {
  const said = /@\s*(\S+)\s*$/.exec(String(backlog.source || ''));
  if (!said)
    throw new Misuse(`${where}: --at ${at} asks for the tracker at a code revision, and the input does not say which revision it reads; the source names it as "<store> <where it was read> @ <revision>"`);
  if (said[1] !== at)
    throw new Misuse(`${where}: --at ${at} asks for the tracker at ${at}, and the input reads it at ${said[1]}; a read at a revision is the newest snapshot published at or before it, and one read for another revision cannot stand for it`);
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
      config: Boolean(c.config),
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
    `source: ${report.source}${report.at ? ` (read at the code revision ${report.at})` : ''}`,
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
  // One line for every config-driven rule that fired, never one per rule: the
  // config a run read is the one in its own checkout, and a value changed on the
  // main line is not in it. Saying so once is what lets a refusal be read as
  // "this branch's config", not as "the gate is blocked".
  if (report.results.some((r) => r.config && r.violations.length))
    lines.push('',
      'These read the config of this checkout: a newer one on the main line may differ,',
      'so bring the branch up to date with the main line where a value was changed there.');
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
    requireRegularFile(projectConfigPath, 'the project config');
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
  const files = ['check.mjs', 'check-commits.mjs', 'model.md', 'SKILL.md', 'integration.md', 'protocol.md', 'fixtures/config.json', 'fixtures/leaf-close.json'];
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
  // The close path of every leaf shape, walked end to end: no shape is left
  // without a legal state. A leaf with no stage closes at its merge, and one
  // filed with no parent never takes the two middle states at all, so the walk
  // carries the evidence rules' refusal of them as a step of its own.
  const walk = load('.', 'leaf-close.json');
  for (const c of walk.cases) {
    const bad = [];
    for (const [n, step] of c.steps.entries()) {
      const fired = evaluate(step.backlog, cfg, null).results.filter((r) => r.violations.length).map((r) => r.id).sort();
      const want = (step.fires || []).slice().sort();
      if (fired.join(',') !== want.join(','))
        bad.push(`step ${n + 1} (${step.name}) fires [${fired.join(', ') || 'nothing'}], expected [${want.join(', ') || 'nothing'}]`);
    }
    say(!bad.length, `leaf-close/${c.shape} (${c.steps.length} step(s)) -- ${bad.join('; ') || 'all clean'}`);
  }
  const covered = new Set(readdirSync(join(dir, 'bad')).map((n) => n.split('.')[0]));
  for (const c of CHECKS) if (!covered.has(c.id)) say(false, `no fixture for check ${c.id}`);

  // The purpose command is proven on its own fixture: the exact lines a brief
  // carries, the vision's own wording and all. It reads and judges nothing, so
  // its proof is that the text it prints is the text the record holds.
  const purpose = load('.', 'purpose.json');
  for (const c of purpose.cases) {
    const got = describePurpose(purposeOf(purpose.backlog, c.task));
    say(got === c.expect.join('\n'), `purpose/${c.task} → ${got === c.expect.join('\n') ? 'the lines a brief carries' : `got ${JSON.stringify(got)}`}`);
  }
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
  // Work in flight at adoption is exempt, and so is what it was split into;
  // other unclaimed work is still named.
  const unclaimed = load('bad', 'time-work-unclaimed.json');
  const named = (exempt) => evaluate(unclaimed, { ...cfg, timeRecordsExempt: exempt }, ['time-work-unclaimed']).results[0].violations.length;
  say(named(['A']) === 0 && named(['S']) === 0 && named(['B']) === 1, 'time records: work listed at adoption, and its descendants, are exempt; nothing else is');
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
  for (const [what, want, argv, speaks] of [
    ['an unknown --only', 2, ['--config', fixtureCfg, '--strength', 'block', '--only', 'no-such-check', stale]],
    ['an empty backlog file', 2, ['--config', fixtureCfg, '--strength', 'block', put('empty.json', '')]],
    ['a config that is not an object', 2, ['--config', put('cfg.json', '[]'), '--strength', 'block', stale]],
    ['a record comment without its tracker id', 2, ['--config', fixtureCfg, '--strength', 'block', put('noid.json', { issues: [{ id: 'A', status: 'closed', comments: [{ at: '2026-01-01T00:00:00Z', author: 'x', body: '[shady2k-time v1] claim' }] }] })]],
    ['a live issue with an unreadable date', 2, ['--config', fixtureCfg, '--strength', 'block', put('nodate.json', { issues: [{ id: 'A', status: 'active', updatedAt: 'not-a-date' }] })]],
    ['no strength chosen anywhere', 2, ['--config', fixtureCfg, stale]],
    ['an unpaired age snapshot', 2, ['--config', fixtureCfg, '--strength', 'block', '--ages-from', stale, stale]],
    ['a config with only a strength, on a violating backlog', 1, ['--config', put('min.json', { strength: 'block' }), stale]],
    // The store lives outside the code branches, so a read at a code revision
    // is the only place the gate can still tell "the tracker at this revision"
    // from a state that was read for another one or for none. Both failures are
    // misuse, and the read that does say which revision it stands for passes.
    ['a read at a revision with no revision in the source', 2, ['--config', fixtureCfg, '--strength', 'block', '--at', 'rev-a', stale]],
    ['a read at a revision that read another one', 2, ['--config', fixtureCfg, '--strength', 'block', '--at', 'rev-a', put('at-other.json', { ...load('bad', 'stale-hold.json'), source: 'fixture bad/stale-hold @ rev-b' })]],
    ['a read at the revision it stands for', 1, ['--config', fixtureCfg, '--strength', 'block', '--at', 'rev-a', put('at-same.json', { ...load('bad', 'stale-hold.json'), source: 'fixture bad/stale-hold @ rev-a' })]],
    ['an empty requested revision', 2, ['--config', fixtureCfg, '--strength', 'report', '--at', '', stale], '--at'],
    ['a whitespace-only requested revision', 2, ['--config', fixtureCfg, '--strength', 'report', '--at', '   ', stale], '--at'],
    ['a missing requested revision', 2, ['--config', fixtureCfg, '--strength', 'report', stale, '--at'], '--at'],
    ['an option instead of the requested revision', 2, ['--config', fixtureCfg, '--strength', 'report', stale, '--at', '--json'], '--at'],
    // Every other option that takes a value, one case each: what a bare
    // `argv[++i]` did with it is in the case's name.
    ['an empty --only, which today names no check at all', 2, ['--config', fixtureCfg, '--strength', 'block', '--only', '', stale], '--only'],
    ['a missing --only, which today is a trailing option that crashes', 2, ['--config', fixtureCfg, '--strength', 'block', stale, '--only'], '--only'],
    ['an empty --config, which today is a config that was not given', 2, ['--config', '', '--strength', 'block', stale], '--config'],
    ['an empty --baseline, which today is no baseline at all', 2, ['--config', fixtureCfg, '--strength', 'block-new', '--baseline', '', stale], '--baseline'],
    ['an empty --baseline-config, which today judges the baseline by today\'s rules', 2, ['--config', fixtureCfg, '--strength', 'block-new', '--baseline', stale, '--baseline-config', '', stale], '--baseline-config'],
    ['an empty --strength, which today is a strength that was not given', 2, ['--config', fixtureCfg, '--strength', '', stale], '--strength'],
    ['an empty --ages-from, which today skips the ages check and answers green or red without it', 2, ['--config', fixtureCfg, '--strength', 'block', '--ages-from', '', stale], '--ages-from'],
    ['an empty --ages-through, which today skips the ages check and answers green or red without it', 2, ['--config', fixtureCfg, '--strength', 'block', '--ages-through', '', stale], '--ages-through'],
    ['an empty --purpose, which today falls back to a normal run', 2, ['--config', fixtureCfg, '--strength', 'block', '--purpose', '', stale], '--purpose'],
    ['an option instead of the task --purpose asks for', 2, ['--purpose', '--json', stale], '--purpose'],
    ['an option instead of the --config value', 2, ['--config', '--strength', 'block', stale], '--config'],
  ]) {
    const done = run(...argv);
    const got = done.status;
    // A row may name the option its refusal has to speak of: misuse exits 2, and
    // it must name the option, or the exit could have come from anywhere else.
    const says = `${done.stdout}${done.stderr}`;
    const names = speaks === undefined || says.includes(`"${speaks}"`);
    say(got === want && names, `cli: ${what} → exit ${got}${got === want ? '' : `, expected ${want}`}${names ? '' : `, and its refusal does not name ${speaks}`}`);
  }
  // Lowering the budget creates violations. Judged by today's config the
  // baseline has them too and block-new is green; judged by its own it is not.
  const roomy = put('roomy.json', { ...cfg, findingBudget: 9, strength: 'block-new' });
  const tight = put('tight.json', { ...cfg, strength: 'block-new' });
  const blind = run('--config', tight, '--baseline', overBudget, overBudget).status;
  const sighted = run('--config', tight, '--baseline', overBudget, '--baseline-config', roomy, overBudget).status;
  say(blind === 0 && sighted === 1, `cli: a lowered budget is old debt without --baseline-config (exit ${blind}) and a new error with it (exit ${sighted})`);

  // What a person reads when a rule refuses: the rule's own fix text has to
  // name the repair. An unreceipted span's names the run script's `gaps`, the
  // one command that recovers a receipt this machine can still read.
  const shown = (name, only) => render(evaluate(load('bad', name), cfg, only));
  const unreceipted = shown('time-span-unreceipted.json', ['time-span-unreceipted']);
  say(/fix: .*`gaps`/.test(unreceipted), "an unreceipted span's refusal names the run script's `gaps`, the command that recovers it");
  // A verdict that read a config value says which config it read: this
  // checkout's own, which a change on the main line does not reach.
  const budget = shown('finding-budget.json', ['finding-budget']);
  say(/config of this checkout/.test(budget) && /main line/.test(budget),
    'a config-driven refusal says it judged by this checkout\'s config, and that the main line may differ');
  say(!/config of this checkout/.test(unreceipted), 'a rule whose verdict reads no config carries no such note');

  console.log(failures ? `\n${failures} failure(s)` : '\nall fixtures pass');
  return failures ? 1 : 0;
}

// argv.mjs refuses a value that cannot be one; this gate says so its own way,
// which is the misuse exit 2 with the usage.
const value = (argv, i, option, what) => {
  try {
    return valueOf(argv, i, option, what);
  } catch (e) {
    if (e instanceof ValueNeeded) throw new Misuse(e.message);
    throw e;
  }
};

function main() {
  const argv = process.argv.slice(2);
  const args = { json: false, only: null, input: null, selftest: false, config: null, baseline: null, baselineConfig: null, strength: null, agesFrom: null, agesThrough: null, at: null, purpose: null };
  for (let i = 0; i < argv.length; i++) {
    if (argv[i] === '--json') args.json = true;
    // Every option that takes a value reads it through argv.mjs: an empty,
    // blank, missing or option-shaped word is misuse and exits 2, where a bare
    // `argv[++i]` would have handed the run a value it could only ignore --
    // `--only ''` a filter no rule matches, `--at ''` no revision at all.
    else if (argv[i] === '--only') { args.only = value(argv, i, '--only', 'a comma-separated list of check ids').split(','); i++; }
    else if (argv[i] === '--config') { args.config = value(argv, i, '--config', 'a project config'); i++; }
    else if (argv[i] === '--baseline') { args.baseline = value(argv, i, '--baseline', 'a baseline backlog'); i++; }
    else if (argv[i] === '--baseline-config') { args.baselineConfig = value(argv, i, '--baseline-config', 'a baseline config'); i++; }
    else if (argv[i] === '--strength') { args.strength = value(argv, i, '--strength', 'block, block-new or report'); i++; }
    else if (argv[i] === '--ages-from') { args.agesFrom = value(argv, i, '--ages-from', 'the snapshot before the bulk edit'); i++; }
    else if (argv[i] === '--ages-through') { args.agesThrough = value(argv, i, '--ages-through', 'the snapshot after the bulk edit'); i++; }
    else if (argv[i] === '--at') { args.at = value(argv, i, '--at', 'a code revision'); i++; }
    else if (argv[i] === '--purpose') { args.purpose = value(argv, i, '--purpose', 'a task id'); i++; }
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
        '          [--at <code revision>]\n' +
        '          [--only <id,id>] [--json]\n' +
        'check.mjs --purpose <task id> <normalized.json>\n' +
        'check.mjs --selftest [--config <project config>]\n' +
        'check.mjs --version\n\n' +
        'Applies the backlog invariants to the normalized backlog on stdin or at <path>.\n' +
        'Strength comes from the config unless given; block-new needs --baseline.\n' +
        '--ages-from and --ages-through correct only timestamps still matching the bulk edit.\n' +
        '--at says which code revision the input was read for, and refuses one read\n' +
        'for another: the store is off the branches, so the input is the only thing\n' +
        'that can say which revision it stands for.\n' +
        '--baseline-config judges the baseline by the rules of its own day, so a\n' +
        'config change that creates violations shows up as new errors.\n' +
        '--purpose prints the purpose of one task for the brief of whoever does it:\n' +
        'the vision items it serves in their current wording, whom it serves and where\n' +
        'a person sees it. One line per fact, and no config: it judges nothing.\n' +
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

  // The purpose of a task is read, never judged: it needs the backlog and
  // nothing else, so a brief can be written in a project whose gate is red.
  if (args.purpose) {
    console.log(describePurpose(purposeOf(checkedBacklog(readJson(args.input, 'the backlog'), 'backlog'), args.purpose)));
    return 0;
  }
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
  const backlog = checkedBacklog(readJson(args.input, 'the backlog'), 'backlog');
  if (args.at) atRevision(backlog, args.at, 'the backlog');
  const report = evaluate(withAgesFrom(backlog, ages, after), cfg, args.only);
  if (args.at) report.at = args.at;
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

// Imported by another checker, this file runs nothing; run directly, it
// behaves exactly as before.
if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    process.exitCode = main();
  } catch (e) {
    // Anything unforeseen is misuse too: exit 1 means "the backlog is red", and
    // a crash has established no such thing.
    console.error(`check.mjs: ${e instanceof Misuse ? e.message : e.stack}`);
    process.exitCode = 2;
  }
}
