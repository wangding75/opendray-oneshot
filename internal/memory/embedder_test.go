package memory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBM25Embedder_DimensionStable(t *testing.T) {
	e := NewBM25Embedder(384)
	if got := e.Dimensions(); got != 384 {
		t.Fatalf("dim = %d", got)
	}
	out, err := e.Embed(context.Background(), []string{"hello", "world"})
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range out {
		if len(v) != 384 {
			t.Errorf("vec %d len = %d", i, len(v))
		}
	}
}

func TestBM25Embedder_Normalised(t *testing.T) {
	e := NewBM25Embedder(64)
	out, err := e.Embed(context.Background(), []string{"opendray memory store test"})
	if err != nil {
		t.Fatal(err)
	}
	var sumSq float32
	for _, x := range out[0] {
		sumSq += x * x
	}
	if sumSq < 0.99 || sumSq > 1.01 {
		t.Errorf("vector not unit-length, sum-sq = %f", sumSq)
	}
}

func TestBM25Embedder_OverlappingTokensSimilar(t *testing.T) {
	e := NewBM25Embedder(256)
	out, err := e.Embed(context.Background(), []string{
		"opendray memory subsystem with bge-m3",
		"opendray memory layer using bge-m3 model",
		"completely unrelated content about pets",
	})
	if err != nil {
		t.Fatal(err)
	}
	simAB := CosineSimilarity(out[0], out[1])
	simAC := CosineSimilarity(out[0], out[2])
	if simAB <= simAC {
		t.Errorf("expected overlap > unrelated; simAB=%f simAC=%f", simAB, simAC)
	}
}

func TestBM25Embedder_EmptyInput(t *testing.T) {
	e := NewBM25Embedder(64)
	out, err := e.Embed(context.Background(), []string{""})
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range out[0] {
		if x != 0 {
			t.Errorf("empty input should produce zero vector, got %f", x)
			return
		}
	}
}

func TestCosineSimilarity_EdgeCases(t *testing.T) {
	cases := []struct {
		name string
		a, b []float32
		want float32
	}{
		{"identical", []float32{1, 0, 0}, []float32{1, 0, 0}, 1},
		{"orthogonal", []float32{1, 0, 0}, []float32{0, 1, 0}, 0},
		{"empty", []float32{}, []float32{}, 0},
		{"size mismatch", []float32{1, 2}, []float32{1, 2, 3}, 0},
		{"zero vec", []float32{0, 0, 0}, []float32{1, 1, 1}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := CosineSimilarity(c.a, c.b)
			if got < c.want-1e-5 || got > c.want+1e-5 {
				t.Errorf("got %f, want %f", got, c.want)
			}
		})
	}
}

func TestOpenAICompatibleEmbedder_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		var req embeddingsRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Model != "test-model" {
			t.Errorf("model not forwarded: %q", req.Model)
		}
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-key" {
			t.Errorf("auth not set: %q", auth)
		}
		out := embeddingsResponse{
			Data: make([]struct {
				Index     int       `json:"index"`
				Embedding []float32 `json:"embedding"`
			}, len(req.Input)),
		}
		for i := range req.Input {
			out.Data[i].Index = i
			out.Data[i].Embedding = []float32{float32(i + 1), 0, 0}
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer srv.Close()

	e, err := NewOpenAICompatibleEmbedder(HTTPEmbedderConfig{
		BaseURL: srv.URL + "/v1",
		Model:   "test-model",
		APIKey:  "test-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := e.Embed(context.Background(), []string{"a", "b", "c"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 {
		t.Fatalf("got %d vectors", len(out))
	}
	if e.Dimensions() != 3 {
		t.Errorf("dim cache not set, got %d", e.Dimensions())
	}
}

func TestOpenAICompatibleEmbedder_NoAPIKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("auth should be absent when APIKey is blank")
		}
		var req embeddingsRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		out := embeddingsResponse{
			Data: []struct {
				Index     int       `json:"index"`
				Embedding []float32 `json:"embedding"`
			}{
				{Index: 0, Embedding: []float32{0.1, 0.2, 0.3}},
			},
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer srv.Close()

	e, _ := NewOpenAICompatibleEmbedder(HTTPEmbedderConfig{
		BaseURL: srv.URL + "/v1",
		Model:   "ollama-embed",
	})
	if _, err := e.Embed(context.Background(), []string{"hello"}); err != nil {
		t.Fatal(err)
	}
}

func TestOpenAICompatibleEmbedder_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("upstream is down"))
	}))
	defer srv.Close()

	e, _ := NewOpenAICompatibleEmbedder(HTTPEmbedderConfig{
		BaseURL: srv.URL + "/v1",
		Model:   "x",
	})
	_, err := e.Embed(context.Background(), []string{"x"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("expected 500 in error: %v", err)
	}
}

func TestOpenAICompatibleEmbedder_RejectsBadConfig(t *testing.T) {
	if _, err := NewOpenAICompatibleEmbedder(HTTPEmbedderConfig{Model: "x"}); err == nil {
		t.Error("missing base_url should error")
	}
	if _, err := NewOpenAICompatibleEmbedder(HTTPEmbedderConfig{BaseURL: "http://x"}); err == nil {
		t.Error("missing model should error")
	}
}
