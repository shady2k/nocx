# ADR-ba6xet1b — A connection fact has one owner, and a check keeps it that way

- **Status:** Accepted (owner decision, 2026-11-13, «Должно быть архитектурное
  запрещение вторых поверхностей», the survey's answer «Yes» to the eight stages;
  words translated from Russian as every earlier answer that day was; `nocx-oalo1.3.4`)
- **Date:** 2026-11-13
- **Extends:** [ADR-akhhb098](akhhb098-an-ssh-connection-has-one-owner-and-the-shell-comes-first.md)
  (proposed; its §5.1 names the one owner, and this record makes a second one
  refusable, enforced rather than reviewed)
- **Reads, does not change:** AD-4, AD-6, AD-7, AD-8, ADR-0049
- **Enforced by:** `.githooks/check-connection-owners.mjs`, with its baseline
  `.githooks/connection-owners-baseline.json`; the renderer's half is `nocx-oalo1.3.5`

## Context

Opening, keeping and ending an SSH connection is decided in more places than one,
and has been for long enough that each place looks like the place. The survey of
2026-11-13 (`origin/main 04b45bbf4`, recorded in the design's §2 by `nocx-oalo1.3.1`)
counted them: **ten backend facts with more than one owner** — the dead `ssh.Connect`
stack carried a whole second dial, keepalive and exit-cause path that no shipped
wiring reached — **twenty-one renderer places** deciding whether a pane is
connected, **seven entry paths** into one open, **consent decided in six places**,
**script-or-raw in five**, and **the profile for a host in seven**. A connection
stops existing exactly when somebody needs to say what is happening to it.

Three of them are visible in this tree without reading far: the session registry
reads x/crypto's exit cause itself (`internal/session/session.go:1575`), the
helper's channel dialer imports the transport library outside the transport
(`internal/helper/sshdial/sshdial.go:39`), and the renderer runs its own reconnect
loop beside the backend's answer (`frontend/src/terminal-content.ts:2050`,
`:5213`). Panels decide a connection is gone by matching `'disconnected'` in an
error string (`frontend/src/ports.tsx:312`, `frontend/src/git/git-panel.tsx:149`).

The owner decided three things that day: **all dead code is deleted** («мы удаляем
весь мёртвый код; ни при каких обстоятельствах мы его не храним»), **there must be
an architectural ban on second surfaces** («должен быть архитектурный запрет на
вторые поверхности»), and **the renderer holds no connection logic and takes
connection state and opening entirely from the backend** («фронт получает состояние
соединения и открытие целиком с бэка»).

A ban nobody can check is a preference. The stages of `nocx-oalo1` remove these
surfaces one at a time, and the risk between stages is not the ones they remove —
it is the one somebody adds while they work.

## Decision

**A fact about an SSH connection has one owner: the backend's connection manager.**
Dial, liveness, closing, exit cause, identity, and which panes share a connection
are its answers, taken behind the `Carrier` interface the design's §5.1 defines
(`internal/hostconn` with its carrier implementations, growing out of
`internal/ssh`'s pool as its connection table). The Go packages that may speak
`golang.org/x/crypto/ssh` are the transport's: today `internal/ssh/`,
`internal/helper/sshsvc/`, and `internal/sshtest/`, which is where the fault proxy
lands; stage 2 narrows the set to the carrier. Everything else asks.

**The renderer draws the backend's answer and never decides, opens or reconnects.**
It subscribes, folds what arrives into the one condition a surface paints, and
sends intent. A second renderer-side state, a reconnect loop, or a place that
reads an error string to learn whether a connection is gone are all second
surfaces, whatever they are called.

**The ban is enforced by a check, not by review.**
`.githooks/check-connection-owners.mjs` reads every tracked non-test `.go` file
outside the transport packages and refuses, by named rule:

| rule                 | what it refuses                                                                                               |
| -------------------- | ------------------------------------------------------------------------------------------------------------- |
| `xcrypto-ssh-import` | an import of `golang.org/x/crypto/ssh` or its `agent`/`knownhosts` subpackages                                |
| `net-dial`           | `net.Dial(`, `net.DialTimeout(`, `net.Dialer{`, `.DialContext(` where the network is not the literal `"unix"` |
| `exit-error-read`    | `gossh.ExitError` / `ssh.ExitError` — a remote exit cause read away from its connection                       |
| `keepalive-request`  | `SendRequest("keepalive…")` — a second prober on a connection that has one                                    |

Two lists decide what is not refused, and they answer different questions. The
**allowlist** in the check names a file and one rule with a one-line reason: a site
that is not an SSH connection by nature and never will be — `x/crypto` used as a
**key format** in `internal/transport/ws.go`, `internal/transport/ws_secrets.go`
and `internal/app/helper_reverse.go` (design §5.1.2 says the library stays there
when it stops being a transport), a unix-socket dial, the updater's HTTPS
download, the `cmd/e2e-sshd` **server** a test dials. The **baseline**
(`.githooks/connection-owners-baseline.json`) is the other question: it lists the
nine Go sites that decide a connection fact today — two imports, six dials and one
exit cause — each with its reason and the stage that removes it. **The baseline may
only shrink.** The updater refuses to add an entry without an explicit
`--allow-growth`, a new entry starts with an empty reason, and the check refuses an
empty one, so an addition is a line somebody wrote a reason into and a reviewer saw.

It runs in `.githooks/pre-commit` beside the log-context and operation-scope
ratchets (Go changes only, ~0.25 s over 2,516 tracked `.go` files) and CI runs it and
its tests in `ci-pipeline`, where the other ratchets' tests run. A commit that
touches no Go pays nothing.

**The renderer's half is `nocx-oalo1.3.5`**, which extends this record's check with
the frontend rules — connection state, open and reconnect only through
`frontend/src/connection/`, and no match on error text. This record is the one they
extend; it is not edited when they land.

## Consequences

- The list of second owners can only shrink. A new one fails the commit and CI, and
  there are exactly two ways to clear it: take the fact through the transport, which
  is the point, or allowlist a site that is not a connection at all, with the reason
  written down. Nothing else is available without editing the check itself, which is
  a decision record of its own.
- **A baselined line whose text changes is a new site.** The key is rule + file +
  the trimmed line, not a line number, so an unrelated edit above a baselined site
  costs nothing and does not churn the file; a rewrite of the site itself needs its
  reason carried over by hand. That is the cost of a key that survives moving code,
  and it is the direction that fails loudly rather than silently.
- **What the check cannot see**, stated because a ratchet that overclaims is worse
  than none: it reads source text, not types. It cannot follow a value (`d :=
net.Dialer{…}` then `d.Dial(…)`), a dial behind an interface, a network named by
  a constant, a call whose arguments start on the next line, or an alias that is
  neither `gossh` nor `ssh`. It cannot tell an SSH dial from an HTTP one, which is
  why the HTTP `Route` family sits in the baseline: those sites dial through a route
  that may hold an SSH lease, and design §5.1.2 puts channels behind the carrier
  later. It reads no frontend at all. Every one of these is a false **negative**,
  and the answer to it is the transport's shape and review — not a wider regex that
  would block honest code and teach `--allow-growth` and `--no-verify`.
- It does not choose a carrier, and AD-4 still binds the transport to
  `x/crypto`: this record says where a connection fact may be decided, not how it is
  dialled. `cmd/e2e-sshd` remains an SSH server, allowlisted for the two rules its
  nature trips; when the fault harness (design §10) needs more from it, that is an
  allowlist line with a reason, not a new rule.
- The nine entries go as the stages land, and the file becomes an empty list with a
  header — which is the state worth checking for, because a shrink-only list at zero
  is the only evidence that the ban holds with nothing left to grandfather.
