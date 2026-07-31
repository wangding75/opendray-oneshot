package delivery

import (
	"testing"
	"time"
)

func TestChannelLimiterReservesDistinctConcurrentSlots(t *testing.T) {
	fixed := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	limiter := newChannelLimiter()
	limiter.now = func() time.Time { return fixed }

	interval := minimumInterval("telegram")
	if got := limiter.reserve("channel-1", "telegram"); got != 0 {
		t.Fatalf("first reservation=%s; want 0", got)
	}
	if got := limiter.reserve("channel-1", "telegram"); got != interval {
		t.Fatalf("second reservation=%s; want %s", got, interval)
	}
	if got := limiter.reserve("channel-1", "telegram"); got != 2*interval {
		t.Fatalf("third reservation=%s; want %s", got, 2*interval)
	}
	if got := limiter.reserve("channel-2", "telegram"); got != 0 {
		t.Fatalf("independent channel reservation=%s; want 0", got)
	}
}
