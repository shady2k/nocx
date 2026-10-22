#!/usr/bin/env node
// Project adapters parse messages; this check validates their normalized links.
// Three optional input fields come from the project's adapter, never guessed:
// - top-level `codePaths`: the path prefixes the project's config marks as code.
//   A changed path is code when it starts with one of these prefixes; anything
//   not under one is not code, so docs, the tracker and the records themselves
//   are never code. Empty or absent means nothing is code.
// - each commit's `paths`: the paths this commit changed, from the diff.
// - each commit's `changes`: one entry per change record judged at THAT
//   commit's revision: `{ id, taskIds, violations }` — the tasks it names and
//   the rule ids the document gate (check-docs.mjs) returned for it there.
//   An empty violations list means admitted. An admission verdict is trusted
//   only from a document-gate run whose model carries a non-null change record
//   in the feature, acceptance or close phase: the product phase returns no
//   violations and admits nothing, so its output is not an admission.
import { readFileSync, readdirSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { hasCriterion } from './check.mjs';

const RULES_VERSION = '0.51.0';
const HERE = dirname(fileURLToPath(import.meta.url));

// Text that shapes nothing is not an understanding: a criterion made only of
// scaffold markers is a heading somebody meant to fill in.
const placeholder = (s) => /\b(TODO|TBD|TKTK)\b|\[NEEDS CLARIFICATION|<[^>]+>/i.test(s);

// The criterion text sits after one of the gate's own markers, before the next
// heading; a criterion counts as written only when a readable slice of it
// carries no scaffold marker.
const criterionIsPlaceholder = (issue) => {
  const b = (issue.body || '').toLowerCase();
  const markers = ['## success criteria', '## done when', 'done when', 'falsified if', '## acceptance criteria'];
  const slices = [];
  for (const mk of markers) {
    let at = b.indexOf(mk);
    while (at >= 0) { slices.push(b.slice(at + mk.length).split(/\n\s*#/)[0]); at = b.indexOf(mk, at + 1); }
  }
  return slices.length > 0 && slices.every(placeholder);
};

function evaluate(input) {
  if (!input || !Array.isArray(input.issues) || !Array.isArray(input.commits) || !input.commits.length)
    throw new Error('input needs issues and a nonempty commits list');
  if (input.codePaths !== undefined) {
    if (!Array.isArray(input.codePaths) || input.codePaths.some((p) => typeof p !== 'string' || !p.trim()))
      throw new Error('codePaths must be an array of nonempty strings');
  }
  if (input.changes !== undefined) // top-level list is not read: the verdict lives on the commit
    throw new Error('a top-level changes list is not read: change records belong on each commit, judged at its revision');
  const issues = new Map();
  for (const issue of input.issues) {
    if (!issue || typeof issue.id !== 'string' || !issue.id.trim() ||
        typeof issue.type !== 'string' || !['epic', 'task', 'bug', 'chore', 'other'].includes(issue.type) || issues.has(issue.id))
      throw new Error('issues need unique ids and normalized types');
    issues.set(issue.id, issue);
  }
  const parents = new Set(input.issues.map((i) => i.parent).filter(Boolean));
  // A leaf is under a feature stage when its ancestor chain contains an epic.
  // Structural, not "has a parent": a bug nested under a chore stands alone.
  const underFeatureStage = (id) => {
    let issue = issues.get(id);
    const walked = new Set();
    while (issue && issue.parent && !walked.has(issue.parent)) {
      walked.add(issue.parent);
      const parent = issues.get(issue.parent);
      if (!parent) break;
      if (parent.type === 'epic') return true;
      issue = parent;
    }
  };
  const isCodePath = (pt) => (input.codePaths ?? []).some((prefix) => pt.startsWith(prefix));
  const seen = new Set();
  const violations = [];
  for (const commit of input.commits) {
    if (!commit || typeof commit.id !== 'string' || !commit.id.trim() || seen.has(commit.id) ||
        !Array.isArray(commit.taskIds) || commit.taskIds.some((id) => typeof id !== 'string' || !id.trim()))
      throw new Error('commits need unique ids and a taskIds list of nonempty strings');
    if (commit.recordsOnly !== undefined && typeof commit.recordsOnly !== 'boolean')
      throw new Error('a commit\'s recordsOnly flag must be true or false');
    if (commit.paths !== undefined &&
      (!Array.isArray(commit.paths) || commit.paths.some((x) => typeof x !== 'string' || !x.trim())))
      throw new Error('a commit\'s paths must be an array of nonempty strings');
    // A project that has declared what counts as code has declared that this
    // check can see it: a commit that carries no changed paths is an input this
    // check cannot read, not a commit that changed nothing. Leaving it out
    // would turn the whole rule off silently, which is the one thing a gate may
    // not do.
    if ((input.codePaths ?? []).length && !Array.isArray(commit.paths))
      throw new Error(`commit ${commit.id} carries no paths, and codePaths is declared: the adapter must list what each commit changed, or the rule that reads it is off`);
    if (commit.changes !== undefined) {
      if (!Array.isArray(commit.changes)) throw new Error('a commit\'s changes must be an array of change records');
      for (const c of commit.changes)
        if (!c || typeof c.id !== 'string' || !c.id.trim() || !Array.isArray(c.taskIds) ||
            c.taskIds.some((id) => typeof id !== 'string' || !id.trim()) || !Array.isArray(c.violations) ||
            c.violations.some((v) => typeof v !== 'string' || !v.trim()))
          throw new Error('each change record needs an id, a taskIds list and a violations list of nonempty strings');
    }
    seen.add(commit.id);
    if (!commit.taskIds.length) violations.push({ id: commit.id, task: null, reason: 'no task link' });
    const commitCode = Array.isArray(commit.paths) && commit.paths.some(isCodePath);
    // A commit that carries only records of what already happened delivers
    // nothing to the work it names: the records belong with the landing they
    // describe, and a commit of their own is bookkeeping the owner's watch
    // reads as progress. The adapter knows the message and the paths, so it
    // marks the commit; the checker refuses it.
    if (commit.recordsOnly) violations.push({ id: commit.id, task: null, reason: 'carries only records; they ride with the landing' });
    for (const id of new Set(commit.taskIds)) {
      const issue = issues.get(id);
      if (!issue) violations.push({ id: commit.id, task: id, reason: 'task does not exist' });
      else if (issue.type === 'epic' || parents.has(id))
        violations.push({ id: commit.id, task: id, reason: 'link must name a leaf task, not a container' });
      else if (commitCode) {
        // A leaf whose ancestor chain holds no epic stands on its own (a bug
        // or chore of its own): its understanding is a criterion on itself,
        // checked in the body, and it must not be a scaffold placeholder.
        // A leaf under a feature stage is understood when an admitted change
        // record names it — a record judged at this commit's revision whose
        // violations list is empty.
        if (underFeatureStage(id)) {
          const records = (commit.changes ?? []).filter((c) => c.taskIds.includes(id));
          if (!records.length)
            violations.push({ id: commit.id, task: id, reason: 'changes code before the task\'s understanding is admitted: no admitted change record names it' });
          else if (!records.some((c) => c.violations.length === 0))
            violations.push({ id: commit.id, task: id, reason: `changes code before the task\'s understanding is admitted: its change records were refused for ${[...new Set(records.flatMap((c) => c.violations))].join(', ')}` });
        } else if (!hasCriterion(issue) || criterionIsPlaceholder(issue))
          violations.push({ id: commit.id, task: id, reason: 'changes code before the task\'s understanding is written: the standalone task carries no criterion a person can read to see what it changes' });
      }
    }
  }
  return { checked: input.commits.length, violations };
}

function result(input) {
  try {
    const report = evaluate(input);
    return { code: report.violations.length ? 1 : 0, report };
  } catch (error) {
    return { code: 2, report: { error: error.message } };
  }
}

function main() {
  const args = process.argv.slice(2);
  if (args.length === 1 && args[0] === '--version') { console.log(RULES_VERSION); return 0; }
  if (args.length === 1 && args[0] === '--help') {
    console.log('check-commits.mjs [<normalized.json>|-]\ncheck-commits.mjs --selftest\nExit 0 linked, 1 missing/invalid links, 2 invalid input.\n\nOptional input from the project adapter, never guessed:\n- codePaths (top level): path prefixes the project config marks as code;\n  a path not under one is never code. Empty or absent means nothing is code.\n- commits[].paths: the paths the commit changed, from the diff.\n- commits[].changes: one entry per change record judged at THAT commit\'s\n  revision: { id, taskIds, violations } with the rule ids the document gate\n  returned; empty violations means admitted. Trust a verdict only from a gate\n  run whose model carries a non-null change record in the feature, acceptance\n  or close phase: the product phase returns no violations and admits nothing.');
    return 0;
  }
  if (args.length === 1 && args[0] === '--selftest') {
    let failures = 0;
    for (const name of readdirSync(join(HERE, 'fixtures', 'commits')).sort()) {
      const fixture = JSON.parse(readFileSync(join(HERE, 'fixtures', 'commits', name), 'utf8'));
      const got = result(fixture.input);
      const ok = got.code === fixture.exit && (fixture.violations === undefined ||
        JSON.stringify(got.report.violations) === JSON.stringify(fixture.violations));
      console.log(`${ok ? 'PASS' : 'FAIL'}  commits/${name}: exit ${got.code}`);
      if (!ok) failures++;
    }
    return failures ? 1 : 0;
  }
  if (args.length > 1 || (args[0]?.startsWith('-') && args[0] !== '-')) throw new Error('unknown arguments; use --help');
  const input = JSON.parse(readFileSync(!args[0] || args[0] === '-' ? 0 : args[0], 'utf8'));
  const output = result(input);
  console.log(JSON.stringify(output.report, null, 2));
  return output.code;
}

try { process.exitCode = main(); }
catch (error) { console.error(`check-commits.mjs: ${error.message}`); process.exitCode = 2; }
