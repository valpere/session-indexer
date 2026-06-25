// Package embed talks to Ollama and codes float32 vectors as BLOBs.
package embed

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"time"
)

// Embedder produces vectors and reports availability.
type Embedder interface {
	Available() bool
	Embed(text string) ([]float32, error)
}

// Client is an Ollama-backed Embedder.
type Client struct {
	baseURL string
	model   string
	http    *http.Client
}

// NewClient returns a client for local Ollama with the bge-m3 model.
func NewClient() *Client { return NewClientURL("http://localhost:11434", "bge-m3:latest") }

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
func (c *Client) Embed(text string) ([]float32, error) {
	body, _ := json.Marshal(map[string]any{"model": c.model, "input": text})
	resp, err := c.http.Post(c.baseURL+"/api/embed", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ollama embed: %w", err)
	}
	defer resp.Body.Close()
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
