//go:build darwin

package sandbox

import (
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

func TestDarwinDevicePinRequiresItsActualNamespaceWithoutOpeningTTY(t *testing.T) {
	const device = "/dev/tty"
	want, err := identity(device)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := openPinned(device, DeviceRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pin.Close() }()
	if verifyErr := verifyPinned(pin, want, DeviceRoot); verifyErr != nil {
		t.Fatalf("valid device namespace refused: %v", verifyErr)
	}
	wrong, err := unix.Open(t.TempDir(), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	forged := os.NewFile(uintptr(wrong), device)
	defer func() { _ = forged.Close() }()
	if verifyPinned(forged, want, DeviceRoot) == nil {
		t.Fatal("unrelated directory descriptor authorized a device root")
	}
}
