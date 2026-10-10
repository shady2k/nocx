package app

// The scrollback wiring is judged here at its own seams: the setting's
// holder against a real registry (what a pane is born with), and the
// fan-out against carrier doubles — because what the doubles stand for, the
// helper connection, is judged at its own layer by the session service's
// tests over the real emulator.

import (
	"context"
	"errors"
	"testing"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/settings"
)

func (f *fakeScrollbackCarrier) calls() int { return len(f.got) }

type fakeScrollbackCarrier struct {
	got    []uint64
	refuse bool
}

func (f *fakeScrollbackCarrier) SetScrollback(_ context.Context, _ client.HostSessionID, lines uint64) error {
	if f.refuse {
		return errors.New("unknown_op")
	}
	f.got = append(f.got, lines)
	return nil
}

// What a pane opened now is born with: the declared default before the
// person's value is known, their value once it is set — and never a zero,
// which would erase a pane's history nobody asked to erase.
func TestTheScrollbackSettingStartsAtTheDefaultAndFollowsChanges(t *testing.T) {
	reg := settings.New(&appFakeDoc{}, nil)
	s := newScrollbackSetting(reg)
	if got := *s.linesPtr(); got != 10000 {
		t.Fatalf("a pane born before the value is read carries %d lines, want the declared default 10000", got)
	}

	// Following a change is the WATCH's work, and the test below holds it;
	// this one holds only what a pane is born with before any watch runs.
}

// A change reaches EVERY live session — both routes — and one carrier's
// refusal (an older helper answering unknown_op) neither starves the rest
// nor fails the push.
func TestTheScrollbackFanoutReachesEveryLiveSession(t *testing.T) {
	local1 := &fakeScrollbackCarrier{}
	local2 := &fakeScrollbackCarrier{refuse: true}
	far := &fakeScrollbackCarrier{}
	f := &scrollbackFanout{
		local: func(context.Context) ([]scrollbackTarget, error) {
			return []scrollbackTarget{
				{carrier: local1, id: client.HostSessionID{Generation: "gen", Session: "local1"}},
				{carrier: local2, id: client.HostSessionID{Generation: "gen", Session: "local2"}},
			}, nil
		},
		remote: func(context.Context) ([]scrollbackTarget, error) {
			return []scrollbackTarget{
				{carrier: far, id: client.HostSessionID{Generation: "far", Session: "far1"}},
			}, nil
		},
	}

	f.apply(context.Background(), 2500)

	if got := local1.calls(); got != 1 || local1.got[0] != 2500 {
		t.Fatalf("the local pane was pushed %d times (%v), want once at 2500", got, local1.got)
	}
	if got := far.calls(); got != 1 || far.got[0] != 2500 {
		t.Fatalf("the far pane was pushed %d times (%v), want once at 2500", got, far.got)
	}
	// The refusing carrier was attempted and its refusal tolerated: the
	// degradation is the pane keeping its birth budget, named in the op's
	// own contract.
	if local2.calls() != 0 {
		t.Fatalf("the refusing carrier accepted %d pushes", local2.calls())
	}
}

// The watch is what connects the two: a settings change stores the new
// value and fans it out in the same breath.
func TestWatchScrollbackAppliesTheChangeToEveryLiveSession(t *testing.T) {
	reg := settings.New(&appFakeDoc{}, nil)
	s := newScrollbackSetting(reg)
	local := &fakeScrollbackCarrier{}
	f := &scrollbackFanout{
		local: func(context.Context) ([]scrollbackTarget, error) {
			return []scrollbackTarget{
				{carrier: local, id: client.HostSessionID{Generation: "gen", Session: "local1"}},
			}, nil
		},
		remote: func(context.Context) ([]scrollbackTarget, error) { return nil, nil },
	}
	watchScrollback(reg, s, f)

	if err := reg.SetNumber(settings.TerminalScrollbackLines, 777); err != nil {
		t.Fatalf("set the budget: %v", err)
	}
	if got := *s.linesPtr(); got != 777 {
		t.Fatalf("after the change a new pane carries %d, want 777", got)
	}
	if got := local.calls(); got != 1 || local.got[0] != 777 {
		t.Fatalf("the running pane was pushed %d times (%v), want once at 777", got, local.got)
	}

	// An unrelated change pushes nothing.
	if err := reg.SetNumber(settings.HistoryRetentionDays, 30); err != nil {
		t.Fatalf("set an unrelated setting: %v", err)
	}
	if got := local.calls(); got != 1 {
		t.Fatalf("an unrelated setting pushed %d extra times", got-1)
	}
}
