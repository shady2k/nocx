//go:build nocx_local_ssh

package app

// TRUSTING A HOST KEY AND PRESSING RECONNECT ASKS FOR THE METHOD
// (nocx-oalo1.25).
//
// What a person can do that they could not before: make the FIRST connection to
// a saved SSH host, answer the host-key question, and finish from the tab that
// connection made — instead of a tab that offers Reconnect and answers
// `hostconn: reconnect is no longer offered` forever.
//
// # The state the defect left a person in
//
// A first open on a host with no host key and no method answer refuses to
// decide the helper question itself: it dials (the platform probe), learns the
// key, releases its probe-only connection and returns the connect-time ask
// (transport.ErrHelperConsentNeeded, ADR-0068). While the person answers, that
// connection has NO holder — the pane's logical hold is released on purpose, so
// a probe nobody asked for does not keep a login on somebody else's machine
// (hostedOpeners.OpenHosted). The supervisor's last holder letting go is
// `ended(released)`, the driver offers Reconnect on it (design §5.3 — the way
// out), and the pane stays.
//
// The press then used to CLAIM that connection. Nothing holds it, so
// RequestReconnect refuses every claim with hostconn.ErrNoReconnect — measured
// on ALX-2046 in 3/3 runs, and the reason a person saw a button that could
// never work. The offer was right; what it named was not. The pane is the only
// interest left, so the press runs the pane's own open again — the same open an
// answered question takes — and the ask it produces is the dialog the person
// then answers.
//
// # What is real here
//
// The real helper (the host protocol over a socketpair, the ssh service, the
// session service), the real carrier and its supervisor, the real open seam
// (hostedOpeners), the real panedriver, the real transport over its real
// socket, and the fixture's own sshd. Scripted, and only: the far host.

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/hostmode"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/panedriver"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/transport"
	"github.com/shady2k/nocx/internal/waittest"
	gossh "golang.org/x/crypto/ssh"
)

func TestTrustingAHostKeyThenReconnectAsksForTheMethod(t *testing.T) {
	const pane = "0190d3a0-1c2b-7def-8abc-012345678901"
	srv := startPasswordSFTPSSHServer(t, t.TempDir())
	secrets := &askRecorder{value: openPasswordFixturePassword}
	driver := panedriver.New("reconnect-trust", log.NewSlogAdapter(discardLogger(t)))
	var published []panedriver.Snapshot
	var publishedMu sync.Mutex
	stand := startFilesStandWith(t, srv, secrets, func(source string, ev proto.ConnectionStateEvent) {
		driver.ApplyFrom(source, ev)
	})
	// A FRESH PROFILE: the fixture's key is offered, nothing has trusted it,
	// and the host has no method answer — which is what makes the connect-time
	// ask (ADR-0033/ADR-0068) the refusal a person meets.
	if err := os.Remove(stand.khPath); err != nil {
		t.Fatalf("clear known_hosts: %v", err)
	}
	recordFixtureMode(t, stand, srv, hostmode.Auto)
	stand.opener.paneConns = driver
	stand.opener.reconnectPolicy = func() string { return "ask" }

	// The machine is unanswered: the store knows another fingerprint, never
	// this host's. That is what makes the open refuse to decide for itself.
	consentStore, _ := testConsentStoresFor(t, "SHA256:some-other-machine")
	remote := newHostedShellRegistry(stand.routes, stubArtifacts(t), consentStore, discardLogger(t))
	remote.registry = stand.registry
	remote.modes = stand.modes
	hosted := &hostedOpeners{local: stand.opener, remote: remote}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	ws := transport.NewWSServer(log.NewSlogAdapter(discardLogger(t)), stand.registry,
		transport.WithHelperSessionOpener(hosted),
		transport.WithProfileResolver(liveHostedCwdResolver{host: srv.addr, config: &ssh.ConnectConfig{
			User: filesFixtureUser, AuthMode: "password", Secrets: secrets, SecretID: filesFixtureSecret,
			AuthorizedEndpoint: srv.addr, IntegrationMode: string(hostmode.Auto), Shell: "bash",
		}}),
		transport.WithPaneConnectionPolicy(paneConnectionOwner{Driver: driver, opener: stand.opener}))
	driver.SetPublisher(func(s panedriver.Snapshot) {
		publishedMu.Lock()
		published = append(published, s)
		publishedMu.Unlock()
		ws.PublishPaneConnection(paneConnectionWire(s))
	})
	driver.SetAction(func(ctx context.Context, paneID string, binding uint64) error {
		return ws.ReconnectPane(ctx, paneID, binding)
	})
	if err := ws.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ws.Stop(context.Background()) }()
	conn := dialTraceWSServer(t, ws)

	// 1. THE FIRST OPEN ASKS, and it does not leave a connection behind.
	open := callAppWS(t, conn, "open", map[string]any{
		"kind": "ssh", "profileId": "fresh-host", "paneId": pane, "cols": 80, "rows": 24,
	}, 1)
	if open.Error == nil {
		t.Fatal("a first open with an unknown host key opened without asking")
	}
	var firstAsk askWireData
	if open.Error.Data != nil {
		_ = json.Unmarshal(open.Error.Data, &firstAsk)
	}
	if !firstAsk.HelperAsk {
		t.Fatalf("the first open's refusal is not the connect-time ask: %+v", open.Error)
	}

	// 2. THE PANE IS OFFERED RECONNECT ON THE RELEASED CONNECTION — the state
	// the defect stuck in. The press is what this test is about.
	epoch := waitForReleasedOffer(t, &publishedMu, &published)
	if epoch == 0 {
		t.Fatal("the pane was never offered Reconnect after its probe-only connection was released")
	}

	// 3. THE PERSON TRUSTS THE KEY. That is the only thing that changed.
	writeKnownHostsFor(t, stand.khPath, srv)

	// 4. THE PRESS. Nothing holds the connection, so it must reopen the pane
	// rather than claim a transport that is gone. Its answer and its
	// notification are read together: the release unblocks a reopen that
	// reports on the pane's channel, so which of the two frames comes first is
	// the scheduler's business, not this test's (ALX-2085 finding 4).
	press, failure := pressReconnectCollectingAsk(t, conn, pane, epoch, 2)
	if press.Error != nil {
		t.Fatalf("pressing Reconnect after trusting the key: %+v", press.Error)
	}
	var registered struct {
		Registered bool `json:"registered"`
	}
	if err := json.Unmarshal(press.Result, &registered); err != nil || !registered.Registered {
		t.Fatalf("press result = %s (%v), want registered", press.Result, err)
	}

	// 5. THE PANE IS TOLD WHAT TO DO NEXT, and it is THE method question about
	// THIS host under THIS key: the ask names the destination, the fingerprint
	// the answer is recorded under (ADR-0034), and asks for the method alone.
	// A refusal of some other shape, or one that carried the host-key evidence
	// again, would put a second trust decision in front of a person who has
	// just made one.
	if failure.Code == 0 || failure.Message == "" {
		t.Fatalf("the pane was told %+v, want the method ask", failure)
	}
	if failure.Sequence != 1 {
		t.Fatalf("the pane's first refusal is sequence %d, want 1: the identity is what tells a republication of this ask apart from a new one", failure.Sequence)
	}
	data, err := json.Marshal(failure.Data)
	if err != nil {
		t.Fatalf("marshal the ask's data %+v: %v", failure.Data, err)
	}
	var ask askWireData
	if err := json.Unmarshal(data, &ask); err != nil {
		t.Fatalf("decode the ask's data %s: %v", data, err)
	}
	if !ask.HelperAsk {
		t.Fatalf("the pane's refusal is not the connect-time ask: %s", failure.Data)
	}
	if want := srv.addr; ask.Host != want {
		t.Fatalf("the ask names host %q, want the destination that was opened, %q", ask.Host, want)
	}
	if want := gossh.FingerprintSHA256(srv.hostSigner.PublicKey()); ask.Fingerprint != want {
		t.Fatalf("the ask names fingerprint %q, want the key this host offered, %q: the answer is recorded under it", ask.Fingerprint, want)
	}
	if ask.ProfileID != "fresh-host" {
		t.Errorf("the ask names profile %q, want the saved connection it came from", ask.ProfileID)
	}
	if ask.HostKey != nil {
		t.Fatalf("the reopen asked about the host key again: the person trusted it in step 3, and the only open question is the method: %+v", ask.HostKey)
	}
}

// askWireData is the connect-time ask as it reaches the wire
// (contracts/open.helperConsent.schema.json). The host-key evidence is a
// pointer so "the key was not questioned again" is the absence of the object,
// not a zero value that could mean either.
type askWireData struct {
	Host        string          `json:"host"`
	Fingerprint string          `json:"fingerprint"`
	HelperAsk   bool            `json:"helperAsk"`
	ProfileID   string          `json:"profileId"`
	HostKey     json.RawMessage `json:"hostKey"`
}

// pressReconnectCollectingAsk presses Reconnect and reads until BOTH the
// response to that request and the pane's own refusal have arrived, in
// whichever order the server wrote them.
//
// WHY IT CANNOT BE callAppWS THEN A READ (ALX-2085 finding 4): the press
// registers, releases the block, and the reopen immediately reports the
// refusal on the pane's channel. The response is written after that, so the
// notification can be ahead of it — and a reader that stops at the response
// drops the frame the press was made to produce. This reads the two together,
// and every refusal it sees is checked, so a duplicate question about a key the
// person already trusted cannot slip past on the losing side of the race.
func pressReconnectCollectingAsk(
	t *testing.T, conn *websocket.Conn, pane string, epoch uint64, id int,
) (wsRPCResult, transport.PaneShellFailure) {
	t.Helper()
	req, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "pane.reconnect",
		"params": map[string]any{"paneId": pane, "connectionEpoch": epoch},
	})
	if err != nil {
		t.Fatalf("marshal press: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, req); err != nil {
		t.Fatalf("write press: %v", err)
	}
	var (
		response *wsRPCResult
		asked    *transport.PaneShellFailure
		seqs     []uint64
	)
	deadline := time.Now().Add(30 * time.Second)
	for response == nil || asked == nil {
		_ = conn.SetReadDeadline(deadline)
		messageType, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("reading the press's two outcomes (response=%v ask=%v): %v", response != nil, asked != nil, err)
		}
		if messageType != websocket.TextMessage {
			continue
		}
		var envelope struct {
			ID     int                      `json:"id"`
			Method string                   `json:"method"`
			Params transport.PaneConnection `json:"params"`
		}
		if json.Unmarshal(raw, &envelope) != nil {
			continue
		}
		switch {
		case envelope.ID == id && envelope.Method == "":
			var result wsRPCResult
			if err := json.Unmarshal(raw, &result); err != nil {
				t.Fatalf("decode the press's response: %v (raw %s)", err, raw)
			}
			response = &result
		case envelope.Method == "pane.connection" && envelope.Params.PaneID == pane && envelope.Params.ShellFailure != nil:
			failure := *envelope.Params.ShellFailure
			rawData, _ := json.Marshal(failure.Data)
			var data askWireData
			if json.Unmarshal(rawData, &data) == nil && data.HostKey != nil {
				t.Fatalf("a frame after the press asked about the host key again: %s", failure.Data)
			}
			seqs = append(seqs, failure.Sequence)
			asked = &failure
		}
	}
	for _, seq := range seqs {
		if seq != seqs[0] {
			t.Fatalf("one press was told about two different refusals: sequences %v", seqs)
		}
	}
	return *response, *asked
}

// waitForReleasedOffer answers the epoch of the pane's observation of a
// connection whose last holder let go, or zero if none arrives. It reads the
// driver's own stream, not the socket: the press needs the epoch the person was
// SHOWN, and the driver mints it before the transport orders it.
func waitForReleasedOffer(t *testing.T, mu *sync.Mutex, all *[]panedriver.Snapshot) uint64 {
	t.Helper()
	var epoch uint64
	waittest.WaitForTimeout(t, "a released connection offering Reconnect", 30*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, s := range *all {
			if s.Transport.State == proto.ConnectionEnded && s.Transport.Cause == "released" && s.Reconnect != nil {
				epoch = s.ConnectionEpoch
			}
		}
		return epoch != 0
	})
	return epoch
}
