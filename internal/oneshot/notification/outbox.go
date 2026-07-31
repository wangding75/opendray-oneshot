// Package notification owns durable One-shot result notifications. It remains
// independent from interactive Session notification semantics.
package notification

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/opendray/opendray-v2/internal/channel"
	"github.com/opendray/opendray-v2/internal/oneshot/domain"
	"github.com/opendray/opendray-v2/internal/oneshot/store"
)

type Repository interface {
	CreateNotification(context.Context, domain.Owner, store.NotificationOutboxRecord) (store.NotificationOutboxRecord, error)
	ClaimNotifications(context.Context, string, int, time.Duration, time.Time) ([]store.NotificationOutboxRecord, error)
	MarkNotificationDelivered(context.Context, string, string, time.Time) (store.NotificationOutboxRecord, error)
	RetryNotification(context.Context, string, string, string, int, time.Time, time.Time) (store.NotificationOutboxRecord, error)
	UpsertChannelBinding(context.Context, store.ChannelBinding) (store.ChannelBinding, error)
}

// OutboxSink implements executor.RunNotificationSink.
type OutboxSink struct {
	repository Repository
	now        func() time.Time
}

func NewOutboxSink(repository Repository) *OutboxSink {
	return &OutboxSink{repository: repository, now: func() time.Time { return time.Now().UTC() }}
}

func (s *OutboxSink) EnqueueRunTerminal(ctx context.Context, owner domain.Owner, task domain.TaskSnapshot, run domain.RunSnapshot) error {
	if s == nil || s.repository == nil || task.Source.ReplyAddress == nil {
		return nil
	}
	address := task.Source.ReplyAddress
	destination := map[string]any{
		"channel_id": address.ChannelID, "conversation_id": address.ConversationID,
		"thread_id": address.ThreadID, "message_id": address.MessageID,
	}
	payload := map[string]any{
		"task_id": task.ID, "run_id": run.ID, "project_id": task.ProjectID,
		"provider_id": task.ProviderID, "task_status": task.Status, "run_status": run.Status,
		"continue_available": task.RuntimeContextID != nil,
		"artifact_path":      fmt.Sprintf("/api/v1/oneshot/runs/%s/artifacts", run.ID),
	}
	if run.ErrorCode != nil {
		payload["error_code"] = *run.ErrorCode
	}
	if run.ErrorMessage != nil {
		payload["error_message"] = *run.ErrorMessage
	}
	id, err := newID("onf")
	if err != nil {
		return err
	}
	_, err = s.repository.CreateNotification(ctx, owner, store.NotificationOutboxRecord{
		ID: id, IdempotencyKey: "terminal:" + run.ID, TaskID: task.ID, RunID: &run.ID,
		EventType: "oneshot.run." + string(run.Status), Destination: destination,
		Payload: payload, Status: store.NotificationPending, NextAttemptAt: s.now(),
	})
	if domain.HasCode(err, domain.ErrorIdempotencyConflict) {
		return nil
	}
	return err
}

// EnqueueWaitingInput emits a durable prompt-needed notification.
func (s *OutboxSink) EnqueueWaitingInput(ctx context.Context, owner domain.Owner, task domain.TaskSnapshot, run domain.RunSnapshot) error {
	if task.Status != domain.TaskWaitingInput || task.Source.ReplyAddress == nil {
		return nil
	}
	address := task.Source.ReplyAddress
	id, err := newID("onf")
	if err != nil {
		return err
	}
	_, err = s.repository.CreateNotification(ctx, owner, store.NotificationOutboxRecord{
		ID: id, IdempotencyKey: "waiting_input:" + run.ID, TaskID: task.ID, RunID: &run.ID,
		EventType:   "oneshot.task.waiting_input",
		Destination: map[string]any{"channel_id": address.ChannelID, "conversation_id": address.ConversationID, "thread_id": address.ThreadID, "message_id": address.MessageID},
		Payload:     map[string]any{"task_id": task.ID, "run_id": run.ID, "project_id": task.ProjectID, "continue_available": true},
		Status:      store.NotificationPending, NextAttemptAt: s.now(),
	})
	if domain.HasCode(err, domain.ErrorIdempotencyConflict) {
		return nil
	}
	return err
}

func newID(prefix string) (string, error) {
	return newIDFrom(prefix, rand.Reader)
}

func newIDFrom(prefix string, reader io.Reader) (string, error) {
	var raw [10]byte
	if _, err := io.ReadFull(reader, raw[:]); err != nil {
		return "", domain.NewDomainError(domain.ErrorInternal, "generate notification id", err)
	}
	return prefix + "_" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw[:])), nil
}

type Worker struct {
	repository Repository
	delivery   channel.OutboundDeliveryService
	workerID   string
	poll       time.Duration
	lease      time.Duration
	maxAttempt int
	bindingTTL time.Duration
	log        *slog.Logger
	now        func() time.Time
	cancel     context.CancelFunc
	wg         sync.WaitGroup
}

type WorkerOptions struct {
	WorkerID    string
	Poll        time.Duration
	Lease       time.Duration
	MaxAttempts int
	BindingTTL  time.Duration
	Log         *slog.Logger
}

func NewWorker(repository Repository, delivery channel.OutboundDeliveryService, options WorkerOptions) (*Worker, error) {
	if repository == nil || delivery == nil {
		return nil, domain.InvalidRequestf("notification repository and delivery service are required")
	}
	if strings.TrimSpace(options.WorkerID) == "" {
		options.WorkerID = "oneshot-notifier"
	}
	if options.Poll <= 0 {
		options.Poll = time.Second
	}
	if options.Lease <= 0 {
		options.Lease = 30 * time.Second
	}
	if options.MaxAttempts <= 0 {
		options.MaxAttempts = 8
	}
	if options.BindingTTL <= 0 {
		options.BindingTTL = 30 * 24 * time.Hour
	}
	if options.Log == nil {
		options.Log = slog.Default()
	}
	return &Worker{repository: repository, delivery: delivery, workerID: options.WorkerID, poll: options.Poll, lease: options.Lease, maxAttempt: options.MaxAttempts, bindingTTL: options.BindingTTL, log: options.Log.With("component", "oneshot-notifier"), now: func() time.Time { return time.Now().UTC() }}, nil
}

func (w *Worker) Start(parent context.Context) {
	if w == nil || w.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	w.cancel = cancel
	w.wg.Add(1)
	go func() { defer w.wg.Done(); w.loop(ctx) }()
}

func (w *Worker) Shutdown(ctx context.Context) error {
	if w == nil {
		return nil
	}
	if w.cancel != nil {
		w.cancel()
	}
	done := make(chan struct{})
	go func() { w.wg.Wait(); close(done) }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

func (w *Worker) loop(ctx context.Context) {
	ticker := time.NewTicker(w.poll)
	defer ticker.Stop()
	for {
		w.runOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *Worker) runOnce(ctx context.Context) {
	now := w.now()
	items, err := w.repository.ClaimNotifications(ctx, w.workerID, 20, w.lease, now)
	if err != nil {
		if ctx.Err() == nil {
			w.log.Warn("claim notification outbox", "err", err)
		}
		return
	}
	for _, item := range items {
		if err := w.deliver(ctx, item); err != nil {
			delay := retryDelay(item.AttemptCount)
			_, markErr := w.repository.RetryNotification(context.WithoutCancel(ctx), item.ID, w.workerID, err.Error(), w.maxAttempt, w.now().Add(delay), w.now())
			if markErr != nil {
				w.log.Error("persist notification retry", "id", item.ID, "err", markErr)
			}
			continue
		}
		if _, err := w.repository.MarkNotificationDelivered(context.WithoutCancel(ctx), item.ID, w.workerID, w.now()); err != nil {
			w.log.Error("persist notification receipt", "id", item.ID, "err", err)
		}
	}
}

func (w *Worker) deliver(ctx context.Context, item store.NotificationOutboxRecord) error {
	channelID, _ := item.Destination["channel_id"].(string)
	conversationID, _ := item.Destination["conversation_id"].(string)
	threadID, _ := item.Destination["thread_id"].(string)
	if strings.TrimSpace(channelID) == "" || strings.TrimSpace(conversationID) == "" {
		return domain.InvalidRequestf("notification destination is incomplete")
	}
	text := render(item)
	delivered, err := w.delivery.Deliver(ctx, channel.ChannelMessage{
		ChannelID: channelID, ConversationID: conversationID, ThreadID: threadID,
		Direction: channel.DirectionOutbound, Text: text, Timestamp: w.now(),
		Metadata: map[string]any{"delivery_idempotency_key": "oneshot-notification:" + item.IdempotencyKey},
	}, resultCard(item, text))
	if err != nil {
		return err
	}
	messageID := strings.TrimSpace(delivered.SourceMessageID)
	if delivered.Metadata != nil {
		if value, ok := delivered.Metadata[channel.MetaOutboundMessageID].(string); ok && strings.TrimSpace(value) != "" {
			messageID = strings.TrimSpace(value)
		}
	}
	if messageID == "" {
		return domain.InvalidRequestf("notification transport did not return an outbound message id")
	}
	expires := w.now().Add(w.bindingTTL)
	_, err = w.repository.UpsertChannelBinding(ctx, store.ChannelBinding{
		Owner: item.Owner, ChannelID: channelID, ConversationID: conversationID, ThreadID: threadID,
		SourceMessageID: &messageID, TaskID: item.TaskID, Kind: "notification", ExpiresAt: &expires,
	})
	return err
}

func render(item store.NotificationOutboxRecord) string {
	taskID, _ := item.Payload["task_id"].(string)
	runID, _ := item.Payload["run_id"].(string)
	status := fmt.Sprint(item.Payload["run_status"])
	if status == "<nil>" || status == "" {
		status = fmt.Sprint(item.Payload["task_status"])
	}
	text := fmt.Sprintf("One-shot task %s", taskID)
	if strings.TrimSpace(runID) != "" {
		text += "\nRun: " + runID
	}
	text += "\nStatus: " + status
	if value, ok := item.Payload["error_message"].(string); ok && value != "" {
		text += "\nError: " + value
	}
	if value, ok := item.Payload["artifact_path"].(string); ok && value != "" {
		text += "\nArtifacts: " + value
	}
	return text
}

func resultCard(item store.NotificationOutboxRecord, text string) *channel.Card {
	taskID, _ := item.Payload["task_id"].(string)
	buttons := []channel.ButtonOption{{Text: "Task", Value: "cmd:/task " + taskID}}
	if enabled, _ := item.Payload["continue_available"].(bool); enabled {
		buttons = append(buttons, channel.ButtonOption{Text: "Continue", Value: "cmd:/continue " + taskID, Style: "primary"})
	}
	return &channel.Card{
		Header: &channel.CardHeader{Title: "One-shot result"},
		Elements: []channel.CardElement{
			channel.CardMarkdown{Content: text},
			channel.CardActions{Buttons: [][]channel.ButtonOption{buttons}},
		},
	}
}

func retryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 8 {
		attempt = 8
	}
	return time.Duration(1<<uint(attempt-1)) * time.Second
}
