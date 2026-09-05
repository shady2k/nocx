/**
 * What each of the static scan's patterns MEANS, in the words of the person
 * being asked to adopt the body (nocx-swn1m, moved here by nocx-qja4m.6).
 *
 * ONE owner, for the same reason `effect-labels.ts` is one: two surfaces now
 * put this evidence in front of a person — the approval prompt, where the
 * ASSISTANT proposes a skill body, and the install ask, where a STRANGER
 * wrote one — and a person must meet the same words in both. It lived in
 * `agent-approval-prompt.tsx` while there was one reader; a second copy in
 * the install ask would be the second-owner defect AGENTS.md names, and the
 * two would agree on every pattern anybody tried until the day the Go table
 * gained a twelfth and only one of them was taught it.
 *
 * The wire carries `patternId` — `prompt_injection`, `exfil_curl` — which is
 * the scan table's own key (internal/skill/scan.go) and names nothing to
 * anybody outside it. A window that printed the token would put the reader in
 * front of a bare identifier and the line it matched, and leave them to guess
 * the relationship between the two; the whole value of the finding is being
 * able to weigh what the pattern says the line DOES against what was asked
 * for. So each sentence says what a person can act on: the subject is the
 * BODY (not "the assistant" and not "you"), because the body is the thing
 * being decided about, and each says what the matched text asks for rather
 * than how sure the scan is — eleven regexes are not a sanitiser and a
 * confident-sounding verdict here would claim more than they can carry.
 *
 * Keyed by string rather than by a union, because the wire's field IS a
 * string: the Go table can grow a twelfth pattern without this file, and the
 * fallback below is what makes that honest instead of silent.
 */
const SCAN_PATTERN_WORDS: Record<string, string> = {
  prompt_injection: 'The body tells the assistant to ignore the instructions it was given.',
  sys_prompt_override: 'The body claims to override the assistant’s system prompt.',
  disregard_rules: 'The body tells the assistant to disregard its rules or guidelines.',
  bypass_restrictions:
    'The body tells the assistant to act as though it has no restrictions or limits.',
  exfil_curl: 'The body runs curl on a line that reads a key, token, secret or password.',
  exfil_wget: 'The body runs wget on a line that reads a key, token, secret or password.',
  read_secrets:
    'The body reads a credentials file — an .env, .netrc, .pgpass, .npmrc, .pypirc or a file named credentials.',
  send_to_url: 'The body sends, posts or uploads something to a URL.',
  context_exfil:
    'The body asks for the conversation or the whole context to be printed, shared or included.',
  agent_config_mod:
    'The body writes to a file of standing agent instructions — AGENTS.md, CLAUDE.md, .cursorrules or .clinerules.',
  hermes_config_mod: 'The body writes to a hermes configuration file under .hermes/.',
}

/**
 * The finding's pattern in a person's words, and the id itself when this
 * build has no sentence for it. Dropping an unrecognised finding would be
 * the silent degrade the scan exists to prevent, and inventing a sentence for
 * a pattern this build does not know would be worse: the id is at least true,
 * and it is greppable in the Go table by whoever it reaches.
 */
export const scanPatternWords = (patternId: string): string =>
  SCAN_PATTERN_WORDS[patternId] ??
  `The body matched a scan pattern this build has no words for: ${patternId}.`
