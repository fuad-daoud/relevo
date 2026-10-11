package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/blobstore"
)

// growingStore answers a Get with far more than the listing said the object
// held, as an object swapped after the listing, or a hostile endpoint, would.
// The body is a valid size followed by a megabyte of whitespace, which JSON
// accepts, so only the read limit tells it from a real one.
type growingStore struct {
	*blobstore.MemBlobStore
}

func (g growingStore) Get(_ context.Context, _ string, w io.Writer) (int64, error) {
	body := append([]byte(`{"entries":1,"bytes":5000}`), bytes.Repeat([]byte(" "), 1<<20)...)
	n, err := w.Write(body)
	return int64(n), err
}

// The size cap holds on the bytes read, not on the size the listing reported.
func TestEstimateStopsReadingAnObjectPastTheSizeCap(t *testing.T) {
	t.Parallel()
	store := guardBucket(t, 0, map[string]int64{"m2": 5000})
	est, err := EstimateJoin(context.Background(), growingStore{store})
	if err != nil {
		t.Fatalf("EstimateJoin: %v", err)
	}
	if est.Known || est.TursoBytes != 0 {
		t.Errorf("estimate = %+v, want the oversized object ignored", est)
	}
}

// A size no log has is not believed: two near the int64 limit would overflow
// the sum into a negative number the guard lets through.
func TestEstimateIgnoresASizeNoLogHas(t *testing.T) {
	t.Parallel()
	store := guardBucket(t, 0, map[string]int64{"m2": math.MaxInt64, "m3": math.MaxInt64, "m4": 700})
	est, err := EstimateJoin(context.Background(), store)
	if err != nil {
		t.Fatalf("EstimateJoin: %v", err)
	}
	if est.TursoBytes != 700 {
		t.Fatalf("estimate = %+v, want only the believable 700", est)
	}
	if err := GuardJoin(est, 1000, 400); err == nil {
		t.Error("a join of 700 against 600 left was let through")
	}
}

// The estimate reads at most maxSizeKeys size objects, however many the bucket
// lists.
func TestEstimateReadsABoundedNumberOfSizes(t *testing.T) {
	t.Parallel()
	store := blobstore.NewMemBlobStore(nil)
	body, _ := json.Marshal(LogSize{Entries: 1, Bytes: 1, At: time.Unix(0, 0).UTC()})
	for i := range maxSizeKeys + 5 {
		origin := "o" + string(rune('a'+i%26)) + string(rune('a'+i/26%26)) + string(rune('a'+i/676))
		if err := store.Put(context.Background(), blobstore.SizeKey(origin), bytes.NewReader(body), int64(len(body))); err != nil {
			t.Fatalf("put: %v", err)
		}
	}
	est, err := EstimateJoin(context.Background(), store)
	if err != nil {
		t.Fatalf("EstimateJoin: %v", err)
	}
	if est.TursoBytes != maxSizeKeys {
		t.Errorf("estimate read %d sizes, want %d", est.TursoBytes, maxSizeKeys)
	}
}
