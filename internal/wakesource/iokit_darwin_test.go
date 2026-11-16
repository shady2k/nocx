//go:build darwin && nocx_local_ssh

package wakesource

import "testing"

func TestPowerCallbackAcknowledgesSleepWithoutAReader(t *testing.T) {
	events := make(chan Event, 1)
	events <- Event{} // A full queue must not delay the OS acknowledgement.
	root := uint32(42)
	var acknowledgements []uintptr
	callback := powerCallback(events, &root, func(port uint32, argument uintptr) int32 {
		if port != root {
			t.Fatalf("acknowledged port %d, want %d", port, root)
		}
		acknowledgements = append(acknowledgements, argument)
		return 0
	})
	callback(0, 0, canSystemSleep, 100)
	callback(0, 0, systemWillSleep, 200)
	if len(acknowledgements) != 2 || acknowledgements[0] != 100 || acknowledgements[1] != 200 {
		t.Fatalf("sleep acknowledgements: %v", acknowledgements)
	}
	<-events
	for _, step := range []struct {
		message uint32
		kind    EventKind
	}{
		{systemWillSleep, EventWillSleep},
		{systemHasPoweredOn, EventWoke},
		{systemWillNotSleep, EventWoke},
	} {
		callback(0, 0, step.message, 300)
		select {
		case event := <-events:
			if event.Kind != step.kind {
				t.Fatalf("message %x: kind=%v want %v", step.message, event.Kind, step.kind)
			}
		default:
			t.Fatalf("message %x produced no edge", step.message)
		}
	}
	callback(0, 0, canSystemSleep, 400)
	callback(0, 0, 0xe0000320, 0) // powered, but not yet usable
	select {
	case event := <-events:
		t.Fatalf("a proposal or will-power-on produced an edge: %+v", event)
	default:
	}
}
