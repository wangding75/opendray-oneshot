package notification

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/opendray/opendray-v2/internal/channel"
	"github.com/opendray/opendray-v2/internal/oneshot/domain"
	"github.com/opendray/opendray-v2/internal/oneshot/store"
)

type notificationRepoFixture struct {
	retries, delivered int
	binding            *store.ChannelBinding
	item               store.NotificationOutboxRecord
}

func (*notificationRepoFixture) CreateNotification(context.Context, domain.Owner, store.NotificationOutboxRecord) (store.NotificationOutboxRecord, error) {
	return store.NotificationOutboxRecord{}, nil
}
func (r *notificationRepoFixture) ClaimNotifications(context.Context, string, int, time.Duration, time.Time) ([]store.NotificationOutboxRecord, error) {
	if r.item.ID != "" {
		return []store.NotificationOutboxRecord{r.item}, nil
	}
	return []store.NotificationOutboxRecord{{ID: "n1", IdempotencyKey: "terminal:r1", TaskID: "t1", AttemptCount: 1, Destination: map[string]any{"channel_id": "telegram-main", "conversation_id": "chat-1"}, Payload: map[string]any{"task_id": "t1", "run_id": "r1"}}}, nil
}
func (r *notificationRepoFixture) MarkNotificationDelivered(context.Context, string, string, time.Time) (store.NotificationOutboxRecord, error) {
	r.delivered++
	return store.NotificationOutboxRecord{}, nil
}
func (r *notificationRepoFixture) RetryNotification(context.Context, string, string, string, int, time.Time, time.Time) (store.NotificationOutboxRecord, error) {
	r.retries++
	return store.NotificationOutboxRecord{}, nil
}
func (r *notificationRepoFixture) UpsertChannelBinding(_ context.Context, value store.ChannelBinding) (store.ChannelBinding, error) {
	r.binding = &value
	return value, nil
}

type failingDeliveryFixture struct{ calls int }

func (d *failingDeliveryFixture) Deliver(context.Context, channel.ChannelMessage, *channel.Card) (channel.ChannelMessage, error) {
	d.calls++
	return channel.ChannelMessage{}, errors.New("transport unavailable")
}

func TestNotificationFailureSchedulesOutboxRetryOnly(t *testing.T) {
	repo := &notificationRepoFixture{}
	delivery := &failingDeliveryFixture{}
	worker, err := NewWorker(repo, delivery, WorkerOptions{WorkerID: "worker", Poll: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	worker.runOnce(context.Background())
	if delivery.calls != 1 || repo.retries != 1 || repo.delivered != 0 {
		t.Fatalf("unexpected notification retry behavior: delivery=%d retries=%d delivered=%d", delivery.calls, repo.retries, repo.delivered)
	}
}

type successfulDeliveryFixture struct{ metadata map[string]any }

func (d *successfulDeliveryFixture) Deliver(_ context.Context, msg channel.ChannelMessage, _ *channel.Card) (channel.ChannelMessage, error) {
	d.metadata = msg.Metadata
	return channel.ChannelMessage{SourceMessageID: "telegram-out-42", Metadata: map[string]any{channel.MetaOutboundMessageID: "telegram-out-42"}}, nil
}

func TestNotificationDeliveryPersistsExactReplyBinding(t *testing.T) {
	repo := &notificationRepoFixture{item: store.NotificationOutboxRecord{
		Owner: domain.Owner{Kind: domain.PrincipalAdmin, ID: "10001"}, ID: "n1",
		IdempotencyKey: "terminal:r1", TaskID: "t1", AttemptCount: 1,
		Destination: map[string]any{"channel_id": "telegram-main", "conversation_id": "chat-1", "thread_id": "thread-1"},
		Payload:     map[string]any{"task_id": "t1", "run_id": "r1", "run_status": "succeeded"},
	}}
	delivery := &successfulDeliveryFixture{}
	worker, err := NewWorker(repo, delivery, WorkerOptions{WorkerID: "worker", Poll: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	worker.runOnce(context.Background())
	if repo.delivered != 1 || repo.retries != 0 || repo.binding == nil {
		t.Fatalf("notification result was not atomically completed with reply binding: delivered=%d retries=%d binding=%+v", repo.delivered, repo.retries, repo.binding)
	}
	if repo.binding.Owner.ID != "10001" || repo.binding.TaskID != "t1" || repo.binding.SourceMessageID == nil || *repo.binding.SourceMessageID != "telegram-out-42" {
		t.Fatalf("unexpected exact notification binding: %+v", repo.binding)
	}
	if got := delivery.metadata["delivery_idempotency_key"]; got != "oneshot-notification:terminal:r1" {
		t.Fatalf("stable channel delivery idempotency key missing: %v", got)
	}
}

func TestRenderTaskOnlyCancellationOmitsBlankRunLine(t *testing.T) {
	text := render(store.NotificationOutboxRecord{Payload: map[string]any{
		"task_id": "t-cancel", "task_status": "cancelled",
	}})
	if text != "One-shot task t-cancel\nStatus: cancelled" {
		t.Fatalf("unexpected task-only notification: %q", text)
	}
}

func TestNotificationIDGenerationFailsClosedOnEntropyError(t *testing.T) {
	if _, err := newIDFrom("onf", errorReader{}); err == nil || !domain.HasCode(err, domain.ErrorInternal) {
		t.Fatalf("entropy failure was hidden: %v", err)
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("entropy unavailable") }
