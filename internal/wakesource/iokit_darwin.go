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
// # Every symbol below is declared by the public SDK
//
// The header and the declaration are named beside each use, because this file
// is compiled only on macOS and a symbol the public headers do not declare
// cannot be caught by a Linux build:
//
//	IOKit/pwr_mgt/IOPMLib.h
//	  io_connect_t IORegisterForSystemPower(void *refcon, IONotificationPortRef *thePortRef,
//	                                        IOServiceInterestCallback callback, io_object_t *notifier)
//	                              AVAILABLE_MAC_OS_X_VERSION_10_0_AND_LATER;
//	  IOReturn IOAllowPowerChange(io_connect_t kernelPort, intptr_t notificationID)
//	                              AVAILABLE_MAC_OS_X_VERSION_10_0_AND_LATER;
//
//	IOKit/IOMessage.h
//	  #define kIOMessageCanSystemSleep     iokit_common_msg(0x270)
//	  #define kIOMessageSystemWillNotSleep iokit_common_msg(0x290)
//	  #define kIOMessageSystemWillSleep    iokit_common_msg(0x280)
//	  #define kIOMessageSystemWillPowerOn  iokit_common_msg(0x320)
//	  #define kIOMessageSystemHasPoweredOn iokit_common_msg(0x300)
//
//	IOKit/IOKitLib.h
//	  typedef void (*IOServiceInterestCallback)(void *refcon, io_service_t service,
//	                                            uint32_t messageType, void *messageArgument);
//	  CFRunLoopSourceRef IONotificationPortGetRunLoopSource(IONotificationPortRef notify);
//
// # What the messages mean, in the public header's own words
//
// IOPMLib.h on IORegisterForSystemPower: "kIOMessageSystemWillSleep is delivered
// at the point the system is initiating a non-abortable sleep. Callers MUST
// acknowledge this event by calling IOAllowPowerChange"; and "Caller must
// acknowledge kIOMessageCanSystemSleep by calling IOAllowPowerChange or
// IOCancelPowerChange". Both are answered here, because a client that answers
// only one leaves the system waiting on it: a missing acknowledgement to
// kIOMessageSystemWillSleep costs a 30-second timeout per sleep, and a missing
// one to kIOMessageCanSystemSleep the same on every idle-sleep proposal.
//
//   - kIOMessageCanSystemSleep (0x270) — an idle sleep is being pondered.
//     Allowed: nocx never vetoes a system sleep, so the answer is
//     IOAllowPowerChange (IOCancelPowerChange is the veto, "the app vetoes the
//     idle sleep request"). It is NOT reported as a sleep edge: the sleep may
//     still be cancelled, and kIOMessageSystemWillSleep is the edge supervisors
//     act on.
//   - kIOMessageSystemWillSleep (0x280) — the machine sleeps now. Reported as
//     EventWillSleep, then allowed at once: nothing here has anything left to
//     do, and telling the supervisors costs no time.
//   - kIOMessageSystemHasPoweredOn (0x300) — the machine is awake. Reported as
//     a full wake; see the limit below.
//   - kIOMessageSystemWillNotSleep (0x290) — a sleep was cancelled. For a
//     connection that is the same fact as a wake: the machine never left, so
//     one proof of life and the probe cadence resumes.
//   - kIOMessageSystemWillPowerOn (0x320) — powered but not usable yet; the
//     powered-on edge above is the wake. Nothing to report.
//
// # The limit, stated: this bridge cannot tell a full wake from a DARK wake
//
// A dark wake (Power Nap, maintenance, a network daemon) powers the CPU and the
// network without the display or the user's session, and the design says it must
// not make every connection probe. The API that tells them apart is a
// CAPABILITY set, and it is NOT in the public SDK: IOPMCapabilityBits,
// IOPMIsADarkWake, IOPMIsASilentWake and IOPMConnectionCreate are declared in
// IOPMLibPrivate.h (Apple's SPI header, not shipped as API), and the function an
// earlier draft of this file called — IOPMCopyCurrentCapabilities — is not
// declared in the public IOPMLib.h at all, or anywhere in the current
// IOPMLibPrivate.h. Nothing else in IOPMLib.h reports the current capability
// set; the header mentions dark wake only in the discussion of two assertion
// types ("This assertion has no effect if the system is in Dark Wake").
//
// So every kIOMessageSystemHasPoweredOn is reported as a wake, a dark wake reads
// as a wake, and this package has no second state for it because there is
// nothing that could feed one. What that costs: one small probe per connection
// per dark wake, on a link that is usually healthy. It cannot close a shell — a
// probe is evidence and never a verdict, and silence closes only under the
// policy in design §5.3. Taking the distinction from IOPMLibPrivate.h is the
// alternative, and a private API is not something a shipped helper may link
// against.
//
// The callback runs on a CFRunLoop this file owns, on a thread locked to it. It
// never blocks: it hands the event to a buffered Go channel and answers IOKit
// immediately.

/*
#cgo LDFLAGS: -framework IOKit -framework CoreFoundation

#include <IOKit/IOKitLib.h>
#include <IOKit/IOMessage.h>
#include <IOKit/pwr_mgt/IOPMLib.h>
#include <CoreFoundation/CoreFoundation.h>

// The Go side, defined in this file's //export below.
extern void nocxWakeEvent(int kind);

static io_connect_t nocxPowerRootPort;
static IONotificationPortRef nocxPowerNotifyPort;
static io_object_t nocxPowerNotifier;

// nocxPowerCallback runs on the run loop this file starts. It must never
// block, and it answers every message the public header says needs an answer.
static void nocxPowerCallback(void *refcon, io_service_t service, uint32_t messageType, void *messageArgument) {
	(void)refcon;
	(void)service;
	switch (messageType) {
	case kIOMessageCanSystemSleep:
		// Pondering an idle sleep. Allow it: nocx never vetoes a system sleep.
		IOAllowPowerChange(nocxPowerRootPort, (intptr_t)messageArgument);
		break;
	case kIOMessageSystemWillSleep:
		nocxWakeEvent(0); // EventWillSleep
		IOAllowPowerChange(nocxPowerRootPort, (intptr_t)messageArgument);
		break;
	case kIOMessageSystemHasPoweredOn:
		nocxWakeEvent(1); // EventWoke
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
		// IOPMLib.h: IORegisterForSystemPower "Returns a io_connect_t session
		// for the IOPMrootDomain or IO_OBJECT_NULL if request failed."
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
// The kinds are the EventKind values this package's callback contract fixes:
// 0 EventWillSleep, 1 EventWoke. There is no third one to send: the public API
// has no dark-wake edge, as the file's comment above sets out.
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
// then blocks for the life of the process; that is the source's stated
// lifetime, so nothing deregisters (IODeregisterForSystemPower plus
// IONotificationPortDestroy would be the pair, in that order).
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
