// Package embed turns text into vectors with a local embedding model served
// by Ollama. Documents and queries are embedded through separate methods
// because retrieval models expect different prompts for each side.
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/vitalbh/elan/internal/config"
)

// Doc is a document to embed. Title may be empty.
type Doc struct {
	Title string
	Text  string
}

// Embedder embeds documents and queries into the same vector space.
type Embedder interface {
	// Model identifies the model; the index records it and refuses to mix
	// vectors of different models.
	Model() string
	EmbedDocuments(ctx context.Context, docs []Doc) ([][]float32, error)
	EmbedQuery(ctx context.Context, text string) ([]float32, error)
}

// batchSize is how many inputs go in one /api/embed request.
const batchSize = 32

// Ollama is an Embedder backed by Ollama's /api/embed.
type Ollama struct {
	url   string
	model string
	http  *http.Client
}

// NewOllama builds an Ollama embedder from the profile config.
func NewOllama(cfg config.Embed) *Ollama {
	return &Ollama{
		url:   strings.TrimRight(cfg.URL, "/"),
		model: cfg.Model,
		http:  &http.Client{Timeout: 2 * time.Minute},
	}
}

// Model returns the model name with an explicit tag (":latest" when the
// config has none), so "embeddinggemma" and "embeddinggemma:latest" match.
func (o *Ollama) Model() string {
	if strings.Contains(o.model, ":") {
		return o.model
	}
	return o.model + ":latest"
}

// EmbedDocuments embeds docs in batches, in order.
func (o *Ollama) EmbedDocuments(ctx context.Context, docs []Doc) ([][]float32, error) {
	inputs := make([]string, len(docs))
	for i, d := range docs {
		inputs[i] = o.documentPrompt(d)
	}
	out := make([][]float32, 0, len(docs))
	for start := 0; start < len(inputs); start += batchSize {
		end := min(start+batchSize, len(inputs))
		vecs, err := o.embed(ctx, inputs[start:end])
		if err != nil {
			return nil, err
		}
		out = append(out, vecs...)
	}
	return out, nil
}

// EmbedQuery embeds a search query.
func (o *Ollama) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	vecs, err := o.embed(ctx, []string{o.queryPrompt(text)})
	if err != nil {
		return nil, err
	}
	return vecs[0], nil
}

// isGemma reports whether the model is EmbeddingGemma, which was trained with
// task prompts (https://ai.google.dev/gemma/docs/embeddinggemma).
func (o *Ollama) isGemma() bool {
	return strings.HasPrefix(o.model, "embeddinggemma")
}

func (o *Ollama) queryPrompt(text string) string {
	if o.isGemma() {
		return "task: search result | query: " + text
	}
	return text
}

func (o *Ollama) documentPrompt(d Doc) string {
	if !o.isGemma() {
		if d.Title == "" {
			return d.Text
		}
		return d.Title + "\n\n" + d.Text
	}
	title := d.Title
	if title == "" {
		title = "none"
	}
	return "title: " + title + " | text: " + d.Text
}

type embedRequest struct {
	Model    string   `json:"model"`
	Input    []string `json:"input"`
	Truncate bool     `json:"truncate"`
}

type embedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
	Error      string      `json:"error"`
}

func (o *Ollama) embed(ctx context.Context, inputs []string) ([][]float32, error) {
	body, err := json.Marshal(embedRequest{Model: o.model, Input: inputs, Truncate: true})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.url+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var opErr *net.OpError
		if errors.As(err, &opErr) && opErr.Op == "dial" {
			return nil, fmt.Errorf("Ollama fora do ar em %s: rode `brew services start ollama`", o.url)
		}
		return nil, fmt.Errorf("ollama: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	if err != nil {
		return nil, fmt.Errorf("ollama: %w", err)
	}
	var er embedResponse
	if err := json.Unmarshal(raw, &er); err != nil {
		return nil, fmt.Errorf("ollama: resposta inválida (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode == http.StatusNotFound && strings.Contains(er.Error, "not found") {
		return nil, fmt.Errorf("modelo %q não encontrado no Ollama: rode `ollama pull %s`", o.model, o.model)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama: HTTP %d: %s", resp.StatusCode, er.Error)
	}
	if len(er.Embeddings) != len(inputs) {
		return nil, fmt.Errorf("ollama: %d embeddings para %d entradas", len(er.Embeddings), len(inputs))
	}
	return er.Embeddings, nil
}
