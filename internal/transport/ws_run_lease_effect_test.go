package transport

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/sys/unix"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/session"
)

func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// readSessionMarker waits on the real data-plane reader until the shell emits
// prefix followed by decimal digits. The shell echoes the submitted line
// first, so requiring digits after the prefix distinguishes execution output
// from the echo without waiting on a duration.
func readSessionMarker(t *testing.T, conn *websocket.Conn, sid, prefix string) int {
	t.Helper()
	var output strings.Builder
	for {
		mt, payload, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read session marker %q: %v", prefix, err)
		}
		if mt != websocket.BinaryMessage {
			continue
		}
		frame, err := DecodeFrame(payload)
		if err != nil || string(session.IDFromBytes(frame.SessionID)) != sid {
			continue
		}
		output.Write(frame.Payload)
		text := output.String()
		for from := 0; from < len(text); {
			i := strings.Index(text[from:], prefix)
			if i < 0 {
				break
			}
			i += from + len(prefix)
			j := i
			for j < len(text) && text[j] >= '0' && text[j] <= '9' {
				j++
			}
			if j > i {
				pid, err := strconv.Atoi(text[i:j])
				if err != nil || pid <= 0 {
					t.Fatalf("session marker %q contained invalid pid %q", prefix, text[i:j])
				}
				return pid
			}
			from = i
		}
	}
}

func readRunState(t *testing.T, tap *socketTap, wantRunID int64) string {
	t.Helper()
	for {
		raw, ok := <-tap.msgs
		if !ok {
			t.Fatal("socket closed before agent.runState")
		}
		var notification struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(raw, &notification); err != nil || notification.Method != "agent.runState" {
			continue
		}
		var state struct {
			RunID int64  `json:"runId"`
			State string `json:"state"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal(notification.Params, &state); err != nil {
			t.Fatalf("agent.runState unmarshal: %v\nraw: %s", err, notification.Params)
		}
		if state.RunID != wantRunID || state.State != "failed" {
			continue
		}
		return state.Error
	}
}

// TestRunLease_BoundBeforeForkLeavesNoProcessOrSignal proves the complete
// effect on a real session: the broker delivery barrier holds the request
// before the renderer can submit command bytes, the bound expires it, and the
// shell remains usable while neither the command nor a signal exists.
func TestRunLease_BoundBeforeForkLeavesNoProcessOrSignal(t *testing.T) {
	h := newRunLeaseHarness(t, RunLeaseConfig{WallClock: time.Hour})
	h.createEndpointAt()
	sid := h.openSession(t)

	dir := t.TempDir()
	signalMarker := filepath.Join(dir, "signal.marker")
	shellPIDFile := filepath.Join(dir, "shell.pid")
	commandPIDFile := filepath.Join(dir, "command.pid")
	readyPrefix := "__lease_ready__:"
	probePrefix := "__lease_probe__:"

	setupAction := "printf signal > " + shellSingleQuote(signalMarker)
	setup := "trap " + shellSingleQuote(setupAction) + " INT TERM; " +
		"printf '%s' \"$$\" > " + shellSingleQuote(shellPIDFile) + "; " +
		"printf '" + readyPrefix + "%s\\n' \"$$\""
	tapCommand := func(command string) {
		submitCommand(t, h.conn, sid, command)
	}
	tapCommand(setup)
	shellPID := readSessionMarker(t, h.conn, sid, readyPrefix)

	commandBody := "printf %s $$ > " + shellSingleQuote(commandPIDFile) + "; exec sleep 100"
	command := "sh -c " + shellSingleQuote(commandBody)

	deliverStarted := make(chan struct{})
	releaseDeliver := make(chan struct{})
	var deliverOnce sync.Once
	var releaseOnce sync.Once
	broker := NewBroker(
		func() []Conn { return []Conn{&harnessConn{}} },
		func(Conn, string, json.RawMessage) error {
			deliverOnce.Do(func() { close(deliverStarted) })
			<-releaseDeliver
			return errors.New("delivery held before command submission")
		},
	)
	h.ws.broker = broker

	res := h.askRunsTool(sid, command)
	tap := newSocketTap(h.conn)
	<-deliverStarted

	broker.mu.Lock()
	var lease *runLease
	for _, candidate := range broker.runLeases {
		lease = candidate
		break
	}
	broker.mu.Unlock()
	if lease == nil {
		t.Fatal("broker did not register the run lease before delivery")
	}
	lease.mu.Lock()
	delivered := lease.submissionDelivered
	lease.mu.Unlock()
	if delivered {
		t.Fatal("delivery barrier marked the submission delivered")
	}
	lease.fire(content.TermTimeout)
	releaseOnce.Do(func() { close(releaseDeliver) })
	defer releaseOnce.Do(func() { close(releaseDeliver) })

	runError := readRunState(t, tap, res.RunID)
	if !strings.Contains(runError, "run submission expired before execution started") {
		t.Fatalf("agent.runState error = %q, want the submission-expired sentence", runError)
	}
	for _, forbidden := range []string{"output", "budget", "bounded", "truncated", "terminalized"} {
		if strings.Contains(strings.ToLower(runError), forbidden) {
			t.Fatalf("agent.runState error = %q, must not describe output limiting or terminalization", runError)
		}
	}

	// A probe is an observable post-run barrier. If escalation had sent KILL,
	// the shell could not answer it; if INT or TERM arrived, its trap would
	// have produced the marker before this response was emitted.
	tapCommand("printf '" + probePrefix + "%s\\n' \"$$\"")
	readSessionMarkerFromTap(t, tap, sid, probePrefix)

	if _, err := os.Stat(commandPIDFile); !os.IsNotExist(err) {
		t.Fatalf("command pid file exists after expired submission: %v", err)
	}
	if _, err := os.Stat(signalMarker); !os.IsNotExist(err) {
		t.Fatalf("signal marker exists after expired submission: %v", err)
	}
	if err := unix.Kill(shellPID, 0); err != nil {
		t.Fatalf("shell pid %d is not alive after expired submission: %v", shellPID, err)
	}
}

func readSessionMarkerFromTap(t *testing.T, tap *socketTap, sid, prefix string) {
	t.Helper()
	var output strings.Builder
	for {
		frame, ok := <-tap.data
		if !ok {
			t.Fatalf("socket closed before session marker %q", prefix)
		}
		if string(session.IDFromBytes(frame.SessionID)) != sid {
			continue
		}
		output.Write(frame.Payload)
		text := output.String()
		for from := 0; from < len(text); {
			i := strings.Index(text[from:], prefix)
			if i < 0 {
				break
			}
			i += from + len(prefix)
			j := i
			for j < len(text) && text[j] >= '0' && text[j] <= '9' {
				j++
			}
			if j > i {
				return
			}
			from = i
		}
	}
}
