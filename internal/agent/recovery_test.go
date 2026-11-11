package agent

import (
	"testing"
	"time"
)

func TestBackoffDelay(t *testing.T) {
	// Attempts 1..7: 1, 2, 4, 8, 16, 32 minutes, then the hour cap - the
	// schedule a flaky gateway rides out in the queue.
	want := []time.Duration{
		time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute,
		16 * time.Minute, 32 * time.Minute, time.Hour, time.Hour,
	}
	for i, w := range want {
		if got := backoffDelay(i + 1); got != w {
			t.Fatalf("backoffDelay(%d) = %v, want %v", i+1, got, w)
		}
	}
}
