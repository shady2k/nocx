# Spike: a second channel on a hand-typed ssh (nocx-qtnp) — measurements

**Recommendation: take it.** ControlMaster multiplexing holds on a real OpenSSH
client and server, the push rides the master with zero additional server-side
authentications, the timing margin is two orders of magnitude, and every failure
mode either degrades to the existing fail-open path or is preventable by
construction. Three wiring rules discovered below (push must carry
`ControlMaster=auto`, the socket path must be `%C`-derived under a nocx-created
0700 dir, the bootstrap fallback must be `if/else` rather than `|| exec`) are
what make it clean; they are not blockers.

Deciding numbers:

- 20/20 runs: 20 TCP connections, 20 authentications, 40 sessions (20 shell +
  20 sftp) on the server — the 35,243-byte push added no connection and no auth.
- submit → mux answers `-O check`: median 115.6 ms (p90 119.0, max 119.8).
  submit → 35 KB on the far filesystem: median 126.7 ms (max 129.1). The wait
  the bootstrap must cover (remote command start → file present): median
  9.7 ms, max 11.7 ms — a 3 s timeout is ~250× the worst measured.
- Full rewritten line: 158 bytes vs 189–207 today; the remote payload (35,243
  bytes) leaves argv entirely.
- Fail-closed cases are both construction-preventable: `ControlPath` > 107
  bytes → ssh refuses to start; socket dir missing/unwritable → ssh refuses to
  start. A short prefix plus `%C` (40-char hash, 74-byte full path measured)
  bounds the path, and the app creates the dir before submitting the line.

## What was measured and how

Real OpenSSH 10.4p1 client (`ssh`/`sftp` from `/run/current-system/sw/bin`) and
real `sshd` instances run as the current user with per-instance configs, host
keys minted per run, `LogLevel VERBOSE` auth logs, and `internal-sftp`
subsystems. Throwaway harness: `.internal/spikes/nocx-mlm7-multiplex/`
(`run.sh` + `driver.py`); results log at `/tmp/nocx-mlm7-exp4/full.log` on the
measurement box. One sshd per concern: main (timing + most failure modes),
`max1` (`MaxSessions 1`), `nosftp` (subsystem removed), `jump`/`final`
(ProxyJump). Timing used a monotonic clock; the "remote" filesystem is the same
machine (127.0.0.1), so file-arrival was polled locally.

The master shape was the proposed production line:

```
ssh -o ControlMaster=auto -o ControlPath=<p> -t user@127.0.0.1 \
  'printf "R\n"; while [ ! -s <target> ]; do sleep 0.005; done; echo F; cat <target> >/dev/null; echo D'
```

Each run spawns one master plus a few dozen `-O check` polls (the driver polls
every ~2 ms until the socket answers) plus one sftp push.

The second process was `sftp -o ControlMaster=auto -o ControlPath=<p> -b - -P <port> user@127.0.0.1`
pushing the payload (35,243 bytes — the documented ShellAuto launcher size,
`internal/shellintegration/stage.go`) via `put <payload> <target>.tmp` +
`rename` so the bootstrap's `-s` test can never see a partial file. Every run
verified sha256(pushed) == sha256(source).

## 1. Does the master work, and does the second process re-authenticate?

Yes, and no. Server-side proof, from the main sshd's verbose log during the 20
runs:

| event                                  | count |
| -------------------------------------- | ----- |
| TCP connections (`Connection from`)    | 20    |
| authentications (`Accepted publickey`) | 20    |
| sessions (`Starting session`)          | 40    |

The server saw exactly one connection and one authentication per run, and two
sessions per connection: `command … id 0` (the master's shell) and
`subsystem 'sftp' … id 1` (the push). The push and every poll added nothing to
the auth log. The identity check is per-run `sha=True` on all 20.

## 2. Timing, 20 runs, monotonic, milliseconds

| interval                                         | min  | p25   | median | p75   | p90   | max   |
| ------------------------------------------------ | ---- | ----- | ------ | ----- | ----- | ----- |
| submit → mux answers `-O check`                  | 77.1 | 113.8 | 115.6  | 117.8 | 119.0 | 119.8 |
| submit → remote command start                    | 77.1 | 115.8 | 116.4  | 118.1 | 118.5 | 119.5 |
| submit → 35 KB on far FS                         | 86.2 | 124.1 | 126.7  | 127.5 | 128.6 | 129.1 |
| submit → sftp exit                               | 86.2 | 124.1 | 126.7  | 127.5 | 128.6 | 129.1 |
| submit → bootstrap done (`D`)                    | 93.7 | 126.3 | 132.0  | 133.7 | 135.5 | 136.0 |
| mux → remote start (session setup)               | −1.4 | −0.3  | 0.6    | 2.4   | 3.5   | 4.0   |
| remote start → file present (the bootstrap wait) | 5.3  | 7.3   | 9.7    | 11.0  | 11.4  | 11.7  |
| mux → sftp exit (push wall)                      | 9.1  | 9.2   | 9.4    | 10.5  | 12.0  | 13.5  |
| file → `D` (bootstrap consumes)                  | 0.6  | 3.1   | 5.7    | 6.9   | 7.5   | 8.1   |
| `D` → ssh exit                                   | 0.2  | 1.6   | 4.1    | 8.3   | 8.6   | 8.7   |

Notes. The socket answers before the remote command starts, by about a
millisecond — the master binds the mux listener immediately after
authentication, before the session request. The −1.4 ms minimum on
"mux → remote start" is measurement noise: the `-O check` poll's own spawn
latency (±10 ms) can timestamp t_mux after t_remote when they are this close;
the p90 value of 3.5 ms is the honest bound. The bootstrap wait (9.7 ms
median) is the number that sets the timeout; it is dominated by sftp startup
over the mux, which on a WAN costs a handful of round trips (session request,
subsystem request, sftp init, the put — 35 KB fits one window), so roughly
4–6× RTT on top of the loopback cost. A 3-second timeout covers a 50 ms-RTT
link with ~10× margin, and a 250 ms-RTT link with ~2×. These are localhost
numbers; the absolute submit→… values would grow by the connection round trip
on a real link, but the bootstrap wait is the only one that feeds a timeout,
and it is the push duration, not the connect time.

## 3. Failure modes

| mode                                                 | observed behavior                                                                                                                                                                                                                                                                                                                                                                                       | fail-open reachable?                                |
| ---------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------- |
| User config `ControlMaster no` + `ControlPath none`  | Command-line `-o` wins: master created, `-O check` rc=0, payload pushed over the mux (subsystem session on the master's connection, no second auth)                                                                                                                                                                                                                                                     | n/a — works                                         |
| Same config, no CLI flags (baseline)                 | No socket ever appears; `-O check` rc=255 "No such file or directory"                                                                                                                                                                                                                                                                                                                                   | n/a — control                                       |
| `ControlPath` > 107 bytes (190-char dir)             | `ControlPath too long ('…' >= 108 bytes)`; ssh exits, no connection                                                                                                                                                                                                                                                                                                                                     | **No — first connection dies**                      |
| `%C` expansion                                       | 40-char hash (`mux-<32hex>`), full path 74 bytes with a short prefix; bounded regardless of `$HOME`/hostname length                                                                                                                                                                                                                                                                                     | prevents the above                                  |
| Stale dead socket, `ControlMaster=auto`              | Socket removed, ssh becomes master, `-O check` rc=0                                                                                                                                                                                                                                                                                                                                                     | yes                                                 |
| Regular file at the socket path, `auto`              | Same: removed, master takes over, rc=0                                                                                                                                                                                                                                                                                                                                                                  | yes                                                 |
| Stale dead socket, `ControlMaster=yes` (alternative) | `ControlSocket … already exists, disabling multiplexing`; ssh connects directly (full re-auth), rc=0                                                                                                                                                                                                                                                                                                    | yes, at the cost of a second auth                   |
| Socket dir unwritable                                | `unix_listener: cannot bind …: Permission denied`, ssh exits                                                                                                                                                                                                                                                                                                                                            | **No — first connection dies**                      |
| Socket dir missing                                   | `unix_listener: cannot bind …: No such file or directory`, ssh exits                                                                                                                                                                                                                                                                                                                                    | **No — first connection dies**                      |
| Server `MaxSessions 1`                               | Mux session request refused (`mux_client_request_session: session request failed: Session open refused by peer`); sftp falls back to its own direct connection — second authentication in the server log — and the payload still lands (35,243 bytes, server's `max1` log: two connections, two auths, one sftp subsystem each)                                                                         | yes (delivery works, no-second-auth promise broken) |
| Server without an sftp subsystem                     | `subsystem request failed on channel 0` + `Connection closed`, sftp rc=255; the master's session is unaffected                                                                                                                                                                                                                                                                                          | yes (bootstrap times out → login shell)             |
| ProxyJump (`-J`)                                     | Works: master via jump, `-O check` and the push do **not** need `-J` (the mux destination is the final host); final sshd log: one connection, one auth, two sessions, sha-identical payload. Caveat: the jump leg ignores command-line `-o` flags and re-reads the config file — the master must use a `-F` config (or the user's own config must be sane) or the jump leg runs default host-key policy | yes (see 4c)                                        |
| Mismatched destination on a shared `ControlPath`     | `-O check` answers rc=0 regardless of destination; an sftp with a different `-P` rode the master anyway (second `subsystem 'sftp'` session on the master's connection, no new auth, file written through the master's server)                                                                                                                                                                           | n/a — security note (4b)                            |
| Master never appears / push never lands              | With the flag failures above (long path, bad dir) there is no session at all. With a live master but a failed push, the bounded bootstrap times out and hands to a login shell — observed `NOCX_TIMEOUT` marker then normal session continuation                                                                                                                                                        | yes (integration lost, session works)               |

## 4. Findings that change the implementation

These were not in the brief's list and none of them kills the approach, but all
four are wiring requirements, and each was found by measuring the wrong shape
first.

**a. The push must pass `-o ControlMaster=auto` itself.** `ControlPath` is
inert unless `ControlMaster` is set (ssh(1): "If set to 'no', then the
ControlPath and ControlPersist options will have no effect"). The first harness
run omitted it on sftp; the push silently opened a second fully-authenticated
connection and the auth proof looked broken. With `ControlMaster=auto` on the
sftp line, the 20-run proof above holds.

**b. The socket path must be per-destination (`%C` or equivalent), never
fixed.** With a fixed `ControlPath` shared by two destinations, a second
process was not rejected: the mux master accepted the mismatched session
request and executed it on its own connection (observed: `put` with a wrong
`-P` landed on the master's server, second `subsystem 'sftp'` session, no new
auth). OpenSSH 10.4's mux protocol does not isolate destinations; the control
socket is the trust boundary. `%C` makes collisions impossible by construction
and also bounds the path length (see the `%C` row in §3).
**c. The ProxyJump leg does not inherit command-line `-o` options.** The jump
child re-reads the config file; with `-o StrictHostKeyChecking=no` on the
command line the jump leg still ran default policy and failed host-key
verification (`ssh_askpass: exec(/usr/bin/false)` / `Host key verification
failed`). The final-leg mux works regardless — the finding is about the jump
leg only, and it is the user's own config, which we should not be overriding.
The harness switched to a dedicated `-F` file for its own keys; production
needs nothing, since the jump leg is governed by the user's config by design.

**d. The bootstrap fallback must be `if/else`, not `exec A || exec B`.**
Measured: in a non-interactive shell (which is how sshd runs a remote command),
`exec` of a nonexistent file terminates the shell with 127 and the `||` branch
never runs — a `… || exec "${SHELL:-/bin/sh}" -l` bootstrap dies silently with
no session. The `if [ -s F ]; then exec F; else exec …; fi` form (the one the
design already uses in §3.3) works and is what the timeout variant below uses.

## 5. Byte lengths

| line                                                                                                                        | bytes |
| --------------------------------------------------------------------------------------------------------------------------- | ----- |
| today's bootstrap rewrite (canonical path `/home/u/.nocx/run/launcher-12345`)                                               | 189   |
| today's bootstrap rewrite (realistic `/home/dev` path)                                                                      | 207   |
| proposed full line: `ssh -o ControlMaster=auto -o ControlPath=/tmp/nocx-mux/m -t <user>@<lan-ip> '<bootstrap>'`             | 158   |
| remote bootstrap alone, POSIX (sleep 1): `while [ ! -s /tmp/nocx-mux/launch ]; do sleep 1; done; exec /tmp/nocx-mux/launch` | 80    |
| remote bootstrap, fractional sleep 0.1 (non-POSIX; faster poll)                                                             | 76    |
| remote bootstrap + 30 s timeout + login-shell fallback, if/else form                                                        | 177   |
| installed-mode line today (design §3.3, for reference)                                                                      | 134   |

The canonical today's line was reproduced from `buildBootstrapRewrite`'s
template with the test file's exact inputs (`'/home/u/.nocx/run/launcher-12345'`
including quotes, `ssh <user>@<lan-ip>`). The proposed line is 31–49 bytes
shorter than today's and, more importantly, its payload component is 35,243
bytes that no longer travel in argv at all (they go over the mux), and it
works from the first connection to a host with no remote footprint. The line
is even competitive with the installed-mode line (158 vs 134 bytes) — only 24
bytes longer, with none of the installation machinery.

Payload: 35,243 bytes (documented ShellAuto launcher size, stage.go). The
brief's "~35 KB" and the current launcher agree; no substitution was needed.

## 6. Untestable on this box

- **WAN timing.** Everything is loopback; the submit→… absolutes and the
  bootstrap wait include ~one extra round trip per leg on a real link. The
  wait number that matters (9.7 ms median, 11.7 ms max) is the push duration,
  so a seconds-scale timeout is safe; exact WAN distributions were not
  measured.
- **The login-shell prompt in the timeout path.** With `-t` and EOF on stdin
  the fallback login shell exits before a prompt is readable; the timeout
  branch itself was observed (marker printed, session continued). Prompt
  rendering on a real tty was not captured.
- **`ControlPersist`** was not part of the question and was not tested.
- **Non-Linux servers.** The 104-byte socket limit is the Linux `sun_path`
  bound; other platforms differ. The `%C` construction makes this moot on all
  of them, since it bounds the length rather than relying on a platform value.

## 7. Aggregate audit note

The timing slice is the controlled proof (20/20/40 above). Across the whole
run the main server logged 30 connections / 29 auths: the extra authenticated
connection over the 27 directly attributable to timing + failure modes could
not be tied to a specific mode phase from the aggregate log. It is non-decisive
— every failure mode's observable behavior was captured in its own output —
and is noted here rather than hidden.

## Commands

```bash
bash .internal/spikes/nocx-mlm7-multiplex/run.sh /tmp/nocx-mlm7-exp4   # full run, N=20 (N=3 for a smoke)
# run.sh: keys, payload, per-mode sshd instances, timing driver, failure modes, byte lengths
# driver.py: monotonic timing, -O check poll, sftp push, sha verification
# evidence: /tmp/nocx-mlm7-exp4/{full.log,sshd-*.log,auth.log.slice}
```
