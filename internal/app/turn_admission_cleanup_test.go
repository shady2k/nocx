package app

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/toolendpoint/turnbind"
	"github.com/shady2k/nocx/internal/turnbearer"
)

type blockedAdmissionTools struct {
	testTurnTools
	entered   chan struct{}
	cancelled chan struct{}
	finish    chan struct{}
}

func (b *blockedAdmissionTools) Admit(ctx context.Context) (assistant.ToolInvocation, error) {
	close(b.entered)
	<-ctx.Done()
	close(b.cancelled)
	<-b.finish
	return assistant.ToolInvocation{}, ctx.Err()
}

func TestTurnEndingWaitsForUnpublishedAdmissionCleanup(t *testing.T) {
	for _, retireSession := range []bool{false, true} {
		name := "end-turn"
		if retireSession {
			name = "retire"
		}
		t.Run(name, func(t *testing.T) {
			stand := newTurnStand(t, &turnDispatcher{})
			tools := &blockedAdmissionTools{testTurnTools: testTurnTools{ctx: agenttools.RunContext{RunID: "blocked-admit", Session: string(stand.sess.ID())}}, entered: make(chan struct{}), cancelled: make(chan struct{}), finish: make(chan struct{})}
			bridge, retire, err := mintTurn(t, stand, turnbearer.Authority{Tools: tools})
			if err != nil {
				t.Fatal(err)
			}
			var finish sync.Once
			finishAdmission := func() { finish.Do(func() { close(tools.finish) }) }
			t.Cleanup(finishAdmission)
			conn, err := net.Dial("unix", stand.endpoint.SocketPath())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			preamble, err := turnbind.EncodeToken(envToken(t, bridge.Env))
			if err != nil {
				t.Fatal(err)
			}
			if _, writeErr := conn.Write(preamble); writeErr != nil {
				t.Fatal(writeErr)
			}
			select {
			case <-tools.entered:
			case <-time.After(10 * time.Second):
				t.Fatal("admission never started")
			}
			ended := make(chan struct{})
			go func() {
				if retireSession {
					retire()
				} else {
					stand.minter.EndTurn("blocked-admit")
				}
				close(ended)
			}()
			select {
			case <-tools.cancelled:
			case <-time.After(10 * time.Second):
				finishAdmission()
				t.Fatal("turn end did not cancel unpublished admission")
			}
			select {
			case <-ended:
				finishAdmission()
				t.Fatal("turn ended before admission returned and cleaned up")
			default:
			}
			finishAdmission()
			select {
			case <-ended:
			case <-time.After(10 * time.Second):
				t.Fatal("turn did not finish after admission cleanup")
			}
			// EndTurn/Retire has returned: its slot is already free, and the
			// peer observes EOF without supplying another request.
			release, acquired := stand.auth.turnSlots.acquire(session.ID("blocked-admit"))
			if !acquired {
				t.Fatal("turn ended with its unpublished caller slot held")
			}
			release()
			if deadlineErr := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); deadlineErr != nil {
				t.Fatal(deadlineErr)
			}
			_, err = io.ReadAll(conn)
			if err != nil && !errors.Is(err, io.EOF) {
				t.Fatalf("unpublished socket did not close: %v", err)
			}
			if calls := stand.dispatcher.recorded(); len(calls) != 0 {
				t.Fatal("unpublished admission dispatched a call")
			}
		})
	}
}
