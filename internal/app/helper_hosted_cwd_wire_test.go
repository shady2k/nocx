//go:build nocx_local_ssh

package app

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	helperclient "github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/host"
	helpersession "github.com/shady2k/nocx/internal/helper/session"
	"github.com/shady2k/nocx/internal/helper/sshdial"
	"github.com/shady2k/nocx/internal/helper/sshsvc"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/transport"
	gossh "golang.org/x/crypto/ssh"
)

type liveHostedCwdResolver struct {
	host   string
	config *ssh.ConnectConfig
}

func withLiveSessionHome(path string) liveSshdOption {
	return func(c *liveSshdConfig) { c.sessionHome = path }
}

func (r liveHostedCwdResolver) Resolve(_ string) (string, *ssh.ConnectConfig, error) {
	return r.host, r.config, nil
}

func TestARealRemoteCDReachesTheAttachedRendererAsCwdMetadata(t *testing.T) {
	shortRoot, err := os.MkdirTemp("/tmp", "nx")
	if err != nil {
		t.Fatalf("short fixture root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(shortRoot) })
	fx := startLiveSshd(t, true, withLiveSessionHome(filepath.Join(shortRoot, "h")))
	logger := logtest.Slog(t)
	sshLog := log.NewSlogAdapter(logger)
	signerBlock, err := gossh.MarshalPrivateKey(fx.clientRaw, "")
	if err != nil {
		t.Fatalf("marshal fixture key: %v", err)
	}
	key := liveFixtureKeyResolver{pem: pem.EncodeToMemory(signerBlock)}
	rc, err := ssh.NewReal(sshLog, ssh.WithKnownHostsFile(fx.knownHostsPath(t)))
	if err != nil {
		t.Fatalf("ssh.NewReal: %v", err)
	}
	t.Cleanup(func() { _ = rc.Close() })

	// This is the real local helper protocol and SSH spawner. The only fake
	// endpoint is absent: its SSH service dials the real live-sshd above.
	helperEnd, coordEnd := net.Pipe()
	dialer, err := sshdial.New(logger)
	if err != nil {
		t.Fatalf("sshdial.New: %v", err)
	}
	sshService := sshsvc.New(dialer, logger)
	sessions := helpersession.New(helpersession.Options{
		Generation: filesFixtureGenera,
		Spawner:    &scriptedSpawner{},
		SSHSpawner: helpersession.NewSSHSpawner(sshService, logger),
		Log:        logger,
	})
	// The daemon ends with the test, so the sessions it holds do too
	// (nocx-1ebrs.66.1.29).
	t.Cleanup(sessions.Close)
	engine := host.New(helperEnd, helperEnd, filesFixtureGenera, "cwd-wire-helper", logger)
	engine.Register(sshService)
	engine.Register(sessions)
	unbind := sessions.Bind(engine)
	t.Cleanup(unbind)
	helperCtx, stopHelper := context.WithCancel(context.Background())
	helperDone := make(chan struct{})
	go func() { defer close(helperDone); _ = engine.Serve(helperCtx) }()
	t.Cleanup(func() {
		stopHelper()
		select {
		case <-helperDone:
		case <-time.After(5 * time.Second):
			t.Error("helper did not stop")
		}
	})
	peer, err := helperclient.Dial(helperCtx, helperclient.Config{
		Exec: helperclient.NewSocketConn(coordEnd), ExpectHash: filesFixtureGenera,
		SentinelTTL: 5 * time.Second,
		Reverse:     helperReverseHandlers(rc, key, &helperPrompt{log: logger}, nil, logger),
		Log:         logger,
	})
	if err != nil {
		t.Fatalf("helperclient.Dial: %v", err)
	}
	t.Cleanup(func() { _ = peer.Close() })

	registry := session.New(sshLog, nil)
	opener := &localHelperOpener{
		log: logger, registry: registry, client: peer, sshTargets: rc,
		dir: t.TempDir(), installed: helperInstalledStub(),
	}
	lifePub := lifecyclepub.New(lifecycle.New(lifecycle.Options{}))
	opener.kernel = lifePub
	overHelper := &sshOverHelper{local: opener, resolve: rc, log: logger}
	probes := &helperProbes{local: opener, resolve: rc}
	leases := installLeaseRoutes{viaLocal: overHelper, probes: probes}
	source := realHelperArtifacts(t)
	// THE MACHINE IS ANSWERED BY ITS REAL KEY (ADR-0105): this stand opens in
	// helper mode against a live sshd, and the fingerprint the resolver reads
	// is the one that fixture minted — no canned identity can stand in for it.
	consentStore, installStore := testConsentStoresFor(t, gossh.FingerprintSHA256(fx.hostKey))
	_, helperReg := helperGitFactory(leases, source, consentStore, installStore, logger)
	helperReg.registry = registry
	helperReg.lifecycle = lifePub
	hosted := &hostedOpeners{local: opener, remote: helperReg}
	resolverConfig := &ssh.ConnectConfig{
		User: fx.user, AuthMode: "publickey", Secrets: key,
		KeySecretID: liveFixtureKeyRef, AuthorizedEndpoint: fx.addr,
		DesiredMode: string(profile.DesiredHelper), Shell: ssh.ShellBash,
	}
	ws := transport.NewWSServer(sshLog, registry,
		transport.WithProfileResolver(liveHostedCwdResolver{host: fx.addr, config: resolverConfig}),
		transport.WithHelperSessionOpener(hosted),
		transport.WithLifecyclePublisher(lifePub),
	)
	helperReg.publishScreen = ws.PublishScreenFrame
	lifePub.SetEmitter(ws)
	if startErr := ws.Start(context.Background()); startErr != nil {
		t.Fatalf("start WSServer: %v", startErr)
	}
	t.Cleanup(func() { _ = ws.Stop(context.Background()) })

	opened, err := ws.OpenSession(context.Background(), transport.OpenSpec{
		Kind: "ssh", ProfileID: "live-sshd-cwd", Cols: 100, Rows: 30,
	})
	if err != nil {
		t.Fatalf("open helper-hosted SSH session: %v", err)
	}
	sid := opened.Session.ID()
	conn := dialTestWSServer(t, ws)
	attachAndAckRenderer(t, conn, string(sid))
	readyCtx, readyCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer readyCancel()
	ready, err := ws.AwaitIntegration(readyCtx, sid)
	if err != nil {
		t.Fatalf("wait for the real shell's integration readiness: %v", err)
	}
	if !ready.Registered || ready.Status == transport.IntegrationStarting {
		t.Fatalf("integration readiness = %+v, want a resolved shell", ready)
	}

	target := filepath.Join(t.TempDir(), "remote-cwd")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatalf("mkdir cwd: %v", err)
	}
	const marker = "NOCX_REAL_REMOTE_CWD_PROBE"
	command := "cd '" + target + "' && printf '" + marker + ":%s\\n' \"$PWD\"\n"
	if _, err := opened.Session.Write([]byte(command)); err != nil {
		t.Fatalf("write real cd and proof command: %v", err)
	}

	sidBytes, decodeErr := hex.DecodeString(string(sid))
	if decodeErr != nil || len(sidBytes) != 16 {
		t.Fatalf("invalid session id %q: %v", sid, decodeErr)
	}
	var expectedSID [16]byte
	copy(expectedSID[:], sidBytes)
	var output strings.Builder
	var streamOffset uint64
	deadline := time.Now().Add(40 * time.Second)
	if err := conn.SetReadDeadline(deadline); err != nil {
		t.Fatalf("set public websocket read deadline: %v", err)
	}
	for time.Now().Before(deadline) {
		messageType, payload, err := conn.ReadMessage()
		if err != nil {
			if netErr, ok := err.(interface{ Timeout() bool }); ok && netErr.Timeout() {
				break
			}
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) || strings.Contains(err.Error(), "unexpected EOF") {
				break
			}
			t.Fatalf("read active session public wire: %v", err)
		}
		if messageType != websocket.BinaryMessage {
			continue
		}
		frame, err := transport.DecodeFrame(payload)
		if err != nil {
			t.Fatalf("decode active session public frame: %v", err)
		}
		if frame.SessionID != expectedSID {
			continue
		}
		if frame.MsgType == transport.MsgTypeData {
			output.Write(frame.Payload)
			streamOffset += uint64(len(frame.Payload))
			sendPublicAck(t, conn, string(sid), streamOffset)
			continue
		}
		if frame.MsgType != transport.MsgTypeMetadata {
			continue
		}
		var doc struct {
			Revision uint64 `json:"revision"`
			State    struct {
				Cwd *struct {
					Path string `json:"path"`
				} `json:"cwd"`
			} `json:"state"`
		}
		if err := json.Unmarshal(frame.Payload, &doc); err != nil {
			t.Fatalf("decode metadata frame: %v\n%s", err, frame.Payload)
		}
		if doc.State.Cwd != nil && strings.TrimSuffix(doc.State.Cwd.Path, "/") == strings.TrimSuffix(target, "/") {
			return
		}
	}
	if !strings.Contains(output.String(), marker+":"+target) {
		t.Fatalf("remote shell did not prove `cd` on its public type-1 stream; output=%q", output.String())
	}
	t.Fatalf("real remote shell emitted the cd proof on type-1, but no public type-2 frame carried cwd %q for active session %s", target, sid)
}

func attachAndAckRenderer(t *testing.T, conn *websocket.Conn, sid string) {
	t.Helper()
	request, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "attach", "params": map[string]any{"sessionId": sid, "offset": 0}})
	if err != nil {
		t.Fatalf("marshal attach: %v", err)
	}
	if writeErr := conn.WriteMessage(websocket.TextMessage, request); writeErr != nil {
		t.Fatalf("send attach: %v", writeErr)
	}
	sidBytes, err := hex.DecodeString(sid)
	if err != nil {
		t.Fatalf("decode session id: %v", err)
	}
	var expected [16]byte
	copy(expected[:], sidBytes)
	var offset uint64
	for {
		messageType, payload, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read while attaching: %v", err)
		}
		if messageType == websocket.BinaryMessage {
			frame, err := transport.DecodeFrame(payload)
			if err != nil {
				t.Fatalf("decode replay frame: %v", err)
			}
			if frame.SessionID == expected && frame.MsgType == transport.MsgTypeData {
				offset += uint64(len(frame.Payload))
				sendPublicAck(t, conn, sid, offset)
			}
			continue
		}
		var envelope struct {
			ID uint64 `json:"id"`
		}
		if json.Unmarshal(payload, &envelope) == nil && envelope.ID == 1 {
			return
		}
	}
}

func sendPublicAck(t *testing.T, conn *websocket.Conn, sid string, offset uint64) {
	t.Helper()
	data, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "ack", "params": map[string]any{"sessionId": sid, "offset": offset}})
	if err != nil {
		t.Fatalf("marshal ack: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("ack public output: %v", err)
	}
}

func dialTestWSServer(t *testing.T, ws *transport.WSServer) *websocket.Conn {
	t.Helper()
	u := url.URL{Scheme: "ws", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(ws.Port())), Path: "/session"}
	d := websocket.Dialer{Subprotocols: []string{"nocx.token." + ws.Token()}}
	conn, _, err := d.Dial(u.String(), nil)
	if err != nil {
		t.Fatalf("dial test WSServer: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}
