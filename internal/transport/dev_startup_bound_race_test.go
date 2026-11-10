package transport

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
)

// CONCURRENT OPENS SHARE NOTHING BUT THE SERVER (nocx-evj4b.1.6, review round
// 4). The stand's startup bound used to travel through a map written by the
// open that decided it and read by the timer that armed from it — outside the
// lock, so two opens raced. It travels as an argument now; this is the test
// that would have caught the map, and it is the reason the shape changed.
//
// Run it with -race: the assertion is the detector, not a count.
func TestConcurrentRegistrationsCarryTheirOwnStartupBound(t *testing.T) {
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithVaultLifecycle(newFakeVaultLifecycle()))

	const workers = 8
	const rounds = 25
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				sid := session.ID(fmt.Sprintf("sid-%d-%d", w, i))
				// Half the opens carry the stand's number, half the product's:
				// the two must never bleed into each other.
				bound := time.Duration(0)
				if i%2 == 0 {
					bound = 120 * time.Second
				}
				ws.registerIntegration(sid, "/bin/sh", "starting", "", "", bound)
				ws.unregisterIntegration(sid)
			}
		}(w)
	}
	wg.Wait()
}
