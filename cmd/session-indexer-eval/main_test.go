package main

import "testing"

func TestValidateModes(t *testing.T) {
	if err := validateModes([]string{"cosine", "bm25", "naive", "cosine-mine", "bm25-mine"}); err != nil {
		t.Fatalf("known modes rejected: %v", err)
	}
	for _, bad := range [][]string{{"bm-25"}, {""}, {"cosine", "typo"}} {
		if err := validateModes(bad); err == nil {
			t.Errorf("validateModes(%q) accepted an unknown mode", bad)
		}
	}
}

func TestValidateModesAcceptsHybrid(t *testing.T) {
	if err := validateModes([]string{"hybrid", "hybrid-mine"}); err != nil {
		t.Fatalf("hybrid modes rejected: %v", err)
	}
}

func TestValidateRealModesRejectsMineModes(t *testing.T) {
	if err := validateRealModes([]string{"cosine", "bm25", "naive", "hybrid"}); err != nil {
		t.Fatalf("real modes rejected: %v", err)
	}
	// "-mine" modes re-ingest a dataset through the parser; a real store is already mined.
	if err := validateRealModes([]string{"cosine-mine"}); err == nil {
		t.Fatal("cosine-mine must be rejected for a real store")
	}
}
