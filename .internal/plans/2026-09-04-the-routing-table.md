# The routing table — nocx-dkawo.4

`nocx-dkawo.3` made a fact reach somebody. This makes it reach the RIGHT somebody, which
the bead calls "the design question that decides whether any of this is useful": which
facts are routine, which wake the coordinator, and which go to the human.

## 1. The table, and why it reads the wave rather than the fact

Two rules, in this order:

1. **A participant that did not succeed needs judgement, whatever else is running.** A
   worker that reported failure or died without saying anything is the situation a
   coordinator exists to handle. Holding it until the wave finishes would report a crash
   after the work that depended on it, and that ordering cannot be undone.
2. **A participant that succeeded needs judgement only when nothing else is running.** "A
   worker finished with two still running" is routine — the coordinator is waiting on all
   of them and has nothing to decide — and waking it spends a turn to say what it already
   expects. The last one landing is the wave arriving, which is the moment the coordinator
   is for.

The input is therefore the wave's remaining work, not the fact alone. The same fact means
different things at different moments and the record is the only thing that knows which
moment it is; a table that classified facts in isolation would have to choose between
waking on every completion — the poll this mechanism replaced — and waking on none, which
loses the end of the wave.

A `NonTerminal` read that FAILED counts as judgement. It is not evidence the wave is
finished and not evidence it is not; a fact the coordinator did not need costs it one
turn, and a fact it never learns about costs it the wave.

## 2. What coalesces, and what deliberately does not

**The deadline stays per fact** (D2). Nothing about coalescing changes what is being timed;
what coalesces is the two things that cost somebody something.

- **One wake per wave per undispatched run.** The coordinator's answer to a wake is
  `wave.holdings`, which returns everything its session holds, so a second wake before it
  has fetched spends a turn to say what the first turn was already going to show. A
  **refused** wake does not count as having woken anything — a refusal told the coordinator
  nothing, and the next fact is a fresh chance to catch a pane that is waiting for input.
  Treating a refusal as "already awake" would silence a wave for good the first time the
  coordinator happened to be mid-turn.
- **One card per wave, and the card says how many.** Five cards for one situation is how an
  attention surface becomes noise, which is the failure `nocx-ms7v.4` warns about in its own
  first paragraph. Suppression ends when the coordinator fetches: the wave owes nothing, and
  the next fact raises a new card.

Every fact still reaches the end of its own deadline and is marked escalated. Coalescing
suppresses the CARD, never the accounting.

## 3. Two numbers, because a design can be wrong in either direction

`Stats` counts `Routine`, `Judgement`, `Woken`, `Escalated`, `Cards` and `Dispatched`.

- **`Escalated` over `Facts()`** is the fraction §12 says the whole design is judged by: if
  most facts reach the human rather than the coordinator, the mechanism moved the work to a
  person and should say so out loud rather than be described as orchestration.
- **`Cards`** is how many times a person was interrupted. Five facts reaching someone in one
  card is five facts that reached them AND one interruption, and a design can be wrong in
  either of those alone.

The routine branch is counted for exactly this reason. A table whose routine facts left no
trace could report the fraction only over the facts it had already decided were
interesting, which is the flattering denominator.

## 4. What is out, and where it went

- **The deadline's measured VALUE** — `nocx-dkawo.10`. This slice ships the instrument; the
  reading needs traffic, and §10.8 says the number gets measured rather than guessed. A
  second unmeasured number per fact class would be guessing dressed as design.
- **The mailbox** — `nocx-dkawo.11`, filed as a gap. It is the sixth thing §6 names and
  nothing has built it; it is also what would give the table a word for "a worker ASKS",
  which today has to be read as "a worker declared failure".
- **The attention queue row** — `nocx-dkawo.9`, still behind `nocx-ms7v.4`.
- **The wait-for graph** (D12) — not filed; nothing needs it before mesh.
