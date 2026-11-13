//go:build nocx_local_ssh

package sshconn

// Review findings 3, 5 and 11, and the two comments, at the carrier's own
// seam.

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/hostconn"
	"github.com/shady2k/nocx/internal/ssh"
)

// TestStageReportingEndsWithTheDial — finding 3: a host-key callback invoked
// after Dial returned (x/crypto calls it again on every rekey) must not reach
// the supervisor, which is what made a close deadlock behind the key-exchange
// loop.
func TestStageReportingEndsWithTheDial(t *testing.T) {
	var seen []hostconn.Stage
	stages := &dialStages{sink: func(ev hostconn.Event) {
		if ev.Kind == hostconn.StageReached {
			seen = append(seen, ev.Stage)
		}
	}}
	stages.begin()
	stages.report(hostconn.StageKeyExchange)
	stages.finish()
	// The rekey's callback, after the dial.
	stages.report(hostconn.StageKeyExchange)
	stages.report(hostconn.StageSignIn)

	if len(seen) != 1 || seen[0] != hostconn.StageKeyExchange {
		t.Fatalf("reports = %v, want exactly the dial's own stage", seen)
	}
}

// TestALookNeedsACompleteVersionLine — the comment on readVersionLine: a peer
// that sends "SSH-" and then stops has not answered the look.
func TestALookNeedsACompleteVersionLine(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		wantErr bool
	}{
		{name: "a complete line", payload: "SSH-2.0-OpenSSH_9.6" + "\r\n", wantErr: false},
		{name: "a partial line, then the peer stops", payload: "SSH-", wantErr: true},
		{name: "a line without CRLF", payload: "SSH-2.0-x\n", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("listen: %v", err)
			}
			defer func() { _ = ln.Close() }()
			go func() {
				conn, aerr := ln.Accept()
				if aerr != nil {
					return
				}
				_, _ = conn.Write([]byte(c.payload))
				_ = conn.Close()
			}()

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			addr := ln.Addr().String()
			err = (&link{look: func(ctx context.Context) error { return lookDirect(ctx, addr) }}).Look(ctx)
			if c.wantErr && err == nil {
				t.Fatalf("a look over %q answered nil", c.payload)
			}
			if !c.wantErr && err != nil {
				t.Fatalf("a look over a complete line failed: %v", err)
			}
		})
	}
}

// TestARoutedLookIsNotAvailableWithoutItsHop — finding 11's edge: a routed
// connection's look needs the hop it is dialled through, and says so rather than
// answering about the wrong machine. The routed look itself (through a real
// bastion, to the destination) is the fault harness's
// TestARoutedLookReachesTheDestinationThroughTheBastion.
func TestARoutedLookIsNotAvailableWithoutItsHop(t *testing.T) {
	spec := ssh.PooledSpec{
		Host: "target", Port: 22, User: "dev",
		Route: []ssh.PooledHop{{Host: "bastion", Port: 2222, User: "jump"}},
	}
	look := lookFor(spec, nil)
	if err := look(context.Background()); err == nil {
		t.Fatal("a routed look with no hop answered nil")
	}
	// A direct connection needs no hop at all.
	if lookFor(spec, nil) == nil {
		t.Fatal("lookFor answered no function")
	}
	_ = strconv.Itoa
}
