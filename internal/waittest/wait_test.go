package waittest

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

type recordingTB struct {
	testing.TB
	fatalMessage string
}

func (t *recordingTB) Helper() {}

func (t *recordingTB) Fatalf(format string, args ...any) {
	t.fatalMessage = fmt.Sprintf(format, args...)
}

func TestWaitForReturnsWhenPredicateBecomesTrue(t *testing.T) {
	var calls int
	WaitFor(t, "the predicate", func() bool {
		calls++
		return calls == 2
	})
	if calls != 2 {
		t.Fatalf("predicate calls = %d, want 2", calls)
	}
}

func TestWaitForTimeoutNamesTheCondition(t *testing.T) {
	tb := &recordingTB{}
	WaitForTimeout(tb, "the second domain to be granted", time.Nanosecond, func() bool {
		return false
	})
	if !strings.Contains(tb.fatalMessage, "the second domain to be granted") {
		t.Fatalf("failure format %q does not name the condition", tb.fatalMessage)
	}
}

func TestWaitForTimeoutAllowsShortOverride(t *testing.T) {
	start := time.Now()
	WaitForTimeout(&recordingTB{}, "a condition", 5*time.Millisecond, func() bool {
		return false
	})
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("override took %s, want it to bound the wait", elapsed)
	}
}
