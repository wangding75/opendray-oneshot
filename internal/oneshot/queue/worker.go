package queue

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"time"

	"github.com/opendray/opendray-v2/internal/oneshot/domain"
)

// Worker polls the durable queue until its context is cancelled.
type Worker struct {
	repository   Repository
	processor    Processor
	workerID     string
	claimLimit   int
	lease        time.Duration
	pollInterval time.Duration
	retryPolicy  RetryPolicy
	log          *slog.Logger
}

type WorkerOption func(*Worker)

func WithWorkerTiming(pollInterval, lease time.Duration) WorkerOption {
	return func(worker *Worker) {
		if pollInterval > 0 {
			worker.pollInterval = pollInterval
		}
		if lease > 0 {
			worker.lease = lease
		}
	}
}

func WithWorkerClaimLimit(limit int) WorkerOption {
	return func(worker *Worker) {
		if limit > 0 {
			worker.claimLimit = limit
		}
	}
}

func WithWorkerRetryPolicy(policy RetryPolicy) WorkerOption {
	return func(worker *Worker) { worker.retryPolicy = policy.normalized() }
}

func WithWorkerLogger(log *slog.Logger) WorkerOption {
	return func(worker *Worker) {
		if log != nil {
			worker.log = log
		}
	}
}

func NewWorker(repository Repository, processor Processor, workerID string, options ...WorkerOption) (*Worker, error) {
	if repository == nil || processor == nil {
		return nil, domain.InvalidRequestf("queue repository and processor are required")
	}
	if strings.TrimSpace(workerID) == "" {
		return nil, domain.InvalidRequestf("worker_id is required")
	}
	worker := &Worker{
		repository: repository, processor: processor, workerID: workerID,
		claimLimit: DefaultClaimLimit, lease: DefaultLease,
		pollInterval: DefaultPollInterval,
		retryPolicy:  RetryPolicy{}.normalized(),
		log:          slog.Default(),
	}
	for _, option := range options {
		option(worker)
	}
	return worker, nil
}

// Run blocks until ctx is cancelled. An immediate drain avoids startup delay.
func (w *Worker) Run(ctx context.Context) {
	w.drain(ctx)
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.drain(ctx)
		}
	}
}

// DrainOnce is a deterministic test and application lifecycle seam.
func (w *Worker) DrainOnce(ctx context.Context) error {
	claims, err := w.repository.ClaimDue(ctx, w.workerID, w.claimLimit, w.lease)
	if err != nil {
		return err
	}
	for _, claim := range claims {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := w.process(ctx, claim); err != nil {
			return err
		}
	}
	return nil
}

func (w *Worker) drain(ctx context.Context) {
	if err := w.DrainOnce(ctx); err != nil && ctx.Err() == nil {
		w.log.WarnContext(ctx, "oneshot queue drain failed", "worker_id", w.workerID, "err", err)
	}
}

func (w *Worker) process(ctx context.Context, claim Claim) (err error) {
	processCtx, cancelProcess := context.WithCancel(ctx)
	defer cancelProcess()

	stopHeartbeat := make(chan struct{})
	heartbeatDone := make(chan error, 1)
	go func() {
		heartbeatErr := w.maintainLease(processCtx, claim.Delivery.ID, stopHeartbeat)
		if heartbeatErr != nil {
			cancelProcess()
		}
		heartbeatDone <- heartbeatErr
	}()

	outcome := Outcome{Action: ActionRetry, Code: domain.ErrorInternal}
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				w.log.ErrorContext(processCtx, "oneshot processor panic",
					"delivery_id", claim.Delivery.ID,
					"worker_id", w.workerID,
					"panic", recovered,
					"stack", string(debug.Stack()))
				outcome = Outcome{Action: ActionRetry, Code: domain.ErrorInternal}
			}
		}()
		outcome = w.processor.Process(processCtx, claim)
	}()

	close(stopHeartbeat)
	if heartbeatErr := <-heartbeatDone; heartbeatErr != nil {
		cancelProcess()
		return heartbeatErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if validationErr := outcome.Validate(); validationErr != nil {
		outcome = Outcome{Action: ActionDeadLetter, Code: domain.ErrorInternal}
	}

	switch outcome.Action {
	case ActionAck:
		_, err = w.repository.Ack(ctx, claim.Delivery.ID, w.workerID)
		if err == nil {
			if observer, ok := w.processor.(AckObserver); ok {
				err = observer.Acked(ctx, claim)
			}
		}
	case ActionRecover:
		// Leave the current lease/run attachment for the crash reconciler.
		err = nil
	case ActionRetry:
		_, err = w.repository.Nack(ctx, claim.Delivery.ID, w.workerID, outcome.Code, w.retryPolicy)
	case ActionDeadLetter:
		_, err = w.repository.DeadLetter(ctx, claim.Delivery.ID, w.workerID, outcome.Code)
	default:
		err = fmt.Errorf("unsupported queue action %q", outcome.Action)
	}
	return err
}

func (w *Worker) maintainLease(ctx context.Context, deliveryID string, stop <-chan struct{}) error {
	interval := w.lease / 3
	if interval < time.Millisecond {
		interval = time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if _, err := w.repository.RenewLease(ctx, deliveryID, w.workerID, w.lease); err != nil {
				return err
			}
		}
	}
}
