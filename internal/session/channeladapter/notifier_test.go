package channeladapter

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/opendray/opendray-v2/internal/channel"
	"github.com/opendray/opendray-v2/internal/eventbus"
)

func TestSessionIdleCardGolden(t *testing.T) {
	card := buildSessionNotificationCard(eventbus.Event{Topic: "session.idle", Data: map[string]any{"session_id": "ses-1", "idle_for_ms": int64(12000), "recent_output": "last output"}}, channel.AdapterChatPolicy{IncludeSnippet: true, SnippetMaxChars: 100})
	if card == nil {
		t.Fatal("card is nil")
	}
	want, err := os.ReadFile("testdata/session_idle_card.txt")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(card.RenderText())
	if got != strings.TrimSpace(string(want)) {
		t.Fatalf("card mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestSessionNotifierSkipsIntegrationOrigin(t *testing.T) {
	ch := &fakeChannel{id: "ch", kind: "telegram"}
	host := newFakeHost(ch)
	bindings := NewMemoryBindingStore(10, 0, nil)
	tracker := NewReplyTracker(host, bindings, nil, nil)
	notifier := NewSessionNotifier(host, bindings, tracker, eventbus.New(nil), nil)
	notifier.dispatch(context.Background(), eventbus.Event{Topic: "session.idle", Data: map[string]any{"session_id": "ses", "origin": "integration", "recent_output": "private"}})
	if len(ch.messages()) != 0 {
		t.Fatal("integration-origin session notification was delivered")
	}
}

func TestSessionNotifierRecordsConversationAndReplyBinding(t *testing.T) {
	ch := &fakeChannel{id: "ch", kind: "telegram"}
	host := newFakeHost(ch)
	bindings := NewMemoryBindingStore(10, 0, nil)
	tracker := NewReplyTracker(host, bindings, nil, nil)
	notifier := NewSessionNotifier(host, bindings, tracker, eventbus.New(nil), nil)
	notifier.dispatch(context.Background(), eventbus.Event{Topic: "session.ended", Data: map[string]any{"session_id": "ses", "exit_code": int64(0)}})
	messages := ch.messages()
	if len(messages) != 1 {
		t.Fatalf("messages=%d; want 1", len(messages))
	}
	msg := channel.ChannelMessage{ChannelID: "ch", ConversationID: "default", Metadata: map[string]any{"reply_to_outbound_msg_id": "out-1"}}
	if got, ok := bindings.Resolve(msg); !ok || got != "ses" {
		t.Fatalf("reply binding=%q,%v; want ses,true", got, ok)
	}
}

func TestSessionNotifierUsesResolvedNonTelegramConversationBinding(t *testing.T) {
	ch := &fakeChannel{id: "slack", kind: "slack"}
	host := newFakeHost(ch)
	host.resolved[ch.id] = "C-real"
	bindings := NewMemoryBindingStore(10, 0, nil)
	notifier := NewSessionNotifier(host, bindings, NewReplyTracker(host, bindings, nil, nil), eventbus.New(nil), nil)
	notifier.dispatch(context.Background(), eventbus.Event{Topic: "session.ended", Data: map[string]any{"session_id": "ses", "exit_code": int64(0)}})

	if got, ok := bindings.Resolve(channel.ChannelMessage{ChannelID: ch.id, ConversationID: "C-real"}); !ok || got != "ses" {
		t.Fatalf("conversation binding=%q,%v; want ses,true", got, ok)
	}
	if got, ok := bindings.Resolve(channel.ChannelMessage{
		ChannelID: ch.id, ConversationID: "C-real",
		Metadata: map[string]any{"reply_to_outbound_msg_id": "out-1"},
	}); !ok || got != "ses" {
		t.Fatalf("reply binding=%q,%v; want ses,true", got, ok)
	}
}

func TestSessionNotifierRespectsMuteAndSuppression(t *testing.T) {
	muted := &fakeChannel{id: "muted", kind: "telegram"}
	suppressed := &fakeChannel{id: "suppressed", kind: "telegram"}
	open := &fakeChannel{id: "open", kind: "telegram"}
	host := newFakeHost(muted, suppressed, open)
	host.muted["muted"] = true
	host.suppressed["suppressed|session.ended|ses"] = true
	bindings := NewMemoryBindingStore(10, 0, nil)
	notifier := NewSessionNotifier(host, bindings, NewReplyTracker(host, bindings, nil, nil), eventbus.New(nil), nil)
	notifier.dispatch(context.Background(), eventbus.Event{Topic: "session.ended", Data: map[string]any{"session_id": "ses", "exit_code": int64(1)}})
	if len(muted.messages()) != 0 || len(suppressed.messages()) != 0 || len(open.messages()) != 1 {
		t.Fatalf("muted=%d suppressed=%d open=%d", len(muted.messages()), len(suppressed.messages()), len(open.messages()))
	}
}

func TestOriginAndSessionIDExtractionAreDefensive(t *testing.T) {
	for _, tc := range []struct {
		data       any
		wantOrigin string
		wantID     string
	}{
		{map[string]any{"origin": "integration", "session_id": "ses"}, "integration", "ses"},
		{map[string]any{"session_id": "ses"}, "", "ses"},
		{"not-a-map", "", ""},
		{nil, "", ""},
	} {
		ev := eventbus.Event{Data: tc.data}
		if got := originFromEvent(ev); got != tc.wantOrigin {
			t.Fatalf("origin=%q; want %q", got, tc.wantOrigin)
		}
		if got := sessionIDFromEvent(ev); got != tc.wantID {
			t.Fatalf("session=%q; want %q", got, tc.wantID)
		}
	}
}

func TestSessionNotifierAddsStableDeliveryIdempotencyKey(t *testing.T) {
	ch := &fakeChannel{id: "ch", kind: "telegram"}
	host := newFakeHost(ch)
	bindings := NewMemoryBindingStore(10, 0, nil)
	notifier := NewSessionNotifier(host, bindings, NewReplyTracker(host, bindings, nil, nil), eventbus.New(nil), nil)
	event := eventbus.Event{
		Topic: "session.ended",
		Time:  time.Unix(123, 456).UTC(),
		Data:  map[string]any{"session_id": "ses", "exit_code": int64(0)},
	}
	notifier.dispatch(context.Background(), event)

	host.mu.Lock()
	defer host.mu.Unlock()
	if len(host.delivered) != 1 {
		t.Fatalf("deliveries=%d; want 1", len(host.delivered))
	}
	got, _ := host.delivered[0].Metadata[deliveryIdempotencyMetadataKey].(string)
	want := eventDeliveryKey("session-notification", ch.ID(), "ses", event)
	if got != want {
		t.Fatalf("idempotency key=%q; want %q", got, want)
	}
}
