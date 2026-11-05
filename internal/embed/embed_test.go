package embed

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vitalbh/elan/internal/config"
)

func TestOllamaPromptsAndBatching(t *testing.T) {
	var requests [][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			http.NotFound(w, r)
			return
		}
		var req embedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		requests = append(requests, req.Input)
		vecs := make([][]float32, len(req.Input))
		for i := range vecs {
			vecs[i] = []float32{float32(len(requests)), float32(i)}
		}
		json.NewEncoder(w).Encode(embedResponse{Embeddings: vecs}) //nolint:errcheck
	}))
	defer srv.Close()

	o := NewOllama(config.Embed{URL: srv.URL + "/", Model: "embeddinggemma"})
	if o.Model() != "embeddinggemma:latest" {
		t.Fatalf("model %q", o.Model())
	}
	docs := make([]Doc, batchSize+3)
	for i := range docs {
		docs[i] = Doc{Text: "texto"}
	}
	docs[0].Title = "Reunião"
	vecs, err := o.EmbedDocuments(context.Background(), docs)
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != len(docs) || len(requests) != 2 || len(requests[1]) != 3 {
		t.Fatalf("vecs=%d requests=%d", len(vecs), len(requests))
	}
	if vecs[batchSize][0] != 2 || vecs[batchSize][1] != 0 {
		t.Fatalf("order lost: %v", vecs[batchSize])
	}
	if requests[0][0] != "title: Reunião | text: texto" || requests[0][1] != "title: none | text: texto" {
		t.Fatalf("document prompts %q", requests[0][:2])
	}
	if _, err := o.EmbedQuery(context.Background(), "o que decidimos?"); err != nil {
		t.Fatal(err)
	}
	if got := requests[2][0]; got != "task: search result | query: o que decidimos?" {
		t.Fatalf("query prompt %q", got)
	}

	plain := NewOllama(config.Embed{URL: srv.URL, Model: "qwen3-embedding:0.6b"})
	if plain.Model() != "qwen3-embedding:0.6b" {
		t.Fatalf("model %q", plain.Model())
	}
	if _, err := plain.EmbedQuery(context.Background(), "oi"); err != nil || requests[3][0] != "oi" {
		t.Fatalf("plain query %v %q", err, requests[3])
	}
}

func TestOllamaErrors(t *testing.T) {
	// A port nobody listens on.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	_, err = NewOllama(config.Embed{URL: "http://" + addr, Model: "embeddinggemma"}).EmbedQuery(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "Ollama fora do ar") || !strings.Contains(err.Error(), "brew services start ollama") {
		t.Fatalf("down: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"model 'nope' not found"}`)) //nolint:errcheck
	}))
	defer srv.Close()
	_, err = NewOllama(config.Embed{URL: srv.URL, Model: "nope"}).EmbedQuery(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "ollama pull nope") {
		t.Fatalf("missing model: %v", err)
	}
}
