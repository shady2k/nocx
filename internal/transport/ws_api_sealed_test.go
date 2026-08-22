package transport

// A sealed vault is not an exchange (nocx-pgp9c.7).
//
// An environment routing through an SSH connection needs that connection's
// credential, and the credential is in the vault. With the vault sealed the
// lease cannot be taken, so nothing is dialled and nothing leaves the
// machine — and before this the send answered a RESULT: a dead run reading
// "…dial pi@192.168.0.93:22: vault is sealed", a sentence describing a door
// with nothing to press.
//
// It answers the canonical sealed ERROR now, which is the shape the
// renderer's dispatcher keys on to raise one unlock dialog and re-send the
// request verbatim (vault_sealed.go). So these tests assert the SHAPE the
// other half reads — the code AND the data — rather than merely that the
// call failed: an error with the right words and the wrong data raises no
// prompt at all, which is the same dead end wearing a different sentence.
//
// EVERY CASE GOES OVER THE REAL SOCKET, through the real sender and the real
// route table. Only the SSH pool is a double, and only because a live pooled
// connection needs an sshd: the leaser below returns the error the pool
// really produces (its own wrap of the vault's sentinel, ssh/pool.go) or a
// lease that dials a real listener. What is under test is everything between
// that error and the frame on the wire.

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/apicoll"
	"github.com/shady2k/nocx/internal/apisend"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/vault"
)

// sealedLeaser is the SSH pool as a route reaches it, refusing the way it
// really refuses when the vault is shut.
//
// The error is built the way the production chain builds it — the pool wraps
// whatever the dial returned with `%w` (ssh/pool.go), and the credential read
// inside that dial is where the sentinel comes from — so `errors.Is` has the
// same job here as there. It is NOT the vault's sentence as a string: a test
// that handed the transport the words would prove the string fallback works
// and say nothing about the chain.
type sealedLeaser struct{ asked atomic.Int64 }

func (l *sealedLeaser) LeaseForProfile(context.Context, string) (ssh.TunnelConn, error) {
	l.asked.Add(1)
	return nil, fmt.Errorf("dial pi@192.168.0.93:22: %w", vault.ErrVaultSealed)
}

// refusingLeaser refuses for a reason that has nothing to do with the vault —
// the pair that keeps the test above about SEALED and not about "a connection
// route that failed".
type refusingLeaser struct{}

func (refusingLeaser) LeaseForProfile(context.Context, string) (ssh.TunnelConn, error) {
	return nil, fmt.Errorf("dial pi@192.168.0.93:22: %w", net.ErrClosed)
}

// tcpLease is a granted lease that really carries bytes: ssh.TunnelConn over
// the local network, so its channels are ordinary TCP connections to whatever
// the test started. It is what makes "a connection whose credential the vault
// does not hold still sends while the vault is sealed" a send that ANSWERS
// rather than one that merely fails differently — and it is its own leaser,
// because a lease that needed no secret is granted by simply being handed
// over.
type tcpLease struct{ done chan struct{} }

func (c tcpLease) LeaseForProfile(context.Context, string) (ssh.TunnelConn, error) {
	return c, nil
}

func (c tcpLease) Dial(addr string) (net.Conn, error) { return net.Dial("tcp", addr) }
func (c tcpLease) Listen(string) (net.Listener, error) {
	return nil, fmt.Errorf("this lease forwards nothing")
}
func (c tcpLease) Done() <-chan struct{} { return c.done }
func (c tcpLease) LostErr() error        { return nil }
func (c tcpLease) Close() error          { return nil }

// newAPIWSServerWithLeaser builds the whole api surface over the REAL sender
// with a route table whose pool is the caller's.
func newAPIWSServerWithLeaser(t *testing.T, leaser apisend.ConnectionLeaser) *websocket.Conn {
	t.Helper()
	logger := log.NewSlogAdapter(nil)
	sender := apisend.New(
		apisend.WithLogger(logger),
		apisend.WithRoutes(apisend.NewRoutes(leaser)),
	)
	ws := NewWSServer(logger, newRegWithStub(logger),
		WithAPI(apicoll.NewCollections(apiTestPaths(t)), sender))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })
	return connectWS(t, ws)
}

// apiConnectionFolder writes a collection whose environments route the SAME
// request two ways: through a connection, and out of this machine. One
// folder, so a test can ask both questions of one open.
func apiConnectionFolder(t *testing.T, url string) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	write("nocx-collection.json", `{"schemaVersion":1,"name":"acme"}`)
	write("ping.json", `{"id":"r1","name":"ping","method":"GET","url":"`+url+`",`+
		`"headers":[],"query":[],"body":{"kind":"none"},"auth":{"kind":"none"}}`)
	write("environments/bastion.json",
		`{"name":"bastion","values":{},"route":{"kind":"connection","profileId":"ssh:custom:1"}}`)
	write("environments/here.json", `{"name":"here","values":{},"route":{"kind":"direct"}}`)
	// An environment with verification OFF, out of this machine — the
	// switch §6.5 gives a development host with a self-signed certificate.
	write("environments/unverified.json",
		`{"name":"unverified","values":{},"route":{"kind":"direct","insecureTls":true}}`)
	return root
}

// THE DEFECT, and the shape that fixes it.
func TestAPIRequestSend_ASealedVaultAnswersTheCanonicalUnlockError(t *testing.T) {
	leaser := &sealedLeaser{}
	conn := newAPIWSServerWithLeaser(t, leaser)
	root := apiConnectionFolder(t, "https://example.test/ping")
	handle := openAPICollection(t, conn, root, 1)

	resp := vaultCall(t, conn, "api.request.send", map[string]any{
		"handle": handle, "relPath": "ping.json",
		"envRelPath": "environments/bastion.json", "token": "t-1",
	}, 2)

	if resp.Error == nil {
		t.Fatalf("a sealed vault answered a RESULT (%s) — a dead row saying the vault is "+
			"shut, with nothing to press", resp.Result)
	}
	// THE CODE AND THE DATA, because the renderer reads the data: it raises
	// the unlock on `data.reason === "vault-sealed"` and on nothing else
	// (frontend/src/dispatcher.ts). A -32001 with no data raises no prompt.
	if resp.Error.Code != vaultSealedCode {
		t.Errorf("code = %d, want %d", resp.Error.Code, vaultSealedCode)
	}
	var data struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(resp.Error.Data, &data); err != nil {
		t.Fatalf("the error carries no data object for the renderer to read: %v", err)
	}
	if data.Reason != "vault-sealed" {
		t.Errorf("data.reason = %q, want vault-sealed", data.Reason)
	}
	// And the lease really was attempted — otherwise this would pass on a
	// build that refused connection routes outright.
	if n := leaser.asked.Load(); n != 1 {
		t.Errorf("the pool was asked for %d leases, want 1", n)
	}
}

// The three sends that must be UNTOUCHED. Each is a real send over the real
// socket while the same sealed leaser sits behind the route table, so the
// lifting above is proved to be about the vault rather than about anything
// that merely failed near it.
func TestAPIRequestSend_WhatASealedVaultDoesNotChange(t *testing.T) {
	t.Run("a direct route sends, because it never asks the vault anything", func(t *testing.T) {
		var reached atomic.Int64
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			reached.Add(1)
			_, _ = w.Write([]byte("pong"))
		}))
		defer srv.Close()

		leaser := &sealedLeaser{}
		conn := newAPIWSServerWithLeaser(t, leaser)
		root := apiConnectionFolder(t, srv.URL)
		handle := openAPICollection(t, conn, root, 1)

		resp := vaultCall(t, conn, "api.request.send", map[string]any{
			"handle": handle, "relPath": "ping.json",
			"envRelPath": "environments/here.json", "token": "t-1",
		}, 2)
		if resp.Error != nil {
			t.Fatalf("a direct send was refused while the vault was sealed: %+v", resp.Error)
		}
		if got := decodeSend(t, resp); got.Outcome != "answered" {
			t.Errorf("outcome = %q, want answered", got.Outcome)
		}
		if reached.Load() != 1 {
			t.Errorf("the server was reached %d times, want 1", reached.Load())
		}
		if n := leaser.asked.Load(); n != 0 {
			t.Errorf("the pool was asked %d times for a direct route", n)
		}
	})

	t.Run("a connection whose credential the vault does not hold sends too", func(t *testing.T) {
		// The lease is granted and carries the bytes, which is what a
		// profile with no vault secret behind it looks like from here. It
		// answers, while the vault is as sealed as it was in the case above.
		var reached atomic.Int64
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			reached.Add(1)
			_, _ = w.Write([]byte("through the tunnel"))
		}))
		defer srv.Close()

		done := make(chan struct{})
		defer close(done)
		conn := newAPIWSServerWithLeaser(t, tcpLease{done: done})
		root := apiConnectionFolder(t, srv.URL)
		handle := openAPICollection(t, conn, root, 1)

		resp := vaultCall(t, conn, "api.request.send", map[string]any{
			"handle": handle, "relPath": "ping.json",
			"envRelPath": "environments/bastion.json", "token": "t-1",
		}, 2)
		if resp.Error != nil {
			t.Fatalf("a connection route that needed no secret was refused: %+v", resp.Error)
		}
		got := decodeSend(t, resp)
		if got.Outcome != "answered" {
			t.Fatalf("outcome = %q, want answered", got.Outcome)
		}
		if got.Response == nil || got.Response.Text != "through the tunnel" {
			t.Errorf("the run does not carry the answer that came back: %+v", got.Response)
		}
		if reached.Load() != 1 {
			t.Errorf("the server was reached %d times through the lease, want 1", reached.Load())
		}
	})

	t.Run("a connection that fails for any other reason is still a run", func(t *testing.T) {
		conn := newAPIWSServerWithLeaser(t, refusingLeaser{})
		root := apiConnectionFolder(t, "https://example.test/ping")
		handle := openAPICollection(t, conn, root, 1)

		resp := vaultCall(t, conn, "api.request.send", map[string]any{
			"handle": handle, "relPath": "ping.json",
			"envRelPath": "environments/bastion.json", "token": "t-1",
		}, 2)
		if resp.Error != nil {
			t.Fatalf("a lease refused for a reason that is not the vault answered an "+
				"error rather than a run: %+v", resp.Error)
		}
		got := decodeSend(t, resp)
		if got.Outcome != "failed" {
			t.Errorf("outcome = %q, want failed", got.Outcome)
		}
		if got.Failure == nil || got.Failure.Phase != "connection" {
			t.Fatalf("failure = %+v, want phase connection", got.Failure)
		}
		// …and it is still a run worth reading: the request it composed is
		// on it, which is what this epic put there.
		if got.Request.Text == "" {
			t.Error("the failed run carries no request text")
		}
	})
}

// A SEALED VAULT MET MID-EXCHANGE IS STILL A RUN. This changes the
// precondition and nothing else: an exchange that got past the lease has
// gone out, so whatever ends it is a thing that happened to a request the
// person sent — and a request that HAS crossed can never be replayed
// verbatim by a dispatcher, which is what answering an error would ask for.
//
// The recording sender is the seam here rather than the route table, because
// the phase is what decides and no route can produce a sealed vault at the
// exchange phase on demand.
func TestAPIRequestSend_ASealedVaultMidExchangeIsStillARun(t *testing.T) {
	sender := &recordingSender{
		failure: &apisend.Failure{
			Phase:  apisend.PhaseExchange,
			Reason: "apisend: reading the response body of GET https://example.test: vault is sealed",
			Err:    fmt.Errorf("reading the response body: %w", vault.ErrVaultSealed),
		},
	}
	conn := newAPIWSServerWithSender(t, sender)
	root := apiConnectionFolder(t, "https://example.test/ping")
	handle := openAPICollection(t, conn, root, 1)

	resp := vaultCall(t, conn, "api.request.send", map[string]any{
		"handle": handle, "relPath": "ping.json",
		"envRelPath": "environments/bastion.json", "token": "t-1",
	}, 2)
	if resp.Error != nil {
		t.Fatalf("a vault that sealed mid-exchange answered an error: %+v — that request "+
			"went out, so it is a run and cannot be re-sent verbatim", resp.Error)
	}
	got := decodeSend(t, resp)
	if got.Outcome != "failed" {
		t.Errorf("outcome = %q, want failed", got.Outcome)
	}
	if got.Failure == nil || got.Failure.Phase != "exchange" {
		t.Errorf("failure = %+v, want phase exchange", got.Failure)
	}
}

// The token is FREE again when the sealed error goes out, because the
// renderer re-sends the request verbatim — the same token — the moment the
// vault answers. A registration still standing would refuse the replay as a
// duplicate, and the unlock would end in the error it exists to prevent.
func TestAPIRequestSend_ASealedRefusalReleasesTheTokenForTheReplay(t *testing.T) {
	conn := newAPIWSServerWithLeaser(t, &sealedLeaser{})
	root := apiConnectionFolder(t, "https://example.test/ping")
	handle := openAPICollection(t, conn, root, 1)

	params := map[string]any{
		"handle": handle, "relPath": "ping.json",
		"envRelPath": "environments/bastion.json", "token": "same-token",
	}
	first := vaultCall(t, conn, "api.request.send", params, 2)
	if first.Error == nil || first.Error.Code != vaultSealedCode {
		t.Fatalf("expected the sealed error, got %+v / %s", first.Error, first.Result)
	}

	// The replay, verbatim, exactly as the dispatcher sends it.
	again := vaultCall(t, conn, "api.request.send", params, 3)
	if again.Error != nil && again.Error.Code == -32602 {
		t.Fatalf("the replay was refused as a duplicate token: %+v", again.Error)
	}
	// It is sealed again here — this backend has no vault to unlock — and
	// that is the right answer: what is asserted is that the token was not
	// what stopped it.
	if again.Error == nil || again.Error.Code != vaultSealedCode {
		t.Fatalf("the replay answered %+v / %s, want the sealed error again", again.Error, again.Result)
	}
}
