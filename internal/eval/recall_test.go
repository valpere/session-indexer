package eval

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestSensitive(t *testing.T) {
	for _, s := range []string{
		"my password is hunter2 for the server",
		"export ANTHROPIC_API_KEY=sk-ant-abcdefghijklmnop1234",
		"Authorization: Bearer abcdefghijklmnopqrstuvwxyz",
		"-----BEGIN PRIVATE KEY-----",
		"write to valentyn@example.com about it",
		"token ghp_abcdefghijklmnopqrstuvwxyz0123456789",
		"the secret value is 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
		"cat .env and check DATABASE_URL",
	} {
		if !Sensitive(s) {
			t.Errorf("want sensitive: %q", s)
		}
	}
	for _, s := range []string{
		"we chose WAL mode because readers must not block the stop hook",
		"Ми вирішили використати RRF для злиття двох списків результатів",
	} {
		if Sensitive(s) {
			t.Errorf("want not sensitive: %q", s)
		}
	}
}

func TestOverlapRatio(t *testing.T) {
	chunk := "We decided to enable write-ahead logging because concurrent readers stalled during the stop hook"
	// pure paraphrase: no shared content stems
	if r := OverlapRatio(chunk, "why did we pick journaling mode for simultaneous access"); r > 0.25 {
		t.Errorf("paraphrase ratio = %v", r)
	}
	// reuses the distinctive words
	if r := OverlapRatio(chunk, "why enable write-ahead logging because readers stalled"); r < 0.5 {
		t.Errorf("copy ratio = %v", r)
	}
	// inflected Ukrainian forms share a 5-rune stem
	if r := OverlapRatio("рішення про налаштування конфігурації сховища", "налаштуванням конфігурацією сховищем"); r < 0.9 {
		t.Errorf("ua stem ratio = %v", r)
	}
	if r := OverlapRatio(chunk, "a b c"); r != 1 {
		t.Errorf("no content stems must be rejected (ratio 1), got %v", r)
	}
}

func TestParseQueries(t *testing.T) {
	resp := "Sure!\n```json\n[{\"chunk\":1,\"query\":\"first question\"},{\"chunk\":3,\"query\":\"third question\"}]\n```"
	got, err := ParseQueries(resp)
	if err != nil || len(got) != 2 || got[1].Chunk != 3 || got[1].Query != "third question" {
		t.Fatalf("got %+v, %v", got, err)
	}
	if _, err := ParseQueries("no json here"); err == nil {
		t.Error("want error on missing JSON")
	}
}

func TestSampleChunks(t *testing.T) {
	turns := []Turn{}
	for i := 0; i < 100; i++ {
		text := strings.Repeat(fmt.Sprintf("topic%d word ", i), 30)
		if i%10 == 0 {
			text = "password: " + text // sensitive → excluded
		}
		if i%7 == 0 {
			text = "short" // too short → excluded
		}
		turns = append(turns, Turn{DiaID: fmt.Sprintf("c%d", i), Text: text})
	}
	a := SampleChunks(turns, 20)
	b := SampleChunks(turns, 20)
	if len(a) != 20 || fmt.Sprint(a) != fmt.Sprint(b) {
		t.Fatalf("want 20 deterministic, got %d", len(a))
	}
	for _, tr := range a {
		if Sensitive(tr.Text) || len(tr.Text) < minRecallChunk {
			t.Errorf("ineligible chunk sampled: %s", tr.DiaID)
		}
	}
}

func TestGenerateQueries(t *testing.T) {
	turns := []Turn{
		{DiaID: "c1", Text: "We decided to enable write-ahead logging because concurrent readers stalled"},
		{DiaID: "c2", Text: "The tokenizer keeps diacritics so Ukrainian words stay distinct in search"},
		{DiaID: "c3", Text: "Embeddings are stored as little-endian float32 blobs in the vectors table"},
	}
	gen := func(_ context.Context, prompt string) (string, error) {
		// answers by batch position: 1 = paraphrase, 2 = copies distinctive words, 3 = garbage index
		return `[{"chunk":1,"query":"why did we pick journaling for simultaneous access"},` +
			`{"chunk":2,"query":"keeps diacritics Ukrainian words distinct tokenizer"},` +
			`{"chunk":9,"query":"orphan question about nothing"}]`, nil
	}
	qas, st, err := GenerateQueries(context.Background(), gen, turns, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(qas) != 1 || qas[0].Evidence[0] != "c1" {
		t.Fatalf("qas = %+v", qas)
	}
	if st.Generated != 3 || st.Kept != 1 || st.Overlap != 1 || st.BadIndex != 1 {
		t.Errorf("stats = %+v", st)
	}
}

func TestGenerateQueriesBatchErrorSkipsBatch(t *testing.T) {
	turns := []Turn{{DiaID: "c1", Text: strings.Repeat("alpha beta gamma ", 20)}}
	gen := func(context.Context, string) (string, error) { return "", errors.New("agy down") }
	qas, st, err := GenerateQueries(context.Background(), gen, turns, 10)
	if err != nil || len(qas) != 0 || st.BatchErrors != 1 {
		t.Fatalf("qas=%v st=%+v err=%v", qas, st, err)
	}
}
