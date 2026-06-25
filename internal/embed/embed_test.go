package embed

import (
	"math"
	"net/http"
	"net/http/httptest"
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
	v, err := c.Embed("hello")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(v) != 3 || v[0] != 0.5 {
		t.Fatalf("vector = %v", v)
	}
}
