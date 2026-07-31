package channel

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/opendray/opendray-v2/internal/eventbus"
)

// Hub manages the lifecycle of all configured channels in this process.
type Hub struct {
	log                    *slog.Logger
	bus                    *eventbus.Hub
	store                  *store
	cmds                   *CommandRegistry
	dispatchMu             sync.RWMutex
	inboundDispatcher      InboundDispatcher
	inboundDispatchTimeout time.Duration

	interactiveTargetMu sync.RWMutex
	interactiveTargets  InteractiveTargetController

	deliveryMu sync.RWMutex
	delivery   OutboundDeliveryService

	mu        sync.RWMutex
	channels  map[string]Channel
	started   bool
	cancelOut context.CancelFunc
	outDone   chan struct{}

	notifyMu    sync.Mutex
	notifyState map[string]map[string]time.Time // channelID -> "topic|sessionID" -> sent-at

	// senderAuthz, when set, gates ALL inbound (plain text that reaches
	// a session's stdin, slash commands, and button taps) to authorized
	// senders only — unauthorized messages are dropped at the door. nil
	// = allow all (preserves behavior for deployments that don't
	// configure an owner). See SetSenderAuthorizer.
	senderAuthz func(msg ChannelMessage) bool
}

// defaultCooldown is the per-channel cooldown applied when the
// channel's config does not explicitly set notify_cooldown_s. Keeps
// flapping CLIs (which emit periodic output between idle windows)
// from re-notifying every minute. Operators can lower or raise it
// per channel.
const defaultCooldown = 5 * time.Minute

func NewHub(pool *pgxpool.Pool, bus *eventbus.Hub, log *slog.Logger) *Hub {
	if log == nil {
		log = slog.Default()
	}
	h := &Hub{
		log:                    log.With("component", "channel"),
		bus:                    bus,
		store:                  newStore(pool),
		cmds:                   NewCommandRegistry(),
		channels:               make(map[string]Channel),
		notifyState:            make(map[string]map[string]time.Time),
		inboundDispatchTimeout: defaultInboundDispatchTimeout,
	}
	h.registerBuiltinCommands()
	return h
}

// SetOutboundDelivery installs the shared durable transport delivery service.
// The service is intentionally injected after Hub construction so the
// implementation may depend on Hub's channel registry and message recorder
// without creating a package import cycle.
func (h *Hub) SetOutboundDelivery(service OutboundDeliveryService) {
	h.deliveryMu.Lock()
	h.delivery = service
	h.deliveryMu.Unlock()
}

func (h *Hub) outboundDelivery() OutboundDeliveryService {
	h.deliveryMu.RLock()
	defer h.deliveryMu.RUnlock()
	return h.delivery
}

// SetCipher wires the at-rest cipher used to encrypt channel config
// secrets (bot tokens, app secrets, webhook keys). Called once at boot
// after the backup subsystem is available. Until set — or while the
// backup feature is unarmed — secrets are stored plaintext, the
// historical behaviour.
func (h *Hub) SetCipher(c FieldCipher) { h.store.setCipher(c) }

const defaultInboundDispatchTimeout = 5 * time.Second

// SetInboundDispatcher installs the single neutral execution-domain routing
// entry point. A DispatcherChain can host multiple independently owned
// handlers while keeping Channel Core unaware of their domain types.
func (h *Hub) SetInboundDispatcher(dispatcher InboundDispatcher) {
	h.dispatchMu.Lock()
	h.inboundDispatcher = dispatcher
	h.dispatchMu.Unlock()
}

// SetInboundDispatchTimeout overrides the bounded dispatch window. It is
// primarily intended for tests and controlled deployments; non-positive
// values restore the default.
func (h *Hub) SetInboundDispatchTimeout(timeout time.Duration) {
	if timeout <= 0 {
		timeout = defaultInboundDispatchTimeout
	}
	h.dispatchMu.Lock()
	h.inboundDispatchTimeout = timeout
	h.dispatchMu.Unlock()
}

// SetSenderAuthorizer installs a GLOBAL fallback predicate the Hub
// consults when a channel has no per-channel owner allowlist configured.
// Per-channel `owner_user_ids` (set from the dashboard) takes
// precedence; this env-driven predicate is the deployment-wide default.
// Pass nil (or never call this) to allow all senders when no owner is
// configured anywhere.
//
// The gate sits at the inbound door (handleInbound), not per-command:
// gating only control commands would still let an unauthorized sender
// type instructions into a session, since plain text goes straight to
// the PTY.
func (h *Hub) SetSenderAuthorizer(fn func(msg ChannelMessage) bool) { h.senderAuthz = fn }

// authorizeSender reports whether msg may be processed at all, given the
// channel's chat config. Precedence: a per-channel owner allowlist (from
// the dashboard) wins; otherwise the global env predicate; otherwise
// open. When owners are configured the check is fail-closed — the sender
// must present a matching id.
func (h *Hub) authorizeSender(cc chatConfig, msg ChannelMessage) bool {
	if owners := cc.ownerSet(); len(owners) > 0 {
		id, _ := msg.Metadata["tg_user_id"].(string)
		return id != "" && owners[id]
	}
	if h.senderAuthz != nil {
		return h.senderAuthz(msg)
	}
	return true
}

// Commands returns the command registry so app code can wire additional
// session-aware commands (cancel, resume, status, ...) at startup.
func (h *Hub) Commands() *CommandRegistry { return h.cmds }

// RegisterCommand is a convenience wrapper around Commands().Register().
func (h *Hub) RegisterCommand(c Command) { h.cmds.Register(c) }

// registerBuiltinCommands wires only the channel-scoped commands
// that don't need session access. /list, /end, and /resume live in
// the app layer (internal/app) so the channel package stays free of
// the session dependency.
//
// Slash-command set (intentionally small — operators get tired of
// long /help output and unmaintained shims):
//
//	/help    — list available commands (registered by NewCommandRegistry)
//	/notify  — toggle channel notifications on/off
//	/list    — active sessions (registered by app code)
//	/end     — end a session (registered by app code)
//	/resume  — resume a stopped/ended session (registered by app code)
//
// What we DELETED and why:
//   - /status   — channel-level diagnostic ("which capabilities does
//     this channel have"). Useful exactly once, then
//     noise in the /help output. The web admin shows
//     the same info with more context.
//   - /select   — pin a chat to a specific session_id. The reply-
//     to-message routing owned by the Session adapter covers the
//     multi-session case more naturally; the pin was a
//     power-user feature that nobody used.
//   - /sessions — listed sessions that had previously NOTIFIED this
//     channel, not currently-active sessions. Confused
//     operators who expected /sessions == /list.
//
// Interactive target selection is owned by the Session channel adapter.
// The Hub retains compatibility methods used by app-level commands, but only
// delegates to the installed InteractiveTargetController.
func (h *Hub) registerBuiltinCommands() {
	h.cmds.Register(Command{
		Name:        "notify",
		Description: "Toggle notifications: /notify on|off",
		Source:      "builtin",
		Handler: func(ctx context.Context, cc CommandContext) (string, error) {
			if len(cc.Args) == 0 {
				return "Usage: /notify on|off", nil
			}
			on := cc.Args[0] == "on"
			if !on && cc.Args[0] != "off" {
				return "Usage: /notify on|off", nil
			}
			if err := h.setMuted(ctx, cc.Channel.ID(), !on); err != nil {
				return "", err
			}
			if on {
				return "Notifications enabled.", nil
			}
			return "Notifications muted.", nil
		},
	})
	// /confirm gates the destructive control buttons (Stop/Restart) on
	// a reply card: tapping one opens this two-button confirmation
	// instead of acting immediately, so a stray tap on a phone can't
	// interrupt a live session. The Yes button carries the real command
	// (/end or /resume); Cancel just reopens the session list.
	h.cmds.Register(Command{
		Name:        "confirm",
		Description: "Confirm a session action (used by inline buttons)",
		Source:      "builtin",
		CardHandler: confirmCardHandler,
	})
}

// confirmCardHandler renders the Yes/Cancel card for a destructive
// control action. Args are [verb, sessionID]; verb is "stop",
// "restart", or "remove". Unknown input degrades to a harmless hint.
func confirmCardHandler(_ context.Context, cc CommandContext) (*Card, error) {
	if len(cc.Args) < 2 {
		return &Card{Elements: []CardElement{
			CardMarkdown{Content: "Nothing to confirm."},
		}}, nil
	}
	verb, sid := strings.ToLower(cc.Args[0]), cc.Args[1]
	var title, body, yesText, yesCmd string
	switch verb {
	case "stop":
		title = "Stop session?"
		body = fmt.Sprintf("Stop session `%s`? This interrupts the running agent.", sid)
		yesText, yesCmd = "✓ Yes, stop", "cmd:/end "+sid
	case "restart":
		title = "Restart session?"
		body = fmt.Sprintf("Restart session `%s`? It re-spawns under the same id.", sid)
		yesText, yesCmd = "✓ Yes, restart", "cmd:/resume "+sid
	case "remove":
		title = "Remove session?"
		body = fmt.Sprintf("Remove session `%s`? This stops it AND deletes it permanently — no restart.", sid)
		yesText, yesCmd = "🗑 Yes, remove", "cmd:/remove "+sid
	default:
		return &Card{Elements: []CardElement{
			CardMarkdown{Content: "Unknown action to confirm."},
		}}, nil
	}
	return &Card{
		Header: &CardHeader{Title: title, Color: "orange"},
		Elements: []CardElement{
			CardMarkdown{Content: body},
			CardActions{Buttons: [][]ButtonOption{{
				{Text: yesText, Value: yesCmd, Style: "danger"},
				{Text: "✗ Cancel", Value: "cmd:/list"},
			}}},
		},
	}, nil
}

// Start loads enabled channels from DB, instantiates each via its
// registered factory, calls Channel.Start, and subscribes to outbound
// session.* events. Caller must call Shutdown to stop.
func (h *Hub) Start(ctx context.Context) error {
	h.mu.Lock()
	if h.started {
		h.mu.Unlock()
		return nil
	}
	h.started = true
	h.mu.Unlock()

	rows, err := h.store.List(ctx)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if !r.Enabled {
			continue
		}
		if err := h.spawn(ctx, r); err != nil {
			h.log.Error("channel start failed", "id", r.ID, "kind", r.Kind, "err", err)
		}
	}

	outCtx, cancel := context.WithCancel(context.Background())
	h.cancelOut = cancel
	h.outDone = make(chan struct{})
	if lifecycle, ok := h.outboundDelivery().(OutboundDeliveryLifecycle); ok {
		lifecycle.Start(outCtx)
	}
	go h.runOutbound(outCtx)
	return nil
}

// Shutdown stops all channels and the outbound dispatcher.
func (h *Hub) Shutdown(ctx context.Context) error {
	h.mu.Lock()
	if !h.started {
		h.mu.Unlock()
		return nil
	}
	h.started = false
	cancel := h.cancelOut
	done := h.outDone
	chs := make([]Channel, 0, len(h.channels))
	for _, c := range h.channels {
		chs = append(chs, c)
	}
	// Keep the channel registry available until the durable delivery worker has
	// stopped. Clearing it first can turn an in-flight shutdown delivery into a
	// false transport-not-found failure and permanently dead-letter the outbox
	// record.
	h.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if lifecycle, ok := h.outboundDelivery().(OutboundDeliveryLifecycle); ok {
		if err := lifecycle.Shutdown(ctx); err != nil {
			h.log.Error("outbound delivery shutdown", "err", err)
		}
	}
	for _, c := range chs {
		if err := c.Stop(ctx); err != nil {
			h.log.Error("channel stop", "id", c.ID(), "err", err)
		}
	}
	h.mu.Lock()
	if !h.started {
		h.channels = make(map[string]Channel)
	}
	h.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (h *Hub) spawn(ctx context.Context, r channelRow) error {
	factory := Lookup(r.Kind)
	if factory == nil {
		return fmt.Errorf("%w: %s", ErrUnknownKind, r.Kind)
	}
	ch, err := factory(r.ID, r.Config, h.log)
	if err != nil {
		return fmt.Errorf("factory: %w", err)
	}
	if err := ch.Start(ctx, h.handleInbound); err != nil {
		return fmt.Errorf("channel start: %w", err)
	}
	h.mu.Lock()
	h.channels[r.ID] = ch
	h.mu.Unlock()
	return nil
}

// handleInbound is invoked by Channel implementations when a message arrives.
// It authorizes and persists the transport-neutral message, then delegates
// command handling and execution-domain routing to processInboundAfterPersist.
// Channel Core never guesses an execution target after a dispatcher failure.
func (h *Hub) handleInbound(ctx context.Context, msg ChannelMessage) error {
	msg.Direction = DirectionInbound
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now().UTC()
	}
	// Read the channel's chat config once (owner allowlist + chat/typing
	// toggles), then reuse it for the gate and routing below.
	cc := h.chatConfigFor(ctx, msg.ChannelID)
	// Sender gate: when an owner is configured, only authorized senders
	// may interact at all. Drop silently before persisting or routing —
	// a Telegram bot receives DMs from anyone, and unauthorized text
	// would otherwise reach a session's stdin. Silent (no reply) so a
	// hostile sender gets no confirmation the bot is live; the attempt
	// is logged + published for monitoring.
	if !h.authorizeSender(cc, msg) {
		h.log.Warn("inbound from unauthorized sender dropped",
			"channel", msg.ChannelID, "author", msg.Author)
		h.bus.Publish(eventbus.Event{
			Topic: "channel.inbound_denied",
			Data: map[string]any{
				"channel_id": msg.ChannelID,
				"author":     msg.Author,
			},
		})
		return nil
	}
	id, err := h.store.InsertMessage(ctx, msg)
	if err != nil {
		h.log.Error("inbound persist failed", "channel", msg.ChannelID, "err", err)
		return err
	}
	return h.processInboundAfterPersist(ctx, msg, id, cc)
}

// processInboundAfterPersist handles channel-level commands first, then sends
// ordinary messages through the neutral execution-domain dispatcher. Once a
// dispatcher errors, panics, or times out, processing stops: falling through
// to PTY after an uncertain partial side effect could duplicate delivery.
func (h *Hub) processInboundAfterPersist(ctx context.Context, msg ChannelMessage, id int64, cc chatConfig) error {
	if name, args, ok := ParseCommand(msg.Text); ok {
		// Channel- and app-registered commands keep precedence. Unknown slash
		// commands are offered to execution-domain adapters before Channel Core
		// emits the unknown-command response, allowing One-shot to own /run,
		// /task, /continue, and related commands without importing that domain.
		if _, registered := h.cmds.Lookup(name); registered {
			h.handleCommand(ctx, msg, id, name, args)
			return nil
		}
		result, err := h.dispatchInbound(ctx, msg, id, cc)
		if h.stopAfterDispatchError(msg, id, result, err) || result.Handled() {
			return nil
		}
		h.handleCommand(ctx, msg, id, name, args)
		return nil
	}

	// Persistent control-keyboard taps arrive as literal label text. Keep this
	// channel-level translation ahead of execution-domain routing.
	if name, args, hint, isButton := h.controlButtonAction(msg); isButton {
		if hint != "" {
			h.replyTextLookup(ctx, msg, hint)
		} else {
			h.handleCommand(ctx, msg, id, name, args)
		}
		return nil
	}

	result, err := h.dispatchInbound(ctx, msg, id, cc)
	if h.stopAfterDispatchError(msg, id, result, err) || result.Handled() {
		return nil
	}

	h.publishEvent(eventbus.Event{
		Topic: "channel.message_received",
		Data: map[string]any{
			"channel_id":         msg.ChannelID,
			"channel_message_id": id,
			"conversation_id":    msg.ConversationID,
			"author":             msg.Author,
			"text":               msg.Text,
		},
	})
	return nil
}

// stopAfterDispatchError records a terminal execution-domain routing error.
// A persisted inbound is deliberately acknowledged because the failing handler
// may already have completed a partial side effect; retrying at the transport
// boundary could duplicate the action.
func (h *Hub) stopAfterDispatchError(msg ChannelMessage, id int64, result InboundDispatchResult, err error) bool {
	if err == nil {
		return false
	}
	h.log.Error("inbound execution-domain dispatch stopped",
		"channel", msg.ChannelID,
		"channel_message_id", id,
		"handler", result.Handler,
		"err", err)
	return true
}

type inboundDispatchOutcome struct {
	result InboundDispatchResult
	err    error
}

// dispatchInbound invokes the configured dispatcher exactly once for a
// persisted message and records explicit handled/not-handled/failure audit
// events. Panic and timeout are terminal and never fall through to PTY.
func (h *Hub) dispatchInbound(ctx context.Context, msg ChannelMessage, id int64, cc chatConfig) (InboundDispatchResult, error) {
	h.dispatchMu.RLock()
	dispatcher := h.inboundDispatcher
	timeout := h.inboundDispatchTimeout
	h.dispatchMu.RUnlock()
	if dispatcher == nil {
		return InboundDispatchResult{Status: DispatchNotHandled}, nil
	}
	if timeout <= 0 {
		timeout = defaultInboundDispatchTimeout
	}

	h.mu.RLock()
	ch := h.channels[msg.ChannelID]
	h.mu.RUnlock()
	req := InboundDispatchRequest{
		PersistedMessageID: id,
		Channel:            ch,
		Message:            msg,
		ReplyAddress:       msg.ReplyAddress(),
		Policy: InboundPolicy{
			ChatEnabled:   cc.chatEnabled(),
			TypingEnabled: cc.typingEnabled(),
		},
	}

	h.publishEvent(eventbus.Event{
		Topic: "channel.inbound_dispatch.started",
		Data: map[string]any{
			"channel_id":         msg.ChannelID,
			"channel_message_id": id,
		},
	})

	dispatchCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	outcomes := make(chan inboundDispatchOutcome, 1)
	go func() {
		outcome := inboundDispatchOutcome{}
		defer func() {
			if recovered := recover(); recovered != nil {
				outcome.err = fmt.Errorf("%w: %v", ErrInboundDispatchPanic, recovered)
			}
			outcomes <- outcome
		}()
		outcome.result, outcome.err = dispatcher.Dispatch(dispatchCtx, req)
	}()

	select {
	case <-dispatchCtx.Done():
		err := dispatchCtx.Err()
		topic := "channel.inbound_dispatch.failed"
		if errors.Is(err, context.DeadlineExceeded) {
			err = fmt.Errorf("%w after %s", ErrInboundDispatchTimeout, timeout)
			topic = "channel.inbound_dispatch.timed_out"
		}
		h.publishEvent(eventbus.Event{
			Topic: topic,
			Data: map[string]any{
				"channel_id":         msg.ChannelID,
				"channel_message_id": id,
				"error":              err.Error(),
			},
		})
		return InboundDispatchResult{Status: DispatchNotHandled}, err
	case outcome := <-outcomes:
		if outcome.err != nil {
			topic := "channel.inbound_dispatch.failed"
			if errors.Is(outcome.err, ErrInboundDispatchPanic) {
				topic = "channel.inbound_dispatch.panicked"
			}
			h.publishEvent(eventbus.Event{
				Topic: topic,
				Data: map[string]any{
					"channel_id":         msg.ChannelID,
					"channel_message_id": id,
					"handler":            outcome.result.Handler,
					"error":              outcome.err.Error(),
				},
			})
			return outcome.result, outcome.err
		}
		topic := "channel.inbound_dispatch.not_handled"
		if outcome.result.Handled() {
			topic = "channel.inbound_dispatch.handled"
		}
		h.publishEvent(eventbus.Event{
			Topic: topic,
			Data: map[string]any{
				"channel_id":         msg.ChannelID,
				"channel_message_id": id,
				"handler":            outcome.result.Handler,
			},
		})
		return outcome.result, nil
	}
}

func (h *Hub) publishEvent(ev eventbus.Event) {
	if h.bus != nil {
		h.bus.Publish(ev)
	}
}

// replyTextLookup is a defensive helper: it fetches the channel impl
// from the live map and posts a reply. Returns silently if the
// channel isn't running anymore (Stop racing with inbound).
func (h *Hub) replyTextLookup(ctx context.Context, src ChannelMessage, text string) {
	h.mu.RLock()
	ch := h.channels[src.ChannelID]
	h.mu.RUnlock()
	if ch == nil {
		return
	}
	h.replyText(ctx, ch, src, text)
}

// handleCommand dispatches a parsed command to its registered handler
// and ships the (optional) reply back through the originating channel.
// Unknown commands publish channel.command_unknown and reply with a
// hint pointing to /help.
func (h *Hub) handleCommand(ctx context.Context, msg ChannelMessage, mid int64, name string, args []string) {
	h.mu.RLock()
	ch := h.channels[msg.ChannelID]
	h.mu.RUnlock()

	cmd, ok := h.cmds.Lookup(name)
	if !ok {
		h.bus.Publish(eventbus.Event{
			Topic: "channel.command_unknown",
			Data: map[string]any{
				"channel_id":         msg.ChannelID,
				"channel_message_id": mid,
				"command":            name,
				"args":               args,
			},
		})
		if ch != nil {
			h.replyText(ctx, ch, msg, fmt.Sprintf("Unknown command /%s — try /help", name))
		}
		return
	}
	// Note: sender authorization is enforced once, at the inbound door
	// (handleInbound → authorizeSender), so every command reaching here
	// is already from an authorized sender — no per-command re-check.
	h.bus.Publish(eventbus.Event{
		Topic: "channel.command_received",
		Data: map[string]any{
			"channel_id":         msg.ChannelID,
			"channel_message_id": mid,
			"command":            name,
			"args":               args,
			"source":             cmd.Source,
		},
	})
	if ch == nil {
		return
	}
	cc := CommandContext{
		Channel: ch, Message: msg, Hub: h,
		Command: name, Args: args, Raw: msg.Text,
	}
	// CardHandler wins when both are set — structured reply
	// (buttons) is strictly more capable than plain text, and the
	// CardSender adapters degrade to Card.RenderText() on channels
	// that don't render buttons. We log the misconfig so future
	// contributors notice they accidentally provided both.
	if cmd.CardHandler != nil {
		if cmd.Handler != nil {
			h.log.Warn("command has both Handler and CardHandler; using CardHandler",
				"command", name)
		}
		card, err := cmd.CardHandler(ctx, cc)
		if err != nil {
			h.log.Error("card command handler failed", "command", name, "err", err)
			h.replyText(ctx, ch, msg,
				fmt.Sprintf("Error running /%s: %s", name, err))
			return
		}
		if card == nil {
			return
		}
		out := ChannelMessage{
			ChannelID:      msg.ChannelID,
			Direction:      DirectionOutbound,
			ConversationID: msg.ConversationID,
			ThreadID:       msg.ThreadID,
			Text:           card.RenderText(),
			Timestamp:      time.Now().UTC(),
			ReplyCtx:       msg.ReplyCtx,
		}
		if _, err := h.Deliver(ctx, out, card); err != nil {
			h.log.Error("send card reply", "command", name, "err", err)
		}
		return
	}
	if cmd.Handler == nil {
		h.log.Warn("command has no handler", "command", name)
		return
	}
	reply, err := cmd.Handler(ctx, cc)
	if err != nil {
		h.log.Error("command handler failed", "command", name, "err", err)
		h.replyText(ctx, ch, msg, fmt.Sprintf("Error running /%s: %s", name, err))
		return
	}
	if reply != "" {
		// Commands flagged DocksControlKeyboard refresh the persistent
		// keyboard alongside their text reply (e.g. /select, /start), so a
		// layout change shows up at a natural tap point rather than only on
		// the next agent reply. replyControlText falls back to plain text
		// on transports without a docked keyboard.
		if cmd.DocksControlKeyboard {
			h.replyControlText(ctx, ch, msg, reply)
		} else {
			h.replyText(ctx, ch, msg, reply)
		}
	}
}

// replyText posts a text reply back through the originating channel,
// preserving ReplyCtx so it threads correctly. Persisted to
// channel_messages and treated like any other outbound.
func (h *Hub) replyText(ctx context.Context, ch Channel, src ChannelMessage, text string) {
	if _, err := h.Reply(ctx, src, text, false); err != nil {
		h.log.Error("command reply send failed", "channel", ch.ID(), "err", err)
	}
}

// controlButtonAction resolves a persistent-keyboard tap (whose inbound
// text is the button's literal label) into the command to dispatch.
//
//   - isButton=false → msg.Text is not a control label; the caller falls
//     through to normal command / plain-text routing.
//   - isButton=true, hint!="" → the button targets a session but none is
//     active; the caller should post hint and stop.
//   - isButton=true, hint=="" → dispatch (name, args) as a command.
func (h *Hub) controlButtonAction(msg ChannelMessage) (name string, args []string, hint string, isButton bool) {
	btn, ok := MatchControlButton(msg.Text)
	if !ok {
		return "", nil, "", false
	}
	cmdText := btn.Command
	if btn.NeedsSession() {
		controller := h.interactiveTargetController()
		if controller == nil {
			return "", nil, "No active session — tap 🔀 Switch to pick one.", true
		}
		targetID, ok := controller.ResolveTarget(msg)
		if !ok {
			return "", nil, "No active session — tap 🔀 Switch to pick one.", true
		}
		cmdText = btn.Resolve(targetID)
	}
	n, a, _ := ParseCommand(cmdText)
	return n, a, "", true
}

// replyControlText is replyText that also flags the message for the
// persistent control keyboard, so a plain-text chat reply (e.g. the
// "no text output" acknowledgement) still establishes / refreshes the
// keyboard on transports that support it.
func (h *Hub) replyControlText(ctx context.Context, ch Channel, src ChannelMessage, text string) {
	if _, err := h.Reply(ctx, src, text, true); err != nil {
		h.log.Error("control reply send failed", "channel", ch.ID(), "err", err)
	}
}

// setMuted patches the channel's config JSON to set or clear the
// muted flag. dispatch() honours this flag by skipping muted channels.
func (h *Hub) setMuted(ctx context.Context, channelID string, muted bool) error {
	row, err := h.store.Get(ctx, channelID)
	if err != nil {
		return err
	}
	var cfg map[string]any
	if len(row.Config) > 0 {
		_ = json.Unmarshal(row.Config, &cfg)
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	cfg["muted"] = muted
	patched, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal channel config: %w", err)
	}
	return h.store.Update(ctx, channelID, patched, nil)
}

// isMuted returns true when the channel's config has muted=true.
func (h *Hub) isMuted(ctx context.Context, channelID string) bool {
	row, err := h.store.Get(ctx, channelID)
	if err != nil {
		return false
	}
	var cfg struct {
		Muted bool `json:"muted"`
	}
	_ = json.Unmarshal(row.Config, &cfg)
	return cfg.Muted
}

// NotifyMode is the per-channel suppression policy for repeat
// session.* notifications.
//
//	once     — fire once per (channel, topic, session) tuple. Stays
//	           suppressed until either the channel is updated, the
//	           session ends, or user input arrives back via the same
//	           channel (which resets the entry).
//	cooldown — time-window suppression keyed off notify_cooldown_s.
//	every    — never suppress; every event fires a notification.
//
// Default is `once`: matches user expectations ("notify me once when
// the session needs me, not every minute").
type NotifyMode string

const (
	NotifyModeOnce     NotifyMode = "once"
	NotifyModeCooldown NotifyMode = "cooldown"
	NotifyModeEvery    NotifyMode = "every"
)

// onceModeTTL bounds how long a `once`-mode suppression record sits
// in memory before GC reclaims it. Channels left running for weeks
// shouldn't leak memory. After TTL, a fresh idle event would notify
// again — usually fine, since by then the user has either responded
// or stopped caring.
const onceModeTTL = 24 * time.Hour

// notifyPolicy returns the resolved (mode, cooldown) for the channel.
// When the channel config does not set notify_mode, the legacy field
// notify_cooldown_s decides: > 0 → cooldown mode, anything else →
// once mode. This keeps existing channels saved before notify_mode
// existed working sanely (no spam).
func (h *Hub) notifyPolicy(ctx context.Context, channelID string) (NotifyMode, time.Duration) {
	row, err := h.store.Get(ctx, channelID)
	if err != nil {
		return NotifyModeOnce, 0
	}
	var cfg struct {
		Mode    string `json:"notify_mode"`
		Seconds *int   `json:"notify_cooldown_s"`
	}
	_ = json.Unmarshal(row.Config, &cfg)
	cooldown := time.Duration(0)
	if cfg.Seconds != nil && *cfg.Seconds > 0 {
		cooldown = time.Duration(*cfg.Seconds) * time.Second
	}
	switch NotifyMode(cfg.Mode) {
	case NotifyModeOnce:
		return NotifyModeOnce, 0
	case NotifyModeCooldown:
		if cooldown <= 0 {
			cooldown = defaultCooldown
		}
		return NotifyModeCooldown, cooldown
	case NotifyModeEvery:
		return NotifyModeEvery, 0
	}
	// Mode missing — infer from legacy cooldown field. Pre-feature
	// channels (no notify_cooldown_s either) get `once`.
	if cooldown > 0 {
		return NotifyModeCooldown, cooldown
	}
	return NotifyModeOnce, 0
}

// suppressByPolicy reports whether the (channel, topic, session)
// notification should be skipped under the channel's notify mode.
//   - every    → never
//   - cooldown → suppressed if last fire was within `cd` ago
//   - once     → suppressed forever after the first fire (until
//     forgetNotifyState* / TTL elapses)
//
// The state map is opportunistically GC'd: entries older than the
// effective TTL (cooldown→2×cd, once→onceModeTTL) are dropped on
// every call, so memory stays bounded under session churn.
func (h *Hub) suppressByPolicy(ctx context.Context, channelID, topic, targetID string) bool {
	mode, cd := h.notifyPolicy(ctx, channelID)
	if mode == NotifyModeEvery {
		return false
	}
	now := time.Now()
	key := topic + "|" + targetID

	h.notifyMu.Lock()
	defer h.notifyMu.Unlock()
	chState := h.notifyState[channelID]
	if chState == nil {
		chState = make(map[string]time.Time)
		h.notifyState[channelID] = chState
	}
	if last, ok := chState[key]; ok {
		switch mode {
		case NotifyModeOnce:
			// Suppressed forever, until forgetNotifyForTarget is
			// called (user input arrives) or the entry ages out.
			if now.Sub(last) < onceModeTTL {
				return true
			}
		case NotifyModeCooldown:
			if cd > 0 && now.Sub(last) < cd {
				return true
			}
		}
	}
	chState[key] = now

	// GC. Use the larger of the two windows so we don't lose state
	// the same call just wrote.
	ttl := onceModeTTL
	if mode == NotifyModeCooldown {
		ttl = 2 * cd
	}
	if ttl > 0 {
		cutoff := now.Add(-ttl)
		for k, t := range chState {
			if t.Before(cutoff) {
				delete(chState, k)
			}
		}
	}
	return false
}

// forgetNotifyState clears the per-channel suppression record. Called
// on channel update / delete so config changes (e.g. switching modes)
// take effect immediately and deleted channels don't leak memory.
func (h *Hub) forgetNotifyState(channelID string) {
	h.notifyMu.Lock()
	defer h.notifyMu.Unlock()
	delete(h.notifyState, channelID)
}

// forgetNotifyForTarget clears the suppression entry for one
// (channel, topic, target) triple. Execution adapters call it when the
// user interacts with that target so the next lifecycle event may notify
// again. Channel Core does not interpret the target or topic namespace.
func (h *Hub) forgetNotifyForTarget(channelID, targetID string) {
	h.notifyMu.Lock()
	defer h.notifyMu.Unlock()
	state, ok := h.notifyState[channelID]
	if !ok {
		return
	}
	suffix := "|" + targetID
	for k := range state {
		if strings.HasSuffix(k, suffix) {
			delete(state, k)
		}
	}
}

// forgetNotifyForTargetAll clears suppression entries for a target across
// every channel. Execution adapters use it when activity arrives outside a
// channel context. Topic is matched by the target-id suffix.
func (h *Hub) forgetNotifyForTargetAll(targetID string) {
	if targetID == "" {
		return
	}
	h.notifyMu.Lock()
	defer h.notifyMu.Unlock()
	suffix := "|" + targetID
	for _, chState := range h.notifyState {
		for k := range chState {
			if strings.HasSuffix(k, suffix) {
				delete(chState, k)
			}
		}
	}
}

// runOutbound owns channel-scoped notifications that are unrelated to an
// execution domain. Interactive Session lifecycle events are consumed by
// the interactive execution adapter; future One-shot events are consumed by the
// One-shot adapter.
func (h *Hub) runOutbound(ctx context.Context) {
	defer close(h.outDone)
	prChecks, unsubPR := h.bus.Subscribe("pr.checks_completed", 64)
	defer unsubPR()
	backupFailed, unsubBackup := h.bus.Subscribe("backup.failed", 32)
	defer unsubBackup()
	verifyFailed, unsubVerify := h.bus.Subscribe("backup.verify_failed", 32)
	defer unsubVerify()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-prChecks:
			if !ok {
				return
			}
			h.dispatchPlatformEvent(ctx, event)
		case event, ok := <-backupFailed:
			if !ok {
				return
			}
			h.dispatchPlatformEvent(ctx, event)
		case event, ok := <-verifyFailed:
			if !ok {
				return
			}
			h.dispatchPlatformEvent(ctx, event)
		}
	}
}

func (h *Hub) dispatchPlatformEvent(ctx context.Context, ev eventbus.Event) {
	channels := h.ChannelsSnapshot()
	suppressKey := ev.Topic
	if data, ok := ev.Data.(map[string]any); ok {
		if backupID, _ := data["backup_id"].(string); backupID != "" {
			suppressKey = backupID
		} else if prURL, _ := data["pr_url"].(string); prURL != "" {
			suppressKey = prURL
		}
	}
	for _, ch := range channels {
		if ch == nil || h.ChannelMuted(ctx, ch.ID()) {
			continue
		}
		if h.SuppressNotification(ctx, ch.ID(), ev.Topic, suppressKey) {
			continue
		}
		card := buildNotificationCard(ev)
		if card == nil {
			continue
		}
		if _, err := h.Deliver(ctx, ChannelMessage{
			ChannelID: ch.ID(), Direction: DirectionOutbound, ConversationID: "default",
			Text: card.RenderText(), Timestamp: time.Now().UTC(), Metadata: map[string]any{},
		}, card); err != nil {
			h.log.Error("channel send failed", "id", ch.ID(), "err", err)
			continue
		}
		h.publishEvent(eventbus.Event{Topic: "channel.message_sent", Data: map[string]any{
			"channel_id": ch.ID(), "topic": ev.Topic,
		}})
	}
}

// sendWithFallback ships a card via CardSender when supported, else
// falls back to Channel.Send with the rendered text. Centralised so
// every outbound code path observes the same degradation rule.
//
// CardSender impls that return ErrNotSupported (e.g. a bridge whose
// adapter did not claim the "card" capability) trigger the same
// fallback as channels that don't implement CardSender at all.
func (h *Hub) sendWithFallback(ctx context.Context, c Channel, msg ChannelMessage, card *Card) error {
	if cs, ok := c.(CardSender); ok && card != nil {
		err := cs.SendCard(ctx, msg, card)
		if err == nil || !errors.Is(err, ErrNotSupported) {
			return err
		}
	}
	return c.Send(ctx, msg)
}

// CreateChannel registers a new channel and starts it if enabled.
func (h *Hub) CreateChannel(ctx context.Context, kind string, config json.RawMessage, enabled bool) (string, error) {
	if Lookup(kind) == nil {
		return "", fmt.Errorf("%w: %s", ErrUnknownKind, kind)
	}
	id := newID()
	if err := h.store.Insert(ctx, id, kind, config, enabled); err != nil {
		return "", err
	}
	if enabled && h.isStarted() {
		if err := h.spawn(ctx, channelRow{ID: id, Kind: kind, Config: config, Enabled: true}); err != nil {
			return "", err
		}
	}
	return id, nil
}

// UpdateChannel persists changes and restarts the impl when running.
// Pass nil for any unchanged field.
func (h *Hub) UpdateChannel(ctx context.Context, id string, config json.RawMessage, enabled *bool) error {
	if err := h.store.Update(ctx, id, config, enabled); err != nil {
		return err
	}
	row, err := h.store.Get(ctx, id)
	if err != nil {
		return err
	}
	h.mu.Lock()
	existing, running := h.channels[id]
	delete(h.channels, id)
	h.mu.Unlock()
	if running {
		_ = existing.Stop(ctx)
	}
	// Cooldown bookkeeping is keyed by channelID — drop it on update so
	// a freshly-lowered cooldown isn't blocked by an old timestamp.
	h.forgetNotifyState(id)
	if row.Enabled && h.isStarted() {
		return h.spawn(ctx, row)
	}
	return nil
}

// DeleteChannel stops the running impl (if any) and removes the row.
func (h *Hub) DeleteChannel(ctx context.Context, id string) error {
	h.mu.Lock()
	ch, ok := h.channels[id]
	delete(h.channels, id)
	h.mu.Unlock()
	if ok {
		_ = ch.Stop(ctx)
	}
	h.forgetNotifyState(id)
	if controller := h.interactiveTargetController(); controller != nil {
		controller.ClearChannel(id)
	}
	return h.store.Delete(ctx, id)
}

// LookupWebhook returns the WebhookHandler-implementing channel with
// the given id, or (nil, false) when the channel either does not exist
// or does not accept webhooks. Used by the public webhook route to
// route inbound HTTP POSTs to the right impl.
func (h *Hub) LookupWebhook(id string) (WebhookHandler, bool) {
	h.mu.RLock()
	c, ok := h.channels[id]
	h.mu.RUnlock()
	if !ok {
		return nil, false
	}
	wh, ok := c.(WebhookHandler)
	return wh, ok
}

// SendTest pushes a fixed text message via channel.Send.
func (h *Hub) SendTest(ctx context.Context, id string) error {
	h.mu.RLock()
	_, ok := h.channels[id]
	h.mu.RUnlock()
	if !ok {
		return ErrNotFound
	}
	_, err := h.Deliver(ctx, ChannelMessage{
		ChannelID:      id,
		Direction:      DirectionOutbound,
		ConversationID: "default",
		Text:           "OpenDray channel test ✓",
		Timestamp:      time.Now().UTC(),
	}, nil)
	return err
}

// List returns the persisted channels along with a "running" flag.
func (h *Hub) List(ctx context.Context) ([]ChannelView, error) {
	rows, err := h.store.List(ctx)
	if err != nil {
		return nil, err
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]ChannelView, 0, len(rows))
	for _, r := range rows {
		ch, running := h.channels[r.ID]
		out = append(out, viewOf(r, ch, running))
	}
	return out, nil
}

// Get returns one channel view.
func (h *Hub) Get(ctx context.Context, id string) (ChannelView, error) {
	r, err := h.store.Get(ctx, id)
	if err != nil {
		return ChannelView{}, err
	}
	h.mu.RLock()
	ch, running := h.channels[id]
	h.mu.RUnlock()
	return viewOf(r, ch, running), nil
}

// viewOf renders the public REST shape for one channel row, including
// the capability list and muted flag (both read live from the running
// impl + config JSON respectively). Channels that have not been
// instantiated yet report only the text capability.
func viewOf(r channelRow, ch Channel, running bool) ChannelView {
	caps := []Capability{CapText}
	if ch != nil {
		caps = Capabilities(ch)
	}
	muted := false
	if len(r.Config) > 0 {
		var cfg struct {
			Muted bool `json:"muted"`
		}
		_ = json.Unmarshal(r.Config, &cfg)
		muted = cfg.Muted
	}
	return ChannelView{
		ID:           r.ID,
		Kind:         r.Kind,
		Config:       r.Config,
		Enabled:      r.Enabled,
		Running:      running,
		Capabilities: caps,
		Muted:        muted,
	}
}

// ChannelView is the public wire shape for REST.
type ChannelView struct {
	ID           string          `json:"id"`
	Kind         string          `json:"kind"`
	Config       json.RawMessage `json:"config"`
	Enabled      bool            `json:"enabled"`
	Running      bool            `json:"running"`
	Capabilities []Capability    `json:"capabilities"`
	Muted        bool            `json:"muted"`
}

func (h *Hub) isStarted() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.started
}

// snippetPrefs is the per-channel preference for embedding the
// terminal tail in session.* notifications.
//
// MaxChars semantics: 0 (the default) means "no cap — channel impl
// handles platform chunking". A positive value applies a hard cap
// with the "[…]" elided-prefix marker.
type snippetPrefs struct {
	Include  bool
	MaxChars int
}

// snippetPrefsFor reads notify_include_snippet (default true) and
// notify_snippet_max_chars (default 0 = no cap → multi-message
// chunking takes over) from the channel's config JSON.
func (h *Hub) snippetPrefs(ctx context.Context, channelID string) snippetPrefs {
	out := snippetPrefs{Include: true, MaxChars: 0}
	row, err := h.store.Get(ctx, channelID)
	if err != nil {
		return out
	}
	var cfg struct {
		Include  *bool `json:"notify_include_snippet"`
		MaxChars *int  `json:"notify_snippet_max_chars"`
	}
	_ = json.Unmarshal(row.Config, &cfg)
	if cfg.Include != nil {
		out.Include = *cfg.Include
	}
	if cfg.MaxChars != nil && *cfg.MaxChars >= 0 {
		out.MaxChars = *cfg.MaxChars
	}
	return out
}

// buildNotificationCard turns channel-scoped platform events into a
// structured Card. Execution-domain event rendering lives in each domain
// adapter and never enters Channel Core.
func buildNotificationCard(ev eventbus.Event) *Card {
	data, _ := ev.Data.(map[string]any)
	switch ev.Topic {
	case "backup.failed":
		backupID, _ := data["backup_id"].(string)
		errorMessage, _ := data["error"].(string)
		return &Card{
			Header: &CardHeader{Title: "Backup failed", Color: "red"},
			Elements: []CardElement{
				CardMarkdown{Content: fmt.Sprintf(
					"Backup `%s` failed: %s", backupID, trimForCardN(errorMessage, 300))},
				CardActions{Buttons: [][]ButtonOption{{
					{Text: "Open backups", Value: "nav:/backups"},
					{Text: "Mute", Value: "cmd:/notify off"},
				}}},
			},
		}
	case "backup.verify_failed":
		backupID, _ := data["backup_id"].(string)
		errorMessage, _ := data["error"].(string)
		return &Card{
			Header: &CardHeader{Title: "Backup unverified", Color: "red"},
			Elements: []CardElement{
				CardMarkdown{Content: fmt.Sprintf(
					"Backup `%s` was written but failed verification: %s",
					backupID, trimForCardN(errorMessage, 300))},
				CardActions{Buttons: [][]ButtonOption{{
					{Text: "Open backups", Value: "nav:/backups"},
					{Text: "Mute", Value: "cmd:/notify off"},
				}}},
			},
		}
	case "pr.checks_completed":
		conclusion, _ := data["conclusion"].(string)
		number := toInt64(data["pr_number"])
		title, _ := data["pr_title"].(string)
		prURL, _ := data["pr_url"].(string)
		head, _ := data["pr_head"].(string)
		base, _ := data["pr_base"].(string)
		checks := toInt64(data["checks"])

		color, verdict := "green", "Passed"
		switch conclusion {
		case "failure":
			color, verdict = "red", "Failed"
		case "mixed":
			color, verdict = "yellow", "Mixed"
		}
		elements := []CardElement{CardMarkdown{Content: fmt.Sprintf(
			"PR #%d — *%s*  \n%s → %s · %s · %d checks",
			number, title, head, base, verdict, checks)}}
		if prURL != "" {
			elements = append(elements, CardActions{Buttons: [][]ButtonOption{{
				{Text: "Open PR", Value: "nav:" + prURL, Style: "primary"},
			}}})
		}
		return &Card{Header: &CardHeader{Title: "CI checks complete", Color: color}, Elements: elements}
	default:
		return nil
	}
}

// trimForCardN hard-caps the recent_output snippet so the card stays
// within the per-channel message limit. When trimming, keeps the
// *trailing* portion (most recent / most relevant) and prepends a
// "[…]" marker so the reader knows content was elided.
func trimForCardN(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || len(s) <= max {
		return s
	}
	tail := s[len(s)-max:]
	// Avoid splitting in the middle of a UTF-8 rune.
	for i := 0; i < 4 && len(tail) > 0; i++ {
		if tail[0]&0xC0 == 0x80 {
			tail = tail[1:]
			continue
		}
		break
	}
	return "[…]\n" + tail
}

func toInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	}
	return 0
}

func newID() string {
	var b [9]byte
	_, _ = rand.Read(b[:])
	return "ch_" + base64.RawURLEncoding.EncodeToString(b[:])
}
