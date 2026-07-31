package channeladapter

import (
	"fmt"
	"testing"
	"time"

	"github.com/opendray/opendray-v2/internal/channel"
)

func TestMemoryBindingStoreResolutionPriority(t *testing.T) {
	store := NewMemoryBindingStore(10, 0, nil)
	msg := channel.ChannelMessage{ChannelID: "telegram-1", ConversationID: "chat-1"}
	if _, ok := store.Resolve(msg); ok {
		t.Fatal("empty store unexpectedly resolved a session")
	}
	store.RecordLast("telegram-1", "chat-1", "ses-last")
	if got, _ := store.Resolve(msg); got != "ses-last" {
		t.Fatalf("last target = %q", got)
	}
	store.SetActive("telegram-1", "chat-1", "ses-active")
	if got, _ := store.Resolve(msg); got != "ses-active" {
		t.Fatalf("active target = %q", got)
	}
	store.RecordOutbound("telegram-1", "chat-1", "out-1", "ses-reply")
	msg.Metadata = map[string]any{"reply_to_outbound_msg_id": "out-1"}
	if got, _ := store.Resolve(msg); got != "ses-reply" {
		t.Fatalf("reply target = %q", got)
	}
	msg.Metadata["reply_to_outbound_msg_id"] = "unknown"
	if got, _ := store.Resolve(msg); got != "ses-active" {
		t.Fatalf("unknown reply fallback = %q", got)
	}
	store.SetActive("telegram-1", "chat-1", "")
	if got, _ := store.Resolve(msg); got != "ses-last" {
		t.Fatalf("cleared active fallback = %q", got)
	}
}

func TestMemoryBindingStoreScopesByChannelAndConversation(t *testing.T) {
	store := NewMemoryBindingStore(10, 0, nil)
	for _, value := range []struct{ channelID, conversationID, sessionID string }{
		{"telegram-a", "chat-1", "ses-a1"},
		{"telegram-a", "chat-2", "ses-a2"},
		{"telegram-b", "chat-1", "ses-b1"},
	} {
		store.RecordLast(value.channelID, value.conversationID, value.sessionID)
	}
	for _, value := range []struct{ channelID, conversationID, want string }{
		{"telegram-a", "chat-1", "ses-a1"},
		{"telegram-a", "chat-2", "ses-a2"},
		{"telegram-b", "chat-1", "ses-b1"},
	} {
		got, ok := store.Resolve(channel.ChannelMessage{ChannelID: value.channelID, ConversationID: value.conversationID})
		if !ok || got != value.want {
			t.Fatalf("resolve %s/%s = %q,%v; want %q,true", value.channelID, value.conversationID, got, ok, value.want)
		}
	}
	if _, ok := store.Resolve(channel.ChannelMessage{ChannelID: "telegram-a", ConversationID: "chat-3"}); ok {
		t.Fatal("unrelated conversation inherited a target")
	}
}

func TestMemoryBindingStoreExpiresReplyBinding(t *testing.T) {
	now := time.Unix(100, 0)
	store := NewMemoryBindingStore(10, time.Minute, func() time.Time { return now })
	store.RecordLast("ch", "conv", "ses-last")
	store.RecordOutbound("ch", "conv", "out", "ses-reply")
	msg := channel.ChannelMessage{ChannelID: "ch", ConversationID: "conv", Metadata: map[string]any{"reply_to_outbound_msg_id": "out"}}
	if got, _ := store.Resolve(msg); got != "ses-reply" {
		t.Fatalf("before expiry = %q", got)
	}
	now = now.Add(time.Minute)
	if got, _ := store.Resolve(msg); got != "ses-last" {
		t.Fatalf("at expiry = %q; want fallback", got)
	}
}

func TestMemoryBindingStoreEvictsOldestOutbound(t *testing.T) {
	now := time.Unix(100, 0)
	store := NewMemoryBindingStore(3, 0, func() time.Time { now = now.Add(time.Second); return now })
	for i := 0; i < 5; i++ {
		store.RecordOutbound("ch", "conv", fmt.Sprintf("out-%d", i), fmt.Sprintf("ses-%d", i))
	}
	for i := 0; i < 2; i++ {
		msg := channel.ChannelMessage{ChannelID: "ch", ConversationID: "conv", Metadata: map[string]any{"reply_to_outbound_msg_id": fmt.Sprintf("out-%d", i)}}
		if _, ok := store.Resolve(msg); ok {
			t.Fatalf("old binding out-%d was not evicted", i)
		}
	}
	for i := 2; i < 5; i++ {
		msg := channel.ChannelMessage{ChannelID: "ch", ConversationID: "conv", Metadata: map[string]any{"reply_to_outbound_msg_id": fmt.Sprintf("out-%d", i)}}
		if got, ok := store.Resolve(msg); !ok || got != fmt.Sprintf("ses-%d", i) {
			t.Fatalf("recent binding out-%d = %q,%v", i, got, ok)
		}
	}
}

func TestMemoryBindingStoreClearChannel(t *testing.T) {
	store := NewMemoryBindingStore(10, 0, nil)
	store.RecordLast("ch-a", "conv", "ses-a")
	store.SetActive("ch-a", "conv", "ses-a")
	store.RecordOutbound("ch-a", "conv", "out", "ses-a")
	store.RecordLast("ch-b", "conv", "ses-b")
	store.ClearChannel("ch-a")
	if _, ok := store.Resolve(channel.ChannelMessage{ChannelID: "ch-a", ConversationID: "conv", Metadata: map[string]any{"reply_to_outbound_msg_id": "out"}}); ok {
		t.Fatal("cleared channel still resolves")
	}
	if got, ok := store.Resolve(channel.ChannelMessage{ChannelID: "ch-b", ConversationID: "conv"}); !ok || got != "ses-b" {
		t.Fatalf("unrelated channel changed: %q,%v", got, ok)
	}
}
