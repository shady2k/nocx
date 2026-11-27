package main

// The shared half of the two build-variant assertions: one real helper
// connection — the same host, the same handshake, the same client a coordinator
// uses — with THIS build's seam registered on it.
//
// It is untagged so both builds run the same stand; only the assertion differs,
// because the difference between the two builds is exactly one thing: whether
// an ssh service is on the host at all.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/portsprobe"
	portshostsvc "github.com/shady2k/nocx/internal/portsprobe/hostsvc"
)

const helperHash = "testhash"

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// stubService is a service the build-composition tests register beside the real
// ones: it answers nothing, and it exists so those tests can call the REAL
// registerHelperServices without standing up a git factory or a PTY. It lives
// in the test tree rather than beside that function, because code only a test
// reaches is exactly what the deadcode ratchet is for.
type stubService struct{ name string }

func (s stubService) Name() string                     { return s.name }
func (s stubService) Ops() []string                    { return []string{"noop"} }
func (s stubService) ParamsSchema(string) *host.Schema { return host.SchemaFor(struct{}{}) }

func (s stubService) Call(context.Context, string, json.RawMessage) (any, error) {
	return struct{}{}, nil
}

// quietPortTools is a host whose own ss answers an empty table: it is the ports
// service's runner in this package's stands, so a test that asks for a sample
// never executes a real probe on the machine running the tests.
type quietPortTools struct{}

func (quietPortTools) Run(_ context.Context, c portsprobe.Command) (*portsprobe.Result, error) {
	if c.Argv[0] == "ss" {
		return &portsprobe.Result{}, nil
	}
	return nil, fmt.Errorf("%w: %s", portsprobe.ErrCommandAbsent, c.Argv[0])
}

// standUpHelper runs one real helper connection — the same host, the same
// handshake, the same client a coordinator uses — with the services THIS BUILD
// registers on it.
//
// The registration goes through registerHelperServices, which is the function
// the daemon's accept loop calls: a stand that registered the seam itself would
// keep passing on a build that had stopped registering anything, which is the
// one defect these two tests exist to catch.
func standUpHelper(t *testing.T, seam sshSeam) *client.Client {
	t.Helper()
	helperEnd, coordEnd := net.Pipe()
	h := host.New(helperEnd, helperEnd, helperHash, "instance-1", discardLog())
	registerHelperServices(h, stubService{"git-stub"}, stubService{"session-stub"}, stubService{"fs-stub"}, portshostsvc.New(quietPortTools{}), seam)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = h.Serve(ctx) }()

	c, err := client.Dial(ctx, client.Config{
		Exec:        client.NewSocketConn(coordEnd),
		ExpectHash:  helperHash,
		SentinelTTL: 5 * time.Second,
		Log:         discardLog(),
	})
	if err != nil {
		t.Fatalf("client.Dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}
