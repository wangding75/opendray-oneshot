package channeladapter

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/opendray/opendray-v2/internal/channel"
)

// SessionTargetResolver resolves reply-to, explicit selection, and last-target
// bindings without involving Channel Core.
type SessionTargetResolver interface {
	Resolve(msg channel.ChannelMessage) (string, bool)
}

// InteractiveHandler translates ordinary channel text into queued PTY input.
// It deliberately rejects slash commands so explicit execution-domain commands
// can be claimed by a higher-priority adapter and unknown commands can still be
// reported by Channel Core instead of being typed into a shell.
type InteractiveHandler struct {
	queue    *InputQueue
	host     ChannelHost
	resolver SessionTargetResolver
	log      *slog.Logger
}

func NewInteractiveHandler(queue *InputQueue, host ChannelHost, resolver SessionTargetResolver, log *slog.Logger) *InteractiveHandler {
	if log == nil {
		log = slog.Default()
	}
	return &InteractiveHandler{queue: queue, host: host, resolver: resolver, log: log.With("component", "session-channel-adapter")}
}

func (h *InteractiveHandler) Dispatch(ctx context.Context, req channel.InboundDispatchRequest) (channel.InboundDispatchResult, error) {
	msg := req.Message
	if h == nil || h.queue == nil || h.host == nil || h.resolver == nil || strings.TrimSpace(msg.Text) == "" || !req.Policy.ChatEnabled {
		return channel.InboundDispatchResult{Status: channel.DispatchNotHandled}, nil
	}
	if strings.HasPrefix(strings.TrimSpace(msg.Text), "/") {
		return channel.InboundDispatchResult{Status: channel.DispatchNotHandled}, nil
	}
	sessionID, ok := h.resolver.Resolve(msg)
	if !ok {
		return channel.InboundDispatchResult{Status: channel.DispatchNotHandled}, nil
	}
	if err := h.queue.Enqueue(ctx, queuedInput{
		sessionID:          sessionID,
		source:             msg,
		channel:            req.Channel,
		typingEnabled:      req.Policy.TypingEnabled,
		persistedMessageID: req.PersistedMessageID,
	}); err != nil {
		h.log.Warn("queue session input failed", "channel", msg.ChannelID, "session", sessionID, "err", err)
		_, _ = h.host.Reply(ctx, msg, fmt.Sprintf("Could not queue delivery to %s: %s", sessionID, err), false)
		// A target was resolved and delivery was attempted. Claim the message so
		// a later execution domain can never receive the same prompt.
		return channel.InboundDispatchResult{Status: channel.DispatchHandled}, nil
	}
	return channel.InboundDispatchResult{Status: channel.DispatchHandled}, nil
}
