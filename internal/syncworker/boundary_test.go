package syncworker

import (
	"errors"
	"testing"
)

func signedEntry(pk string) Entry {
	e := upsertEntry("origin-a", "board", pk, `{"title":"signed"}`)
	e.MAC = []byte("a signature")
	return e
}

func logRows(t *testing.T, path string) int {
	t.Helper()
	var n int
	if err := openAt(t, path).QueryRow("SELECT COUNT(*) FROM log").Scan(&n); err != nil {
		t.Fatalf("count the log: %v", err)
	}
	return n
}

func TestWorkerRefusesAMispredictedBoundary(t *testing.T) {
	path := tempReplica(t)
	b := backendFor(t, &localDriver{path: path}, "origin-a")
	key := []byte("the passphrase this machine stretched")
	if _, err := b.Append([]Entry{upsertEntry("origin-a", "board", `["u"]`, `{"title":"one"}`)}); err != nil {
		t.Fatalf("append the unsigned entry: %v", err)
	}

	// The batch gets seq 2, the exporter said 3.
	_, err := b.AppendSigned([]Entry{signedEntry(`["s"]`)}, boundaryAt(key, "origin-a", 3))
	if !errors.Is(err, ErrBoundary) || RefusalCodeOf(err) != CodeBoundary {
		t.Fatalf("append = %v (code %q), want ErrBoundary under CodeBoundary", err, RefusalCodeOf(err))
	}
	if n := logRows(t, path); n != 1 {
		t.Errorf("the log holds %d rows, want 1: a refused append wrote entries", n)
	}
	if _, ok := signedFromMeta(t, path, "origin-a"); ok {
		t.Error("a refused append wrote the boundary row")
	}
}

func TestWorkerRefusesASignedBatchWithoutABoundaryOnAnUnsignedOrigin(t *testing.T) {
	path := tempReplica(t)
	b := backendFor(t, &localDriver{path: path}, "origin-a")

	_, err := b.Append([]Entry{signedEntry(`["s"]`)})
	if !errors.Is(err, ErrBoundary) {
		t.Fatalf("append = %v, want ErrBoundary", err)
	}
	if n := logRows(t, path); n != 0 {
		t.Errorf("the log holds %d rows after the refusal", n)
	}
}

func TestWorkerTakesASignedBatchWithoutABoundaryOnceTheOriginHasOne(t *testing.T) {
	path := tempReplica(t)
	b := backendFor(t, &localDriver{path: path}, "origin-a")
	key := []byte("the passphrase this machine stretched")
	if _, err := b.AppendSigned([]Entry{signedEntry(`["s"]`)}, boundaryAt(key, "origin-a", 1)); err != nil {
		t.Fatalf("first signed append: %v", err)
	}
	if _, err := b.Append([]Entry{signedEntry(`["t"]`)}); err != nil {
		t.Fatalf("second signed append: %v", err)
	}
}

func TestParseBoundaryReadsOnlyWhatParses(t *testing.T) {
	for value, want := range map[string]struct {
		seq int
		tag bool
	}{
		"2 AAAA":    {2, true},
		"2":         {2, false},
		"2 !!":      {2, false},
		"2 AAAA x":  {2, false},
		"-1 AAAA":   {0, false},
		"nope AAAA": {0, false},
		"":          {0, false},
	} {
		seq, tag := parseBoundary(value)
		if seq != want.seq || (tag != nil) != want.tag {
			t.Errorf("parseBoundary(%q) = %d, %x; want seq %d, tag %v", value, seq, tag, want.seq, want.tag)
		}
	}
}

func TestWorkerRefusesABoundaryWithoutAFullTag(t *testing.T) {
	path := tempReplica(t)
	b := backendFor(t, &localDriver{path: path}, "origin-a")

	for name, tag := range map[string][]byte{"no tag": nil, "short tag": {1, 2, 3}} {
		_, err := b.AppendSigned([]Entry{signedEntry(`["s"]`)}, Boundary{Seq: 1, Tag: tag})
		if !errors.Is(err, ErrBoundary) {
			t.Errorf("%s: append = %v, want ErrBoundary", name, err)
		}
	}
}
