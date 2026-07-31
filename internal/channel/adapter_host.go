package channel

import (
	"context"
	"errors"
	"time"
)

// InteractiveTargetController is implemented by the owner of an interactive
// execution domain. Channel Core delegates target resolution and explicit
// target selection through this interface; it does not own or interpret the
// target identifier.
type InteractiveTargetController interface {
	ResolveTarget(ChannelMessage) (string, bool)
	SetActiveTarget(channelID, conversationID, targetID string)
	ActiveTarget(channelID, conversationID string) string
	ClearChannel(channelID string)
}

// AdapterChatPolicy is the transport-neutral subset of channel configuration
// needed by execution-domain adapters.
type AdapterChatPolicy struct {
	ChatEnabled     bool
	TypingEnabled   bool
	ReplyMaxChars   int
	IncludeSnippet  bool
	SnippetMaxChars int
}

// SetInteractiveTargetController installs the interactive execution-domain
// routing controller. The controller owns all target bindings and selection
// state; Hub only delegates control-keyboard and legacy command calls to it.
func (h *Hub) SetInteractiveTargetController(controller InteractiveTargetController) {
	h.interactiveTargetMu.Lock()
	h.interactiveTargets = controller
	h.interactiveTargetMu.Unlock()
}

func (h *Hub) interactiveTargetController() InteractiveTargetController {
	h.interactiveTargetMu.RLock()
	defer h.interactiveTargetMu.RUnlock()
	return h.interactiveTargets
}

// SetActiveSession is the compatibility entry point used by existing app
// commands. New callers should use SetActiveSessionFor so selection is scoped
// to one conversation instead of the whole configured channel.
func (h *Hub) SetActiveSession(channelID, sessionID string) {
	h.SetActiveSessionFor(channelID, "", sessionID)
}

// SetActiveSessionFor pins or clears the interactive target for one channel
// conversation. Target ownership and storage remain outside Channel Core.
func (h *Hub) SetActiveSessionFor(channelID, conversationID, sessionID string) {
	if controller := h.interactiveTargetController(); controller != nil {
		controller.SetActiveTarget(channelID, conversationID, sessionID)
	}
}

// ActiveSession is the compatibility counterpart to SetActiveSession.
func (h *Hub) ActiveSession(channelID string) string {
	return h.ActiveSessionFor(channelID, "")
}

// ActiveSessionFor returns the selected interactive target for one channel
// conversation.
func (h *Hub) ActiveSessionFor(channelID, conversationID string) string {
	if controller := h.interactiveTargetController(); controller != nil {
		return controller.ActiveTarget(channelID, conversationID)
	}
	return ""
}

// ChannelByID returns a currently running channel implementation.
func (h *Hub) ChannelByID(channelID string) Channel {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.channels[channelID]
}

// ChannelsSnapshot returns the currently running channel implementations.
func (h *Hub) ChannelsSnapshot() []Channel {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]Channel, 0, len(h.channels))
	for _, ch := range h.channels {
		out = append(out, ch)
	}
	return out
}

// AdapterChatPolicyFor returns the channel settings required by an execution
// adapter without exposing the storage-specific config type.
func (h *Hub) AdapterChatPolicyFor(ctx context.Context, channelID string) AdapterChatPolicy {
	chat := h.chatConfigFor(ctx, channelID)
	snippet := h.snippetPrefs(ctx, channelID)
	return AdapterChatPolicy{
		ChatEnabled:     chat.chatEnabled(),
		TypingEnabled:   chat.typingEnabled(),
		ReplyMaxChars:   chat.replyMaxChars(),
		IncludeSnippet:  snippet.Include,
		SnippetMaxChars: snippet.MaxChars,
	}
}

// ChannelMuted reports whether an adapter should suppress outbound messages
// for this channel.
func (h *Hub) ChannelMuted(ctx context.Context, channelID string) bool {
	return h.isMuted(ctx, channelID)
}

// SuppressNotification applies the shared channel repeat policy to a neutral
// topic/target tuple.
func (h *Hub) SuppressNotification(ctx context.Context, channelID, topic, targetID string) bool {
	return h.suppressByPolicy(ctx, channelID, topic, targetID)
}

// ResetNotificationTarget re-arms all topics for one channel/target tuple.
func (h *Hub) ResetNotificationTarget(channelID, targetID string) {
	h.forgetNotifyForTarget(channelID, targetID)
}

// ResetNotificationTargetAll re-arms all channels for one target.
func (h *Hub) ResetNotificationTargetAll(targetID string) {
	h.forgetNotifyForTargetAll(targetID)
}

// Deliver sends one outbound message using the channel's richest supported
// representation and then persists the same transport-neutral record. The
// returned message retains transport metadata added during Send/SendCard.
func (h *Hub) Deliver(ctx context.Context, msg ChannelMessage, card *Card) (ChannelMessage, error) {
	if service := h.outboundDelivery(); service != nil {
		return service.Deliver(ctx, msg, card)
	}
	ch := h.ChannelByID(msg.ChannelID)
	if ch == nil {
		return msg, ErrNotFound
	}
	if msg.Direction == "" {
		msg.Direction = DirectionOutbound
	}
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now().UTC()
	}
	if msg.Metadata == nil {
		msg.Metadata = map[string]any{}
	}
	msg = resolveOutboundAddress(ch, msg)
	if card != nil && msg.Text == "" {
		msg.Text = card.RenderText()
	}
	if err := h.sendWithFallback(ctx, ch, msg, card); err != nil {
		return msg, err
	}
	if h.store != nil {
		if _, err := h.store.InsertMessage(ctx, msg); err != nil {
			return msg, err
		}
	}
	return msg, nil
}

// PersistOutbound records one logical outbound delivery in channel_messages.
// It is the recorder seam consumed by the shared delivery service. Nil-pool
// test hubs intentionally treat persistence as a no-op.
func (h *Hub) PersistOutbound(ctx context.Context, msg ChannelMessage) (int64, error) {
	if h == nil || h.store == nil {
		return 0, nil
	}
	return h.store.InsertMessage(ctx, msg)
}

// Reply sends a threaded text response to an inbound message. When
// controlKeyboard is true the transport may attach its persistent controls.
func (h *Hub) Reply(ctx context.Context, src ChannelMessage, text string, controlKeyboard bool) (ChannelMessage, error) {
	if h.ChannelByID(src.ChannelID) == nil {
		return ChannelMessage{}, ErrNotFound
	}
	metadata := map[string]any{}
	if controlKeyboard {
		metadata[MetaControlKeyboard] = true
	}
	out := ChannelMessage{
		ChannelID:      src.ChannelID,
		Direction:      DirectionOutbound,
		ConversationID: src.ConversationID,
		ThreadID:       src.ThreadID,
		Text:           text,
		Timestamp:      time.Now().UTC(),
		ReplyCtx:       src.ReplyCtx,
		Metadata:       metadata,
	}
	return h.Deliver(ctx, out, nil)
}

// IsChannelNotFound lets adapters avoid importing storage implementation
// errors when a channel is stopped while a reply is being delivered.
func IsChannelNotFound(err error) bool { return errors.Is(err, ErrNotFound) }

func resolveOutboundAddress(ch Channel, msg ChannelMessage) ChannelMessage {
	resolver, ok := ch.(OutboundAddressResolver)
	if !ok {
		return msg
	}
	address := resolver.ResolveOutboundAddress(msg)
	if address.ConversationID != "" {
		msg.ConversationID = address.ConversationID
		msg.Metadata[MetaOutboundConversationID] = address.ConversationID
	}
	if address.ThreadID != "" {
		msg.ThreadID = address.ThreadID
		msg.Metadata[MetaOutboundThreadID] = address.ThreadID
	}
	if address.ReplyCtx != nil {
		msg.ReplyCtx = address.ReplyCtx
	}
	return msg
}
