//go:build nocx_local_ssh

package sshsvc

import (
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/shady2k/nocx/internal/connkey"
	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/hostconn"
	"github.com/shady2k/nocx/internal/ssh"
)

type watchGateClient struct {
	Client
	entered   chan struct{}
	release   chan struct{}
	installed atomic.Bool
	calls     atomic.Int32
}

func (c *watchGateClient) WatchConnection(_ connkey.Key, _ func(hostconn.Facts)) func() {
	c.calls.Add(1)
	close(c.entered)
	<-c.release
	c.installed.Store(true)
	return func() {}
}

func TestConcurrentConnectionSubscribersWaitForInstallation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := &watchGateClient{entered: make(chan struct{}), release: make(chan struct{})}
		s := &Service{client: c}
		coordinator := new(host.Host)
		key := ssh.PooledSpec{Host: "host", Port: 22, User: "user", Identity: "principal"}.Key()
		firstDone := make(chan struct{})
		go func() { s.ensureConnectionWatch(coordinator, key); close(firstDone) }()
		<-c.entered
		// A owns the placeholder but WatchConnection has not installed anything.
		// B must await that installation, rather than treating the placeholder as
		// a completed subscription. Its return itself is the assertion boundary.
		secondStarted := make(chan struct{})
		secondDone := make(chan bool, 1)
		go func() {
			close(secondStarted)
			s.ensureConnectionWatch(coordinator, key)
			secondDone <- c.installed.Load()
		}()
		<-secondStarted
		synctest.Wait()
		select {
		case <-firstDone:
			t.Fatal("the first subscription returned before installation")
		case installed := <-secondDone:
			if !installed {
				t.Fatal("concurrent subscription returned while installation was blocked")
			}
		default:
		}
		close(c.release)
		<-firstDone
		if !<-secondDone {
			t.Fatal("concurrent subscription returned before WatchConnection installed")
		}
		if c.calls.Load() != 1 {
			t.Fatalf("installed %d subscriptions, want one", c.calls.Load())
		}
	})
}

// Keep the embedded production interface visible to the compiler: the gate
// overrides only the method this concurrency test consumes.
var _ Client = (*watchGateClient)(nil)
