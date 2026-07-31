package queue

import "time"

// RetryPolicy is deterministic exponential backoff. Jitter belongs at a
// higher scheduling layer so queue tests and audit evidence remain reproducible.
type RetryPolicy struct {
	BaseDelay time.Duration
	MaxDelay  time.Duration
}

func (p RetryPolicy) normalized() RetryPolicy {
	if p.BaseDelay <= 0 {
		p.BaseDelay = time.Second
	}
	if p.MaxDelay <= 0 {
		p.MaxDelay = time.Minute
	}
	if p.MaxDelay < p.BaseDelay {
		p.MaxDelay = p.BaseDelay
	}
	return p
}

// Delay returns the delay after the current persisted attempt (1-based).
func (p RetryPolicy) Delay(attempt int) time.Duration {
	p = p.normalized()
	if attempt <= 1 {
		return p.BaseDelay
	}
	delay := p.BaseDelay
	for i := 1; i < attempt; i++ {
		if delay >= p.MaxDelay/2 {
			return p.MaxDelay
		}
		delay *= 2
	}
	if delay > p.MaxDelay {
		return p.MaxDelay
	}
	return delay
}
