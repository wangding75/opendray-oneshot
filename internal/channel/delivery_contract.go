package channel

import "context"

// OutboundDeliveryService is the transport-neutral delivery seam shared by
// interactive Session notifications and the independent One-shot domain.
// Implementations own durable outbox, segmentation, transport retries,
// editing fallback, attachment delivery, rate limiting, and receipts. They
// must not contain Session or One-shot business decisions.
type OutboundDeliveryService interface {
	Deliver(ctx context.Context, msg ChannelMessage, card *Card) (ChannelMessage, error)
}

// OutboundDeliveryLifecycle is implemented by durable delivery services that
// recover pending outbox records in the background. Hub starts and stops this
// lifecycle alongside the concrete channel transports.
type OutboundDeliveryLifecycle interface {
	Start(ctx context.Context)
	Shutdown(ctx context.Context) error
}
