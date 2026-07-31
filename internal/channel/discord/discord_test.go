package discord

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/opendray/opendray-v2/internal/channel"
)

type discordServer struct {
	mu    sync.Mutex
	calls map[string][]map[string]any
	srv   *httptest.Server
}

func newDiscordServer() *discordServer {
	s := &discordServer{calls: make(map[string][]map[string]any)}
	s.srv = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

func (s *discordServer) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	defer r.Body.Close()
	key := r.Method + " " + r.URL.Path
	auth := r.Header.Get("Authorization")
	var payload map[string]any
	if len(body) > 0 {
		_ = json.Unmarshal(body, &payload)
	}
	if payload == nil {
		payload = map[string]any{}
	}
	payload["_auth"] = auth
	s.mu.Lock()
	s.calls[key] = append(s.calls[key], payload)
	s.mu.Unlock()

	switch {
	case r.URL.Path == "/gateway":
		_ = json.NewEncoder(w).Encode(map[string]any{"url": "wss://test/"})
	case strings.HasPrefix(r.URL.Path, "/channels/") && strings.HasSuffix(r.URL.Path, "/messages"):
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "123", "channel_id": strings.TrimPrefix(strings.TrimSuffix(r.URL.Path, "/messages"), "/channels/")})
	case r.Method == http.MethodPatch:
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "123", "channel_id": strings.TrimPrefix(strings.TrimSuffix(r.URL.Path, "/messages"), "/channels/")})
	default:
		_ = json.NewEncoder(w).Encode(map[string]any{})
	}
}

func (s *discordServer) close() { s.srv.Close() }
func (s *discordServer) callsFor(method, path string) []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]map[string]any, len(s.calls[method+" "+path]))
	copy(out, s.calls[method+" "+path])
	return out
}

func patchAPI(t *testing.T, base string) {
	t.Helper()
	prev := apiBaseOverride
	apiBaseOverride = base
	t.Cleanup(func() { apiBaseOverride = prev })
}

func TestNew_RequiresBotToken(t *testing.T) {
	if _, err := New("ch_x", json.RawMessage(`{}`), nil); err == nil {
		t.Fatal("expected error for missing bot_token")
	}
}

func TestSend_PostsToChannel(t *testing.T) {
	srv := newDiscordServer()
	defer srv.close()
	patchAPI(t, srv.srv.URL)

	c, err := New("ch_x", json.RawMessage(`{"bot_token":"abc","channel_id":"99"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Send(context.Background(), channel.ChannelMessage{Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	got := srv.callsFor("POST", "/channels/99/messages")
	if len(got) != 1 || got[0]["content"] != "hi" {
		t.Errorf("post = %v", got)
	}
	if !strings.HasPrefix(got[0]["_auth"].(string), "Bot ") {
		t.Errorf("auth header = %v", got[0]["_auth"])
	}
}

func TestSend_HonoursReplyCtx(t *testing.T) {
	srv := newDiscordServer()
	defer srv.close()
	patchAPI(t, srv.srv.URL)

	c, _ := New("ch_x", json.RawMessage(`{"bot_token":"abc","channel_id":"99"}`), nil)
	err := c.Send(context.Background(), channel.ChannelMessage{
		Text:     "echo",
		ReplyCtx: ReplyCtx{ChannelID: "777", MessageID: "M1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := srv.callsFor("POST", "/channels/777/messages")[0]
	mr, ok := got["message_reference"].(map[string]any)
	if !ok || mr["message_id"] != "M1" {
		t.Errorf("message_reference = %v", got["message_reference"])
	}
}

func TestSendCard_UsesEmbedAndComponents(t *testing.T) {
	srv := newDiscordServer()
	defer srv.close()
	patchAPI(t, srv.srv.URL)

	c, _ := New("ch_x", json.RawMessage(`{"bot_token":"abc","channel_id":"99"}`), nil)
	cs := c.(channel.CardSender)
	card := &channel.Card{
		Header: &channel.CardHeader{Title: "Idle", Color: "yellow"},
		Elements: []channel.CardElement{
			channel.CardMarkdown{Content: "Session abc went idle."},
			channel.CardActions{Buttons: [][]channel.ButtonOption{{
				{Text: "Resume", Value: "cmd:/resume", Style: "primary"},
				{Text: "End", Value: "cmd:/cancel", Style: "danger"},
			}}},
			channel.CardNote{Text: "footer"},
		},
	}
	if err := cs.SendCard(context.Background(), channel.ChannelMessage{}, card); err != nil {
		t.Fatal(err)
	}
	got := srv.callsFor("POST", "/channels/99/messages")[0]
	embeds, _ := got["embeds"].([]any)
	if len(embeds) != 1 {
		t.Fatalf("embeds = %v", got["embeds"])
	}
	embed, _ := embeds[0].(map[string]any)
	if embed["title"] != "Idle" {
		t.Errorf("embed title = %v", embed["title"])
	}
	if embed["color"] == nil {
		t.Errorf("embed color missing")
	}
	footer, _ := embed["footer"].(map[string]any)
	if footer == nil || footer["text"] != "footer" {
		t.Errorf("footer = %v", embed["footer"])
	}
	components, _ := got["components"].([]any)
	if len(components) != 1 {
		t.Fatalf("components = %v", got["components"])
	}
	row, _ := components[0].(map[string]any)
	btns, _ := row["components"].([]any)
	if len(btns) != 2 {
		t.Fatalf("buttons = %d", len(btns))
	}
	first, _ := btns[0].(map[string]any)
	if first["custom_id"] != "cmd:/resume" {
		t.Errorf("button custom_id = %v", first["custom_id"])
	}
	if int(first["style"].(float64)) != 1 {
		t.Errorf("primary style = %v", first["style"])
	}
}

func TestUpdateMessage_PatchesMessage(t *testing.T) {
	srv := newDiscordServer()
	defer srv.close()
	patchAPI(t, srv.srv.URL)

	c, _ := New("ch_x", json.RawMessage(`{"bot_token":"abc","channel_id":"99"}`), nil)
	mu := c.(channel.MessageUpdater)
	if err := mu.UpdateMessage(context.Background(), channel.ChannelMessage{}, "M1", "edited"); err != nil {
		t.Fatal(err)
	}
	got := srv.callsFor("PATCH", "/channels/99/messages/M1")[0]
	if got["content"] != "edited" {
		t.Errorf("patch payload = %v", got)
	}
}

func TestCapabilities(t *testing.T) {
	c, _ := New("ch_x", json.RawMessage(`{"bot_token":"abc","channel_id":"99"}`), nil)
	caps := channel.Capabilities(c)
	want := map[channel.Capability]bool{
		channel.CapText:           true,
		channel.CapCard:           true,
		channel.CapButtons:        true,
		channel.CapUpdateMessage:  true,
		channel.CapReplyToMessage: true,
	}
	got := map[channel.Capability]bool{}
	for _, k := range caps {
		got[k] = true
	}
	for k := range want {
		if !got[k] {
			t.Errorf("missing %s", k)
		}
	}
}

func TestSendRecordsOutboundMessageAndConversationMetadata(t *testing.T) {
	srv := newDiscordServer()
	defer srv.close()
	patchAPI(t, srv.srv.URL)

	c, err := New("ch_x", json.RawMessage(`{"bot_token":"abc","channel_id":"99"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	metadata := map[string]any{}
	if err := c.Send(context.Background(), channel.ChannelMessage{ConversationID: "88", Text: "out", Metadata: metadata}); err != nil {
		t.Fatal(err)
	}
	if got := metadata[channel.MetaOutboundMessageID]; got != "123" {
		t.Fatalf("outbound message id=%v", got)
	}
	if got := metadata[channel.MetaOutboundConversationID]; got != "88" {
		t.Fatalf("outbound conversation id=%v", got)
	}
}

func TestResolveOutboundAddressUsesConfiguredAndReplyTargets(t *testing.T) {
	d := &Discord{id: "discord-1", cfg: config{ChannelID: "C-config"}}
	got := d.ResolveOutboundAddress(channel.ChannelMessage{ConversationID: "default"})
	if got.ConversationID != "C-config" || got.MessageID != "" {
		t.Fatalf("configured address=%+v", got)
	}
	got = d.ResolveOutboundAddress(channel.ChannelMessage{ReplyCtx: ReplyCtx{ChannelID: "C-reply", MessageID: "M-parent"}})
	if got.ConversationID != "C-reply" || got.MessageID != "M-parent" {
		t.Fatalf("reply address=%+v", got)
	}
}
