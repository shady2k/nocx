# Where this cat came from

|            |                                                                                                   |
| ---------- | ------------------------------------------------------------------------------------------------- |
| Pack       | **Pet Cats Pack** by luizmelo                                                                     |
| Source     | <https://luizmelo.itch.io/pet-cat-pack>                                                           |
| Licence    | **CC0-1.0**, stated by the author in `LICENSE.txt`, which is the pack's own file, copied verbatim |
| Downloaded | 2026-09-02                                                                                        |
| Archive    | `Pet Cats Pack.zip`, sha256 `3370f103a5383fec39dfa3a7fa95635edad7701764982960e7a3237a002a40f6`    |

## What we changed

Nothing was redrawn. The pack ships six cats; we vendor **Cat-1** only, and its
files were renamed from `Cat-1-<State>.png` to lower-case `<state>.png` so the
loader can address them by clip name.

All twelve are here, and this directory is one of six — `cat-1` … `cat-6`,
one per colour, each with the same file names.

`Licking 2` and `Sleeping2` are the pack's second drawings of two behaviours
that repeat often, and `pack.ts` plays them as alternate TAKES: a fresh one is
chosen each time the clip starts. They were dropped from the first cut, when
the format still allowed one file per behaviour, and came back with it — a
cat that washes itself with the identical five frames every time is a loop
rather than an animal. Cat-3 has no `Itch` sheet at all, which costs it that
one behaviour and nothing else.

Each file is one horizontal strip of 50×50 cells. The cat occupies roughly
30×16 of each cell; the loader trims that padding at load time by scanning
the alpha channel — see `frontend/src/pets/pack.ts` for why the trim is
computed once across every frame of every clip rather than per frame.

## Why the provenance matters

nocx ships builds from a public repository, so anything vendored here is
REDISTRIBUTED by us. CC0 is the only licence that needs no further thought;
everything else in this genre (Elthen, seethingswarm's Catset, NVPH's dogs)
either forbids redistribution or forbids modification. A pack whose licence
cannot be produced on demand does not belong in this directory — see
`AGENTS.md` on why the burden sits here rather than in a release checklist.
