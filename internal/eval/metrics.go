package eval

// Judgement is the outcome of one question against one ranked result list.
type Judgement struct {
	FirstHit int          // 1-based rank of the first evidence-bearing result; 0 = none
	AnyAt    map[int]bool // k -> some evidence turn is within the top k
	AllAt    map[int]bool // k -> every evidence turn is within the top k
}

// Judge scores one question. ranked[i] holds the dialogue ids the i-th result
// maps to (a result text shared by several turns maps to several ids, so any
// of them counts — slightly optimistic on duplicated utterances like "Thanks!").
func Judge(evidence []string, ranked [][]string, ks []int) Judgement {
	want := map[string]bool{}
	for _, e := range evidence {
		want[e] = true
	}
	j := Judgement{AnyAt: map[int]bool{}, AllAt: map[int]bool{}}
	for i, ids := range ranked {
		for _, id := range ids {
			if want[id] {
				j.FirstHit = i + 1
				break
			}
		}
		if j.FirstHit != 0 {
			break
		}
	}
	for _, k := range ks {
		got := map[string]bool{}
		for i := 0; i < k && i < len(ranked); i++ {
			for _, id := range ranked[i] {
				if want[id] {
					got[id] = true
				}
			}
		}
		j.AnyAt[k] = len(got) > 0
		j.AllAt[k] = len(want) > 0 && len(got) == len(want)
	}
	return j
}

// QResult is one question's judgement plus cost measurements.
type QResult struct {
	Category      int
	J             Judgement
	LatencyMs     float64
	ContextTokens int // est. tokens (chars/4) of the top-contextK results
}

// Summary aggregates QResults.
type Summary struct {
	N                int
	RecallAny        map[int]float64
	RecallAll        map[int]float64
	MRR              float64
	AvgLatencyMs     float64
	AvgContextTokens float64
}

// Summarize averages qs; an empty slice yields a zero Summary (no NaN).
func Summarize(qs []QResult, ks []int) Summary {
	s := Summary{N: len(qs), RecallAny: map[int]float64{}, RecallAll: map[int]float64{}}
	if len(qs) == 0 {
		return s
	}
	n := float64(len(qs))
	any, all := map[int]int{}, map[int]int{}
	var mrr, lat, ctx float64
	for _, q := range qs {
		for _, k := range ks {
			if q.J.AnyAt[k] {
				any[k]++
			}
			if q.J.AllAt[k] {
				all[k]++
			}
		}
		if q.J.FirstHit > 0 {
			mrr += 1 / float64(q.J.FirstHit)
		}
		lat += q.LatencyMs
		ctx += float64(q.ContextTokens)
	}
	for _, k := range ks {
		s.RecallAny[k] = float64(any[k]) / n
		s.RecallAll[k] = float64(all[k]) / n
	}
	s.MRR, s.AvgLatencyMs, s.AvgContextTokens = mrr/n, lat/n, ctx/n
	return s
}
