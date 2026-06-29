package embed

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVectorRoundTrip(t *testing.T) {
	in := []float32{0.1, -0.5, 3.14159, 0}
	out := DecodeVector(EncodeVector(in))
	if len(out) != len(in) {
		t.Fatalf("len = %d, want %d", len(out), len(in))
	}
	for i := range in {
		if math.Abs(float64(in[i]-out[i])) > 1e-6 {
			t.Fatalf("out[%d] = %v, want %v", i, out[i], in[i])
		}
	}
}

func TestEncodeVectorByteLength(t *testing.T) {
	if got := len(EncodeVector(make([]float32, 1024))); got != 4096 {
		t.Fatalf("1024 floats = %d bytes, want 4096", got)
	}
}

func TestAvailableTrueWhenModelPresent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"models":[{"name":"bge-m3:latest"}]}`))
	}))
	defer srv.Close()
	c := NewClientURL(srv.URL, "bge-m3:latest")
	if !c.Available() {
		t.Fatal("Available() = false, want true")
	}
}

func TestAvailableFalseWhenModelMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"models":[{"name":"llama3:latest"}]}`))
	}))
	defer srv.Close()
	c := NewClientURL(srv.URL, "bge-m3:latest")
	if c.Available() {
		t.Fatal("Available() = true, want false (model absent)")
	}
}

func TestEmbedReturnsVector(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"embeddings":[[0.5,0.25,0.125]]}`))
	}))
	defer srv.Close()
	c := NewClientURL(srv.URL, "bge-m3:latest")
	v, err := c.Embed(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(v) != 3 || v[0] != 0.5 {
		t.Fatalf("vector = %v", v)
	}
}

// TestEmbedSurfacesHTTPStatus verifies that a non-2xx response returns a
// useful status error instead of a vague decode failure.
func TestEmbedSurfacesHTTPStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"model loading failed"}`))
	}))
	defer srv.Close()
	c := NewClientURL(srv.URL, "bge-m3:latest")
	_, err := c.Embed(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error for 500 response, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("error should mention status 500, got: %v", err)
	}
}

// TestEmbedRespectsContextCancellation verifies that a cancelled ctx aborts
// the HTTP request — this is the mechanism that enforces the 50s mine deadline.
func TestEmbedRespectsContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block until the client cancels.
		<-r.Context().Done()
	}))
	defer srv.Close()
	c := NewClientURL(srv.URL, "bge-m3:latest")
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled
	_, err := c.Embed(ctx, "test")
	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}
}
