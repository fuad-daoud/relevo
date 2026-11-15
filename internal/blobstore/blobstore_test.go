package blobstore

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
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

// TestMemPutIfAbsent pins the create-only write on the fake: a first call
// creates, a second on the same key is refused without touching the object or
// the counter, the size is checked whether or not the key exists, and the store
// keeps its own copy of the bytes.
func TestMemPutIfAbsent(t *testing.T) {
	m := NewMemBlobStore(nil)
	ctx := context.Background()
	key := Key("laptop", strings.Repeat("a", 64))
	first := []byte("the body the first writer stored")

	created, err := m.PutIfAbsent(ctx, key, bytes.NewReader(first), int64(len(first)))
	if err != nil || !created {
		t.Fatalf("first PutIfAbsent = (%v, %v), want (true, nil)", err, created)
	}
	if m.Puts != 1 {
		t.Errorf("Puts = %d, want 1", m.Puts)
	}

	// A losing writer's body must not reach the store, and a refused create is
	// not a landed one.
	second := []byte("the body a second writer would have stored")
	created, err = m.PutIfAbsent(ctx, key, bytes.NewReader(second), int64(len(second)))
	if err != nil || created {
		t.Errorf("existing key = (%v, %v), want (false, nil)", err, created)
	}
	if m.Puts != 1 {
		t.Errorf("Puts after the refused create = %d, want 1", m.Puts)
	}
	var got bytes.Buffer
	if _, err := m.Get(ctx, key, &got); err != nil || !bytes.Equal(got.Bytes(), first) {
		t.Errorf("Get = %q (err %v), want the first writer's bytes", got.Bytes(), err)
	}

	// The size check sees the call itself, not the object that is there: a
	// miscounted size is refused on a fresh key and on an existing one alike,
	// and refuses means nothing was stored.
	if _, err := m.PutIfAbsent(ctx, key, strings.NewReader("mismatched"), 99); err == nil {
		t.Error("PutIfAbsent on an existing key accepted a miscounted size")
	}
	fresh := Key("laptop", strings.Repeat("b", 64))
	if _, err := m.PutIfAbsent(ctx, fresh, strings.NewReader("mismatched"), 99); err == nil {
		t.Error("PutIfAbsent on a fresh key accepted a miscounted size")
	}
	if ok, _ := m.Has(ctx, fresh); ok {
		t.Error("the refused call stored an object anyway")
	}

	// The copy is taken before the call returns: mutating the caller's slice
	// afterwards cannot change what the store holds.
	buf := bytes.Clone(first)
	if _, err := m.PutIfAbsent(ctx, fresh, bytes.NewReader(buf), int64(len(buf))); err != nil {
		t.Fatalf("PutIfAbsent: %v", err)
	}
	for i := range buf {
		buf[i] = 'x'
	}
	var again bytes.Buffer
	if _, err := m.Get(ctx, fresh, &again); err != nil || !bytes.Equal(again.Bytes(), first) {
		t.Errorf("Get = %q (err %v), want bytes the caller's mutation cannot reach", again.Bytes(), err)
	}
}

// TestMemPutIfAbsentRace pins that the existence check and the store are one
// step under the lock: sixteen writers race for one key and exactly one of them
// creates it, the loser returning with the winner's body in the store.
func TestMemPutIfAbsentRace(t *testing.T) {
	m := NewMemBlobStore(nil)
	ctx := context.Background()
	key := Key("laptop", strings.Repeat("c", 64))

	const writers = 16
	created := make([]bool, writers)
	var mu sync.Mutex
	winner := -1
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := []byte(fmt.Sprintf("writer %2d's body", i))
			c, err := m.PutIfAbsent(ctx, key, bytes.NewReader(body), int64(len(body)))
			if err != nil {
				t.Errorf("writer %d: %v", i, err)
				return
			}
			created[i] = c
			if c {
				mu.Lock()
				winner = i
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()

	count := 0
	for _, c := range created {
		if c {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("%d writers created the object, want exactly one", count)
	}
	if m.Puts != 1 {
		t.Errorf("Puts = %d, want 1", m.Puts)
	}
	var got bytes.Buffer
	if _, err := m.Get(ctx, key, &got); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if want := fmt.Sprintf("writer %2d's body", winner); !bytes.Equal(got.Bytes(), []byte(want)) {
		t.Errorf("Get = %q, want the one winner's body %q", got.Bytes(), want)
	}
}

// TestMemListAfter pins the fake's start-after behaviour, which the S3
// endpoint implements itself: strictly greater-than, in key order, so a reader
// that holds the last segment it fetched does not fetch that segment again. A
// startAfter that is not a key cuts between the keys around it, an empty one
// lists the whole prefix, and one below the prefix returns the prefix and
// nothing outside it.
func TestMemListAfter(t *testing.T) {
	m := NewMemBlobStore(nil)
	ctx := context.Background()
	keys := []string{
		"a/log/000000000001",
		"a/log/000000000257",
		"a/log/000000000513",
		"b/log/000000000001",
	}
	for _, k := range keys {
		if err := m.Put(ctx, k, strings.NewReader("x"), 1); err != nil {
			t.Fatalf("Put %s: %v", k, err)
		}
	}

	check := func(name string, got []Object, want ...string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s = %+v, want %v", name, got, want)
		}
		for i := range want {
			if got[i].Key != want[i] {
				t.Errorf("%s object %d = %s, want %s", name, i, got[i].Key, want[i])
			}
		}
	}

	// The key named is excluded, so the reader that holds it is not sent it.
	check("ListAfter from the first key",
		mustListAfter(t, m, ctx, "a/log/", "a/log/000000000001"),
		"a/log/000000000257", "a/log/000000000513")
	// Nothing sorts above the last key, so the answer is empty.
	check("ListAfter from the last key",
		mustListAfter(t, m, ctx, "a/log/", "a/log/000000000513"))
	// A startAfter that is not a key still cuts the listing at it.
	check("ListAfter from a key that is not there",
		mustListAfter(t, m, ctx, "a/log/", "a/log/000000000100"),
		"a/log/000000000257", "a/log/000000000513")
	// An empty startAfter is List.
	fromEmpty := mustListAfter(t, m, ctx, "a/log/", "")
	listed, err := m.List(ctx, "a/log/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(fromEmpty) != len(listed) {
		t.Fatalf("ListAfter with an empty startAfter = %+v, List = %+v", fromEmpty, listed)
	}
	for i := range listed {
		if fromEmpty[i].Key != listed[i].Key {
			t.Errorf("ListAfter[%d] = %s, List = %s", i, fromEmpty[i].Key, listed[i].Key)
		}
	}
	// A startAfter below the prefix returns the whole prefix and nothing
	// outside it: "a" sorts below every a/ key.
	check("ListAfter from below the prefix",
		mustListAfter(t, m, ctx, "a/log/", "a"),
		"a/log/000000000001", "a/log/000000000257", "a/log/000000000513")
}

// mustListAfter calls ListAfter so the cases above can name what they wanted.
func mustListAfter(t *testing.T, m *MemBlobStore, ctx context.Context, prefix, startAfter string) []Object {
	t.Helper()
	got, err := m.ListAfter(ctx, prefix, startAfter)
	if err != nil {
		t.Fatalf("ListAfter(%q, %q): %v", prefix, startAfter, err)
	}
	return got
}

// TestMemBlobStoreImplementsBlobStore is the compile-time half of the fake's
// contract: the fake and the S3 store are interchangeable wherever the sync
// code takes a BlobStore, so a method added to one and missed on the other has
// to fail here rather than at the call site that needs it.
func TestMemBlobStoreImplementsBlobStore(t *testing.T) {
	var _ BlobStore = (*MemBlobStore)(nil)
	var _ BlobStore = (*S3Store)(nil)
}
