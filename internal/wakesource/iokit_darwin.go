//go:build darwin && cgo && nocx_local_ssh

package wakesource

// IOKit's system power notifications, for the LOCAL helper's Darwin build.
//
// The build tag is the boundary: this file carries cgo and links IOKit, and the
// helper that is installed on somebody else's machine is built WITHOUT
// nocx_local_ssh (the two are different bytes of one repository, D11). The far
// helper therefore never links this, which TestDeployedHelperDependencyGraphHas
// NoSSHClient and its siblings enforce.
//
// WHAT IOKIT GIVES
//
// IORegisterForSystemPower delivers the sleep and wake edges a process can act
// on:
//
//   - kIOMessageSystemWillSleep — the machine is going to sleep NOW. The system
//     waits for IOAllowPowerChange before it does, so the supervisors are told
//     first and the change is allowed at once.
//   - kIOMessageSystemHasPoweredOn — the machine has woken. THIS IS NOT ALWAYS A
//     WAKE FOR A PERSON: a dark wake (Power Nap, maintenance, a network
//     daemon) powers the CPU and the network without the display or the user's
//     session. IOKit answers the distinction through IOPMCopyCurrentCapabilities:
//     kIOPMSystemCapabilityGraphics is set on a full wake and cleared on a dark
//     one, so that bit is what this file reads. A dark wake is reported as such
//     and produces no wake signal, and it suppresses the clock input (the gap
//     a power nap leaves between the two clocks is the nap, not a resume).
//   - kIOMessageSystemWillPowerOn — the machine is coming back but is not
//     usable yet; the powered-on edge above is the wake.
//   - kIOMessageSystemWillNotSleep — the sleep was cancelled. For a connection
//     it is the same fact as a wake: one proof of life and the probe cadence
//     resumes.
//
// The callback runs on a CFRunLoop that this file owns, on a thread locked to
// it. It never blocks: it hands the event to a buffered Go channel and, for
// kIOMessageSystemWillSleep, answers IOKit immediately.

/*
#cgo LDFLAGS: -framework IOKit -framework CoreFoundation

#include <IOKit/pwr_mgt/IOPMLib.h>
#include <IOKit/IOMessage.h>
#include <CoreFoundation/CoreFoundation.h>

// The Go side, defined in this file's //export below.
extern void nocxWakeEvent(int kind);

static io_connect_t nocxPowerRootPort;
static IONotificationPortRef nocxPowerNotifyPort;
static io_object_t nocxPowerNotifier;

// nocxIsFullWake answers whether the system is fully up. IOKit clears the
// Graphics capability for a dark wake and sets it on a full one.
static int nocxIsFullWake(void) {
	IOPMCapabilityBits caps = IOPMCopyCurrentCapabilities();
	return (caps & kIOPMSystemCapabilityGraphics) != 0;
}

// nocxPowerCallback runs on the run loop this file starts. It must never
// block: the sleep edge is answered at once, and the Go side only queues.
static void nocxPowerCallback(void *refcon, io_service_t service, natural_t messageType, void *messageArgument) {
	switch (messageType) {
	case kIOMessageSystemWillSleep:
		nocxWakeEvent(0); // EventWillSleep
		// The system waits for every client to allow the change; nothing here
		// has anything left to do, and telling the supervisors costs no time.
		IOAllowPowerChange(nocxPowerRootPort, (long)messageArgument);
		break;
	case kIOMessageSystemHasPoweredOn:
		if (nocxIsFullWake()) {
			nocxWakeEvent(1); // EventWoke
		} else {
			nocxWakeEvent(2); // EventDarkWoke
		}
		break;
	case kIOMessageSystemWillNotSleep:
		// Cancelled: the machine never left. A wake for every connection.
		nocxWakeEvent(1);
		break;
	default:
		// kIOMessageSystemWillPowerOn and anything this file does not act on.
		break;
	}
}

// nocxRegisterPower registers for power notifications and adds their source to
// the CALLING thread's run loop; nocxRunPowerLoop then drives it. Split in two
// so the Go caller learns whether registration worked before the thread
// disappears into the loop.
static int nocxRegisterPower(void) {
	nocxPowerRootPort = IORegisterForSystemPower(NULL, &nocxPowerNotifyPort, nocxPowerCallback, &nocxPowerNotifier);
	if (nocxPowerRootPort == MACH_PORT_NULL) {
		return 1;
	}
	CFRunLoopAddSource(
		CFRunLoopGetCurrent(),
		IONotificationPortGetRunLoopSource(nocxPowerNotifyPort),
		kCFRunLoopDefaultMode);
	return 0;
}

static void nocxRunPowerLoop(void) {
	CFRunLoopRun();
}
*/
import "C"

import (
	"fmt"
	"runtime"

	"github.com/shady2k/nocx/internal/monoclock"
)

// powerEvents is the one channel the IOKit callback feeds. There is one wake
// source per process (the helper constructs it once), and the C callback has no
// Go context to carry one, so the destination is package state.
var powerEvents chan Event

// nocxWakeEvent is called from IOKit's run loop thread. It queues and returns;
// nothing here may block, and a full queue drops the edge rather than stalling
// the system's power machine.
//
//export nocxWakeEvent
func nocxWakeEvent(kind C.int) {
	events := powerEvents
	if events == nil {
		return
	}
	ev := Event{Kind: EventKind(kind), At: monoclock.Now()}
	select {
	case events <- ev:
	default:
	}
}

// osEvents registers for system power notifications and answers the channel
// they arrive on. Registration happens on a thread locked to a run loop that
// then blocks for the life of the process.
func osEvents() (<-chan Event, error) {
	events := make(chan Event, 8)
	powerEvents = events

	started := make(chan error, 1)
	go func() {
		// The run loop belongs to this OS thread; without the lock Go may move
		// the goroutine and the callback would fire on a loop nobody runs.
		runtime.LockOSThread()
		if code := int(C.nocxRegisterPower()); code != 0 {
			started <- fmt.Errorf("IORegisterForSystemPower failed (code %d)", code)
			return
		}
		started <- nil
		C.nocxRunPowerLoop() // never returns
	}()

	if err := <-started; err != nil {
		powerEvents = nil
		return nil, err
	}
	return events, nil
}
