package app

import (
	"context"
	"errors"
	"net"
	"os"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	pkgsftp "github.com/pkg/sftp"

	"github.com/shady2k/nocx/internal/filesystem"
	fssftp "github.com/shady2k/nocx/internal/filesystem/sftp"
	"github.com/shady2k/nocx/internal/ssh"
)

// A blackhole accepts writes without delivering them. The real SFTP client
// and lease watchdog still own requests, expiry and close-to-cancel.
type listingBlackhole struct {
	net.Conn
	mu      sync.Mutex
	blocked bool
	written chan struct{}
}

func (s *listingBlackhole) Write(p []byte) (int, error) {
	s.mu.Lock()
	blocked := s.blocked
	s.mu.Unlock()
	if !blocked {
		return s.Conn.Write(p)
	}
	select {
	case s.written <- struct{}{}:
	default:
	}
	return len(p), nil
}

func listingLease(t *testing.T) (ssh.FSConn, *listingBlackhole) {
	t.Helper()
	client, remote := net.Pipe()
	server, err := pkgsftp.NewServer(remote)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		defer func() { _ = remote.Close() }()
		_ = server.Serve()
	}()
	stream := &listingBlackhole{Conn: client, written: make(chan struct{}, 1)}
	lease, err := ssh.NewFSConn(context.Background(), stream)
	if err != nil {
		t.Fatal(err)
	}
	return lease, stream
}

func TestFilesListingSharedLeaseDeadlineStaysATimeout(t *testing.T) {
	root := t.TempDir()
	synctest.Test(t, func(t *testing.T) {
		lease, network := listingLease(t)
		defer func() { _ = lease.Close() }()
		provider := fssftp.New(fsTransferLease{FSConn: lease}, fssftp.WithListTimeout(fsRemoteListTimeout))
		page := filesystem.Page{Limit: 1}
		if _, err := provider.List(context.Background(), root, page); err != nil {
			t.Fatalf("listing before the blackhole: %v", err)
		}
		network.mu.Lock()
		network.blocked = true
		network.mu.Unlock()

		// A watch poll takes the same lease just before the person's expand.
		background := make(chan error, 1)
		go func() {
			_, err := provider.List(context.Background(), root, page)
			background <- err
		}()
		<-network.written
		synctest.Wait()
		time.Sleep(time.Millisecond)
		ctx, cancel := context.WithTimeout(context.Background(), fsRemoteListTimeout)
		defer cancel()
		pending := make(chan error, 1)
		go func() {
			_, err := provider.List(ctx, root, page)
			pending <- err
		}()
		<-network.written
		synctest.Wait()

		// A distinct healthy lease still answers while both calls are pending.
		healthy, _ := listingLease(t)
		defer func() { _ = healthy.Close() }()
		other := fssftp.New(fsTransferLease{FSConn: healthy})
		if _, err := other.List(context.Background(), root, page); err != nil {
			t.Fatalf("healthy domain beside the blackhole: %v", err)
		}
		for _, result := range []<-chan error{background, pending} {
			err := <-result
			if !errors.Is(err, ssh.ErrFSDead) {
				t.Fatalf("the lease watchdog did not settle the call: %v", err)
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("listing lost its timeout classification before the Files handler: %v", err)
			}
		}
		if ctx.Err() != nil {
			t.Fatalf("the person's own deadline fired first: %v", ctx.Err())
		}
	})
}

type listingErrorLease struct {
	fakeLease
	canonicalErr error
	listErr      error
}

func (l *listingErrorLease) RealPath(path string) (string, error) {
	return path, l.canonicalErr
}

func (l *listingErrorLease) ReadDir(context.Context, string) ([]os.FileInfo, error) {
	return nil, l.listErr
}

func TestFilesListingWatchdogDuringEnumerationIsTimedOut(t *testing.T) {
	provider := fssftp.New(
		fsTransferLease{FSConn: &listingErrorLease{listErr: ssh.ErrFSDead}},
		fssftp.WithListTimeout(fsRemoteListTimeout),
	)
	listing, err := provider.List(context.Background(), "/churn/d20", filesystem.Page{Limit: 1})
	var timedOut *filesystem.ErrTimedOut
	if !errors.As(err, &timedOut) || timedOut.Timeout != fsRemoteListTimeout {
		t.Fatalf("enumeration deadline = %v, want the provider's timedOut state", err)
	}
	if len(listing.Entries) != 0 {
		t.Fatal("a timed-out listing returned partial entries")
	}
}

func TestFilesListingPreservesNonTimeoutFailures(t *testing.T) {
	for _, cause := range []error{
		ssh.ErrFSClosed,
		ssh.ErrFSLost,
		context.Canceled,
		os.ErrPermission,
		errors.New(ssh.ErrFSDead.Error()), // text is not a deadline discriminator
	} {
		t.Run(cause.Error(), func(t *testing.T) {
			for _, at := range []string{"canonical", "enumeration"} {
				t.Run(at, func(t *testing.T) {
					lease := &listingErrorLease{}
					if at == "canonical" {
						lease.canonicalErr = cause
					} else {
						lease.listErr = cause
					}
					provider := fssftp.New(fsTransferLease{FSConn: lease})
					_, err := provider.List(context.Background(), "/churn/d20", filesystem.Page{Limit: 1})
					var timedOut *filesystem.ErrTimedOut
					if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &timedOut) {
						t.Fatalf("ordinary failure became a timeout: %v", err)
					}
					if !errors.Is(err, cause) {
						t.Fatalf("listing lost its original cause: %v", err)
					}
				})
			}
		})
	}
}
