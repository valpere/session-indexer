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
