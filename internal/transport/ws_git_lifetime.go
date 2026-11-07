package transport

import (
	"errors"
	"fmt"

	"github.com/shady2k/nocx/internal/capability"
)

// The git domain's per-checkout order and per-mount bulkhead (ADR-8eej3e66
// stage 5). The whole-domain git gate is gone: a mutation chains on the
// checkout it changes, a read takes no key, and every call draws on the mount
// its checkout sits on.
//
// SIZES ARE MEASURED, not inherited. On this 2-CPU arm64 box, `git status
// --porcelain=v2 --branch --untracked-files=all` on this 5,539-file repository
// takes 8 ms warm; 8 concurrent processes take 33 ms in total (4 ms each), 16
// take 61 ms and 64 take 257 ms. Eight permits is where a saturated mount
// costs a person ~1/30 s per read instead of a second, so it is the bulkhead's
// capacity. Two of the eight are reserved for person-origin work: a panel's
// poll is background and may hold at most six, so the person's own stage or
// commit always finds a permit.
const (
	gitBulkheadCapacity   = 8
	gitPersonReserved     = 2
	gitBulkheadQueue      = 32
	gitAbandonedPerDomain = 8
	gitAbandonedGlobal    = 32
)

// GitOperationExpiredCode answers a git call whose declared lifetime ran out
// before the work finished. It is the one lifetime code: the call
// was accepted and its outcome is the declaration's, not a refusal to run.
const GitOperationExpiredCode = -32005

// answerGitOperationError is the one error seam for git.* operations. A
// declaration's expiry is answered with its own outcome; a lane or queue
// wait keeps the declaration's own answer; anything else is the
// handler's ordinary internal error.
func answerGitOperationError(r Responder, req jsonrpcRequest, err error) {
	if err == nil {
		return
	}
	// The message is the error's own text, taken once so both arms below carry
	// the same sentence.
	sentence := fmt.Sprint(err)
	var notStarted *capability.NotStartedError
	if errors.As(err, &notStarted) {
		_ = r.TryError(req.ID, RPCError{
			Code:    GitOperationExpiredCode,
			Message: sentence,
			Data:    &gitErrorData{Reason: "not-started"},
		})
		return
	}
	var expired *capability.ExpiredError
	if errors.As(err, &expired) {
		// A suspect domain refused the call before any work started, so the
		// declared outcome is not the answer even when the operation is a
		// mutation: nothing took effect. A max expiry that reached the work
		// keeps the declaration's own outcome.
		reason := string(expired.Outcome)
		if expired.Suspected {
			reason = "not-started"
		}
		_ = r.TryError(req.ID, RPCError{
			Code:    GitOperationExpiredCode,
			Message: sentence,
			Data:    &gitErrorData{Reason: reason},
		})
		return
	}
	answerOperationRefusal(r, req, err)
}
