import type { VaultSecretKind } from './vault-client'

/** Where the person is standing.
 *
 *  `field` is the honest answer for a surface that knows a secret is being
 *  made and does NOT know which field it is for — the '@' picker, which is
 *  reachable from the URL, a header, a parameter, the body and Auth alike.
 *  It exists so such a caller states that rather than naming some other site
 *  to obtain the kind it wanted: a fabricated site works by accident and
 *  changes meaning silently the day the site it borrowed is given a rule of
 *  its own. */
type ProposalSite =
  | { at: 'auth'; scheme: 'bearer' | 'basic' }
  | { at: 'header'; name: string }
  | { at: 'query'; name: string }
  | { at: 'field' }

export interface ProposalContext {
  site: ProposalSite
  /** The request's URL exactly as typed — `{{baseUrl}}/users` and all. */
  url: string
  /** Resolves an environment variable by name, when the caller has an
   * environment. Absent or returning undefined means "not resolved". */
  resolveVariable?: (name: string) => string | undefined
  /** The folder the request lives in, if any. */
  folder?: string
  /** The request's own name, if any. */
  request?: string
}

export interface SecretProposal {
  name: string
  kind: VaultSecretKind
}

const VARIABLE_PATTERN = /\{\{([^{}]+)\}\}/g
const EXPLICIT_SCHEME_PATTERN = /^[a-z][a-z\d+.-]*:/i
const HIERARCHICAL_SCHEME_PATTERN = /^[a-z][a-z\d+.-]*:\/\//i

/** NOTE THE SIGNATURE: there is no value parameter. The proposal is metadata
 * and the material cannot reach it, because the function is never given it. */
export function proposeSecret(ctx: ProposalContext): SecretProposal {
  const siteWord = wordForSite(ctx.site)
  const where = hostFromURL(ctx) || nonEmpty(ctx.folder) || nonEmpty(ctx.request)
  const kind: VaultSecretKind =
    ctx.site.at === 'auth' && ctx.site.scheme === 'basic' ? 'password' : 'api-token'

  if (where === '') return { name: siteWord, kind }
  if (ctx.site.at === 'auth' || ctx.site.at === 'field') {
    return { name: `${where} ${siteWord}`, kind }
  }
  return { name: `${siteWord} for ${where}`, kind }
}

function wordForSite(site: ProposalSite): string {
  if (site.at === 'auth') return site.scheme === 'basic' ? 'password' : 'token'
  // A field the caller cannot name has no word of its own to lend.
  if (site.at === 'field') return 'token'
  // A blank field name is not a useful site word. Keep the proposal non-empty
  // without inventing a secret-like placeholder name.
  return nonEmpty(site.name) || 'token'
}

function hostFromURL(ctx: ProposalContext): string {
  const input = ctx.url.trim()
  if (input === '' || /^\{\{[^{}]+\}\}$/.test(input)) return ''

  const hostVariables = variablesInAuthority(input)
  const parsedWithPlaceholders = parseURL(replaceVariables(input, () => 'nocx-variable.invalid'))
  if (parsedWithPlaceholders === undefined || parsedWithPlaceholders.hostname === '') return ''
  if (hostVariables.length === 0) return parsedWithPlaceholders.hostname
  if (ctx.resolveVariable === undefined) return ''

  const values = new Map<string, string>()
  for (const name of hostVariables) {
    const value = ctx.resolveVariable(name)
    if (value === undefined) return ''
    values.set(name, value)
  }

  const substituted = replaceVariables(input, (name) => values.get(name) ?? 'nocx-variable.invalid')
  return parseURL(substituted)?.hostname ?? ''
}

function variablesInAuthority(input: string): string[] {
  const authority = authorityPart(input)
  const names = new Set<string>()
  for (const match of authority.matchAll(VARIABLE_PATTERN)) names.add(match[1])
  return [...names]
}

function authorityPart(input: string): string {
  let start = 0
  const hierarchicalScheme = input.match(HIERARCHICAL_SCHEME_PATTERN)
  if (hierarchicalScheme !== null) {
    start = hierarchicalScheme[0].length
  } else if (input.startsWith('//')) {
    start = 2
  } else if (EXPLICIT_SCHEME_PATTERN.test(input)) {
    return ''
  }

  const authority = input.slice(start)
  const delimiter = authority.search(/[/?#]/)
  return delimiter === -1 ? authority : authority.slice(0, delimiter)
}

function replaceVariables(input: string, replacement: (name: string) => string): string {
  return input.replace(VARIABLE_PATTERN, (_whole, name: string) => replacement(name))
}

function parseURL(input: string): URL | undefined {
  let candidate = input
  if (candidate.startsWith('//')) {
    candidate = `https:${candidate}`
  } else if (!EXPLICIT_SCHEME_PATTERN.test(candidate)) {
    candidate = `https://${candidate}`
  } else if (!HIERARCHICAL_SCHEME_PATTERN.test(candidate)) {
    return undefined
  }

  try {
    return new URL(candidate)
  } catch {
    return undefined
  }
}

function nonEmpty(value: string | undefined): string {
  return value?.trim() ?? ''
}
