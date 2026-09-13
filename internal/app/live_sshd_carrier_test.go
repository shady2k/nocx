//go:build !nocx_local_ssh

package app

// The live-sshd fixture's bundle carrier, in a build WITHOUT the helper's ssh
// service.
//
// # Why this file exists at all
//
// The publish is this machine's helper's (nocx-50w7p.15), and the helper's ssh
// SERVICE is a build-tagged package (`internal/helper/sshsvc`, tag
// `nocx_local_ssh`) because it links the ssh client that the artifact deployed
// to somebody else's host must not carry. So the default build cannot serve a
// helper in-process, and a live-sshd journey — a real OpenSSH, a real bash, a
// real lifecycle domain — needs a carrier to get a bundle onto the far host.
//
// This is that carrier, and it is DELIBERATELY a test double: it publishes over
// a connection the TEST owns, which is the shape the product retired. It says
// so here rather than hiding it, and the tagged build replaces it with the real
// route — same function, other file (live_sshd_helper_carrier_test.go) — so
// every journey in these files runs over this machine's helper wherever one can
// be served. What this file buys is that the session, lifecycle and canary
// proofs in the live-sshd suite keep measuring their subjects in the default
// build; what it cannot buy is the routing claim, and that claim is asserted
// where the helper exists (helper_bundle_acceptance_test.go).

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	pkgsftp "github.com/pkg/sftp"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/remoteprobe"
	"github.com/shady2k/nocx/internal/shellintegration"
	"github.com/shady2k/nocx/internal/ssh"
	gossh "golang.org/x/crypto/ssh"
)

// liveBundleCarrier is the publisher a live-sshd journey wires. In this build
// it is the fixture's own connection.
func liveBundleCarrier(t *testing.T, fx *liveSshd) ssh.RemoteInstaller {
	t.Helper()
	return &fixtureBundleCarrier{t: t, fx: fx}
}

// rawClient opens a production-compatible SSH client to the fixture. Tests
// that exercise SFTP publication use this instead of reaching through
// ssh.RealClient's connection pool.
func (fx *liveSshd) rawClient(t *testing.T) *gossh.Client {
	t.Helper()
	client, err := gossh.Dial("tcp", fx.addr, &gossh.ClientConfig{
		User: fx.user,
		Auth: []gossh.AuthMethod{gossh.PublicKeys(fx.signer)},
		HostKeyCallback: func(_ string, _ net.Addr, key gossh.PublicKey) error {
			if !bytes.Equal(key.Marshal(), fx.hostKey.Marshal()) {
				return fmt.Errorf("host key mismatch")
			}
			return nil
		},
		Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("dial live sshd: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// fixtureBundleCarrier publishes over the fixture's own dial: the account's
// home over an exec channel, the bundle over an sftp client.
//
// It is a TEST-OWNED transport and deliberately not the product's: it predates
// the helper-backed route and exists to drive the publish protocol against a
// real sshd without a helper daemon. It borrows nothing from the production
// carrier, which is why the home probe below is its own copy rather than the
// production one that used to be exported to it — that helper was deleted with
// the coordinator's dial (nocx-50w7p.5), and a test that needed it back would
// be a test keeping the code the epic removed.
type fixtureBundleCarrier struct {
	t  *testing.T
	fx *liveSshd
}

// fixtureHomeOver asks the fixture's host where its home is, over the client
// this fixture dialed. The commands are internal/remoteprobe's list and in its
// order — the same rule the production paths follow: nothing here composes
// shell text.
type fixtureHomeOver struct {
	client *gossh.Client
}

func (f fixtureHomeOver) Home() (string, error) {
	for _, command := range remoteprobe.HomeCommands {
		sess, err := f.client.NewSession()
		if err != nil {
			return "", err
		}
		out, err := sess.Output(command)
		_ = sess.Close()
		if err != nil {
			return "", err
		}
		if home := strings.TrimSpace(string(out)); home != "" {
			return home, nil
		}
	}
	return "", nil
}

func (c *fixtureBundleCarrier) EnsureInstalledRemote(ctx context.Context, _ string, _ ...ssh.ConnectOption) error {
	impl := shellintegration.New(log.NewSlogAdapter(nil))
	client := c.fx.rawClient(c.t)
	home, err := impl.GetRemoteHome(fixtureHomeOver{client: client})
	if err != nil {
		return err
	}
	clientSFTP, err := pkgsftp.NewClient(client)
	if err != nil {
		return err
	}
	defer func() { _ = clientSFTP.Close() }()
	return impl.EnsureInstalledRemote(ctx, shellIntegrationSFTPFS{SFTPFS: ssh.NewSFTPFS(clientSFTP)}, home)
}
