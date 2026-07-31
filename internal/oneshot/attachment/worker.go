package attachment

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

type Worker struct {
	service *Service
	poll    time.Duration
	log     *slog.Logger
	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
}

func NewWorker(service *Service, poll time.Duration, log *slog.Logger) *Worker {
	if poll <= 0 {
		poll = time.Minute
	}
	if log == nil {
		log = slog.Default()
	}
	return &Worker{service: service, poll: poll, log: log.With("component", "oneshot-attachment-cleaner")}
}
func (w *Worker) Start(ctx context.Context) {
	if w == nil || w.service == nil {
		return
	}
	w.mu.Lock()
	if w.cancel != nil {
		w.mu.Unlock()
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	w.done = make(chan struct{})
	done := w.done
	w.mu.Unlock()
	go func() {
		defer close(done)
		ticker := time.NewTicker(w.poll)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				if count, err := w.service.CleanupExpired(runCtx, 100); err != nil {
					w.log.Warn("attachment cleanup failed", "err", err)
				} else if count > 0 {
					w.log.Info("expired attachments cleaned", "count", count)
				}
			}
		}
	}()
}
func (w *Worker) Shutdown(ctx context.Context) error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	cancel, done := w.cancel, w.done
	w.cancel = nil
	w.done = nil
	w.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
