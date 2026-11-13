#!/usr/bin/env node
/**
 * Connection-owner ratchet, renderer half — a NEW place in frontend/src that
 * decides a connection fact from text, subscribes to a connection
 * notification, or opens/reconnects an SSH session is refused
 * (nocx-oalo1.3.5).
 *
 * ADR-ba6xet1b says a fact about an SSH connection has ONE owner — the
 * backend's connection manager behind the Carrier interface — and that "the
 * renderer draws the backend's answer and never decides, opens or reconnects".
 * Its Go half is .githooks/check-connection-owners.mjs; this is the half the
 * record names as nocx-oalo1.3.5, and it enforces the same owner decision:
 * "there must be an architectural ban on second surfaces", not a review.
 *
 * IDENTITY, NOT SPELLING. A name decides nothing on its own: any
 * `reconnect(` is not an open, and a subscription is not a string at the call
 * site. A rule that reads names reports an unrelated type's method and misses
 * the same member reached another way — `client.openSSHSession.bind(client)`,
 * `const event = 'exit'; client.subscribe(event, …)`, `err['message']`.
 * The rules therefore ask the TypeScript checker (the repo already runs typed
 * eslint, so the program exists and costs nothing extra):
 *
 *   - open/reconnect: the member's DECLARING symbol must belong to a class or
 *     interface declared in frontend/src/ipc.ts — the client surface. Any
 *     reference counts: a call, `.bind`, a property read, a destructuring
 *     binding, a class member declaration.
 *   - subscribe: the FIRST ARGUMENT'S TYPE must be, or include, the literal
 *     `session.liveness` or `exit` — whether written inline or through a const.
 *   - error text: the shape stays syntactic (`x['message']` now reads like
 *     `x.message`; every error text is a string, so the type cannot tell it
 *     from prose), and the method/comparison list is unchanged.
 *   - loss: a `cause` property whose VALUE'S TYPE is the literal
 *     `interrupted`, inline or through a const.
 *
 * WHAT IT REFUSES, by rule id (the baseline and every failure message name the
 * rule, so an entry says what it is):
 *
 *   nocx/no-error-text-match
 *       A decision made by reading an error's or a refusal's TEXT: .includes(,
 *       .startsWith(, .endsWith(, .match(, .test( — or === / !== / switch
 *       against a string literal — where the value is error text. Error text
 *       is the message of an error (`err.message`, `e.message`,
 *       `error.message`, a caught value's descendants), a refusal's `reason`
 *       (`err.data.reason`, a claim's reason), a parameter or local the author
 *       named message/reason/msg/err/error/detail/failure/refusal, and
 *       anything derived from one by toLowerCase()/trim()/String() or by a
 *       variable. This is how a panel used to learn a connection was gone
 *       (`'disconnected'` in a toast sentence), and how a broken attach used
 *       to be told from a lost one (`'unknown-session'`).
 *   nocx/no-connection-subscription
 *       `.subscribe('session.liveness', …)` or `.subscribe('exit', …)`
 *       outside frontend/src/connection/, by the argument's TYPE. Those two
 *       are the connection's own answers: the backend's measurement of
 *       reaching a host, and a session's end, from which ipc.ts mints an
 *       `interrupted` exit. lifecycle.changed is deliberately NOT here: it is
 *       the authenticated execution lifecycle (ADR-0024), whose owner is
 *       src/lifecycle/, and rule 9 already fences that boundary.
 *   nocx/no-connection-open
 *       A reference to a member of the ipc client type — openSSHSession,
 *       openSSHSessionByHost, retrySessionAttach, retryAttach, reconnect —
 *       outside frontend/src/connection/. Seven entry paths into one open is
 *       what the design's §2 counts; stage 7 of nocx-oalo1 collapses them into
 *       one. A same-named method on another type is not a connection entry and
 *       is not reported.
 *   nocx/no-connection-loss
 *       An object literal minting `cause: 'interrupted'` (including through a
 *       const whose type is that literal) — the renderer declaring a session
 *       lost. `interrupted` is the one exit cause that means a loss, and it is
 *       the connection manager's verdict, not the renderer's: ipc.ts invents it
 *       twice today, once from an exit whose cause was not a shell exit and
 *       once from a failed attach.
 *
 * Scope: every .ts and .tsx under frontend/src. Tests (`*.test.ts(x)`,
 * `*.spec.ts(x)`), `src/test-support/`, `*-fixtures.ts` and `src/generated/`
 * are not read, for the reason the Go half does not read `_test.go`: a test
 * builds whatever it is testing and asserts on sentences on purpose, and the
 * generated tree is the contracts' output. The one place a connection fact may
 * be decided is `frontend/src/connection/` — it does not exist yet; stage 3 of
 * nocx-oalo1 creates it, and until then the two owner rules refuse it
 * everywhere.
 *
 * WHAT IT CANNOT SEE, stated because a ratchet that overclaims is worse than
 * none:
 *   - The error-text rule is a same-file AST over names and shapes, not types:
 *     every error text arrives as `string`, so a value that crossed a module
 *     boundary has no shape and a differently-named local is invisible.
 *     `const x = failure(); if (x.includes(…))` is not reported.
 *   - A reference that resolves to a member of an interface IMPLEMENTED by the
 *     ipc client rather than declared in it keeps its declaring symbol; only
 *     the declaration in ipc.ts is the client's. That is the intended reading
 *     — a second type with the same member is a second type, not this owner.
 *   - It reads no Go. A new backend owner is the other half's business
 *     (.githooks/check-connection-owners.mjs).
 *
 * THE BASELINE, and why it is a file rather than eslint-disable comments:
 * `lint-fixtures/connection-owners-baseline.json` is today's list, keyed
 * `rule:file:symbol:target` where `symbol` is the enclosing function or method
 * (`<module>` at top level) and `target` is the resolved member or literal
 * (`openSSHSession`, `exit`, `includes:'disconnected'`), with an OCCURRENCE
 * COUNT. Never a line number, so an unrelated edit above a baselined site
 * costs nothing. The check fails when a key's count grows, a new key appears,
 * or a key shrinks or disappears without the baseline updated: an entry
 * exempts N occurrences, not a sentence, which is what stops two new
 * `client.subscribe('exit', …)` calls from hiding behind one old entry. The
 * updater refuses growth without --allow-growth and preserves every reason;
 * an added entry starts with an empty reason, and this check refuses an empty
 * one. Stages 3, 4 and 7 of nocx-oalo1 empty it. An eslint-disable comment
 * would be invisible to that list and would outlive the stage that owes the
 * removal, which is the whole point of keeping it here.
 *
 * Invocation: node lint-fixtures/connection-owners.mjs   (from frontend/)
 * It is also `npm run lint`'s third rule set: eslint.config.js imports
 * `connectionOwnerRules` and wires them for src/**, and
 * src/connection-owner-gate.test.ts proves the fixtures in eslint-fixtures/
 * fire. Regenerate: node lint-fixtures/update-connection-owners-baseline.mjs
 */
import { readFileSync } from 'node:fs'
import { execFileSync } from 'node:child_process'
import { dirname, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { Linter } from 'eslint'
import tseslint from 'typescript-eslint'
import ts from 'typescript'

const __dirname = dirname(fileURLToPath(import.meta.url))
const FRONTEND_DIR = resolve(__dirname, '..')
const REPO_ROOT = resolve(FRONTEND_DIR, '..')
export const BASELINE_PATH = resolve(__dirname, 'connection-owners-baseline.json')

/** The one directory allowed to decide a connection fact in the renderer. */
export const CONNECTION_DIR = 'frontend/src/connection/'

/** The module whose classes are the ipc client surface. A member is an owner
 *  entry when its DECLARING symbol lives here, whatever it is called on the
 *  receiving end. */
export const IPC_MODULE = resolve(FRONTEND_DIR, 'src/ipc.ts')

export const RULES = {
  'nocx/no-error-text-match': 'decides a fact by matching an error or a refusal text',
  'nocx/no-connection-subscription':
    'subscribes to a connection notification outside frontend/src/connection/',
  'nocx/no-connection-open': 'opens or reconnects an SSH session outside frontend/src/connection/',
  'nocx/no-connection-loss': "mints a lost session's exit cause in the renderer",
}

/**
 * The notifications that ARE the connection's answer. A renderer that handles
 * one is deciding a connection fact, and there is one place for that.
 */
export const CONNECTION_NOTIFICATIONS = {
  'session.liveness':
    'the backend measurement of REACHING a host — slow, unreachable, unknown. Folding it into a ' +
    'pane condition outside the owner is a second derivation of one fact (ADR-ba6xet1b)',
  exit:
    "a session's end, from which ipc.ts mints an `interrupted` exit when the cause is not a shell " +
    "exit. Loss is the connection manager's answer, not a second reading of the same event",
}

/**
 * The open/reconnect entries. One entry path is the target (design §2 counted
 * seven); until stage 7 makes that true, the names are what exists.
 */
export const CONNECTION_ENTRIES = {
  openSSHSession: 'opens an SSH session by saved profile',
  openSSHSessionByHost: 'opens an SSH session by host or alias',
  retrySessionAttach: "claims the same session again — the client's own retry",
  retryAttach: "the session handle's name for the same claim",
  reconnect: 'reconnects a connection the ipc client holds',
}

const OPEN_NAMES = new Set(Object.keys(CONNECTION_ENTRIES))

// ─── Scope ───────────────────────────────────────────────────────────────────
/** Repo-relative, '/'-separated path of the file being linted. */
export function repoPathOf(filename) {
  if (!filename) return ''
  return relative(REPO_ROOT, filename).replace(/\\/g, '/')
}

/** Whether the rules read this file at all. */
export function isScopedFile(rel) {
  if (!rel.startsWith('frontend/src/')) return false
  if (/\.(test|spec)\.(ts|tsx)$/.test(rel)) return false
  if (rel.includes('/test-support/')) return false
  if (/-fixtures\.ts$/.test(rel)) return false
  if (rel.startsWith('frontend/src/generated/')) return false
  return true
}

export function isConnectionFile(rel) {
  return rel.startsWith(CONNECTION_DIR)
}

// ─── The AST shapes both halves share ────────────────────────────────────────
function staticName(node) {
  if (!node) return null
  if (node.type === 'Identifier') return node.name
  if (node.type === 'PrivateIdentifier') return `#${node.name}`
  if (node.type === 'Literal' && typeof node.value === 'string') return node.value
  return null
}

/** The property an expression reads, when it reads a static one. A computed
 *  access with a string literal names the same property as the dot form:
 *  `x['message']` decides exactly what `x.message` decides, and an author who
 *  wrote it that way meant the same thing. */
function propertyName(node) {
  if (!node || node.type !== 'MemberExpression') return null
  if (!node.computed) return staticName(node.property)
  const p = unwrap(node.property)
  if (!p) return null
  if (p.type === 'Literal' && typeof p.value === 'string') return p.value
  if (p.type === 'TemplateLiteral' && p.expressions.length === 0) {
    return p.quasis[0].value.cooked ?? null
  }
  return null
}

function unwrap(node) {
  let n = node
  while (
    n &&
    (n.type === 'ChainExpression' ||
      n.type === 'TSNonNullExpression' ||
      n.type === 'TSAsExpression' ||
      n.type === 'TSTypeAssertion' ||
      n.type === 'TSSatisfiesExpression' ||
      n.type === 'ParenthesizedExpression')
  ) {
    n = n.expression
  }
  return n
}

/** A site's own source text, collapsed to one line, for the report a person
 *  reads. It is NOT the baseline key: the key is the resolved identity below. */
export function collapseSiteText(text) {
  return text.replace(/\s+/g, ' ').trim()
}

function siteTextAt(sourceCode, loc) {
  const start = sourceCode.getIndexFromLoc(loc.start)
  const end = sourceCode.getIndexFromLoc(loc.end)
  return collapseSiteText(sourceCode.text.slice(start, end))
}

// ─── The enclosing symbol, which is half of a baseline key ───────────────────
function classNameOf(classNode) {
  if (!classNode) return '<class>'
  if (classNode.id?.name) return classNode.id.name
  const parent = classNode.parent
  if (parent?.type === 'VariableDeclarator' && parent.id.type === 'Identifier')
    return parent.id.name
  return '<class>'
}

/**
 * The name of the function or method a node sits in: `WSClient._connect`,
 * `Listing`, `default.handler`, or `<module>` at the top of a file. Anonymous
 * callbacks are qualified by the symbol that encloses them, so two callbacks
 * with the same literal in one file do not share a count by accident.
 */
export function enclosingSymbol(node) {
  return enclosing(node, 0)
}

function enclosing(node, depth) {
  if (!node || depth > 40) return '<module>'
  switch (node.type) {
    case 'MethodDefinition':
    case 'PropertyDefinition': {
      const cls = node.parent?.type === 'ClassBody' ? node.parent.parent : node.parent
      return `${classNameOf(cls)}.${staticName(node.key) ?? '<computed>'}`
    }
    case 'FunctionDeclaration':
      return node.id?.name ?? '<module>'
    case 'FunctionExpression':
    case 'ArrowFunctionExpression': {
      const p = node.parent
      if (p?.type === 'MethodDefinition' || p?.type === 'PropertyDefinition') {
        const cls = p.parent?.type === 'ClassBody' ? p.parent.parent : p.parent
        return `${classNameOf(cls)}.${staticName(p.key) ?? '<computed>'}`
      }
      if (p?.type === 'VariableDeclarator' && p.id.type === 'Identifier') return p.id.name
      if (p?.type === 'Property' && !p.computed) {
        const key = staticName(p.key)
        const obj = p.parent?.parent
        if (key === null) return `${enclosing(p, depth + 1)}.<anonymous>`
        if (obj?.type === 'VariableDeclarator' && obj.id.type === 'Identifier') {
          return `${obj.id.name}.${key}`
        }
        return key
      }
      return `${enclosing(p, depth + 1)}.<anonymous>`
    }
    case 'Program':
      return '<module>'
    default:
      return enclosing(node.parent, depth + 1)
  }
}

// ─── The TypeScript checker, when the lint run has one ───────────────────────
/**
 * typescript-eslint's parser services for this file. Production and the CLI
 * below run typed (`parserOptions.project`), so this is the ordinary path; a
 * scoped file WITHOUT a program is an error, not a silent weakening — the
 * identity rules cannot answer without it.
 */
function typeServices(context) {
  const services = context.sourceCode?.parserServices ?? context.parserServices
  if (!services?.program || !services.esTreeNodeToTSNodeMap) return null
  return services
}

function tsNodeOf(services, node) {
  if (!node) return null
  return services.esTreeNodeToTSNodeMap.get(node) ?? null
}

/** A class or interface member declared in frontend/src/ipc.ts: the client
 *  surface. The symbol, not the name, is what makes it an owner entry. */
function declaredOnIpcType(checker, symbol) {
  if (!symbol) return false
  let sym = symbol
  try {
    if (sym.flags & ts.SymbolFlags.Alias) sym = checker.getAliasedSymbol(sym)
  } catch {
    // An alias that cannot be resolved is not a declaration we can name.
  }
  for (const decl of sym.declarations ?? []) {
    const parent = decl.parent
    if (!parent) continue
    if (
      !ts.isClassDeclaration(parent) &&
      !ts.isClassExpression(parent) &&
      !ts.isInterfaceDeclaration(parent)
    ) {
      continue
    }
    if (resolve(decl.getSourceFile().fileName) === IPC_MODULE) return true
  }
  return false
}

/** The symbol a reference to `name` resolves to, through an alias if needed. */
function referencedSymbol(checker, services, node) {
  const tsNode = tsNodeOf(services, node)
  if (!tsNode) return null
  return checker.getSymbolAtLocation(tsNode) ?? null
}

/** The property a binding pattern names, resolved on the pattern's own type. */
function propertyOfPattern(checker, services, typeNode, name) {
  const tsNode = tsNodeOf(services, typeNode)
  if (!tsNode) return null
  const type = checker.getTypeAtLocation(tsNode)
  const from = (t) => {
    if (t.isUnion?.()) {
      for (const part of t.types) {
        const found = from(part)
        if (found) return found
      }
      return null
    }
    return checker.getPropertyOfType(t, name) ?? null
  }
  return from(type)
}

/** The string-literal values a type carries, flattened through a union. */
function literalStrings(type) {
  const values = []
  const walk = (t) => {
    if (!t) return
    if (t.isStringLiteral?.()) {
      values.push(t.value)
      return
    }
    if (t.isUnion?.()) for (const part of t.types) walk(part)
  }
  walk(type)
  return values
}

// ─── Error text: the shapes and the names ────────────────────────────────────
/** A name that says "this is text a failure produced". The type cannot say it:
 *  every one of them arrives as `string`. `detail`/`details` are deliberately
 *  absent: a quick-connect row's detail and a file's detail are domain text,
 *  and a name list that reaches them reports honest search filters. */
const ERROR_TEXT_NAME =
  /^(?:e|err|error|ex|caught|reason|message|msg|failure|refusal)$|(?:Err|Error|Reason|Message|Msg|Failure|Refusal)$/

/** A name that says "this object is the failure": the thing whose .message and
 *  .reason are error text, and whose .data is the refusal's payload. */
const ERROR_ROOT_NAME = /^(?:e|err|error|ex|caught|cause)$/

/** Calls that respell the same text rather than decide anything. */
const NORMALISERS = new Set(['toLowerCase', 'toUpperCase', 'trim', 'valueOf'])

/** Methods whose receiver (or argument) being error text is a textual decision. */
const TEXT_QUERIES = new Set(['includes', 'startsWith', 'endsWith'])

/** Members of a caught/unknown value that carry the refusal rather than the
 *  message: `err.data.reason`, `err.cause`, `err.details`. */
const ERROR_PAYLOAD_MEMBERS = new Set(['data', 'details', 'cause', 'error', 'body'])

/** A function call whose NAME says it answers error text: claimRefusalReason,
 *  failureMessage … The call is not followed; only its name is read, which is
 *  the one thing a same-file AST can know about it. */
function isErrorAnswerCall(node) {
  if (!node || node.type !== 'CallExpression') return false
  const name =
    node.callee.type === 'Identifier'
      ? node.callee.name
      : propertyName(node.callee)
        ? staticName(node.callee.property)
        : null
  return name !== null && /(?:Reason|Refusal|Message|Msg|Failure|Error)$/.test(name)
}

/** The variable an identifier names, from the scope it is written in. */
function resolveVariable(sourceCode, node) {
  let scope = sourceCode.getScope(node)
  while (scope) {
    const variable = scope.set.get(node.name)
    if (variable) return variable
    scope = scope.upper
  }
  return null
}

/** Whether the expression IS the failure: `err`, `error`, `cause`, a catch
 *  binding, or a member chain rooted at one (`err.cause`, `e.details`). */
function isErrorRooted(sourceCode, node) {
  const n = unwrap(node)
  if (!n) return false
  if (n.type === 'Identifier') {
    if (ERROR_ROOT_NAME.test(n.name)) return true
    const variable = resolveVariable(sourceCode, n)
    if (!variable) return false
    return variable.defs.some((def) => def.type === 'CatchClause')
  }
  if (n.type === 'MemberExpression') return isErrorRooted(sourceCode, n.object)
  return isErrorAnswerCall(n)
}

/** Whether the expression's own NAME says it is error text — a parameter named
 *  `reason`, a field named `lastError`, a call named `failureMessage`. */
function isNamedErrorText(node) {
  const n = unwrap(node)
  if (!n) return false
  if (n.type === 'Identifier') return ERROR_TEXT_NAME.test(n.name)
  const prop = propertyName(n)
  if (prop !== null) return ERROR_TEXT_NAME.test(prop)
  return isErrorAnswerCall(n)
}

/**
 * Whether the expression is error text: the message of an error or a refusal's
 * reason, however a person spelled it. `seen` breaks identifier cycles.
 *
 * Two readings, because the two decisions they feed are not equally safe:
 *
 *   - `strict` (equality and switch): only a value that IS a failure's own
 *     text — `.message` or `.reason` of a caught/unknown value, a local bound
 *     from one, or a call whose name answers a reason/message. A field named
 *     `reason` on a RESULT is a declared discriminator (`ask().reason`,
 *     `terminationReason`), and comparing it to its own closed set of codes is
 *     how the wire is meant to be read.
 *   - the loose reading (a textual query: includes/match/test): the value's
 *     own name counts too, because matching PROSE decides nothing however the
 *     value got there. `listingFailureMessage(reason: string)` is a `string`
 *     the type cannot tell from an error's, and `reason.includes('disconnected')`
 *     is the defect this rule exists for.
 */
export function isErrorText(sourceCode, node, seen = new Set(), strict = false) {
  const n = unwrap(node)
  if (!n) return false
  switch (n.type) {
    case 'Identifier': {
      if (!strict && ERROR_TEXT_NAME.test(n.name)) return true
      const variable = resolveVariable(sourceCode, n)
      if (!variable || seen.has(variable)) return false
      seen.add(variable)
      for (const def of variable.defs) {
        if (def.type === 'CatchClause') return true
        if (def.type === 'Parameter') {
          const name = staticName(def.name)
          if (name !== null && ERROR_ROOT_NAME.test(name)) return true
          if (!strict && name !== null && ERROR_TEXT_NAME.test(name)) return true
        }
        if (def.type === 'Variable' && def.node.init) {
          if (isErrorText(sourceCode, def.node.init, seen, strict)) return true
          // `const data = err.data` — a local bound to the refusal's payload
          const init = unwrap(def.node.init)
          if (
            init &&
            init.type === 'MemberExpression' &&
            ERROR_PAYLOAD_MEMBERS.has(propertyName(init)) &&
            isErrorRooted(sourceCode, init.object)
          ) {
            return true
          }
        }
      }
      return false
    }
    case 'MemberExpression': {
      const prop = propertyName(n)
      if (prop === 'message' || prop === 'reason') {
        return isErrorRooted(sourceCode, n.object) || (!strict && isNamedErrorText(n.object))
      }
      if (prop !== null && ERROR_PAYLOAD_MEMBERS.has(prop)) {
        return isErrorRooted(sourceCode, n.object)
      }
      return false
    }
    case 'CallExpression': {
      const prop = propertyName(n.callee)
      if (prop !== null && NORMALISERS.has(prop) && n.arguments.length === 0) {
        return isErrorText(sourceCode, n.callee.object, seen, strict)
      }
      if (
        n.callee.type === 'Identifier' &&
        n.callee.name === 'String' &&
        n.arguments.length === 1
      ) {
        return isErrorText(sourceCode, n.arguments[0], seen, strict)
      }
      return isErrorAnswerCall(n)
    }
    default:
      return false
  }
}

/** A string literal that names something, or a template literal with nothing
 *  substituted. The EMPTY string is not one: `text === ''` asks whether there
 *  is text at all, which decides nothing about what it says. */
function isTextLiteral(node) {
  const n = unwrap(node)
  if (!n) return false
  if (n.type === 'Literal') return typeof n.value === 'string' && n.value !== ''
  if (n.type === 'TemplateLiteral') {
    return n.expressions.length === 0 && n.quasis[0].value.cooked !== ''
  }
  return false
}

/** A literal that reads as PROSE rather than as a declared code: a phrase has
 *  whitespace in it, and no protocol value does (`'unknown-session'`,
 *  `'vault-sealed'`, `'ask'`). This is the line between reading a closed set of
 *  codes and matching a sentence an error happened to contain. */
function isProseLiteral(node) {
  const n = unwrap(node)
  if (!n) return false
  if (n.type === 'Literal') return typeof n.value === 'string' && /\s/.test(n.value.trim())
  if (n.type === 'TemplateLiteral' && n.expressions.length === 0) {
    return /\s/.test((n.quasis[0].value.cooked ?? '').trim())
  }
  return false
}

/** The literal a decision names, in a form a person can read in the baseline:
 *  `'disconnected'`, `/not connected|closed/`. */
function literalTarget(sourceCode, node) {
  const n = unwrap(node)
  if (!n) return ''
  if (n.type === 'Literal') {
    if (typeof n.value === 'string') return JSON.stringify(n.value)
    if (n.regex) return `/${n.regex.pattern}/${n.regex.flags}`
    return null
  }
  if (n.type === 'TemplateLiteral' && n.expressions.length === 0) {
    return JSON.stringify(n.quasis[0].value.cooked ?? '')
  }
  return collapseSiteText(sourceCode.getText(n))
}

// ─── The rules ───────────────────────────────────────────────────────────────
/** Every rule reads the committed baseline once per process, and spends one
 *  credit per report: an entry exempts its COUNT, never the sentence. */
let baselineCounts = null
let baselineUsed = null

function baselineCredit(key) {
  if (baselineCounts === null) {
    baselineCounts = new Map(loadBaseline().map((entry) => [violationKey(entry), entry.count]))
    baselineUsed = new Map()
  }
  const allowed = baselineCounts.get(key) ?? 0
  const used = baselineUsed.get(key) ?? 0
  if (used >= allowed) return false
  baselineUsed.set(key, used + 1)
  return true
}

/**
 * Report one site, unless the baseline still has an occurrence of its key to
 * spend. `baseline: 'ignore'` is the collector's mode (and the updater's):
 * every site is reported so today's list can be regenerated.
 */
function report(pluginContext, info) {
  const options = pluginContext.options?.[0] ?? {}
  const rel = repoPathOf(pluginContext.filename)
  const symbol = info.symbol ?? enclosingSymbol(info.node)
  const target = info.target
  const site = siteTextAt(pluginContext.sourceCode, info.node.loc)
  const key = violationKey({ rule: info.ruleId, file: rel, symbol, target })
  if (options.baseline !== 'ignore' && baselineCredit(key)) return
  pluginContext.report({
    node: info.node,
    messageId: info.messageId,
    data: { ...info.data, site, symbol, target },
  })
}

const BASELINE_OPTION_SCHEMA = [
  {
    type: 'object',
    properties: { baseline: { enum: ['read', 'ignore'] } },
    additionalProperties: false,
  },
]

const ERROR_TEXT_RULE = 'nocx/no-error-text-match'
const SUBSCRIPTION_RULE = 'nocx/no-connection-subscription'
const OPEN_RULE = 'nocx/no-connection-open'
const LOSS_RULE = 'nocx/no-connection-loss'

function errorTextMatchRule() {
  return {
    meta: {
      type: 'suggestion',
      docs: {
        description:
          'Reject deciding a fact by matching an error message or a refusal reason string. See ADR-ba6xet1b.',
      },
      schema: BASELINE_OPTION_SCHEMA,
      messages: {
        errorTextMatch:
          'This decides something by matching error text ("{{site}}"). A failure is not a sentence ' +
          'to read: ask the backend for the state it produced (a typed reason on the result, ' +
          'pane.connection) instead of matching a message or a refusal string (ADR-ba6xet1b; ' +
          'nocx-oalo1.3.5).',
      },
    },
    create(context) {
      const rel = repoPathOf(context.filename)
      if (!isScopedFile(rel)) return {}
      const sourceCode = context.sourceCode
      const flag = (node, target) =>
        report(context, { ruleId: ERROR_TEXT_RULE, node, messageId: 'errorTextMatch', target })
      return {
        CallExpression(node) {
          const callee = unwrap(node.callee)
          if (!callee || callee.type !== 'MemberExpression') return
          const method = propertyName(callee)
          if (method === null) return
          // The text is the receiver in E.includes(…) and the argument in
          // RE.test(E) — either way it is being matched.
          if (TEXT_QUERIES.has(method) || method === 'indexOf' || method === 'lastIndexOf') {
            const subject = isErrorText(sourceCode, callee.object)
              ? callee.object
              : isErrorText(sourceCode, node.arguments[0])
                ? node.arguments[0]
                : null
            if (subject) flag(node, `${method}:${literalTarget(sourceCode, node.arguments[0])}`)
            return
          }
          if (method === 'match') {
            if (isErrorText(sourceCode, callee.object)) {
              flag(node, `match:${literalTarget(sourceCode, node.arguments[0])}`)
            }
            return
          }
          if (method === 'test') {
            if (isErrorText(sourceCode, node.arguments[0])) {
              flag(node, `test:${literalTarget(sourceCode, callee.object)}`)
            }
            return
          }
          // `REFUSALS.has(reason)` — a set of refusal strings whose contents
          // are not in this file, so the set's name is what can be read.
          if (method === 'has' && /REFUSAL|REASON/.test(staticName(callee.object) ?? '')) {
            if (isErrorText(sourceCode, node.arguments[0])) {
              flag(node, `has:${collapseSiteText(sourceCode.getText(node.arguments[0]))}`)
            }
          }
        },
        BinaryExpression(node) {
          if (node.operator !== '===' && node.operator !== '!==') return
          const decided = (literal, other) =>
            (isTextLiteral(literal) && isErrorText(sourceCode, other, new Set(), true)) ||
            (isProseLiteral(literal) && isErrorText(sourceCode, other))
          if (decided(node.left, node.right)) {
            flag(node, `${node.operator}:${literalTarget(sourceCode, node.left)}`)
          } else if (decided(node.right, node.left)) {
            flag(node, `${node.operator}:${literalTarget(sourceCode, node.right)}`)
          }
        },
        SwitchStatement(node) {
          if (isErrorText(sourceCode, node.discriminant, new Set(), true)) {
            flag(
              node.discriminant,
              `switch:${collapseSiteText(sourceCode.getText(node.discriminant))}`,
            )
            return
          }
          const proseCase = node.cases.find((c) => c.test && isProseLiteral(c.test))
          if (proseCase && isErrorText(sourceCode, node.discriminant)) {
            flag(node.discriminant, `switch:${literalTarget(sourceCode, proseCase.test)}`)
          }
        },
      }
    },
  }
}

function connectionSubscriptionRule() {
  return {
    meta: {
      type: 'suggestion',
      docs: {
        description:
          'Subscribe to a connection notification only inside frontend/src/connection/. See ADR-ba6xet1b.',
      },
      schema: BASELINE_OPTION_SCHEMA,
      messages: {
        subscription:
          '"{{name}}" is the connection\'s own answer, so it is handled in one place ' +
          '(frontend/src/connection/, nocx-oalo1 stages 3-4). Fold it there and draw what it ' +
          'publishes: a second subscriber decides a connection fact away from its owner ' +
          '(ADR-ba6xet1b).',
        noTypeInfo:
          'The connection-owner rules need the TypeScript checker and this lint run did not ' +
          'provide one. Point parserOptions at the project (frontend/tsconfig.json); a check ' +
          'that silently stops resolving identity is worse than none (ADR-ba6xet1b).',
      },
    },
    create(context) {
      const rel = repoPathOf(context.filename)
      if (!isScopedFile(rel) || isConnectionFile(rel)) return {}
      const services = typeServices(context)
      if (!services) return { Program: () => reportNoTypeInfo(context) }
      const checker = services.program.getTypeChecker()
      return {
        CallExpression(node) {
          const callee = unwrap(node.callee)
          if (!callee || callee.type !== 'MemberExpression' || propertyName(callee) !== 'subscribe')
            return
          const arg = node.arguments[0]
          if (!arg) return
          const tsArg = tsNodeOf(services, arg)
          if (!tsArg) return
          const names = literalStrings(checker.getTypeAtLocation(tsArg))
            .filter((name) => Object.hasOwn(CONNECTION_NOTIFICATIONS, name))
            .sort()
          if (names.length === 0) return
          report(context, {
            ruleId: SUBSCRIPTION_RULE,
            node: arg,
            messageId: 'subscription',
            target: names.join('+'),
            data: { name: names.map((n) => `'${n}'`).join(' and ') },
          })
        },
      }
    },
  }
}

function connectionOpenRule() {
  return {
    meta: {
      type: 'suggestion',
      docs: {
        description:
          'Open or reconnect an SSH session only inside frontend/src/connection/. See ADR-ba6xet1b.',
      },
      schema: BASELINE_OPTION_SCHEMA,
      messages: {
        open:
          '"{{name}}": {{what}}. There is one entry: send intent to the connection owner and draw ' +
          'its answer (ADR-ba6xet1b). Until frontend/src/connection/ exists (nocx-oalo1 stages 3 ' +
          'and 7) the decision belongs to the backend and that one seam, not here.',
        noTypeInfo:
          'The connection-owner rules need the TypeScript checker and this lint run did not ' +
          'provide one. Point parserOptions at the project (frontend/tsconfig.json); a check ' +
          'that silently stops resolving identity is worse than none (ADR-ba6xet1b).',
      },
    },
    create(context) {
      const rel = repoPathOf(context.filename)
      if (!isScopedFile(rel) || isConnectionFile(rel)) return {}
      const services = typeServices(context)
      if (!services) return { Program: () => reportNoTypeInfo(context) }
      const checker = services.program.getTypeChecker()

      const flag = (node, name) =>
        report(context, {
          ruleId: OPEN_RULE,
          node,
          messageId: 'open',
          target: name,
          data: { name, what: CONNECTION_ENTRIES[name] },
        })

      /** Report a member reference node (property name, key, or binding name)
       *  when the symbol it names is declared on the ipc client type. */
      const flagReference = (nameNode) => {
        const name = staticName(nameNode)
        if (name === null || !OPEN_NAMES.has(name)) return
        if (declaredOnIpcType(checker, referencedSymbol(checker, services, nameNode))) {
          flag(nameNode, name)
        }
      }

      return {
        // A call, `.bind`, an optional read — every one of them is a property
        // access, and the symbol at the property is the declaration.
        MemberExpression(node) {
          if (node.object.type === 'Super') return
          flagReference(node.property)
        },
        // The declaration on the client type is the entry itself: the
        // implementation is the second surface, even where nothing calls it.
        MethodDefinition(node) {
          flagReference(node.key)
        },
        PropertyDefinition(node) {
          flagReference(node.key)
        },
        // `const { openSSHSession } = client` — the binding names the member,
        // and the pattern's own type is the client, so the property resolves
        // through the checker rather than through the local binding.
        ObjectPattern(node) {
          for (const property of node.properties) {
            if (property.type !== 'Property') continue
            const name = staticName(property.key)
            if (name === null || !OPEN_NAMES.has(name)) continue
            if (declaredOnIpcType(checker, propertyOfPattern(checker, services, node, name))) {
              flag(property.key, name)
            }
          }
        },
      }
    },
  }
}

function connectionLossRule() {
  return {
    meta: {
      type: 'suggestion',
      docs: {
        description:
          "Reject minting a session's loss in the renderer (an `interrupted` exit). See ADR-ba6xet1b.",
      },
      schema: BASELINE_OPTION_SCHEMA,
      messages: {
        loss:
          'This mints a lost session in the renderer ("{{site}}"). Whether a session is gone, and ' +
          "why, is the connection owner's answer: draw it, do not construct it " +
          '(ADR-ba6xet1b; frontend/src/connection/ from nocx-oalo1 stage 3).',
        noTypeInfo:
          'The connection-owner rules need the TypeScript checker and this lint run did not ' +
          'provide one. Point parserOptions at the project (frontend/tsconfig.json); a check ' +
          'that silently stops resolving identity is worse than none (ADR-ba6xet1b).',
      },
    },
    create(context) {
      const rel = repoPathOf(context.filename)
      if (!isScopedFile(rel) || isConnectionFile(rel)) return {}
      const services = typeServices(context)
      if (!services) return { Program: () => reportNoTypeInfo(context) }
      const checker = services.program.getTypeChecker()
      const flag = (node) =>
        report(context, {
          ruleId: LOSS_RULE,
          node,
          messageId: 'loss',
          target: 'interrupted',
        })
      return {
        Property(node) {
          // `cause: x`, `'cause': x` and `['cause']: x` all mint the same
          // member; the computed spelling must not be a way around the rule.
          let name = null
          if (!node.computed) name = staticName(node.key)
          else {
            const key = unwrap(node.key)
            if (key?.type === 'Literal' && typeof key.value === 'string') name = key.value
          }
          if (name !== 'cause') return
          const tsValue = tsNodeOf(services, node.value)
          if (!tsValue) return
          // `interrupted` is the one Exit cause that means a loss
          // (contracts/exit.schema.json): a renderer that writes it has decided
          // the session is gone. The TYPE is what is asked, so a const named
          // anything at all still counts.
          const type = checker.getTypeAtLocation(tsValue)
          // The literal type, not a union that merely includes it: a log line
          // writing `cause: exit.cause` carries the whole closed set, and
          // printing a cause is not minting one.
          if (type.isStringLiteral?.() && type.value === 'interrupted') flag(node)
        },
      }
    },
  }
}

/** A scoped file whose lint run carries no type program: report it once, at
 *  the top of the file, instead of silently weakening three rules. */
function reportNoTypeInfo(context) {
  context.report({
    loc: { start: { line: 1, column: 0 }, end: { line: 1, column: 1 } },
    messageId: 'noTypeInfo',
  })
}

/** The rules, as eslint.config.js's `nocx` plugin carries them. */
export const connectionOwnerRules = {
  'no-error-text-match': errorTextMatchRule(),
  'no-connection-subscription': connectionSubscriptionRule(),
  'no-connection-open': connectionOpenRule(),
  'no-connection-loss': connectionLossRule(),
}

// ─── Baseline ────────────────────────────────────────────────────────────────
/** The key of one baselined site: what decides (`rule`), where (`file`), in
 *  which function (`symbol`), and the resolved member or literal (`target`).
 *  The COUNT lives on the entry and is not part of its key. */
export function violationKey(v) {
  return `${v.rule}:${v.file}:${v.symbol}:${v.target}`
}

/** Load the committed baseline. A missing file is an empty list, not a crash:
 *  the first `--allow-growth` run creates it, and a tree without one is a tree
 *  where every site is new. */
export function loadBaseline(path = BASELINE_PATH) {
  try {
    const data = JSON.parse(readFileSync(path, 'utf8'))
    return data.violations ?? []
  } catch {
    return []
  }
}

/** An entry with no reason is an unexplained exception: it names a site that
 *  decides a connection fact, and nobody wrote down why it may or who removes
 *  it. */
export function unreasonedEntries(baseline = loadBaseline()) {
  return baseline.filter((entry) => typeof entry.reason !== 'string' || entry.reason.trim() === '')
}

export function unknownRuleEntries(baseline = loadBaseline()) {
  return baseline.filter((entry) => !Object.hasOwn(RULES, entry.rule))
}

/** Every key with the number of sites the rule reports for it. */
export function countsByKey(violations) {
  const counts = new Map()
  for (const v of violations) {
    const key = violationKey(v)
    counts.set(key, (counts.get(key) ?? 0) + 1)
  }
  return counts
}

/**
 * The comparison the CLI fails on. A key's count may not grow, a key the
 * baseline does not have is new, and a count that SHRANK or a key that
 * disappeared is stale — the baseline may only move when the updater moves it,
 * so a shrink is noticed rather than silently leaving slack in the exemption.
 */
export function compareToBaseline(current, baseline = loadBaseline()) {
  const found = countsByKey(current)
  const known = new Map(baseline.map((entry) => [violationKey(entry), entry]))
  const fresh = []
  const grew = []
  const shrank = []
  const stale = []
  for (const [key, count] of found) {
    const entry = known.get(key)
    if (!entry) {
      fresh.push({ key, count })
      continue
    }
    const allowed = entry.count ?? 1
    if (count > allowed) grew.push({ entry, count })
    else if (count < allowed) shrank.push({ entry, count })
  }
  for (const [key, entry] of known) {
    if (!found.has(key)) stale.push(entry)
  }
  return { fresh, grew, shrank, stale }
}

// ─── Collection (shared by the CLI, the updater and the fixture gate) ────────
const COLLECTOR_PARSER_OPTIONS = {
  project: ['./tsconfig.json'],
  tsconfigRootDir: FRONTEND_DIR,
}

/**
 * The collector runs the SAME rule objects through a wrapper that records the
 * structured report instead of formatting it. A LintMessage carries a string;
 * a baseline key needs the symbol and target the rule resolved, and re-deriving
 * either here would be a second implementation of the question. The wrapper
 * intercepts exactly what `report()` was handed, so the two entry points cannot
 * disagree about what a site is called.
 */
function collectionConfig(sink, parserOptions) {
  const wrapped = {}
  for (const [name, rule] of Object.entries(connectionOwnerRules)) {
    wrapped[name] = {
      meta: rule.meta,
      create(context) {
        // A facade, not a proxy over the context: ESLint's `report` is a
        // non-configurable own property, and a proxy that answers it with
        // anything else violates the proxy invariant. The facade delegates
        // every read and replaces only `report`.
        const facade = new Proxy(
          {},
          {
            get(_target, prop) {
              if (prop === 'report') {
                return (descriptor) => sink.push({ rule: `nocx/${name}`, ...descriptor })
              }
              const value = context[prop]
              return typeof value === 'function' ? value.bind(context) : value
            },
            has(_target, prop) {
              return prop in context
            },
          },
        )
        return rule.create(facade)
      },
    }
  }
  return [
    {
      files: ['**/*.{ts,tsx}'],
      languageOptions: { parser: tseslint.parser, parserOptions },
      plugins: { nocx: { rules: wrapped } },
      rules: Object.fromEntries(
        Object.keys(wrapped).map((name) => [`nocx/${name}`, ['error', { baseline: 'ignore' }]]),
      ),
    },
  ]
}

const collectorLinter = new Linter({ configType: 'flat' })

/**
 * Violations in one file's source, decoupled from the filesystem and from git
 * so a test can hand it a fixture directly. `file` is only ever the label on
 * the returned records, and decides the scope.
 *
 * `parserOptions` is the test seam: a fixture's path does not exist on disk,
 * so it is linted against a program the test built from the fixture text.
 * Production callers pass nothing and the tsconfig project is used.
 */
export function violationsInText(file, text, parserOptions) {
  if (!isScopedFile(file)) return []
  const filename = resolve(REPO_ROOT, file)
  const sink = []
  const messages = collectorLinter.verify(
    text,
    collectionConfig(sink, parserOptions ?? COLLECTOR_PARSER_OPTIONS),
    { filename },
  )
  const fatal = messages.find((message) => message.fatal)
  if (fatal) {
    throw new Error(
      `linting ${file} failed before the connection-owner rules ran: ${fatal.message}`,
    )
  }
  return sink.map(({ rule, node, data }) => {
    if (!data?.symbol || !data?.target) {
      throw new Error(
        `${rule} reported ${file}:${node?.loc?.start?.line ?? 0} without a symbol/target ` +
          'identity. The collector cannot key a site it cannot resolve (ADR-ba6xet1b).',
      )
    }
    return {
      rule,
      file,
      symbol: data.symbol,
      target: data.target,
      text: data.site,
      line: node?.loc?.start?.line ?? 0,
    }
  })
}

/**
 * Every frontend/src source the rules read, repo-relative and '/'-separated:
 * tracked, staged and untracked-but-not-ignored, which is what `eslint .`
 * walks and what a commit would carry. `git ls-files` reads the INDEX, so a
 * new file is included the moment it is staged — the moment pre-commit runs —
 * and an unstaged draft is included too, because leaving it out would let the
 * standalone check say OK about a tree eslint is about to fail.
 */
export function trackedSources() {
  const out = execFileSync(
    'git',
    ['ls-files', '-z', '--cached', '--others', '--exclude-standard', '--', 'frontend/src'],
    { cwd: REPO_ROOT, encoding: 'utf8', maxBuffer: 64 * 1024 * 1024 },
  )
  return out.split('\0').filter((f) => f !== '' && /\.tsx?$/.test(f) && isScopedFile(f))
}

/** Every violation across the scanned tree, with its identity. */
export function collectViolations() {
  const violations = []
  for (const file of trackedSources()) {
    violations.push(...violationsInText(file, readFileSync(resolve(REPO_ROOT, file), 'utf8')))
  }
  return violations
}

// ─── CLI entry point ─────────────────────────────────────────────────────────
if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const baseline = loadBaseline()
  const problems = [
    ...unknownRuleEntries(baseline).map((e) => `unknown rule ${e.rule}: ${e.file}: ${e.target}`),
    ...unreasonedEntries(baseline).map(
      (e) => `no reason written for ${e.rule}: ${e.file}: ${e.symbol}: ${e.target}`,
    ),
    ...baseline
      .filter((e) => !Number.isInteger(e.count) || e.count < 1)
      .map((e) => `bad occurrence count ${e.count} for ${e.rule}: ${e.file}: ${e.target}`),
  ]
  if (problems.length > 0) {
    for (const p of problems) console.error(`baseline: ${p}`)
    console.error(
      'lint-fixtures/connection-owners-baseline.json is the list of renderer sites that decide a ' +
        'connection fact today; each entry names why it may still and the stage that removes it ' +
        '(ADR-ba6xet1b).',
    )
    process.exit(1)
  }

  const current = collectViolations()
  const printAll = process.env.NOCX_BASELINE_UPDATE === '1'
  if (printAll) {
    for (const v of current) console.log(JSON.stringify(v))
  }

  const { fresh, grew, shrank, stale } = compareToBaseline(current, baseline)
  const occurrences = baseline.reduce((sum, entry) => sum + entry.count, 0)

  if (fresh.length > 0) {
    console.error(
      `${fresh.length} new renderer site(s) decide a connection fact. A fact about an SSH ` +
        'connection has one owner — the backend connection manager — and the renderer draws ' +
        'its answer (ADR-ba6xet1b, design §5.1). Take it through frontend/src/connection/, or ' +
        'add it deliberately: node lint-fixtures/update-connection-owners-baseline.mjs ' +
        '--allow-growth, and write its reason.',
    )
    for (const { key, count } of fresh) console.error(`  NEW  ${key} x${count}`)
  }
  if (grew.length > 0) {
    console.error(
      `${grew.length} baselined key(s) occur more often than their entry allows. An entry ` +
        'exempts a COUNT, not a sentence: a second site with an old name is still a second owner ' +
        '(ADR-ba6xet1b). Remove the new occurrence, or raise the count deliberately with ' +
        '`node lint-fixtures/update-connection-owners-baseline.mjs --allow-growth` and say why.',
    )
    for (const { entry, count } of grew) {
      console.error(`  GREW ${violationKey(entry)} — ${count} now, ${entry.count} baselined`)
    }
  }
  if (shrank.length > 0) {
    console.error(
      `${shrank.length} baselined key(s) occur less often than their entry says. The list may ` +
        'only shrink, and a stale count hides that it did: run ' +
        '`node lint-fixtures/update-connection-owners-baseline.mjs`.',
    )
    for (const { entry, count } of shrank) {
      console.error(`  SHRANK ${violationKey(entry)} — ${count} now, ${entry.count} baselined`)
    }
  }
  if (stale.length > 0) {
    console.error(
      `${stale.length} baseline entr${stale.length === 1 ? 'y is' : 'ies are'} no longer a site ` +
        'in the tree. The list may only shrink, and a stale entry hides that it did: run ' +
        '`node lint-fixtures/update-connection-owners-baseline.mjs` to drop it.',
    )
    for (const e of stale) console.error(`  STALE ${violationKey(e)}`)
  }

  if (fresh.length + grew.length + shrank.length + stale.length > 0) {
    process.exitCode = 1
  } else {
    console.log(
      `OK — renderer connection-owner ratchet: ${baseline.length} baselined key(s), ` +
        `${occurrences} occurrence(s), 0 new, 0 stale`,
    )
  }
}
