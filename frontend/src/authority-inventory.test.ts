// Authority inventory (ADR-0024 §1, §5, §6) — the guard import rules cannot
// provide. Enumerates every operation that requires lifecycle authority and
// pins each to an authority-bearing value (a domain or an attempt), never a
// boolean.
//
// The ADR-committed lifecycle state does not exist yet (it is the renderer
// half of epic nocx-u7uh; the child bead ids were not available in this
// worktree). So this file carries two kinds of contract:
//
//  1. MANIFEST — the intended signatures, one entry per operation. Entries
//     are either 'pending' (the forward-declared src/lifecycle/ module does
//     not exist yet: the test requires that it STAY absent, and fails the
//     moment it lands, forcing this entry to be flipped to 'live') or 'wake'
//     (the module exists today in its pre-ADR shape: the test requires the
//     authority check to FAIL, and fails the moment the surface gains the
//     authority type, forcing the flip). A 'live' entry requires the module,
//     the symbol and the authority-bearing parameter type to all exist. The
//     check is a real TypeScript AST inspection of the parameter type
//     annotations — not test.skip, not a comment, and not Function.toString
//     introspection.
//
//  2. PRE-ADR WAKE-UPS — today's stream-derived surfaces that ADR-0024
//     "Consequences" deletes (the `trusted` laundering rule, `trusted` on the
//     history record, `onMarker` as an entry point for anonymous kinds, the
//     `_shellIntegrated` latch, the boolean editor axis). The severance bead
//     (nocx-u7uh.1) deleted all five; the wake-ups and their checks were
//     removed with them as the acknowledgment.
import { describe, expect, it } from 'vitest'
import * as ts from 'typescript'
import { existsSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { join, resolve } from 'node:path'

const SRC_ROOT = resolve(fileURLToPath(new URL('..', import.meta.url)))

function parseSource(relPath: string): ts.SourceFile {
  const full = join(SRC_ROOT, relPath)
  const text = readFileSync(full, 'utf8')
  return ts.createSourceFile(relPath, text, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
}

function isExported(node: ts.Node): boolean {
  return (
    (ts.canHaveModifiers(node) &&
      ts.getModifiers(node)?.some((m) => m.kind === ts.SyntaxKind.ExportKeyword)) ??
    false
  )
}

/** The base names of parameter annotations, including nested callback parameters. */
function paramTypeNames(fn: ts.FunctionLikeDeclaration): string[] {
  const names: string[] = []
  const visitType = (type: ts.TypeNode): void => {
    names.push(type.getText().replace(/<.*>$/, ''))
    if (ts.isFunctionTypeNode(type) || ts.isConstructorTypeNode(type)) {
      for (const parameter of type.parameters) {
        if (parameter.type) visitType(parameter.type)
      }
    }
  }
  for (const p of fn.parameters) {
    if (p.type) visitType(p.type)
  }
  return names
}

function findExportFunction(
  source: ts.SourceFile,
  symbol: string,
): ts.FunctionLikeDeclaration | null {
  for (const stmt of source.statements) {
    if (ts.isFunctionDeclaration(stmt) && stmt.name?.text === symbol && isExported(stmt)) {
      return stmt
    }
    // An exported class method is an authority surface like any other
    // (CommandLedger.complete, BlockManager.freezeFromAttempt): the class
    // itself must be exported, and the method must be public.
    if (ts.isClassDeclaration(stmt) && stmt.name?.text !== undefined && isExported(stmt)) {
      for (const member of stmt.members) {
        if (
          ts.isMethodDeclaration(member) &&
          member.name !== undefined &&
          ts.isIdentifier(member.name) &&
          member.name.text === symbol &&
          (member.modifiers === undefined ||
            !member.modifiers.some((m) => m.kind === ts.SyntaxKind.PrivateKeyword))
        ) {
          return member
        }
      }
    }
    if (ts.isVariableStatement(stmt) && isExported(stmt)) {
      for (const decl of stmt.declarationList.declarations) {
        if (ts.isIdentifier(decl.name) && decl.name.text === symbol && decl.initializer) {
          if (ts.isArrowFunction(decl.initializer) || ts.isFunctionExpression(decl.initializer)) {
            return decl.initializer
          }
        }
      }
    }
  }
  return null
}

// ─── The manifest ────────────────────────────────────────────────────────────
// `symbol` + `authorityTypes` is the ADR-committed signature: the operation
// must take a parameter whose type annotation is one of the authority types.
// The types name the ADR vocabulary (ExecutionAttempt, IntegrationDomain,
// LifecycleState); the exact symbol names are this file's commitment until the
// epic lands, and the comment on each entry names the ADR section that fixes
// the requirement.
interface AuthorityEntry {
  op: string
  module: string
  symbol: string
  authorityTypes: string[]
  state: 'pending' | 'wake' | 'live'
  bead: string
  note: string
}

const BEAD = 'nocx-u7uh' // ADR-0024 renderer work; exact child bead ids TBD

const MANIFEST: AuthorityEntry[] = [
  {
    op: 'show the DOM editor',
    module: 'src/lifecycle/state.ts',
    symbol: 'shouldShowEditor',
    authorityTypes: ['LifecycleState'],
    state: 'live',
    bead: BEAD,
    note: 'ADR-0024 §6: the editor owns keys because the lifecycle axis says PromptReady(domain), not because a boolean does. The boolean axis (native-mode.ts shouldShowEditor(owned, nativeMode)) is deleted.',
  },
  {
    op: 'receive a backend-owned history receipt',
    module: 'src/history-client.ts',
    symbol: 'subscribeHistoryRecorded',
    authorityTypes: ['HistoryRecorded'],
    state: 'live',
    bead: BEAD,
    note: 'The backend lifecycle writer owns masking, persistence and capture offers; the renderer only consumes the history.recorded receipt for the frozen block.',
  },
  {
    op: 'complete a ledger record',
    module: 'src/command-ledger.ts',
    symbol: 'complete',
    authorityTypes: ['ExecutionAttempt'],
    state: 'live',
    bead: BEAD,
    note: 'ADR-0024 §5 (bead nocx-u7uh.7): a ledger record closes only on an authenticated attempt — exit status exactly once, an abandoned attempt unknown and never successful. onMarker stays deleted.',
  },
  {
    op: 'freeze a block from an authenticated attempt',
    module: 'src/scrollback/blocks.ts',
    symbol: 'freezeFromAttempt',
    authorityTypes: ['ExecutionAttempt'],
    state: 'live',
    bead: BEAD,
    note: 'ADR-0024 §7 (bead nocx-u7uh.7): the visual freeze is authorized by the authenticated completed attempt for the block bound to it — a stream D must not freeze a block (the kernel derivation freezeBlock is the gate; this paints it).',
  },
  {
    op: 'abandon a block from an abandoned attempt',
    module: 'src/scrollback/blocks.ts',
    symbol: 'abandonAttempt',
    authorityTypes: ['ExecutionAttempt'],
    state: 'live',
    bead: BEAD,
    note: 'ADR-0024 §5 (bead nocx-u7uh.7): an abandoned attempt is unknown and never successful — the block freezes as abandoned only from the attempt, never from a stream verdict.',
  },
  {
    op: 'complete an attempt',
    module: 'src/lifecycle/state.ts',
    symbol: 'completeAttempt',
    authorityTypes: ['ExecutionAttempt', 'IntegrationDomain'],
    state: 'live',
    bead: BEAD,
    note: "ADR-0024 §5: an attempt is open until an authenticated same-domain completion; nothing may mark it successful on the stream's say-so.",
  },
  {
    op: 'assign an exit status',
    module: 'src/lifecycle/state.ts',
    symbol: 'completeAttempt',
    authorityTypes: ['ExecutionAttempt'],
    state: 'live',
    bead: BEAD,
    note: 'ADR-0024 §5 interval: the exit code rides the authenticated completion; there is deliberately no separate status-assignment surface.',
  },
  {
    op: 'freeze an authoritative block',
    module: 'src/lifecycle/state.ts',
    symbol: 'freezeBlock',
    authorityTypes: ['ExecutionAttempt', 'IntegrationDomain'],
    state: 'live',
    bead: BEAD,
    note: 'ADR-0024 §7 render ordering: the visual freeze waits for the authenticated event AND the matching fence; a stream D must not freeze a block (today scrollback.freezeBlock takes the stream exitCode).',
  },
  {
    op: 'activate an environment',
    module: 'src/lifecycle/domains.ts',
    symbol: 'activateDomain',
    authorityTypes: ['IntegrationDomain'],
    state: 'live',
    bead: BEAD,
    note: 'ADR-0024 §2, §6: the domain stack transitions only on authenticated events. The OSC 636 readiness passport that once carried an expected-id invariant is deleted (bead nocx-u7uh.11) — nothing stream-derived can name a domain at all, which is strictly stronger than the tracker invariant it replaced.',
  },
  {
    op: 'enable integration-sensitive ssh rewriting',
    module: 'src/lifecycle/state.ts',
    symbol: 'rewriteAuthority',
    authorityTypes: ['LifecycleState'],
    state: 'live',
    bead: BEAD,
    note: 'ADR-0024 §1: integration-sensitive command rewriting needs authority. The _shellIntegrated latch it rode is deleted.',
  },
  {
    op: 'authorize a re-run',
    module: 'src/lifecycle/state.ts',
    symbol: 'rerunAuthority',
    authorityTypes: ['LifecycleState'],
    state: 'live',
    bead: BEAD,
    note: 'ADR-0024 §1: a re-run must be authorized by the attempt/domain, never by a block the stream forged.',
  },
]

describe('authority inventory — every authority operation takes a domain or an attempt', () => {
  for (const entry of MANIFEST) {
    const fullPath = join(SRC_ROOT, entry.module)
    const exists = existsSync(fullPath)

    if (entry.state === 'pending') {
      it(`${entry.op}: ${entry.module} is forward-declared and must not exist until the ADR work lands`, () => {
        expect(
          exists,
          `module ${entry.module} landed (${entry.bead}) — flip this manifest entry to 'live' and satisfy the signature contract`,
        ).toBe(false)
      })
      continue
    }

    it(`${entry.op}: ${entry.symbol} in ${entry.module} takes an authority-bearing value`, () => {
      expect(exists, `module ${entry.module} must exist for a ${entry.state} entry`).toBe(true)
      const source = parseSource(entry.module)
      const fn = findExportFunction(source, entry.symbol)
      expect(fn, `symbol ${entry.symbol} must be exported from ${entry.module}`).not.toBeNull()
      const types = paramTypeNames(fn as ts.FunctionLikeDeclaration)
      const hasAuthority = entry.authorityTypes.some((t) => types.includes(t))
      if (entry.state === 'wake') {
        // Pre-ADR: the surface must exist but must NOT yet carry authority.
        expect(
          hasAuthority,
          `${entry.symbol} now carries ${entry.authorityTypes.join('/')} — the ADR-0024 surface landed; flip this entry to 'live'`,
        ).toBe(false)
      } else {
        expect(
          hasAuthority,
          `${entry.symbol} must take a parameter typed ${entry.authorityTypes.join(' or ')} — got: ${types.join(', ') || '(none)'}. ${entry.note}`,
        ).toBe(true)
      }
    })
  }
})
