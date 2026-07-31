package channeladapter

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/opendray/opendray-v2/internal/channel"
	"github.com/opendray/opendray-v2/internal/eventbus"
)

type fakeChannel struct {
	id          string
	kind        string
	mu          sync.Mutex
	sent        []channel.ChannelMessage
	cards       []*channel.Card
	typingStart int
	typingStop  int
}

func (f *fakeChannel) ID() string                                       { return f.id }
func (f *fakeChannel) Kind() string                                     { return f.kind }
func (f *fakeChannel) Start(context.Context, channel.InboundFunc) error { return nil }
func (f *fakeChannel) Stop(context.Context) error                       { return nil }
func (f *fakeChannel) Send(_ context.Context, msg channel.ChannelMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, msg)
	return nil
}
func (f *fakeChannel) SendCard(_ context.Context, msg channel.ChannelMessage, card *channel.Card) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, msg)
	f.cards = append(f.cards, card)
	return nil
}
func (f *fakeChannel) StartTyping(context.Context, channel.ChannelMessage) func() {
	f.mu.Lock()
	f.typingStart++
	f.mu.Unlock()
	return func() {
		f.mu.Lock()
		f.typingStop++
		f.mu.Unlock()
	}
}
func (f *fakeChannel) messages() []channel.ChannelMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]channel.ChannelMessage(nil), f.sent...)
}
func (f *fakeChannel) typingCounts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.typingStart, f.typingStop
}

type fakeHost struct {
	mu         sync.Mutex
	channels   map[string]*fakeChannel
	policies   map[string]channel.AdapterChatPolicy
	muted      map[string]bool
	suppressed map[string]bool
	resolved   map[string]string
	delivered  []channel.ChannelMessage
	replies    []channel.ChannelMessage
	nextID     int
}

func newFakeHost(channels ...*fakeChannel) *fakeHost {
	h := &fakeHost{channels: map[string]*fakeChannel{}, policies: map[string]channel.AdapterChatPolicy{}, muted: map[string]bool{}, suppressed: map[string]bool{}, resolved: map[string]string{}}
	for _, ch := range channels {
		h.channels[ch.id] = ch
		h.policies[ch.id] = channel.AdapterChatPolicy{ChatEnabled: true, TypingEnabled: true, ReplyMaxChars: 4000, IncludeSnippet: true, SnippetMaxChars: 1000}
	}
	return h
}

func (h *fakeHost) ChannelByID(id string) channel.Channel {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.channels[id]
}
func (h *fakeHost) ChannelsSnapshot() []channel.Channel {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]channel.Channel, 0, len(h.channels))
	for _, ch := range h.channels {
		out = append(out, ch)
	}
	return out
}
func (h *fakeHost) AdapterChatPolicyFor(_ context.Context, id string) channel.AdapterChatPolicy {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.policies[id]
}
func (h *fakeHost) ChannelMuted(_ context.Context, id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.muted[id]
}
func (h *fakeHost) SuppressNotification(_ context.Context, id, topic, target string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.suppressed[id+"|"+topic+"|"+target]
}
func (h *fakeHost) ResetNotificationTarget(string, string) {}
func (h *fakeHost) ResetNotificationTargetAll(string)      {}
func (h *fakeHost) Deliver(_ context.Context, msg channel.ChannelMessage, card *channel.Card) (channel.ChannelMessage, error) {
	h.mu.Lock()
	h.nextID++
	if msg.Metadata == nil {
		msg.Metadata = map[string]any{}
	}
	msg.Metadata[channel.MetaOutboundMessageID] = fmt.Sprintf("out-%d", h.nextID)
	if resolved := h.resolved[msg.ChannelID]; resolved != "" {
		msg.ConversationID = resolved
	}
	msg.Metadata[channel.MetaOutboundConversationID] = msg.ConversationID
	h.delivered = append(h.delivered, msg)
	ch := h.channels[msg.ChannelID]
	h.mu.Unlock()
	if ch == nil {
		return msg, errors.New("channel not found")
	}
	if card != nil {
		_ = ch.SendCard(context.Background(), msg, card)
	} else {
		_ = ch.Send(context.Background(), msg)
	}
	return msg, nil
}
func (h *fakeHost) Reply(ctx context.Context, src channel.ChannelMessage, text string, control bool) (channel.ChannelMessage, error) {
	msg := channel.ChannelMessage{ChannelID: src.ChannelID, ConversationID: src.ConversationID, ThreadID: src.ThreadID, Text: text, ReplyCtx: src.ReplyCtx, Metadata: map[string]any{}}
	if control {
		msg.Metadata[channel.MetaControlKeyboard] = true
	}
	out, err := h.Deliver(ctx, msg, nil)
	if err == nil {
		h.mu.Lock()
		h.replies = append(h.replies, out)
		h.mu.Unlock()
	}
	return out, err
}

type fakeSessionInput struct {
	*recordingInputter
	mu       sync.Mutex
	expected []string
}

func (f *fakeSessionInput) ExpectTurn(sessionID string) {
	f.mu.Lock()
	f.expected = append(f.expected, sessionID)
	f.mu.Unlock()
}
func (f *fakeSessionInput) expectedSnapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.expected...)
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	if !condition() {
		t.Fatalf("condition not met within %s", timeout)
	}
}

func TestInteractiveHandlerNotHandledWithoutTarget(t *testing.T) {
	input := &fakeSessionInput{recordingInputter: &recordingInputter{}}
	ch := &fakeChannel{id: "ch", kind: "telegram"}
	host := newFakeHost(ch)
	adapter := New(input, host, eventbus.New(nil), nil)
	result, err := adapter.Dispatch(context.Background(), channel.InboundDispatchRequest{Channel: ch, Message: channel.ChannelMessage{ChannelID: "ch", ConversationID: "conv", Text: "hello"}, Policy: channel.InboundPolicy{ChatEnabled: true}})
	if err != nil || result.Handled() {
		t.Fatalf("result=%+v err=%v; want not handled", result, err)
	}
	if len(input.snapshot()) != 0 {
		t.Fatal("input was written without a target")
	}
}

func TestInteractiveHandlerNotHandledWhenChatDisabled(t *testing.T) {
	input := &fakeSessionInput{recordingInputter: &recordingInputter{}}
	ch := &fakeChannel{id: "ch", kind: "telegram"}
	host := newFakeHost(ch)
	adapter := New(input, host, eventbus.New(nil), nil)
	adapter.SetActiveTarget("ch", "conv", "ses")
	result, err := adapter.Dispatch(context.Background(), channel.InboundDispatchRequest{Channel: ch, Message: channel.ChannelMessage{ChannelID: "ch", ConversationID: "conv", Text: "hello"}, Policy: channel.InboundPolicy{ChatEnabled: false}})
	if err != nil || result.Handled() {
		t.Fatalf("result=%+v err=%v; want not handled", result, err)
	}
}

func TestInteractiveHandlerForwardsAndTracksTurn(t *testing.T) {
	input := &fakeSessionInput{recordingInputter: &recordingInputter{}}
	ch := &fakeChannel{id: "ch", kind: "telegram"}
	host := newFakeHost(ch)
	adapter := New(input, host, eventbus.New(nil), nil)
	adapter.queue.submitter = newInputSubmitterForTest(input, 0, 0)
	adapter.SetActiveTarget("ch", "conv", "ses")
	result, err := adapter.Dispatch(context.Background(), channel.InboundDispatchRequest{Channel: ch, PersistedMessageID: 9, Message: channel.ChannelMessage{ChannelID: "ch", ConversationID: "conv", Text: "hi"}, Policy: channel.InboundPolicy{ChatEnabled: true, TypingEnabled: true}})
	if err != nil || !result.Handled() {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	waitFor(t, time.Second, func() bool {
		starts, _ := ch.typingCounts()
		return len(input.snapshot()) == 3 && len(input.expectedSnapshot()) == 1 && starts == 1
	})
	calls := input.snapshot()
	if len(calls) != 3 || string(calls[0].data) != "h" || string(calls[1].data) != "i" || string(calls[2].data) != "\r" {
		t.Fatalf("input calls=%#v", calls)
	}
	expected := input.expectedSnapshot()
	if len(expected) != 1 || expected[0] != "ses" {
		t.Fatalf("expected turns=%v", expected)
	}
	starts, _ := ch.typingCounts()
	if starts != 1 {
		t.Fatalf("typing starts=%d; want 1", starts)
	}
}

func TestInteractiveHandlerClaimsPartialFailure(t *testing.T) {
	boom := errors.New("pty write failed")
	input := &fakeSessionInput{recordingInputter: &recordingInputter{failAt: 1, err: boom}}
	ch := &fakeChannel{id: "ch", kind: "telegram"}
	host := newFakeHost(ch)
	adapter := New(input, host, eventbus.New(nil), nil)
	adapter.queue.submitter = newInputSubmitterForTest(input, 0, 0)
	adapter.SetActiveTarget("ch", "conv", "ses")
	result, err := adapter.Dispatch(context.Background(), channel.InboundDispatchRequest{Channel: ch, Message: channel.ChannelMessage{ChannelID: "ch", ConversationID: "conv", Text: "abc"}, Policy: channel.InboundPolicy{ChatEnabled: true}})
	if err != nil || !result.Handled() {
		t.Fatalf("result=%+v err=%v; partial delivery must be claimed", result, err)
	}
	waitFor(t, time.Second, func() bool {
		host.mu.Lock()
		defer host.mu.Unlock()
		return len(host.replies) == 1
	})
	calls := input.snapshot()
	if len(calls) != 1 || string(calls[0].data) != "a" {
		t.Fatalf("calls=%#v; want only a", calls)
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	if len(host.replies) != 1 {
		t.Fatalf("error replies=%d; want 1", len(host.replies))
	}
}

func TestAdapterTargetControllerIsConversationScoped(t *testing.T) {
	adapter := New(nil, newFakeHost(), nil, nil)
	adapter.SetActiveTarget("ch", "conv-a", "ses-a")
	adapter.SetActiveTarget("ch", "conv-b", "ses-b")
	if got := adapter.ActiveTarget("ch", "conv-a"); got != "ses-a" {
		t.Fatalf("conv-a target=%q", got)
	}
	if got := adapter.ActiveTarget("ch", "conv-b"); got != "ses-b" {
		t.Fatalf("conv-b target=%q", got)
	}
}

func TestReplyTrackerTimeoutKeepsPendingForLateTurn(t *testing.T) {
	input := &fakeSessionInput{recordingInputter: &recordingInputter{}}
	ch := &fakeChannel{id: "ch", kind: "telegram"}
	host := newFakeHost(ch)
	bindings := NewMemoryBindingStore(10, 0, nil)
	tracker := NewReplyTracker(host, bindings, input, nil)
	tracker.typingCap = 5 * time.Millisecond
	source := channel.ChannelMessage{ChannelID: "ch", ConversationID: "conv", Text: "hello"}
	tracker.Begin(ch, source, "ses", true)
	time.Sleep(20 * time.Millisecond)
	tracker.DeliverTurn(context.Background(), eventbus.Event{Topic: "session.turn_completed", Data: map[string]any{"session_id": "ses", "recent_output": "late reply"}})
	messages := ch.messages()
	if len(messages) != 2 {
		t.Fatalf("messages=%d; want timeout note and late reply", len(messages))
	}
	_, stops := ch.typingCounts()
	if stops != 1 {
		t.Fatalf("typing stops=%d; want 1", stops)
	}
}

func TestInteractiveHandlerRejectsSlashCommands(t *testing.T) {
	input := &fakeSessionInput{recordingInputter: &recordingInputter{}}
	ch := &fakeChannel{id: "ch", kind: "telegram"}
	host := newFakeHost(ch)
	adapter := New(input, host, eventbus.New(nil), nil)
	adapter.SetActiveTarget("ch", "conv", "ses")
	result, err := adapter.Dispatch(context.Background(), channel.InboundDispatchRequest{
		Channel: ch,
		Message: channel.ChannelMessage{ChannelID: "ch", ConversationID: "conv", Text: "/run codex fix tests"},
		Policy:  channel.InboundPolicy{ChatEnabled: true},
	})
	if err != nil || result.Handled() {
		t.Fatalf("result=%+v err=%v; slash commands must remain available to explicit domain handlers", result, err)
	}
	if len(input.snapshot()) != 0 {
		t.Fatal("slash command was typed into the PTY")
	}
}

func TestInteractiveHandlerLongInputOutlivesDispatchDeadline(t *testing.T) {
	input := &fakeSessionInput{recordingInputter: &recordingInputter{}}
	ch := &fakeChannel{id: "ch", kind: "telegram"}
	host := newFakeHost(ch)
	adapter := New(input, host, eventbus.New(nil), nil)
	adapter.queue.submitter = newInputSubmitterForTest(input, time.Millisecond, 0)
	adapter.SetActiveTarget("ch", "conv", "ses")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	text := strings.Repeat("x", 100)
	result, err := adapter.Dispatch(ctx, channel.InboundDispatchRequest{
		Channel: ch,
		Message: channel.ChannelMessage{ChannelID: "ch", ConversationID: "conv", Text: text},
		Policy:  channel.InboundPolicy{ChatEnabled: true},
	})
	if err != nil || !result.Handled() {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	<-ctx.Done()
	waitFor(t, time.Second, func() bool { return len(input.snapshot()) == len(text)+1 })
	calls := input.snapshot()
	if got := string(calls[len(calls)-1].data); got != "\r" {
		t.Fatalf("last write=%q; want carriage return", got)
	}
}

func TestInputQueueSerializesMessagesPerSession(t *testing.T) {
	input := &fakeSessionInput{recordingInputter: &recordingInputter{}}
	ch := &fakeChannel{id: "ch", kind: "telegram"}
	host := newFakeHost(ch)
	adapter := New(input, host, eventbus.New(nil), nil)
	adapter.queue.submitter = newInputSubmitterForTest(input, 0, 0)
	adapter.SetActiveTarget("ch", "conv", "ses")
	for _, text := range []string{"ab", "cd"} {
		result, err := adapter.Dispatch(context.Background(), channel.InboundDispatchRequest{
			Channel: ch,
			Message: channel.ChannelMessage{ChannelID: "ch", ConversationID: "conv", Text: text},
			Policy:  channel.InboundPolicy{ChatEnabled: true},
		})
		if err != nil || !result.Handled() {
			t.Fatalf("text=%q result=%+v err=%v", text, result, err)
		}
	}
	waitFor(t, time.Second, func() bool { return len(input.snapshot()) == 6 })
	calls := input.snapshot()
	var got strings.Builder
	for _, call := range calls {
		got.Write(call.data)
	}
	if got.String() != "ab\rcd\r" {
		t.Fatalf("serialized writes=%q; want %q", got.String(), "ab\rcd\r")
	}
}

func TestInteractiveHandlerFiveThousandRuneInputIsComplete(t *testing.T) {
	input := &fakeSessionInput{recordingInputter: &recordingInputter{}}
	ch := &fakeChannel{id: "ch", kind: "telegram"}
	host := newFakeHost(ch)
	adapter := New(input, host, eventbus.New(nil), nil)
	adapter.queue.submitter = newInputSubmitterForTest(input, 0, 0)
	adapter.SetActiveTarget("ch", "conv", "ses")

	text := strings.Repeat("界", 5000)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	result, err := adapter.Dispatch(ctx, channel.InboundDispatchRequest{
		Channel: ch,
		Message: channel.ChannelMessage{ChannelID: "ch", ConversationID: "conv", Text: text},
		Policy:  channel.InboundPolicy{ChatEnabled: true},
	})
	if err != nil || !result.Handled() {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	waitFor(t, 3*time.Second, func() bool { return len(input.snapshot()) == 5001 })
	calls := input.snapshot()
	var got strings.Builder
	for _, call := range calls {
		got.Write(call.data)
	}
	if got.String() != text+"\r" {
		t.Fatalf("complete write length=%d; want %d", len([]rune(got.String())), 5001)
	}
}

func TestInputQueueSerializesConsecutiveLongMessages(t *testing.T) {
	input := &fakeSessionInput{recordingInputter: &recordingInputter{}}
	ch := &fakeChannel{id: "ch", kind: "telegram"}
	host := newFakeHost(ch)
	adapter := New(input, host, eventbus.New(nil), nil)
	adapter.queue.submitter = newInputSubmitterForTest(input, 0, 0)
	adapter.SetActiveTarget("ch", "conv", "ses")

	first := strings.Repeat("a", 1500)
	second := strings.Repeat("b", 1500)
	for _, text := range []string{first, second} {
		result, err := adapter.Dispatch(context.Background(), channel.InboundDispatchRequest{
			Channel: ch,
			Message: channel.ChannelMessage{ChannelID: "ch", ConversationID: "conv", Text: text},
			Policy:  channel.InboundPolicy{ChatEnabled: true},
		})
		if err != nil || !result.Handled() {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	}
	waitFor(t, 3*time.Second, func() bool { return len(input.snapshot()) == 3002 })
	calls := input.snapshot()
	var got strings.Builder
	for _, call := range calls {
		got.Write(call.data)
	}
	if got.String() != first+"\r"+second+"\r" {
		t.Fatal("long queued messages interleaved or were truncated")
	}
}
