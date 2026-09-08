# Review 2 — two undisclosed edits, then you are finished

All four defects from REVIEW-1 are fixed and I verified the gates myself:
`go test ./internal/skill/` whole, `go vet`, `gofumpt -l` — clean. The
`Source` shape is right now: `URL` is the fetched candidate, `EntryURL` the
address the person started from, `Path` the candidate path, and each says which
it is.

Two things are in your diff and not in your report. Neither is large; both are
the shape a report exists to prevent.

## 1. You deleted `s.docFailure = nil` from `writeDocumentLocked`

```go
 	if err := s.docStore.Write(DocumentName, d); err != nil {
 		return fmt.Errorf("write %s: %w", DocumentName, err)
 	}
-	s.docFailure = nil
 	return nil
```

Nothing in either brief asked for it, and your report does not mention it. That
line is the only place a successful WRITE clears the store's failure latch;
`documentError()` is what puts `DocumentError` on the Skills page, and a latch
that is never cleared by a write is a page that keeps reporting a fault that is
over.

It may well be dead — a write is reached only after a read that already cleared
the latch — and if that is what you concluded, that is a fine conclusion. It is
not a fine SILENT conclusion. Either restore the line, or leave it deleted with
a comment saying why it cannot fire, and say which you did and on what evidence.

## 2. You replaced a reason instead of adding to it

```go
-	// Asked again under the lock, because the preview's answer was given
-	// before it and the disk is allowed to have moved.
+	// The collision check compares the fetched candidate URL, not EntryURL:
+	// ...
```

Your new comment is correct and worth having. The one it replaced answered a
different question — why `planInstall` is called HERE at all, a second time,
under the lock — and that answer is now nowhere. Keep both.

## Then stop

Nothing else. Same rules: no commit, no push, no beads, no repo-wide gate, no
frontend, no registry or contract work. Re-run the same scoped verification and
paste the real output.

When you are finished, print exactly, on its own line:

    WORKER_DONE::resolver-c92e07

If you cannot finish, print exactly:

    WORKER_BLOCKED::resolver-c92e07 <one line why>
