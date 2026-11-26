package helperfs_test

// A helper generation that is draining has stopped admitting new work: the last
// hosted session ended, and the daemon is on its way out (review m1). It is not
// a broken host, and the panel must not be told that it is — a `draining`
// refusal answered as `internal` sends the Files pane into a replay against a
// daemon that is leaving, and the person watches an upload fail for no stated
// reason instead of a panel pausing with a reason of its own.

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/filesystem/helperfs"
	"github.com/shady2k/nocx/internal/filesystem/hostsvc"
	"github.com/shady2k/nocx/internal/filesystem/local"
	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/transfer"
)

// drainingAdmission is the daemon telling the fs service that it has retired.
// The wrapped error is the session service's own sentinel, spelled the way the
// composition root spells it (cmd/nocx-helper/admission.go).
type drainingAdmission struct{}

func (drainingAdmission) Admit() (func(), error) {
	return nil, &hostsvc.DrainingRefusal{Err: errors.New("session: helper generation is draining")}
}

func TestAnUploadAgainstADrainingHelperPausesThePanel(t *testing.T) {
	root := tempTree(t)
	svc, err := hostsvc.New(local.New(local.WithRoot(root)), hostsvc.JournalConfig{
		Root:       t.TempDir(),
		Hostname:   "fixture",
		InstanceID: strings.Repeat("b", 32),
	}, hostsvc.WithAdmission(drainingAdmission{}))
	if err != nil {
		t.Fatalf("host fs service: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := svc.Close(); closeErr != nil {
			t.Errorf("closing host fs service: %v", closeErr)
		}
	})
	reg, _ := singlePeer(t, &fsObserver{Service: svc})
	p := helperfs.New(reg, panelID, root)
	ctx := testContext(t)

	upload := transfer.Upload{
		DestDir:  root,
		Name:     "note.txt",
		Size:     int64(len("data")),
		OnExists: transfer.Overwrite,
	}
	_, err = p.Sink().Put(ctx, upload, bytes.NewReader([]byte("data")), nil)
	var unavailable *filesystem.ErrUnavailable
	if !errors.As(err, &unavailable) {
		t.Fatalf("an upload against a draining helper = %v, want the axis' typed unavailability", err)
	}
	// The refusal is the daemon's retirement, so the pane pauses with the axis
	// reason rather than surfacing the raw wire code as a failure of the host.
	if errors.As(err, new(*client.RefusalError)) {
		t.Fatalf("a draining helper's refusal reached the panel as a raw wire error: %v", err)
	}
}
