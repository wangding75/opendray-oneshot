package queue

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/opendray/opendray-v2/internal/oneshot/domain"
)

type terminalProcessor struct{ repository *MemoryQueue }

func (p terminalProcessor) Process(_ context.Context, claim Claim) Outcome {
	_ = p.repository.AttachTerminalRun(claim.Delivery.ID, domain.NewRunID(), domain.RunCompleted)
	return Outcome{Action: ActionAck}
}

type panicProcessor struct{}

func (panicProcessor) Process(context.Context, Claim) Outcome { panic("fixture panic") }

func TestWorkerProcessesAndAcknowledgesClaim(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	repository := NewMemoryQueue(nil)
	repository.SetClock(func() time.Time { return now })
	request, _ := queuedFixture(t, now, 3)
	if _, err := repository.Enqueue(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	worker, err := NewWorker(repository, terminalProcessor{repository: repository}, "worker",
		WithWorkerClaimLimit(1), WithWorkerTiming(time.Millisecond, time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.DrainOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	delivery, _ := repository.GetDelivery(request.Delivery.ID)
	if delivery.Status != domain.DeliveryAcknowledged {
		t.Fatalf("delivery status=%s", delivery.Status)
	}
}

func TestWorkerPanicBecomesRetryableInternalFailure(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	repository := NewMemoryQueue(nil)
	repository.SetClock(func() time.Time { return now })
	request, _ := queuedFixture(t, now, 3)
	if _, err := repository.Enqueue(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	worker, err := NewWorker(repository, panicProcessor{}, "worker",
		WithWorkerClaimLimit(1), WithWorkerRetryPolicy(RetryPolicy{BaseDelay: time.Second, MaxDelay: time.Second}))
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.DrainOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	delivery, _ := repository.GetDelivery(request.Delivery.ID)
	if delivery.Status != domain.DeliveryRetryWait || delivery.LastErrorCode == nil || *delivery.LastErrorCode != string(domain.ErrorInternal) {
		t.Fatalf("panic result=%+v", delivery)
	}
}

type renewalTrackingQueue struct {
	*MemoryQueue
	renewed chan domain.DeliverySnapshot
}

func (q *renewalTrackingQueue) RenewLease(ctx context.Context, deliveryID, workerID string, lease time.Duration) (domain.DeliverySnapshot, error) {
	out, err := q.MemoryQueue.RenewLease(ctx, deliveryID, workerID, lease)
	if err == nil {
		select {
		case q.renewed <- out:
		default:
		}
	}
	return out, err
}

type renewalAwareProcessor struct {
	repository *MemoryQueue
	renewed    <-chan domain.DeliverySnapshot
}

func (p renewalAwareProcessor) Process(ctx context.Context, claim Claim) Outcome {
	select {
	case <-ctx.Done():
		return Outcome{Action: ActionRetry, Code: domain.ErrorInternal}
	case <-time.After(2 * time.Second):
		return Outcome{Action: ActionRetry, Code: domain.ErrorTimeout}
	case renewed := <-p.renewed:
		if renewed.LeaseUntil == nil || claim.Delivery.LeaseUntil == nil || !renewed.LeaseUntil.After(*claim.Delivery.LeaseUntil) {
			return Outcome{Action: ActionDeadLetter, Code: domain.ErrorInternal}
		}
		if err := p.repository.AttachTerminalRun(claim.Delivery.ID, domain.NewRunID(), domain.RunCompleted); err != nil {
			return Outcome{Action: ActionDeadLetter, Code: domain.ErrorInternal}
		}
		return Outcome{Action: ActionAck}
	}
}

func TestWorkerRenewsLeaseWhileProcessorIsRunning(t *testing.T) {
	now := time.Now().UTC()
	memory := NewMemoryQueue(nil)
	repository := &renewalTrackingQueue{
		MemoryQueue: memory,
		renewed:     make(chan domain.DeliverySnapshot, 1),
	}
	request, _ := queuedFixture(t, now, 3)
	if _, err := repository.Enqueue(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	processor := renewalAwareProcessor{repository: memory, renewed: repository.renewed}
	worker, err := NewWorker(repository, processor, "heartbeat-worker",
		WithWorkerClaimLimit(1), WithWorkerTiming(time.Millisecond, 120*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.DrainOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	delivery, _ := repository.GetDelivery(request.Delivery.ID)
	if delivery.Status != domain.DeliveryAcknowledged {
		t.Fatalf("delivery status=%s; want acknowledged", delivery.Status)
	}
}

type leaseLossQueue struct{ *MemoryQueue }

func (q *leaseLossQueue) RenewLease(_ context.Context, deliveryID, workerID string, _ time.Duration) (domain.DeliverySnapshot, error) {
	return domain.DeliverySnapshot{}, &LeaseLostError{DeliveryID: deliveryID, WorkerID: workerID}
}

type cancellationObservingProcessor struct{ cancelled chan struct{} }

func (p cancellationObservingProcessor) Process(ctx context.Context, _ Claim) Outcome {
	<-ctx.Done()
	close(p.cancelled)
	return Outcome{Action: ActionRetry, Code: domain.ErrorInternal}
}

func TestWorkerCancelsProcessorWhenLeaseRenewalFails(t *testing.T) {
	now := time.Now().UTC()
	memory := NewMemoryQueue(nil)
	repository := &leaseLossQueue{MemoryQueue: memory}
	request, _ := queuedFixture(t, now, 3)
	if _, err := repository.Enqueue(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	cancelled := make(chan struct{})
	worker, err := NewWorker(repository, cancellationObservingProcessor{cancelled: cancelled}, "lease-loss-worker",
		WithWorkerClaimLimit(1), WithWorkerTiming(time.Millisecond, 120*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	err = worker.DrainOnce(context.Background())
	var leaseErr *LeaseLostError
	if !errors.As(err, &leaseErr) {
		t.Fatalf("DrainOnce err=%v; want LeaseLostError", err)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("processor context was not cancelled after lease loss")
	}
	delivery, _ := repository.GetDelivery(request.Delivery.ID)
	if delivery.Status != domain.DeliveryReserved {
		t.Fatalf("delivery status=%s; lease loss must not apply a second transition", delivery.Status)
	}
}

type failingAckObserverProcessor struct{ repository *MemoryQueue }

func (p failingAckObserverProcessor) Process(_ context.Context, claim Claim) Outcome {
	_ = p.repository.AttachTerminalRun(claim.Delivery.ID, domain.NewRunID(), domain.RunCompleted)
	return Outcome{Action: ActionAck}
}

func (failingAckObserverProcessor) Acked(context.Context, Claim) error {
	return errors.New("injected Saga acknowledgement checkpoint failure")
}

func TestWorkerReturnsAckObserverFailureAfterDurableQueueACK(t *testing.T) {
	now := time.Date(2026, 7, 28, 8, 0, 0, 0, time.UTC)
	repository := NewMemoryQueue(nil)
	repository.SetClock(func() time.Time { return now })
	request, _ := queuedFixture(t, now, 3)
	if _, err := repository.Enqueue(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	worker, err := NewWorker(repository, failingAckObserverProcessor{repository: repository}, "observer-worker",
		WithWorkerClaimLimit(1), WithWorkerTiming(time.Millisecond, time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.DrainOnce(context.Background()); err == nil {
		t.Fatal("AckObserver failure was not returned")
	}
	delivery, _ := repository.GetDelivery(request.Delivery.ID)
	if delivery.Status != domain.DeliveryAcknowledged {
		t.Fatalf("queue ACK was not durable: %s", delivery.Status)
	}
}
