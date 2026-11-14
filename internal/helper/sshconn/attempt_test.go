//go:build nocx_local_ssh

package sshconn

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/shady2k/nocx/internal/hostconn"
	"github.com/shady2k/nocx/internal/hostconn/hostconntest"
)

func TestAttemptRenewsOnNewStageAndPausesForPerson(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clock := hostconntest.New()
		err := runAttemptOnClock(context.Background(), clock, func(ctx context.Context) error {
			clock.Advance(9 * time.Minute)
			synctest.Wait()
			hostconn.ReportAttempt(ctx, hostconn.Event{Kind: hostconn.StageReached, Stage: hostconn.StageTCP})
			clock.Advance(9 * time.Minute)
			synctest.Wait()
			if ctx.Err() != nil {
				t.Fatal("old stage deadline ended a new stage")
			}
			hostconn.ReportAttempt(ctx, hostconn.Event{Kind: hostconn.NeedsPerson, Wait: hostconn.WaitPassword})
			clock.Advance(time.Hour)
			synctest.Wait()
			if ctx.Err() != nil {
				t.Fatal("a person's wait consumed the silence budget")
			}
			hostconn.ReportAttempt(ctx, hostconn.Event{Kind: hostconn.PersonAnswered})
			clock.Advance(9 * time.Minute)
			synctest.Wait()
			if ctx.Err() != nil {
				t.Fatal("answered wait did not resume a fresh phase")
			}
			clock.Advance(time.Minute)
			synctest.Wait()
			if ctx.Err() == nil {
				t.Fatal("attempt did not expire after its current phase budget")
			}
			<-ctx.Done()
			return ctx.Err()
		})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("deadline returned %v", err)
		}
	})
}

func TestDuplicateStageDoesNotRenewAttempt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clock := hostconntest.New()
		err := runAttemptOnClock(context.Background(), clock, func(ctx context.Context) error {
			event := hostconn.Event{Kind: hostconn.StageReached, Stage: hostconn.StageTCP}
			hostconn.ReportAttempt(ctx, event)
			clock.Advance(9 * time.Minute)
			synctest.Wait()
			hostconn.ReportAttempt(ctx, event)
			clock.Advance(time.Minute)
			synctest.Wait()
			if ctx.Err() == nil {
				t.Fatal("attempt did not expire after its current phase budget")
			}
			<-ctx.Done()
			return ctx.Err()
		})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("deadline returned %v", err)
		}
	})
}
