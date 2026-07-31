package api

import (
	"context"

	"github.com/opendray/opendray-v2/internal/eventbus"
)

// EventAuditor publishes only sanitized control-plane metadata. Prompt text,
// provider environment and credentials never enter this event.
type EventAuditor struct{ bus *eventbus.Hub }

func NewEventAuditor(bus *eventbus.Hub) *EventAuditor { return &EventAuditor{bus: bus} }

func (a *EventAuditor) Record(_ context.Context, record AuditRecord) {
	if a == nil || a.bus == nil {
		return
	}
	a.bus.Publish(eventbus.Event{Topic: "oneshot.audit", Data: record, Time: record.OccurredAt})
}
