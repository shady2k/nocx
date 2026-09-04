package assistant

// The coordinator's two calls (nocx-dkawo.8).
//
// §7.2 of the orchestration mechanism design names five: one spawn primitive,
// say to a participant, report structurally, check the inbox, and ask what my
// session holds. Two of them are here — spawn and holdings — and the other
// three need more than one worker to mean anything, so they arrive with
// fan-out rather than being written now against nothing.
//
// NOTHING RESTS ON EITHER CALL. The backend holds the record and watches the
// workers whether or not the coordinator ever calls back: a coordinator that
// goes quiet loses its own promptness and nothing else. That is what makes
// these a convenience over an invariant rather than the mechanism, and it is
// the whole difference from the lease this design started with.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/wave"
)

// WaveRecord is the assistant's seam onto the wave record (AD-8): start one
// worker, and say what this session holds. The assistant depends on this and
// not on internal/wave's registrar, so a run can be tested against a double
// that never opens a pane.
type WaveRecord interface {
	Register(ctx context.Context, req wave.RegisterRequest) (wave.Participant, error)
	HeldBy(ctx context.Context, coordinatorSession string) ([]wave.Participant, error)
	// Undispatched is what the record still owes judgement on. It is read
	// BEFORE HeldBy, because HeldBy is the fetch that clears it (D8): asking
	// afterwards would always answer nothing, which is a truthful answer to
	// the wrong question.
	Undispatched() []wave.Fact
}

// waveParticipantResult is one row of what a coordinator is told. It restates
// the record's vocabulary and never invents one: the states are the record's
// own words, so what the model reads and what the store holds cannot drift.
type waveParticipantResult struct {
	ID      string `json:"id"`
	State   string `json:"state"`
	Task    string `json:"task"`
	Summary string `json:"summary,omitempty"`
	// NeedsJudgement marks a worker something happened to that this
	// coordinator has not been told about AND that the routing table decided
	// needs a decision. It is what makes the wake actionable: nocx types
	// "call wave.holdings", and this is what distinguishes the worker it was
	// about from the four that have not moved.
	//
	// A routine completion is deliberately NOT marked — a worker finishing
	// while others still run does not need the coordinator, which is the
	// whole of nocx-dkawo.4's table. Omitted rather than false, so a list
	// where nothing needs deciding reads as nothing needing deciding.
	NeedsJudgement bool `json:"needsJudgement,omitempty"`
}

type waveHoldingsResult struct {
	Participants []waveParticipantResult `json:"participants"`
}

type waveSpawnParams struct {
	Command string `json:"command"`
	Task    string `json:"task"`
}

type waveSpawnResult struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

func waveCoordinatorFrom(cap agenttools.Capability, tool string) (*agenttools.WaveCoordinator, error) {
	c, ok := cap.(*agenttools.WaveCoordinator)
	if !ok {
		return nil, fmt.Errorf("%s: capability is %T, not *agenttools.WaveCoordinator", tool, cap)
	}
	if c.Session() == "" {
		// A coordinator with no session cannot be answered about, and
		// answering an empty holdings would be indistinguishable from a
		// coordinator that holds nothing.
		return nil, fmt.Errorf("%s: this run has no session to answer about", tool)
	}
	return c, nil
}

// executeWaveHoldings answers D3: a coordinator asks what its SESSION holds
// and is told by name.
//
// It asks the session and not the run, because the run that spawned the worker
// has ended by the time this question matters — that is the entire situation it
// exists for. An empty list is an honest and ordinary answer.
func executeWaveHoldings(ctx context.Context, cap agenttools.Capability, _ json.RawMessage, seams toolSeams) (string, error) {
	coordinator, err := waveCoordinatorFrom(cap, "wave.holdings")
	if err != nil {
		return "", err
	}
	if seams.waves == nil {
		return "", errors.New("wave.holdings: this backend keeps no wave record")
	}
	// Read what is owed BEFORE the fetch, because the fetch is what clears
	// it. The other order would answer this question with the record's state
	// after it had been answered, which is always "nothing new".
	owed := make(map[wave.ParticipantID]bool)
	for _, f := range seams.waves.Undispatched() {
		owed[f.Participant] = true
	}
	held, err := seams.waves.HeldBy(ctx, coordinator.Session())
	if err != nil {
		return "", fmt.Errorf("wave.holdings: %w", err)
	}
	out := waveHoldingsResult{Participants: make([]waveParticipantResult, 0, len(held))}
	for _, p := range held {
		row := waveParticipantResult{
			ID: string(p.ID), State: string(p.State), Task: p.Task,
			NeedsJudgement: owed[p.ID],
		}
		if p.Declared != nil {
			row.Summary = p.Declared.Summary
		}
		out.Participants = append(out.Participants, row)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("wave.holdings: result: %w", err)
	}
	return string(raw), nil
}

// executeWaveSpawn starts one worker and returns only when it is LIVE.
//
// Live means its enrolment arrived, which is what proves the agent started —
// never that this call returned. A spawn that did not reach live is an error
// and not a result, so there is no half-answer for a coordinator to
// misinterpret as a worker it can address.
func executeWaveSpawn(ctx context.Context, cap agenttools.Capability, args json.RawMessage, seams toolSeams) (string, error) {
	coordinator, err := waveCoordinatorFrom(cap, "wave.spawn")
	if err != nil {
		return "", err
	}
	if seams.waves == nil {
		return "", errors.New("wave.spawn: this backend keeps no wave record")
	}
	var p waveSpawnParams
	if argErr := json.Unmarshal(args, &p); argErr != nil {
		return "", fmt.Errorf("wave.spawn: %w", argErr)
	}
	if p.Command == "" || p.Task == "" {
		return "", errors.New("wave.spawn: a worker needs both a command to start it and a task to do")
	}
	// The environment is checked against the CAPABILITY, which holds only
	// what the run's grant named. A spawn outside it is refused and the
	// refusal names what was available; escalating instead is a property of a
	// policy row rather than a special case for one tool.
	environment := seams.waveEnvironment
	if !coordinator.MaySpawnInto(environment) {
		return "", fmt.Errorf("wave.spawn: this run may not start a worker in %q; it may start one in %v",
			environment, coordinator.Environments())
	}
	participant, err := seams.waves.Register(ctx, wave.RegisterRequest{
		CoordinatorSession: coordinator.Session(),
		Role:               wave.RoleWorker,
		Task:               p.Task,
		Command:            p.Command,
		Environment:        environment,
		CreatedByRunID:     seams.runID,
	})
	if err != nil {
		return "", fmt.Errorf("wave.spawn: %w", err)
	}
	raw, err := json.Marshal(waveSpawnResult{
		ID:    string(participant.ID),
		State: string(participant.State),
	})
	if err != nil {
		return "", fmt.Errorf("wave.spawn: result: %w", err)
	}
	return string(raw), nil
}
