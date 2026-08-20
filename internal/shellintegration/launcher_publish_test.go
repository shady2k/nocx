package shellintegration

// The bundle both carriers publish, and the contract it has to satisfy.
//
// This file used to also hold the sh publish writer's tests: the prelude that
// travelled inside the ~90 KiB remote command published the bundle from the
// far side, so there were TWO writers of one contract and a bidirectional
// conformance criterion between them. ADR-0035 removed the command, and the
// prelude with it; there is one writer now — the Go publisher, over SFTP or
// over an auxiliary channel of a multiplex master — so the conformance the
// deleted tests proved is no longer a property this repository has to hold.
// What survives is the bundle descriptor itself, which both transports still
// publish and which still has to satisfy validateBundle.

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestLaunchBundle_ConformsToPublisherContract: the bundle descriptor both
// carriers publish satisfies validateBundle and publishes through the Go
// publisher, and the committed manifest verifies — the contract the sh
// writer must mirror.
func TestLaunchBundle_ConformsToPublisherContract(t *testing.T) {
	b := launchBundle()
	if err := validateBundle(b); err != nil {
		t.Fatalf("launchBundle fails validateBundle: %v", err)
	}
	root := filepath.Join(t.TempDir(), dirName)
	if _, err := NewPublisher(testLogger(), NewOSFS(), root).Publish(b); err != nil {
		t.Fatalf("Go publish of the shared bundle: %v", err)
	}
	vr, err := NewPublisher(testLogger(), NewOSFS(), root).Verify()
	if err != nil || !vr.Installed {
		t.Fatalf("Verify after Go publish: %+v err=%v", vr, err)
	}
	// The carrier file itself is what the launcher installs; it must be a
	// parseable POSIX script with a /bin/sh shebang.
	carrier, ok := b.file(launchName)
	if !ok {
		t.Fatal("bundle has no launch carrier")
	}
	if !strings.HasPrefix(string(carrier.Data), "#!/bin/sh\n") {
		t.Errorf("carrier shebang: %q", string(carrier.Data[:min(10, len(carrier.Data))]))
	}
}
