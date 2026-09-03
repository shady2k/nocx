package endpoint

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
)

// Serve accepts connections on ln until ctx is done or the listener closes,
// handing each to handle on its own goroutine and closing it when handle
// returns. It waits for every handler before returning.
//
// This is the whole of the decomposition D6 asks for, and the reason it is so
// small is that nocx-k6p18.3 already put the process-scoped state where it
// belongs: the sessions, their windows and the write capability live in the
// session service, which is constructed ONCE beside this loop, while
// internal/helper/host is one connection's protocol engine and is constructed
// per accept. So a connection ending releases that connection's reader and, if
// it held it, the write capability — and every session, window and process
// survives untouched. That is D1, and it is why Bind exists rather than the
// sink being a constructor argument.
//
// Two coordinators may connect at once and both are served: D12 is same-UID
// trust, and any nocx under that account may reach the helper. Nothing here
// refuses a second connection, and nothing here is per-connection state.
//
// SHUTDOWN CLOSES THE CONNECTIONS, and it must: a handler is blocked in a read
// on its socket, and a context cannot interrupt a read. Cancelling ctx without
// closing them would leave a helper that has stopped accepting, holds every
// connection open forever and never returns from here — which on a machine
// somebody is trying to log out of is worse than not stopping at all.
func Serve(ctx context.Context, ln net.Listener, handle func(net.Conn)) error {
	var (
		mu       sync.Mutex
		live     = map[net.Conn]struct{}{}
		stopping bool
	)
	// track registers a connection unless shutdown has begun, in which case
	// the caller must close it rather than serve it: the interval a connection
	// is in this set runs from before its handler starts until the handler
	// returns, so a shutdown can never miss one that is about to be added.
	track := func(conn net.Conn) bool {
		mu.Lock()
		defer mu.Unlock()
		if stopping {
			return false
		}
		live[conn] = struct{}{}
		return true
	}
	untrack := func(conn net.Conn) {
		mu.Lock()
		delete(live, conn)
		mu.Unlock()
	}

	var closeOnce sync.Once
	shutdown := func() {
		closeOnce.Do(func() {
			_ = ln.Close()
			mu.Lock()
			stopping = true
			conns := make([]net.Conn, 0, len(live))
			for conn := range live {
				conns = append(conns, conn)
			}
			mu.Unlock()
			for _, conn := range conns {
				_ = conn.Close()
			}
		})
	}

	stop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			shutdown()
		case <-stop:
		}
	}()
	defer close(stop)

	var conns sync.WaitGroup
	defer conns.Wait()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("endpoint: accept: %w", err)
		}
		if !track(conn) {
			_ = conn.Close()
			continue
		}
		conns.Add(1)
		go func() {
			defer conns.Done()
			defer func() {
				untrack(conn)
				_ = conn.Close()
			}()
			handle(conn)
		}()
	}
}
