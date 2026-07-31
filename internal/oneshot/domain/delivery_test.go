package domain

import (
	"strings"
	"testing"
	"time"
)

func TestDeliveryLegalTransitions(t *testing.T) {
	task := mustTask(t).Snapshot()

	t.Run("pending reserve acknowledge", func(t *testing.T) {
		delivery := mustDelivery(t, task, DeliveryNew, testNow)
		if err := delivery.Reserve("worker-1", testNow.Add(time.Minute), testNow); err != nil {
			t.Fatal(err)
		}
		runID := NewRunID()
		if err := delivery.AttachRun(runID, testNow.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		if err := delivery.Acknowledge(testNow.Add(2 * time.Second)); err != nil {
			t.Fatal(err)
		}
		snapshot := delivery.Snapshot()
		if snapshot.Status != DeliveryAcknowledged || snapshot.LeaseOwner != nil || snapshot.LeaseUntil != nil {
			t.Fatalf("unexpected acknowledged Delivery: %+v", snapshot)
		}
	})

	t.Run("pending cancel", func(t *testing.T) {
		delivery := mustDelivery(t, task, DeliveryNew, testNow)
		if err := delivery.Cancel(false, testNow.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("reserved nack retry reserve", func(t *testing.T) {
		delivery := mustReservedDelivery(t, task, DeliveryNew, testNow)
		available := testNow.Add(2 * time.Minute)
		if err := delivery.Nack(ErrorQueueUnavailable, available, testNow.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		if got := delivery.Snapshot().Status; got != DeliveryRetryWait {
			t.Fatalf("status = %s", got)
		}
		if err := delivery.Reserve("worker-2", available.Add(time.Minute), available); err != nil {
			t.Fatal(err)
		}
		if delivery.Snapshot().Attempt != 2 {
			t.Fatalf("attempt = %d", delivery.Snapshot().Attempt)
		}
	})

	t.Run("retry wait cancel", func(t *testing.T) {
		delivery := mustReservedDelivery(t, task, DeliveryNew, testNow)
		if err := delivery.Nack(ErrorQueueUnavailable, testNow.Add(time.Minute), testNow.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		if err := delivery.Cancel(false, testNow.Add(2*time.Second)); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("reserved dead letter", func(t *testing.T) {
		delivery := mustReservedDelivery(t, task, DeliveryNew, testNow)
		if err := delivery.DeadLetter(ErrorUnsupportedProvider, testNow.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("reserved cancel", func(t *testing.T) {
		delivery := mustReservedDelivery(t, task, DeliveryNew, testNow)
		if err := delivery.Cancel(true, testNow.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
	})
}

func TestDeliveryReservedRequiresCleanupBeforeCancel(t *testing.T) {
	delivery := mustReservedDelivery(t, mustTask(t).Snapshot(), DeliveryNew, testNow)
	err := delivery.Cancel(false, testNow.Add(time.Second))
	requireCode(t, err, ErrorCancelFailed)
	if delivery.Snapshot().Status != DeliveryReserved {
		t.Fatal("Delivery mutated after failed cleanup guard")
	}
}

func TestDeliveryCannotCreateTwoRuns(t *testing.T) {
	delivery := mustReservedDelivery(t, mustTask(t).Snapshot(), DeliveryNew, testNow)
	first := NewRunID()
	if err := delivery.AttachRun(first, testNow); err != nil {
		t.Fatal(err)
	}
	if err := delivery.AttachRun(first, testNow); err != nil {
		t.Fatalf("idempotent same run attach failed: %v", err)
	}
	err := delivery.AttachRun(NewRunID(), testNow.Add(time.Second))
	requireCode(t, err, ErrorRunConflict)
	if got := delivery.Snapshot().RunID; got == nil || *got != first {
		t.Fatal("Delivery run_id changed")
	}
}

func TestDeliveryTerminalStatesAreIrreversible(t *testing.T) {
	for _, status := range []DeliveryStatus{DeliveryAcknowledged, DeliveryDeadLetter, DeliveryCancelled} {
		t.Run(status.String(), func(t *testing.T) {
			snapshot := mustDelivery(t, mustTask(t).Snapshot(), DeliveryNew, testNow).Snapshot()
			snapshot.Status = status
			if status == DeliveryAcknowledged {
				runID := NewRunID()
				snapshot.RunID = &runID
			}
			delivery, err := RestoreDelivery(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			err = delivery.Reserve("worker", testNow.Add(time.Minute), testNow)
			requireCode(t, err, ErrorInvalidTransition)
			if delivery.Snapshot().Status != status {
				t.Fatal("terminal Delivery mutated")
			}
		})
	}
}

func TestDeliveryInputIsImmutableSnapshot(t *testing.T) {
	task := mustTask(t).Snapshot()
	input := DeliveryInput{
		AttachmentRefs: []string{"oar_attachment"},
		Options:        map[string]any{"nested": map[string]any{"value": "original"}},
	}
	delivery, err := NewDelivery(DeliveryArgs{
		TaskID: task.ID, Operation: DeliveryNew, RequestedBy: testOwner(),
		Input: input, IdempotencyKey: "immutable", PayloadSHA256: strings.Repeat("b", 64),
		MaxAttempts: 2, AvailableAt: testNow,
	}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	input.AttachmentRefs[0] = "changed"
	input.Options["nested"].(map[string]any)["value"] = "changed"
	first := delivery.Snapshot()
	if first.Input.AttachmentRefs[0] != "oar_attachment" || first.Input.Options["nested"].(map[string]any)["value"] != "original" {
		t.Fatalf("constructor retained aliases: %+v", first.Input)
	}
	first.Input.AttachmentRefs[0] = "changed-again"
	first.Input.Options["nested"].(map[string]any)["value"] = "changed-again"
	second := delivery.Snapshot()
	if second.Input.AttachmentRefs[0] != "oar_attachment" || second.Input.Options["nested"].(map[string]any)["value"] != "original" {
		t.Fatalf("Snapshot exposed aliases: %+v", second.Input)
	}
}

func TestDeliveryRejectsExhaustedReservation(t *testing.T) {
	task := mustTask(t).Snapshot()
	delivery, err := NewDelivery(DeliveryArgs{
		TaskID: task.ID, Operation: DeliveryNew, RequestedBy: testOwner(),
		Input: DeliveryInput{}, IdempotencyKey: "one", PayloadSHA256: strings.Repeat("c", 64),
		MaxAttempts: 1, AvailableAt: testNow,
	}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := delivery.Reserve("worker", testNow.Add(time.Minute), testNow); err != nil {
		t.Fatal(err)
	}
	if err := delivery.Nack(ErrorQueueUnavailable, testNow.Add(2*time.Minute), testNow.Add(time.Second)); !HasCode(err, ErrorDeliveryExhausted) {
		t.Fatalf("expected delivery exhausted, got %v", err)
	}
}
