//go:build darwin && nocx_local_ssh

package wakesource

// IOKit's system power notifications, for the LOCAL helper's Darwin build.
//
// The build tag is the boundary: this file loads IOKit at runtime without SDK headers, and the
// helper that is installed on somebody else's machine is built WITHOUT
// nocx_local_ssh (the two are different bytes of one repository, D11). The far
// helper therefore never links this, which TestDeployedHelperDependencyGraphHas
// NoSSHClient and its siblings enforce.
//
// # Every symbol below is declared by the public SDK
//
// The header and the declaration are named beside each use, because the foreign-call ABI must match them even when cross-compiled
// on Linux without a macOS SDK:
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

import (
	"fmt"
	"runtime"

	"github.com/ebitengine/purego"
	"github.com/shady2k/nocx/internal/monoclock"
)

// IOKit/IOReturn.h: iokit_common_msg(x) is sys_iokit | sub_iokit_common | x.
const (
	canSystemSleep     uint32 = 0xe0000270
	systemWillSleep    uint32 = 0xe0000280
	systemWillNotSleep uint32 = 0xe0000290
	systemHasPoweredOn uint32 = 0xe0000300
)

// powerAPI is the public SDK ABI. Mach ports (io_connect_t, io_object_t) and
// IOReturn are 32-bit, while pointers and intptr_t are pointer-sized on both
// supported Darwin architectures. The frameworks stay loaded for process life.
type powerAPI struct {
	// IOPMLib.h: io_connect_t IORegisterForSystemPower(void *,
	// IONotificationPortRef *, IOServiceInterestCallback, io_object_t *).
	register func(uintptr, *uintptr, uintptr, *uint32) uint32
	// IOPMLib.h: IOReturn IOAllowPowerChange(io_connect_t, intptr_t).
	allow func(uint32, uintptr) int32
	// IOKitLib.h: CFRunLoopSourceRef IONotificationPortGetRunLoopSource(IONotificationPortRef).
	source func(uintptr) uintptr
	// CFRunLoop.h: CFRunLoopRef CFRunLoopGetCurrent(void);
	// void CFRunLoopAddSource(CFRunLoopRef, CFRunLoopSourceRef, CFRunLoopMode);
	// void CFRunLoopRun(void).
	current func() uintptr
	add     func(uintptr, uintptr, uintptr)
	run     func()
	// CFString.h: CFStringRef CFStringCreateWithCString(CFAllocatorRef,
	// const char *, CFStringEncoding); CFBase.h: void CFRelease(CFTypeRef).
	stringCreate func(uintptr, string, uint32) uintptr
	release      func(uintptr)
}

func loadPowerAPI() (*powerAPI, error) {
	io, err := purego.Dlopen("/System/Library/Frameworks/IOKit.framework/IOKit", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return nil, fmt.Errorf("load IOKit: %w", err)
	}
	cf, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return nil, fmt.Errorf("load CoreFoundation: %w", err)
	}
	api := &powerAPI{}
	for _, symbol := range []struct {
		lib  uintptr
		name string
		fn   any
	}{
		{io, "IORegisterForSystemPower", &api.register},
		{io, "IOAllowPowerChange", &api.allow},
		{io, "IONotificationPortGetRunLoopSource", &api.source},
		{cf, "CFRunLoopGetCurrent", &api.current},
		{cf, "CFRunLoopAddSource", &api.add},
		{cf, "CFRunLoopRun", &api.run},
		{cf, "CFStringCreateWithCString", &api.stringCreate},
		{cf, "CFRelease", &api.release},
	} {
		addr, err := purego.Dlsym(symbol.lib, symbol.name)
		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w", symbol.name, err)
		}
		purego.RegisterFunc(symbol.fn, addr)
	}
	return api, nil
}

// powerCallback implements IOServiceInterestCallback (IOKitLib.h): void
// (*)(void *refcon, io_service_t service, uint32_t messageType, void *argument).
// Acknowledgement is synchronous and never waits for a channel reader.
func powerCallback(events chan<- Event, root *uint32, allow func(uint32, uintptr) int32) func(uintptr, uint32, uint32, uintptr) {
	return func(_ uintptr, _ uint32, message uint32, argument uintptr) {
		var kind EventKind
		switch message {
		case canSystemSleep:
			allow(*root, argument)
			return
		case systemWillSleep:
			allow(*root, argument)
			kind = EventWillSleep
		case systemWillNotSleep, systemHasPoweredOn:
			kind = EventWoke
		default:
			return
		}
		select {
		case events <- Event{Kind: kind, At: monoclock.Now()}:
		default:
		}
	}
}

// osEvents registers on a locked thread and drives that thread's CFRunLoop for
// process life. The callback retains its channel and root port; no package-wide
// callback destination is needed. The deployed helper never imports purego.
func osEvents() (<-chan Event, error) {
	api, err := loadPowerAPI()
	if err != nil {
		return nil, err
	}
	events := make(chan Event, 8)
	started := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		var root, notifier uint32
		var port uintptr
		callback := purego.NewCallback(powerCallback(events, &root, api.allow))
		root = api.register(0, &port, callback, &notifier)
		if root == 0 {
			started <- fmt.Errorf("IORegisterForSystemPower failed (IO_OBJECT_NULL)")
			return
		}
		// CFRunLoop.h: kCFRunLoopDefaultMode is the CFString
		// "kCFRunLoopDefaultMode". Create that value rather than dereferencing
		// a foreign data pointer. CFString.h: kCFStringEncodingUTF8=0x08000100.
		mode := api.stringCreate(0, "kCFRunLoopDefaultMode", 0x08000100)
		api.add(api.current(), api.source(port), mode)
		api.release(mode)
		started <- nil
		api.run()
		close(events)
	}()
	if err := <-started; err != nil {
		return nil, err
	}
	return events, nil
}
