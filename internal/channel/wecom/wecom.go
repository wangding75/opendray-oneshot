// Package wecom is opendray's bundled Enterprise WeChat (企业微信)
// channel.
//
// The minimum-viable implementation talks to the **group-robot
// webhook** endpoint (qyapi.weixin.qq.com/cgi-bin/webhook/send?key=…)
// — outbound only, supports text + markdown. Inbound via the
// app-platform callback URL (AES-encrypted) is out of scope for v1.
//
// Provisioning summary: in the WeCom group → Group settings →
// Group robots → Add → copy the webhook URL key.
//
// Capabilities implemented: text, card (rendered as markdown — group
// robots cannot fire callback buttons, so card actions are dropped or
// rendered as inline link rows when their value is a URL).
package wecom

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/opendray/opendray-v2/internal/channel"
)

const (
	apiBase     = "https://qyapi.weixin.qq.com"
	httpTimeout = 30 * time.Second
)

// Test seam.
var apiBaseOverride = ""

func apiURL(path string) string {
	if apiBaseOverride != "" {
		return apiBaseOverride + path
	}
	return apiBase + path
}

func init() { channel.Register("wecom", New) }

type config struct {
	// WebhookKey is the `key=` query parameter from the group robot URL.
	// Either this OR WebhookURL is required.
	WebhookKey string `json:"webhook_key,omitempty"`
	// WebhookURL is the full URL pasted from the WeCom UI when the
	// admin prefers not to extract just the key.
	WebhookURL string `json:"webhook_url,omitempty"`
}

type WeCom struct {
	id     string
	cfg    config
	log    *slog.Logger
	client *http.Client

	mu      sync.Mutex
	cancel  context.CancelFunc
	inbound channel.InboundFunc
}

func New(id string, raw json.RawMessage, log *slog.Logger) (channel.Channel, error) {
	var cfg config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("wecom: parse config: %w", err)
	}
	if cfg.WebhookKey == "" && cfg.WebhookURL == "" {
		return nil, fmt.Errorf("wecom: webhook_key or webhook_url is required")
	}
	if log == nil {
		log = slog.Default()
	}
	return &WeCom{
		id:     id,
		cfg:    cfg,
		log:    log.With("channel", "wecom", "channel_id", id),
		client: &http.Client{Timeout: httpTimeout},
	}, nil
}

func (w *WeCom) ID() string   { return w.id }
func (w *WeCom) Kind() string { return "wecom" }

func (w *WeCom) Start(_ context.Context, inbound channel.InboundFunc) error {
	w.mu.Lock()
	w.inbound = inbound
	if w.cancel != nil {
		w.mu.Unlock()
		return nil
	}
	_, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	w.mu.Unlock()
	w.log.Info("wecom channel started (outbound-only)")
	return nil
}

func (w *WeCom) Stop(_ context.Context) error {
	w.mu.Lock()
	cancel := w.cancel
	w.cancel = nil
	w.inbound = nil
	w.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	w.log.Info("wecom channel stopped")
	return nil
}

func (w *WeCom) Send(ctx context.Context, msg channel.ChannelMessage) error {
	body := map[string]any{
		"msgtype": "text",
		"text":    map[string]any{"content": msg.Text},
	}
	return w.post(ctx, body)
}

// SendCard renders Card → WeCom markdown. Buttons whose values look
// like URLs are appended as a link row at the bottom.
func (w *WeCom) SendCard(ctx context.Context, _ channel.ChannelMessage, card *channel.Card) error {
	body := renderMarkdown(card)
	return w.post(ctx, map[string]any{
		"msgtype":  "markdown",
		"markdown": map[string]any{"content": body},
	})
}

func (w *WeCom) post(ctx context.Context, body any) error {
	endpoint := w.endpoint()
	if endpoint == "" {
		return fmt.Errorf("wecom: no webhook configured")
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := w.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("wecom webhook: HTTP %d: %s", resp.StatusCode, respBody)
	}
	var probe struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	_ = json.Unmarshal(respBody, &probe)
	if probe.ErrCode != 0 {
		return fmt.Errorf("wecom webhook: errcode=%d errmsg=%s", probe.ErrCode, probe.ErrMsg)
	}
	return nil
}

func (w *WeCom) endpoint() string {
	if w.cfg.WebhookURL != "" {
		// honour test seam if user pasted a relative URL via override
		if apiBaseOverride != "" && strings.HasPrefix(w.cfg.WebhookURL, apiBase) {
			return apiBaseOverride + strings.TrimPrefix(w.cfg.WebhookURL, apiBase)
		}
		return w.cfg.WebhookURL
	}
	return apiURL("/cgi-bin/webhook/send?key=" + w.cfg.WebhookKey)
}

func renderMarkdown(card *channel.Card) string {
	if card == nil {
		return ""
	}
	var lines []string
	if card.Header != nil && card.Header.Title != "" {
		lines = append(lines, "**"+card.Header.Title+"**")
	}
	var links []string
	for _, el := range card.Elements {
		switch v := el.(type) {
		case channel.CardMarkdown:
			lines = append(lines, v.Content)
		case channel.CardDivider:
			lines = append(lines, "---")
		case channel.CardActions:
			for _, row := range v.Buttons {
				for _, b := range row {
					if u, ok := navURL(b.Value); ok {
						links = append(links, fmt.Sprintf("[%s](%s)", b.Text, u))
					}
				}
			}
		case channel.CardListItem:
			line := v.Text
			if u, ok := navURL(v.Button.Value); ok {
				line += fmt.Sprintf("  [%s](%s)", v.Button.Text, u)
			}
			lines = append(lines, line)
		case channel.CardSelect:
			lines = append(lines, v.Placeholder)
		case channel.CardNote:
			lines = append(lines, "> "+v.Text)
		}
	}
	if len(links) > 0 {
		lines = append(lines, strings.Join(links, "  ·  "))
	}
	return strings.Join(filterEmpty(lines), "\n\n")
}

// navURL pulls a clickable URL from a button value. We honour both
// `nav:https://…` and bare `http(s)://…`. Relative `nav:/…` is dropped
// because the WeCom group robot has no way back to opendray's UI.
func navURL(v string) (string, bool) {
	switch {
	case strings.HasPrefix(v, "nav:"):
		u := strings.TrimPrefix(v, "nav:")
		if strings.HasPrefix(u, "/") {
			return "", false
		}
		return u, true
	case strings.HasPrefix(v, "http://"), strings.HasPrefix(v, "https://"):
		return v, true
	}
	return "", false
}

func filterEmpty(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}
