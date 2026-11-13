import { describe, it, expect } from 'vitest'
import { isInteractiveTransition, extractDestination } from './ssh-transition'

describe('isInteractiveTransition — a case per refusal, each paired with an acceptance', () => {
  /** Every line here is one this parser cannot classify confidently, so the
   *  renderer labels no environment and offers no connection: a missed offer
   *  is invisible and cheap, a wrong one is noise on every command. */
  const refuses = (line: string) => expect(isInteractiveTransition(line)).toBe(false)
  /** Each group is paired with a line that MUST be accepted, so a parser
   *  that refuses everything fails this table rather than passing it. */
  const accepts = (line: string) => expect(isInteractiveTransition(line)).toBe(true)

  it('not-ssh: a non-ssh first word is refused; ssh is accepted', () => {
    refuses('ls -la')
    refuses('sudo ssh host')
    refuses('(ssh host)')
    refuses('   ')
    accepts('ssh host')
  })

  it('no-destination: ssh with no host is refused; with a host accepted', () => {
    refuses('ssh')
    refuses('ssh -p 2222')
    refuses('ssh ""')
    accepts('ssh host')
  })

  it('remote-command: a second positional is refused; one positional accepted', () => {
    refuses('ssh host uptime')
    refuses('ssh -t host tmux attach')
    refuses('ssh -p 2222 host whoami')
    refuses('ssh host echo "hi there"')
    accepts('ssh -p 2222 host')
  })

  it('shell-operator: every operator refuses; the same byte inside quotes is literal', () => {
    refuses('ssh host | cat')
    refuses('ssh host > out')
    refuses('ssh host >> out')
    refuses('ssh host < in')
    refuses('ssh host && echo hi')
    refuses('ssh host || true')
    refuses('ssh host; ls')
    refuses('ssh host &')
    refuses('ssh host 2>&1')
    // Quoted, the operator is an option value, not grammar.
    accepts('ssh -o "ProxyCommand=cat > /dev/null" host')
    accepts("ssh -o 'ProxyCommand=nc -x proxy 22' host")
  })

  it('unparseable: expansion grammar and broken quoting refuse; the literal single-quoted twin is accepted', () => {
    refuses("ssh 'host") // unterminated quote
    refuses('ssh -o "User=$USER" host') // shell expansion we cannot resolve
    refuses('ssh -i ~/key\\ with\\ space host') // unquoted backslash
    refuses('ssh host `uptime`') // backticks
    refuses('ssh -i /tmp/*.key host') // pathname expansion
    refuses('ssh -o Foo={a,b} host') // brace expansion
    refuses('ssh host # comment') // a word-start # makes the rest another statement
    refuses('ssh host!') // history expansion
    refuses('ssh\nhost') // a second line is a second command
    refuses('"ssh" host') // a quoted first word is a different word to the shell
    refuses('ssh -p') // a value-taking option with no value left: not a guess
    refuses('ssh -F')
    // Single quotes make the same bytes literal and therefore faithful.
    accepts("ssh -o 'User=$USER' host")
    accepts("ssh -i '/tmp/*.key' host")
  })

  it('unknown-option: an option we do not know refuses; a known one is accepted', () => {
    refuses('ssh -Z host')
    refuses('ssh -1 host')
    refuses('ssh -h host')
    refuses('ssh -vZ host') // unknown inside a cluster
    accepts('ssh -v host')
  })

  it('double-dash: -- refuses; an ordinary option is accepted', () => {
    refuses('ssh -- host')
    refuses('ssh host --')
    accepts('ssh -p 22 host')
  })

  it('no-pty (-T): refuses, including inside a cluster; -t is accepted', () => {
    refuses('ssh -T host')
    refuses('ssh -T -A host')
    refuses('ssh -Tv host')
    accepts('ssh -t host')
    accepts('ssh -tt host')
  })

  it('no-shell (-N): refuses; a plain login is accepted', () => {
    refuses('ssh -N host')
    refuses('ssh -N -f host')
    accepts('ssh host')
  })

  it('background (-f): refuses; a foreground login is accepted', () => {
    refuses('ssh -f host')
    accepts('ssh host')
  })

  it('stdio-forward (-W): refuses; a jump host is accepted', () => {
    refuses('ssh -W host:22 jump')
    accepts('ssh -J jump host')
  })

  it('dynamic-forward (-D): refuses outright; an interactive local forward is accepted', () => {
    refuses('ssh -D 1080 host')
    refuses('ssh -D 127.0.0.1:1080 host')
    accepts('ssh -L 8080:localhost:80 host')
    accepts('ssh -R 9000:localhost:80 host')
  })

  it('non-interactive-forward (-L/-R with -N or -f): refuses; the same forward alone is accepted', () => {
    refuses('ssh -N -L 8080:localhost:80 host')
    refuses('ssh -f -R 9000:localhost:80 host')
    accepts('ssh -L 8080:localhost:80 host')
  })

  it('config-query (-G, -Q): a query that prints and exits refuses; a login is accepted', () => {
    refuses('ssh -G host')
    refuses('ssh -Q cipher host')
    accepts('ssh host')
  })

  it('control-command (-O): refuses; a login is accepted', () => {
    refuses('ssh -O check host')
    accepts('ssh host')
  })
})

describe('extractDestination — the consent key the connection offer carries', () => {
  it('extracts the destination from a simple ssh command', () => {
    expect(extractDestination('ssh pi@192.0.2.93')).toBe('pi@192.0.2.93')
    expect(extractDestination('ssh -p 2222 root@box')).toBe('root@box')
    expect(extractDestination('ssh -i ~/.ssh/id_ed25519 -o StrictHostKeyChecking=no bob@h')).toBe(
      'bob@h',
    )
    expect(extractDestination('  ssh -p 2222 pi@host-2  ')).toBe('pi@host-2')
  })

  it('answers nothing for a line that is not a transition', () => {
    expect(extractDestination('ssh host uptime')).toBe('')
    expect(extractDestination('ssh -T host')).toBe('')
    expect(extractDestination('ls -la')).toBe('')
    expect(extractDestination('')).toBe('')
  })

  it('carries attached, clustered and quoted option values without touching the destination', () => {
    expect(extractDestination('ssh -p2222 -luser host')).toBe('host')
    expect(extractDestination('ssh -vvvA -i ~/.ssh/key user@host')).toBe('user@host')
    expect(extractDestination('ssh -o "StrictHostKeyChecking no" host')).toBe('host')
    expect(extractDestination('ssh host -p 2222')).toBe('host')
  })

  it('keeps a metacharacter destination whole', () => {
    expect(extractDestination("ssh 'user@[fe80::1]%eth0'")).toBe('user@[fe80::1]%eth0')
  })
})
