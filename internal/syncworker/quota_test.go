package syncworker

import (
	"errors"
	"testing"
)

func TestQuotaCodeMarksARefusal(t *testing.T) {
	t.Parallel()
	cases := map[string]RefusalCode{
		"sync engine: push failed: BLOCKED: monthly write limit": CodeQuota,
		"BLOCKED":                  CodeQuota,
		"sync engine: UNBLOCKED":   "",
		"sync engine: blocked":     "",
		"sync engine: BLOCKEDNESS": "",
	}
	for text, want := range cases {
		if got := RefusalCodeOf(markDriverRefusal(errors.New(text))); got != want {
			t.Errorf("%q marked %q, want %q", text, got, want)
		}
	}
}

func TestDatabaseFullIsNotQuota(t *testing.T) {
	t.Parallel()
	err := markDriverRefusal(errors.New("sync engine: TURSO_DATABASE_FULL: database or disk is full"))
	if got := RefusalCodeOf(err); got == CodeQuota {
		t.Fatalf("a full local disk was marked %q", got)
	}
}
