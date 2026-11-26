package hostsvc

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"syscall"

	filesystem "github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/helper/proto"
)

// The fs service's refusal codes — the machine-readable half of an answer the
// coordinator switches on. They are the plan's closed list (design §5.10) and
// nothing else: a code invented here would be a vocabulary the coordinator has
// no case for, which arrives as an unclassified failure wearing a name.
const (
	// ErrCodeNotFound: the path does not exist, including a broken symlink.
	ErrCodeNotFound = "not-found"
	// ErrCodePermissionDenied: an explicit refusal, never an empty listing.
	ErrCodePermissionDenied = "permission-denied"
	// ErrCodeNotDir: the path is not a directory where one was required.
	ErrCodeNotDir = "not-dir"
	// ErrCodeNotRegular: a read was asked of something that is not a regular
	// file. Details carries the finer kind (`fifo`, `device`, `socket`, `dir`)
	// because "not a file" is not enough to tell a person what they clicked.
	ErrCodeNotRegular = "not-regular"
	// ErrCodeInvalidPath: the path violates the provider's syntax. Details
	// carries the reason.
	ErrCodeInvalidPath = "invalid-path"
	// ErrCodeInvalidPage: the page cannot be asked for at all — a negative
	// offset, a non-positive length, a whole-file read not at 0, or a length
	// over the read ceiling. An offset past a directory's end is NOT this: it
	// names a page the provider answers empty, with the real total and rev.
	ErrCodeInvalidPage = "invalid-page"
	// ErrCodeTooLarge: the entry cap, or a whole-file read of a file larger
	// than the caller's bound. Details carries `observed` and `limit`.
	ErrCodeTooLarge = "too-large"
	// ErrCodeTooLargeSize: the listing's byte cap — the wire cost of the whole
	// listing summed over its entries. It is not `too-large` because the two
	// caps count different things against different limits, and a caller that
	// must name the cap it hit, or recover the error type behind the refusal,
	// cannot get either from one word.
	ErrCodeTooLargeSize = "too-large-size"
	// ErrCodeTimedOut: the operation's elapsed-time cap, or the request's own
	// context being cancelled.
	ErrCodeTimedOut = "timed-out"
	// ErrCodeChangedDuringRead: three attempts and the file was still moving.
	// The bytes were never returned, because a mixture of two versions has no
	// version to name.
	ErrCodeChangedDuringRead = "changed-during-read"

	// ── the write session's refusals (nocx-oalo1.12.3) ─────────────────

	// ErrCodeNoSpace: the filesystem refused bytes — ENOSPC, or EDQUOT when the
	// account's quota is what ran out. One code, because the caller's action is
	// the same in both and the difference is not the panel's to explain.
	ErrCodeNoSpace = "no-space"
	// ErrCodeChangedSinceRead: the commit's expectation no longer holds. The
	// session stays open with its temporary, so the caller may re-read and send
	// only the changed chunks rather than the whole file again.
	ErrCodeChangedSinceRead = "changed-since-read"
	// ErrCodeExists: `expect.absent` was asked of a name that is taken. Nothing
	// was replaced; the session stays open so the caller can commit under
	// another name (KeepBoth) without re-sending its bytes.
	ErrCodeExists = "exists"
	// ErrCodeBadOffset: a chunk that does not continue the session's stream.
	// Details carries `expected` and `offset`.
	ErrCodeBadOffset = "bad-offset"
	// ErrCodeSizeMismatch: the commit states a byte count the session does not
	// hold. Details carries `expected` and `size`.
	ErrCodeSizeMismatch = "size-mismatch"
	// ErrCodeBadSession: the id is malformed, already in use, or names a session
	// that is no longer open. Details carries the reason.
	ErrCodeBadSession = "bad-session"
)

// Refusal gives the service's errors their wire codes and structured details.
// The mapping is typed at every step: filesystem's own error markers first,
// then the raw *os.PathError conditions through errors.Is, never a string
// match. A refused op leaves the connection serving — the host answers one
// request's error and reads the next, which is scenario 14's second half.
func (s *Service) Refusal(err error) (string, json.RawMessage) {
	if code, details, ok := refusalOf(err); ok {
		return code, details
	}
	return "", nil
}

func refusalOf(err error) (string, json.RawMessage, bool) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ErrCodeTimedOut, nil, true
	}

	var page *invalidPage
	if errors.As(err, &page) {
		return ErrCodeInvalidPage, nil, true
	}
	var regular *notRegular
	if errors.As(err, &regular) {
		return ErrCodeNotRegular, mustJSON(kindDetails{Kind: regular.kind}), true
	}
	var large *tooLarge
	if errors.As(err, &large) {
		return ErrCodeTooLarge, mustJSON(sizeDetails{Observed: large.observed, Limit: large.limit}), true
	}
	var moving *changedDuringRead
	if errors.As(err, &moving) {
		return ErrCodeChangedDuringRead, nil, true
	}

	var badParams *badParams
	if errors.As(err, &badParams) {
		return proto.ErrCodeBadParams, nil, true
	}
	var badSession *badSession
	if errors.As(err, &badSession) {
		return ErrCodeBadSession, mustJSON(reasonDetails{Reason: badSession.reason}), true
	}
	var offset *badOffset
	if errors.As(err, &offset) {
		return ErrCodeBadOffset, mustJSON(offsetDetails{Expected: offset.expected, Offset: offset.got}), true
	}
	var mismatch *sizeMismatch
	if errors.As(err, &mismatch) {
		return ErrCodeSizeMismatch, mustJSON(mismatchDetails{Expected: mismatch.expected, Size: mismatch.got}), true
	}
	var changed *changedSinceRead
	if errors.As(err, &changed) {
		return ErrCodeChangedSinceRead, mustJSON(reasonDetails{Reason: changed.reason}), true
	}
	var exists *targetExists
	if errors.As(err, &exists) {
		return ErrCodeExists, nil, true
	}
	var full *noSpace
	if errors.As(err, &full) {
		return ErrCodeNoSpace, nil, true
	}

	var notFound *filesystem.ErrNotFound
	if errors.As(err, &notFound) {
		return ErrCodeNotFound, nil, true
	}
	var denied *filesystem.ErrPermission
	if errors.As(err, &denied) {
		return ErrCodePermissionDenied, nil, true
	}
	var notDir *filesystem.ErrNotDir
	if errors.As(err, &notDir) {
		return ErrCodeNotDir, nil, true
	}
	var notReg *filesystem.ErrNotRegular
	if errors.As(err, &notReg) {
		return ErrCodeNotRegular, mustJSON(kindDetails{Kind: string(notReg.Kind)}), true
	}
	var badPath *filesystem.ErrInvalidPath
	if errors.As(err, &badPath) {
		return ErrCodeInvalidPath, mustJSON(reasonDetails{Reason: badPath.Reason}), true
	}
	var badPage *filesystem.ErrInvalidPage
	if errors.As(err, &badPage) {
		return ErrCodeInvalidPage, nil, true
	}
	var counted *filesystem.ErrTooLarge
	if errors.As(err, &counted) {
		return ErrCodeTooLarge, mustJSON(countDetails{Observed: counted.ObservedCount, Limit: counted.Limit}), true
	}
	var sized *filesystem.ErrTooLargeSize
	if errors.As(err, &sized) {
		return ErrCodeTooLargeSize, mustJSON(sizeDetails{Observed: sized.ObservedBytes, Limit: sized.Limit}), true
	}
	var timedOut *filesystem.ErrTimedOut
	if errors.As(err, &timedOut) {
		return ErrCodeTimedOut, nil, true
	}

	// The raw conditions, for the os calls this package makes itself (the
	// open and fstat of a read, the stat behind fs.stat). errors.Is walks the
	// *os.PathError the standard library returns, so EACCES and EPERM are one
	// answer — a person cannot act on the difference.
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return ErrCodeNotFound, nil, true
	case errors.Is(err, fs.ErrPermission):
		return ErrCodePermissionDenied, nil, true
	case errors.Is(err, syscall.ENOTDIR):
		return ErrCodeNotDir, nil, true
	case errors.Is(err, syscall.ENOSPC), errors.Is(err, syscall.EDQUOT):
		return ErrCodeNoSpace, nil, true
	}
	return "", nil, false
}

// invalidPage is a page this op cannot express: a non-positive length, a
// negative offset, a whole-file read not at 0, or a length over the ceiling. It
// is the service's own type because filesystem.ErrInvalidPage carries ints and
// a read page is 64-bit; the CODE is still the plan's single invalid-page
// spelling. An offset past a directory's end never reaches it — that page is
// answered empty, the way local.List answers it.
type invalidPage struct{ reason string }

func (e *invalidPage) Error() string { return "hostsvc: invalid page: " + e.reason }

// notRegular is a read of something that is not a regular file. kind is the
// finer spelling the plan asks for in details — fifo, device, socket, dir —
// because filesystem.Kind's own vocabulary deliberately flattens every
// non-regular non-directory object into `other`, which tells a person nothing
// about what is at that path.
type notRegular struct {
	path string
	kind string
}

func (e *notRegular) Error() string {
	return "hostsvc: " + e.path + " is not a regular file (" + e.kind + ")"
}

// tooLarge is a whole-file read of a file bigger than the caller's bound. The
// observed size and the limit travel, so the caller can say how far past the
// bound it was instead of only that it was.
type tooLarge struct {
	observed int64
	limit    int64
}

func (e *tooLarge) Error() string {
	return "hostsvc: file is larger than the read bound"
}

// changedDuringRead is the fence's refusal: the file moved under all three
// attempts, so no version can be named for what was read.
type changedDuringRead struct {
	path     string
	attempts int
}

func (e *changedDuringRead) Error() string {
	return "hostsvc: " + e.path + " changed during every read attempt"
}

// ── the write session's own error types ───────────────────────────────

// badParams is a request whose parts contradict each other — an expectation
// that asks for two conditions at once, a negative size. It carries the wire's
// generic `bad_params` rather than a code of its own because it says nothing
// about the host: the request was wrong.
type badParams struct{ reason string }

func (e *badParams) Error() string { return "hostsvc: bad params: " + e.reason }

// badSession is an id this service cannot serve: malformed, already taken, or
// naming a session that is no longer open.
type badSession struct{ reason string }

func (e *badSession) Error() string { return "hostsvc: bad session: " + e.reason }

// badOffset is a chunk that does not continue the stream. Details carries both
// ends so the caller can resend from where the session actually is.
type badOffset struct{ expected, got int64 }

func (e *badOffset) Error() string {
	return "hostsvc: chunk does not continue the write session"
}

// sizeMismatch is a commit stating a byte count the session does not hold.
type sizeMismatch struct{ expected, got int64 }

func (e *sizeMismatch) Error() string {
	return "hostsvc: the commit's size is not the session's size"
}

// changedSinceRead is the fence's refusal: the target moved between the read
// the caller's version came from and this commit.
type changedSinceRead struct{ path, reason string }

func (e *changedSinceRead) Error() string {
	return "hostsvc: " + e.path + " changed since it was read (" + e.reason + ")"
}

// targetExists is `expect.absent` meeting a name that is taken.
type targetExists struct{ path string }

func (e *targetExists) Error() string {
	return "hostsvc: " + e.path + " already exists"
}

// noSpace is the filesystem refusing bytes. diskFull wraps the raw errno in it
// so the refusal code does not depend on which call noticed.
type noSpace struct{ err error }

func (e *noSpace) Error() string { return "hostsvc: no space left for the write" }

func (e *noSpace) Unwrap() error { return e.err }

// diskFull turns ENOSPC and EDQUOT into the one refusal the caller acts on and
// leaves every other error as it was.
func diskFull(err error) error {
	if errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT) {
		return &noSpace{err: err}
	}
	return err
}

type kindDetails struct {
	Kind string `json:"kind"`
}

type reasonDetails struct {
	Reason string `json:"reason"`
}

type countDetails struct {
	Observed int `json:"observed"`
	Limit    int `json:"limit"`
}

type sizeDetails struct {
	Observed int64 `json:"observed"`
	Limit    int64 `json:"limit"`
}

type offsetDetails struct {
	Expected int64 `json:"expected"`
	Offset   int64 `json:"offset"`
}

type mismatchDetails struct {
	Expected int64 `json:"expected"`
	Size     int64 `json:"size"`
}

func mustJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return raw
}
