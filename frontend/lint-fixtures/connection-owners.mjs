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
 *       outside frontend/src/connection/. Those two are the connection's own
 *       answers: the backend's measurement of reaching a host, and a session's
 *       end, from which ipc.ts mints an `interrupted` exit. lifecycle.changed
 *       is deliberately NOT here: it is the authenticated execution lifecycle
 *       (ADR-0024), whose owner is src/lifecycle/, and rule 9 already fences
 *       that boundary.
 *   nocx/no-connection-open
 *       A call to, or a class member declaring, one of the open/reconnect
 *       entries: openSSHSession, openSSHSessionByHost, retrySessionAttach,
 *       retryAttach, reconnect. Seven entry paths into one open is what the
 *       design's §2 counts; stage 7 of nocx-oalo1 collapses them into one.
 *   nocx/no-connection-loss
 *       An object literal minting `cause: 'interrupted'` — the renderer
 *       declaring a session lost. `interrupted` is the one exit cause that
 *       means a loss, and it is the connection manager's verdict, not the
 *       renderer's: ipc.ts invents it twice today, once from an exit whose
 *       cause was not a shell exit and once from a failed attach.
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
 *   - It is an AST over one file, not a type checker. A string is not
 *     distinguishable from "error text" by its type — every one of them
 *     arrives as `string` — so the error-text rule recognises the SHAPES above
 *     and the NAMES an author chose. `const x = failure(); if (x.includes(…))`
 *     is invisible: the shape stops at a function call whose name does not say
 *     reason/message. A value that crossed a module boundary has no shape at
 *     all.
 *   - It follows the plumbing a person actually writes — a local bound to
 *     err.message, a param named reason, .toLowerCase() — but not a value
 *     returned through an interface, an alias passed by reference, or a
 *     decision written as a map, a differently-named Set, or a regex held in
 *     an object property.
 *   - It reads no Go. A new backend owner is the other half's business
 *     (.githooks/check-connection-owners.mjs).
 *   - A `reconnect()` on something that is not an SSH connection (a socket, a
 *     device) would be refused by name. That is why the names are a closed
 *     list rather than a shape, and why adding one is a decision.
 *
 * THE BASELINE, and why it is a file rather than eslint-disable comments:
 * `lint-fixtures/connection-owners-baseline.json` is today's list, keyed
 * `rule:file:text` where the text is the reported site's own source text with
 * whitespace collapsed — never a line number, so an unrelated edit above a
 * baselined site costs nothing. It may only SHRINK: the updater refuses growth
 * without --allow-growth, an added entry starts with an empty reason, and this
 * check refuses an empty one. Stages 3, 4 and 7 of nocx-oalo1 empty it. An
 * eslint-disable comment would be invisible to that list and would outlive the
 * stage that owes the removal, which is the whole point of keeping it here.
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

const __dirname = dirname(fileURLToPath(import.meta.url))
const FRONTEND_DIR = resolve(__dirname, '..')
const REPO_ROOT = resolve(FRONTEND_DIR, '..')
export const BASELINE_PATH = resolve(__dirname, 'connection-owners-baseline.json')

/** The one directory allowed to decide a connection fact in the renderer. */
export const CONNECTION_DIR = 'frontend/src/connection/'

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
  reconnect: "the pane's own reconnect loop entry (terminal-content.ts)",
}

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

function staticName(node) {
  if (!node) return null
  if (node.type === 'Identifier') return node.name
  if (node.type === 'PrivateIdentifier') return `#${node.name}`
  if (node.type === 'Literal' && typeof node.value === 'string') return node.value
  return null
}

/** The property an expression reads, when it reads a static one. */
function propertyName(node) {
  if (!node || node.type !== 'MemberExpression' || node.computed) return null
  return staticName(node.property)
}

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

// ─── The rules ───────────────────────────────────────────────────────────────
/** Collapse a site's source text to one line, so the baseline key survives an
 *  unrelated reformat of the site and reads as what a person would grep. */
export function collapseSiteText(text) {
  return text.replace(/\s+/g, ' ').trim()
}

/** The site text of a report, from its own coordinates — the same derivation
 *  the collector below makes from a lint message, so the eslint run and
 *  `node lint-fixtures/connection-owners.mjs` can never disagree about what a
 *  site is called. */
function siteTextAt(sourceCode, loc) {
  const start = sourceCode.getIndexFromLoc(loc.start)
  const end = sourceCode.getIndexFromLoc(loc.end)
  return collapseSiteText(sourceCode.text.slice(start, end))
}

/** Every rule reads the committed baseline once per process. */
let baselineKeys = null
function isBaselined(ruleId, rel, text) {
  if (baselineKeys === null) baselineKeys = new Set(loadBaseline().map(violationKey))
  return baselineKeys.has(`${ruleId}:${rel}:${text}`)
}

/**
 * Report one site, unless it is in the baseline. `baseline: 'ignore'` is the
 * collector's mode (and the updater's): every site is reported so today's list
 * can be regenerated. The production rules pass no options and read the
 * baseline.
 */
function report(pluginContext, info) {
  const options = pluginContext.options?.[0] ?? {}
  const rel = repoPathOf(pluginContext.filename)
  const text = siteTextAt(pluginContext.sourceCode, info.node.loc)
  if (options.baseline !== 'ignore' && isBaselined(info.ruleId, rel, text)) return
  pluginContext.report({
    node: info.node,
    messageId: info.messageId,
    data: { ...info.data, site: text },
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
      const flag = (node) =>
        report(context, { ruleId: ERROR_TEXT_RULE, node, messageId: 'errorTextMatch' })
      return {
        CallExpression(node) {
          const callee = unwrap(node.callee)
          if (!callee || callee.type !== 'MemberExpression' || callee.computed) return
          const method = staticName(callee.property)
          if (method === null) return
          // The text is the receiver in E.includes(…) and the argument in
          // RE.test(E) — either way it is being matched.
          if (TEXT_QUERIES.has(method) || method === 'indexOf' || method === 'lastIndexOf') {
            if (
              isErrorText(sourceCode, callee.object) ||
              isErrorText(sourceCode, node.arguments[0])
            ) {
              flag(node)
            }
            return
          }
          if (method === 'match') {
            if (isErrorText(sourceCode, callee.object)) flag(node)
            return
          }
          if (method === 'test') {
            if (isErrorText(sourceCode, node.arguments[0])) flag(node)
            return
          }
          // `REFUSALS.has(reason)` — a set of refusal strings whose contents
          // are not in this file, so the set's name is what can be read.
          if (method === 'has' && /REFUSAL|REASON/.test(staticName(callee.object) ?? '')) {
            if (isErrorText(sourceCode, node.arguments[0])) flag(node)
          }
        },
        BinaryExpression(node) {
          if (node.operator !== '===' && node.operator !== '!==') return
          const decided = (literal, other) =>
            (isTextLiteral(literal) && isErrorText(sourceCode, other, new Set(), true)) ||
            (isProseLiteral(literal) && isErrorText(sourceCode, other))
          if (decided(node.left, node.right) || decided(node.right, node.left)) flag(node)
        },
        SwitchStatement(node) {
          if (isErrorText(sourceCode, node.discriminant, new Set(), true)) {
            flag(node.discriminant)
            return
          }
          const proseCase = node.cases.some((c) => c.test && isProseLiteral(c.test))
          if (proseCase && isErrorText(sourceCode, node.discriminant)) flag(node.discriminant)
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
      },
    },
    create(context) {
      const rel = repoPathOf(context.filename)
      if (!isScopedFile(rel) || isConnectionFile(rel)) return {}
      return {
        CallExpression(node) {
          const callee = unwrap(node.callee)
          if (!callee || callee.type !== 'MemberExpression' || callee.computed) return
          if (staticName(callee.property) !== 'subscribe') return
          const arg = unwrap(node.arguments[0])
          if (!arg || arg.type !== 'Literal' || typeof arg.value !== 'string') return
          const name = arg.value
          if (!Object.hasOwn(CONNECTION_NOTIFICATIONS, name)) return
          report(context, {
            ruleId: SUBSCRIPTION_RULE,
            node: arg,
            messageId: 'subscription',
            data: { name },
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
      },
    },
    create(context) {
      const rel = repoPathOf(context.filename)
      if (!isScopedFile(rel) || isConnectionFile(rel)) return {}
      const checkName = (nameNode) => {
        const name = staticName(nameNode)
        if (name === null || !Object.hasOwn(CONNECTION_ENTRIES, name)) return
        report(context, {
          ruleId: OPEN_RULE,
          node: nameNode,
          messageId: 'open',
          data: { name, what: CONNECTION_ENTRIES[name] },
        })
      }
      return {
        CallExpression(node) {
          const callee = unwrap(node.callee)
          if (!callee) return
          if (callee.type === 'MemberExpression') checkName(callee.property)
          else if (callee.type === 'Identifier') checkName(callee)
        },
        // A class member IS the entry, even where nothing calls it yet: the
        // implementation is the second surface. Interface signatures are not
        // read — a type describes the seam, it does not decide anything.
        MethodDefinition(node) {
          checkName(node.key)
        },
        PropertyDefinition(node) {
          checkName(node.key)
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
      },
    },
    create(context) {
      const rel = repoPathOf(context.filename)
      if (!isScopedFile(rel) || isConnectionFile(rel)) return {}
      return {
        Property(node) {
          if (node.computed || staticName(node.key) !== 'cause') return
          const value = unwrap(node.value)
          // `interrupted` is the one Exit cause that means a loss
          // (contracts/exit.schema.json): a renderer that writes it has decided
          // the session is gone.
          if (!value || value.type !== 'Literal' || value.value !== 'interrupted') return
          report(context, { ruleId: LOSS_RULE, node, messageId: 'loss' })
        },
      }
    },
  }
}

/** The rules, as eslint.config.js's `nocx` plugin carries them. */
export const connectionOwnerRules = {
  'no-error-text-match': errorTextMatchRule(),
  'no-connection-subscription': connectionSubscriptionRule(),
  'no-connection-open': connectionOpenRule(),
  'no-connection-loss': connectionLossRule(),
}

// ─── Baseline ────────────────────────────────────────────────────────────────
export function violationKey(v) {
  return `${v.rule}:${v.file}:${v.text}`
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

// ─── Collection (shared by the CLI, the updater and the fixture gate) ────────
/** The site text of a report, derived the way the rules derive it. */
function siteTextOfMessage(source, message) {
  const lines = source.split('\n')
  const offset = (line, column) => {
    let index = 0
    for (let i = 0; i < line - 1; i += 1) index += lines[i].length + 1
    return index + column - 1
  }
  const start = offset(message.line, message.column)
  const end =
    message.endLine !== undefined && message.endColumn !== undefined
      ? offset(message.endLine, message.endColumn)
      : start
  return collapseSiteText(source.slice(start, end))
}

const COLLECTOR_CONFIG = [
  {
    files: ['**/*.{ts,tsx}'],
    languageOptions: { parser: tseslint.parser },
    plugins: { nocx: { rules: connectionOwnerRules } },
    rules: Object.fromEntries(
      Object.keys(connectionOwnerRules).map((name) => [
        `nocx/${name}`,
        ['error', { baseline: 'ignore' }],
      ]),
    ),
  },
]

const collectorLinter = new Linter({ configType: 'flat' })

/**
 * Violations in one file's source, decoupled from the filesystem and from git
 * so a test can hand it a fixture directly. `file` is only ever the label on
 * the returned records, and decides the scope.
 */
export function violationsInText(file, text) {
  if (!isScopedFile(file)) return []
  const messages = collectorLinter.verify(text, COLLECTOR_CONFIG, {
    filename: resolve(REPO_ROOT, file),
  })
  const violations = []
  for (const message of messages) {
    if (typeof message.ruleId !== 'string' || !Object.hasOwn(RULES, message.ruleId)) continue
    violations.push({
      rule: message.ruleId,
      file,
      text: siteTextOfMessage(text, message),
      line: message.line,
    })
  }
  return violations
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

/** Every violation across the scanned tree. */
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
    ...unknownRuleEntries(baseline).map((e) => `unknown rule ${e.rule}: ${e.file}: ${e.text}`),
    ...unreasonedEntries(baseline).map(
      (e) => `no reason written for ${e.rule}: ${e.file}: ${e.text}`,
    ),
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
    for (const v of current)
      console.log(JSON.stringify({ rule: v.rule, file: v.file, text: v.text }))
  }

  const known = new Set(baseline.map(violationKey))
  const found = new Set(current.map(violationKey))
  const fresh = current.filter((v) => !known.has(violationKey(v)))
  const stale = baseline.filter((e) => !found.has(violationKey(e)))

  if (stale.length > 0) {
    console.error(
      `${stale.length} baseline entr${stale.length === 1 ? 'y is' : 'ies are'} no longer a site in ` +
        'the tree. The list may only shrink, and a stale entry hides that it did: run ' +
        '`node lint-fixtures/update-connection-owners-baseline.mjs` to drop it.',
    )
    for (const e of stale) console.error(`  STALE ${e.rule}: ${e.file}: ${e.text}`)
    process.exitCode = 1
  }

  if (fresh.length > 0) {
    console.error(
      `${fresh.length} new renderer site(s) decide a connection fact. A fact about an SSH ` +
        'connection has one owner — the backend connection manager — and the renderer draws ' +
        'its answer (ADR-ba6xet1b, design §5.1). Take it through frontend/src/connection/, or ' +
        'add it deliberately: node lint-fixtures/update-connection-owners-baseline.mjs ' +
        '--allow-growth, and write its reason.',
    )
    for (const v of fresh) console.error(`  NEW ${v.rule}: ${v.file}:${v.line}  ${v.text}`)
    process.exitCode = 1
  } else if (stale.length === 0) {
    console.log(
      `OK — renderer connection-owner ratchet: ${baseline.length} baselined site(s), 0 new, 0 stale`,
    )
  }
}
