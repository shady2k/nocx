package hostsvc

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"syscall"

	filesystem "github.com/shady2k/nocx/internal/filesystem"
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
	// ErrCodeInvalidPage: the page cannot exist — a non-positive length, a
	// negative offset, a whole-file read not at 0, or an offset past the end.
	ErrCodeInvalidPage = "invalid-page"
	// ErrCodeTooLarge: the entry cap, the listing size cap, or a whole-file
	// read of a file larger than the caller's bound. Details carries `observed`
	// and `limit`.
	ErrCodeTooLarge = "too-large"
	// ErrCodeTimedOut: the operation's elapsed-time cap, or the request's own
	// context being cancelled.
	ErrCodeTimedOut = "timed-out"
	// ErrCodeChangedDuringRead: three attempts and the file was still moving.
	// The bytes were never returned, because a mixture of two versions has no
	// version to name.
	ErrCodeChangedDuringRead = "changed-during-read"
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
		return ErrCodeTooLarge, mustJSON(sizeDetails{Observed: sized.ObservedBytes, Limit: sized.Limit}), true
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
	}
	return "", nil, false
}

// invalidPage is a page this op refuses: one that cannot exist (a non-positive
// length, a negative offset) or one past what the directory holds. It is the
// service's own type because filesystem.ErrInvalidPage carries ints and a read
// page is 64-bit; the CODE is still the plan's single invalid-page spelling.
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

func mustJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return raw
}
