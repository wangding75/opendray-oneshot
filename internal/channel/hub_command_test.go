package channel

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// fakeChannel is a CardSender + ButtonSender that records every call.
// Used to drive Hub command-dispatch tests without a real platform.
type fakeChannel struct {
	id, kind string
	mu       sync.Mutex
	sent     []ChannelMessage
	cards    []*Card
}

func (f *fakeChannel) ID() string                                   { return f.id }
func (f *fakeChannel) Kind() string                                 { return f.kind }
func (f *fakeChannel) Start(_ context.Context, _ InboundFunc) error { return nil }
func (f *fakeChannel) Stop(_ context.Context) error                 { return nil }
func (f *fakeChannel) Send(_ context.Context, msg ChannelMessage) error {
	f.mu.Lock()
	f.sent = append(f.sent, msg)
	f.mu.Unlock()
	return nil
}
func (f *fakeChannel) SendCard(_ context.Context, msg ChannelMessage, card *Card) error {
	f.mu.Lock()
	f.sent = append(f.sent, msg)
	f.cards = append(f.cards, card)
	f.mu.Unlock()
	return nil
}

func (f *fakeChannel) sentTexts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.sent))
	for i, m := range f.sent {
		out[i] = m.Text
	}
	return out
}

func TestHub_HandleCommand_DispatchesAndReplies(t *testing.T) {
	h := newTestHub(t)
	fc := &fakeChannel{id: "ch_test", kind: "stub"}
	h.mu.Lock()
	h.channels[fc.id] = fc
	h.mu.Unlock()

	called := false
	h.RegisterCommand(Command{
		Name: "echo",
		Handler: func(_ context.Context, cc CommandContext) (string, error) {
			called = true
			if cc.Channel.ID() != "ch_test" {
				t.Errorf("handler got channel %q", cc.Channel.ID())
			}
			return "echo:" + cc.Args[0], nil
		},
	})

	// Use direct call to avoid needing a DB. Persist path is exercised
	// in integration tests.
	h.dispatchCommandForTest(t, ChannelMessage{
		ChannelID: "ch_test",
		Text:      "/echo hi",
	}, "echo", []string{"hi"})

	if !called {
		t.Fatal("handler not invoked")
	}
	texts := fc.sentTexts()
	if len(texts) != 1 || texts[0] != "echo:hi" {
		t.Errorf("reply texts = %v, want [echo:hi]", texts)
	}
}

func TestHub_HandleCommand_UnknownReplies(t *testing.T) {
	h := newTestHub(t)
	fc := &fakeChannel{id: "ch_test", kind: "stub"}
	h.mu.Lock()
	h.channels[fc.id] = fc
	h.mu.Unlock()

	h.dispatchCommandForTest(t, ChannelMessage{
		ChannelID: "ch_test", Text: "/nope",
	}, "nope", nil)
	texts := fc.sentTexts()
	if len(texts) != 1 || texts[0] != "Unknown command /nope — try /help" {
		t.Errorf("reply texts = %v", texts)
	}
}

func TestHub_HandleCommand_DocksControlKeyboard(t *testing.T) {
	h := newTestHub(t)
	fc := &fakeChannel{id: "ch_test", kind: "stub"}
	h.mu.Lock()
	h.channels[fc.id] = fc
	h.mu.Unlock()

	reply := func(_ context.Context, _ CommandContext) (string, error) { return "ok", nil }
	h.RegisterCommand(Command{Name: "dock", Handler: reply, DocksControlKeyboard: true})
	h.RegisterCommand(Command{Name: "plain", Handler: reply})

	h.dispatchCommandForTest(t, ChannelMessage{ChannelID: "ch_test", Text: "/dock"}, "dock", nil)
	h.dispatchCommandForTest(t, ChannelMessage{ChannelID: "ch_test", Text: "/plain"}, "plain", nil)

	fc.mu.Lock()
	defer fc.mu.Unlock()
	if len(fc.sent) != 2 {
		t.Fatalf("want 2 sends, got %d", len(fc.sent))
	}
	if v, _ := fc.sent[0].Metadata[MetaControlKeyboard].(bool); !v {
		t.Error("DocksControlKeyboard command reply should carry MetaControlKeyboard")
	}
	if _, has := fc.sent[1].Metadata[MetaControlKeyboard]; has {
		t.Error("plain command reply must not carry MetaControlKeyboard")
	}
}

// dispatchCommandForTest runs handleCommand without touching the DB.
// Tests insert channels directly into Hub.channels via the lock above
// and then exercise the lookup → handler → reply path.
func (h *Hub) dispatchCommandForTest(t *testing.T, msg ChannelMessage, name string, args []string) {
	t.Helper()
	h.mu.RLock()
	ch := h.channels[msg.ChannelID]
	h.mu.RUnlock()
	if ch == nil {
		t.Fatalf("channel %s not registered", msg.ChannelID)
	}
	cmd, ok := h.cmds.Lookup(name)
	if !ok {
		// Mimic handleCommand's unknown-reply branch.
		h.replyText(context.Background(), ch, msg, "Unknown command /"+name+" — try /help")
		return
	}
	cc := CommandContext{Channel: ch, Message: msg, Hub: h, Command: name, Args: args, Raw: msg.Text}
	reply, err := cmd.Handler(context.Background(), cc)
	if err != nil {
		t.Fatalf("handler err: %v", err)
	}
	if reply != "" {
		// Mirror handleCommand's DocksControlKeyboard branch.
		if cmd.DocksControlKeyboard {
			h.replyControlText(context.Background(), ch, msg, reply)
		} else {
			h.replyText(context.Background(), ch, msg, reply)
		}
	}
}

// newTestHub returns a Hub with no DB pool; tests must skip code paths
// that touch the store. Used only for command-dispatch logic.
func newTestHub(t *testing.T) *Hub {
	t.Helper()
	return &Hub{
		log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		cmds:        NewCommandRegistry(),
		channels:    make(map[string]Channel),
		notifyState: make(map[string]map[string]time.Time),
	}
}

type routedFakeChannel struct {
	fakeChannel
	conversationID string
	threadID       string
}

func (f *routedFakeChannel) ResolveOutboundAddress(msg ChannelMessage) ReplyAddress {
	return ReplyAddress{
		ChannelID:      f.ID(),
		ConversationID: f.conversationID,
		ThreadID:       f.threadID,
		ReplyCtx:       msg.ReplyCtx,
	}
}

func TestHubDeliverPersistsResolvedOutboundConversation(t *testing.T) {
	h := newTestHub(t)
	fc := &routedFakeChannel{
		fakeChannel:    fakeChannel{id: "ch_test", kind: "slack"},
		conversationID: "C-real",
		threadID:       "T-real",
	}
	h.channels[fc.ID()] = fc

	out, err := h.Deliver(context.Background(), ChannelMessage{
		ChannelID: fc.ID(), ConversationID: "default", Text: "notification",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.ConversationID != "C-real" || out.ThreadID != "T-real" {
		t.Fatalf("resolved address=%s/%s", out.ConversationID, out.ThreadID)
	}
	if got := out.Metadata[MetaOutboundConversationID]; got != "C-real" {
		t.Fatalf("outbound conversation metadata=%v", got)
	}
	if got := out.Metadata[MetaOutboundThreadID]; got != "T-real" {
		t.Fatalf("outbound thread metadata=%v", got)
	}
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if len(fc.sent) != 1 || fc.sent[0].ConversationID != "C-real" {
		t.Fatalf("sent=%+v", fc.sent)
	}
}

type fakeOutboundDelivery struct {
	calls int
	last  ChannelMessage
}

func (d *fakeOutboundDelivery) Deliver(_ context.Context, msg ChannelMessage, _ *Card) (ChannelMessage, error) {
	d.calls++
	d.last = msg
	msg.ConversationID = "delivered-conversation"
	if msg.Metadata == nil {
		msg.Metadata = map[string]any{}
	}
	msg.Metadata["delivery_id"] = "delivery-1"
	return msg, nil
}

func TestHubDeliverDelegatesSharedOutboundService(t *testing.T) {
	h := newTestHub(t)
	service := &fakeOutboundDelivery{}
	h.SetOutboundDelivery(service)

	out, err := h.Deliver(context.Background(), ChannelMessage{ChannelID: "ch", Text: "hello"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if service.calls != 1 || service.last.Text != "hello" {
		t.Fatalf("service calls=%d last=%+v", service.calls, service.last)
	}
	if out.ConversationID != "delivered-conversation" || out.Metadata["delivery_id"] != "delivery-1" {
		t.Fatalf("out=%+v", out)
	}
}
