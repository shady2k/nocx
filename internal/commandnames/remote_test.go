package commandnames_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/commandnames"
)

// fakeConn records the commands it was given and answers scripted output.
type fakeConn struct {
	mu     sync.Mutex
	cmds   []string
	answer func(cmd string) (*commandnames.ExecResult, error)
	closed bool
}

func (c *fakeConn) Exec(_ context.Context, cmd string) (*commandnames.ExecResult, error) {
	c.mu.Lock()
	c.cmds = append(c.cmds, cmd)
	c.mu.Unlock()
	return c.answer(cmd)
}

func (c *fakeConn) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	return nil
}

// nonceOf reads the nonce back out of the command the source built, so the
// fixture can frame its answer the way the far side would.
func nonceOf(cmd string) string {
	const marker = "sh -s "
	i := strings.Index(cmd, marker)
	if i < 0 {
		return ""
	}
	rest := cmd[i+len(marker):]
	end := strings.IndexAny(rest, " \n")
	if end < 0 {
		return ""
	}
	return rest[:end]
}

func remoteSource(answer func(cmd string) (*commandnames.ExecResult, error)) (*commandnames.RemoteSource, *fakeConn) {
	conn := &fakeConn{answer: answer}
	src := commandnames.NewRemoteSource("ssh:deploy@app1:22", "v39",
		func(context.Context) (commandnames.ExecConn, error) { return conn, nil })
	return src, conn
}

func TestRemoteSource_ProbeAndScanReadTheFramedAnswer(t *testing.T) {
	src, conn := remoteSource(func(cmd string) (*commandnames.ExecResult, error) {
		n := nonceOf(cmd)
		if strings.Contains(cmd, "NOCX_CN %s BEGIN") && strings.Contains(cmd, "printf 'V 1") {
			return &commandnames.ExecResult{Stdout: []byte(
				"Welcome to app1\nNOCX_CN " + n + " BEGIN\nV 1\nU deploy\nF bash\nP /usr/bin\nD /usr/bin\nS 42\nNOCX_CN " + n + " END\n")}, nil
		}
		return &commandnames.ExecResult{Stdout: []byte(
			"NOCX_CN " + n + " BEGIN\nN ls\nN grep\nNOCX_CN " + n + " END\n")}, nil
	})

	p, err := src.Probe(context.Background())
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if p.User != "deploy" || p.Path != "/usr/bin" || len(p.Stamps) != 1 {
		t.Fatalf("probe = %+v", p)
	}
	scan, err := src.Scan(context.Background(), p)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if strings.Join(scan.Names, ",") != "ls,grep" {
		t.Fatalf("names = %v", scan.Names)
	}
	// The lease is released after every call: a discovery lease held past
	// its question is a pooled connection nobody else can have.
	if !conn.closed {
		t.Fatalf("the lease was not closed")
	}
	if src.Identity().Route != "ssh:deploy@app1:22" {
		t.Fatalf("identity = %+v", src.Identity())
	}
}

// A truncated remote answer is a prefix, and a prefix is never published. It
// reports the deadline's state because the cause is the same — a bound
// stopped the work.
func TestRemoteSource_ATruncatedAnswerIsNeverPublished(t *testing.T) {
	src, _ := remoteSource(func(cmd string) (*commandnames.ExecResult, error) {
		n := nonceOf(cmd)
		return &commandnames.ExecResult{
			Stdout:    []byte("NOCX_CN " + n + " BEGIN\nN ls\n"),
			Truncated: true,
		}, nil
	})
	_, err := src.Scan(context.Background(), commandnames.Probe{})
	if !errors.Is(err, commandnames.ErrScanDeadline) {
		t.Fatalf("err = %v, want ErrScanDeadline", err)
	}
}

// An answer whose frame never closes — the far side was cut off mid
// enumeration — is rejected whole rather than half-parsed.
func TestRemoteSource_AnUnclosedFrameIsRejectedWhole(t *testing.T) {
	src, _ := remoteSource(func(cmd string) (*commandnames.ExecResult, error) {
		n := nonceOf(cmd)
		return &commandnames.ExecResult{Stdout: []byte("NOCX_CN " + n + " BEGIN\nN ls\nN grep\n")}, nil
	})
	if _, err := src.Scan(context.Background(), commandnames.Probe{}); err == nil {
		t.Fatalf("an unclosed frame was accepted")
	}
}

// A refused exec is `failed`, not `timed-out`: the two are different facts
// and the surface tells them apart.
func TestRemoteSource_ARefusedExecIsAFailureNotADeadline(t *testing.T) {
	src, _ := remoteSource(func(string) (*commandnames.ExecResult, error) {
		return nil, errors.New("exec request refused")
	})
	_, err := src.Scan(context.Background(), commandnames.Probe{})
	if err == nil {
		t.Fatalf("a refused exec returned a result")
	}
	if errors.Is(err, commandnames.ErrScanDeadline) {
		t.Fatalf("a refusal was reported as a deadline: %v", err)
	}
}

// A non-zero exit status is a failure of the far side's shell, not an empty
// answer.
func TestRemoteSource_ANonZeroExitIsAFailure(t *testing.T) {
	src, _ := remoteSource(func(cmd string) (*commandnames.ExecResult, error) {
		n := nonceOf(cmd)
		return &commandnames.ExecResult{
			Stdout:     []byte("NOCX_CN " + n + " BEGIN\nN ls\nNOCX_CN " + n + " END\n"),
			ExitStatus: 127,
		}, nil
	})
	if _, err := src.Scan(context.Background(), commandnames.Probe{}); err == nil {
		t.Fatalf("exit 127 was accepted")
	}
}

// Every remote call is bounded and every failure of the lease is named,
// rather than surfacing as a nil-pointer somewhere downstream.
func TestRemoteSource_ALeaseFailureIsNamed(t *testing.T) {
	src := commandnames.NewRemoteSource("ssh:deploy@app1:22", "v39",
		func(context.Context) (commandnames.ExecConn, error) {
			return nil, errors.New("no route to host")
		})
	_, err := src.Probe(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no route to host") {
		t.Fatalf("err = %v", err)
	}
}
