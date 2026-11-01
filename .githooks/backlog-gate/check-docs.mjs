#!/usr/bin/env node
// Pure rules over deterministic project exports; see documents.md for the trust boundary.
import { createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { requireRegularFile } from './regular-file.mjs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { ValueNeeded, valueOf } from './argv.mjs';

export const RULES_VERSION = '0.59.0';
const here = dirname(fileURLToPath(import.meta.url));
const object = (x) => x !== null && typeof x === 'object' && !Array.isArray(x);
const canonical = (x) => Array.isArray(x) ? x.map(canonical) : object(x)
  ? Object.fromEntries(Object.keys(x).sort().map((k) => [k, canonical(x[k])])) : x;
export const digest = (x) => createHash('sha256').update(JSON.stringify(canonical(x))).digest('hex');
const same = (a, b) => digest(a) === digest(b);
const ref = (capability, requirement) => `${capability}/${requirement}`;

// What a receipt stands for in the policy. A check's result is a statement about
// what that check reads, so what can void it is that check's own definition and
// the policy fields that apply to every check — `appliesTo`, `ignores`, `fixture`,
// `kind` — never the rest of the required list: a change that adds a check asks
// for that check's receipt and leaves every other receipt standing, and a change
// to one check's entry asks again for that check alone. A policy-wide field
// (whether approval is required, whether the review answers the design match)
// applies to every check, so it voids every receipt. A check the policy does not
// require — one a change names in its own coverage — stands for its absence from
// the list, which adding or editing another check does not move.
export const policyWide = (policy) => {
  const { requiredChecks, ...wide } = policy;
  return wide;
};
export const policyScope = (policy, id) => digest({
  wide: digest(policyWide(policy)),
  check: policy.requiredChecks.find((c) => c.id === id) ?? null,
});

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
// What a judgement stands for: the design as a whole — what the change decides,
// and the constraints and kept principles the design answers with. An approval
// and a judgement answer different questions, so they bind different digests:
// editing a constraint voids the judgement and leaves the approval standing.
// The design as the gate stands behind it, and the text a design judgement's
// quotes are searched in: one projection, so the digest and the search cannot
// drift apart.
export const judgedText = (change) => ({
  decided: decided(change), constraints: change.constraints ?? [], principles: change.principles ?? [],
});
export const judged = (change) => digest(judgedText(change));
const present = (s) => s.trim().length > 0 && !/\b(TODO|TBD|TKTK)\b|\[NEEDS CLARIFICATION|<[^>]+>/i.test(s);

// A judgement's quote is a passage of the document it judged, not a description
// of one, and the gate finds it without a model. What it searches is the text
// the record is bound to — the same object whose digest the record has to carry:
// the charter for a charter judgement, and for a design the projection `judged`
// hashes. So a passage that is found is a passage at the bound revision by
// construction, and a quote from anything the digest does not stand for (a
// rationale, a task id) is refused instead of taken on somebody's word. The
// document is never read a second time, in the tree or from a copy: a second
// copy would be a second owner. Whitespace is collapsed on both sides, because
// where a line breaks is not part of a passage; nothing else is forgiven,
// because a quote that is nearly there is not in the document.
export const QUOTE_MIN_CHARS = 20;
export const QUOTE_MIN_WORDS = 3;
const normalize = (s) => s.replace(/\s+/g, ' ').trim();
// A word is what a person would count as one: a space-separated token that
// carries a letter. An identifier is one word however many fragments it joins,
// and a run of dots or markers is none.
const wordsIn = (s) => s.split(' ').filter((w) => /\p{L}/u.test(w)).length;
const passages = (x) => typeof x === 'string' ? [x] : Array.isArray(x) ? x.flatMap(passages)
  : object(x) ? Object.values(x).flatMap(passages) : [];
// Two minimums, because length alone does not measure whether a passage can be
// located: punctuation, a marker line or a run of dots reaches any length, and a
// bare id is one word however long it is. One word, one identifier, a
// placeholder or a dotted line locates nothing; an ordinary clause passes both.
const quoteFound = (quote, judged) => {
  const wanted = normalize(quote);
  return wanted.length >= QUOTE_MIN_CHARS && wordsIn(wanted) >= QUOTE_MIN_WORDS
    // One field is one passage: a quote spliced across two fields is in neither.
    && passages(judged).some((t) => normalize(t).includes(wanted));
};

// Closed schemas catch misspelled fields and wrong types before evaluating rules.
const array = (of) => ({ array: of });
const nullable = (of) => ({ nullable: of });
const enumeration = (...values) => ({ enum: values });
const optional = (of) => ({ optional: of });
const scenario = { id: 'id', given: 'string', when: 'string', then: 'string' };
const requirement = { id: 'id', title: 'string', statement: 'string', scenarios: array(scenario) };
const capability = { id: 'id', title: 'string', requirements: array(requirement) };
const reference = { capability: 'id', requirement: 'id' };
// A constraint the design states: a limit, an invariant, a "never". It is one
// closed section of the design record, each entry with an id, and each binds a
// requirement the change carries, so the coverage rules already judge whether a
// check proves it. A constraint about scale names its size, and the check that
// proves it declares the size of the fixture it runs on — the gate compares the
// two numbers itself, in their own unit.
const constraint = {
  id: 'id', statement: 'string', requirement: reference,
  scale: optional({ size: 'number', unit: 'string' }),
  principle: optional('id'),
};
const changeSchema = {
  id: 'id', title: 'string', taskIds: array('id'), kind: enumeration('behavior', 'no-behavior', 'supporting'),
  intent: 'string', outOfScope: 'string', rationale: 'string', openQuestions: array('string'),
  deltas: array({ capability: 'id', requirement: 'id', before: nullable(requirement), after: nullable(requirement) }),
  preserves: array(reference),
  coverage: array({ ...reference, checks: array('id') }),
  // Where the change was seen at: the walking evidence that it is real where a
  // person reaches it. See documents.md, the change record's whereSeen section.
  whereSeen: optional({
    kind: enumeration('screen', 'command', 'request', 'log', 'internal'),
    place: 'string', steps: array('string'), mockup: optional('string'), internalReason: optional('string'),
  }),
  // The design's constraints, and the kept principles it answers to (see the
  // constraint shape above and documents.md).
  constraints: optional(array(constraint)),
  principles: optional(array('id')),
};
const modelSchema = {
  schemaVersion: enumeration(1), phase: enumeration('product', 'feature', 'acceptance', 'close'),
  revision: 'string',
  // A check that says what it does not read is pinned to a revision of its
  // own inputs; the wrapper computes each (document-format.mjs, checkRevision).
  checkRevisions: optional(array({ id: 'id', revision: 'string' })),
  // The mockup files the adapter found in the tree at this revision (an image,
  // an HTML file, or a prototype document), so a screen's mockup is judged
  // without the check touching the filesystem itself.
  mockups: optional(array('string')),
  project: {
    mode: enumeration('new', 'existing'),
    vision: {
      audience: 'string', problem: 'string', outcome: 'string', exclusions: 'string',
      // The owner's kept principles, each with a stable id, as the vision record
      // holds them. Present means the project keeps them: a design then answers
      // to at least one, and each it names is one of its constraints.
      // A journey and a principle are both a title: the owner's own wording,
      // which every document below names by id and never copies. A judgement
      // names either, so the export carries both.
      journeys: optional(array({ id: 'id', title: 'string' })),
      principles: optional(array({ id: 'id', title: 'string' })),
    },
    milestone: { id: 'string', outcomes: array('string'), exclusions: 'string' },
  },
  tasks: array({ id: 'id', kind: enumeration('leaf', 'container') }),
  baseline: array(capability), current: array(capability), change: nullable(changeSchema),
};
const policySchema = {
  schemaVersion: enumeration(1), requireApproval: 'boolean',
  // The review's answer about the design is part of what this project requires
  // of a review. The switch is the project's, so an installation that already
  // has review receipts is not stopped the day the rule ships.
  requireDesignMatch: optional('boolean'),
  requiredChecks: array({
    id: 'id', kind: enumeration('static', 'test', 'mutation', 'review', 'manual'),
    appliesTo: optional(array(enumeration('behavior', 'no-behavior', 'supporting'))),
    ignores: optional(array('string')),
    // The fixture this check runs on. A constraint that states a scale is proved
    // only by a check whose fixture is at least that size, in the same unit.
    fixture: optional({ size: 'number', unit: 'string' }),
  }),
};
const evidenceSchema = {
  schemaVersion: enumeration(1), revision: optional('string'),
  // The whole-policy digest an installation wrote before a receipt could stand
  // for its own check's definition. Read only for a receipt that carries none.
  policyDigest: optional('string'),
  approvals: array({ changeDigest: 'string', reference: 'string' }),
  checks: array({
    id: 'id', status: enumeration('passed', 'failed', 'skipped', 'unsupported'), reference: 'string', revision: optional('string'),
    // What this receipt stands for in the policy: its own check's entry and the
    // policy-wide fields (policyScope), never the whole required list.
    policyDigest: optional('string'),
    // What the review answered about the build against the design.
    designMatch: optional({ matches: enumeration('yes', 'no'), divergences: array('string') }),
  }),
  // The boundary judgement: a different model's answer, on the revision it
  // answered about, to which journeys and principles this document serves and
  // which it breaks. One record per boundary, never per commit.
  // Each entry is a claim about one of the owner's journeys or principles that
  // the record answers with, and the passage it was read from; both are checked
  // (inspectJudgement): the ids against the vision, the passages against the
  // judged document itself.
  judgement: optional({
    charterDigest: optional('string'), designDigest: optional('string'),
    answeredBy: 'string', serves: array({ id: 'id', quote: 'string' }), breaks: array({ id: 'id', quote: 'string' }),
  }),
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
  'missing-observation': 'a change is seen where a person reaches it: give whereSeen a kind (screen, command, request, log or internal), the place, at least one step a person follows — and an internal change, the reason nothing a person reaches changes',
  'missing-mockup': 'a screen change is judged beside its drawing: name a mockup the tree has (figure it in, or point at a file the adapter already listed in mockups)',
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
  'stale-evidence': 'a receipt stands for the revision of what its check reads, under this policy: run that check again on the current revision',
  'unpinned-check': 'the policy says what this check does not read, so the export carries a revision of its own inputs for it (checkRevisions): the wrapper computes it with checkRevision',
  'unproved-check': 'this check needs a receipt with status passed and a reference',
  'unknown-constraint': 'the design states a constraint on a requirement this change neither moves nor promises to leave alone: a constraint binds a requirement the change carries',
  'small-fixture': 'the design states a limit of scale and the check that proves it declares no fixture size, a different unit, or a smaller one. Green looks like this: that check declares a fixture at least the size the constraint names, in the same unit',
  'unknown-principle': 'the design names a principle the vision record does not declare, so what it answers to is nobody\'s kept principle: name an id the vision keeps',
  'principle-without-constraint': 'a kept principle this design names carries no constraint, so nothing in the build is held to it: give the principle a constraint, and the constraint a check',
  'missing-principle': 'a change to what the product does answers to none of the principles the vision keeps: name the ones this area serves',
  'missing-design-match': 'the review that admitted this change did not answer whether the build matches the design: the review record carries "matches the design: yes/no, divergences"',
  'design-divergence': 'the review answered that the build does not match the design. Green looks like this: the divergence is decided — the build changed to the design, or the design changed and was approved again — and no divergence is left named',
  'missing-judgement': 'no different model\'s judgement stands on this document\'s current revision: which journeys and principles of the vision it serves and which it breaks, each with the passage it was read from. An edit needs a new record; one is asked for at a boundary, never per commit',
  'unknown-vision-id': 'a judgement\'s entry names one of the owner\'s journeys or principles, because what this document serves or breaks is one of his own: an id the vision record does not declare names nothing, so no one can read the claim. Green looks like this: name an id the vision record carries, and say which passage of the document the claim rests on',
  'unfound-quote': 'a judgement\'s quote is a passage of the document it judged — found verbatim in the text the record is bound to: a charter\'s outcomes and exclusions, or what a design decides, its constraints and the principles it names — with only whitespace forgiven, and at least 20 characters and three words long, so that a word, a bare id, a line of markers or a description of a passage is not passed off as one. Green looks like this: copy the sentence out of the document instead of describing it',
};
// Two refusals say their likely cause when the state at hand shows it, and only
// then. The refusal is the same one and the condition is unchanged: what green
// looks like is said beside it, because a delta that adds what the base already
// holds, and documents standing at the change's end outside closure, are states
// whose cause is one thing and not a search.
const STALE_BASE_ALREADY_THERE = 'the requirement the delta adds already exists where the change is judged from, so the commit that added it is likely not counted as this change\'s: it names another task, or this change\'s commits interleave with another\'s. Green looks like this: the delta\'s before is that requirement as the base holds it, or the range the push judges reaches the commit that added it';
const UNSYNCED_CURRENT_AT_THE_END = 'current already stands where the change ends, and closing is the phase that accepts that: run the close phase, or keep current at the baseline until then';
// A mutation run measures how strong the tests are; it never decides whether
// the change is correct. What its run gave to this revision is reported and
// never refused, and its survivors are filed tasks, not a verdict it passes.
const NOT_MADE = 'a mutation run measures how strong the tests are, it never refuses a change: record what happened, file its survivors as tasks';

export function checkDocuments(model, policy, evidence = null) {
  schema(model, modelSchema, 'model');
  schema(policy, policySchema, 'policy');
  if (evidence !== null) schema(evidence, evidenceSchema, 'evidence');
  const violations = [];
  // `why` overrides the rule's standing wording where the state at hand shows a
  // likely cause. It says the cause, never waives the refusal: same rule, same
  // condition, same verdict.
  const check = (id, condition, at, why) => {
    if (condition) violations.push({ id, at, why: why ?? WHY[id] ?? null });
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
    // The vision's own text; its principles are objects and are judged below.
    for (const k of ['audience', 'problem', 'outcome', 'exclusions']) required(model.project.vision[k], `vision/${k}`);
    required(model.project.milestone.id, 'milestone/id');
    required(model.project.milestone.exclusions, 'milestone/exclusions');
    check('missing-outcome', model.project.milestone.outcomes.length === 0, 'milestone/outcomes');
    model.project.milestone.outcomes.forEach((o, i) => required(o, `milestone/outcomes/${i}`));
  }
  // The kept principles of the vision record, each with a stable id, and the
  // judgement the charter answers to at its own acceptance. The principles list
  // is the switch: a project that keeps none is judged by none of this.
  const principles = model.project.vision.principles ?? [];
  const journeys = model.project.vision.journeys ?? [];
  // One namespace, so one uniqueness check over both lists: an id that stood for
  // a journey and a principle at once would make a judgement's claim ambiguous.
  unique([...journeys, ...principles].map((x) => x.id), 'vision');
  for (const x of principles) required(x.title, `vision/principles/${x.id}`);
  for (const x of journeys) required(x.title, `vision/journeys/${x.id}`);
  // What a judgement may name: the vision's journeys and principles, one
  // namespace, because a claim of what a document serves or breaks is a claim
  // about one of the owner's own. The judged document is searched for the quote
  // and nothing else is: the object the record is bound to is the object a
  // passage has to be in.
  const declared = new Set([...journeys, ...principles].map((x) => x.id));
  const inspectJudgement = (record, at, judged) => {
    required(record.answeredBy, `${at}/answeredBy`);
    // A record that serves nothing is not an answer to the question it was asked.
    check('missing-judgement', record.serves.length === 0, `${at}/serves`);
    for (const [kind, list] of [['serves', record.serves], ['breaks', record.breaks]])
      list.forEach((entry, i) => {
        const entryAt = `${at}/${kind}/${i}`;
        // Each entry gets one refusal, not two for one thing: a placeholder
        // quote is unfinished content and is not asked the harder question.
        const placeholder = !present(entry.quote);
        check('unfinished-content', placeholder, `${entryAt}/quote`);
        check('unknown-vision-id', declared.size > 0 && !declared.has(entry.id), `${entryAt}/${entry.id}`);
        check('unfound-quote', !placeholder && !quoteFound(entry.quote, judged), entryAt);
      });
  };
  if (model.phase === 'product' && principles.length) {
    const record = evidence?.judgement;
    const bound = record && record.charterDigest === digest(model.project.milestone);
    check('missing-judgement', !bound, 'milestone');
    if (bound) inspectJudgement(record, 'evidence/judgement', model.project.milestone);
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

  // The observation section: the change is admitted only when someone can see
  // where it was seen. An internal change says why nothing a person reaches
  // changes; a screen change is judged beside a mockup the tree has. The approval
  // digest (decided) does not cover this section: it binds what the change
  // commits to, and the observation is admitted by these rules, not approved.
  const whereSeen = change.whereSeen;
  check('missing-observation', !whereSeen
    || (whereSeen.kind === 'internal' && !present(whereSeen.internalReason ?? ''))
    || !present(whereSeen.place)
    || whereSeen.steps.length === 0, 'change/whereSeen');
  if (whereSeen) {
    required(whereSeen.place, 'change/whereSeen/place');
    // An absent internalReason is enough to refuse (missing-observation, which
    // already fired); unfinished-content is for what is there but empty/placeholder.
    if (whereSeen.kind === 'internal' && whereSeen.internalReason !== undefined)
      required(whereSeen.internalReason, 'change/whereSeen/internalReason');
    whereSeen.steps.forEach((s, i) => required(s, `change/whereSeen/steps/${i}`));
  }
  check('missing-mockup', Boolean(whereSeen) && whereSeen.kind === 'screen'
    && (!Boolean(whereSeen.mockup) || !model.mockups?.includes(whereSeen.mockup)), 'change/whereSeen/mockup');

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
    check('stale-base', !same(baseline.get(key) ?? null, d.before), key,
      !d.before && baseline.has(key) ? STALE_BASE_ALREADY_THERE : null);
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
    const drifted = !same(wanted.get(key) ?? null, current.get(key) ?? null);
    // Before closure, current standing at the change's own end is not a lost
    // document: it is the end brought forward, and saying so points at the phase.
    const atTheEnd = model.phase !== 'close' && same(current.get(key) ?? null, expected.get(key) ?? null);
    check('unsynced-current', drifted, key, atTheEnd ? UNSYNCED_CURRENT_AT_THE_END : null);
  }

  // The design's constraints, one closed section, each with an id and each
  // binding a requirement the change carries. The coverage rules below stay the
  // one owner of "every changed requirement names a check that proves it": a
  // constraint joins the affected set instead of repeating that loop. A
  // constraint about scale names its size, and the check that proves it declares
  // the size of its fixture; the gate compares the two numbers itself.
  const constraints = change.constraints ?? [];
  unique(constraints.map((c) => c.id), 'change/constraints');
  for (const c of constraints) {
    const at = `change/constraints/${c.id}`;
    required(c.statement, at);
    const key = ref(c.requirement.capability, c.requirement.requirement);
    check('unknown-constraint', !affected.has(key), key);
    if (c.scale) {
      required(c.scale.unit, `${at}/scale/unit`);
      const proved = change.coverage.find((x) => ref(x.capability, x.requirement) === key)?.checks ?? [];
      for (const id of proved) {
        const fixture = policy.requiredChecks.find((x) => x.id === id)?.fixture;
        check('small-fixture', !fixture || fixture.unit !== c.scale.unit || fixture.size < c.scale.size, `${key}/${id}`);
      }
    }
  }
  // What the design answers to: the kept principles of its area, each of them a
  // constraint. A change to what the product does, in a project that keeps
  // principles, answers to at least one of them.
  const named = change.principles ?? [];
  for (const id of named) {
    check('unknown-principle', principles.length > 0 && !principles.some((x) => x.id === id), `change/principles/${id}`);
    check('principle-without-constraint', !constraints.some((c) => c.principle === id), `change/principles/${id}`);
  }
  check('missing-principle', principles.length > 0 && change.kind === 'behavior' && named.length === 0, 'change/principles');

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
    // No receipts at all is not a stale receipt: it is every required check
    // unproved, and the loop below says so check by check.
    check('stale-evidence', !evidence, 'evidence');
    const needed = new Set([...applicable.map((c) => c.id), ...change.coverage.flatMap((c) => c.checks)]);
    // Each receipt stands for what its own check reads. A check that says
    // nothing about its inputs reads the whole revision, as before.
    const pinOf = (id) => model.checkRevisions?.find((r) => r.id === id)?.revision;
    for (const c of policy.requiredChecks) check('unpinned-check', (c.ignores?.length ?? 0) > 0 && !pinOf(c.id), `policy/${c.id}`);
    const kindOf = new Map(policy.requiredChecks.map((c) => [c.id, c.kind]));
    // What a receipt stands for in the policy: its own check's entry and the
    // policy-wide fields (policyScope above). A receipt that carries no digest of
    // its own is the older form and is read against the whole policy, which is
    // what it was given: an installation whose policy has not moved since keeps
    // its receipts, and the next run records the narrower form.
    const holdsPolicy = (receipt) => receipt.policyDigest !== undefined
      ? receipt.policyDigest === policyScope(policy, receipt.id)
      : evidence.policyDigest === digest(policy);
    const notes = [];
    for (const id of needed) {
      const receipt = evidence?.checks.find((c) => c.id === id);
      const stale = Boolean(evidence && receipt)
        && (!holdsPolicy(receipt) || (receipt.revision ?? evidence.revision) !== (pinOf(id) ?? model.revision));
      if (kindOf.get(id) === 'mutation' && (!receipt || receipt.status !== 'passed' || !present(receipt.reference) || stale)) {
        notes.push({ id: 'not-made', at: id, why: NOT_MADE });
        continue;
      }
      check('unproved-check', !receipt || receipt.status !== 'passed' || !present(receipt.reference), id);
      check('stale-evidence', stale, id);
    }
    // A review receipt is evidence only if it answered the design question too.
    // A receipt that never passed is refused by unproved-check above and is not
    // asked this: one refusal, not two, for one missing thing.
    if (policy.requireDesignMatch) {
      for (const id of needed) {
        if (kindOf.get(id) !== 'review') continue;
        const receipt = evidence?.checks.find((c) => c.id === id);
        if (!receipt || receipt.status !== 'passed' || !present(receipt.reference)) continue;
        check('missing-design-match', !receipt.designMatch, id);
        if (receipt.designMatch) {
          receipt.designMatch.divergences.forEach((d, i) => required(d, `evidence/${id}/divergences/${i}`));
          check('design-divergence', receipt.designMatch.matches === 'no' || receipt.designMatch.divergences.length > 0, id);
        }
      }
    }
    // The design's judgement: the same question as the charter's, asked at the
    // design's own acceptance, about the change to what the product does that
    // the project's vision keeps principles for. A bug with no design is not
    // asked it, and the cheap path stays cheap.
    if (principles.length && change.kind === 'behavior') {
      const record = evidence?.judgement;
      const bound = record && record.designDigest === judged(change);
      check('missing-judgement', !bound, 'change');
      if (bound) inspectJudgement(record, 'evidence/judgement', judgedText(change));
    }
    if (notes.length) violations.notes = notes;
  }
  return violations;
}

// The notes ride on the returned array, so serializing the array alone would
// drop them; wrappers read them through this export.
export const notesOf = (result) => result.notes ?? [];

// Fixture patches mutate only an in-memory copy; shipped source is never edited.
export function fixtureCases() {
  const fixtures = JSON.parse(readFileSync(join(here, 'fixtures/documents.json'), 'utf8'));
  return fixtures.cases.map((c) => {
    const input = structuredClone(fixtures.valid);
    const apply = (target, patch) => {
      const parts = patch.path.split('/');
      let owner = target;
      for (const part of parts.slice(0, -1)) owner = owner[part];
      const key = parts.at(-1);
      // The value is cloned, never shared: two copies of the fixture built from
      // the same case must not be edited through one object.
      if (patch.delete) delete owner[key]; else owner[key] = structuredClone(patch.value);
    };
    // 'pristine' records a receipt as it would have been given BEFORE the
    // patches: the only way to ask whether an edit voids it. A judgement's
    // 'pristine' is the state before the case's LAST patch, which is the edit
    // itself — so a rule that stopped hashing constraints is still caught here.
    const approved = c.receipts === 'pristine' ? structuredClone(fixtures.valid) : input;
    const judgedAt = c.judgement === 'pristine' ? (() => {
      const before = structuredClone(fixtures.valid);
      for (const patch of c.patches.slice(0, -1)) apply(before, patch);
      return before;
    })() : input;
    for (const patch of c.patches) apply(input, patch);
    // A fixture may ask for a judgement on its own revision: the digests are
    // computed from the model as it stood at that boundary, so a case can edit
    // the document and ask whether the judgement it carried still stands. The
    // record answers with the first id the vision declares and a passage of the
    // document it judges; a case names its own id ('id') or its own quote
    // ('quote') when the rule is what it is asking about.
    if (c.judgement) {
      const source = judgedAt;
      // What the gate searches, and what the record is bound to: the charter for
      // a charter judgement, and for a design the projection `judged` hashes.
      const charter = source.model.phase === 'product';
      const documentText = charter ? source.model.project.milestone
        : source.model.change ? judgedText(source.model.change) : {};
      const vision = source.model.project.vision;
      const ids = [...(vision.journeys ?? []), ...(vision.principles ?? [])].map((x) => x.id);
      const quoted = c.quote ?? (charter ? documentText.outcomes?.[0] : documentText.decided?.intent) ?? '';
      input.evidence.judgement = {
        charterDigest: digest(source.model.project.milestone),
        // Absent, never null: a product-phase record answers about the charter.
        ...(source.model.change ? { designDigest: judged(source.model.change) } : {}),
        answeredBy: 'fixture:another-model',
        serves: [{ id: c.id ?? ids[0], quote: quoted }],
        breaks: [],
      };
    }
    // Fixtures may request fresh receipts, never production inputs. Each receipt
    // is bound to its own check's scope under the policy the receipts were given
    // at ('pristine' is the policy before the case's edits, so a case can ask
    // whether an edit of one entry voids that receipt). The whole-policy digest
    // is written too, in the older form: a case that asks for 'oldForm' records
    // that form alone, which is what an installation that has not run since
    // carries.
    if (c.receipts) {
      input.evidence.policyDigest = digest(approved.policy);
      input.evidence.approvals = approved.model.change
        ? [{ changeDigest: digest(decided(approved.model.change)), reference: 'fixture:approval' }] : [];
      // 'oldForm' records the older shape: no digest of its own on any receipt,
      // so every receipt stands or falls with the whole-policy digest. A receipt
      // the case writes itself can say '@final': recorded under the policy as the
      // case leaves it, which is how a wrapper records the check the change adds.
      for (const receipt of input.evidence.checks) {
        if (c.oldForm) delete receipt.policyDigest;
        else if (receipt.policyDigest === undefined) receipt.policyDigest = policyScope(approved.policy, receipt.id);
        else if (receipt.policyDigest === '@final') receipt.policyDigest = policyScope(input.policy, receipt.id);
      }
    }
    return { ...c, ...input };
  });
}

export function selftest() {
  let failures = 0;
  const unexplained = new Set();
  for (const c of fixtureCases()) {
    let actual, actualNotes, found = [];
    try {
      found = checkDocuments(c.model, c.policy, c.evidence);
      for (const v of found) if (!v.why) unexplained.add(v.id);
      const notes = notesOf(found);
      for (const n of notes) if (!n.why) unexplained.add(`not-made:${n.at}`);
      actual = [...new Set(found.map((v) => v.id))].sort();
      actualNotes = [...new Set(notes.map((n) => `not-made:${n.at}`))].sort();
    } catch { actual = ['invalid-input']; actualNotes = []; found = []; }
    const wantedNotes = [...(c.notes ?? [])].sort();
    // A case may also ask WHICH subjects were refused ('at': the exact set, so a
    // rule that voids one receipt is caught voiding a second), and WHAT the
    // refusal says ('why': the wording, or the cause it names where the state
    // shows one).
    const said = found.map((v) => ({ id: v.id, at: v.at, why: v.why ?? '' }));
    const subjects = [...new Set(found.map((v) => v.at))].sort();
    const wrongAt = c.at === undefined || same(subjects, [...c.at].sort())
      ? [] : [`refused ${JSON.stringify(subjects)}, wanted ${JSON.stringify([...c.at].sort())}`];
    const wrongWhy = Object.entries(c.why ?? {}).filter(([id, fragment]) =>
      !said.some((v) => v.id === id && v.why.includes(fragment)));
    const ok = same(actual, [...c.expected].sort()) && same(actualNotes, wantedNotes)
      && wrongAt.length === 0 && wrongWhy.length === 0;
    console.log(`${ok ? 'PASS' : 'FAIL'}  documents/${c.name}: ${JSON.stringify(actual)}` +
      (wantedNotes.length || actualNotes?.length ? ` notes: ${JSON.stringify(actualNotes ?? [])}` : '') +
      (wrongAt.length ? ` (${wrongAt[0]})` : '') +
      (wrongWhy.length ? ` (nothing said ${wrongWhy.map(([id, f]) => `${id} ~ ${f}`).join(', ')})` : ''));
    if (!ok) failures++;
  }
  // A refusal that names no remedy sends its reader to read the rules instead.
  const explained = unexplained.size === 0;
  console.log(`${explained ? 'PASS' : 'FAIL'}  documents/every verdict says what green looks like` +
    `${explained ? '' : `: ${[...unexplained].sort().join(', ')}`}`);
  if (!explained) failures++;
  // The command line reads every option's value through argv.mjs: an empty,
  // blank, missing or option-shaped word is invalid input that names the option,
  // where `args[++i]` accepted nothing and went on with no input at all.
  for (const [name, argv, option] of [
    ['an empty --input', ['--input', '', '--policy', 'p'], '--input'],
    ['a missing --policy', ['--input', 'i', '--policy'], '--policy'],
    ['an option instead of the --evidence value', ['--input', 'i', '--policy', 'p', '--evidence', '--json'], '--evidence'],
  ]) {
    let said = '';
    const write = process.stderr.write.bind(process.stderr);
    process.stderr.write = (c) => { said += String(c); return true; };
    try { main(argv); } catch (error) { said += error.message; } finally { process.stderr.write = write; }
    const ok = said.includes(`"${option}"`);
    console.log(`${ok ? 'PASS' : 'FAIL'}  documents/cli: ${name}${ok ? '' : ` (said ${said.trim().slice(0, 90)})`}`);
    if (!ok) failures++;
  }
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
      // One reader for every option that takes a value: an empty, blank,
      // missing or option-shaped word is invalid input, and the refusal names
      // the option, so a path an unset variable emptied is never read as none.
      try { options[key] = valueOf(args, i, key); } catch (e) {
        if (e instanceof ValueNeeded) throw new Error(e.message);
        throw e;
      }
      i++;
    }
  }
  if (!options['--input'] || !options['--policy']) throw new Error('--input and --policy are required');
  // Every path here is named on the command line, so each one is known to be a
  // regular file before it is opened: a named pipe would never answer.
  const read = (p) => {
    requireRegularFile(p, 'a document the check reads');
    return JSON.parse(readFileSync(p, 'utf8'));
  };
  const model = read(options['--input']);
  const violations = checkDocuments(model, read(options['--policy']), options['--evidence'] ? read(options['--evidence']) : null);
  const c = model.change;
  const context = c
    ? `Change ${c.id}, kind ${c.kind}: ${c.deltas.length} delta(s), ${c.preserves.length} preserved, ` +
      `${c.coverage.length} covered; capabilities in the baseline: ${model.baseline.map((x) => x.id).join(', ') || 'none yet'}`
    : `Phase ${model.phase}, no change record`;
  const notes = notesOf(violations);
  console.log(options['--json'] ? JSON.stringify({ version: RULES_VERSION, violations, notes })
    : violations.length || notes.length
      ? [context, ...violations.map((v) => `${v.id}: ${v.at}${v.why ? ` \u2014 ${v.why}` : ''}`),
         ...notes.map((n) => `not-made: ${n.at} \u2014 ${n.why}`)].join('\n')
      : 'Document gate: clean');
  return violations.length ? 1 : 0;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try { process.exitCode = main(process.argv.slice(2)); }
  catch (error) { console.error(`Invalid document gate input: ${error.message}`); process.exitCode = 2; }
}
