package channeladapter

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/opendray/opendray-v2/internal/channel"
	"github.com/opendray/opendray-v2/internal/eventbus"
)

// SessionNotifier owns all Session lifecycle subscriptions and chat semantics.
type SessionNotifier struct {
	host     ChannelHost
	bindings InteractiveBindingStore
	tracker  *ReplyTracker
	bus      *eventbus.Hub
	log      *slog.Logger

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func NewSessionNotifier(host ChannelHost, bindings InteractiveBindingStore, tracker *ReplyTracker, bus *eventbus.Hub, log *slog.Logger) *SessionNotifier {
	if log == nil {
		log = slog.Default()
	}
	return &SessionNotifier{host: host, bindings: bindings, tracker: tracker, bus: bus, log: log.With("component", "session-channel-notifier")}
}

func (n *SessionNotifier) Start(parent context.Context) {
	if n == nil || n.bus == nil || n.host == nil {
		return
	}
	n.mu.Lock()
	if n.cancel != nil {
		n.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	n.cancel = cancel
	n.done = make(chan struct{})
	done := n.done
	n.mu.Unlock()
	go func() {
		defer close(done)
		n.run(ctx)
	}()
}

func (n *SessionNotifier) Shutdown(ctx context.Context) error {
	if n == nil {
		return nil
	}
	n.mu.Lock()
	cancel, done := n.cancel, n.done
	n.cancel = nil
	n.done = nil
	n.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (n *SessionNotifier) run(ctx context.Context) {
	idle, unsubIdle := n.bus.Subscribe("session.idle", 64)
	defer unsubIdle()
	ended, unsubEnded := n.bus.Subscribe("session.ended", 64)
	defer unsubEnded()
	turn, unsubTurn := n.bus.Subscribe("session.turn_completed", 64)
	defer unsubTurn()
	stopped, unsubStopped := n.bus.Subscribe("session.stopped", 64)
	defer unsubStopped()
	interrupted, unsubInterrupted := n.bus.Subscribe("session.interrupted", 64)
	defer unsubInterrupted()
	input, unsubInput := n.bus.Subscribe("session.input", 64)
	defer unsubInput()

	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-idle:
			if !ok {
				return
			}
			n.dispatch(ctx, event)
		case event, ok := <-ended:
			if !ok {
				return
			}
			if n.tracker != nil {
				n.tracker.Cancel(sessionIDFromEvent(event))
			}
			n.dispatch(ctx, event)
		case event, ok := <-turn:
			if !ok {
				return
			}
			if n.tracker != nil {
				n.tracker.DeliverTurn(ctx, event)
			}
		case event, ok := <-stopped:
			if !ok {
				return
			}
			if n.tracker != nil {
				n.tracker.Cancel(sessionIDFromEvent(event))
			}
		case event, ok := <-interrupted:
			if !ok {
				return
			}
			if n.tracker != nil {
				n.tracker.Cancel(sessionIDFromEvent(event))
			}
		case event, ok := <-input:
			if !ok {
				return
			}
			n.host.ResetNotificationTargetAll(sessionIDFromEvent(event))
		}
	}
}

func (n *SessionNotifier) dispatch(ctx context.Context, event eventbus.Event) {
	if originFromEvent(event) == "integration" {
		return
	}
	sessionID := sessionIDFromEvent(event)
	if sessionID == "" {
		return
	}
	if event.Topic == "session.idle" && n.tracker != nil {
		if data, ok := event.Data.(map[string]any); ok {
			output, _ := data["recent_output"].(string)
			if n.tracker.AlreadyDelivered(sessionID, strings.TrimSpace(output)) {
				return
			}
		}
	}
	for _, ch := range n.host.ChannelsSnapshot() {
		if ch == nil || n.host.ChannelMuted(ctx, ch.ID()) || n.host.SuppressNotification(ctx, ch.ID(), event.Topic, sessionID) {
			continue
		}
		policy := n.host.AdapterChatPolicyFor(ctx, ch.ID())
		card := buildSessionNotificationCard(event, policy)
		if card == nil {
			continue
		}
		out, err := n.host.Deliver(ctx, channel.ChannelMessage{
			ChannelID: ch.ID(), Direction: channel.DirectionOutbound,
			ConversationID: "default", Text: card.RenderText(), Timestamp: time.Now().UTC(),
			Metadata: map[string]any{
				deliveryIdempotencyMetadataKey: eventDeliveryKey("session-notification", ch.ID(), sessionID, event),
			},
		}, card)
		if err != nil {
			n.log.Error("session notification send failed", "channel", ch.ID(), "session", sessionID, "err", err)
			continue
		}
		conversationID := outboundConversation(out)
		n.bindings.RecordLast(ch.ID(), conversationID, sessionID)
		recordOutboundFromMessage(n.bindings, out, sessionID)
		if n.bus != nil {
			n.bus.Publish(eventbus.Event{Topic: "channel.message_sent", Data: map[string]any{"channel_id": ch.ID(), "topic": event.Topic}})
		}
	}
}

func sessionIDFromEvent(event eventbus.Event) string {
	data, _ := event.Data.(map[string]any)
	if data == nil {
		return ""
	}
	value, _ := data["session_id"].(string)
	return value
}

func originFromEvent(event eventbus.Event) string {
	data, _ := event.Data.(map[string]any)
	if data == nil {
		return ""
	}
	value, _ := data["origin"].(string)
	return value
}
