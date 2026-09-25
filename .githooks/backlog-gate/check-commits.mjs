#!/usr/bin/env node
// Project adapters parse messages; this check validates their normalized links.
import { readFileSync, readdirSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const RULES_VERSION = '0.26.0';
const HERE = dirname(fileURLToPath(import.meta.url));

function evaluate(input) {
  if (!input || !Array.isArray(input.issues) || !Array.isArray(input.commits) || !input.commits.length)
    throw new Error('input needs issues and a nonempty commits list');
  const issues = new Map();
  for (const issue of input.issues) {
    if (!issue || typeof issue.id !== 'string' || !issue.id.trim() ||
        typeof issue.type !== 'string' || !['epic', 'task', 'bug', 'chore', 'other'].includes(issue.type) || issues.has(issue.id))
      throw new Error('issues need unique ids and normalized types');
    issues.set(issue.id, issue);
  }
  const parents = new Set(input.issues.map((i) => i.parent).filter(Boolean));
  const seen = new Set();
  const violations = [];
  for (const commit of input.commits) {
    if (!commit || typeof commit.id !== 'string' || !commit.id.trim() || seen.has(commit.id) ||
        !Array.isArray(commit.taskIds) || commit.taskIds.some((id) => typeof id !== 'string' || !id.trim()))
      throw new Error('commits need unique ids and a taskIds list of nonempty strings');
    seen.add(commit.id);
    if (!commit.taskIds.length) violations.push({ id: commit.id, task: null, reason: 'no task link' });
    for (const id of new Set(commit.taskIds)) {
      const issue = issues.get(id);
      if (!issue) violations.push({ id: commit.id, task: id, reason: 'task does not exist' });
      else if (issue.type === 'epic' || parents.has(id))
        violations.push({ id: commit.id, task: id, reason: 'link must name a leaf task, not a container' });
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
    console.log('check-commits.mjs [<normalized.json>|-]\ncheck-commits.mjs --selftest\nExit 0 linked, 1 missing/invalid links, 2 invalid input.');
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
