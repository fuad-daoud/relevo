package blobstore

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

// TestKeyValidatesBothHalves pins the key shape and the refusals: an origin
// holding a separator would put one machine's objects under another's prefix,
// where the weekly cleanup would delete them, and a digest that is not exactly
// 64 lowercase hex characters names an object that was never written.
func TestKeyValidatesBothHalves(t *testing.T) {
	const digest = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

	if got, want := Key("laptop", digest), "laptop/"+digest; got != want {
		t.Errorf("Key = %q, want %q", got, want)
	}

	bad := []struct {
		name       string
		origin     string
		digest     string
		wantPanics bool
	}{
		{"empty origin", "", digest, true},
		{"origin with a slash", "lap/top", digest, true},
		{"empty digest", "laptop", "", true},
		{"short digest", "laptop", strings.Repeat("a", 63), true},
		{"long digest", "laptop", strings.Repeat("a", 65), true},
		{"uppercase digest", "laptop", strings.ToUpper(digest), true},
		{"non-hex digest", "laptop", strings.Repeat("g", 64), true},
	}
	for _, c := range bad {
		func() {
			defer func() {
				if recover() == nil && c.wantPanics {
					t.Errorf("%s: Key(%q, %q) did not panic", c.name, c.origin, c.digest)
				}
			}()
			_ = Key(c.origin, c.digest)
		}()
	}
}

// TestMemBlobStoreRoundTrip pins the fake against the interface the S3 store
// implements: a body survives a Put and a Get byte for byte, a copy is taken on
// the way in, an absent key reports ErrNotFound rather than an empty body, and
// a listing returns only its own prefix in key order.
func TestMemBlobStoreRoundTrip(t *testing.T) {
	clock := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	m := NewMemBlobStore(func() time.Time { return clock })
	ctx := context.Background()

	const digest = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	key := Key("laptop", digest)
	body := []byte(strings.Repeat("runner stream body ", 300))

	// The buffer is reused after the Put: the store must hold its own copy.
	buf := bytes.Clone(body)
	if err := m.Put(ctx, key, bytes.NewReader(buf), int64(len(buf))); err != nil {
		t.Fatalf("Put: %v", err)
	}
	for i := range buf {
		buf[i] = 'x'
	}

	var got bytes.Buffer
	n, err := m.Get(ctx, key, &got)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if n != int64(len(body)) {
		t.Errorf("Get returned %d, want %d", n, len(body))
	}
	if !bytes.Equal(got.Bytes(), body) {
		t.Error("Get returned different bytes than Put stored")
	}
	if m.Puts != 1 || m.Gets != 1 {
		t.Errorf("counters = %d puts / %d gets, want 1 / 1", m.Puts, m.Gets)
	}

	ok, err := m.Has(ctx, key)
	if err != nil || !ok {
		t.Errorf("Has = (%v, %v), want (true, nil)", ok, err)
	}

	// A body of a different length than declared is refused, so a caller that
	// miscounts fails here rather than leaving an object under a length no row
	// will ever ask for.
	if err := m.Put(ctx, key, strings.NewReader("short"), 99); err == nil {
		t.Error("Put with a miscounted size was accepted")
	}

	// A listing returns one origin's prefix and leaves another's alone.
	other := Key("contabo", strings.Repeat("b", 64))
	if err := m.Put(ctx, other, strings.NewReader("other machine"), 13); err != nil {
		t.Fatalf("Put other: %v", err)
	}
	objects, err := m.List(ctx, "laptop/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(objects) != 1 || objects[0].Key != key {
		t.Fatalf("List(laptop/) = %+v, want only %s", objects, key)
	}
	if objects[0].Size != int64(len(body)) {
		t.Errorf("listed size = %d, want %d", objects[0].Size, len(body))
	}
	if !objects[0].Modified.Equal(clock) {
		t.Errorf("listed Modified = %s, want the injected clock %s", objects[0].Modified, clock)
	}

	if err := m.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if ok, _ := m.Has(ctx, key); ok {
		t.Error("Has after Delete = true, want false")
	}
	// Deleting what is already gone is the outcome the cleanup wanted.
	if err := m.Delete(ctx, key); err != nil {
		t.Errorf("Delete of an absent key = %v, want nil", err)
	}
	if _, err := m.Get(ctx, key, io.Discard); err == nil {
		t.Error("Get of an absent key returned no error")
	}
}

// TestMemBlobStoreImplementsBlobStore is the compile-time half of the fake's
// contract: the fake and the S3 store are interchangeable wherever the sync
// code takes a BlobStore, so a method added to one and missed on the other has
// to fail here rather than at the call site that needs it.
func TestMemBlobStoreImplementsBlobStore(t *testing.T) {
	var _ BlobStore = (*MemBlobStore)(nil)
	var _ BlobStore = (*S3Store)(nil)
}
