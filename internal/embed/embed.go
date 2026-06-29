// Package embed talks to Ollama and codes float32 vectors as BLOBs.
package embed

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strings"
	"time"
)

// Embedder produces vectors and reports availability.
type Embedder interface {
	Available() bool
	Embed(ctx context.Context, text string) ([]float32, error)
}

// Client is an Ollama-backed Embedder.
type Client struct {
	baseURL string
	model   string
	http    *http.Client
}

// NewClient returns a client configured from environment:
//
//	OLLAMA_HOST  — base URL (default: http://localhost:11434);
//	               if value has no scheme, http:// is prepended
//	OLLAMA_MODEL — embedding model (default: bge-m3:latest)
func NewClient() *Client {
	baseURL := os.Getenv("OLLAMA_HOST")
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	} else if !strings.Contains(baseURL, "://") {
		baseURL = "http://" + baseURL
	}
	model := os.Getenv("OLLAMA_MODEL")
	if model == "" {
		model = "bge-m3:latest"
	}
	return NewClientURL(baseURL, model)
}

// NewClientURL builds a client against an arbitrary base URL (tests).
func NewClientURL(baseURL, model string) *Client {
	return &Client{baseURL: baseURL, model: model, http: &http.Client{Timeout: 30 * time.Second}}
}

// Available probes GET /api/tags (2s) and checks the model is present.
func (c *Client) Available() bool {
	ctxClient := &http.Client{Timeout: 2 * time.Second}
	resp, err := ctxClient.Get(c.baseURL + "/api/tags")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if json.NewDecoder(resp.Body).Decode(&tags) != nil {
		return false
	}
	for _, m := range tags.Models {
		if m.Name == c.model {
			return true
		}
	}
	return false
}

// Embed returns the embedding for text via POST /api/embed.
// The request is cancelled when ctx is done, enforcing the caller's deadline.
// A 30s HTTP client timeout acts as a per-call backstop.
func (c *Client) Embed(ctx context.Context, text string) ([]float32, error) {
	body, _ := json.Marshal(map[string]any{"model": c.model, "input": text})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ollama embed: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama embed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return nil, fmt.Errorf("ollama embed: status %d: %s",
			resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	var out struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode embed response: %w", err)
	}
	if len(out.Embeddings) == 0 {
		return nil, fmt.Errorf("ollama returned no embeddings")
	}
	return out.Embeddings[0], nil
}

// EncodeVector serializes floats as little-endian float32 bytes.
func EncodeVector(v []float32) []byte {
	buf := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

// DecodeVector reverses EncodeVector.
func DecodeVector(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}
