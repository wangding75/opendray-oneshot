package queue

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opendray/opendray-v2/internal/oneshot/domain"
)

func queuedFixture(t *testing.T, now time.Time, maxAttempts int) (EnqueueRequest, domain.Owner) {
	t.Helper()
	owner := domain.Owner{Kind: domain.PrincipalAdmin, ID: "queue-test-owner"}
	task, err := domain.NewTask(domain.TaskArgs{
		Owner: owner, ProjectID: "project-queue", ProviderID: "provider-queue",
		Model:  "mock-model",
		Source: domain.Source{Kind: domain.SourceAPI, ClientRequestID: "request-1"},
		Prompt: "run reliable queue fixture",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := domain.NewDelivery(domain.DeliveryArgs{
		TaskID: task.Snapshot().ID, Operation: domain.DeliveryNew, RequestedBy: owner,
		Input:          domain.DeliveryInput{AttachmentRefs: []string{}, Options: map[string]any{}},
		IdempotencyKey: "queue-key", PayloadSHA256: strings.Repeat("a", 64),
		MaxAttempts: maxAttempts, AvailableAt: now,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := task.QueueInitialDelivery(delivery.Snapshot(), now); err != nil {
		t.Fatal(err)
	}
	return EnqueueRequest{
		Task: task.Snapshot(), Delivery: delivery.Snapshot(), Method: "POST",
		CanonicalPath: "/api/v1/oneshot/tasks", IdempotencyKey: "queue-key",
		PayloadSHA256: strings.Repeat("a", 64),
	}, owner
}

func TestMemoryQueueConcurrentClaimHasSingleWinner(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	repository := NewMemoryQueue(nil)
	repository.SetClock(func() time.Time { return now })
	request, _ := queuedFixture(t, now, 3)
	if _, err := repository.Enqueue(context.Background(), request); err != nil {
		t.Fatal(err)
	}

	var winners atomic.Int32
	var wait sync.WaitGroup
	for index := 0; index < 32; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			claims, err := repository.ClaimDue(context.Background(), "worker-"+string(rune('a'+index)), 1, time.Minute)
			if err != nil {
				t.Errorf("claim: %v", err)
				return
			}
			winners.Add(int32(len(claims)))
		}(index)
	}
	wait.Wait()
	if got := winners.Load(); got != 1 {
		t.Fatalf("claim winners=%d; want 1", got)
	}
}

func TestMemoryQueueLeaseExpiryRecovery(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	repository := NewMemoryQueue(nil)
	repository.SetClock(func() time.Time { return now })
	request, _ := queuedFixture(t, now, 3)
	if _, err := repository.Enqueue(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	first, err := repository.ClaimDue(context.Background(), "worker-a", 1, time.Minute)
	if err != nil || len(first) != 1 {
		t.Fatalf("first claim=%v err=%v", first, err)
	}
	now = now.Add(2 * time.Minute)
	second, err := repository.ClaimDue(context.Background(), "worker-b", 1, time.Minute)
	if err != nil || len(second) != 1 {
		t.Fatalf("recovery claim=%v err=%v", second, err)
	}
	if second[0].Delivery.Attempt != 2 || second[0].Delivery.LeaseOwner == nil || *second[0].Delivery.LeaseOwner != "worker-b" {
		t.Fatalf("recovered delivery=%+v", second[0].Delivery)
	}
}

func TestMemoryQueueNackBackoffAndExhaustion(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	repository := NewMemoryQueue(nil)
	repository.SetClock(func() time.Time { return now })
	request, _ := queuedFixture(t, now, 2)
	if _, err := repository.Enqueue(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	claim, _ := repository.ClaimDue(context.Background(), "worker", 1, time.Minute)
	retry, err := repository.Nack(context.Background(), claim[0].Delivery.ID, "worker", domain.ErrorRateLimited, RetryPolicy{BaseDelay: time.Second, MaxDelay: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if retry.Status != domain.DeliveryRetryWait || !retry.AvailableAt.Equal(now.Add(time.Second)) {
		t.Fatalf("retry=%+v", retry)
	}
	now = now.Add(time.Second)
	claim, _ = repository.ClaimDue(context.Background(), "worker", 1, time.Minute)
	dead, err := repository.Nack(context.Background(), claim[0].Delivery.ID, "worker", domain.ErrorRateLimited, RetryPolicy{BaseDelay: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if dead.Status != domain.DeliveryDeadLetter || dead.LastErrorCode == nil || *dead.LastErrorCode != string(domain.ErrorDeliveryExhausted) {
		t.Fatalf("dead=%+v", dead)
	}
	task, _ := repository.GetTask(request.Task.ID)
	if task.Status != domain.TaskFailed {
		t.Fatalf("task status=%s; want failed", task.Status)
	}
}

func TestMemoryQueueAckPreventsRestartDuplicate(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	repository := NewMemoryQueue(nil)
	repository.SetClock(func() time.Time { return now })
	request, _ := queuedFixture(t, now, 3)
	if _, err := repository.Enqueue(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	claims, _ := repository.ClaimDue(context.Background(), "worker-a", 1, time.Minute)
	runID := domain.NewRunID()
	if err := repository.AttachTerminalRun(claims[0].Delivery.ID, runID, domain.RunCompleted); err != nil {
		t.Fatal(err)
	}
	acked, err := repository.Ack(context.Background(), claims[0].Delivery.ID, "worker-a")
	if err != nil {
		t.Fatal(err)
	}
	if acked.Status != domain.DeliveryAcknowledged {
		t.Fatalf("status=%s", acked.Status)
	}

	// A new worker instance after service restart cannot claim the completed row.
	now = now.Add(10 * time.Minute)
	claims, err = repository.ClaimDue(context.Background(), "worker-after-restart", 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 0 {
		t.Fatalf("restart claimed acknowledged delivery: %+v", claims)
	}
}

func TestMemoryQueueExpiredLeaseWithTerminalRunWaitsForSagaReconciler(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	repository := NewMemoryQueue(nil)
	repository.SetClock(func() time.Time { return now })
	request, _ := queuedFixture(t, now, 3)
	if _, err := repository.Enqueue(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	claims, _ := repository.ClaimDue(context.Background(), "crashed-worker", 1, time.Minute)
	runID := domain.NewRunID()
	if err := repository.AttachTerminalRun(claims[0].Delivery.ID, runID, domain.RunCompleted); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	claims, err := repository.ClaimDue(context.Background(), "recovery-worker", 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 0 {
		t.Fatalf("terminal Run was claimed again: %+v", claims)
	}
	delivery, _ := repository.GetDelivery(request.Delivery.ID)
	if delivery.Status != domain.DeliveryReserved {
		t.Fatalf("queue silently acknowledged Saga-owned delivery: %s", delivery.Status)
	}
	delivery, err = repository.AcknowledgeRecovered(context.Background(), request.Delivery.ID, runID)
	if err != nil {
		t.Fatal(err)
	}
	if delivery.Status != domain.DeliveryAcknowledged {
		t.Fatalf("recovered status=%s", delivery.Status)
	}
}

func TestMemoryQueueCancelIsIdempotent(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	repository := NewMemoryQueue(nil)
	repository.SetClock(func() time.Time { return now })
	request, owner := queuedFixture(t, now, 3)
	if _, err := repository.Enqueue(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	first, err := repository.Cancel(context.Background(), request.Delivery.ID, owner, "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := repository.Cancel(context.Background(), request.Delivery.ID, owner, "")
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != domain.DeliveryCancelled || second.Status != domain.DeliveryCancelled {
		t.Fatalf("cancel results=%s/%s", first.Status, second.Status)
	}
}
