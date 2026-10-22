import { createHash } from 'node:crypto';
// Reference reader for the shipped Markdown templates, not a parser for all formats.
// Project adapters may reuse these functions; syntax errors must fail their export.
const idPattern = '[a-zA-Z0-9][a-zA-Z0-9_.:-]*';

function lines(text) {
  if (typeof text !== 'string') throw new Error('document must be text');
  let fence = null;
  const result = text.replace(/\r\n/g, '\n').split('\n').map((raw) => {
    const mark = raw.match(/^\s{0,3}(`{3,}|~{3,})(.*)$/);
    if (fence) {
      if (mark && mark[1][0] === fence[0] && mark[1].length >= fence.length && !mark[2].trim()) fence = null;
      return { raw, structural: false };
    }
    if (mark) { fence = mark[1]; return { raw, structural: false }; }
    return { raw, structural: true };
  });
  if (fence) throw new Error('unclosed code fence');
  return result;
}

function sections(text, names) {
  const result = {};
  let active = null;
  for (const { raw, structural } of lines(text)) {
    const header = structural && raw.match(/^## (.+)$/);
    if (header) {
      active = header[1];
      if (!names.includes(active)) throw new Error(`unrecognized section: ${active}`);
      if (Object.hasOwn(result, active)) throw new Error(`duplicate section: ${active}`);
      result[active] = [];
    } else if (active) result[active].push(raw);
  }
  return Object.fromEntries(Object.entries(result).map(([k, v]) => [k, v.join('\n').trim()]));
}

export function parseVision(text) {
  const s = sections(text, ['Audience', 'Non-users', 'Problem', 'Alternatives and difference', 'Key journeys', 'Principles', 'Outcome',
    'Success signal', 'Exclusions', 'Constraints and assumptions', 'Direction']);
  // Journeys and principles are the owner's own words, one per line with a stable
  // id in front of it (J1, P1, …): the ids everything below the vision names, and
  // the wording it is read from. One reader for both, so an id a document names
  // is read the same way whichever of the two it is.
  const items = (text) => text.split('\n')
    .map((line) => /^\s*[-*>]?\s*([a-zA-Z0-9][a-zA-Z0-9_.:-]*)\s*[:.)—-]\s*(\S.*)$/.exec(line))
    .filter(Boolean).map((m) => ({ id: m[1], title: m[2].trim() }));
  return {
    audience: s.Audience ?? '', problem: s.Problem ?? '', outcome: s.Outcome ?? '', exclusions: s.Exclusions ?? '',
    // Absent when the record has no such section, so a vision without principles
    // carries no empty list for every reader to tell apart from "none kept".
    ...(s['Key journeys'] == null ? {} : { journeys: items(s['Key journeys']) }),
    ...(s.Principles == null ? {} : { principles: items(s.Principles) }),
  };
}

export function parseMilestone(text, id) {
  const s = sections(text, ['Outcomes and acceptance', 'Exclusions', 'Scope decisions', 'Next horizon']);
  return { id, outcomes: s['Outcomes and acceptance'] ? [s['Outcomes and acceptance']] : [], exclusions: s.Exclusions ?? '' };
}

export function parseCapability(text) {
  const capability = { id: '', title: '', requirements: [] };
  let requirement = null, scenario = null, field = null, section = null;
  let scenarioFields = new Set();
  const statements = new Map(), seenSections = new Set();
  for (const { raw, structural } of lines(text)) {
    const title = structural && raw.match(/^# (.+)$/);
    if (title) {
      if (capability.title) throw new Error('duplicate capability title');
      capability.title = title[1]; continue;
    }
    const identity = structural && raw.match(/^Capability: (.+)$/);
    if (identity) {
      if (capability.id) throw new Error('duplicate capability identity');
      capability.id = identity[1]; continue;
    }
    const heading = structural && raw.match(/^## (.+)$/);
    if (heading) {
      requirement = null; scenario = null; field = null;
      section = heading[1];
      if (seenSections.has(section)) throw new Error(`duplicate section: ${section}`);
      seenSections.add(section);
      const match = section.match(new RegExp(`^Requirement: (${idPattern}) — (.+)$`));
      if (match) {
        requirement = { id: match[1], title: match[2], statement: '', scenarios: [] };
        capability.requirements.push(requirement); statements.set(requirement, []);
      } else if (!['Purpose', 'Quality requirements', 'Context', 'Coverage limits'].includes(section)) throw new Error(`unrecognized section: ${section}`);
      continue;
    }
    const scenarioHeading = structural && raw.match(/^### (.+)$/);
    if (scenarioHeading) {
      const match = scenarioHeading[1].match(new RegExp(`^Scenario: (${idPattern})$`));
      if (!requirement || !match) throw new Error(`unrecognized scenario: ${scenarioHeading[1]}`);
      scenario = { id: match[1], given: '', when: '', then: '' };
      requirement.scenarios.push(scenario); field = null; scenarioFields = new Set(); continue;
    }
    if (structural && /^#{1,6}\s/.test(raw)) throw new Error(`unsupported heading: ${raw}`);
    if (requirement && !scenario) statements.get(requirement).push(raw);
    else if (scenario) {
      const part = structural && raw.match(/^- (Given|When|Then):\s*(.*)$/);
      if (part) {
        field = part[1].toLowerCase();
        if (scenarioFields.has(field)) throw new Error(`duplicate scenario field: ${field}`);
        scenarioFields.add(field);
        scenario[field] = part[2];
      } else if (raw.trim()) {
        if (!field || !/^\s+/.test(raw)) throw new Error(`unrecognized scenario content: ${raw}`);
        scenario[field] += `\n${raw.trim()}`;
      }
    } else if (!section && raw.trim()) throw new Error(`unrecognized preamble: ${raw}`);
  }
  if (!new RegExp(`^${idPattern}$`).test(capability.id) || !capability.title) throw new Error('missing/invalid capability identity');
  for (const r of capability.requirements) r.statement = statements.get(r).join('\n').trim();
  return capability;
}

// ---- what a check reads ---------------------------------------------------

// A path pattern: `*` inside one name, `**` across folders, `?` one character.
// A pattern with no slash matches a name at any depth (`*.md`); one ending in
// a slash matches everything under that folder (`docs/`).
export function pathMatcher(pattern) {
  let p = String(pattern).trim();
  if (!p) throw new Error('an empty path pattern');
  if (p.endsWith('/')) p += '**';
  const anywhere = !p.includes('/');
  if (p.startsWith('/')) p = p.slice(1);
  let re = '';
  for (let i = 0; i < p.length; i++) {
    const ch = p[i];
    if (ch === '*' && p[i + 1] === '*') {
      i++;
      if (p[i + 1] === '/') { i++; re += '(?:.*/)?'; } else re += '.*';
    } else if (ch === '*') re += '[^/]*';
    else if (ch === '?') re += '[^/]';
    else re += ch.replace(/[.+^${}()|[\]\\]/g, '\\$&');
  }
  const rx = new RegExp(`^${anywhere ? '(?:.*/)?' : ''}${re}$`);
  return (path) => rx.test(path);
}

/**
 * The revision one check's receipt is pinned to: a digest of every file it
 * reads at a commit, given as `git ls-tree -r --full-tree <rev>` prints it.
 * `exclude` is what the gate verifies itself on every run (the revision's own
 * exclusions, the same for every check); `ignores` is what the policy says
 * this check does not read. Leaving out a file a check does read is the one
 * mistake that weakens a receipt, so the list names what is left out and
 * starts empty: forgetting an entry only costs a run.
 */
export function checkRevision(lsTree, { exclude = [], ignores = [] } = {}) {
  const skip = [...exclude, ...ignores].map(pathMatcher);
  const entries = [];
  for (const line of String(lsTree).split('\n')) {
    if (!line.trim()) continue;
    const m = /^(\d+) (\w+) ([0-9a-f]+)\t(.+)$/.exec(line);
    if (!m) throw new Error(`not a line of git ls-tree: ${line.slice(0, 80)}`);
    if (skip.some((f) => f(m[4]))) continue;
    entries.push(`${m[1]} ${m[3]} ${m[4]}`);
  }
  return createHash('sha256').update(entries.sort().join('\n')).digest('hex');
}
