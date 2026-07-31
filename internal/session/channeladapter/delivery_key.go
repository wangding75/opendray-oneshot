package channeladapter

import (
	"fmt"
	"time"

	"github.com/opendray/opendray-v2/internal/eventbus"
)

const deliveryIdempotencyMetadataKey = "delivery_idempotency_key"

func eventDeliveryKey(domain, channelID, targetID string, event eventbus.Event) string {
	stamp := event.Time.UTC()
	if stamp.IsZero() {
		stamp = time.Unix(0, 0).UTC()
	}
	return fmt.Sprintf("%s:%s:%s:%s:%d", domain, event.Topic, channelID, targetID, stamp.UnixNano())
}
