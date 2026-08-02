package ssh

import (
	"time"

	gossh "golang.org/x/crypto/ssh"
)

// startKeepalive launches a goroutine that sends keepalive@openssh.com probes
// on the SSH connection at the given interval. It returns a stop function that
// signals the goroutine to exit, and a done channel that is closed when the
// goroutine has terminated (useful in tests to verify clean shutdown). Passing
// a zero interval is a no-op (returns nil, nil).
//
// Each probe requests a reply (wantReply=true). On failure (error or false
// reply), a counter is incremented; when it reaches countMax the connection
// is closed via gclient.Close(). A successful probe resets the counter.
//
// The returned stop function is safe to call only once (close of closed
// channel panics). In practice it is called exactly once from
// pooledSSHConn.Close's closeOnce guard.
func startKeepalive(gclient *gossh.Client, interval time.Duration, countMax int) (func(), <-chan struct{}) {
	if interval <= 0 {
		return nil, nil
	}
	stopCh := make(chan struct{})
	doneCh := make(chan struct{})
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		defer close(doneCh)
		var failures int
		for {
			select {
			case <-ticker.C:
				ok, _, err := gclient.SendRequest("keepalive@openssh.com", true, nil)
				if err != nil || !ok {
					failures++
					if countMax > 0 && failures >= countMax {
						_ = gclient.Close()
						return
					}
					if countMax <= 0 {
						_ = gclient.Close()
						return
					}
				} else {
					failures = 0
				}
			case <-stopCh:
				return
			}
		}
	}()
	return func() { close(stopCh) }, doneCh
}
