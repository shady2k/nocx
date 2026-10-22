// The wrapper proofs execute a real shell against a fake ssh, so this file
// touches node builtins. @types/node is not installed (see
// theme-catalogue.test.ts, which does the same for the same reason), so every
// call through them is an untyped value and no-unsafe-* must be disabled at
// the file level rather than at ~60 call sites.

import { describe, it, expect } from 'vitest'
import { execFileSync } from 'node:child_process'
import { chmodSync, existsSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import {
  isInteractiveTransition,
  extractDestination,
  buildRewrite,
  planSsh,
  buildBootstrapRewrite,
  applyProfile,
  typedDestinationParts,
  type SshPlan,
  type SshRefusalReason,
} from './ssh-transition'
import { resolveSshProfileOverlay } from './quick-connect-assembly'
import type { SshProfileOverlay } from './quick-connect-assembly'
import type { SSHProfile } from './profiles'

describe('planSsh — refusal table, a case per reason and a paired acceptance', () => {
  /** Assert the line is refused with exactly this reason. */
  const refuses = (line: string, reason: SshRefusalReason, depth = 0) => {
    expect(planSsh(line, depth)).toEqual({ kind: 'refusal', typedLine: line.trim(), reason })
  }
  /** Assert the line is accepted and return the plan. */
  const accepts = (line: string, depth = 0): SshPlan => {
    const t = planSsh(line, depth)
    expect(t.kind).toBe('plan')
    return t as SshPlan
  }

  it('not-ssh: a non-ssh first word is refused; ssh is accepted', () => {
    refuses('ls -la', 'not-ssh')
    refuses('sudo ssh host', 'not-ssh')
    refuses('(ssh host)', 'not-ssh')
    refuses('   ', 'not-ssh')
    accepts('ssh host')
  })

  it('no-destination: ssh with no host is refused; with a host accepted', () => {
    refuses('ssh', 'no-destination')
    refuses('ssh -p 2222', 'no-destination')
    refuses('ssh ""', 'no-destination')
    accepts('ssh host')
  })

  it('remote-command: a second positional is refused; one positional accepted', () => {
    refuses('ssh host uptime', 'remote-command')
    refuses('ssh -t host tmux attach', 'remote-command')
    refuses('ssh -p 2222 host whoami', 'remote-command')
    refuses('ssh host echo "hi there"', 'remote-command')
    accepts('ssh -p 2222 host')
  })

  it('shell-operator: every operator refuses; the same byte inside quotes is literal', () => {
    refuses('ssh host | cat', 'shell-operator')
    refuses('ssh host > out', 'shell-operator')
    refuses('ssh host >> out', 'shell-operator')
    refuses('ssh host < in', 'shell-operator')
    refuses('ssh host && echo hi', 'shell-operator')
    refuses('ssh host || true', 'shell-operator')
    refuses('ssh host; ls', 'shell-operator')
    refuses('ssh host &', 'shell-operator')
    refuses('ssh host 2>&1', 'shell-operator')
    // Quoted, the operator is an option value, not grammar.
    accepts('ssh -o "ProxyCommand=cat > /dev/null" host')
    accepts("ssh -o 'ProxyCommand=nc -x proxy 22' host")
  })

  it('unparseable: expansion grammar and broken quoting refuse; the literal single-quoted twin is accepted', () => {
    refuses("ssh 'host", 'unparseable') // unterminated quote
    refuses('ssh -o "User=$USER" host', 'unparseable') // shell expansion we cannot resolve
    refuses('ssh -i ~/key\\ with\\ space host', 'unparseable') // unquoted backslash
    refuses('ssh host `uptime`', 'unparseable') // backticks
    refuses('ssh -i /tmp/*.key host', 'unparseable') // pathname expansion
    refuses('ssh -o Foo={a,b} host', 'unparseable') // brace expansion
    refuses('ssh host # comment', 'unparseable') // a word-start # would swallow the payload
    refuses('ssh host!', 'unparseable') // history expansion
    refuses('ssh\nhost', 'unparseable') // a second line cannot ride the wrapper
    refuses('"ssh" host', 'unparseable') // quoted first word: raw splice would corrupt the line
    // Single quotes make the same bytes literal and therefore faithful.
    accepts("ssh -o 'User=$USER' host")
    accepts("ssh -i '/tmp/*.key' host")
  })

  it('unknown-option: an option we do not know refuses; a known one is accepted', () => {
    refuses('ssh -Z host', 'unknown-option')
    refuses('ssh -1 host', 'unknown-option')
    refuses('ssh -h host', 'unknown-option')
    refuses('ssh -vZ host', 'unknown-option') // unknown inside a cluster
    accepts('ssh -v host')
  })

  it('double-dash: -- refuses; an ordinary option is accepted', () => {
    refuses('ssh -- host', 'double-dash')
    refuses('ssh host --', 'double-dash')
    accepts('ssh -p 22 host')
  })

  it('no-pty (-T): refuses, including inside a cluster; -t is accepted', () => {
    refuses('ssh -T host', 'no-pty')
    refuses('ssh -T -A host', 'no-pty')
    refuses('ssh -Tv host', 'no-pty')
    accepts('ssh -t host')
    accepts('ssh -tt host')
  })

  it('no-shell (-N): refuses; a plain login is accepted', () => {
    refuses('ssh -N host', 'no-shell')
    refuses('ssh -N -f host', 'no-shell')
    accepts('ssh host')
  })

  it('background (-f): refuses; a foreground login is accepted', () => {
    refuses('ssh -f host', 'background')
    accepts('ssh host')
  })

  it('stdio-forward (-W): refuses; a jump host is accepted', () => {
    refuses('ssh -W host:22 jump', 'stdio-forward')
    accepts('ssh -J jump host')
  })

  it('dynamic-forward (-D): refuses outright; an interactive local forward is accepted', () => {
    refuses('ssh -D 1080 host', 'dynamic-forward')
    refuses('ssh -D 127.0.0.1:1080 host', 'dynamic-forward')
    accepts('ssh -L 8080:localhost:80 host')
    accepts('ssh -R 9000:localhost:80 host')
  })

  it('non-interactive-forward (-L/-R with -N or -f): refuses; the same forward alone is accepted', () => {
    refuses('ssh -N -L 8080:localhost:80 host', 'non-interactive-forward')
    refuses('ssh -f -R 9000:localhost:80 host', 'non-interactive-forward')
    accepts('ssh -L 8080:localhost:80 host')
  })

  it('config-query (-G, -Q): a query that prints and exits refuses; a login is accepted', () => {
    refuses('ssh -G host', 'config-query')
    refuses('ssh -Q cipher host', 'config-query')
    accepts('ssh host')
  })

  it('control-command (-O): refuses; a login is accepted', () => {
    refuses('ssh -O check host', 'control-command')
    accepts('ssh host')
  })

  it('inside-environment: depth > 0 refuses raw; depth 0 accepts', () => {
    refuses('ssh host', 'inside-environment', 1)
    refuses('ssh -p 2222 host', 'inside-environment', 2)
    accepts('ssh host', 0)
  })
})

describe('planSsh — every accepted option reaches the plan exactly as typed (nocx-c5az)', () => {
  const P = (line: string) => planSsh(line, 0) as SshPlan

  it('carries -p -F -o -l -J with their values, and the destination', () => {
    const line = 'ssh -p 2222 -F ~/.ssh/other -o StrictHostKeyChecking=no -l pi -J jump host'
    expect(P(line)).toEqual({
      kind: 'plan',
      typedLine: line,
      destination: 'host',
      options: [
        { letter: 'p', token: '-p', value: '2222', valueToken: '2222' },
        { letter: 'F', token: '-F', value: '~/.ssh/other', valueToken: '~/.ssh/other' },
        {
          letter: 'o',
          token: '-o',
          value: 'StrictHostKeyChecking=no',
          valueToken: 'StrictHostKeyChecking=no',
        },
        { letter: 'l', token: '-l', value: 'pi', valueToken: 'pi' },
        { letter: 'J', token: '-J', value: 'jump', valueToken: 'jump' },
      ],
      // The complete argv the ssh -G oracle must see — P7 execs this.
      oracleArgv: [
        'ssh',
        '-G',
        '-p',
        '2222',
        '-F',
        '~/.ssh/other',
        '-o',
        'StrictHostKeyChecking=no',
        '-l',
        'pi',
        '-J',
        'jump',
        'host',
      ],
    })
  })

  it('carries attached values written without a space (-oKey=value)', () => {
    const line = 'ssh -p2222 -oStrictHostKeyChecking=no -luser -Ffile -Jjump host'
    const plan = P(line)
    expect(plan.options.map((o) => [o.letter, o.token, o.value, o.valueToken])).toEqual([
      ['p', '-p2222', '2222', null],
      ['o', '-oStrictHostKeyChecking=no', 'StrictHostKeyChecking=no', null],
      ['l', '-luser', 'user', null],
      ['F', '-Ffile', 'file', null],
      ['J', '-Jjump', 'jump', null],
    ])
    expect(plan.oracleArgv).toEqual([
      'ssh',
      '-G',
      '-p2222',
      '-oStrictHostKeyChecking=no',
      '-luser',
      '-Ffile',
      '-Jjump',
      'host',
    ])
  })

  it('carries a quoted option value with a space as one faithful argv element', () => {
    const line = 'ssh -o "StrictHostKeyChecking no" host'
    const plan = P(line)
    expect(plan.options[0]).toEqual({
      letter: 'o',
      token: '-o',
      value: 'StrictHostKeyChecking no',
      valueToken: 'StrictHostKeyChecking no',
    })
    expect(plan.oracleArgv).toEqual(['ssh', '-G', '-o', 'StrictHostKeyChecking no', 'host'])
  })

  it('preserves options typed after the destination', () => {
    const plan = P('ssh host -p 2222')
    expect(plan.destination).toBe('host')
    expect(plan.options.map((o) => o.letter)).toEqual(['p'])
    expect(plan.oracleArgv).toEqual(['ssh', '-G', '-p', '2222', 'host'])
  })

  it('preserves flag clusters and benign flags', () => {
    const plan = P('ssh -vvvA -i ~/.ssh/key user@host')
    expect(plan.options.map((o) => [o.letter, o.token, o.value])).toEqual([
      ['v', '-vvvA', null],
      ['i', '-i', '~/.ssh/key'],
    ])
    expect(plan.oracleArgv).toEqual(['ssh', '-G', '-vvvA', '-i', '~/.ssh/key', 'user@host'])
  })

  it('carries an interactive forward in typed order', () => {
    const plan = P('ssh -L 8080:localhost:80 -p 2222 host')
    expect(plan.options.map((o) => [o.letter, o.value])).toEqual([
      ['L', '8080:localhost:80'],
      ['p', '2222'],
    ])
    expect(plan.oracleArgv).toEqual(['ssh', '-G', '-L', '8080:localhost:80', '-p', '2222', 'host'])
  })

  it('preserves a metacharacter destination unquoted in the plan', () => {
    const plan = P("ssh 'user@[fe80::1]%eth0'")
    expect(plan.destination).toBe('user@[fe80::1]%eth0')
    expect(plan.oracleArgv).toEqual(['ssh', '-G', 'user@[fe80::1]%eth0'])
  })

  it('a value-taking option with no value left is unparseable, not a guess', () => {
    refusesUnparseable('ssh -p')
    refusesUnparseable('ssh -F')
    function refusesUnparseable(line: string) {
      expect(planSsh(line, 0)).toEqual({ kind: 'refusal', typedLine: line, reason: 'unparseable' })
    }
  })
})

describe('isInteractiveTransition / extractDestination (legacy seam, unchanged contract)', () => {
  it('a simple ssh to a host is interactive', () => {
    expect(isInteractiveTransition('ssh host')).toBe(true)
    expect(isInteractiveTransition('ssh user@host')).toBe(true)
    expect(isInteractiveTransition('ssh -p 2222 host')).toBe(true)
  })

  it('a remote command is NOT interactive', () => {
    expect(isInteractiveTransition('ssh host uptime')).toBe(false)
  })

  it('a pipeline, redirection or operator is NOT interactive', () => {
    expect(isInteractiveTransition('ssh host | cat')).toBe(false)
    expect(isInteractiveTransition('ssh host > out')).toBe(false)
  })

  it('non-ssh commands are not transitions', () => {
    expect(isInteractiveTransition('ls -la')).toBe(false)
    expect(isInteractiveTransition('')).toBe(false)
  })

  it('ssh with flags only (no host) is not a transition', () => {
    expect(isInteractiveTransition('ssh -p 2222')).toBe(false)
  })

  it('non-interactive ssh forms are not transitions', () => {
    expect(isInteractiveTransition('ssh -G host')).toBe(false)
    expect(isInteractiveTransition('ssh -N host')).toBe(false)
    expect(isInteractiveTransition('ssh -T host')).toBe(false)
  })

  it('extracts the destination from a simple ssh command', () => {
    expect(extractDestination('ssh pi@192.0.2.93')).toBe('pi@192.0.2.93')
    expect(extractDestination('ssh -p 2222 root@box')).toBe('root@box')
    expect(extractDestination('ssh -i ~/.ssh/id_ed25519 -o StrictHostKeyChecking=no bob@h')).toBe(
      'bob@h',
    )
    expect(extractDestination('ssh host uptime')).toBe('')
  })
})

describe('buildBootstrapRewrite (nocx-pu4.6 + nocx-sxdd consume-once)', () => {
  // What the backend returns is a shell-quoted PATH, not the launcher. The
  // launcher is ~35 KB and a typed line has only the tty, whose canonical
  // buffer is 4096 bytes: the first attempt sent the payload inline and the
  // shell executed the fragments of a truncated script.
  const P = "'/home/u/.nocx/run/launcher-12345'"

  /** The shape every bootstrap rewrite has: read the staged file if it is
   *  there, run the integrated ssh, consume the file so a history rerun takes
   *  the else branch, and otherwise run exactly what the user typed. The
   *  removal lives inside the substitution: the payload is read first, the
   *  file is consumed before ssh spawns, and the branch status stays ssh's. */
  const guarded = (sshCmd: string, original: string) =>
    `if [ -s ${P} ]; then ${sshCmd} "$(cat ${P}; rm -f ${P})"; else ${original}; fi`

  it('reads the staged launcher and forces a pty', () => {
    expect(buildRewrite('ssh pi@host-2', P)).toBe(guarded('ssh -t pi@host-2', 'ssh pi@host-2'))
    expect(buildRewrite('ssh myserver', P)).toBe(guarded('ssh -t myserver', 'ssh myserver'))
  })

  it('preserves existing flags', () => {
    expect(buildRewrite('ssh -p 2222 host', P)).toBe(
      guarded('ssh -t -p 2222 host', 'ssh -p 2222 host'),
    )
    expect(buildRewrite('ssh -i ~/.ssh/key user@host', P)).toBe(
      guarded('ssh -t -i ~/.ssh/key user@host', 'ssh -i ~/.ssh/key user@host'),
    )
    expect(buildRewrite('ssh -A -X host', P)).toBe(guarded('ssh -t -A -X host', 'ssh -A -X host'))
  })

  it('does not double -t when already present', () => {
    expect(buildRewrite('ssh -t host', P)).toBe(guarded('ssh -t host', 'ssh -t host'))
    expect(buildRewrite('ssh -tt host', P)).toBe(guarded('ssh -tt host', 'ssh -tt host'))
  })

  it('refuses -T (explicit no-PTY)', () => {
    expect(buildRewrite('ssh -T host', P)).toBeNull()
    expect(buildRewrite('ssh -T -A host', P)).toBeNull()
  })

  it('refuses every non-interactive form', () => {
    expect(buildRewrite('ssh -N host', P)).toBeNull()
    expect(buildRewrite('ssh -f host', P)).toBeNull()
    expect(buildRewrite('ssh -W host:22 jump', P)).toBeNull()
    expect(buildRewrite('ssh -D 1080 host', P)).toBeNull()
    expect(buildRewrite('ssh -N -L 8080:localhost:80 host', P)).toBeNull()
    expect(buildRewrite('ssh host uptime', P)).toBeNull()
    expect(buildRewrite('ssh -- host', P)).toBeNull()
    expect(buildRewrite('ssh -Z host', P)).toBeNull()
  })

  it('refuses inside a remote environment (depth > 0)', () => {
    expect(buildRewrite('ssh host', P, 1)).toBeNull()
    expect(buildRewrite('ssh host', P, 0)).not.toBeNull()
  })

  it('preserves quoting around flags and values', () => {
    expect(buildRewrite('ssh -o "StrictHostKeyChecking no" host', P)).toBe(
      guarded(
        'ssh -t -o "StrictHostKeyChecking no" host',
        'ssh -o "StrictHostKeyChecking no" host',
      ),
    )
  })

  it('refuses non-ssh lines', () => {
    expect(buildRewrite('ls -la', P)).toBeNull()
    expect(buildRewrite('', P)).toBeNull()
  })

  // The `else` branch is the whole fail-open (ADR-0004 §1). A staged file
  // that is missing, empty or unreadable must run the user's own line — never
  // `ssh host ""`, which asks sshd for an empty remote command instead of a
  // shell, and never a second ssh chained off the first one's exit status.
  it('falls back to the line the user typed, byte for byte', () => {
    const out = buildRewrite('  ssh -p 2222 pi@host-2  ', P)!
    expect(out).toContain('; else ssh -p 2222 pi@host-2; fi')
    expect(out).not.toContain('||')
    expect(out).not.toContain('&&')
  })

  it('never sends an empty remote command', () => {
    const out = buildRewrite('ssh host', P)!
    expect(out).not.toContain('""')
    // The guard is on the file, before anything is handed to ssh.
    expect(out.indexOf('[ -s ')).toBeLessThan(out.indexOf('ssh -t'))
  })

  it('consumes the staged file inside the command substitution (nocx-sxdd)', () => {
    const out = buildRewrite('ssh host', P)!
    expect(out).toContain(`rm -f ${P})"; else`)
    // The removal sits inside "$(...)", so it runs before ssh is spawned and
    // its own status can never mask ssh's: a rerun of this exact line cannot
    // bootstrap, and `$?` stays ssh's 255 / 130 / remote code.
    expect(out.indexOf(`rm -f ${P}`)).toBeGreaterThan(out.indexOf('ssh -t host'))
    expect(out.indexOf(`rm -f ${P}`)).toBeLessThan(out.indexOf('; else'))
  })

  // The measured ceiling. A Linux canonical line buffer is 4096 bytes
  // (N_TTY_BUF_SIZE) and 4095 was the largest that survived intact on a real
  // pty, so 4095 is the number, not 4096. The exact byte count is asserted
  // too: this line is the one thing between the user's Enter and the tty, and
  // a change that grows it belongs in a review, not in a screenshot of a
  // truncated script. 177 = 145, the pre-privacy-scrub length, plus the
  // 42 bytes of `; rm -f P`, minus the 10 the scrub saved: the fixture's
  // hostname stand-in is 5 bytes shorter than what it replaced, and the
  // rewritten line carries the user's command twice.
  it('produces a line the tty can carry', () => {
    const rewritten = buildRewrite('ssh pi@host-2', P)!
    const bytes = new TextEncoder().encode(rewritten).byteLength
    expect(bytes).toBe(177)
    expect(bytes).toBeLessThanOrEqual(4095)
  })

  // The ceiling is enforced, not just asserted. Two copies of the path go
  // into the line and a path may be PATH_MAX (4096) on its own, so "short
  // because it is only a path" is an assumption, not a property. When the
  // line would not survive the tty, there is no rewrite to make: refuse, and
  // the user's own line goes to the pty.
  it('refuses a rewrite the tty could not carry', () => {
    const huge = `'/home/${'d'.repeat(3000)}/.nocx/run/launcher-1'`
    expect(buildRewrite('ssh host', huge)).toBeNull()
  })

  it('accepts a realistically long path', () => {
    const long = `'/Users/a-fairly-long-user-name/Library/Application Support/nocx/.nocx/run/launcher-987654'`
    const out = buildRewrite('ssh some-quite-long-hostname.internal.example.com', long)!
    expect(new TextEncoder().encode(out).byteLength).toBeLessThanOrEqual(4095)
  })

  // The launcher itself must never appear in the line — that is the defect
  // this bead was reopened for. Only its path does.
  it('carries a path, never a payload', () => {
    const rewritten = buildRewrite('ssh host', P)!
    expect(rewritten).toContain(P)
    expect(rewritten).not.toContain('BASH_ENV')
    expect(rewritten).not.toContain('printf')
  })
})

describe('typedDestinationParts — the spelled destination the profile match keys on', () => {
  const parts = (line: string) => typedDestinationParts(planSsh(line, 0) as SshPlan)

  it('splits user@host and keeps a bare host whole', () => {
    expect(parts('ssh root@prod')).toEqual({ host: 'prod', user: 'root' })
    expect(parts('ssh prod')).toEqual({ host: 'prod' })
  })

  it('a typed -l is the operative user — OpenSSH consumes options before the positional (measured: ssh -G -l alice root@host resolves user alice)', () => {
    expect(parts('ssh -l bob prod')).toEqual({ host: 'prod', user: 'bob' })
    expect(parts('ssh -l bob root@prod')).toEqual({ host: 'prod', user: 'bob' })
  })

  it('an IPv6 destination with a zone survives whole', () => {
    expect(parts('ssh user@[fe80::1]%eth0')).toEqual({ host: '[fe80::1]%eth0', user: 'user' })
  })
})

describe("applyProfile — the saved profile's settings reach the plan, typed always wins", () => {
  const overlay = (over: Partial<SshProfileOverlay> = {}): SshProfileOverlay => ({
    profileId: 'p1',
    identity: 'root@prod:2222',
    user: 'root',
    port: 2222,
    keyPath: '~/.ssh/prod',
    jumpHost: 'root@bastion:2200',
    ...over,
  })
  const plan = (line: string) => planSsh(line, 0) as SshPlan

  it('adds -l -p -i -J in that fixed order when the line spells none of them', () => {
    expect(applyProfile(plan('ssh prod'), overlay()).profileFlags).toEqual([
      '-l',
      'root',
      '-p',
      '2222',
      '-i',
      '~/.ssh/prod',
      '-J',
      'root@bastion:2200',
    ])
  })

  it('a user in the destination suppresses the profile user', () => {
    expect(applyProfile(plan('ssh root@prod'), overlay()).profileFlags).toEqual([
      '-p',
      '2222',
      '-i',
      '~/.ssh/prod',
      '-J',
      'root@bastion:2200',
    ])
  })

  it('a typed -l suppresses the profile user', () => {
    expect(applyProfile(plan('ssh -l bob prod'), overlay()).profileFlags).not.toContain('-l')
  })

  it('a typed -p beats the profile port', () => {
    const out = applyProfile(plan('ssh -p 2223 prod'), overlay())
    expect(out.profileFlags).toEqual(['-l', 'root', '-i', '~/.ssh/prod', '-J', 'root@bastion:2200'])
    expect(out.profileFlags).not.toContain('2222')
  })

  it('a typed -i beats the profile key', () => {
    expect(applyProfile(plan('ssh -i /keys/other prod'), overlay()).profileFlags).not.toContain(
      '-i',
    )
  })

  it('a typed -J beats the profile jump', () => {
    expect(applyProfile(plan('ssh -J other prod'), overlay()).profileFlags).not.toContain('-J')
  })

  it('an overlay with nothing to contribute leaves the plan untouched', () => {
    const p = plan('ssh prod')
    expect(applyProfile(p, { profileId: 'p1', identity: 'prod:22' })).toBe(p)
    expect(p.profileFlags).toBeUndefined()
  })

  it('never touches oracleArgv, typedLine or the destination — the oracle sees only the typed argv', () => {
    const p = plan('ssh -p 2223 root@prod')
    const out = applyProfile(p, overlay())
    expect(out.oracleArgv).toEqual(['ssh', '-G', '-p', '2223', 'root@prod'])
    expect(out.typedLine).toBe('ssh -p 2223 root@prod')
    expect(out.destination).toBe('root@prod')
  })
})

describe('the generated wrapper, executed (real shell + fake ssh that records argv)', () => {
  const proc = process as unknown as { env: Record<string, string | undefined> }

  interface HarnessOpts {
    home?: string
    exit?: number
    exec?: boolean
    execLog?: string
    shell?: string
  }

  function fakeSshHarness(): {
    dir: string
    argvFile: string
    home: string
    run: (wrapper: string, opts?: HarnessOpts) => string[]
    status: (wrapper: string, opts?: HarnessOpts) => number
    cleanup: () => void
  } {
    const dir = mkdtempSync(join(tmpdir(), 'nocx-ssh-'))
    const argvFile = join(dir, 'argv.txt')
    const home = join(dir, 'home')
    mkdirSync(join(home, '.nocx'), { recursive: true })
    // A fake ssh that records its exact argv, one element per line, then
    // exits with NOCX_FAKE_EXIT (default 0) — or, with NOCX_FAKE_EXEC set,
    // plays sshd: it runs the remote command (the last argv element) in a
    // shell so the guard's then/else branches are really executed.
    writeFileSync(
      join(dir, 'ssh'),
      '#!/bin/sh\n' +
        'printf \'%s\\n\' "$(basename "$0")" "$@" > "$NOCX_FAKE_ARGV"\n' +
        'if [ -n "$NOCX_FAKE_EXEC" ]; then\n' +
        '  last=\n' +
        '  for a in "$@"; do last=$a; done\n' +
        '  exec /bin/sh -c "$last"\n' +
        'fi\n' +
        'exit "${NOCX_FAKE_EXIT:-0}"\n',
    )
    chmodSync(join(dir, 'ssh'), 0o755)
    const env = { ...proc.env, PATH: `${dir}:${proc.env.PATH ?? ''}` }
    const harnessEnv = (opts?: HarnessOpts) => {
      const e: Record<string, string | undefined> = {
        ...env,
        NOCX_FAKE_ARGV: argvFile,
        HOME: opts?.home ?? home,
      }
      if (opts?.exit !== undefined) e.NOCX_FAKE_EXIT = String(opts.exit)
      if (opts?.exec) e.NOCX_FAKE_EXEC = '1'
      if (opts?.execLog) e.NOCX_FAKE_EXEC_LOG = opts.execLog
      if (opts?.shell) e.SHELL = opts.shell
      return e
    }
    const run = (wrapper: string, opts?: HarnessOpts) => {
      execFileSync('bash', ['-c', wrapper], { env: harnessEnv(opts), cwd: dir, encoding: 'utf8' })
      return readArgv(argvFile)
    }
    const status = (wrapper: string, opts?: HarnessOpts): number => {
      try {
        execFileSync('bash', ['-c', wrapper], { env: harnessEnv(opts), cwd: dir, encoding: 'utf8' })
        return 0
      } catch (e) {
        return (e as { status?: number }).status ?? -1
      }
    }
    return {
      dir,
      argvFile,
      home,
      run,
      status,
      cleanup: () => rmSync(dir, { recursive: true, force: true }),
    }
  }

  function readArgv(file: string): string[] {
    const content = execFileSync('cat', [file], { encoding: 'utf8' })
    return content === '' ? [] : content.replace(/\n$/, '').split('\n')
  }

  it('sends the exact argv through a destination full of metacharacters, then consumes the payload', () => {
    const h = fakeSshHarness()
    try {
      const line = 'ssh -o "ProxyCommand=nc -x proxy 22" -p 2222 \'user@[fe80::1]%eth0\''
      const plan = planSsh(line, 0) as SshPlan
      const launcherFile = join(h.dir, 'launcher-1')
      writeFileSync(launcherFile, 'LAUNCHER_PAYLOAD') // no trailing newline
      const wrapper = buildBootstrapRewrite(plan, `'${launcherFile}'`)!

      // First execution: integrated ssh, options unquoted back into argv the
      // way the shell would have, the payload as one trailing element.
      expect(h.run(wrapper)).toEqual([
        'ssh',
        '-t',
        '-o',
        'ProxyCommand=nc -x proxy 22',
        '-p',
        '2222',
        'user@[fe80::1]%eth0',
        'LAUNCHER_PAYLOAD',
      ])
      // The payload was consumed by the run.
      expect(existsSync(launcherFile)).toBe(false)

      // A rerun of the identical line (Ctrl-R from shell history) takes the
      // else branch: the plain original argv, no payload.
      expect(h.run(wrapper)).toEqual([
        'ssh',
        '-o',
        'ProxyCommand=nc -x proxy 22',
        '-p',
        '2222',
        'user@[fe80::1]%eth0',
      ])
    } finally {
      h.cleanup()
    }
  })

  it('the wrapper status is the fake ssh status — 255, 130, 0 — and the payload is consumed once (nocx-nl6q)', () => {
    const h = fakeSshHarness()
    try {
      const plan = planSsh('ssh host', 0) as SshPlan
      const launcherFile = join(h.dir, 'launcher-1')
      const wrapper = buildBootstrapRewrite(plan, `'${launcherFile}'`)!
      const payload = 'LAUNCHER_PAYLOAD'

      // rm -f lives inside "$(cat P; rm -f P)", so its own status can never
      // mask ssh's: a dropped connection delivers 255, Ctrl-C delivers 130, a
      // clean exit delivers 0 — and every run consumes the staged file.
      for (const code of [255, 130, 0]) {
        writeFileSync(launcherFile, payload)
        expect(h.status(wrapper, { exit: code })).toBe(code)
        expect(existsSync(launcherFile)).toBe(false)
      }

      // The identical rerun (Ctrl-R from shell history, file gone) takes the
      // else branch: the plain original argv, no payload, no -t.
      expect(h.run(wrapper)).toEqual(['ssh', 'host'])
    } finally {
      h.cleanup()
    }
  })

  it("a saved profile's settings reach the rewritten argv — -i -p -J, with the fail-open else branch untouched", () => {
    const h = fakeSshHarness()
    try {
      const profiles: SSHProfile[] = [
        {
          id: 'p1',
          type: 'ssh',
          name: 'Prod',
          options: {
            host: 'prod',
            user: 'root',
            port: 2222,
            keyPath: '~/.ssh/prod',
            jumpHost: 'bastion',
          },
        },
        {
          id: 'j1',
          type: 'ssh',
          name: 'bastion',
          options: { host: 'bastion.example.com', user: 'root', port: 2200 },
        },
      ]
      const plan = planSsh('ssh root@prod', 0) as SshPlan
      const overlay = resolveSshProfileOverlay(profiles, typedDestinationParts(plan))
      expect(overlay).not.toBeNull()
      const enriched = applyProfile(plan, overlay!)
      const launcherFile = join(h.dir, 'launcher-1')
      writeFileSync(launcherFile, 'LAUNCHER_PAYLOAD')
      const wrapper = buildBootstrapRewrite(enriched, `'${launcherFile}'`)!

      // First execution: the profile's port, key and jump (resolved through
      // the jump profile it names) ride in the rewritten ssh argv.
      expect(h.run(wrapper)).toEqual([
        'ssh',
        '-t',
        '-p',
        '2222',
        '-i',
        '~/.ssh/prod',
        '-J',
        'root@bastion.example.com:2200',
        'root@prod',
        'LAUNCHER_PAYLOAD',
      ])

      // The fail-open else branch stays the exact typed line (ADR-0004): a
      // recalled line runs byte-for-byte what the user typed.
      expect(h.run(wrapper)).toEqual(['ssh', 'root@prod'])
    } finally {
      h.cleanup()
    }
  })

  it('a typed flag beats the profile — -p 2223 stays, the profile port never appears', () => {
    const h = fakeSshHarness()
    try {
      const profiles: SSHProfile[] = [
        {
          id: 'p1',
          type: 'ssh',
          name: 'Prod',
          options: { host: 'prod', port: 2222, keyPath: '~/.ssh/prod' },
        },
      ]
      const plan = planSsh('ssh -p 2223 root@prod', 0) as SshPlan
      const overlay = resolveSshProfileOverlay(profiles, typedDestinationParts(plan))
      expect(overlay).not.toBeNull()
      const enriched = applyProfile(plan, overlay!)
      const launcherFile = join(h.dir, 'launcher-1')
      writeFileSync(launcherFile, 'LAUNCHER_PAYLOAD')
      const wrapper = buildBootstrapRewrite(enriched, `'${launcherFile}'`)!
      const argv = h.run(wrapper)
      expect(argv).toEqual([
        'ssh',
        '-t',
        '-i',
        '~/.ssh/prod',
        '-p',
        '2223',
        'root@prod',
        'LAUNCHER_PAYLOAD',
      ])
      expect(argv).not.toContain('2222')
    } finally {
      h.cleanup()
    }
  })

  it('no matching profile — the argv is exactly what the user typed, and the plan carries no profile flags', () => {
    const h = fakeSshHarness()
    try {
      const plan = planSsh('ssh root@prod', 0) as SshPlan
      const overlay = resolveSshProfileOverlay([], typedDestinationParts(plan))
      expect(overlay).toBeNull()
      expect(plan.profileFlags).toBeUndefined()
      const launcherFile = join(h.dir, 'launcher-1')
      writeFileSync(launcherFile, 'LAUNCHER_PAYLOAD')
      const wrapper = buildBootstrapRewrite(plan, `'${launcherFile}'`)!
      expect(h.run(wrapper)).toEqual(['ssh', '-t', 'root@prod', 'LAUNCHER_PAYLOAD'])
    } finally {
      h.cleanup()
    }
  })

  it('a password-auth profile contributes no key and no password material — OpenSSH takes no password non-interactively', () => {
    const h = fakeSshHarness()
    try {
      const profiles: SSHProfile[] = [
        {
          id: 'p1',
          type: 'ssh',
          name: 'Prod',
          options: { host: 'prod', user: 'root', port: 2222, auth: 'password' },
        },
      ]
      const plan = planSsh('ssh root@prod', 0) as SshPlan
      const overlay = resolveSshProfileOverlay(profiles, typedDestinationParts(plan))
      const enriched = applyProfile(plan, overlay!)
      const launcherFile = join(h.dir, 'launcher-1')
      writeFileSync(launcherFile, 'LAUNCHER_PAYLOAD')
      const wrapper = buildBootstrapRewrite(enriched, `'${launcherFile}'`)!
      expect(h.run(wrapper)).toEqual(['ssh', '-t', '-p', '2222', 'root@prod', 'LAUNCHER_PAYLOAD'])
    } finally {
      h.cleanup()
    }
  })

  it('a key path containing a space survives the wrapper as one argv element', () => {
    const h = fakeSshHarness()
    try {
      const profiles: SSHProfile[] = [
        {
          id: 'p1',
          type: 'ssh',
          name: 'Prod',
          options: { host: 'prod', keyPath: '/Users/dev/My Keys/prod' },
        },
      ]
      const plan = planSsh('ssh prod', 0) as SshPlan
      const overlay = resolveSshProfileOverlay(profiles, typedDestinationParts(plan))
      const enriched = applyProfile(plan, overlay!)
      const launcherFile = join(h.dir, 'launcher-1')
      writeFileSync(launcherFile, 'LAUNCHER_PAYLOAD')
      const wrapper = buildBootstrapRewrite(enriched, `'${launcherFile}'`)!
      expect(h.run(wrapper)).toEqual([
        'ssh',
        '-t',
        '-i',
        '/Users/dev/My Keys/prod',
        'prod',
        'LAUNCHER_PAYLOAD',
      ])
    } finally {
      h.cleanup()
    }
  })
})
