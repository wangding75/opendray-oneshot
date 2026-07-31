package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ProbeResult is the shape returned by a single endpoint
// reachability check. Reused for both startup auto-detect and the
// UI's "Test connection" button.
type ProbeResult struct {
	BaseURL    string   `json:"base_url"`
	Reachable  bool     `json:"reachable"`
	StatusCode int      `json:"status_code,omitempty"`
	Models     []string `json:"models,omitempty"`
	Error      string   `json:"error,omitempty"`
	// Detected provider name when we recognise the response shape.
	// Examples: "ollama" (the upstream returns its custom envelope
	// at /api/tags) or "openai-compatible" (generic /v1/models JSON).
	Detected string `json:"detected,omitempty"`
}

// ProbeEndpoint hits a base URL with the OpenAI-compatible
// /models or /v1/models route and returns whether the service is
// alive plus the list of model IDs it advertises. apiKey may be
// empty for local services.
//
// Two probe shapes:
//
//   - OpenAI-compatible: GET <base>/models or <base>/v1/models →
//     {"data": [{"id": "..."}]}
//   - Ollama-native: GET <base>/api/tags →
//     {"models": [{"name": "..."}]}  — used as a fallback when the
//     base URL points at the bare ollama host without /v1.
func ProbeEndpoint(ctx context.Context, baseURL, apiKey string) ProbeResult {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	res := ProbeResult{BaseURL: baseURL}
	if baseURL == "" {
		res.Error = "empty base_url"
		return res
	}
	client := &http.Client{Timeout: 3 * time.Second}

	// Try the OpenAI-compatible path first. Ollama and LM Studio
	// both expose this when reachable through their /v1 prefix.
	if models, status, err := tryOpenAIModels(ctx, client, baseURL, apiKey); err == nil {
		res.Reachable = true
		res.StatusCode = status
		res.Models = models
		res.Detected = "openai-compatible"
		return res
	} else {
		res.Error = err.Error()
		res.StatusCode = status
	}

	// Fallback: ollama's native /api/tags. Strip a trailing /v1 if
	// present so we hit the bare host.
	hostBase := strings.TrimSuffix(baseURL, "/v1")
	if models, status, err := tryOllamaTags(ctx, client, hostBase); err == nil {
		res.Reachable = true
		res.StatusCode = status
		res.Models = models
		res.Detected = "ollama"
		res.Error = "" // clear the openai-path error
		return res
	}

	return res
}

func tryOpenAIModels(ctx context.Context, client *http.Client, baseURL, apiKey string) ([]string, int, error) {
	url := baseURL + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return nil, resp.StatusCode, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(body), 120))
	}
	var doc struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("decode: %w", err)
	}
	out := make([]string, 0, len(doc.Data))
	for _, m := range doc.Data {
		if m.ID != "" {
			out = append(out, m.ID)
		}
	}
	return out, resp.StatusCode, nil
}

func tryOllamaTags(ctx context.Context, client *http.Client, baseURL string) ([]string, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/tags", nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return nil, resp.StatusCode, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var doc struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, resp.StatusCode, err
	}
	out := make([]string, 0, len(doc.Models))
	for _, m := range doc.Models {
		if m.Name != "" {
			out = append(out, m.Name)
		}
	}
	return out, resp.StatusCode, nil
}

// AutoDetect runs ProbeEndpoint against the well-known local
// embedding endpoints and returns the first one that responds.
// Used at app startup to surface "we can see ollama running"
// in the UI without forcing the operator to configure anything.
//
// Returns (nil, nil) when nothing is reachable — the caller treats
// that as "operator hasn't set up an embedding service, BM25 will
// be the active default".
func AutoDetect(ctx context.Context) []ProbeResult {
	type candidate struct {
		baseURL string
		label   string
	}
	candidates := []candidate{
		{"http://localhost:11434/v1", "ollama"},
		{"http://localhost:1234/v1", "lmstudio"},
		{"http://127.0.0.1:11434/v1", "ollama"},
		{"http://127.0.0.1:1234/v1", "lmstudio"},
	}
	seen := map[string]bool{}
	var hits []ProbeResult
	for _, c := range candidates {
		if seen[c.baseURL] {
			continue
		}
		seen[c.baseURL] = true
		probeCtx, cancel := context.WithTimeout(ctx, 800*time.Millisecond)
		r := ProbeEndpoint(probeCtx, c.baseURL, "")
		cancel()
		if r.Reachable && len(r.Models) > 0 {
			// Pin our own short label so the UI doesn't have to guess.
			r.Detected = c.label
			hits = append(hits, r)
		}
	}
	return hits
}

// Compile-time assertion: ensure errors are at least mentioned so
// the linter doesn't complain when callers ignore Service-level
// auto-detect helpers.
var _ = errors.New
