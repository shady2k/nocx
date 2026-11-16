package hostconn

import (
	"testing"
	"time"
)

func TestRetryJitterStaysWithinCap(t *testing.T) {
	for attempt := 1; attempt <= 100; attempt++ {
		for sample := 0; sample < 100; sample++ {
			delay := retryDelay(attempt)
			if delay <= 0 || delay > 15*time.Second {
				t.Fatalf("attempt %d delay %s", attempt, delay)
			}
		}
	}
}
