package helperfs_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/farhelper"
	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/filesystem/helperfs"
	"github.com/shady2k/nocx/internal/filesystem/local"
	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/hostconn/hostconntest"
	"github.com/shady2k/nocx/internal/monoclock"
	"github.com/shady2k/nocx/internal/transfer"
)

type retryClock struct {
	*hostconntest.Clock
	armed chan time.Duration
}

func (c retryClock) After(d time.Duration) <-chan monoclock.Nanos {
	alarm := c.Clock.After(d)
	select {
	case c.armed <- d:
	default:
	}
	return alarm
}

// recoveryRegistry still acquires from the real registry. It tells the test that
// the provider actually tried to recover before the automatic retry was due.
type recoveryRegistry struct {
	*farhelper.Registry
	lost chan struct{}
}

func (r recoveryRegistry) Acquire(ctx context.Context, id string) (*farhelper.Lease, error) {
	l, err := r.Registry.Acquire(ctx, id)
	var unavailable *farhelper.Unavailable
	if errors.As(err, &unavailable) && unavailable.Reason == farhelper.ReasonHelperLost {
		select {
		case r.lost <- struct{}{}:
		default:
		}
	}
	return l, err
}

func TestHelperFilesystemLostCommitRecovery(t *testing.T) {
	for _, tc := range []struct {
		name             string
		restart, replace bool
		replayOutcome    string
	}{
		{name: "same helper remembers committed", replayOutcome: proto.FSOutcomeCommitted},
		{name: "restarted helper compares matching digest", restart: true, replayOutcome: proto.FSOutcomeUnknown},
		{name: "restarted helper refuses different digest", restart: true, replace: true, replayOutcome: proto.FSOutcomeUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, journal := tempTree(t), t.TempDir()
			originalInstance := strings.Repeat("a", 32)
			newInstance := originalInstance
			provider := local.New(local.WithRoot(root))
			original := sameHostService(t, provider, journal, originalInstance)
			nextService := original
			if tc.restart {
				newInstance = strings.Repeat("b", 32)
				nextService = sameHostService(t, provider, journal, newInstance)
			}
			var firstLane *pipeLane
			committed := make(chan error, 1)
			first := &fsObserver{Service: original, intercept: func(_ context.Context, op string, _ json.RawMessage, next func() (any, error)) (any, error) {
				out, err := next()
				if op == proto.OpWriteCommit {
					committed <- err
					firstLane.dropReply()
				}
				return out, err
			}}
			firstPeer, lane := servePeer(t, first, originalInstance)
			firstLane = lane
			replayed := make(chan proto.FSWriteCommitResult, 1)
			second := &fsObserver{Service: nextService, intercept: func(_ context.Context, op string, _ json.RawMessage, next func() (any, error)) (any, error) {
				out, err := next()
				if op == proto.OpWriteCommit && err == nil {
					reply, ok := out.(proto.FSWriteCommitResult)
					if !ok {
						t.Errorf("write-commit returned %T, want FSWriteCommitResult", out)
					}
					replayed <- reply
				}
				return out, err
			}}
			secondPeer, _ := servePeer(t, second, newInstance)
			clock := retryClock{Clock: hostconntest.New(), armed: make(chan time.Duration, 1)}
			r, _, _ := registry(t, clock, map[string][]*client.Client{panelID: {firstPeer, secondPeer}})
			observedRegistry := recoveryRegistry{Registry: r, lost: make(chan struct{}, 1)}
			p := helperfs.New(observedRegistry, panelID, root)
			ctx := testContext(t)
			intended, other := []byte("nocx original data"), []byte("external edit data")
			if len(other) != len(intended) {
				t.Fatal("comparison must disprove identical size as evidence")
			}
			u := transfer.Upload{DestDir: root, Name: "note", Size: int64(len(intended)), OnExists: transfer.Overwrite}
			path := filepath.Join(root, u.Name)
			type answer struct {
				out transfer.Outcome
				err error
			}
			done := make(chan answer, 1)
			go func() {
				out, err := p.Sink().Put(ctx, u, bytes.NewReader(intended), func(int64) {})
				done <- answer{out, err}
			}()
			if err := receive(t, committed); err != nil {
				t.Fatalf("the first commit failed locally: %v", err)
			}
			if !bytes.Equal(readFile(t, path), intended) {
				t.Fatal("the original commit did not write the intended bytes")
			}
			wantBytes := intended
			if tc.replace {
				replacement := filepath.Join(root, "external")
				writeFile(t, replacement, other)
				if err := os.Rename(replacement, path); err != nil {
					t.Fatal(err)
				}
				wantBytes = other
			}
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			retryIn := receive(t, clock.armed)
			select {
			case early := <-done:
				t.Fatalf("commit returned before the registry retried: %+v (%v)", early.out, early.err)
			case <-observedRegistry.lost:
			case <-ctx.Done():
				t.Fatal("provider did not acquire while the real registry was broken")
			}
			clock.Advance(retryIn)
			// The only path to the second helper is the registry's normal
			// loss/backoff/redial loop; the test never calls Acquire again.
			got := receive(t, done)
			if tc.replace {
				if !errors.As(got.err, new(*filesystem.ErrUnknownWriteOutcome)) {
					t.Fatalf("lost commit with different bytes at target: %+v (%v), want ErrUnknownWriteOutcome", got.out, got.err)
				}
			} else if got.err != nil || got.out.State != transfer.StateWritten || got.out.FinalName != u.Name {
				t.Fatalf("successful recovery: %+v (%v)", got.out, got.err)
			}
			reply := receive(t, replayed)
			if reply.Outcome != tc.replayOutcome {
				t.Fatalf("replacement helper answered %q, want %q", reply.Outcome, tc.replayOutcome)
			}
			firstCalls, secondCalls := first.observed(), second.observed()
			if !slices.Equal(ops(firstCalls), []string{proto.OpWriteOpen, proto.OpWrite, proto.OpWriteCommit}) ||
				!slices.Equal(ops(secondCalls), []string{proto.OpWriteCommit}) ||
				!bytes.Equal(firstCalls[2].params, secondCalls[0].params) {
				t.Fatalf("recovery must send exactly the same commit and no write data: first=%v, second=%v", ops(firstCalls), ops(secondCalls))
			}
			after, err := os.Stat(path)
			if err != nil || !os.SameFile(before, after) || !bytes.Equal(readFile(t, path), wantBytes) {
				t.Fatalf("replaying commit rewrote the current file: %v", err)
			}
		})
	}
}
