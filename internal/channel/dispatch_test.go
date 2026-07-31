package channel

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opendray/opendray-v2/internal/eventbus"
)

func TestChannelMessageReplyAddressPreservesNeutralRoutingData(t *testing.T) {
	replyCtx := struct{ NativeID int }{NativeID: 7}
	msg := ChannelMessage{
		ChannelID:       "ch_telegram",
		ConversationID:  "chat-42",
		ThreadID:        "topic-9",
		SourceMessageID: "message-100",
		Author:          "alice",
		Metadata:        map[string]any{"key": "value"},
		ReplyCtx:        replyCtx,
		Attachments: []Attachment{{
			ID: "file-1", Kind: "file", Name: "report.txt", MIMEType: "text/plain", Size: 12,
		}},
	}

	got := msg.ReplyAddress()
	if got.ChannelID != msg.ChannelID || got.ConversationID != msg.ConversationID ||
		got.ThreadID != msg.ThreadID || got.MessageID != msg.SourceMessageID || got.Author != msg.Author {
		t.Fatalf("ReplyAddress() = %+v; does not preserve stable routing fields", got)
	}
	if !reflect.DeepEqual(got.ReplyCtx, replyCtx) {
		t.Fatalf("ReplyAddress().ReplyCtx = %#v; want %#v", got.ReplyCtx, replyCtx)
	}
	if len(msg.Attachments) != 1 || msg.Attachments[0].Name != "report.txt" {
		t.Fatalf("attachments = %+v", msg.Attachments)
	}
}

func TestInboundDispatcherChainDeterministicAndStopsAfterHandled(t *testing.T) {
	chain := NewInboundDispatcherChain()
	var calls []string
	register := func(name string, priority int, status DispatchStatus) {
		t.Helper()
		err := chain.Register(name, priority, InboundDispatcherFunc(func(context.Context, InboundDispatchRequest) (InboundDispatchResult, error) {
			calls = append(calls, name)
			return InboundDispatchResult{Status: status}, nil
		}))
		if err != nil {
			t.Fatal(err)
		}
	}
	register("interactive", 1000, DispatchHandled)
	register("one-shot", 100, DispatchNotHandled)
	register("must-not-run", 2000, DispatchHandled)

	result, err := chain.Dispatch(context.Background(), InboundDispatchRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Handled() || result.Handler != "interactive" {
		t.Fatalf("result = %+v; want handled by interactive", result)
	}
	if want := []string{"one-shot", "interactive"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %v; want %v", calls, want)
	}
}

func TestInboundDispatcherChainRegistrationByNameDoesNotDuplicate(t *testing.T) {
	chain := NewInboundDispatcherChain()
	var oldCalls, newCalls atomic.Int32
	if err := chain.Register("interactive", 1000, InboundDispatcherFunc(func(context.Context, InboundDispatchRequest) (InboundDispatchResult, error) {
		oldCalls.Add(1)
		return InboundDispatchResult{Status: DispatchNotHandled}, nil
	})); err != nil {
		t.Fatal(err)
	}
	if err := chain.Register("interactive", 1000, InboundDispatcherFunc(func(context.Context, InboundDispatchRequest) (InboundDispatchResult, error) {
		newCalls.Add(1)
		return InboundDispatchResult{Status: DispatchHandled}, nil
	})); err != nil {
		t.Fatal(err)
	}

	result, err := chain.Dispatch(context.Background(), InboundDispatchRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Handled() || oldCalls.Load() != 0 || newCalls.Load() != 1 {
		t.Fatalf("result=%+v old=%d new=%d", result, oldCalls.Load(), newCalls.Load())
	}
}

func TestProcessInboundDispatcherReceivesNormalizedMessage(t *testing.T) {
	h := newTestHub(t)
	fc := &fakeChannel{id: "ch_test", kind: "telegram"}
	h.channels[fc.id] = fc
	var received InboundDispatchRequest
	h.SetInboundDispatcher(InboundDispatcherFunc(func(_ context.Context, req InboundDispatchRequest) (InboundDispatchResult, error) {
		received = req
		return InboundDispatchResult{Status: DispatchHandled}, nil
	}))
	msg := ChannelMessage{
		ChannelID: "ch_test", ConversationID: "conv", ThreadID: "thread",
		SourceMessageID: "source", Author: "alice", Text: "do work",
		Attachments: []Attachment{{Name: "a.txt"}},
	}
	if err := h.processInboundAfterPersist(context.Background(), msg, 88, chatConfig{}); err != nil {
		t.Fatal(err)
	}
	if received.PersistedMessageID != 88 || received.Channel != fc || received.Message.Text != "do work" {
		t.Fatalf("received = %+v", received)
	}
	if received.ReplyAddress.MessageID != "source" || received.ReplyAddress.ThreadID != "thread" {
		t.Fatalf("reply address = %+v", received.ReplyAddress)
	}
	if !received.Policy.ChatEnabled || !received.Policy.TypingEnabled {
		t.Fatalf("default policy = %+v; want enabled", received.Policy)
	}
}

func TestProcessInboundHandledDoesNotPublishUnroutedMessage(t *testing.T) {
	h := newTestHub(t)
	h.bus = eventbus.New(nil)
	received, unsubscribe := h.bus.Subscribe("channel.message_received", 1)
	defer unsubscribe()
	h.SetInboundDispatcher(InboundDispatcherFunc(func(context.Context, InboundDispatchRequest) (InboundDispatchResult, error) {
		return InboundDispatchResult{Status: DispatchHandled}, nil
	}))

	if err := h.processInboundAfterPersist(context.Background(), ChannelMessage{
		ChannelID: "ch_test", Text: "handled",
	}, 1, chatConfig{}); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-received:
		t.Fatalf("handled message was published as unrouted: %+v", event)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestProcessInboundNotHandledPublishesNeutralMessageEvent(t *testing.T) {
	h := newTestHub(t)
	h.bus = eventbus.New(nil)
	received, unsubscribe := h.bus.Subscribe("channel.message_received", 1)
	defer unsubscribe()
	h.SetInboundDispatcher(InboundDispatcherFunc(func(context.Context, InboundDispatchRequest) (InboundDispatchResult, error) {
		return InboundDispatchResult{Status: DispatchNotHandled}, nil
	}))

	if err := h.processInboundAfterPersist(context.Background(), ChannelMessage{
		ChannelID: "ch_test", ConversationID: "conv", Author: "alice", Text: "unrouted",
	}, 2, chatConfig{}); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-received:
		data, _ := event.Data.(map[string]any)
		if data["text"] != "unrouted" || data["conversation_id"] != "conv" {
			t.Fatalf("event data = %#v", data)
		}
	case <-time.After(time.Second):
		t.Fatal("not-handled message event was not published")
	}
}

func TestProcessInboundDispatcherErrorIsTerminal(t *testing.T) {
	h := newTestHub(t)
	h.bus = eventbus.New(nil)
	received, unsubscribe := h.bus.Subscribe("channel.message_received", 1)
	defer unsubscribe()
	boom := errors.New("partial handler failure")
	h.SetInboundDispatcher(InboundDispatcherFunc(func(context.Context, InboundDispatchRequest) (InboundDispatchResult, error) {
		return InboundDispatchResult{Status: DispatchNotHandled}, boom
	}))

	// The durable inbound is acknowledged after auditing the error so the
	// transport does not redeliver an action whose side effects are uncertain.
	if err := h.processInboundAfterPersist(context.Background(), ChannelMessage{
		ChannelID: "ch_test", Text: "must not duplicate",
	}, 3, chatConfig{}); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-received:
		t.Fatalf("failed dispatch fell through: %+v", event)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestDispatchInboundPanicAndTimeoutAreAuditedAndTerminal(t *testing.T) {
	for _, tc := range []struct {
		name      string
		dispatch  InboundDispatcher
		wantError error
	}{
		{
			name: "panic",
			dispatch: InboundDispatcherFunc(func(context.Context, InboundDispatchRequest) (InboundDispatchResult, error) {
				panic("boom")
			}),
			wantError: ErrInboundDispatchPanic,
		},
		{
			name: "timeout",
			dispatch: InboundDispatcherFunc(func(ctx context.Context, _ InboundDispatchRequest) (InboundDispatchResult, error) {
				<-ctx.Done()
				// Delay return so the Hub's timeout branch deterministically wins.
				time.Sleep(20 * time.Millisecond)
				return InboundDispatchResult{}, ctx.Err()
			}),
			wantError: ErrInboundDispatchTimeout,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHub(t)
			h.bus = eventbus.New(nil)
			events, unsubscribe := h.bus.Subscribe("channel.inbound_dispatch.*", 16)
			defer unsubscribe()
			h.SetInboundDispatchTimeout(5 * time.Millisecond)
			h.SetInboundDispatcher(tc.dispatch)

			_, err := h.dispatchInbound(context.Background(), ChannelMessage{ChannelID: "ch_test", Text: "x"}, 4, chatConfig{})
			if !errors.Is(err, tc.wantError) {
				t.Fatalf("error = %v; want %v", err, tc.wantError)
			}

			var topics []string
			deadline := time.After(200 * time.Millisecond)
			for len(topics) < 2 {
				select {
				case ev := <-events:
					topics = append(topics, ev.Topic)
				case <-deadline:
					t.Fatalf("audit topics = %v; want started and terminal event", topics)
				}
			}
			joined := strings.Join(topics, "|")
			if !strings.Contains(joined, "channel.inbound_dispatch.started") {
				t.Fatalf("audit topics = %v", topics)
			}
			terminal := "channel.inbound_dispatch.panicked"
			if tc.name == "timeout" {
				terminal = "channel.inbound_dispatch.timed_out"
			}
			if !strings.Contains(joined, terminal) {
				t.Fatalf("audit topics = %v; missing %s", topics, terminal)
			}
		})
	}
}

func TestUnknownSlashCommandCanBeClaimedByExecutionDomain(t *testing.T) {
	h := newTestHub(t)
	h.bus = eventbus.New(nil)
	fc := &fakeChannel{id: "ch_test", kind: "stub"}
	h.channels[fc.id] = fc
	var got string
	h.SetInboundDispatcher(InboundDispatcherFunc(func(_ context.Context, req InboundDispatchRequest) (InboundDispatchResult, error) {
		got = req.Message.Text
		return InboundDispatchResult{Status: DispatchHandled}, nil
	}))

	if err := h.processInboundAfterPersist(context.Background(), ChannelMessage{
		ChannelID: "ch_test", ConversationID: "conv", Text: "/run codex fix tests",
	}, 101, chatConfig{}); err != nil {
		t.Fatal(err)
	}
	if got != "/run codex fix tests" {
		t.Fatalf("dispatcher got %q", got)
	}
	if texts := fc.sentTexts(); len(texts) != 0 {
		t.Fatalf("claimed command produced Channel Core reply: %v", texts)
	}
}

func TestUnknownSlashCommandRepliesOnlyAfterDomainsDecline(t *testing.T) {
	h := newTestHub(t)
	h.bus = eventbus.New(nil)
	fc := &fakeChannel{id: "ch_test", kind: "stub"}
	h.channels[fc.id] = fc
	var calls atomic.Int32
	h.SetInboundDispatcher(InboundDispatcherFunc(func(context.Context, InboundDispatchRequest) (InboundDispatchResult, error) {
		calls.Add(1)
		return InboundDispatchResult{Status: DispatchNotHandled}, nil
	}))

	if err := h.processInboundAfterPersist(context.Background(), ChannelMessage{
		ChannelID: "ch_test", ConversationID: "conv", Text: "/unknown value",
	}, 102, chatConfig{}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("dispatcher calls=%d; want 1", calls.Load())
	}
	texts := fc.sentTexts()
	if len(texts) != 1 || texts[0] != "Unknown command /unknown — try /help" {
		t.Fatalf("reply texts=%v", texts)
	}
}

func TestRegisteredChannelCommandKeepsPrecedenceOverDomains(t *testing.T) {
	h := newTestHub(t)
	h.bus = eventbus.New(nil)
	fc := &fakeChannel{id: "ch_test", kind: "stub"}
	h.channels[fc.id] = fc
	var dispatchCalls atomic.Int32
	h.SetInboundDispatcher(InboundDispatcherFunc(func(context.Context, InboundDispatchRequest) (InboundDispatchResult, error) {
		dispatchCalls.Add(1)
		return InboundDispatchResult{Status: DispatchHandled}, nil
	}))
	h.RegisterCommand(Command{Name: "echo", Handler: func(_ context.Context, cc CommandContext) (string, error) {
		return strings.Join(cc.Args, " "), nil
	}})

	if err := h.processInboundAfterPersist(context.Background(), ChannelMessage{
		ChannelID: "ch_test", ConversationID: "conv", Text: "/echo hello",
	}, 103, chatConfig{}); err != nil {
		t.Fatal(err)
	}
	if dispatchCalls.Load() != 0 {
		t.Fatalf("registered command was offered to domains %d times", dispatchCalls.Load())
	}
	texts := fc.sentTexts()
	if len(texts) != 1 || texts[0] != "hello" {
		t.Fatalf("reply texts=%v", texts)
	}
}
