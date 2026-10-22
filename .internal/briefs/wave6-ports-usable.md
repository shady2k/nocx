# Wave 6 — the ports panel becomes usable

Two workers. Read the ground rules, then only your own section.

The owner reviewed the panel in the running app and raised four things. Three are
in section B, one is section A. The screenshots are not in the repo; the
descriptions below are what they showed.

## Ground rules (both workers)

- **No commit, no push, no branch.** **Do not touch `bd`.**
- Gates scoped to what you touch: `go build ./...`, `go vet`,
  `golangci-lint run`, `gofumpt -l`. From `frontend/`:
  `./node_modules/.bin/tsc --noEmit`, `npx eslint src/ --max-warnings 0`,
  `npx prettier --check src/`, `npm run contracts:check`, `npm test -- --run`.
- Report by writing `.internal/reports/<your-bead>.md`. The coordinator reads the
  worktree.
- **A owns Go, B owns `frontend/src`.** The one thing you share is the target
  identity for a local tab — A defines it on the wire, B consumes it. A: put the
  exact shape in your report early.

---

## A — `nocx-wzc4.8`: the local machine listens too, and we ask the kernel

Owner: _"почему для локального шелл мы не показываем порты? Давай показывать."_
Then, on reading the first version of this brief: _"Как мы узнаем порты в
зависимости от ОС? У нас же есть абстракция? Нативные вызовы ОС должны идти
отдельным модулем."_ They are right, and the first version of this section was
wrong. What follows replaces it.

A local tab renders "No active connection". The machine you are sitting at
listens on ports like any other.

### The command ladder is the remote answer, and only the remote answer

`ss` → `netstat` → busybox `netstat` → `lsof` → `sockstat` exists because on
another machine a shell command is the only thing we can run. That reasoning does
not reach the local machine, where the kernel will hand us the table directly.
Shelling out locally would make the feature depend on tools the user may not have
installed on their own box, and would have us parse version-variant text for data
we can read structurally. **Do not reuse the ladder locally.**

### Two providers, one domain

`internal/discovery` owns the domain — `Listener`, the five result states, the
three-valued process evidence, the cadence — and must know nothing about how the
listeners were obtained. One interface there, two implementations behind it,
chosen at the composition root (AD-8):

- **remote** — the existing ladder over an exec channel. Unchanged.
- **local** — native, per OS, **its own package**.

That also fixes the inversion the owner is pointing at: `discovery.Connector`
currently returns `ssh.DiscoveryConn`, so the domain package names the transport
it happened to be built against first.

### The native module

Follow the house pattern exactly — `internal/contentkey` is the worked example:
one exported function, `_linux.go` / `_darwin.go` / `_windows.go` behind
`//go:build`, and an `_other.go` that returns a typed "not implemented on this
platform" rather than pretending. That fallback maps to the existing `unavailable`
state, so an unsupported OS degrades into a sentence the panel already knows how
to render.

- **Linux** — `/proc/net/tcp`, `/proc/net/tcp6`. Owner via the socket inode
  matched through `/proc/*/fd`, which is also where `permission-denied` evidence
  comes from naturally: you can only walk the processes you own. Same three-valued
  evidence as remote, for the same reason, so it must render identically.
- **Windows** — `GetExtendedTcpTable` through `golang.org/x/sys/windows`
  (already a dependency, `v0.47.0`). No cgo.
- **macOS** — this is the one that needs a decision, and it is yours to make with
  evidence. The native route is `libproc` (`proc_listpids` / `proc_pidfdinfo`),
  which needs cgo. The repo has **no `import "C"` today and sets `CGO_ENABLED`
  nowhere**, so check what the Wails build actually does before assuming cgo is
  free — if `-race` and the release build already pull it in, the cost is nil; if
  they do not, introducing it is a real change and you must say so rather than
  slide it in. If you conclude cgo is not worth it, `lsof` on darwin is a
  defensible fallback **as long as your report says it is a fallback and why**,
  and it still lives in the native module behind the same interface. nocx ships
  macOS first, so a shrug here is not an answer.

### Forwarding, and the wire

There is nothing to forward from the machine you are already on, so a local row
must not offer it. Whatever replaces the action should be useful rather than
disabled chrome — copying the address is the obvious candidate; argue for what
you pick. (Exposing a local port on a remote host is `-R` and needs a chosen
connection: a later bead, not this one.)

`ports.*` is keyed by `profileId` and a local tab has no profile. Define the
target identity for "this machine", put it in the schema, and **tell worker B the
exact shape in your report before you finish** — B is blocked on that one fact
and nothing else.

### Test first

The local provider lists this machine's listening ports and the test asserts a
port the test itself opened, so it cannot pass against a stale table. An
unsupported platform degrades to `unavailable` rather than an empty list.
Switching between a local tab and an SSH tab re-scopes in **both** directions.
The discovery package's own tests stop needing an SSH server to exercise the
domain. And per `AGENTS.md`: for every "returns an error when…" there is a paired
"and on an ordinary machine it succeeds".

## B — `nocx-wzc4.9`: it has to be readable in a sidebar

Three defects the owner named, in one surface. The panel was designed against a
tab's width and now lives in a narrow sidebar; that is the root of two of them.

**1. There is no loading state.** Ports appear seconds after the view opens — the
settle delay plus a round trip — and until then the panel is blank, which reads
as broken rather than as working. First open shows that it is working. Later
refreshes must update **in place**: never blank a populated list to show a
spinner, because the list is what the user is watching.

**2. The rows do not fit.** In the screenshot the address — the row's primary
key, the thing the user came for — is truncated to `127.0....`, while the process
chip takes most of the width and wraps to three lines, making each row about
three times taller than it needs to be. The address must be fully readable at the
sidebar's default width; the process is secondary and must truncate before the
address does. The action is an icon, not a text button, and `Detected —
<lan-ip>` repeats what the tab already says in a column that has no room for
it.

**3. Retry and Pause should not be standing in the body.** Retry already exists
inside the failure states, which is where it belongs; the toolbar copy is a
second vocabulary for one concept. Pause is a real need — nobody wants a
background poll against a production host — but it belongs in the view's header
actions, which `SidebarViewDescriptor.actions` already provides. `last sample`
is muted micro-text, not a `Badge` chip.

**And answer the owner's actual question with a number.** They asked whether
sampling is heavy enough to justify a manual control at all. Measure one sample —
the exec channel open, the probe, the parse — against a real host, and put the
milliseconds in your report. If it is cheap, say so and let the cadence run
without asking; if it is not, say what makes it expensive. Do not answer this
one with an adjective.

**Read `frontend/src/ui/README.md` and list `frontend/src/ui/` first.** There is
no spinner or skeleton in the kit today. If you need one, it goes **into `ui/`**
with its CSS file, identity class, test and README row — not hand-rolled inside
the surface. That rule is what two epics were spent unwinding.

**Test first**, and assert what a user can do rather than what renders: the view
shows it is loading before the first sample lands, a populated list is not
blanked by a refresh, a failure state offers exactly one Retry, and pausing from
the header stops sampling.

Worker A is adding a local-target identity to `ports.*`. Until their report names
it, build against the SSH path and leave the local branch behind whatever they
define; do not invent a shape and hope it matches.
