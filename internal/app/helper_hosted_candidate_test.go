package app

import (
	"context"
	"errors"
	"testing"

	helperclient "github.com/shady2k/nocx/internal/helper/client"
)

func TestHostedSpawnCandidatePublishIsIdempotent(t *testing.T) {
	calls := 0
	want := hostedSpawnResult{Entry: helperclient.SessionEntry{HostSessionID: helperclient.HostSessionID{Generation: "generation", Session: "candidate"}}}
	candidate := &hostedSpawnCandidate{publish: func(context.Context) (hostedSpawnResult, error) {
		calls++
		return want, nil
	}}
	first, err := candidate.Publish(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := candidate.Publish(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || first.Entry.HostSessionID != second.Entry.HostSessionID || first.Entry.HostSessionID != want.Entry.HostSessionID {
		t.Fatalf("publish calls=%d first=%+v second=%+v", calls, first.Entry.HostSessionID, second.Entry.HostSessionID)
	}
}

func TestHostedSpawnCandidateAbortIsIdempotentAndBlocksPublish(t *testing.T) {
	cleanupErr := errors.New("close uncertain")
	aborts, publishes := 0, 0
	candidate := &hostedSpawnCandidate{
		publish: func(context.Context) (hostedSpawnResult, error) {
			publishes++
			return hostedSpawnResult{}, nil
		},
		abort: func(context.Context) error {
			aborts++
			return cleanupErr
		},
	}
	for range 2 {
		if err := candidate.Abort(context.Background()); !errors.Is(err, cleanupErr) {
			t.Fatalf("abort error=%v, want cleanup uncertainty", err)
		}
	}
	if _, err := candidate.Publish(context.Background()); err == nil {
		t.Fatal("an aborted candidate was published")
	}
	if aborts != 1 || publishes != 0 {
		t.Fatalf("abort calls=%d publish calls=%d", aborts, publishes)
	}
}
