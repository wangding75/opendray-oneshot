package queue

import (
	"context"
	"log/slog"
	"time"

	"github.com/opendray/opendray-v2/internal/oneshot/domain"
)

// AuditEvent is emitted after a queue state transition commits.
type AuditEvent struct {
	Type        string
	DeliveryID  string
	TaskID      string
	WorkerID    string
	Attempt     int
	Code        domain.ErrorCode
	AvailableAt *time.Time
	OccurredAt  time.Time
}

// AuditSink receives structured queue transition evidence.
type AuditSink interface {
	RecordQueueEvent(context.Context, AuditEvent)
}

type nopAuditSink struct{}

func (nopAuditSink) RecordQueueEvent(context.Context, AuditEvent) {}

// SlogAuditSink writes stable structured queue audit records.
type SlogAuditSink struct{ Log *slog.Logger }

func (s SlogAuditSink) RecordQueueEvent(ctx context.Context, event AuditEvent) {
	if s.Log == nil {
		return
	}
	attrs := []any{
		"event_type", event.Type,
		"delivery_id", event.DeliveryID,
		"task_id", event.TaskID,
		"worker_id", event.WorkerID,
		"attempt", event.Attempt,
		"error_code", event.Code,
		"occurred_at", event.OccurredAt,
	}
	if event.AvailableAt != nil {
		attrs = append(attrs, "available_at", *event.AvailableAt)
	}
	s.Log.InfoContext(ctx, "oneshot queue transition", attrs...)
}
