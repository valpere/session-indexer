package eval

import (
	"context"
	"errors"
	"hash/fnv"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const fixture = `[
 {"sample_id":"conv-1",
  "conversation":{"speaker_a":"Ann","speaker_b":"Bob",
   "session_2_date_time":"2:00 pm on 9 May, 2023",
   "session_2":[{"speaker":"Ann","dia_id":"D2:1","text":"Second session: we adopted a puppy named Rex yesterday."}],
   "session_1_date_time":"1:56 pm on 8 May, 2023",
   "session_1":[
     {"speaker":"Ann","dia_id":"D1:1","text":"I joined the pottery class downtown last week, it is great."},
     {"speaker":"Bob","dia_id":"D1:2","text":"ok"}]},
  "qa":[
   {"question":"What class did Ann join?","answer":"pottery","evidence":["D1:1"],"category":1},
   {"question":"Who is Rex?","answer":"puppy","evidence":["D2:1; D1:2"],"category":2},
   {"question":"No evidence here?","answer":"x","evidence":[],"category":3},
   {"question":"Garbled evidence?","answer":"x","evidence":["D","D:11:26"],"category":4}
  ]}
]`

func writeFixture(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "locomo.json")
	if err := os.WriteFile(p, []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseEvidence(t *testing.T) {
	got := ParseEvidence([]string{"D1:3", "D8:6; D9:17", "D9:1 D4:4 D4:6", "D", "D:11:26", "D1:3"})
	want := []string{"D1:3", "D8:6", "D9:17", "D9:1", "D4:4", "D4:6"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseEvidence = %v, want %v", got, want)
	}
	if len(ParseEvidence(nil)) != 0 {
		t.Fatal("nil evidence must yield none")
	}
}

func TestLoadLoCoMoOrdersSessionsNumerically(t *testing.T) {
	convs, err := LoadLoCoMo(writeFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(convs) != 1 || convs[0].ID != "conv-1" {
		t.Fatalf("convs = %+v", convs)
	}
	var ids []string
	for _, tn := range convs[0].Turns {
		ids = append(ids, tn.DiaID)
	}
	if strings.Join(ids, ",") != "D1:1,D1:2,D2:1" {
		t.Fatalf("turn order = %v (session_1 must precede session_2)", ids)
	}
	if convs[0].Turns[0].Date != "2023-05-08" {
		t.Fatalf("date = %q, want 2023-05-08", convs[0].Turns[0].Date)
	}
}

func TestLoadLoCoMoDropsQuestionsWithoutUsableEvidence(t *testing.T) {
	convs, err := LoadLoCoMo(writeFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	c := convs[0]
	if len(c.QAs) != 2 || c.NoEvidence != 2 {
		t.Fatalf("QAs=%d NoEvidence=%d, want 2 and 2", len(c.QAs), c.NoEvidence)
	}
	if !reflect.DeepEqual(c.QAs[1].Evidence, []string{"D2:1", "D1:2"}) {
		t.Fatalf("evidence = %v", c.QAs[1].Evidence)
	}
}

func TestJudgeHitAtRankOne(t *testing.T) {
	j := Judge([]string{"D1:1"}, [][]string{{"D1:1"}, {"D9:9"}}, []int{1, 3})
	if j.FirstHit != 1 || !j.AnyAt[1] || !j.AnyAt[3] || !j.AllAt[1] {
		t.Fatalf("j = %+v", j)
	}
}

func TestJudgeHitAtRankThree(t *testing.T) {
	j := Judge([]string{"D1:1"}, [][]string{{"a"}, {"b"}, {"D1:1"}}, []int{1, 3, 5})
	if j.FirstHit != 3 || j.AnyAt[1] || !j.AnyAt[3] || !j.AnyAt[5] {
		t.Fatalf("j = %+v", j)
	}
}

func TestJudgeNoHit(t *testing.T) {
	j := Judge([]string{"D1:1"}, [][]string{{"a"}, {"b"}}, []int{1, 3})
	if j.FirstHit != 0 || j.AnyAt[3] || j.AllAt[3] {
		t.Fatalf("j = %+v", j)
	}
}

func TestJudgeAllEvidenceNeedsEveryItem(t *testing.T) {
	ev := []string{"D1:1", "D2:2"}
	j := Judge(ev, [][]string{{"D1:1"}, {"x"}, {"D2:2"}}, []int{1, 2, 3})
	if !j.AnyAt[1] || j.AllAt[1] || j.AllAt[2] || !j.AllAt[3] {
		t.Fatalf("j = %+v", j)
	}
}

func TestJudgeDuplicateContentSetsCountForAnyMatch(t *testing.T) {
	// One result text shared by several turns maps to several dia_ids.
	j := Judge([]string{"D2:2"}, [][]string{{"D1:1", "D2:2"}}, []int{1})
	if !j.AnyAt[1] {
		t.Fatalf("j = %+v", j)
	}
}

func TestSummarize(t *testing.T) {
	ks := []int{1, 3}
	qs := []QResult{
		{Category: 1, J: Judge([]string{"a"}, [][]string{{"a"}}, ks), LatencyMs: 10, ContextTokens: 100},
		{Category: 1, J: Judge([]string{"a"}, [][]string{{"x"}, {"y"}, {"a"}}, ks), LatencyMs: 30, ContextTokens: 300},
		{Category: 1, J: Judge([]string{"a"}, [][]string{{"x"}}, ks), LatencyMs: 20, ContextTokens: 200},
	}
	s := Summarize(qs, ks)
	if s.N != 3 || s.RecallAny[1] != 1.0/3 || s.RecallAny[3] != 2.0/3 {
		t.Fatalf("s = %+v", s)
	}
	if want := (1.0 + 1.0/3 + 0) / 3; s.MRR != want {
		t.Fatalf("MRR = %v, want %v", s.MRR, want)
	}
	if s.AvgLatencyMs != 20 || s.AvgContextTokens != 200 {
		t.Fatalf("avg = %v / %v", s.AvgLatencyMs, s.AvgContextTokens)
	}
	if Summarize(nil, ks).N != 0 {
		t.Fatal("empty summarize must not divide by zero")
	}
}

func TestRenderMarkdownIsDeterministic(t *testing.T) {
	ks := []int{1, 3}
	r := Report{Ks: ks, Modes: []ModeReport{{Mode: "bm25", PerCategory: map[int]Summary{2: {N: 1}, 1: {N: 2}}, Overall: Summary{N: 3}}}}
	a, b := RenderMarkdown(r), RenderMarkdown(r)
	if a != b || !strings.Contains(a, "bm25") || strings.Index(a, "cat 1") > strings.Index(a, "cat 2") {
		t.Fatalf("render not deterministic/ordered:\n%s", a)
	}
}

func TestNaiveRetrieverRanksByKeywordOverlap(t *testing.T) {
	turns := []Turn{
		{DiaID: "D1:1", Text: "We talked about the weather today."},
		{DiaID: "D1:2", Text: "Ann joined the pottery class downtown."},
		{DiaID: "D1:3", Text: "Pottery is fun."},
	}
	got, err := NaiveRetriever(turns)("What pottery class did Ann join?", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != turns[1].Text {
		t.Fatalf("got %v", got)
	}
}

// fakeEmb is a deterministic bag-of-words hashing embedder, so the DB-backed
// retrievers can be exercised without Ollama.
type fakeEmb struct{ down bool }

func (f fakeEmb) Available() bool { return !f.down }
func (f fakeEmb) Embed(_ context.Context, text string) ([]float32, error) {
	if f.down {
		return nil, errors.New("down")
	}
	v := make([]float32, 64)
	for _, w := range tokenize(text) {
		h := fnv.New32a()
		h.Write([]byte(w))
		v[h.Sum32()%64]++
	}
	return v, nil
}

func smallConv() Conversation {
	return Conversation{ID: "c", Turns: []Turn{
		{DiaID: "D1:1", Speaker: "Ann", Date: "2023-05-08", Text: "I joined the pottery class downtown last week, it is great."},
		{DiaID: "D1:2", Speaker: "Bob", Date: "2023-05-08", Text: "short one"},
		{DiaID: "D1:3", Speaker: "Ann", Date: "2023-05-08", Text: "We adopted a puppy named Rex and he loves long walks outside."},
	}}
}

func TestDirectDBKeepsShortTurnsAndCachesEmbeddings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.db")
	emb := fakeEmb{}
	d, err := BuildDirectDB(path, smallConv(), emb)
	if err != nil {
		t.Fatal(err)
	}
	var n, ne int
	d.QueryRow(`SELECT COUNT(*) FROM chunks`).Scan(&n)
	d.QueryRow(`SELECT COUNT(*) FROM embeddings`).Scan(&ne)
	d.Close()
	if n != 3 || ne != 3 {
		t.Fatalf("chunks=%d embeddings=%d, want 3/3 (direct mode must keep the short turn)", n, ne)
	}
	// Rebuild with an embedder that fails: cached vectors must be reused.
	d2, err := BuildDirectDB(path, smallConv(), fakeEmb{down: true})
	if err != nil {
		t.Fatalf("rebuild with cache: %v", err)
	}
	d2.Close()
}

func TestMineDBDropsShortTurns(t *testing.T) {
	d, err := BuildMineDB(filepath.Join(t.TempDir(), "c.db"), smallConv(), fakeEmb{})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var n int
	d.QueryRow(`SELECT COUNT(*) FROM chunks`).Scan(&n)
	if n != 2 {
		t.Fatalf("chunks = %d, want 2 (the <30-char turn is noise-filtered by mine)", n)
	}
}

func TestEvaluateCosineAndBM25OverDirectDB(t *testing.T) {
	conv := smallConv()
	conv.QAs = []QA{{Question: "pottery class", Evidence: []string{"D1:1"}, Category: 1}}
	d, err := BuildDirectDB(filepath.Join(t.TempDir(), "c.db"), conv, fakeEmb{})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	idx := ContentIndex(conv.Turns)
	for name, r := range map[string]Retriever{
		"cosine": DBRetriever(d, fakeEmb{}),
		"bm25":   DBRetriever(d, NoEmbedder{}),
	} {
		qs, err := Evaluate(conv, idx, r, []int{1, 3})
		if err != nil {
			t.Fatal(err)
		}
		if len(qs) != 1 || qs[0].J.FirstHit != 1 {
			t.Fatalf("%s: qs = %+v", name, qs)
		}
		if qs[0].ContextTokens <= 0 {
			t.Fatalf("%s: context tokens not measured", name)
		}
	}
}
