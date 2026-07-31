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

const defaultTypingCap = 90 * time.Second

type pendingReply struct {
	channelID    string
	conversation string
	sessionID    string
	source       channel.ChannelMessage
	stopTyping   func()
	timer        *time.Timer
}

func pendingKey(channelID, conversationID, sessionID string) string {
	return scopeKey(channelID, conversationID) + "\x00" + sessionID
}

func (pending *pendingReply) stop() {
	if pending.stopTyping != nil {
		pending.stopTyping()
	}
	if pending.timer != nil {
		pending.timer.Stop()
	}
}

// ReplyTracker owns waiting-chat state and last-delivered de-duplication for
// the interactive Session domain.
type ReplyTracker struct {
	host      ChannelHost
	bindings  InteractiveBindingStore
	expecter  TurnExpecter
	log       *slog.Logger
	typingCap time.Duration
	mu        sync.Mutex
	pending   map[string]*pendingReply
	delivered map[string]string
}

func NewReplyTracker(host ChannelHost, bindings InteractiveBindingStore, expecter TurnExpecter, log *slog.Logger) *ReplyTracker {
	if log == nil {
		log = slog.Default()
	}
	return &ReplyTracker{host: host, bindings: bindings, expecter: expecter, log: log.With("component", "session-channel-replies"), typingCap: defaultTypingCap, pending: make(map[string]*pendingReply), delivered: make(map[string]string)}
}

func (t *ReplyTracker) Begin(ch channel.Channel, source channel.ChannelMessage, sessionID string, typingEnabled bool) {
	if t == nil || t.expecter == nil || ch == nil || sessionID == "" {
		return
	}
	t.expecter.ExpectTurn(sessionID)
	stop := func() {}
	if typingEnabled {
		if typer, ok := ch.(channel.TypingIndicator); ok {
			stop = typer.StartTyping(context.Background(), source)
		}
	}
	key := pendingKey(ch.ID(), source.ConversationID, sessionID)
	t.mu.Lock()
	if previous := t.pending[key]; previous != nil {
		previous.stop()
	}
	pending := &pendingReply{channelID: ch.ID(), conversation: source.ConversationID, sessionID: sessionID, source: source, stopTyping: stop}
	pending.timer = time.AfterFunc(t.typingCap, func() { t.onTimeout(key) })
	t.pending[key] = pending
	t.mu.Unlock()
}

func (t *ReplyTracker) onTimeout(key string) {
	t.mu.Lock()
	pending := t.pending[key]
	if pending == nil {
		t.mu.Unlock()
		return
	}
	if pending.stopTyping != nil {
		pending.stopTyping()
		pending.stopTyping = func() {}
	}
	t.mu.Unlock()
	if t.host != nil {
		_, _ = t.host.Reply(context.Background(), pending.source, "⏳ Still working — I'll post the result when it settles.", false)
	}
}

func (t *ReplyTracker) take(sessionID string) []*pendingReply {
	t.mu.Lock()
	defer t.mu.Unlock()
	var result []*pendingReply
	for key, pending := range t.pending {
		if pending.sessionID == sessionID {
			result = append(result, pending)
			delete(t.pending, key)
		}
	}
	return result
}

func (t *ReplyTracker) DeliverTurn(ctx context.Context, event eventbus.Event) {
	sessionID := sessionIDFromEvent(event)
	if sessionID == "" {
		return
	}
	pendings := t.take(sessionID)
	if len(pendings) == 0 {
		return
	}
	reply := ""
	if data, ok := event.Data.(map[string]any); ok {
		reply, _ = data["recent_output"].(string)
	}
	reply = strings.TrimSpace(reply)
	for _, pending := range pendings {
		pending.stop()
		t.bindings.RecordLast(pending.channelID, pending.conversation, sessionID)
		if reply == "" {
			_, _ = t.host.Reply(ctx, pending.source, "✅ Done — no text output for that turn.", true)
			continue
		}
		t.MarkDelivered(sessionID, reply)
		policy := t.host.AdapterChatPolicyFor(ctx, pending.channelID)
		body, footer := trimReply(reply, policy.ReplyMaxChars)
		card := buildReplyCard(sessionID, body+footer)
		out, err := t.host.Deliver(ctx, channel.ChannelMessage{
			ChannelID: pending.channelID, Direction: channel.DirectionOutbound,
			ConversationID: pending.source.ConversationID, ThreadID: pending.source.ThreadID,
			Text: card.RenderText(), Timestamp: time.Now().UTC(), ReplyCtx: pending.source.ReplyCtx,
			Metadata: map[string]any{
				channel.MetaControlKeyboard:    true,
				deliveryIdempotencyMetadataKey: eventDeliveryKey("session-turn", pending.channelID, sessionID, event),
			},
		}, card)
		if err != nil {
			t.log.Error("turn reply send failed", "channel", pending.channelID, "session", sessionID, "err", err)
			continue
		}
		recordOutboundFromMessage(t.bindings, out, sessionID)
	}
}

func (t *ReplyTracker) Cancel(sessionID string) {
	if t == nil || sessionID == "" {
		return
	}
	for _, pending := range t.take(sessionID) {
		pending.stop()
	}
}

func (t *ReplyTracker) MarkDelivered(sessionID, text string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.delivered[sessionID] = text
	t.mu.Unlock()
}

func (t *ReplyTracker) AlreadyDelivered(sessionID, text string) bool {
	if t == nil || sessionID == "" || text == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.delivered[sessionID] == text
}
