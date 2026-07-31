package wechat

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

type wxServer struct {
	mu    sync.Mutex
	calls []map[string]any
	srv   *httptest.Server
	code  int // wxpusher response code (default 1000)
}

func newServer() *wxServer {
	s := &wxServer{code: 1000}
	s.srv = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

func (s *wxServer) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	defer r.Body.Close()
	var payload map[string]any
	_ = json.Unmarshal(body, &payload)
	s.mu.Lock()
	s.calls = append(s.calls, payload)
	code := s.code
	s.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "msg": "ok"})
}

func (s *wxServer) close() { s.srv.Close() }
func (s *wxServer) all() []map[string]any {
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

func TestNew_Validates(t *testing.T) {
	if _, err := New("ch_x", json.RawMessage(`{}`), nil); err == nil {
		t.Fatal("expected error for missing app_token")
	}
	if _, err := New("ch_x", json.RawMessage(`{"app_token":"AT_x"}`), nil); err == nil {
		t.Fatal("expected error for missing uids/topic_ids")
	}
}

func TestSend_TextWithUIDs(t *testing.T) {
	srv := newServer()
	defer srv.close()
	patchAPI(t, srv.srv.URL)

	c, err := New("ch_x",
		json.RawMessage(`{"app_token":"AT_x","uids":["UID_alice","UID_bob"]}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Send(context.Background(), channel.ChannelMessage{Text: "hi opendray"}); err != nil {
		t.Fatal(err)
	}
	calls := srv.all()
	if len(calls) != 1 {
		t.Fatalf("got %d pushes", len(calls))
	}
	got := calls[0]
	if got["appToken"] != "AT_x" {
		t.Errorf("appToken = %v", got["appToken"])
	}
	if got["content"] != "hi opendray" {
		t.Errorf("content = %v", got["content"])
	}
	if int(got["contentType"].(float64)) != wxpContentText {
		t.Errorf("contentType = %v", got["contentType"])
	}
	uids, _ := got["uids"].([]any)
	if len(uids) != 2 {
		t.Errorf("uids = %v", uids)
	}
}

func TestSend_TopicIDs(t *testing.T) {
	srv := newServer()
	defer srv.close()
	patchAPI(t, srv.srv.URL)

	c, _ := New("ch_x",
		json.RawMessage(`{"app_token":"AT_x","topic_ids":[123,456]}`), nil)
	if err := c.Send(context.Background(), channel.ChannelMessage{Text: "x"}); err != nil {
		t.Fatal(err)
	}
	got := srv.all()[0]
	tids, _ := got["topicIds"].([]any)
	if len(tids) != 2 || int(tids[0].(float64)) != 123 {
		t.Errorf("topicIds = %v", tids)
	}
}

func TestSendCard_Markdown(t *testing.T) {
	srv := newServer()
	defer srv.close()
	patchAPI(t, srv.srv.URL)

	c, _ := New("ch_x",
		json.RawMessage(`{"app_token":"AT_x","uids":["UID_alice"]}`), nil)
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
	if int(got["contentType"].(float64)) != wxpContentMarkdown {
		t.Fatalf("contentType = %v, want markdown", got["contentType"])
	}
	if got["summary"] != "Idle" {
		t.Errorf("summary = %v", got["summary"])
	}
	body := got["content"].(string)
	for _, want := range []string{"## Idle", "went idle", "[Open](https://example.com/x)"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q in:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Cancel") {
		t.Errorf("cmd:* button should be dropped:\n%s", body)
	}
}

func TestSend_PropagatesAPIError(t *testing.T) {
	srv := newServer()
	defer srv.close()
	patchAPI(t, srv.srv.URL)
	srv.mu.Lock()
	srv.code = 1001 // any non-1000
	srv.mu.Unlock()

	c, _ := New("ch_x",
		json.RawMessage(`{"app_token":"AT_x","uids":["UID_alice"]}`), nil)
	err := c.Send(context.Background(), channel.ChannelMessage{Text: "x"})
	if err == nil {
		t.Fatal("expected error from non-1000 wxpusher response")
	}
}

func TestSummary_TrimsLong(t *testing.T) {
	long := strings.Repeat("漢", 30)
	got := summary(long)
	runes := []rune(got)
	if len(runes) > 21 {
		t.Errorf("summary too long: %d runes", len(runes))
	}
}
