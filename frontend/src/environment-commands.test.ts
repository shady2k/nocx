import { describe, it, expect } from 'vitest'
import { environmentEntry } from './environment-commands'

describe('environmentEntry recognises an environment change', () => {
  // ── Commands that enter a new environment ──────────────────────────

  it('recognises ssh', () => {
    expect(environmentEntry('ssh user@host') !== null).toBe(true)
    expect(environmentEntry('ssh -i key.pem user@host') !== null).toBe(true)
    expect(environmentEntry('ssh pi@192.0.2.93') !== null).toBe(true)
    expect(environmentEntry('  ssh user@host  ') !== null).toBe(true)
  })

  it('recognises docker exec', () => {
    expect(environmentEntry('docker exec -it container bash') !== null).toBe(true)
    expect(environmentEntry('docker exec container ls') !== null).toBe(true)
  })

  it('recognises podman exec', () => {
    expect(environmentEntry('podman exec -it container bash') !== null).toBe(true)
    expect(environmentEntry('podman exec container ls') !== null).toBe(true)
  })

  it('recognises kubectl exec', () => {
    expect(environmentEntry('kubectl exec -it pod -- bash') !== null).toBe(true)
    expect(environmentEntry('kubectl exec pod -- ls') !== null).toBe(true)
  })

  it('recognises su', () => {
    expect(environmentEntry('su') !== null).toBe(true)
    expect(environmentEntry('su -') !== null).toBe(true)
    expect(environmentEntry('su root') !== null).toBe(true)
  })

  it('recognises sudo -i and sudo -s', () => {
    expect(environmentEntry('sudo -i') !== null).toBe(true)
    expect(environmentEntry('sudo -s') !== null).toBe(true)
    expect(environmentEntry('sudo -i -u root') !== null).toBe(true)
  })

  it('recognises nix-shell', () => {
    expect(environmentEntry('nix-shell') !== null).toBe(true)
    expect(environmentEntry('nix-shell -p hello') !== null).toBe(true)
  })

  it('recognises tmux', () => {
    expect(environmentEntry('tmux') !== null).toBe(true)
    expect(environmentEntry('tmux new -s session') !== null).toBe(true)
    expect(environmentEntry('tmux attach') !== null).toBe(true)
  })

  it('recognises screen', () => {
    expect(environmentEntry('screen') !== null).toBe(true)
    expect(environmentEntry('screen -S session') !== null).toBe(true)
  })

  // ── Default: not an environment change ─────────────────────────────

  it('the default for an unknown command is false', () => {
    expect(environmentEntry('ls -la') !== null).toBe(false)
    expect(environmentEntry('echo hello') !== null).toBe(false)
    expect(environmentEntry('sleep 5') !== null).toBe(false)
    expect(environmentEntry('cat /etc/hosts') !== null).toBe(false)
    expect(environmentEntry('git status') !== null).toBe(false)
    expect(environmentEntry('make build') !== null).toBe(false)
    expect(environmentEntry('python') !== null).toBe(false)
    expect(environmentEntry('node') !== null).toBe(false)
    expect(environmentEntry('vim file.txt') !== null).toBe(false)
    expect(environmentEntry('') !== null).toBe(false)
  })

  // ── Partial matches: the command is NOT an environment entry ───────

  it('docker without exec is not an environment entry', () => {
    expect(environmentEntry('docker ps') !== null).toBe(false)
    expect(environmentEntry('docker build .') !== null).toBe(false)
    expect(environmentEntry('docker run -it ubuntu bash') !== null).toBe(false)
  })

  it('podman without exec is not an environment entry', () => {
    expect(environmentEntry('podman ps') !== null).toBe(false)
    expect(environmentEntry('podman build .') !== null).toBe(false)
  })

  it('kubectl without exec is not an environment entry', () => {
    expect(environmentEntry('kubectl get pods') !== null).toBe(false)
    expect(environmentEntry('kubectl logs pod') !== null).toBe(false)
  })

  it('sudo without -i or -s is not an environment entry', () => {
    expect(environmentEntry('sudo ls') !== null).toBe(false)
    expect(environmentEntry('sudo make install') !== null).toBe(false)
  })
})

describe('environmentEntry — the destination we can name because we sent the line', () => {
  const cases: Array<[string, string | null]> = [
    ['ssh pi@192.0.2.93', 'pi@192.0.2.93'],
    ['ssh -p 2222 root@box', 'root@box'],
    ['ssh -i ~/.ssh/id_ed25519 -o StrictHostKeyChecking=no bob@h', 'bob@h'],
    // A remote command comes straight back: not a place the user is sitting in.
    ['ssh host uptime', null],
    // A redirection means the shell is doing something with the output.
    ['ssh host > out.txt', null],
    ['ssh host | tee log', null],
    ['docker exec -it web bash', 'docker:web'],
    ['kubectl exec -it pod-1 -- sh', 'kubectl:pod-1'],
    ['sudo -i', 'root'],
    ['su bob', 'su bob'],
    ['sleep 5', null],
    ['ls', null],
    ['', null],
  ]
  for (const [line, want] of cases) {
    it(`${JSON.stringify(line)} -> ${want ?? 'null'}`, () => {
      expect(environmentEntry(line)?.label ?? null).toBe(want)
    })
  }
})
