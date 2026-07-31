package dingtalk

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/opendray/opendray-v2/internal/channel"
)

type dtServer struct {
	mu    sync.Mutex
	calls []map[string]any
	srv   *httptest.Server
}

func newDtServer() *dtServer {
	s := &dtServer{}
	s.srv = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

func (s *dtServer) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	defer r.Body.Close()
	var payload map[string]any
	if len(body) > 0 {
		_ = json.Unmarshal(body, &payload)
	}
	if payload == nil {
		payload = map[string]any{}
	}
	payload["_query"] = r.URL.RawQuery
	s.mu.Lock()
	s.calls = append(s.calls, payload)
	s.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]any{"errcode": 0, "errmsg": "ok"})
}

func (s *dtServer) close() { s.srv.Close() }
func (s *dtServer) all() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]map[string]any, len(s.calls))
	copy(out, s.calls)
	return out
}

func TestNew_RequiresWebhook(t *testing.T) {
	if _, err := New("ch_x", json.RawMessage(`{}`), nil); err == nil {
		t.Fatal("expected error for missing webhook_url")
	}
}

func TestSend_Text(t *testing.T) {
	srv := newDtServer()
	defer srv.close()
	cfg, _ := json.Marshal(config{WebhookURL: srv.srv.URL})
	c, err := New("ch_x", cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Send(context.Background(), channel.ChannelMessage{Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	got := srv.all()
	if len(got) != 1 || got[0]["msgtype"] != "text" {
		t.Fatalf("payload = %v", got)
	}
	text, _ := got[0]["text"].(map[string]any)
	if text == nil || text["content"] != "hi" {
		t.Errorf("text = %v", got[0]["text"])
	}
}

func TestSend_Signed(t *testing.T) {
	srv := newDtServer()
	defer srv.close()
	cfg, _ := json.Marshal(config{WebhookURL: srv.srv.URL, Secret: "S"})
	c, _ := New("ch_x", cfg, nil)
	if err := c.Send(context.Background(), channel.ChannelMessage{Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	got := srv.all()[0]
	q, _ := url.ParseQuery(got["_query"].(string))
	if q.Get("timestamp") == "" || q.Get("sign") == "" {
		t.Errorf("missing signature: %v", got["_query"])
	}
}

func TestSendCard_MarkdownNoButtons(t *testing.T) {
	srv := newDtServer()
	defer srv.close()
	cfg, _ := json.Marshal(config{WebhookURL: srv.srv.URL})
	c, _ := New("ch_x", cfg, nil)
	cs := c.(channel.CardSender)
	card := &channel.Card{
		Header: &channel.CardHeader{Title: "Idle"},
		Elements: []channel.CardElement{
			channel.CardMarkdown{Content: "Session abc went idle."},
		},
	}
	if err := cs.SendCard(context.Background(), channel.ChannelMessage{}, card); err != nil {
		t.Fatal(err)
	}
	got := srv.all()[0]
	if got["msgtype"] != "markdown" {
		t.Fatalf("msgtype = %v", got["msgtype"])
	}
	md, _ := got["markdown"].(map[string]any)
	if md["title"] != "Idle" {
		t.Errorf("title = %v", md["title"])
	}
	if !strings.Contains(md["text"].(string), "went idle") {
		t.Errorf("text = %v", md["text"])
	}
}

func TestSendCard_ActionCardWithNavButtons(t *testing.T) {
	srv := newDtServer()
	defer srv.close()
	cfg, _ := json.Marshal(config{WebhookURL: srv.srv.URL})
	c, _ := New("ch_x", cfg, nil)
	cs := c.(channel.CardSender)
	card := &channel.Card{
		Header: &channel.CardHeader{Title: "Done"},
		Elements: []channel.CardElement{
			channel.CardMarkdown{Content: "Run finished."},
			channel.CardActions{Buttons: [][]channel.ButtonOption{{
				{Text: "Open log", Value: "https://example.com/log"},
				{Text: "Cancel", Value: "cmd:/cancel"},
			}}},
		},
	}
	if err := cs.SendCard(context.Background(), channel.ChannelMessage{}, card); err != nil {
		t.Fatal(err)
	}
	got := srv.all()[0]
	if got["msgtype"] != "actionCard" {
		t.Fatalf("msgtype = %v", got["msgtype"])
	}
	ac, _ := got["actionCard"].(map[string]any)
	btns, _ := ac["btns"].([]any)
	if len(btns) != 1 {
		t.Fatalf("btns = %v (cmd:* should be dropped)", btns)
	}
	first, _ := btns[0].(map[string]any)
	if first["title"] != "Open log" || first["actionURL"] != "https://example.com/log" {
		t.Errorf("button = %v", first)
	}
}

func TestSignedURL_AppendsParams(t *testing.T) {
	now := time.Unix(1700000000, 0)
	got := signedURL("https://oapi.dingtalk.com/robot/send?access_token=ABC", "S", now)
	if !strings.Contains(got, "access_token=ABC") {
		t.Error("preserves existing query")
	}
	if !strings.Contains(got, "timestamp=1700000000000") {
		t.Errorf("ts missing: %s", got)
	}
	if !strings.Contains(got, "sign=") {
		t.Errorf("sign missing: %s", got)
	}
}
