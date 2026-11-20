package app

import (
	"context"
	"errors"
	"os"
	"path"
	"reflect"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/farhelper"
	"github.com/shady2k/nocx/internal/helper/consent"
	"github.com/shady2k/nocx/internal/helper/deploy"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/storage"
)

type footprintInstallChannel struct {
	conn   ssh.HelperInstallConn
	err    error
	params proto.OpenChannelParams
}

func (c *footprintInstallChannel) openInstallFor(_ context.Context, params proto.OpenChannelParams) (ssh.HelperInstallConn, error) {
	c.params = params
	return c.conn, c.err
}

type footprintFailHome struct{ ssh.HelperInstallConn }

func (footprintFailHome) Home() (string, error) { return "", os.ErrPermission }

type footprintFailPrune struct{ ssh.HelperInstallConn }

func (footprintFailPrune) ReadDir(string) ([]os.FileInfo, error) { return nil, os.ErrPermission }

type footprintFailWrite struct{ storage.DocumentStore }

func (footprintFailWrite) Write(string, any) error { return os.ErrPermission }

func TestFarHelperFootprint(t *testing.T) {
	for _, operation := range []string{"Install", "Find"} {
		for _, outcome := range []string{"success", "missing", "channel failure", "home failure", "deploy failure", "prune failure", "record failure"} {
			if operation == "Install" && outcome == "missing" || operation == "Find" && outcome == "prune failure" {
				continue
			}
			t.Run(operation+"/"+outcome, func(t *testing.T) {
				const home = "/home/remote"
				platform := farhelper.Platform{GOOS: "linux", GOARCH: "amd64"}
				target := farhelper.Target{HostKeyFingerprint: "SHA256:footprint", Destination: proto.SSHDestination{User: "remote", Host: "db.example", Port: 2222}}
				fs := newFakeInstallConn(home)
				source := stubArtifacts(t)
				if operation == "Find" && outcome != "missing" {
					if _, _, err := deploy.Ensure(t.Context(), installFS{fs}, source, home, deploy.Platform{GOOS: platform.GOOS, GOARCH: platform.GOARCH}, nil); err != nil {
						t.Fatal(err)
					}
				}
				channel := &footprintInstallChannel{conn: fs}
				switch outcome {
				case "channel failure":
					channel.err = os.ErrPermission
				case "home failure":
					channel.conn = footprintFailHome{fs}
				case "deploy failure":
					source = refusingSource{}
				case "prune failure":
					channel.conn = footprintFailPrune{fs}
				}
				dir := t.TempDir()
				var docs storage.DocumentStore = storage.NewDocumentStore(dir)
				if outcome == "record failure" {
					docs = footprintFailWrite{docs}
				}
				logger := log.NewSlogAdapter(discardLogger(t))
				installs := consent.NewInstallStore(logger, docs, "installs.json")
				installer := &farHelperInstall{local: &localHelperOpener{}, over: channel, source: source, installs: installs}
				before := time.Now()
				var installed farhelper.Installed
				var found bool
				var err error
				if operation == "Install" {
					installed, err = installer.Install(t.Context(), target, platform)
				} else {
					installed, found, err = installer.Find(t.Context(), target, platform)
				}
				after := time.Now()
				wantErr := outcome != "success" && outcome != "missing"
				if (err != nil) != wantErr {
					t.Fatalf("%s error = %v, want error %v", operation, err, wantErr)
				}
				if wantErr && outcome != "deploy failure" && !errors.Is(err, os.ErrPermission) {
					t.Fatalf("error cause lost: %v", err)
				}
				if operation == "Find" && found != (outcome == "success") {
					t.Fatalf("found = %v for %s", found, outcome)
				}
				// Read a fresh store to prove the footprint survives the coordinator.
				rows := consent.NewInstallStore(logger, storage.NewDocumentStore(dir), "installs.json").All()
				if outcome != "success" {
					if len(rows) != 0 {
						t.Fatalf("%s recorded an unconfirmed footprint: %+v", outcome, rows)
					}
					return
				}
				wantPath := path.Dir(deploy.InstalledPath(home, deploy.Platform{GOOS: "linux", GOARCH: "amd64"}, syntheticArtifactHash))
				if len(rows) != 1 {
					t.Fatalf("successful %s recorded %d footprints, want 1", operation, len(rows))
				}
				row := rows[0]
				if row.Fingerprint != target.HostKeyFingerprint || row.Identity != "remote@db.example:2222" || row.Path != wantPath || row.Hash != syntheticArtifactHash {
					t.Fatalf("footprint = %+v", row)
				}
				if row.InstalledAt.Before(before) || row.InstalledAt.After(after) {
					t.Fatalf("observation timestamp = %s, outside call interval", row.InstalledAt)
				}
				if installed.Dir != row.Path || installed.Generation != row.Hash {
					t.Fatalf("result %+v disagrees with footprint %+v", installed, row)
				}
				if !reflect.DeepEqual(channel.params.Destination, target.Destination) {
					t.Fatal("install destination changed")
				}
			})
		}
	}
}
