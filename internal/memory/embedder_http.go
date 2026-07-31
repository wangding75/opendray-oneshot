package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// OpenAICompatibleEmbedder posts to any /v1/embeddings endpoint
// that follows OpenAI's request/response shape. The same envelope
// is implemented by:
//
//   - OpenAI itself                         (api.openai.com)
//   - Ollama (after `ollama serve`)         (localhost:11434/v1)
//   - LocalAI                               (configurable port)
//   - vLLM                                  (when launched with --api-key)
//   - Text Embeddings Inference             (HF's reference server)
//   - Most "OpenAI-compatible" router LLMs
//
// Auth is "Bearer <api_key>" when APIKey is non-empty, otherwise
// no auth header — Ollama needs no key, OpenAI needs one.
//
// The embedder caches the dimension after the first successful
// call so callers can rely on Dimensions() returning the live
// value (helps the Store reject mismatched vectors at insert
// time). If Config.Dimensions is set, that wins and no probe
// happens.
type OpenAICompatibleEmbedder struct {
	cfg    HTTPEmbedderConfig
	client *http.Client

	dimMu sync.Mutex
	dim   int
}

// HTTPEmbedderConfig collects the runtime knobs for the
// OpenAI-compatible embedder. Mirrors config.MemoryHTTPConfig —
// kept as a separate type so the memory package doesn't import
// config (avoiding a cycle when settings handlers import memory).
type HTTPEmbedderConfig struct {
	BaseURL    string
	Model      string
	APIKey     string
	Dimensions int
	Timeout    time.Duration
}

// NewOpenAICompatibleEmbedder returns an embedder pointed at the
// given /v1/embeddings endpoint. The trailing slash on BaseURL is
// trimmed; callers don't have to normalise.
func NewOpenAICompatibleEmbedder(cfg HTTPEmbedderConfig) (*OpenAICompatibleEmbedder, error) {
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if cfg.BaseURL == "" {
		return nil, errors.New("memory: HTTP embedder needs a base_url")
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, errors.New("memory: HTTP embedder needs a model name")
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	return &OpenAICompatibleEmbedder{
		cfg:    cfg,
		client: &http.Client{Timeout: cfg.Timeout},
		dim:    cfg.Dimensions, // 0 = unknown until first call
	}, nil
}

func (e *OpenAICompatibleEmbedder) Name() string {
	return fmt.Sprintf("http:%s", e.cfg.Model)
}

func (e *OpenAICompatibleEmbedder) Dimensions() int {
	e.dimMu.Lock()
	defer e.dimMu.Unlock()
	return e.dim
}

type embeddingsRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embeddingsResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	// OpenAI-style error envelope. Some servers (Ollama) just
	// return a plain string in `error`, so we accept both via
	// json.RawMessage and parse leniently below.
	Error json.RawMessage `json:"error,omitempty"`
}

// Embed POSTs all texts in a single batched request — every
// supported backend handles batching, and a single round-trip
// dominates cost for memory.store.
func (e *OpenAICompatibleEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	body, err := json.Marshal(embeddingsRequest{
		Model: e.cfg.Model,
		Input: texts,
	})
	if err != nil {
		return nil, fmt.Errorf("memory: marshal embeddings request: %w", err)
	}

	url := e.cfg.BaseURL + "/embeddings"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("memory: build embeddings request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if e.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.cfg.APIKey)
	}

	res, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("memory: post %s: %w", url, err)
	}
	defer res.Body.Close()

	bodyBytes, _ := io.ReadAll(res.Body)
	if res.StatusCode/100 != 2 {
		return nil, fmt.Errorf("memory: %s returned %d: %s", url, res.StatusCode, truncate(string(bodyBytes), 240))
	}

	var parsed embeddingsResponse
	if err := json.Unmarshal(bodyBytes, &parsed); err != nil {
		return nil, fmt.Errorf("memory: decode embeddings response: %w", err)
	}
	if len(parsed.Error) > 0 && string(parsed.Error) != "null" {
		return nil, fmt.Errorf("memory: %s reported error: %s", url, truncate(string(parsed.Error), 240))
	}
	if len(parsed.Data) != len(texts) {
		return nil, fmt.Errorf("memory: expected %d embeddings, got %d", len(texts), len(parsed.Data))
	}

	out := make([][]float32, len(texts))
	for _, item := range parsed.Data {
		if item.Index < 0 || item.Index >= len(texts) {
			return nil, fmt.Errorf("memory: embedding index %d out of range", item.Index)
		}
		out[item.Index] = item.Embedding
	}
	for i, v := range out {
		if v == nil {
			return nil, fmt.Errorf("memory: missing embedding for text index %d", i)
		}
	}

	// Cache the dimension on first success so Dimensions() returns
	// a live value rather than 0.
	if d := len(out[0]); d > 0 {
		e.dimMu.Lock()
		if e.dim == 0 {
			e.dim = d
		}
		e.dimMu.Unlock()
	}

	return out, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
