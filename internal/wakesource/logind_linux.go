//go:build linux

package wakesource

// logind's PrepareForSleep, subscribed on the system bus.
//
// THE SIGNAL IS A BROADCAST, not a privileged request: any user may subscribe
// to `org.freedesktop.login1.Manager.PrepareForSleep` on the system bus. What
// needs policy (and usually root) is an INHIBITOR LOCK — the thing that
// DELAYS a sleep — and this source never asks for one: it only listens.
//
// One message per edge: `b true` is said before the machine sleeps, `b false`
// when it comes back (or when the sleep was cancelled, which is the same fact
// for a supervisor). The payload is decoded here into the package's Event and
// nothing else leaves this file.

import (
	"fmt"

	"github.com/godbus/dbus/v5"
	"github.com/shady2k/nocx/internal/monoclock"
)

const (
	login1Service = "org.freedesktop.login1"
	login1Path    = dbus.ObjectPath("/org/freedesktop/login1")
	login1Manager = "org.freedesktop.login1.Manager"
	// login1Sleep is the member this package listens to. Its one argument is
	// `b active`: true before the sleep, false after it.
	login1Sleep = "PrepareForSleep"
)

// busConn is the two things this input needs from a bus connection, so a test
// can drive it with a fake and with a private daemon alike.
type busConn interface {
	AddMatch(rule string) error
	Signal(chan<- *dbus.Signal)
}

// dbusBus is *dbus.Conn reduced to that seam.
type dbusBus struct{ conn *dbus.Conn }

func (b dbusBus) AddMatch(rule string) error {
	return b.conn.BusObject().Call("org.freedesktop.DBus.AddMatch", 0, rule).Err
}

func (b dbusBus) Signal(ch chan<- *dbus.Signal) { b.conn.Signal(ch) }

// osEvents answers logind's sleep/wake events from the system bus. A machine
// with no system bus or no logind answers an error, which the source reports
// once and then covers with the clock comparison.
func osEvents() (<-chan Event, error) {
	conn, err := dbus.SystemBus()
	if err != nil {
		return nil, fmt.Errorf("the system bus: %w", err)
	}
	return login1Input(dbusBus{conn: conn})
}

// login1Input subscribes to PrepareForSleep on conn and answers the events it
// carries. The subscription is a match rule any user may add; the bus answers
// errors for a machine with no logind behind it.
func login1Input(conn busConn) (<-chan Event, error) {
	rule := fmt.Sprintf(
		"type='signal',sender='%s',interface='%s',member='%s'",
		login1Service, login1Manager, login1Sleep,
	)
	if err := conn.AddMatch(rule); err != nil {
		return nil, fmt.Errorf("subscribe to logind's %s: %w", login1Sleep, err)
	}

	signals := make(chan *dbus.Signal, 8)
	conn.Signal(signals)

	events := make(chan Event, 8)
	go func() {
		// Closed when the connection goes away: the merge logs an ended input
		// once and carries on with the clocks.
		defer close(events)
		for sig := range signals {
			ev, ok := sleepEvent(sig)
			if !ok {
				continue
			}
			select {
			case events <- ev:
			default:
				// A burst of edges nobody read is one fact for the merge; the
				// newest is not worth blocking the bus's reader for.
			}
		}
	}()
	return events, nil
}

// sleepEvent decodes one bus signal into this package's Event, and reports
// false for anything that is not a logind sleep edge.
func sleepEvent(sig *dbus.Signal) (Event, bool) {
	if sig == nil || sig.Path != login1Path || sig.Name != login1Manager+"."+login1Sleep {
		return Event{}, false
	}
	if len(sig.Body) != 1 {
		return Event{}, false
	}
	active, ok := sig.Body[0].(bool)
	if !ok {
		return Event{}, false
	}
	kind := EventWoke
	if active {
		kind = EventWillSleep
	}
	return Event{Kind: kind, At: monoclock.Now()}, true
}
