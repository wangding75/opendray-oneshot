package delivery

import (
	"context"
	"sync"
	"time"
)

// RateLimiter limits transport operations only. It does not gate or retry the
// business action that produced the outbound notification.
type RateLimiter interface {
	Wait(ctx context.Context, channelID, kind string) error
}

type channelLimiter struct {
	mu   sync.Mutex
	last map[string]time.Time
	now  func() time.Time
}

func newChannelLimiter() *channelLimiter {
	return &channelLimiter{last: make(map[string]time.Time), now: time.Now}
}

func (l *channelLimiter) Wait(ctx context.Context, channelID, kind string) error {
	wait := l.reserve(channelID, kind)
	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// reserve atomically assigns the next transport slot. Reserving while holding
// the lock prevents concurrent senders from observing the same previous
// timestamp and bursting together after an identical wait.
func (l *channelLimiter) reserve(channelID, kind string) time.Duration {
	interval := minimumInterval(kind)
	if interval <= 0 {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	slot := now
	if next := l.last[channelID].Add(interval); next.After(slot) {
		slot = next
	}
	l.last[channelID] = slot
	return slot.Sub(now)
}

func minimumInterval(kind string) time.Duration {
	switch kind {
	case "telegram":
		return 35 * time.Millisecond
	case "discord":
		return 200 * time.Millisecond
	case "slack", "feishu", "dingtalk", "wecom":
		return 100 * time.Millisecond
	default:
		return 0
	}
}

type noRateLimit struct{}

func (noRateLimit) Wait(context.Context, string, string) error { return nil }
