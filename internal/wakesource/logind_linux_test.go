//go:build linux

package wakesource

// The logind input, twice: the decode against a fake connection, and the whole
// subscription against a private dbus-daemon this test starts. The daemon is a
// session bus, so the login1 service is a name a test can own — what is under
// test here is this package's rule, member and decode, not logind itself.

import (
	"bufio"
	"os/exec"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/shady2k/nocx/internal/waittest"
)

// fakeBus is the two methods the input uses, with a channel the test speaks
// through.
type fakeBus struct {
	rule string
	err  error
	sigs chan<- *dbus.Signal
}

func (f *fakeBus) AddMatch(rule string) error {
	f.rule = rule
	return f.err
}

func (f *fakeBus) Signal(ch chan<- *dbus.Signal) { f.sigs = ch }

func TestTheLogindInputDecodesOnlyPrepareForSleep(t *testing.T) {
	bus := &fakeBus{}
	events, err := login1Input(bus)
	if err != nil {
		t.Fatalf("login1Input: %v", err)
	}
	if bus.rule == "" {
		t.Fatal("the input subscribed to nothing")
	}
	for _, want := range []string{login1Service, login1Manager, login1Sleep} {
		if !contains(bus.rule, want) {
			t.Fatalf("the match rule %q does not name %q", bus.rule, want)
		}
	}

	// Anything that is not a login1 sleep edge is dropped.
	bus.sigs <- &dbus.Signal{Path: "/elsewhere", Name: login1Manager + "." + login1Sleep, Body: []any{true}}
	bus.sigs <- &dbus.Signal{Path: login1Path, Name: "org.freedesktop.login1.Manager.Lock", Body: []any{true}}
	bus.sigs <- &dbus.Signal{Path: login1Path, Name: login1Manager + "." + login1Sleep, Body: []any{"yes"}}
	bus.sigs <- &dbus.Signal{Path: login1Path, Name: login1Manager + "." + login1Sleep}

	// The two edges, in order.
	bus.sigs <- &dbus.Signal{Path: login1Path, Name: login1Manager + "." + login1Sleep, Body: []any{true}}
	bus.sigs <- &dbus.Signal{Path: login1Path, Name: login1Manager + "." + login1Sleep, Body: []any{false}}

	first := nextEvent(t, events)
	if first.Kind != EventWillSleep {
		t.Fatalf("PrepareForSleep(true) reads %v, want the pre-sleep edge", first.Kind)
	}
	second := nextEvent(t, events)
	if second.Kind != EventWoke {
		t.Fatalf("PrepareForSleep(false) reads %v, want the wake", second.Kind)
	}
}

// TestTheLogindInputOnAPrivateBus is the same input against a real dbus-daemon.
// The service name is the real one, owned by this test; the bus is a session
// bus started here because a CI machine has no system bus with logind on it.
func TestTheLogindInputOnAPrivateBus(t *testing.T) {
	address := startPrivateBus(t)

	listener, err := dbus.Dial(address)
	if err != nil {
		t.Fatalf("dial the private bus: %v", err)
	}
	if authErr := listener.Auth(nil); authErr != nil {
		t.Fatalf("authenticate the listener: %v", authErr)
	}
	if helloErr := listener.Hello(); helloErr != nil {
		t.Fatalf("hello (listener): %v", helloErr)
	}
	t.Cleanup(func() { _ = listener.Close() })

	events, err := login1Input(dbusBus{conn: listener})
	if err != nil {
		t.Fatalf("login1Input: %v", err)
	}

	sender, err := dbus.Dial(address)
	if err != nil {
		t.Fatalf("dial the private bus (sender): %v", err)
	}
	if authErr := sender.Auth(nil); authErr != nil {
		t.Fatalf("authenticate the sender: %v", authErr)
	}
	if helloErr := sender.Hello(); helloErr != nil {
		t.Fatalf("hello (sender): %v", helloErr)
	}
	t.Cleanup(func() { _ = sender.Close() })
	if _, err := sender.RequestName(login1Service, dbus.NameFlagDoNotQueue); err != nil {
		t.Fatalf("own %s on the private bus: %v", login1Service, err)
	}

	emit := func(active bool) {
		t.Helper()
		if err := sender.Emit(login1Path, login1Manager+"."+login1Sleep, active); err != nil {
			t.Fatalf("emit PrepareForSleep(%v): %v", active, err)
		}
	}
	emit(true)
	if ev := nextEvent(t, events); ev.Kind != EventWillSleep {
		t.Fatalf("the bus's PrepareForSleep(true) reads %v, want the pre-sleep edge", ev.Kind)
	}
	emit(false)
	if ev := nextEvent(t, events); ev.Kind != EventWoke {
		t.Fatalf("the bus's PrepareForSleep(false) reads %v, want the wake", ev.Kind)
	}
}

// nextEvent waits for one decoded event and fails if none arrives.
func nextEvent(t *testing.T, events <-chan Event) Event {
	t.Helper()
	var got Event
	waittest.WaitFor(t, "an event from the input", func() bool {
		select {
		case got = <-events:
			return true
		default:
			return false
		}
	})
	return got
}

// startPrivateBus starts a private dbus-daemon and answers its address. A
// machine without dbus-daemon skips the test rather than pretending.
func startPrivateBus(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("dbus-daemon is not installed; the decode is still covered by the fake")
	}
	cmd := exec.Command(bin, "--session", "--nofork", "--print-address=1") // #nosec G204 -- the binary is dbus-daemon, found on PATH, with fixed arguments
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("dbus-daemon stdout: %v", err)
	}
	if startErr := cmd.Start(); startErr != nil {
		t.Fatalf("start dbus-daemon: %v", startErr)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("read dbus-daemon's address: %v", err)
	}
	return line[:len(line)-1]
}

// contains is strings.Contains without the import, kept tiny on purpose.
func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

var _ = time.Second
