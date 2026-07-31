// Package delivery provides the durable, execution-domain-neutral outbound
// delivery pipeline shared by interactive PTY Sessions and One-shot Agents.
package delivery

import (
	"context"
	"encoding/json"
	"time"

	"github.com/opendray/opendray-v2/internal/channel"
)

// ChannelDeliveryService is the shared delivery contract consumed by both
// execution domains. It deliberately accepts transport-neutral ChannelMessage
// values and exposes no Session, Task, Run, or process retry concepts.
type ChannelDeliveryService interface {
	Deliver(ctx context.Context, msg channel.ChannelMessage, card *channel.Card) (channel.ChannelMessage, error)
	Start(ctx context.Context)
	Shutdown(ctx context.Context) error
}

const (
	// MetaIdempotencyKey lets business adapters supply a stable notification
	// key. Re-delivering the same key reuses the existing outbox record instead
	// of repeating the transport side effect.
	MetaIdempotencyKey = "delivery_idempotency_key"
	MetaDeliveryID     = "delivery_id"
	MetaDeliveryState  = "delivery_state"
	MetaSegmentCount   = "delivery_segment_count"
	MetaMessageIDs     = "outbound_message_ids"
)

// OutboundMessage is the durable, transport-neutral unit stored in the
// channel delivery outbox. It intentionally contains no Session/Task/Run
// retry fields; transport delivery attempts are a separate concern.
type OutboundMessage struct {
	ID             string               `json:"id"`
	IdempotencyKey string               `json:"idempotency_key"`
	ChannelID      string               `json:"channel_id"`
	Address        channel.ReplyAddress `json:"address"`
	Text           string               `json:"text,omitempty"`
	Card           *CardSnapshot        `json:"card,omitempty"`
	Attachments    []channel.Attachment `json:"attachments,omitempty"`
	Metadata       map[string]any       `json:"metadata,omitempty"`
	Edit           *MessageEdit         `json:"edit,omitempty"`
	CreatedAt      time.Time            `json:"created_at"`
}

// MessageEdit requests an in-place update. If the transport cannot update or
// the update fails, the service falls back to a normal send without failing
// the business action that produced the notification.
type MessageEdit struct {
	PreviewHandle string `json:"preview_handle"`
}

// DeliveryReceipt is the durable result of a logical outbound delivery.
type DeliveryReceipt struct {
	DeliveryID     string    `json:"delivery_id"`
	ChannelID      string    `json:"channel_id"`
	ConversationID string    `json:"conversation_id,omitempty"`
	ThreadID       string    `json:"thread_id,omitempty"`
	MessageIDs     []string  `json:"message_ids,omitempty"`
	AttemptCount   int       `json:"attempt_count"`
	PartCount      int       `json:"part_count"`
	CompletedParts int       `json:"completed_parts"`
	Edited         bool      `json:"edited,omitempty"`
	FallbackUsed   bool      `json:"fallback_used,omitempty"`
	DeliveredAt    time.Time `json:"delivered_at,omitempty"`
}

// ChannelDeliveryAttempt records one transport attempt. It deliberately uses
// delivery-specific naming so it cannot be confused with a One-shot task/run
// retry.
type ChannelDeliveryAttempt struct {
	DeliveryID string    `json:"delivery_id"`
	Attempt    int       `json:"attempt"`
	Operation  string    `json:"operation"`
	Status     string    `json:"status"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	Error      string    `json:"error,omitempty"`
	RetryAt    time.Time `json:"retry_at,omitempty"`
}

// OutboxStatus is the durable delivery state.
type OutboxStatus string

const (
	StatusPending   OutboxStatus = "pending"
	StatusSending   OutboxStatus = "sending"
	StatusRetry     OutboxStatus = "retry"
	StatusDelivered OutboxStatus = "delivered"
	StatusDead      OutboxStatus = "dead"
)

// OutboxRecord is the persistence representation used by OutboxStore.
type OutboxRecord struct {
	ID             string
	IdempotencyKey string
	ChannelID      string
	Payload        json.RawMessage
	Status         OutboxStatus
	Progress       int
	AttemptCount   int
	NextAttemptAt  time.Time
	LeaseOwner     string
	LeaseUntil     time.Time
	LastError      string
	Receipt        DeliveryReceipt
	CreatedAt      time.Time
	UpdatedAt      time.Time
	DeliveredAt    time.Time
}
