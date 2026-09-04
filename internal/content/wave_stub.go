package content

import (
	"context"
	"fmt"

	"github.com/shady2k/nocx/internal/wave"
)

// waveStub is what the wave record is when the encrypted store never opened.
//
// It REFUSES, and every other stub in this file is a no-op, which is the
// difference worth stating. A no-op is right for a surface whose absence
// degrades an experience: an unrecorded command still ran. It is wrong here,
// because the wave record is the thing supervision IS. A no-op store would
// accept a registration, return no error, record nothing, and leave a real
// agent running in a real pane with nothing in the product that knows it
// exists — which is precisely the defect the record was built to make
// impossible, arrived at through the back door of a degraded start.
//
// So failure is closed (D4): no store, no orchestration. The refusal names the
// cause, so the surface that asked can say it rather than logging it and
// carrying on.
type waveStub struct{ log logger }

// logger is the subset of the store's logger this stub uses.
type logger interface {
	Info(msg string, args ...any)
}

// errNoRecord is what every method answers. It wraps nothing from wave because
// the condition is content's: the store did not open.
func (w *waveStub) errNoRecord(op string) error {
	w.log.Info("content stub: wave record unavailable", "op", op)
	return fmt.Errorf("content: wave %s: %w", op, wave.ErrRecordUnavailable)
}

func (w *waveStub) EnsureWave(context.Context, wave.ID, string) error {
	return w.errNoRecord("ensure wave")
}

func (w *waveStub) NonTerminal(context.Context, wave.ID) ([]wave.Participant, error) {
	return nil, w.errNoRecord("list open participants")
}

func (w *waveStub) AllNonTerminal(context.Context) ([]wave.Participant, error) {
	return nil, w.errNoRecord("list open participants")
}

func (w *waveStub) CommitPrepared(context.Context, wave.Participant) error {
	return w.errNoRecord("commit participant")
}

func (w *waveStub) MarkLive(context.Context, wave.ParticipantID, wave.Liveness) error {
	return w.errNoRecord("mark live")
}

func (w *waveStub) Terminalize(context.Context, wave.ParticipantID, wave.State) error {
	return w.errNoRecord("terminalize")
}

func (w *waveStub) RecordDeclaration(context.Context, wave.ParticipantID, wave.Declaration) (wave.Participant, error) {
	return wave.Participant{}, w.errNoRecord("record declaration")
}

func (w *waveStub) RecordExit(context.Context, wave.ParticipantID, wave.Exit) (wave.Participant, error) {
	return wave.Participant{}, w.errNoRecord("record exit")
}

func (w *waveStub) PutDelegation(context.Context, wave.Delegation) error {
	return w.errNoRecord("put delegation")
}

func (w *waveStub) Participant(context.Context, wave.ParticipantID) (wave.Participant, error) {
	return wave.Participant{}, w.errNoRecord("read participant")
}

func (w *waveStub) CoordinatorSession(context.Context, wave.ID) (string, error) {
	return "", w.errNoRecord("read the coordinator of a wave")
}

func (w *waveStub) HeldBy(context.Context, string) ([]wave.Participant, error) {
	return nil, w.errNoRecord("read holdings")
}
