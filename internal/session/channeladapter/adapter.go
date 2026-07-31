// Package channeladapter owns the interactive Session domain's messaging
// integration. It is the only package allowed to translate a normalized
// channel message into PTY input or turn Session lifecycle events into chat
// notifications.
package channeladapter

import (
	"context"
	"log/slog"

	"github.com/opendray/opendray-v2/internal/channel"
	"github.com/opendray/opendray-v2/internal/eventbus"
)

const (
	// InboundPriority keeps interactive PTY routing behind explicit One-shot
	// command/reply handlers while preserving the current plain-text fallback.
	InboundPriority = 1000
)

// Inputter is the minimal Session Manager capability required to forward text
// into an existing PTY-backed session.
type Inputter interface {
	Input(ctx context.Context, sessionID string, data []byte) error
}

// TurnExpecter arms the Session Manager's turn detector after chat input.
type TurnExpecter interface {
	ExpectTurn(sessionID string)
}

// ChannelHost is the transport-neutral surface exposed by Channel Core to
// execution-domain adapters. The adapter owns all Session semantics and state.
type ChannelHost interface {
	ChannelByID(channelID string) channel.Channel
	ChannelsSnapshot() []channel.Channel
	AdapterChatPolicyFor(ctx context.Context, channelID string) channel.AdapterChatPolicy
	ChannelMuted(ctx context.Context, channelID string) bool
	SuppressNotification(ctx context.Context, channelID, topic, targetID string) bool
	ResetNotificationTarget(channelID, targetID string)
	ResetNotificationTargetAll(targetID string)
	Deliver(ctx context.Context, msg channel.ChannelMessage, card *channel.Card) (channel.ChannelMessage, error)
	Reply(ctx context.Context, src channel.ChannelMessage, text string, controlKeyboard bool) (channel.ChannelMessage, error)
}

// Adapter composes interactive inbound routing, binding state, PTY submission,
// reply tracking, and Session lifecycle notification.
type Adapter struct {
	bindings *MemoryBindingStore
	queue    *InputQueue
	handler  *InteractiveHandler
	notifier *SessionNotifier
}

func New(input Inputter, host ChannelHost, bus *eventbus.Hub, log *slog.Logger) *Adapter {
	if log == nil {
		log = slog.Default()
	}
	bindings := NewMemoryBindingStore(DefaultOutboundBindingLimit, 0, nil)
	submitter := NewInputSubmitter(input)
	var expecter TurnExpecter
	if e, ok := input.(TurnExpecter); ok {
		expecter = e
	}
	tracker := NewReplyTracker(host, bindings, expecter, log)
	queue := NewInputQueue(submitter, host, tracker, bus, log)
	handler := NewInteractiveHandler(queue, host, bindings, log)
	notifier := NewSessionNotifier(host, bindings, tracker, bus, log)
	return &Adapter{bindings: bindings, queue: queue, handler: handler, notifier: notifier}
}

func (a *Adapter) Dispatch(ctx context.Context, req channel.InboundDispatchRequest) (channel.InboundDispatchResult, error) {
	if a == nil || a.handler == nil {
		return channel.InboundDispatchResult{Status: channel.DispatchNotHandled}, nil
	}
	return a.handler.Dispatch(ctx, req)
}

// ResolveTarget implements channel.InteractiveTargetController.
func (a *Adapter) ResolveTarget(msg channel.ChannelMessage) (string, bool) {
	if a == nil || a.bindings == nil {
		return "", false
	}
	return a.bindings.Resolve(msg)
}

// SetActiveTarget implements channel.InteractiveTargetController.
func (a *Adapter) SetActiveTarget(channelID, conversationID, targetID string) {
	if a != nil && a.bindings != nil {
		a.bindings.SetActive(channelID, conversationID, targetID)
	}
}

// ActiveTarget implements channel.InteractiveTargetController.
func (a *Adapter) ActiveTarget(channelID, conversationID string) string {
	if a == nil || a.bindings == nil {
		return ""
	}
	return a.bindings.Active(channelID, conversationID)
}

// ClearChannel removes all interactive routing hints for a deleted channel.
func (a *Adapter) ClearChannel(channelID string) {
	if a != nil && a.bindings != nil {
		a.bindings.ClearChannel(channelID)
	}
}

func (a *Adapter) Start(ctx context.Context) {
	if a != nil && a.notifier != nil {
		a.notifier.Start(ctx)
	}
}

func (a *Adapter) Shutdown(ctx context.Context) error {
	if a == nil {
		return nil
	}
	var firstErr error
	if a.queue != nil {
		if err := a.queue.Shutdown(ctx); err != nil {
			firstErr = err
		}
	}
	if a.notifier != nil {
		if err := a.notifier.Shutdown(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Bindings exposes the adapter-owned binding state for command integration and
// focused tests. Callers should prefer the target-controller methods above.
func (a *Adapter) Bindings() InteractiveBindingStore {
	if a == nil {
		return nil
	}
	return a.bindings
}
