// Package cli provides tests for lock CLI helpers.
package cli

import "testing"

func TestParseLockIDs(t *testing.T) {
	ids, err := parseLockIDs([]string{"1,2", "3"})
	if err != nil {
		t.Fatalf("parseLockIDs: %v", err)
	}
	if len(ids) != 3 || ids[0] != 1 || ids[1] != 2 || ids[2] != 3 {
		t.Fatalf("unexpected ids: %#v", ids)
	}

	if _, err := parseLockIDs([]string{"bad"}); err == nil {
		t.Fatalf("expected error for invalid id")
	}
}

func TestMatchesPathPattern(t *testing.T) {
	if !matchesPathPattern("src/main.go", "src/*.go") {
		t.Fatalf("expected pattern match")
	}
	if !matchesPathPattern("src/main.go", "src/main.go") {
		t.Fatalf("expected exact match")
	}
	if matchesPathPattern("src/main.go", "docs/*.md") {
		t.Fatalf("did not expect match")
	}
}
