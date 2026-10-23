package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
)

type cancellationScreeningKnown struct {
	failure error
}

func (k *cancellationScreeningKnown) FindKnown(ctx context.Context, _ string) ([]KnownMatch, error) {
	if k.failure != nil {
		return nil, k.failure
	}
	return nil, ctx.Err()
}

type cancellationScreeningLedger struct {
	*fakeLedger
	finished []content.FinishExecution
}

func (l *cancellationScreeningLedger) FinishExecution(ctx context.Context, id int64, end content.FinishExecution) error {
	l.finished = append(l.finished, end)
	return l.fakeLedger.FinishExecution(ctx, id, end)
}

type cancellationScreeningRequester struct {
	*recordingRunner
	afterRun func(context.Context)
}

func (r *cancellationScreeningRequester) RequestRun(ctx context.Context, sessionID, command string) (json.RawMessage, error) {
	body, err := r.recordingRunner.RequestRun(ctx, sessionID, command)
	if r.afterRun != nil {
		r.afterRun(ctx)
	}
	return body, err
}

func cancellationScreeningKernel(t *testing.T, runErr error, known KnownMaterial, afterRun func(context.Context)) (*effectKernel, *cancellationScreeningLedger) {
	t.Helper()
	registry, err := agenttools.Assemble(os.DirFS(realToolsFS))
	if err != nil {
		t.Fatal(err)
	}
	ledger := &cancellationScreeningLedger{fakeLedger: &fakeLedger{}}
	exitCode := 0
	requester := &cancellationScreeningRequester{
		recordingRunner: &recordingRunner{err: runErr, body: runResolvedBody("resolved-entry", &exitCode, "success", 1, 0, 1, "unscreened command output")},
		afterRun:        afterRun,
	}
	kernel, err := newEffectKernel(nil, sessionGrant("session-a", autonomousMatrix()), registry, ledger,
		NewApprovalStore(), known, "screening-cancellation", "session-a", 1, "", requester, Attachments{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return kernel, ledger
}

func TestKernelCanceledScreeningPreservesTerminalCause(t *testing.T) {
	program := &cancelCause{why: "assistant process stopped", sentence: "the assistant process stopped the run"}
	lease := &RunLeaseError{Reason: content.TermOutputBudget, Err: context.Canceled}
	for _, tc := range []struct {
		name         string
		runErr       error
		contextCause error
		deadline     bool
		wantCause    error
		wantReason   content.TerminationReason
	}{
		{name: "disconnect", runErr: context.Canceled, wantCause: context.Canceled, wantReason: content.TermTransportGone},
		{name: "deadline", runErr: context.DeadlineExceeded, deadline: true, wantCause: context.DeadlineExceeded, wantReason: content.TermTimeout},
		{name: "completed output withheld after disconnect", wantCause: context.Canceled, wantReason: content.TermTransportGone},
		{name: "execution lease", runErr: lease, wantCause: lease, wantReason: content.TermOutputBudget},
		{name: "execution program cause", runErr: program, wantCause: program, wantReason: content.TermFailed},
		{name: "context program cause", runErr: context.Canceled, contextCause: program, wantCause: program, wantReason: content.TermFailed},
		{name: "context lease cause", runErr: context.Canceled, contextCause: lease, wantCause: lease, wantReason: content.TermOutputBudget},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			if tc.deadline {
				deadlineCtx, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				defer stop()
				ctx = deadlineCtx
			}
			known := &cancellationScreeningKnown{}
			kernel, ledger := cancellationScreeningKernel(t, tc.runErr, known, func(runCtx context.Context) {
				if !tc.deadline && runCtx.Err() != nil {
					t.Fatalf("execution started with an ended context: %v", runCtx.Err())
				}
				cancel(tc.contextCause)
			})
			out, err := kernel.Invoke(ctx, "session.run", "canceled-call", `{"command":"du -sh /"}`)
			if out != "" {
				t.Fatalf("released unscreened output: %q", out)
			}
			if !errors.Is(err, tc.wantCause) {
				t.Fatalf("error = %v, want terminal cause %v", err, tc.wantCause)
			}
			var screeningErr *EgressScreeningError
			if errors.As(err, &screeningErr) {
				t.Fatalf("cancellation misclassified as detector failure: %v", err)
			}
			if len(ledger.finished) != 1 || ledger.finished[0].TerminationReason != tc.wantReason || ledger.finished[0].Status != content.EntryFailure {
				t.Fatalf("attempt ending = %+v, want %s failure", ledger.finished, tc.wantReason)
			}
			if len(ledger.captures) != 0 {
				t.Fatal("unscreened output reached the durable result")
			}
			if len(ledger.finishCalls) != 1 || ledger.finishCalls[0].errAtCall != nil {
				t.Fatal("terminal attempt was not recorded on a detached context")
			}
		})
	}
}

func TestKernelGenuineScreeningFailureRemainsFailClosed(t *testing.T) {
	detectorFailure := errors.New("detector could not read credential inventory")
	for _, tc := range []struct {
		name    string
		cancel  bool
		failure error
	}{
		{name: "active run", failure: detectorFailure},
		{name: "canceled run with independent detector failure", cancel: true, failure: detectorFailure},
		{name: "detector cancellation without run cancellation", failure: context.Canceled},
		{name: "detector deadline without run deadline", failure: context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			known := &cancellationScreeningKnown{failure: tc.failure}
			kernel, ledger := cancellationScreeningKernel(t, errors.New("unscreened executor detail"), known, func(runCtx context.Context) {
				if runCtx.Err() != nil {
					t.Fatalf("execution started with an ended context: %v", runCtx.Err())
				}
				if tc.cancel {
					cancel()
				}
			})
			out, err := kernel.Invoke(ctx, "session.run", "failed-screening", `{"command":"du -sh /"}`)
			var screeningErr *EgressScreeningError
			if out != "" || !errors.As(err, &screeningErr) || !errors.Is(err, tc.failure) {
				t.Fatalf("screening failure changed classification or released bytes: %q %v", out, err)
			}
			if len(ledger.finished) != 1 || ledger.finished[0].TerminationReason != content.TermFailed || ledger.finished[0].Status != content.EntryFailure {
				t.Fatalf("genuine detector failure ending = %+v", ledger.finished)
			}
			if len(ledger.captures) != 0 {
				t.Fatal("unscreened executor detail reached the ledger result")
			}
		})
	}
}
