package wecom

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

type wcServer struct {
	mu    sync.Mutex
	calls []map[string]any
	srv   *httptest.Server
}

func newServer() *wcServer {
	s := &wcServer{}
	s.srv = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

func (s *wcServer) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	defer r.Body.Close()
	var payload map[string]any
	if len(body) > 0 {
		_ = json.Unmarshal(body, &payload)
	}
	if payload == nil {
		payload = map[string]any{}
	}
	payload["_path"] = r.URL.Path + "?" + r.URL.RawQuery
	s.mu.Lock()
	s.calls = append(s.calls, payload)
	s.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]any{"errcode": 0, "errmsg": "ok"})
}

func (s *wcServer) close() { s.srv.Close() }
func (s *wcServer) all() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]map[string]any, len(s.calls))
	copy(out, s.calls)
	return out
}

func patchAPI(t *testing.T, base string) {
	t.Helper()
	prev := apiBaseOverride
	apiBaseOverride = base
	t.Cleanup(func() { apiBaseOverride = prev })
}

func TestNew_RequiresKeyOrURL(t *testing.T) {
	if _, err := New("ch_x", json.RawMessage(`{}`), nil); err == nil {
		t.Fatal("expected error for missing webhook")
	}
}

func TestSend_TextWithKey(t *testing.T) {
	srv := newServer()
	defer srv.close()
	patchAPI(t, srv.srv.URL)

	cfg, _ := json.Marshal(config{WebhookKey: "abcdef"})
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
	if !strings.Contains(got[0]["_path"].(string), "key=abcdef") {
		t.Errorf("query missing key: %v", got[0]["_path"])
	}
}

func TestSendCard_Markdown(t *testing.T) {
	srv := newServer()
	defer srv.close()
	patchAPI(t, srv.srv.URL)

	cfg, _ := json.Marshal(config{WebhookKey: "k"})
	c, _ := New("ch_x", cfg, nil)
	cs := c.(channel.CardSender)
	card := &channel.Card{
		Header: &channel.CardHeader{Title: "Idle"},
		Elements: []channel.CardElement{
			channel.CardMarkdown{Content: "Session abc went idle."},
			channel.CardActions{Buttons: [][]channel.ButtonOption{{
				{Text: "Open", Value: "https://example.com/x"},
				{Text: "Cancel", Value: "cmd:/cancel"},
			}}},
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
	body := md["content"].(string)
	for _, want := range []string{"**Idle**", "went idle", "[Open](https://example.com/x)"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q in:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Cancel") {
		t.Errorf("cmd: button should be dropped from output:\n%s", body)
	}
}
