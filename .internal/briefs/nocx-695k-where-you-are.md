# nocx-695k — the pane says where you are, and it is right

**Epic:** `nocx-695k`. Children, in order: `nocx-695k.1` (the core, blocks the rest),
then `nocx-695k.2`, `nocx-695k.3` and `nocx-pu4.4`. Read all four with `bd show` before
you start; this brief adds the boundaries, the seams and the traps.

Three reports from the owner on 2026-08-04, one root cause.

1. Typing `ssh <user>@<lan-ip>` in a local tab: no offer to integrate, no blocks, nothing.
2. The Ports panel goes on listing the **backend machine's** listeners — `0.0.0.0:22`,
   `orca-ide`, `devharness` — under a tab titled `pi@host-2`, with nothing saying
   whose they are.
3. After `exit`, the tab still reads `pi@host-2: ~` while the pane is back home.

Every surface that names a place reads a fact about the **tab's lifetime** — the session
kind, the sticky integrated flag, a title the remote shell set once — when the thing it
must describe is the **environment currently on stdin**. `terminal-content.ts`'s own
`_mountRail` states the false premise out loud: _"a local shell's capability is static
(always our own, always integrated)"_. The screenshot is its counterexample.

## The boundaries — what they already decided

- **AD-6** — the backend never sniffs the byte stream, and nothing here changes that.
- **ADR-0004 §2** — the line is handed to the PTY **atomically at submit**. The renderer
  therefore knows what it sent; that is knowledge of our own output, not inspection of the
  stream. Combined with "no `D` marker has arrived", it tells us we are inside a command we
  submitted that has not returned.
- **ADR-0004 §1 and its scope note (2026-08-04, nocx-4t37.2)** — read the note. The
  input-ownership machine governs **keyboard ownership**, stays marker-only and fail-open,
  and is **not** the authorisation model for one-shot user-initiated delivery.
  **Do not add a state, a field or a clause to `reduce()` in `input-state.ts`.**
- **ADR-0006** — marker-only prompt, and why a nested environment is only partly visible.
- **AD-8 / Interface-first + DI** for anything that reaches the backend.

## Section A — `nocx-695k.1`, the core (do this first; the rest depend on it)

`_shellIntegrated` in `frontend/src/terminal-content.ts` is set true the first time markers
arrive and never cleared. Make the fact **environment-scoped**: "markers have arrived for
the shell currently on stdin." Entering a nested environment clears it; the `D` that ends
that command restores what was true before. Name it so the next reader cannot mistake it
for a tab-lifetime flag, and delete the false premise comment in `_mountRail` while you are
there.

Recognise environment-entering commands **from the line we submitted**, in **one named
place**, with a comment saying it grows by addition: `ssh`, `docker exec`, `podman exec`,
`kubectl exec`, `su`, `sudo -i`/`sudo -s`, `nix-shell`, `tmux`, `screen`. **The default for
an unknown command is "not an environment change."** Conservative on purpose: a missed
offer is invisible and cheap, a wrong one is noise on every `sleep 5`.

## Section B — `nocx-pu4.4`, the offer

Today `integrateShell()` refuses this case from **both** named authorisations: the
integrated path needs `PROMPT_READY` (we are `RUNNING_RAW`), and the markerless path needs
`!_shellIntegrated`, which the _local_ shell set true. Section A fixes the second.

Then:

- **Mount the rail on every tab.** `_mountRail` currently opens with
  `if (this._rail || !this.sshOpts) return`. Keep the placement (above the pending command).
- **Tell the truth in the statement.** Inside a nested un-integrated environment the
  capability is `native-input`, not `enhanced-input`, and it is **not** `degraded` — a
  nested shell is a normal thing that happened, not a fault. Do not fire the warning dot.
- **`_railActions()` must offer `Integrate this shell` there**; today it offers it only for
  `native-input`, which Section A now makes true anyway — check, don't assume.
- **Offer once per environment entry, and let the refusal stick** for that session
  (`nocx-pu4.4`'s own criterion). Refusing must leave a way back: the chip stays.
- **`off` still means off.** A profile whose `shellIntegration` is `off` (nocx-p0ug, given
  teeth in nocx-4t37.2) refuses this path too, with a stated reason.

The safety argument is unchanged and already built: verification is the **OSC 1337 READY
handshake**. Read `terminal-content.ts` around `renderer.onInBandReady(...)` — the ~25 KB
payload goes out only from inside that callback. The single blind write is one wrapper line;
if what is reading stdin is not a shell, READY never returns, `IN_BAND_TIMEOUT_MS` fires,
nothing further is sent.

## Section C — `nocx-695k.2`, the tab title

The title is whatever the last OSC 2 set, and the remote shell set it on the way in.
Nothing resets it on the way out because nothing knew there was a way out. With Section A
there is: leaving an environment restores the title the pane had before entering it, within
one prompt. No title outlives the environment that set it.

## Section D — `nocx-695k.3`, the ports panel

**Know the limit before you design.** Remote discovery runs over `internal/ssh.DiscoveryConn`,
which needs a **managed** connection from a profile — a second exec channel on a connection
we own. A hand-typed `ssh` is a child process of the local shell; there is no control
channel to it, so that host's ports **cannot be enumerated at all**, integrated or not.

So the fix is **not** "show the remote ports here". The fix is that the panel stops lying:

- It **names whose listeners these are.**
- Where the pane's current environment is one it cannot enumerate, it **says that and why**,
  and names what would change it (open this host as a connection). A soft degrade must be
  visible in the product, not only in a log — AGENTS.md.

Do not invent a way to probe a child `ssh`. If you think you have found one, that is an
`orca orchestration ask`, not a commit.

## Acceptance — the epic's, as assertions

- In a local tab, submitting `ssh <host>` makes the tab title, the ports panel and the
  capability chip **all agree** the pane is on that host.
- Typing `exit` returns all three to the local machine within one prompt.
- The chip offers `Integrate this shell` on arrival; taking it makes the next command a
  block and the location chip name the remote host.
- Refusing dismisses the offer for that session; it does not return on the next redraw, and
  the chip is still there to act on later.
- Submitting `sleep 5` produces no offer and changes no title.
- The ports panel never presents one machine's listeners as another's, and where it cannot
  see, it says so.
- `input-state.ts` is unchanged — one test asserts the machine's transitions are identical.
- **One end-to-end check watches the whole round trip** against a real bash on a real PTY.
  `cmd/e2e-sshd` (built for nocx-4t37.2, used by `e2e/shell-mode.spec.ts`) is your remote
  host, so there is no excuse about the harness. A test that mocks the shell cannot tell you
  any of this worked.

## Out of scope — do not widen

- Enumerating a hand-typed remote host's ports (see Section D).
- Environments entered by typing raw into xterm **in native mode** — we did not submit the
  line and genuinely do not know. Say so in a comment; do not guess and do not sniff.
- `nocx-w7h.15` (remote completion) and `nocx-25k9.22` (the vault) — other workers are in
  those files right now.

## Working rules

TDD, failing test first. Full local gate before you report: `gofumpt -l .`,
`golangci-lint run`, `go test -race ./...`, and in `frontend/`: `npx prettier --check .`,
`npx eslint .`, `npm run typecheck`, `npm test`. Commit messages carry the child's bead id.
Report a blocker via `orca orchestration ask` the same minute you hit it — a blocker that
lives only in a final report evaporates between rounds. And do not report as done anything
with no caller from `main()`: reachable-from-tests is not reachable (AGENTS.md, the
`nocx-rtg0` post-mortem).
