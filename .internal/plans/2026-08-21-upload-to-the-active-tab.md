# Upload to the active tab — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use beads-superpowers:subagent-driven-development (recommended) or beads-superpowers:executing-plans to implement this plan task-by-task. Each Task becomes a bead (`bd create -t task --parent <epic-id>`). Steps within tasks use checkbox (`- [ ]`) syntax for human readability.

**Goal:** A person drops a file on the terminal of an SSH tab, or picks one in the Files panel, and the file lands on that host — with progress, cancellation, and a collision question asked before a byte moves.

**Architecture:** One sink (`internal/transfer`) writes a remote file through its **own** `FSConn` lease, one lane call per chunk. Two sources feed it an `io.Reader`: a file on the backend's disk, named by a backend-minted **source ticket** the renderer can echo but not author, or a byte stream arriving on `POST /upload/{ticket}`. The write capability is discovered at `files.open` time — where the endpoint attester already is — recorded on the binding, and exposed as `Handle.Upload`, so a local binding refuses structurally.

**Tech Stack:** Go 1.x (`github.com/pkg/sftp` v1.13.11, `golang.org/x/crypto/ssh`), TypeScript + SolidJS renderer, Wails v3 beta.9, Playwright + `cmd/devharness` + `cmd/e2e-sshd` for e2e.

**Spec:** `.internal/specs/2026-08-21-upload-to-the-active-tab-design.md` (commit `52f8ca27`). Every decision reference below (`D1`…`D9`, `R1`, `R2`) is a section of that spec. Read `§0` and `§6` before Task 1.

## Global Constraints

- **`fsHardTimeout = 30 * time.Second`, `fsLaneCap = 4`** (`internal/ssh/ssh_fsconn.go:115`, `:123`). Never widen either. A write that needs longer than 30 s is a chunk that is too big, not a timeout that is too short.
- **Chunk size: `256 << 10`** (256 KiB), matching tabby's read buffer and comfortably inside the lane's timeout on any link that is alive at all.
- **Every new JSON-RPC result and notification shape gets a JSON Schema in `contracts/`** with `additionalProperties: false` and an explicit `required`, generated renderer types committed, a Go `…_DTOConformsToContract` test **and** a `…_OverTheWireConformsToContract` test. Four shapes here: `files.upload`, `files.uploadCancel`, `files.uploadProgress`, `files.uploadDone`.
- **`bd` for all tracking.** No TodoWrite, no markdown TODO lists.
- **Commit message format** is `AGENTS.md`'s: `<type>(<scope>): <subject> (<bead-id>)`, body in prose explaining what was wrong and why this way.
- **Workers run the unit tests for the files they changed and stop there.** `make ci-full`, the containerized jobs and the e2e suite belong to whoever integrates. The dead-code ratchet is a CI job, not the pre-commit hook — so `internal/transfer` may be unreachable for the few commits between Task 2 and Task 5, and **must** be reachable from `main()` by the end of Task 5.
- **No backward-compatibility shims.** Greenfield; break and refactor freely.
- **A test may not depend on timing.** Wait on an observable state change, never a duration.

---

### Task 1: `FSConn` grows a write half

**Files:**

- Modify: `internal/ssh/ssh_fsconn.go` (the `FSConn` interface at :36, the `fsConn` methods near :495)
- Test: `internal/ssh/ssh_fsconn_test.go`

**Interfaces:**

- Consumes: `fsConn.run(ctx, fn)` — the lane (`:358`), and `fsConn.classify(err)` — the error mapper.
- Produces:
  ```go
  type FSFile interface {
      Write(p []byte) (int, error)  // one lane call
      Close() error                 // one lane call
  }
  type FSConn interface {
      // … existing read methods unchanged …
      Create(path string) (FSFile, error)
      PosixRename(old, new string) error
      Rename(old, new string) error
      Remove(path string) error
  }
  var ErrPosixRenameUnsupported = errors.New("ssh: server does not support posix-rename@openssh.com")
  ```

**Acceptance Criteria:**

- `Create` uses `OpenFile` with `os.O_WRONLY|os.O_CREATE|os.O_EXCL` and fails on an existing path — it does **not** truncate.
- Every `FSFile.Write` and `FSFile.Close` goes through `run`, so none escapes the lane, the watchdog or cancellation.
- A total write far longer than `fsHardTimeout`, made of short chunks, completes — the watchdog is per call, not per transfer.
- `PosixRename` against a server without the extension returns an error satisfying `errors.Is(err, ErrPosixRenameUnsupported)`, distinguishable from every other failure. **`ssh.ErrPosixRenameUnsupported` must wrap `transfer.ErrPosixRenameUnsupported`** so the sink's fallback keys on its own sentinel and neither package imports the other's error. If `internal/transfer` does not exist in your worktree yet, declare `ssh.ErrPosixRenameUnsupported` as a plain sentinel and leave a one-line `// TODO(<bead>): wrap transfer.ErrPosixRenameUnsupported once both land` — the integrator joins them.

- [ ] **Step 1: Write the failing test for exclusive create**

Add to `internal/ssh/ssh_fsconn_test.go` (the file already has a live-server harness; follow its existing helper for standing up a fixture lease):

```go
func TestFSConn_CreateIsExclusiveAndDoesNotTruncate(t *testing.T) {
	c, dir := newTestFSConn(t) // existing helper
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := c.Create(path)
	if err == nil {
		t.Fatal("Create on an existing path must fail; sftp.Client.Create would have truncated it")
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "original" {
		t.Fatalf("Create truncated an existing file: content is now %q", got)
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/ssh/ -run TestFSConn_CreateIsExclusive -v`
Expected: FAIL — `c.Create undefined (type FSConn has no field or method Create)`.

- [ ] **Step 3: Add the interface methods and the file handle**

In `internal/ssh/ssh_fsconn.go`, extend the `FSConn` interface with the four methods and the `FSFile` interface from **Interfaces** above, each carrying a doc comment in the file's existing voice. Then implement:

```go
// fsFile is a write handle on the lease. It exists between lane calls, but
// every call it makes is a lane call — which is the property that matters:
// nothing escapes the watchdog, the classification or the poisoning. Closing
// the lease closes the subsystem, which invalidates this handle and unblocks
// a wedged write.
type fsFile struct {
	c *fsConn
	f *sftp.File
}

func (w *fsFile) Write(p []byte) (int, error) {
	var n int
	err := w.c.run(context.Background(), func() error {
		var err error
		n, err = w.f.Write(p)
		return err
	})
	if err != nil {
		return n, w.c.classify(err)
	}
	return n, nil
}

func (w *fsFile) Close() error {
	err := w.c.run(context.Background(), func() error { return w.f.Close() })
	if err != nil {
		return w.c.classify(err)
	}
	return nil
}

// Create opens path for writing, refusing to replace an existing file.
// sftp.Client.Create is O_RDWR|O_CREATE|O_TRUNC and would silently destroy
// a concurrent transfer's temp file, so OpenFile with explicit flags is the
// call (design D5).
func (c *fsConn) Create(path string) (FSFile, error) {
	var f *sftp.File
	err := c.run(context.Background(), func() error {
		var err error
		f, err = c.sftp.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
		return err
	})
	if err != nil {
		return nil, c.classify(err)
	}
	return &fsFile{c: c, f: f}, nil
}
```

- [ ] **Step 4: Run the test and watch it pass**

Run: `go test ./internal/ssh/ -run TestFSConn_CreateIsExclusive -v`
Expected: PASS.

- [ ] **Step 5: Write the failing test for the long-transfer property**

This is the test that proves D2 — the reason the design does not put a whole upload in one lane call:

```go
func TestFSConn_ManyShortWritesOutliveTheHardTimeout(t *testing.T) {
	// A lease whose watchdog fires quickly, so the test asserts the
	// PROPERTY (per-call, not per-transfer) without waiting 30 seconds.
	c, dir := newTestFSConnWithTimeout(t, 200*time.Millisecond)
	f, err := c.Create(filepath.Join(dir, "big.bin"))
	if err != nil {
		t.Fatal(err)
	}
	chunk := bytes.Repeat([]byte("x"), 4096)
	for i := 0; i < 40; i++ {
		if _, err := f.Write(chunk); err != nil {
			t.Fatalf("write %d failed — the watchdog is timing the transfer, not the call: %v", i, err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}
```

> Note: this deliberately does **not** sleep. It performs many real calls whose sum exceeds the watchdog while each is short. If `newTestFSConnWithTimeout` does not exist, add it beside the existing helper — `newFSConnLane` already takes `hardTimeout` as a parameter (`internal/ssh/ssh_fsconn.go:176`), which is why this is possible at all.

- [ ] **Step 6: Run it**

Run: `go test ./internal/ssh/ -run TestFSConn_ManyShortWrites -race -v`
Expected: PASS with the implementation from Step 3 (it is already per-call). If it FAILS, the implementation put the loop inside one `run` — fix that, do not widen the timeout.

- [ ] **Step 7: Implement the three rename/remove methods with a failing test each**

```go
func TestFSConn_PosixRenameUnsupportedIsDistinguishable(t *testing.T) {
	c, dir := newTestFSConnNoPosixRename(t) // fixture with the extension withheld
	err := c.PosixRename(filepath.Join(dir, "a"), filepath.Join(dir, "b"))
	if !errors.Is(err, ssh.ErrPosixRenameUnsupported) {
		t.Fatalf("the fallback keys on exactly this error; got %v", err)
	}
}
```

```go
func (c *fsConn) PosixRename(old, new string) error {
	err := c.run(context.Background(), func() error { return c.sftp.PosixRename(old, new) })
	if err == nil {
		return nil
	}
	// pkg/sftp reports an unadvertised extension as an unsupported request;
	// the fallback in internal/transfer keys on this and nothing else.
	if isUnsupportedExtension(err) {
		return fmt.Errorf("%w: %v", ErrPosixRenameUnsupported, err)
	}
	return c.classify(err)
}
```

`Rename` and `Remove` are the same one-line shape over `c.sftp.Rename` / `c.sftp.Remove`. Write the paired success test for each — "and on an ordinary server it succeeds" — not only the failure.

- [ ] **Step 8: Run the package tests**

Run: `go test ./internal/ssh/ -race`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/ssh/ssh_fsconn.go internal/ssh/ssh_fsconn_test.go
git commit -m "feat(ssh): the SFTP lease can write, one lane call per chunk (<bead-id>)"
```

---

### Task 2: The sink — `internal/transfer`

**Files:**

- Create: `internal/transfer/transfer.go`, `internal/transfer/sink.go`, `internal/transfer/errors.go`
- Create: `internal/transfer/fsfake_test.go` (model on `internal/filesystem/sftp/fsfake_test.go`)
- Test: `internal/transfer/sink_test.go`

**Interfaces:**

- Consumes: **nothing from Task 1.** `internal/transfer` declares the narrow interface it needs and `ssh.FSConn` satisfies it structurally — the direction `internal/filesystem` already established, where `filesystem` declares `Caller` and `transport` satisfies it (`internal/filesystem/binding.go:62`). This is why Tasks 1 and 2 can be built in parallel by two workers who never open the same file:

  ```go
  // RemoteFS is the write surface this package needs. ssh.FSConn satisfies
  // it; the dependency points this way so the sink can be built and tested
  // against a fake without internal/ssh existing yet.
  type RemoteFS interface {
      Create(path string) (RemoteFile, error)
      PosixRename(old, new string) error
      Rename(old, new string) error
      Remove(path string) error
  }

  type RemoteFile interface {
      Write(p []byte) (int, error)
      Close() error
  }

  // ErrPosixRenameUnsupported is what the fallback keys on. internal/ssh
  // returns an error satisfying errors.Is against THIS sentinel — Task 1's
  // ssh.ErrPosixRenameUnsupported wraps it — so neither package imports the
  // other's error.
  var ErrPosixRenameUnsupported = errors.New("transfer: server does not support posix-rename@openssh.com")
  ```

- Produces:
  ```go
  type Decision string
  const (
      Overwrite Decision = "overwrite"
      KeepBoth  Decision = "keepBoth"
      Skip      Decision = "skip"
  )

  type Upload struct {
      DestDir  string
      Name     string
      Size     int64
      OnExists Decision
  }

  type Outcome struct {
      State     string   // "written" | "skipped"
      FinalName string
      Stranded  []string
  }

  type Sink interface {
      Put(ctx context.Context, u Upload, r io.Reader, progress func(total int64)) (Outcome, error)
  }

  func NewSink(conn ssh.FSConn, opts ...Option) Sink
  ```

**Acceptance Criteria:**

- Every row of spec `§6`'s table has a test, including `Remove` failing, `Close` failing after a complete write, and both fallback-window losses.
- `Stranded` is a slice and carries **two** paths when a fallback leaves both a temp and a backup.
- Cancellation between `rename(dest→bak)` and `rename(temp→dest)` does **not** abandon the promote (spec `§6`, the last row).
- `KeepBoth` resolves by `O_EXCL`, retries the next suffix on `EEXIST`, and fails with a typed error after 32 attempts.
- A reader that delivers fewer or more bytes than `Size` fails the transfer and leaves `dest` untouched.
- Paired success: an ordinary server writes the file; a server without `posix-rename` also writes it and leaves no `.nocx-bak` behind.

- [ ] **Step 1: Write the fake**

`internal/transfer/fsfake_test.go` implements `ssh.FSConn` with programmable failures: `failCreate`, `failWriteAfter(n)`, `failClose`, `failRemove`, `noPosixRename`, `failRenameAt(step)`, plus an in-memory file map so a test can assert what exists afterwards. Model the shape on `internal/filesystem/sftp/fsfake_test.go`.

- [ ] **Step 2: Write the first failing test — the happy path**

```go
func TestPut_WritesThroughTempAndPromotes(t *testing.T) {
	fs := newFakeFS()
	s := transfer.NewSink(fs)
	out, err := s.Put(context.Background(),
		transfer.Upload{DestDir: "/home/u", Name: "a.txt", Size: 5, OnExists: transfer.Overwrite},
		strings.NewReader("hello"), func(int64) {})
	if err != nil {
		t.Fatal(err)
	}
	if out.State != "written" || out.FinalName != "a.txt" {
		t.Fatalf("got %+v", out)
	}
	if got := fs.content("/home/u/a.txt"); got != "hello" {
		t.Fatalf("destination holds %q", got)
	}
	if left := fs.matching("*.nocx-upload-*"); len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
}
```

- [ ] **Step 3: Run it**

Run: `go test ./internal/transfer/ -run TestPut_WritesThroughTemp -v`
Expected: FAIL — package does not compile, `NewSink` undefined.

- [ ] **Step 4: Implement `Put`**

```go
func (s *sink) Put(ctx context.Context, u Upload, r io.Reader, progress func(int64)) (Outcome, error) {
	name, err := s.resolveName(u)           // §5.3 KeepBoth / Skip / Overwrite
	if err != nil || name == "" {           // "" means Skip
		return Outcome{State: "skipped"}, err
	}
	dest := s.join(u.DestDir, name)
	temp := dest + ".nocx-upload-" + s.nonce()

	f, err := s.conn.Create(temp)           // O_EXCL — Task 1
	if err != nil {
		return Outcome{}, err
	}

	written, copyErr := s.copy(ctx, f, r, progress)
	closeErr := f.Close()
	if copyErr == nil && closeErr == nil && written != u.Size {
		copyErr = &ErrSizeMismatch{Declared: u.Size, Got: written}
	}
	if copyErr != nil || closeErr != nil {
		// The temp is ours to remove. Remove is an external call and can
		// itself fail — when it does the path is REPORTED, never dropped.
		return Outcome{Stranded: s.tryRemove(temp)}, errors.Join(copyErr, closeErr)
	}
	return s.promote(dest, temp)
}
```

`copy` reads in 256 KiB chunks, calls `progress(total)` after each successful write, and checks `ctx.Err()` between chunks — **not** inside a chunk, so the lane call is never abandoned mid-flight.

- [ ] **Step 5: Run it and watch it pass**

Run: `go test ./internal/transfer/ -run TestPut_WritesThroughTemp -v`
Expected: PASS.

- [ ] **Step 6: Write the failing test for the fallback, then implement `promote`**

```go
func TestPromote_FallbackKeepsTheOldContentUnderBak(t *testing.T) {
	fs := newFakeFS()
	fs.noPosixRename = true
	fs.put("/home/u/a.txt", "old")
	fs.failRenameAt(2) // dest→bak succeeds; temp→dest fails

	s := transfer.NewSink(fs)
	out, err := s.Put(context.Background(),
		transfer.Upload{DestDir: "/home/u", Name: "a.txt", Size: 3, OnExists: transfer.Overwrite},
		strings.NewReader("new"), func(int64) {})

	if err == nil {
		t.Fatal("a failed promote must be an error")
	}
	if len(out.Stranded) != 2 {
		t.Fatalf("a failed fallback strands BOTH the backup and the temp; got %v", out.Stranded)
	}
	var bak string
	for _, p := range out.Stranded {
		if strings.Contains(p, ".nocx-bak-") {
			bak = p
		}
	}
	if bak == "" {
		t.Fatal("the outcome must NAME the path holding the old content")
	}
	if fs.content(bak) != "old" {
		t.Fatal("the backup must hold the previous content — this is the whole reason we do not unlink first")
	}
}
```

```go
func (s *sink) promote(dest, temp string) (Outcome, error) {
	switch err := s.conn.PosixRename(temp, dest); {
	case err == nil:
		return Outcome{State: "written", FinalName: path.Base(dest)}, nil
	case !errors.Is(err, ssh.ErrPosixRenameUnsupported):
		return Outcome{Stranded: s.tryRemove(temp)}, err
	}

	// v3 rename refuses an existing destination (nocx-340t), so the old file
	// moves ASIDE rather than away: for the whole window its content is on
	// disk under a named path, which is what tabby's unlink-first does not
	// give you.
	bak := dest + ".nocx-bak-" + s.nonce()
	if err := s.conn.Rename(dest, bak); err != nil {
		if !isNotFound(err) { // a missing destination is not a collision
			return Outcome{Stranded: s.tryRemove(temp)}, err
		}
		bak = ""
	}

	// From here the destination may be missing. Cancellation is REFUSED in
	// this window (design §6): "I pressed cancel" must never be how the
	// destination goes missing.
	if err := s.conn.Rename(temp, dest); err != nil {
		return Outcome{Stranded: present(bak, temp)}, err
	}
	if bak == "" {
		return Outcome{State: "written", FinalName: path.Base(dest)}, nil
	}
	if err := s.conn.Remove(bak); err != nil {
		// Success WITH a stranded backup: the new file is in place and the
		// old one is still on disk. Reporting it is the honest outcome.
		return Outcome{State: "written", FinalName: path.Base(dest), Stranded: []string{bak}}, nil
	}
	return Outcome{State: "written", FinalName: path.Base(dest)}, nil
}
```

- [ ] **Step 7: Run it**

Run: `go test ./internal/transfer/ -run TestPromote -v`
Expected: PASS.

- [ ] **Step 8: Write the remaining `§6` rows**

One test per row, named after the row. Do not batch them into a table test that shares a fake — each row asserts a different post-state and a shared fake hides that. Include the two paired success assertions from **Acceptance Criteria**.

- [ ] **Step 9: Run the package with the race detector**

Run: `go test ./internal/transfer/ -race -count=1`
Expected: PASS.

- [ ] **Step 10: Commit**

```bash
git add internal/transfer/
git commit -m "feat(transfer): a remote file is written through a temp and promoted, and every partial failure names what it left (<bead-id>)"
```

---

### Task 3: The `Uploader` seam and `Handle.Upload`

**Files:**

- Modify: `internal/filesystem/filesystem.go` (add `Uploader`), `internal/filesystem/binding.go` (record the capability; add `Upload` to `Handle` and `handle`), `internal/filesystem/errors.go` (add `ErrUploadUnsupported`)
- Modify: `internal/filesystem/sftp/sftp.go` (implement `Uploader`)
- Test: `internal/filesystem/binding_test.go`, `internal/filesystem/sftp/sftp_test.go`

**Interfaces:**

- Consumes: `transfer.Sink`, `transfer.Upload`, `transfer.Outcome` from Task 2.
- Produces:
  ```go
  // Uploader is the optional write seam. A provider that can write a remote
  // file implements it; local deliberately does not, which is R1.
  type Uploader interface {
      Sink() transfer.Sink
  }

  type Handle interface {
      Root(ctx context.Context) (Root, error)
      List(ctx context.Context, path string, page Page) (Listing, error)
      Read(ctx context.Context, path string, maxBytes int64) (Content, error)
      Watch(ctx context.Context, paths []string) (WatchMode, error)
      Upload(ctx context.Context, u transfer.Upload, r io.Reader, progress func(int64)) (transfer.Outcome, error)
  }

  type ErrUploadUnsupported struct{ BindingID string }
  ```
- `Registry.Register` gains a parameter: `Register(p Provider, sessionID session.ID, endpointID string, up transfer.Sink) (string, error)` — `nil` for a local binding.

**Acceptance Criteria:**

- `handle.Upload` on a binding registered with a `nil` sink returns `*ErrUploadUnsupported` without touching any provider.
- `handle.Upload` after `release` returns `ErrHandleReleased`, like every other handle method.
- The sftp provider implements `Uploader`; `local.Provider` does not, asserted by a compile-time check in the test (`var _ filesystem.Uploader = (*sftp.Provider)(nil)` and a negative assertion for local).

- [ ] **Step 1: Write the failing test**

```go
func TestHandle_UploadIsRefusedOnALocalBinding(t *testing.T) {
	reg := filesystem.New()
	id, err := reg.Register(stubProvider{}, sess.ID(), "", nil) // nil sink = local
	if err != nil {
		t.Fatal(err)
	}
	h, release, err := reg.Acquire(id, ownerOf(sess.ID()))
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	_, err = h.Upload(context.Background(),
		transfer.Upload{DestDir: "/tmp", Name: "a", Size: 1}, strings.NewReader("x"), func(int64) {})

	var unsupported *filesystem.ErrUploadUnsupported
	if !errors.As(err, &unsupported) {
		t.Fatalf("R1: a local binding has no write seam, so the refusal is structural; got %v", err)
	}
}
```

- [ ] **Step 2: Run it**

Run: `go test ./internal/filesystem/ -run TestHandle_UploadIsRefused -v`
Expected: FAIL — `too many arguments in call to reg.Register`.

- [ ] **Step 3: Implement**

Add `sink transfer.Sink` to `Binding`, take it in `Register`, and add to `handle`:

```go
// Upload writes one file through the binding's sink. A binding with no sink
// is a local one, and the refusal is the design's rule R1 expressed as a nil
// field rather than as a condition somebody must remember to check.
func (h *handle) Upload(ctx context.Context, u transfer.Upload, r io.Reader, progress func(int64)) (transfer.Outcome, error) {
	drop, err := h.begin() // returns (func(), error) — see binding.go:303
	if err != nil {
		return transfer.Outcome{}, err
	}
	defer drop()
	if h.b.sink == nil {
		return transfer.Outcome{}, &ErrUploadUnsupported{BindingID: h.b.id}
	}
	return h.b.sink.Put(ctx, u, r, progress)
}
```

Update every existing `Register` call site (grep: `reg.Register(`) to pass `nil`.

- [ ] **Step 4: Run it**

Run: `go test ./internal/filesystem/... -race`
Expected: PASS.

- [ ] **Step 5: Implement `Uploader` on the sftp provider**

`sftp.Provider` already holds its `FSConn`. Add:

```go
// Sink returns the write half of this provider's lease. It is the optional
// Uploader seam (design D7): implementing it is what makes a remote binding
// writable, and local's silence is what makes a local one refuse.
func (p *Provider) Sink() transfer.Sink { return transfer.NewSink(p.fs) }
```

with a compile-time assertion in `sftp_test.go`:

```go
var _ filesystem.Uploader = (*sftp.Provider)(nil)

func TestLocalProviderIsNotAnUploader(t *testing.T) {
	if _, ok := any(local.New()).(filesystem.Uploader); ok {
		t.Fatal("local must NOT implement Uploader — R1 depends on it not doing so")
	}
}
```

- [ ] **Step 6: Run and commit**

Run: `go test ./internal/filesystem/... -race`

```bash
git add internal/filesystem/
git commit -m "feat(filesystem): a binding can write only when its provider carries a sink (<bead-id>)"
```

---

### Task 4: Wire the seam — `internal/capability` and `internal/app`

**Files:**

- Modify: `internal/capability/files.go:87-100` (`OpenBinding`), and the `FilesystemBindingService` interface at `:117`
- Modify: `internal/app/app.go:1271-1300` (`filesystemProviderFactory`)
- Test: `internal/capability/files_test.go`, `internal/app/app_test.go`

**Interfaces:**

- Consumes: `filesystem.Uploader`, `filesystem.Registry.Register` (5-arg) from Task 3.
- Produces: nothing new on the wire; this is the wiring that makes Task 2's package reachable from `main()`.

**Acceptance Criteria:**

- `OpenBinding` type-asserts `filesystem.Uploader` **in the same place** it already asserts `filesystemEndpointAttester`, before `Register`.
- A remote session's binding is registered with a non-nil sink; a local session's with `nil`.
- `deadcode -tags gtk3 -whylive 'github.com/shady2k/nocx/internal/transfer.sink.Put' ./...` prints a path from `main`.

- [ ] **Step 1: Write the failing test**

```go
func TestOpenBinding_RemoteProviderContributesASink(t *testing.T) {
	// factory returns a provider that implements Uploader
	svc := newTestOpenService(t, func(session.Session, string) (filesystem.Provider, error) {
		return uploadableProvider{}, nil
	})
	bid, _, err := svc.OpenBinding(context.Background(), remoteSession, "")
	if err != nil {
		t.Fatal(err)
	}
	h, release, _ := svc.Acquire(bid, ownerOfEverything{})
	defer release()
	_, err = h.Upload(context.Background(),
		transfer.Upload{DestDir: "/home/u", Name: "a.txt", Size: 1, OnExists: transfer.Overwrite},
		strings.NewReader("x"), func(int64) {})
	var unsupported *filesystem.ErrUploadUnsupported
	if errors.As(err, &unsupported) {
		t.Fatal("the provider implements Uploader; OpenBinding did not pick the sink up")
	}
}
```

- [ ] **Step 2: Run it, watch it fail, implement**

```go
	endpointID := ""
	if a, ok := provider.(filesystemEndpointAttester); ok {
		endpointID = a.EndpointID()
	}
	// The write seam is resolved HERE, beside the attestation, because this
	// is the last moment the provider is in hand: Binding.provider is
	// unexported and Acquire returns a Handle, so nothing downstream can
	// perform this assertion (design D7).
	var sink transfer.Sink
	if u, ok := provider.(filesystem.Uploader); ok {
		sink = u.Sink()
	}
	bid, err := s.reg.Register(provider, sess.ID(), endpointID, sink)
```

Note that `endpointAttestedProvider` in `app.go:1305` embeds `filesystem.Provider`; embedding forwards `Sink()` automatically, so no change is needed there — **but write the test that proves it**, because a future change to that wrapper would silently drop the capability.

- [ ] **Step 3: Run the reachability probe**

```bash
deadcode -tags gtk3 -whylive 'github.com/shady2k/nocx/internal/transfer.sink.Put' ./...
```

Expected: a path beginning at `main`. Contrast probe on an unwired sibling in the same package must print the reflection answer — run both, and paste both into the bead's close reason. The `-filter` form is not evidence here.

- [ ] **Step 4: Commit**

```bash
git add internal/capability/ internal/app/
git commit -m "feat(app): the remote provider's sink reaches the binding, so the transfer package is live (<bead-id>)"
```

---

### Task 5: `files.upload` and `files.uploadCancel` on the control plane

**Files:**

- Create: `internal/transport/ws_upload.go`, `contracts/files.upload.schema.json`, `contracts/files.uploadCancel.schema.json`
- Modify: `internal/transport/ws_files.go` (method registration), `internal/capability/files.go` (`FilesystemBindingService` unchanged — `Handle` already carries `Upload`)
- Test: `internal/transport/ws_upload_test.go`

**Interfaces:**

- Consumes: `filesystem.Handle.Upload`, `filesystem.ErrUploadUnsupported`, `transfer.Decision`.
- Produces: a per-session transfer registry:
  ```go
  type transferRegistry struct{ /* transferID → *runningTransfer */ }
  func (r *transferRegistry) start(sessionID session.ID, t *runningTransfer) string
  func (r *transferRegistry) cancel(transferID string) bool
  func (r *transferRegistry) cancelSession(sessionID session.ID)   // D8
  ```

**Acceptance Criteria:**

- `files.upload` with a `bindingId` whose binding is local answers the typed refusal — the R1 test.
- The params schema has **no** `sourcePath` and **no** `source` field, and a request carrying either is rejected — the R2 test.
- `destDir` is validated by the existing `validateFSPath`; `name` must be one path component — non-empty, no `/` or `\`, not `.`, not `..`, within the provider's name bound. Each rejection is `-32602` before anything is stat'd.
- With no `onExists` and an existing destination, the result is `{"collision":"exists"}` and nothing was created.
- `files.close` and session teardown **cancel** running transfers rather than blocking on them (D8) — asserted by a test that closes a binding mid-transfer and observes the transfer end, with `files.close` returning promptly.

- [ ] **Step 1: Write the R2 schema test first**

```go
func TestFilesUpload_RejectsAnythingNamingASource(t *testing.T) {
	for _, params := range []string{
		`{"bindingId":"b","destDir":"/tmp","name":"a","size":1,"sourcePath":"/etc/passwd"}`,
		`{"bindingId":"b","destDir":"/tmp","name":"a","size":1,"source":"path"}`,
	} {
		resp := callRaw(t, "files.upload", params)
		if resp.Error == nil || resp.Error.Code != -32602 {
			t.Fatalf("R2: the renderer may never name a source; %s was accepted", params)
		}
	}
}
```

The rejection comes from `json.Decoder.DisallowUnknownFields` on the params struct — write it that way, so the guarantee is structural rather than a list of forbidden names somebody must maintain.

- [ ] **Step 2: Run it, then write the handler**

Follow `filesBindingHandlers.handleList` (`internal/transport/ws_files.go:539`) exactly: the same `h.op.Run(...)` wrapper, the same `svc.Acquire(params.BindingID, state)` authorisation with `defer release()`, the same `filesErrorCode(err)` mapping. Add `*filesystem.ErrUploadUnsupported` to `filesErrorCode`'s `-32602` arm.

The handler resolves the collision, then **starts the transfer and returns** — it does not hold the binding guard for the transfer's lifetime (D8). The running transfer holds its own `FSConn` lease, acquired by the sink.

- [ ] **Step 3: Write the R1 test**

```go
func TestFilesUpload_RefusesALocalBinding(t *testing.T) {
	bid := openLocalBinding(t)
	resp := call(t, "files.upload", filesUploadParams{BindingID: bid, DestDir: "/tmp", Name: "a", Size: 1})
	if resp.Error == nil {
		t.Fatal("R1: a tab with no remote cannot be uploaded to — including a hand-typed ssh, which is KindLocal")
	}
}
```

- [ ] **Step 4: Write the path-validation tests**

One test per rejection: `name` containing `/`, containing `\`, equal to `.`, equal to `..`, empty, over the bound. Plus the paired success — an ordinary name is accepted.

- [ ] **Step 5: Write the D8 test**

Close the binding while a transfer is running; assert `files.close` returns without waiting for the transfer, and that the transfer ends `cancelled`.

- [ ] **Step 6: Write the contracts and both conformance tests**

`contracts/files.upload.schema.json` describes the three-outcome union from spec `§5.3`. Run `npm run contracts:check`.

- [ ] **Step 7: Run and commit**

```bash
go test ./internal/transport/ -run TestFilesUpload -race
git add internal/transport/ws_upload.go internal/transport/ws_upload_test.go contracts/ frontend/src/generated/
git commit -m "feat(transport): files.upload names where a file lands and never where it comes from (<bead-id>)"
```

---

### Task 6: `POST /upload/{ticket}`

**Files:**

- Modify: `internal/transport/ws.go:1082` (register the route), `internal/transport/ws_upload.go`
- Test: `internal/transport/ws_upload_http_test.go`

**Interfaces:**

- Consumes: the transfer registry from Task 5, `s.origins` (`OriginPolicy`).
- Produces: `func (s *WSServer) handleUpload(w http.ResponseWriter, r *http.Request)`.

**Acceptance Criteria:**

- The four-state table of spec `§5.4` is implemented exactly: unknown → `410`, minted-unclaimed → body read, claimed-running → `409` with the first claimant untouched, claimed-finished → `410`.
- Expiry cancels the transfer **at expiry**, on the mint-side timer; a late `POST` therefore finds an unknown ticket. There is no code path where `410` both means "names nothing" and "cancel what it names".
- Missing or mismatched `Content-Length` is `400` **before** the body is read.
- A short body fails the transfer, removes the temp, and leaves `dest` untouched. A long body is cut at the bound and fails the same way.
- The handler sets its own header deadline and a per-read stall deadline; the shared `http.Server` keeps `ReadHeaderTimeout: 0` for `/session`.
- A request from a disallowed origin is refused by the same `OriginPolicy` that guards `/session`.
- Tickets are ≥128 bits from `crypto/rand`, and no ticket value appears in any log line or error string — asserted by a test that runs a transfer with a capturing logger and greps the output.

- [ ] **Step 1: Write the state-table test first**

```go
func TestUploadEndpoint_UnknownTicketIsGone(t *testing.T) {
	srv := newTestServer(t)
	resp := post(t, srv, "/upload/deadbeefdeadbeefdeadbeefdeadbeef", "x")
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("unknown ticket names no transfer: want 410, got %d", resp.StatusCode)
	}
}

func TestUploadEndpoint_SecondClaimWhileRunningIs409AndLeavesTheFirstAlone(t *testing.T) {
	srv, ticket, gate := newTestServerWithHeldTransfer(t) // gate blocks the sink mid-write
	second := post(t, srv, "/upload/"+ticket, "y")
	if second.StatusCode != http.StatusConflict {
		t.Fatalf("want 409, got %d", second.StatusCode)
	}
	gate.release()
	if out := awaitDone(t, srv); out.State != "written" {
		t.Fatalf("the FIRST claimant must keep its transfer; got %+v", out)
	}
}

func TestUploadEndpoint_ClaimAfterCompletionIsGone(t *testing.T) {
	srv, ticket := newTestServerWithTicket(t, 5)
	if r := post(t, srv, "/upload/"+ticket, "hello"); r.StatusCode != http.StatusOK {
		t.Fatalf("first POST: %d", r.StatusCode)
	}
	awaitDone(t, srv)
	if r := post(t, srv, "/upload/"+ticket, "hello"); r.StatusCode != http.StatusGone {
		t.Fatalf("a finished ticket is gone, not conflicted: got %d", r.StatusCode)
	}
}

func TestUploadEndpoint_ExpiredTicketWasAlreadyCancelledAndReadsAsUnknown(t *testing.T) {
	// Zero TTL through the SAME option production uses, so expiry has
	// already happened when the request arrives. No sleep: a test may not
	// depend on timing.
	srv, ticket := newTestServerWithTicket(t, 5, transport.WithUploadTicketTTL(0))
	if out := awaitDone(t, srv); out.State != "cancelled" {
		t.Fatalf("expiry cancels the transfer AT EXPIRY; got %+v", out)
	}
	if r := post(t, srv, "/upload/"+ticket, "hello"); r.StatusCode != http.StatusGone {
		t.Fatalf("want 410, got %d", r.StatusCode)
	}
}
```

Note what the last test pins: `410` never means "cancel what this names". Expiry already did that on the mint-side timer, so by the time a late `POST` arrives the ticket is simply unknown. That is what removed the first draft's contradiction.

- [ ] **Step 2: Run, implement, run**

Run: `go test ./internal/transport/ -run TestUploadEndpoint -race -v`

- [ ] **Step 3: Write the body-bound tests**

Short body, long body, absent `Content-Length`, mismatched `Content-Length`. Each asserts the destination is untouched afterwards.

- [ ] **Step 4: Write the no-secrets-in-logs test**

- [ ] **Step 5: Commit**

```bash
git add internal/transport/
git commit -m "feat(transport): bytes reach the sink over a streamed POST behind a one-shot ticket (<bead-id>)"
```

---

### Task 7: Progress and completion notifications

**Files:**

- Modify: `internal/transport/ws_upload.go`
- Create: `contracts/files.uploadProgress.schema.json`, `contracts/files.uploadDone.schema.json`
- Test: `internal/transport/ws_upload_notify_test.go`

**Interfaces:**

- Consumes: `wconn.TryNotify` and the subscriber resolution used by `files.changed` (`internal/transport/ws_files.go:939`).
- Produces: retained-done storage keyed by session, flushed on attach.

**Acceptance Criteria:**

- `files.uploadProgress` is emitted to the binding's session's **current** subscriber, resolved at emit time, and dropped when there is none. Coalesced: at most one in flight per transfer.
- `files.uploadDone` is **retained** when there is no subscriber and delivered on re-attach, the way `files.changed`'s dirty set is (`ws_files.go:919`, `:932`, `:956`). Retention is bounded and cleared on delivery.
- A test drops the WebSocket mid-transfer, reattaches after the transfer finished, and observes `files.uploadDone` — the test that proves the UI cannot be left saying "uploading" forever.
- **On a `written` outcome the destination directory is invalidated**, so the existing `files.changed` path re-lists it and the new row appears with nobody pressing anything (spec `§5.5`, "Refresh"). Asserted: a watched destination directory produces a `files.changed` for that path after a successful upload, and does **not** after a `skipped` or `failed` one.
- Over-the-wire conformance for **both** notifications, off the real socket.

- [ ] **Step 1: Write the reconnect test first** — it is the reason this task exists.

```go
func TestUploadDone_SurvivesAReconnectWithNoSubscriber(t *testing.T) {
	// start a transfer, drop the connection, let it finish, reattach
	// assert files.uploadDone arrives after attach
}
```

- [ ] **Step 2: Run, implement, run**

- [ ] **Step 3: Write the progress-is-lossy test** — assert that dropping every progress notification is not an error and does not affect the outcome.

- [ ] **Step 4: Contracts, generated types, both conformance tests each**

Run: `npm run contracts:check`

- [ ] **Step 5: Commit**

---

### Task 8: Source tickets — the picker and the Wails drop

**Files:**

- Create: `contracts/dialog.openFileForUpload.schema.json`
- Modify: `internal/transport/ws_dialog.go`, `internal/transport/ws_upload.go` (source-ticket store), `internal/app/app.go` (Wails window options + the `FilesDropped` handler)
- Test: `internal/transport/ws_dialog_test.go`, `internal/app/app_test.go`

**Interfaces:**

- Produces:
  - `dialog.openFileForUpload` → `{"sourceTicket":"…","name":"…","size":N}` or `{"sourceTicket":""}` on cancel. **Never a path.**
  - A `filesDropped` notification carrying the same shape plus the drop target's `data-session-id`.

**Acceptance Criteria:**

- A source ticket cannot be minted from the wire: the only mint sites are the native picker handler and the Wails drop handler, asserted by a test that calls every wire method and finds no new ticket.
- `dialog.openFile` is unchanged for its existing callers; the new method is a sibling, not a rewrite.
- The renderer never learns the source directory — only a display name and a size.
- The Wails drop path calls `files.upload` with a `bindingId` **through the normal authorised route**; it does not resolve a destination by itself (the finding that the first draft created a second addressing path).
- `EnableFileDrop: true` is set on the window; a regression test asserts tab reordering still works with it on.

- [ ] **Step 1: Write the "cannot be minted from the wire" test first**

- [ ] **Step 2: Implement the source-ticket store** — same shape as the sink ticket: `crypto/rand`, one-shot, TTL, never logged. It resolves to an `os.Open`-able path held only backend-side.

- [ ] **Step 3: Add `EnableFileDrop` and the drop handler**

```go
// Wails delivers the dropped filenames AND the target element's attributes
// (application.DropTargetDetails). The terminal element carries its session
// id, so the backend knows which tab was dropped on. It mints a source
// ticket and tells the renderer — which then calls files.upload with its own
// bindingId, so the native gesture goes through the SAME authorisation as
// every other caller (design §5.5).
```

- [ ] **Step 4: Write the tab-drag regression test**

Wails beta.9's runtime acts only on drags whose `types` contain `Files` (`window.ts:712`) and the strip drags `application/x-nocx-tab` (`frontend/src/layout/strip-drag.ts:15`), so this is expected to pass on the first run. Write it anyway — it is the assertion that keeps a future runtime bump from breaking tab reordering silently.

- [ ] **Step 5: Commit**

---

### Task 9: The renderer's upload client and store

**Files:**

- Create: `frontend/src/files/upload-client.ts`, `frontend/src/files/upload-store.ts`
- Test: `frontend/src/files/upload-client.test.ts`, `frontend/src/files/upload-store.test.ts`

**Interfaces:**

- Consumes: `Dispatcher` (see `frontend/src/dialog-client.ts` for the house shape), and the generated types from Tasks 5–8.
- Produces:

  ```ts
  export type UploadRequest = {
    bindingId: string
    destDir: string
    name: string
    size: number
    sourceTicket?: string
    onExists?: 'overwrite' | 'keepBoth' | 'skip'
  }

  export class UploadClient {
    upload(req: UploadRequest): Promise<FilesUpload>
    cancel(transferId: string): Promise<void>
    /** Streams a File to the url the upload result returned. */
    sendBody(url: string, file: File): Promise<void>
  }

  export type TransferState = {
    transferId: string
    name: string
    total: number
    bytes: number
    bytesPerSecond: number
    phase: 'running' | 'done' | 'failed' | 'cancelled' | 'skipped'
    stranded: string[]
  }
  ```

**Acceptance Criteria:**

- In-flight state is derived from `files.upload`'s result and `files.uploadDone` — **never** from having seen a progress notification, which may never arrive.
- A transfer whose progress notifications are all dropped still resolves when `files.uploadDone` arrives.
- Speed is derived in the store from successive progress samples; the wire carries bytes and total only.
- `sendBody` sets `Content-Length` to the declared size and rejects on any non-2xx status, surfacing the status so a `409`/`410` is distinguishable from a network failure.

- [ ] **Step 1: Write the failing test for the property that matters**

`frontend/src/files/upload-store.test.ts`:

```ts
it('resolves a transfer that never emitted a single progress notification', () => {
  const store = createUploadStore()
  store.started({ transferId: 't1', name: 'a.txt', total: 100 })
  expect(store.get('t1')?.phase).toBe('running')

  // Not one files.uploadProgress arrives — the reconnect case.
  store.done({ transferId: 't1', outcome: 'written', finalName: 'a.txt', stranded: [] })

  expect(store.get('t1')?.phase).toBe('done')
})
```

- [ ] **Step 2: Run it and watch it fail**

Run: `cd frontend && npx vitest run src/files/upload-store.test.ts`
Expected: FAIL — `createUploadStore is not defined`.

- [ ] **Step 3: Implement `upload-store.ts`**

A plain store keyed by `transferId`. `started` seeds `phase: 'running'` from the RPC result; `progress` updates `bytes` and recomputes `bytesPerSecond` from the previous sample; `done` sets the terminal phase. There is no code path where a missing progress sample changes the phase.

- [ ] **Step 4: Run it and watch it pass**

Run: `cd frontend && npx vitest run src/files/upload-store.test.ts`
Expected: PASS.

- [ ] **Step 5: Write the failing test for `sendBody`'s status handling**

```ts
it('surfaces a 409 as a distinguishable failure, not a network error', async () => {
  const fetchMock = vi.fn().mockResolvedValue(new Response('', { status: 409 }))
  const client = new UploadClient(dispatcherStub, fetchMock)
  await expect(client.sendBody('/upload/t', new File(['x'], 'a.txt'))).rejects.toMatchObject({
    status: 409,
  })
})
```

- [ ] **Step 6: Run, implement `upload-client.ts`, run again**

Run: `cd frontend && npx vitest run src/files/upload-client.test.ts`

- [ ] **Step 7: Commit**

```bash
git add frontend/src/files/upload-client.ts frontend/src/files/upload-store.ts frontend/src/files/upload-*.test.ts
git commit -m "feat(frontend): a transfer's state comes from its result and its done, never from a progress sample (<bead-id>)"
```

---

### Task 10: The gestures

**Files:**

- Modify: the terminal pane component (grep `xterm` in `frontend/src/` for the element that owns the viewport) — add the drop target, `data-file-drop-target`, and `data-session-id`
- Modify: `frontend/src/files/files-view.tsx` — the Upload action
- Create: the collision dialog — **in `frontend/src/ui/` if the kit has no near-fit**
- Test: alongside each

**Interfaces:**

- Consumes: `UploadClient`, `uploadStore` from Task 9.

**Acceptance Criteria:**

- A drop on a **remote** tab starts an upload into that tab's cwd.
- A drop on a **local** tab inserts the path into the command line and starts no transfer (D9).
- The collision dialog offers Overwrite / Keep both / Skip with an apply-to-all checkbox, and the choice is applied to every remaining file of a multi-file drop.
- The Upload action goes into the panel's **overflow**, not the header: `nocx-a8cz` owns how that header overflows and this task must not add a seventh button to a header that cannot hold six.

- [ ] **Step 1: Read the kit before building anything**

```bash
sed -n '1,200p' frontend/src/ui/README.md
ls frontend/src/ui/
```

A modal with three choices and a checkbox is very likely already a kit component, or one typed `data-*` variant away from being one. If it is at 90% fit, add the variance there. Building a bespoke `<div class="st-...">` with its own colours is the defect two epics (`nocx-pp3y`, `nocx-v0ai`) spent themselves unwinding. If it is genuinely new: one module in `ui/`, one CSS file in `styles/components/`, a stable identity class, a test, and a row in the README table.

- [ ] **Step 2: Write the failing test for the local-tab rule**

```tsx
it('a drop on a local tab inserts the path and starts no transfer', async () => {
  const upload = vi.fn()
  const { getByTestId } = render(() => (
    <TerminalPane session={localSession} uploadClient={{ upload }} />
  ))

  fireEvent.drop(getByTestId('terminal-viewport'), {
    dataTransfer: dataTransferWithPath('/home/u/report.pdf'),
  })

  expect(upload).not.toHaveBeenCalled()
  expect(insertedIntoCommandLine()).toBe('/home/u/report.pdf')
})
```

- [ ] **Step 3: Run it, watch it fail, implement the drop handler**

Run: `cd frontend && npx vitest run src/terminal-pane.test.tsx`
The handler branches on the session kind that the pane already holds; `KindLocal` inserts, remote uploads. One surface, one input, the meaning the context gives it.

- [ ] **Step 4: Write the failing test for apply-to-all**

```tsx
it('applies one collision answer to every remaining file of a multi-file drop', async () => {
  // three files, all colliding; answer Overwrite with applyToAll on the first
  // assert files.upload was called three times, each carrying onExists: 'overwrite',
  // and the dialog was shown exactly once
})
```

- [ ] **Step 5: Run, implement, run**

Run: `cd frontend && npx vitest run src/files/`

- [ ] **Step 6: Add the Upload action to the panel overflow**

Not the header. Record the dependency on `nocx-a8cz` in this task's bead.

- [ ] **Step 7: Run the frontend gates and commit**

```bash
cd frontend && npx vitest run && npx tsc --noEmit
git add frontend/src/
git commit -m "feat(frontend): dropping a file on a remote tab uploads it, on a local one it types the path (<bead-id>)"
```

---

### Task 11: The end-to-end check

**Files:**

- Create: `e2e/upload.spec.ts`
- Test: itself

**Acceptance Criteria (this is the epic's DONE WHEN):**

- Against `cmd/e2e-sshd`: open an SSH tab, drop a file onto its terminal, assert the file appears in the Files tree **and** its bytes on the far side match.
- It waits on observable state — the row appearing, the done notification — never on a duration.

- [ ] **Step 1: Read `e2e/connection-password.spec.ts:112-160`** for `startSshd`, the existing fixture helper.
- [ ] **Step 2: Write the spec.** Construct the `File` and `DataTransfer` in the page (`e2e/tab-decoration.spec.ts:80` shows the `DataTransfer` idiom already in use here).
- [ ] **Step 3: Run it in the container**

```bash
PW_PROJECTS=chromium e2e/run-in-container.sh e2e/upload.spec.ts
```

Remember the container's failure set is not CI's: confirm in CI, and never "fix" a test that is only red in the container without checking which one is lying.

- [ ] **Step 4: Commit**

---

### Task 12: The ADR and the backlog

**Files:**

- Create: `docs/decisions/0036-an-http-upload-route-beside-the-websocket.md` (next free number; `docs/decisions/` currently ends at two files numbered `0035`, a pre-existing collision — do not "fix" it here)
- Modify: `docs/decisions/INDEX.md`

**Acceptance Criteria:**

- The ADR has Context / Decision / Rationale / Consequences and states plainly that it crosses AD-1's plane allocation, why the data plane was rejected, and what it costs.
- `nocx-9le.5` rescoped to upload only, acceptance = the plan's **Goal** plus Task 11; raised from `P3`.
- New siblings under `nocx-9le`: **download**, and **directory upload** (blocked by this epic — same files).
- `nocx-a8cz` gains a `blocks` edge onto Task 10's bead.
- The epic carries a `discovered-from` edge back to `nocx-0vc5l`.

- [ ] **Step 1: Write the ADR**

Four sections. Context: AD-1 allocates two planes on one WebSocket and forbids PTY bytes in JSON-RPC; an upload is neither PTY nor a small control message. Decision: a streamed `POST /upload/{ticket}` on the existing mux, authorised by a one-shot bearer ticket. Rationale, in this order — the data plane is one TCP connection already carrying PTY, so a large upload competes with terminal responsiveness; backpressure on that plane would have to be invented, while HTTP inherits TCP's; and a new msg-type means a second codec beside `frame.go`/`frame.ts`, which are pinned to each other by golden vectors precisely so nobody changes one alone. Consequences: a second authorisation mechanism now exists beside `/session`'s subprotocol token, and it is a bearer credential whose possession authorises both a destination and its bytes — written down in the ADR because the next person to add an HTTP route will inherit it.

- [ ] **Step 2: Add the row to `docs/decisions/INDEX.md`**

- [ ] **Step 3: Make the backlog changes in one batch**

```bash
bd batch <<'EOF'
update nocx-9le.5 --priority 1
dep add <epic-id> nocx-0vc5l --type discovered-from
dep add <task-10-bead> nocx-a8cz
EOF
```

Then `bd create` the two siblings under `nocx-9le` (download; directory upload), each with a `## Success Criteria` section — `bd create -t epic` refuses without one — and `bd dep add <directory-upload> <epic-id>` so it is blocked by this one.

- [ ] **Step 4: Publish immediately**

```bash
bd dolt push
```

An unpushed bead does not exist for anybody else.

- [ ] **Step 5: Commit**

```bash
git add docs/decisions/
git commit -m "docs(decisions): an HTTP upload route beside the WebSocket, and what it costs (<bead-id>)"
```
