// Package embed talks to a local Ollama instance. nomic-embed-text requires
// task prefixes ("search_document:" / "search_query:") — quality silently
// degrades without them, so they are applied here and nowhere else.
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const (
	DefaultBaseURL = "http://localhost:11434"
	// Pinned to an explicit tag: an untagged ref resolves to :latest, which
	// Ollama may silently move to a new release whose embeddings aren't
	// comparable to what's already indexed. Must match the tag pulled in
	// flake.nix exactly (Ollama treats :v1.5 and :latest as distinct models).
	// After changing this, a full reindex is required: silo-kb reindex --full.
	Model     = "nomic-embed-text:v1.5"
	Dim       = 768
	batchSize = 32
)

type Client struct {
	BaseURL string
	http    *http.Client
}

func New(baseURL string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	// Generous timeout: a cold model load can take 10–30s before the first
	// embedding comes back.
	return &Client{BaseURL: baseURL, http: &http.Client{Timeout: 180 * time.Second}}
}

// Warmup loads the model so subsequent batch calls don't eat the cold start.
func (c *Client) Warmup(ctx context.Context) error {
	_, err := c.embed(ctx, []string{"search_document: warmup"})
	return err
}

// Documents embeds chunk contents with the document task prefix.
func (c *Client) Documents(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += batchSize {
		end := min(start+batchSize, len(texts))
		batch := make([]string, 0, end-start)
		for _, t := range texts[start:end] {
			batch = append(batch, "search_document: "+t)
		}
		vecs, err := c.embed(ctx, batch)
		if err != nil {
			return nil, err
		}
		out = append(out, vecs...)
	}
	return out, nil
}

// Query embeds a search query with the query task prefix.
func (c *Client) Query(ctx context.Context, text string) ([]float32, error) {
	vecs, err := c.embed(ctx, []string{"search_query: " + text})
	if err != nil {
		return nil, err
	}
	return vecs[0], nil
}

func (c *Client) embed(ctx context.Context, inputs []string) ([][]float32, error) {
	body, err := json.Marshal(map[string]any{"model": Model, "input": inputs})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama at %s: %w (is ollama running with %s pulled?)", c.BaseURL, err, Model)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		return nil, fmt.Errorf("ollama /api/embed: %s: %s", resp.Status, e.Error)
	}

	var r struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	if len(r.Embeddings) != len(inputs) {
		return nil, fmt.Errorf("ollama returned %d embeddings for %d inputs", len(r.Embeddings), len(inputs))
	}
	for i, v := range r.Embeddings {
		if len(v) != Dim {
			return nil, fmt.Errorf("embedding %d has dim %d, want %d (wrong model?)", i, len(v), Dim)
		}
	}
	return r.Embeddings, nil
}
