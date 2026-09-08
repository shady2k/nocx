# Review of your first pass — four defects, then you are done

The work is good and the tests are real: I ran `go test ./internal/skill/` whole
(not just your named tests), `go vet` and `gofumpt -l` myself, and all are
clean. Deleting the `store_doc.go` header paragraph that justified NOT parsing
out repository, ref and path was right — its reasoning cited machinery that has
since been restored, so it had become false.

Four things to fix. Do not start anything new.

## 1. The recorded source no longer names what was fetched

`Install` now writes `source.url`, which for a resolved install is
`plan.address` — the address the person GAVE `Resolve` (a repository, or a
page). The raw candidate address that was actually fetched, and the path inside
the repository, are now recorded nowhere.

Two things say that is wrong, and both are in the tree:

- `Source.URL`'s own doc comment, which you left in place, still says "the
  address that was FETCHED and that an update stays pinned to". It is now false
  for every resolved source.
- `recordApprovalDigest`'s comment says the record is what makes the row
  auditable: "a person can re-fetch the address and hash it, and compare against
  what they said yes to". With only the repository address stored, they cannot.

And the spec is explicit that this is an addition, not a substitution — §5:
"The stored source keeps the address the person started from **as well**."

So keep both. The fetched candidate address and the path inside the repository
are facts about what landed; the entry address is the only record that a
transition between sources happened at all, and §5 says a person auditing a
skill a month later is entitled to see it. Say in the field comments which is
which, because the next reader will otherwise have to guess.

While you are there: `planInstall(document.Name, source.url)` changed which
address the reinstall-collision check compares. Decide deliberately which
address that check is about, and write the reason down where the change is.

## 2. `PreviewResolved` blanks the field the approval window shows

```go
document.URL = ""
```

The comment justifies it as "the model gets the bytes and findings, but not the
source address". The model is not who reads that field. `ApprovalInstall.URL`
(`internal/assistant/skillinstall.go`) is assembled SERVER-SIDE from this
preview, and its doc says it is "the address that was FETCHED — the one the
digest, the manifest and every byte below came from … stated here rather than
left to the arguments blob because it is the resolution the question is about".

So blanking it empties the source line in the one window where the person
decides, which is the opposite of spec §3. Keep the model from carrying the
address by some means that does not also blind the approval — and if you
conclude that cannot be done inside `internal/skill`, say so in the report and
leave the field populated; task 3 owns what the model is handed.

## 3. `InstallResolved` can leave a half-installed set and not say so

The loop installs candidate by candidate. If candidate 3 of 5 fails, candidates
1 and 2 are on disk and the function returns `(nil, err)` — `results` is
discarded. The person approved a set and is told only that something went wrong.

AGENTS.md testing rule 3 names this shape: "for a procedure touching several
stores, enumerate the partial failures: step 3 of 5 fails — what is now true on
disk, and how does the next start recover?"

Decide and implement one of: all-or-nothing (roll the installed ones back, the
way `undoInstall` already does for a single install), or report what landed
alongside the failure. Either is defensible; silence is not. Whichever you pick,
there is a test where the middle candidate fails and the assertion names what is
on disk afterwards.

## 4. The interval, stated with both ends

`TestStore_ResolutionHandleIsReplacedAndSpentByResolvedInstall` covers the
opening. Make sure something covers the closing event explicitly: after the LAST
candidate of a multi-candidate set is installed, the handle is gone, and a
further `InstallResolved` with that handle is refused in words. If the test
already does it, say so and move on.

## Same rules as before

No commit, no push, no branch, no beads, no repo-wide gate, no frontend, no
registry declaration or contract schema. Same scoped verification, and paste the
real output.

When you are finished, print exactly, on its own line:

    WORKER_DONE::resolver-b41d92

If you cannot finish, print exactly:

    WORKER_BLOCKED::resolver-b41d92 <one line why>
