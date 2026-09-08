import { describe, it, expect } from 'vitest'
import { readFileSync, readdirSync } from 'node:fs'
import { join } from 'node:path'

// THE UNLOCK'S TITLE IS A SENTENCE THE PRODUCT COMPLETES (nocx-0nhec).
//
// UnlockDialog renders `Unlock the vault to ${reason}`, because a bare "Unlock
// the vault" cannot be told apart from the key and the connection prompts
// (nocx-s8jn). That makes `reason` a VERB PHRASE — "audit a skill", "save this
// connection" — and a caller that supplies a sentence produces:
//
//     Unlock the vault to The vault is locked. Unlock it to continue.
//
// Two callers did, and neither was the one the contract's comment sat beside:
// the dispatcher's global sealed seam and main.tsx's backend-unlock
// subscription, which are between them the two most common ways a person meets
// this prompt at all.
//
// So the check walks the CALLERS rather than one of them. It reads the source
// because the contract is about the argument a call site writes, and a
// behavioural test can only cover the paths its author remembered — which is
// exactly the failure being fixed.

const SETTERS = ['openUnlock', 'setUnlockReason', 'ensureBeforeSave', 'saveSecretWithVault']

// A literal that is the LAST argument of one of those calls, on one line. The
// `[^)]*?` deliberately refuses to cross a closing paren, so a call whose
// argument is an inline arrow function contributes nothing rather than the
// strings inside that function's body.
const CALL = new RegExp(`(?:${SETTERS.join('|')})\\([^)]*?(['"])((?:(?!\\1).)*)\\1\\s*\\)`, 'g')

function sourceFiles(dir: string): string[] {
  const out: string[] = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name)
    if (entry.isDirectory()) {
      out.push(...sourceFiles(path))
      continue
    }
    if (!/\.tsx?$/.test(entry.name) || /\.test\.tsx?$/.test(entry.name)) continue
    out.push(path)
  }
  return out
}

describe('every unlock reason completes "Unlock the vault to …"', () => {
  const found: { file: string; reason: string }[] = []
  for (const file of sourceFiles(__dirname)) {
    const body = readFileSync(file, 'utf8')
    for (const match of body.matchAll(CALL)) {
      found.push({ file, reason: match[2] })
    }
  }

  it('finds the call sites at all', () => {
    // Without this the whole file passes vacuously the day the regex or the
    // setter names drift — which is the way a scanning test dies quietly.
    expect(found.length).toBeGreaterThan(5)
  })

  it.each(found)('$file: "$reason"', ({ reason }) => {
    expect(reason).not.toBe('')
    // A verb phrase, not a sentence: it continues someone else's clause.
    expect(reason[0]).toBe(reason[0]?.toLowerCase())
    expect(reason.endsWith('.')).toBe(false)
  })
})
