/**
 * Recognise a simple interactive SSH login from a submitted command line
 * (nocx-atyf.3, nocx-c5az, nocx-sxdd).
 *
 * "Go somewhere and open a shell" — `ssh host`, `ssh user@host`,
 * `ssh -p 2222 host` — is a transition. A remote command, redirection,
 * pipeline, or non-interactive ssh is not.
 *
 * The same conservative principle as isEnvironmentEntry: a missed offer is
 * invisible and cheap; a wrong offer is noise. Unknown = refusal. The
 * tokenizer does not understand full shell grammar, and that admission is a
 * refusal rather than a guess (nocx-mlm7 P4): anything the local shell would
 * expand ($VAR, backticks, unquoted backslash, history `!`, globs, braces) or
 * treat as grammar (| & ; < > ( ), comments) means the parser cannot say
 * confidently where the user went.
 *
 * Two surfaces ask this question — the environment label a submitted line
 * enters (environment-commands.ts) and the connection offer on a hand-typed
 * ssh block (terminal-content.ts) — so it is answered once, here.
 */

/** OpenSSH options that take an argument (ssh(1) OPTIONS). */
const VALUE_LETTERS: Record<string, true> = {
  b: true,
  c: true,
  D: true,
  E: true,
  e: true,
  F: true,
  I: true,
  i: true,
  J: true,
  L: true,
  l: true,
  m: true,
  O: true,
  o: true,
  p: true,
  Q: true,
  R: true,
  w: true,
  S: true,
  W: true,
}

/** OpenSSH options that are standalone flags. */
const FLAG_LETTERS: Record<string, true> = {
  '4': true,
  '6': true,
  A: true,
  a: true,
  C: true,
  f: true,
  G: true,
  g: true,
  K: true,
  k: true,
  M: true,
  N: true,
  n: true,
  q: true,
  s: true,
  T: true,
  t: true,
  V: true,
  v: true,
  X: true,
  x: true,
  Y: true,
  y: true,
}

/**
 * Returns true when `line` is a simple interactive SSH login — the user is
 * going somewhere and expects a shell, not running a remote command.
 */
export function isInteractiveTransition(line: string): boolean {
  return interactiveDestination(line) !== null
}

/**
 * Extract the destination from a simple `ssh` line. Returns the user@host or
 * host string that was typed, suitable as a consent key. Empty when the line
 * is not an interactive transition.
 */
export function extractDestination(line: string): string {
  return interactiveDestination(line) ?? ''
}

/**
 * The destination positional of an accepted line, or null when the line is
 * not a simple interactive login. The refusal cascade is the contract: every
 * `return null` below is a line whose destination this parser cannot state
 * without guessing.
 *
 * Option values are literal. A `$`, backtick, unquoted backslash, `!`, `*`,
 * `?` or `{` outside single quotes is shell expansion the renderer cannot
 * resolve, so the line is refused rather than classified. `~` is allowed: the
 * local shell expands it, so the spelling stays faithful.
 */
function interactiveDestination(line: string): string | null {
  const trimmed = line.trim()
  if (trimmed === '') return null
  if (trimmed.includes('\n')) {
    // A second line is a second command, not part of this one.
    return null
  }

  // Tokenize first so a non-ssh line reports that rather than whatever its
  // grammar contains. The tokenizer is tolerant; the scanner below is the
  // validator.
  const tokens = tokenize(trimmed)
  if (tokens[0] !== 'ssh') return null
  // The first token must be the literal word `ssh` at position 0 — a quoted
  // `"ssh"` is a different word to the shell.
  if (!/^ssh(?:\s|$)/.test(trimmed)) return null

  if (!grammarIsClean(trimmed)) return null

  const parsed = parseOptions(tokens)
  if (parsed === null) return null

  // The refusal cascade. -L/-R are refused only when they build a
  // non-interactive session (with -N or -f); the rest are refused outright.
  const { letters, positionals } = parsed
  if ((letters.has('L') || letters.has('R')) && (letters.has('N') || letters.has('f'))) return null
  if (letters.has('T')) return null // explicit no-PTY
  if (letters.has('N')) return null // no remote command, so no shell
  if (letters.has('f')) return null // detaches into the background
  if (letters.has('W')) return null // stdio forwarded to the peer
  if (letters.has('D')) return null // dynamic forward
  if (letters.has('O')) return null // control command against a running master
  if (letters.has('G') || letters.has('Q')) return null // config query: prints and exits

  if (positionals.length === 0 || positionals[0] === '') return null
  if (positionals.length > 1) return null // a remote command follows the host
  return positionals[0]
}

interface ParsedArgs {
  /** Every option letter seen, for the refusal cascade. */
  letters: Set<string>
  positionals: string[]
}

/** Walk the tokens after 'ssh' with getopt semantics: flags cluster, a
 *  value-taking letter consumes the rest of the token or the next token.
 *  Null when the line is one this parser refuses. */
function parseOptions(tokens: string[]): ParsedArgs | null {
  const positionals: string[] = []
  const letters = new Set<string>()

  for (let i = 1; i < tokens.length; i++) {
    const tok = tokens[i]
    if (tok === '--') return null
    if (tok === '-') return null
    if (tok.startsWith('-') && tok.length > 1) {
      let j = 1
      while (j < tok.length) {
        const letter = tok[j]
        if (FLAG_LETTERS[letter]) {
          letters.add(letter)
          j++
          continue
        }
        if (VALUE_LETTERS[letter]) {
          letters.add(letter)
          if (tok.slice(j + 1).length === 0) {
            if (i + 1 >= tokens.length) {
              // A value-taking option with no value left: refuse rather than
              // guess what ssh would have done.
              return null
            }
            i++ // the value is the next token
          }
          break
        }
        return null // unknown option
      }
      continue
    }
    positionals.push(tok)
  }

  return { letters, positionals }
}

/**
 * Scan the raw line, outside quotes, for shell grammar the tokenizer cannot
 * parse confidently. False when the line carries any. Single quotes make
 * bytes literal; double quotes still expand `$`, backticks, `\` and `!`, so
 * those refuse inside double quotes too.
 */
function grammarIsClean(line: string): boolean {
  let i = 0
  while (i < line.length) {
    const c = line[i]
    if (c === ' ' || c === '\t') {
      i++
      continue
    }
    if (c === "'" || c === '"') {
      const quote = c
      i++
      while (i < line.length && line[i] !== quote) {
        if (
          quote === '"' &&
          (line[i] === '$' || line[i] === '`' || line[i] === '\\' || line[i] === '!')
        ) {
          return false
        }
        i++
      }
      if (i >= line.length) return false // unterminated quote
      i++
      continue
    }
    if (c === '|' || c === '&' || c === ';' || c === '<' || c === '>' || c === '(' || c === ')') {
      return false
    }
    if (
      c === '$' ||
      c === '`' ||
      c === '!' ||
      c === '\\' ||
      c === '*' ||
      c === '?' ||
      c === '{' ||
      c === '}'
    ) {
      return false
    }
    if (c === '#') {
      // A word-start # is a comment: everything after it is not this command.
      return false
    }
    // Word: everything else is literal (including `~`, `[`, `]`, `%`, `=`,
    // `:` — an IPv6 destination like 'user@[fe80::1]%eth0' must survive).
    while (
      i < line.length &&
      !/\s/.test(line[i]) &&
      !'|&;<>()$`!\\*?{}#'.includes(line[i]) &&
      line[i] !== "'" &&
      line[i] !== '"'
    ) {
      i++
    }
  }
  return true
}

/** Split a validated line into unquoted tokens. Quotes are removed; single
 *  quotes keep their bytes literal. */
function tokenize(line: string): string[] {
  const tokens: string[] = []
  let i = 0
  while (i < line.length) {
    while (i < line.length && /\s/.test(line[i])) i++
    if (i >= line.length) break
    let tok = ''
    while (i < line.length) {
      const c = line[i]
      if (/\s/.test(c)) break
      if (c === "'" || c === '"') {
        const quote = c
        i++
        while (i < line.length && line[i] !== quote) {
          tok += line[i]
          i++
        }
        i++ // closing quote — the scanner guaranteed one
        continue
      }
      tok += c
      i++
    }
    tokens.push(tok)
  }
  return tokens
}
