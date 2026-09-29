package db

import (
	"sort"
	"testing"
	"time"
)

func TestNewIDShapeAndUniqueness(t *testing.T) {
	const n = 1000
	ids := make([]string, n)
	seen := make(map[string]bool, n)

	for i := 0; i < n; i++ {
		id := NewID()
		if len(id) != 26 {
			t.Fatalf("id %q has length %d, want 26", id, len(id))
		}
		for _, c := range id {
			if !isCrockford(byte(c)) {
				t.Fatalf("id %q has non-Crockford-base32 character %q", id, c)
			}
		}
		if seen[id] {
			t.Fatalf("id %q minted twice", id)
		}
		seen[id] = true
		ids[i] = id
	}

	first := NewID()
	time.Sleep(2 * time.Millisecond)
	second := NewID()

	sorted := []string{second, first}
	sort.Strings(sorted)
	if sorted[0] != first {
		t.Errorf("sorted order = %v, want %q before %q (time order)", sorted, first, second)
	}
}
