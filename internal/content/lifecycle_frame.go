package content

// ONE LIFECYCLE FRAME, ONE TRANSACTION (ADR-0077, the owner's decision of
// 2026-09-30).
//
// A coordinator applies the shell's lifecycle frames one at a time, and each
// one may write several rows — an entry, its execution, the block's artifact,
// the settle of a block the frame ends — before the coordinator's own cursor
// says the frame was applied. Those writes and the cursor commit together or
// not at all: a process that dies anywhere inside the frame leaves the store
// exactly as it was before it, and the next coordinator, resuming from the
// cursor, applies the frame once. There is no window in which the rows are
// stored and the cursor is not.
//
// The frame is carried in the context. Every store method routes its
// statements through conn and beginTx, and a context that carries this
// store's frame runs them on the frame's transaction: the transaction begins
// at the frame's first write — never before, so a frame that reads without
// writing holds nothing — and the connection is held from then until the
// frame ends (maxOpenConns is one, sqlite.go). A method's own transaction
// becomes a savepoint inside it.
//
// THE HOLD IS BOUNDED, AND A FAILED TRANSACTION IS BEGUN AGAIN WHERE IT
// FAILED (the owner's decisions of 2026-09-30; ADR-0077 decision 9). The one
// connection is every other reader's and writer's too, so a frame's
// transaction may hold it for LifecycleFrameMaxHold and no longer, counted
// from its first write; past it the transaction is rolled back where it
// stands and the connection is free at once. A transaction whose write
// failed is rolled back the same way. Either way the frame is not over: at
// its next store call — the failed write itself, or whatever the frame does
// next — it pauses, begins a fresh transaction, replays the writes it had
// already made, in order, and carries on, up to lifecycleFrameRetryPauses
// more times. The frame's projection never sees the failure: the write it
// made answers as it would have, and what the projection decides from that
// answer — in the store and in its own memory — is exactly what a frame that
// never failed decides. That is ADR-0076 decision 2 kept: a block's state
// changes only on something the helper reports, and a store write failing is
// not that. Only a frame that fails every attempt ends in failure; then every
// store call it still makes answers ErrLifecycleFrameFailed, nothing of it is
// stored, and whoever keeps state beside the store re-reads it from the
// store when the frame ends (lifecyclecommit.After).
//
// A WRITE THE STORE REFUSES IS ITS ANSWER, NOT A FAILURE: a missing entry, a
// discontinuous append, an id already used. The write's savepoint is rolled
// back, the frame's transaction goes on, and the caller branches on the
// answer as it always has — another attempt would give it the same answer. A
// failure is the database's own error, a transaction that ended under the
// write, or a store that closed (isStoreFailure).
//
// THE CONTRACT THIS PUTS ON A CALLER: every store call a frame makes carries
// the context the frame handed it. A call on the same goroutine with any
// other context waits for the connection the frame holds, and the frame is
// waiting for that call.

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"sync"
	"time"

	sqlite3 "github.com/ncruces/go-sqlite3"

	"github.com/shady2k/nocx/internal/lifecyclecommit"
	"github.com/shady2k/nocx/internal/log"
)

// querier is the statement surface *sql.DB and *sql.Tx share.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// LifecycleFrameMaxHold bounds one frame transaction's hold on the store's
// connection, from its first write to its commit.
//
// 250 ms is the owner's figure (2026-09-30), set against the hold measured on
// the restart and shell-exit acceptances: first write to commit, p50 0.70 ms,
// p99 12.7 ms, max 35.8 ms in a plain run, and p50 0.73 ms, p99 21.9 ms, max
// 52.1 ms under the loaded bar (and one frame of 106.5 ms), almost all of it
// store work — the commit itself up to 11 ms (ADR-0077).
const LifecycleFrameMaxHold = 250 * time.Millisecond

// lifecycleFrameRetryPauses are the pauses before each further transaction
// of a frame whose transaction failed: three more, four attempts in all.
var lifecycleFrameRetryPauses = [...]time.Duration{50 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond}

// lifecycleFrameAttempts is how many transactions a frame may begin.
const lifecycleFrameAttempts = len(lifecycleFrameRetryPauses) + 1

// FrameFailedError is a frame that failed every attempt: nothing of it and
// not the cursor was stored.
type FrameFailedError struct {
	// Attempts is how many transactions the frame began.
	Attempts int
	// Held is how long the last of them held the connection.
	Held time.Duration
	// Cause is the last attempt's failure.
	Cause error
}

func (e *FrameFailedError) Error() string {
	return fmt.Sprintf("content: the lifecycle frame was not applied after %d attempts (the last held the store %s): %v",
		e.Attempts, e.Held, e.Cause)
}

// Unwrap answers both the sentinel every frame failure carries and the cause.
func (e *FrameFailedError) Unwrap() []error { return []error{ErrLifecycleFrameFailed, e.Cause} }

// FrameExpiredError is the failure of a frame transaction that held the
// connection past LifecycleFrameMaxHold and was rolled back.
type FrameExpiredError struct {
	// Held is how long the transaction had held the connection when it was
	// rolled back.
	Held time.Duration
}

func (e *FrameExpiredError) Error() string {
	return fmt.Sprintf("content: the lifecycle frame held the store past its bound (%s, held %s) and was rolled back",
		LifecycleFrameMaxHold, e.Held)
}

// ErrFrameReplayDiverged is a write a frame replayed into a fresh transaction
// that answered differently from the first time — the store changed under
// the frame between its attempts, so the frame the replay would commit is not
// the one its caller decided on. The frame fails rather than store it.
var ErrFrameReplayDiverged = errors.New("content: a lifecycle frame's replayed write answered differently from its first attempt")

// frameAnswer keeps the first answer a store write gave, for the replay of
// that write inside a lifecycle frame (recover). The frame runs a write's
// closure again when its transaction had to be begun again, and the closure
// settles its answer here each time: an answer that differs from the first
// is ErrFrameReplayDiverged, because the frame's caller already acted on the
// first — minted an entry or did not, holds this execution id, installed that
// artifact. What goes into T is every field of the answer that reflects a
// decision the store made; a value the store merely stamps afresh on every
// run (a wall-clock time, the next ingest sequence number, which other
// writers may have taken during the pause) is not a decision, no caller
// inside a frame reads it, and comparing it would fail every retried frame.
type frameAnswer[T comparable] struct {
	set bool
	v   T
}

func (a *frameAnswer[T]) settle(got T) error {
	if a.set && a.v != got {
		return ErrFrameReplayDiverged
	}
	a.set, a.v = true, got
	return nil
}

// ErrFrameAbandoned is what a frame's apply returns to store nothing of it
// and try no further attempt: the caller decided the frame is not its to
// apply after all (the coordinator began stopping while it ran, ADR-0077
// decision 11). ApplyLifecycleFrame answers a FrameFailedError wrapping it.
var ErrFrameAbandoned = errors.New("content: the lifecycle frame was abandoned by its caller")

// ErrLifecycleCursorMissing is a frame whose cursor lands on no binding: the
// session's row is gone, or it records no cursor. The frame cannot be marked
// applied, so it fails rather than commit rows the cursor does not cover.
var ErrLifecycleCursorMissing = errors.New("content: the session's binding is gone or records no lifecycle cursor")

// lifecycleFrameKey carries the frame in a context.
type lifecycleFrameKey struct{}

// lifecycleFrame is one frame's unit of work, across every transaction it
// begins.
type lifecycleFrame struct {
	s *sqliteContent

	mu         sync.Mutex
	tx         *sql.Tx
	savepoints int
	// failed is the current transaction's failure: a write that failed, or
	// the bound that rolled it back. The frame's next store call begins the
	// transaction again (recover), which clears it.
	failed error
	// dead is the frame's end in failure: every attempt spent, or a replay
	// the store answered differently. Every store call answers it.
	dead *FrameFailedError
	// attempts is how many transactions the frame has begun.
	attempts int
	// began is when the current transaction took the connection; stop
	// disarms its bound.
	began time.Time
	stop  func() bool
	// fired closes when the current transaction's bound callback has run;
	// commit waits for it whenever disarming the bound loses the race.
	fired chan struct{}
	held  time.Duration
	ended bool
	// depth is how many writes are running now (a store method's write may
	// run another's inside it), and replaying is the replay itself: neither
	// may begin the transaction again under the write it is running.
	depth     int
	replaying bool
	// writes are the store writes the frame made, in order — each one the
	// store accepted. A fresh transaction replays them.
	writes []func(ctx context.Context) error
	// effects is the frame's post-commit queue (lifecyclecommit): what the
	// frame tells anyone outside the process waits for its end.
	effects *lifecyclecommit.Queue
	// release gives back the frame's goroutine claim (framecheck).
	release func()
}

// frameOf is the frame ctx carries for this store, or nil.
func (s *sqliteContent) frameOf(ctx context.Context) *lifecycleFrame {
	f, _ := ctx.Value(lifecycleFrameKey{}).(*lifecycleFrame)
	if f == nil || f.s != s {
		return nil
	}
	return f
}

// conn is what a statement under ctx runs on: the frame's transaction once
// the frame has one, the pool otherwise. A read a frame makes before its
// first write sees exactly what the transaction would; a read after the
// bound rolled the transaction back begins it again first; a read in a frame
// that failed for good reads what the store holds.
//
// A read on a frame's own goroutine WITHOUT the frame's context would wait
// for the connection the frame holds; under nocx_framecheck it panics with
// ErrFrameContextMissing instead, because *sql.Row has no way to carry an
// error of the store's own.
func (s *sqliteContent) conn(ctx context.Context) querier {
	if err := s.checkFrameContext(ctx); err != nil {
		panic(err)
	}
	if f := s.frameOf(ctx); f != nil {
		if tx := f.readTx(ctx); tx != nil {
			return tx
		}
	}
	return s.db
}

// readTx is the transaction a read in the frame runs on, or nil for the pool.
func (f *lifecycleFrame) readTx(ctx context.Context) *sql.Tx {
	f.mu.Lock()
	if f.dead != nil || (f.tx == nil && f.attempts == 0) {
		f.mu.Unlock()
		return nil
	}
	if f.failed != nil && f.depth == 0 && !f.replaying {
		f.mu.Unlock()
		if err := f.recover(ctx); err != nil {
			return nil
		}
		f.mu.Lock()
	}
	tx := f.tx
	f.mu.Unlock()
	return tx
}

// txEnd ends one method's transaction: the transaction itself, or, inside a
// frame, the savepoint that stands for it.
type txEnd struct {
	tx        *sql.Tx
	frame     *lifecycleFrame
	savepoint string
	done      bool
}

func (e *txEnd) commit() error {
	if e.done {
		return sql.ErrTxDone
	}
	e.done = true
	if e.frame == nil {
		return e.tx.Commit()
	}
	_, err := e.tx.ExecContext(context.Background(), "RELEASE "+e.savepoint)
	if err != nil {
		e.frame.fail(err)
	}
	return err
}

func (e *txEnd) rollback() {
	if e.done {
		return
	}
	e.done = true
	if e.frame == nil {
		_ = e.tx.Rollback()
		return
	}
	if _, err := e.tx.ExecContext(context.Background(), "ROLLBACK TO "+e.savepoint); err != nil {
		e.frame.fail(err)
		return
	}
	if _, err := e.tx.ExecContext(context.Background(), "RELEASE "+e.savepoint); err != nil {
		e.frame.fail(err)
	}
}

// beginTx begins one method's transaction: its own, or a savepoint in the
// frame ctx carries.
func (s *sqliteContent) beginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, *txEnd, error) {
	f := s.frameOf(ctx)
	if f == nil {
		if err := s.checkFrameContext(ctx); err != nil {
			return nil, nil, err
		}
		tx, err := s.db.BeginTx(ctx, opts)
		if err != nil {
			return nil, nil, err
		}
		return tx, &txEnd{tx: tx}, nil
	}
	tx, err := f.current(ctx)
	if err != nil {
		return nil, nil, err
	}
	f.mu.Lock()
	f.savepoints++
	name := fmt.Sprintf("frame_sp_%d", f.savepoints)
	f.mu.Unlock()
	if _, err := tx.ExecContext(ctx, "SAVEPOINT "+name); err != nil {
		f.fail(err)
		return nil, nil, err
	}
	return tx, &txEnd{tx: tx, frame: f, savepoint: name}, nil
}

// current is the frame's live transaction. Inside a write, or the replay, it
// is the one that write is running on, failed or not — a write is never
// moved to another transaction halfway through; its own failure is what
// begins the frame's transaction again (write). Anywhere else a failed
// transaction is begun again first.
func (f *lifecycleFrame) current(ctx context.Context) (*sql.Tx, error) {
	f.mu.Lock()
	if f.dead != nil {
		f.mu.Unlock()
		return nil, f.dead
	}
	if f.depth > 0 || f.replaying {
		tx, failed := f.tx, f.failed
		f.mu.Unlock()
		if failed != nil {
			return nil, failed
		}
		if tx == nil {
			return nil, sql.ErrTxDone
		}
		return tx, nil
	}
	f.mu.Unlock()
	if err := f.ready(ctx); err != nil {
		return nil, err
	}
	f.mu.Lock()
	tx := f.tx
	f.mu.Unlock()
	return tx, nil
}

// ready makes sure the frame has a live transaction: its first, or a fresh
// one after the last failed.
func (f *lifecycleFrame) ready(ctx context.Context) error {
	f.mu.Lock()
	if f.dead != nil {
		f.mu.Unlock()
		return f.dead
	}
	if f.tx != nil && f.failed == nil {
		f.mu.Unlock()
		return nil
	}
	f.mu.Unlock()
	return f.recover(ctx)
}

// recover begins the frame's transaction — the first time, or again after the
// last one failed: the failed one is rolled back, the frame pauses, and the
// fresh one replays every write the frame made, in order. Past the last
// attempt the frame fails for good.
func (f *lifecycleFrame) recover(ctx context.Context) error {
	for {
		f.mu.Lock()
		if f.dead != nil {
			f.mu.Unlock()
			return f.dead
		}
		if f.tx != nil && f.failed == nil {
			f.mu.Unlock()
			return nil
		}
		if f.attempts >= lifecycleFrameAttempts {
			return f.dieLocked(f.failed)
		}
		cause := f.failed
		old := f.endTxLocked()
		n, held := f.attempts, f.held
		f.mu.Unlock()
		if old != nil {
			// A transaction the bound or a failed commit already ended
			// answers ErrTxDone here, and that is all.
			_ = old.Rollback()
		}
		if n > 0 {
			log.From(ctx).Warn("lifecycle frame transaction failed; the same frame begins it again and replays its writes",
				"attempt", n, "held_us", held.Microseconds(), "error", cause)
			f.s.pause(lifecycleFrameRetryPauses[n-1])
		}
		tx, err := f.s.db.BeginTx(context.WithoutCancel(ctx), &sql.TxOptions{Isolation: sql.LevelSerializable})
		f.mu.Lock()
		f.attempts++
		attempt := f.attempts
		if err != nil {
			f.failed = err
			f.mu.Unlock()
			continue
		}
		f.tx, f.failed, f.began, f.replaying = tx, nil, time.Now(), true
		writes := append([]func(ctx context.Context) error(nil), f.writes...)
		f.mu.Unlock()
		// Armed outside the lock: a bound that fires at once rolls back a
		// transaction nothing has written to yet, and the replay below fails
		// on it like on any other.
		fired := make(chan struct{})
		stop := f.s.cfg.FrameTimer(LifecycleFrameMaxHold, func() {
			defer close(fired)
			f.expire(attempt)
		})
		f.mu.Lock()
		f.stop, f.fired = stop, fired
		f.mu.Unlock()
		var replayErr error
		for _, w := range writes {
			if replayErr = w(ctx); replayErr != nil {
				break
			}
		}
		f.mu.Lock()
		f.replaying = false
		if replayErr != nil && f.failed == nil {
			if !isStoreFailure(replayErr) {
				// The store accepted this write on an earlier attempt and
				// refuses it now: whatever changed the store between the
				// attempts, the frame its caller decided on is no longer the
				// one this transaction would commit.
				return f.dieLocked(fmt.Errorf("%w: %w", ErrFrameReplayDiverged, replayErr))
			}
			f.failed = replayErr
		}
		f.mu.Unlock()
	}
}

// dieLocked ends the frame in failure. f.mu is held on entry and released.
func (f *lifecycleFrame) dieLocked(cause error) error {
	var old *sql.Tx
	if f.dead == nil {
		if cause == nil {
			cause = errors.New("content: the lifecycle frame's transaction failed")
		}
		old = f.endTxLocked()
		f.dead = &FrameFailedError{Attempts: f.attempts, Held: f.held, Cause: cause}
	}
	dead := f.dead
	f.mu.Unlock()
	if old != nil {
		_ = old.Rollback()
	}
	return dead
}

// endTxLocked disarms the current transaction's bound, measures its hold and
// forgets it, answering it for the caller to end.
func (f *lifecycleFrame) endTxLocked() *sql.Tx {
	old := f.tx
	if f.stop != nil {
		f.stop()
		f.stop = nil
	}
	if old != nil {
		f.held = time.Since(f.began)
	}
	f.tx = nil
	return old
}

// expire is the bound firing on the frame's attempt'th transaction: it is
// rolled back where it stands and fails, so the connection is free again
// even while the frame's own goroutine is still inside it. The frame's next
// store call begins it again.
func (f *lifecycleFrame) expire(attempt int) {
	f.mu.Lock()
	if f.ended || f.dead != nil || f.tx == nil || f.attempts != attempt {
		f.mu.Unlock()
		return
	}
	held := time.Since(f.began)
	if f.failed == nil {
		f.failed = &FrameExpiredError{Held: held}
	}
	tx := f.tx
	f.mu.Unlock()
	// Waits only for a statement already running on the transaction.
	_ = tx.Rollback()
}

// write runs one store write inside the frame. A write the store refuses is
// answered as it is; a write that fails — or that the bound rolled back
// under it — begins the frame's transaction again and runs once more, so its
// caller sees the answer the store gives the frame, never the failure.
func (f *lifecycleFrame) write(ctx context.Context, fn func(ctx context.Context) error) error {
	f.mu.Lock()
	nested := f.depth > 0 || f.replaying
	f.mu.Unlock()
	if nested {
		// Part of the write — or the replay — already running: it stands
		// or falls with it.
		return fn(ctx)
	}
	for {
		if err := f.ready(ctx); err != nil {
			return err
		}
		f.mu.Lock()
		f.depth++
		f.mu.Unlock()
		err := fn(ctx)
		f.mu.Lock()
		f.depth--
		switch {
		case err == nil:
			// Stored — or rolled back by the bound after it ran, and then
			// the replay stores it again.
			f.writes = append(f.writes, fn)
			f.mu.Unlock()
			return nil
		case f.failed == nil && !isStoreFailure(err):
			// The store's answer: the write's savepoint is rolled back and
			// the transaction goes on.
			f.mu.Unlock()
			return err
		default:
			if f.failed == nil {
				f.failed = err
			}
			f.mu.Unlock()
		}
	}
}

// fail marks the frame's current transaction failed: its next store call
// begins it again.
func (f *lifecycleFrame) fail(err error) {
	f.mu.Lock()
	if f.failed == nil && f.dead == nil {
		f.failed = err
		if f.tx == nil && f.attempts == 0 {
			// An attempt that failed before it wrote anything is still the
			// frame's first.
			f.attempts = 1
		}
	}
	f.mu.Unlock()
}

// isStoreFailure tells a failed write from a refused one: the database's own
// error, a transaction that ended under the write, a lost connection, or a
// closed store. Everything else a write answers is the store's answer.
func isStoreFailure(err error) bool {
	var sqliteErr *sqlite3.Error
	var code sqlite3.ErrorCode
	var extended sqlite3.ExtendedErrorCode
	var expired *FrameExpiredError
	return errors.As(err, &sqliteErr) || errors.As(err, &code) || errors.As(err, &extended) ||
		errors.As(err, &expired) ||
		errors.Is(err, sql.ErrTxDone) || errors.Is(err, sql.ErrConnDone) || errors.Is(err, driver.ErrBadConn) ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, ErrClosed)
}

// ErrFrameContextMissing is a store call made on a lifecycle frame's own
// goroutine without the context the frame handed it — the call that would
// otherwise wait for the connection the frame holds while the frame waits for
// it. Only the nocx_framecheck build detects it (framecheck_on.go); in a
// shipped build the frame's hold bound is what ends that wait.
var ErrFrameContextMissing = errors.New("content: a store call on a lifecycle frame's own goroutine did not carry the frame's context — " +
	"ADR-0077's contract: every store call a frame makes carries the context the frame handed it, " +
	"or it waits for the connection the frame holds")

// ErrLifecycleFrameFailed is what every failed frame's error wraps, and what
// every store call a failed frame still makes answers: nothing of it was
// stored.
var ErrLifecycleFrameFailed = errors.New("content: the lifecycle frame was not applied")

// ApplyLifecycleFrame implements LedgerRepository: apply's store writes and
// the session's lifecycle cursor at offset commit as one transaction, or
// none of them does.
//
// apply runs once — the kernel's ingest and every projection it causes — and
// a transaction that fails under it is begun again where it failed (the
// file's header), so what apply decided is what is stored. An error apply
// returns fails the transaction it ran in, and the frame's writes are
// replayed into a fresh one before the cursor. The cursor is the frame's last
// write. Only a frame that failed every attempt fails its caller, with a
// FrameFailedError.
func (s *sqliteContent) ApplyLifecycleFrame(ctx context.Context, sessionID string, offset uint64, apply func(ctx context.Context) error) error {
	if s.frameOf(ctx) != nil {
		return errors.New("content: a lifecycle frame cannot nest inside another")
	}
	f := &lifecycleFrame{s: s}
	f.release = s.claimGoroutine(f)
	fctx, effects := lifecyclecommit.Begin(context.WithValue(ctx, lifecycleFrameKey{}, f))
	f.effects = effects
	if err := apply(fctx); err != nil {
		if errors.Is(err, ErrFrameAbandoned) {
			f.mu.Lock()
			_ = f.dieLocked(err)
		} else {
			f.fail(err)
		}
	}
	return f.commit(fctx, sessionID, offset)
}

// pause waits d on the store's frame timer, so a test drives it.
func (s *sqliteContent) pause(d time.Duration) {
	done := make(chan struct{})
	s.cfg.FrameTimer(d, func() { close(done) })
	<-done
}

// commit writes the cursor as the frame's last write and commits, beginning
// the transaction again as often as it fails and attempts remain.
func (f *lifecycleFrame) commit(ctx context.Context, sessionID string, offset uint64) error {
	// Written last, in the same transaction: the cursor never covers a
	// frame whose rows are not stored, and rows are never stored for a
	// frame the cursor does not cover.
	if err := f.s.run(ctx, func(ctx context.Context) error {
		return f.s.recordLifecycleApplied(ctx, sessionID, offset)
	}); err != nil {
		f.mu.Lock()
		_ = f.dieLocked(err)
	}
	for {
		f.mu.Lock()
		if f.dead != nil {
			f.ended = true
			dead := f.dead
			f.mu.Unlock()
			f.finish(false)
			return dead
		}
		if f.failed != nil || f.tx == nil {
			f.mu.Unlock()
			_ = f.recover(ctx)
			continue
		}
		tx, stop, fired := f.tx, f.stop, f.fired
		f.stop = nil
		f.mu.Unlock()
		// THE BOUND DECIDES UP TO THE COMMIT, AND THE COMMIT GOES BY WHAT IT
		// DECIDED. Disarming it here fails once its callback has started —
		// the callback may be waiting for f.mu this very moment — and then
		// the commit waits for the callback to finish and looks again: the
		// transaction it rolled back is begun again, never committed. Once
		// disarmed, the bound no longer applies: SQLite's COMMIT cannot be
		// interrupted, and a rollback racing it would only lose the race
		// (database/sql ends a transaction once), so the commit itself —
		// measured at up to 11 ms (ADR-0077 decision 9) — is the one part of
		// a hold the bound does not cut short.
		if stop != nil && !stop() {
			<-fired
			f.mu.Lock()
			if f.failed == nil {
				f.failed = &FrameExpiredError{Held: time.Since(f.began)}
			}
			f.mu.Unlock()
			continue
		}
		if err := tx.Commit(); err != nil {
			f.mu.Lock()
			if f.failed == nil {
				f.failed = err
			}
			f.mu.Unlock()
			continue
		}
		f.mu.Lock()
		f.ended = true
		held := time.Since(f.began)
		f.held, f.tx = held, nil
		f.mu.Unlock()
		f.finish(true)
		// The hold, measured: first write to commit. The number the bound
		// is judged against.
		log.From(ctx).Debug("lifecycle frame committed", "session", sessionID, "held_us", held.Microseconds())
		return nil
	}
}

// finish gives back the frame's goroutine and runs what waited for its end:
// the frame's effects, in order, when it committed — and those that must know
// it did not, when it did not.
func (f *lifecycleFrame) finish(committed bool) {
	if f.release != nil {
		f.release()
	}
	enforceFileModes(f.s.path)
	f.effects.End(committed)
}
