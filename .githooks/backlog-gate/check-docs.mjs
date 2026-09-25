#!/usr/bin/env node
// Pure rules over deterministic project exports; see documents.md for the trust boundary.
import { createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

export const RULES_VERSION = '0.26.0';
const here = dirname(fileURLToPath(import.meta.url));
const object = (x) => x !== null && typeof x === 'object' && !Array.isArray(x);
const canonical = (x) => Array.isArray(x) ? x.map(canonical) : object(x)
  ? Object.fromEntries(Object.keys(x).sort().map((k) => [k, canonical(x[k])])) : x;
export const digest = (x) => createHash('sha256').update(JSON.stringify(canonical(x))).digest('hex');
const same = (a, b) => digest(a) === digest(b);
const ref = (capability, requirement) => `${capability}/${requirement}`;

// What an approval binds to: what the change COMMITS TO — its kind, its scope,
// the requirements it moves and what it promises to leave alone. The rest of
// the record is the agent's own working: the reasoning, which check covers
// which requirement, the task ids, the title. Digesting the whole record made
// an approval that could not exist until the record was finished and died on
// every later edit, so the one thing the preflight batch could never hold was
// the one thing that stopped the run.
export const decided = (change) => ({
  kind: change.kind, intent: change.intent, outOfScope: change.outOfScope,
  deltas: change.deltas, preserves: change.preserves,
});
const present = (s) => s.trim().length > 0 && !/\b(TODO|TBD|TKTK)\b|\[NEEDS CLARIFICATION|<[^>]+>/i.test(s);

// Closed schemas catch misspelled fields and wrong types before evaluating rules.
const array = (of) => ({ array: of });
const nullable = (of) => ({ nullable: of });
const enumeration = (...values) => ({ enum: values });
const optional = (of) => ({ optional: of });
const scenario = { id: 'id', given: 'string', when: 'string', then: 'string' };
const requirement = { id: 'id', title: 'string', statement: 'string', scenarios: array(scenario) };
const capability = { id: 'id', title: 'string', requirements: array(requirement) };
const reference = { capability: 'id', requirement: 'id' };
const changeSchema = {
  id: 'id', title: 'string', taskIds: array('id'), kind: enumeration('behavior', 'no-behavior', 'supporting'),
  intent: 'string', outOfScope: 'string', rationale: 'string', openQuestions: array('string'),
  deltas: array({ capability: 'id', requirement: 'id', before: nullable(requirement), after: nullable(requirement) }),
  preserves: array(reference),
  coverage: array({ ...reference, checks: array('id') }),
};
const modelSchema = {
  schemaVersion: enumeration(1), phase: enumeration('product', 'feature', 'acceptance', 'close'),
  revision: 'string',
  project: {
    mode: enumeration('new', 'existing'),
    vision: { audience: 'string', problem: 'string', outcome: 'string', exclusions: 'string' },
    milestone: { id: 'string', outcomes: array('string'), exclusions: 'string' },
  },
  tasks: array({ id: 'id', kind: enumeration('leaf', 'container') }),
  baseline: array(capability), current: array(capability), change: nullable(changeSchema),
};
const policySchema = {
  schemaVersion: enumeration(1), requireApproval: 'boolean',
  requiredChecks: array({
    id: 'id', kind: enumeration('static', 'test', 'mutation', 'review', 'manual'),
    appliesTo: optional(array(enumeration('behavior', 'no-behavior', 'supporting'))),
  }),
};
const evidenceSchema = {
  schemaVersion: enumeration(1), revision: 'string', policyDigest: 'string',
  approvals: array({ changeDigest: 'string', reference: 'string' }),
  checks: array({ id: 'id', status: enumeration('passed', 'failed', 'skipped', 'unsupported'), reference: 'string' }),
};

function schema(x, rule, path) {
  const fail = (why) => { throw new Error(`${path}: ${why}`); };
  if (rule === 'id') {
    if (typeof x !== 'string' || !/^[a-zA-Z0-9][a-zA-Z0-9_.:-]*$/.test(x)) fail('invalid identifier');
  } else if (typeof rule === 'string') {
    if (typeof x !== rule) fail(`expected ${rule}`);
  } else if (rule.enum) {
    if (!rule.enum.includes(x)) fail(`expected one of ${rule.enum.join(', ')}`);
  } else if (rule.nullable) {
    if (x !== null) schema(x, rule.nullable, path);
  } else if (rule.array) {
    if (!Array.isArray(x)) fail('expected array');
    x.forEach((v, i) => schema(v, rule.array, `${path}[${i}]`));
  } else {
    if (!object(x)) fail('expected object');
    for (const k of Object.keys(x)) if (!Object.hasOwn(rule, k)) fail(`unknown field ${k}`);
    for (const [k, v] of Object.entries(rule)) {
      if (!Object.hasOwn(x, k)) {
        if (v.optional) continue;
        fail(`missing field ${k}`);
      }
      schema(x[k], v.optional || v, `${path}.${k}`);
    }
  }
}

// A check that can refuse can describe green. Each verdict carries the shape
// the author has to reach, so a refusal is not a research task of its own.
const WHY = {
  'duplicate-id': 'two items share an identifier in one scope: give each its own',
  'unfinished-content': 'the text is empty or still a placeholder (TODO, TBD, angle brackets)',
  'missing-scenario': 'every requirement carries at least one scenario, with given, when and then',
  'empty-capability': 'a capability names at least one requirement',
  'empty-policy': 'the policy must require at least one check that applies to this kind of change',
  'missing-outcome': 'the milestone names at least one outcome',
  'missing-change': 'this phase needs a change record; only the product phase runs without one',
  'open-question': 'blocking questions are answered, or moved out of this change, before admission',
  'untracked-change': 'a change names existing leaf tasks: file or split the task first',
  'change-kind': 'behavior needs at least one delta; no-behavior needs preserved references and a rationale and no deltas; supporting needs a rationale and nothing else',
  'invalid-delta': 'a delta adds, removes or replaces one requirement: never both null, never a no-op, never a different id',
  'stale-base': 'the delta\'s before is the requirement as the target holds it now: read the baseline from the target, not from the proposal',
  'unknown-requirement': 'the reference names a requirement this baseline scope does not hold',
  'unsynced-current': 'current equals the accepted baseline before closure, and the replayed change at closure',
  'uncovered-requirement': 'every changed or preserved requirement names at least one check that proves it',
  'missing-approval': 'this change digest needs an approval carrying a reference',
  'stale-evidence': 'receipts must be for this revision and this policy: run the checks again on the current revision',
  'unproved-check': 'this check needs a receipt with status passed and a reference',
};

export function checkDocuments(model, policy, evidence = null) {
  schema(model, modelSchema, 'model');
  schema(policy, policySchema, 'policy');
  if (evidence !== null) schema(evidence, evidenceSchema, 'evidence');
  const violations = [];
  const check = (id, condition, at) => {
    if (condition) violations.push({ id, at, why: WHY[id] ?? null });
  };
  const unique = (items, at) => {
    const seen = new Set();
    for (const id of items) {
      check('duplicate-id', seen.has(id), `${at}/${id}`);
      seen.add(id);
    }
  };
  const required = (value, at) => check('unfinished-content', !present(value), at);
  const inspectRequirement = (r, at) => {
    required(r.title, `${at}/title`);
    required(r.statement, `${at}/statement`);
    check('missing-scenario', r.scenarios.length === 0, at);
    unique(r.scenarios.map((s) => s.id), at);
    for (const s of r.scenarios) for (const field of ['given', 'when', 'then']) required(s[field], `${at}/${s.id}/${field}`);
  };
  const snapshot = (caps, at) => {
    unique(caps.map((c) => c.id), at);
    const result = new Map();
    for (const cap of caps) {
      required(cap.title, `${at}/${cap.id}/title`);
      check('empty-capability', cap.requirements.length === 0, `${at}/${cap.id}`);
      unique(cap.requirements.map((r) => r.id), `${at}/${cap.id}`);
      for (const r of cap.requirements) {
        const key = ref(cap.id, r.id);
        inspectRequirement(r, `${at}/${key}`);
        result.set(key, r);
      }
    }
    return result;
  };

  // These are scoped snapshots, not a demand to document every legacy module.
  const baseline = snapshot(model.baseline, 'baseline');
  const current = snapshot(model.current, 'current');
  unique(model.tasks.map((t) => t.id), 'tasks');
  unique(policy.requiredChecks.map((c) => c.id), 'policy/checks');
  if (evidence) unique(evidence.checks.map((c) => c.id), 'evidence/checks');
  check('empty-policy', policy.requiredChecks.length === 0, 'policy/requiredChecks');
  required(model.revision, 'revision');
  if ((model.project.mode === 'new' && model.change?.kind !== 'supporting') || model.phase === 'product') {
    for (const [k, value] of Object.entries(model.project.vision)) required(value, `vision/${k}`);
    required(model.project.milestone.id, 'milestone/id');
    required(model.project.milestone.exclusions, 'milestone/exclusions');
    check('missing-outcome', model.project.milestone.outcomes.length === 0, 'milestone/outcomes');
    model.project.milestone.outcomes.forEach((o, i) => required(o, `milestone/outcomes/${i}`));
  }
  if (model.phase === 'product') return violations;
  check('missing-change', model.change === null, 'change');
  if (!model.change) return violations;
  const change = model.change;
  for (const k of ['title', 'intent', 'outOfScope']) required(change[k], `change/${k}`);
  check('open-question', change.openQuestions.length > 0, 'change/openQuestions');
  unique(change.taskIds, 'change/tasks');
  check('untracked-change', change.taskIds.length === 0 || change.taskIds.some((id) =>
    !model.tasks.some((t) => t.id === id && t.kind === 'leaf')), 'change/taskIds');
  check('change-kind', change.kind === 'behavior' ? change.deltas.length === 0
    : change.kind === 'no-behavior' ? change.deltas.length !== 0 || !present(change.rationale) || change.preserves.length === 0
      : change.deltas.length !== 0 || change.preserves.length !== 0 || change.coverage.length !== 0 || !present(change.rationale), 'change/kind');

  const affected = new Set();
  const expected = new Map(baseline);
  unique(change.deltas.map((d) => ref(d.capability, d.requirement)), 'change/deltas');
  for (const d of change.deltas) {
    const key = ref(d.capability, d.requirement);
    affected.add(key);
    check('invalid-delta', (!d.before && !d.after) || (d.before && d.before.id !== d.requirement)
      || (d.after && d.after.id !== d.requirement) || same(d.before, d.after), key);
    if (d.before) inspectRequirement(d.before, `before/${key}`);
    if (d.after) inspectRequirement(d.after, `after/${key}`);
    check('stale-base', !same(baseline.get(key) ?? null, d.before), key);
    if (d.after) expected.set(key, d.after); else expected.delete(key);
  }
  unique(change.preserves.map((r) => ref(r.capability, r.requirement)), 'change/preserves');
  for (const r of change.preserves) {
    const key = ref(r.capability, r.requirement);
    affected.add(key);
    check('unknown-requirement', !baseline.has(key), key);
    check('invalid-delta', change.deltas.some((d) => ref(d.capability, d.requirement) === key), key);
  }
  // Before closure, current still describes the accepted baseline. At closure it
  // must equal the replayed change, including preservation of untouched requirements.
  const wanted = model.phase === 'close' ? expected : baseline;
  for (const key of new Set([...wanted.keys(), ...current.keys()])) {
    check('unsynced-current', !same(wanted.get(key) ?? null, current.get(key) ?? null), key);
  }

  unique(change.coverage.map((r) => ref(r.capability, r.requirement)), 'change/coverage');
  for (const c of change.coverage) {
    const key = ref(c.capability, c.requirement);
    check('unknown-requirement', !affected.has(key), `coverage/${key}`);
    unique(c.checks, `coverage/${key}`);
  }
  for (const key of affected) {
    const coverage = change.coverage.find((c) => ref(c.capability, c.requirement) === key);
    check('uncovered-requirement', !coverage || coverage.checks.length === 0, key);
  }

  // A check scoped to other kinds of change is not required of this one, but
  // every change must still owe at least one check.
  const applicable = policy.requiredChecks.filter((c) => !c.appliesTo || c.appliesTo.includes(change.kind));
  check('empty-policy', applicable.length === 0, `policy/requiredChecks for ${change.kind}`);

  // Receipt verification belongs to the protected runner, not a field the author sets.
  if (policy.requireApproval) {
    check('missing-approval', !evidence?.approvals.some((a) => a.changeDigest === digest(decided(change))
      && present(a.reference)), 'change');
  }
  if (model.phase === 'acceptance' || model.phase === 'close') {
    check('stale-evidence', !evidence || evidence.revision !== model.revision
      || evidence.policyDigest !== digest(policy), 'evidence');
    const needed = new Set([...applicable.map((c) => c.id), ...change.coverage.flatMap((c) => c.checks)]);
    for (const id of needed) {
      const receipt = evidence?.checks.find((c) => c.id === id);
      check('unproved-check', !receipt || receipt.status !== 'passed' || !present(receipt.reference), id);
    }
  }
  return violations;
}

// Fixture patches mutate only an in-memory copy; shipped source is never edited.
export function fixtureCases() {
  const fixtures = JSON.parse(readFileSync(join(here, 'fixtures/documents.json'), 'utf8'));
  return fixtures.cases.map((c) => {
    const input = structuredClone(fixtures.valid);
    // 'pristine' records the approval as it would have been given BEFORE the
    // patches: the only way to ask whether an edit voids it.
    const approved = c.receipts === 'pristine' ? structuredClone(fixtures.valid) : input;
    for (const patch of c.patches) {
      const parts = patch.path.split('/');
      let owner = input;
      for (const part of parts.slice(0, -1)) owner = owner[part];
      const key = parts.at(-1);
      if (patch.delete) delete owner[key]; else owner[key] = patch.value;
    }
    // Fixtures may request fresh receipts, never production inputs.
    if (c.receipts) {
      input.evidence.policyDigest = digest(approved.policy);
      input.evidence.approvals = approved.model.change
        ? [{ changeDigest: digest(decided(approved.model.change)), reference: 'fixture:approval' }] : [];
    }
    return { ...c, ...input };
  });
}

export function selftest() {
  let failures = 0;
  const unexplained = new Set();
  for (const c of fixtureCases()) {
    let actual;
    try {
      const found = checkDocuments(c.model, c.policy, c.evidence);
      for (const v of found) if (!v.why) unexplained.add(v.id);
      actual = [...new Set(found.map((v) => v.id))].sort();
    } catch { actual = ['invalid-input']; }
    const ok = same(actual, [...c.expected].sort());
    console.log(`${ok ? 'PASS' : 'FAIL'}  documents/${c.name}: ${JSON.stringify(actual)}`);
    if (!ok) failures++;
  }
  // A refusal that names no remedy sends its reader to read the rules instead.
  const explained = unexplained.size === 0;
  console.log(`${explained ? 'PASS' : 'FAIL'}  documents/every verdict says what green looks like` +
    `${explained ? '' : `: ${[...unexplained].sort().join(', ')}`}`);
  if (!explained) failures++;
  return failures ? 1 : 0;
}

function main(args) {
  if (args.length === 1 && args[0] === '--version') { console.log(RULES_VERSION); return 0; }
  if (args.length === 1 && args[0] === '--selftest') return selftest();
  if (args.length === 1 && args[0] === '--help') {
    console.log('check-docs.mjs --input MODEL --policy POLICY [--evidence RECEIPTS] [--json]\nExit: 0 clean, 1 violations, 2 invalid input. See documents.md.');
    return 0;
  }
  const options = {};
  for (let i = 0; i < args.length; i++) {
    const key = args[i];
    if (!['--input', '--policy', '--evidence', '--json'].includes(key) || Object.hasOwn(options, key)) throw new Error(`unknown or repeated option: ${key}`);
    if (key === '--json') options[key] = true;
    else {
      const value = args[++i];
      if (!value || value.startsWith('--')) throw new Error(`missing value: ${key}`);
      options[key] = value;
    }
  }
  if (!options['--input'] || !options['--policy']) throw new Error('--input and --policy are required');
  const read = (p) => JSON.parse(readFileSync(p, 'utf8'));
  const model = read(options['--input']);
  const violations = checkDocuments(model, read(options['--policy']), options['--evidence'] ? read(options['--evidence']) : null);
  const c = model.change;
  const context = c
    ? `Change ${c.id}, kind ${c.kind}: ${c.deltas.length} delta(s), ${c.preserves.length} preserved, ` +
      `${c.coverage.length} covered; capabilities in the baseline: ${model.baseline.map((x) => x.id).join(', ') || 'none yet'}`
    : `Phase ${model.phase}, no change record`;
  console.log(options['--json'] ? JSON.stringify({ version: RULES_VERSION, violations })
    : violations.length
      ? [context, ...violations.map((v) => `${v.id}: ${v.at}${v.why ? ` \u2014 ${v.why}` : ''}`)].join('\n')
      : 'Document gate: clean');
  return violations.length ? 1 : 0;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try { process.exitCode = main(process.argv.slice(2)); }
  catch (error) { console.error(`Invalid document gate input: ${error.message}`); process.exitCode = 2; }
}
