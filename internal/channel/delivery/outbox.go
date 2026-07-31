package delivery

import (
	"context"
	"time"

	"github.com/opendray/opendray-v2/internal/channel"
)

// OutboxStore persists logical outbound messages independently from the
// execution domain that requested them. Implementations must support lease
// expiry so another process can recover records after a gateway crash.
type OutboxStore interface {
	Create(ctx context.Context, record OutboxRecord) (stored OutboxRecord, created bool, err error)
	Get(ctx context.Context, id string) (OutboxRecord, error)
	Claim(ctx context.Context, id, owner string, lease time.Duration) (OutboxRecord, bool, error)
	ClaimDue(ctx context.Context, owner string, limit int, lease time.Duration) ([]OutboxRecord, error)
	AppendAttempt(ctx context.Context, attempt ChannelDeliveryAttempt) error
	MarkProgress(ctx context.Context, id, owner string, progress int, receipt DeliveryReceipt) error
	MarkDelivered(ctx context.Context, id, owner string, receipt DeliveryReceipt) error
	MarkRetry(ctx context.Context, id, owner, lastError string, nextAttempt time.Time) error
	MarkDead(ctx context.Context, id, owner, lastError string) error
}

// MessageRecorder persists the final logical outbound message in the existing
// channel_messages history after transport delivery succeeds.
type MessageRecorder interface {
	PersistOutbound(ctx context.Context, msg channel.ChannelMessage) (int64, error)
}
