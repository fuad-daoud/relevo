package blobstore

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"
)

// MemBlobStore is the in-memory BlobStore the tests run against: it holds bodies
// in a map, so CI needs no bucket and no network. It copies the bytes on the way
// in and on the way out, so a caller reusing its buffer after a Put cannot change
// what the store holds.
type MemBlobStore struct {
	mu      sync.Mutex
	objects map[string]memObject
	now     func() time.Time
	Puts    int
	Gets    int
}

// memObject is a stored body and the time it was written, the bytes a copy the
// map owns outright.
type memObject struct {
	body     []byte
	modified time.Time
}

// NewMemBlobStore returns an empty store clocked by now, or the wall clock when
// now is nil, which is what a caller outside a test wants.
func NewMemBlobStore(now func() time.Time) *MemBlobStore {
	if now == nil {
		now = time.Now
	}
	return &MemBlobStore{objects: make(map[string]memObject), now: now}
}

// Put stores a copy of body under key. A body shorter or longer than size is
// refused, so a miscounting caller fails here rather than leaving an object the
// importer would later read under the wrong length.
func (m *MemBlobStore) Put(_ context.Context, key string, body io.Reader, size int64) error {
	buf, err := readSized(body, size)
	if err != nil {
		return fmt.Errorf("blobstore: mem put %s: %w", key, err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = memObject{body: buf, modified: m.now().UTC()}
	m.Puts++
	return nil
}

// Get writes a copy of the body under key to w, or reports ErrNotFound.
func (m *MemBlobStore) Get(_ context.Context, key string, w io.Writer) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	obj, ok := m.objects[key]
	m.Gets++
	if !ok {
		return 0, fmt.Errorf("blobstore: mem get %s: %w", key, ErrNotFound)
	}
	n, err := w.Write(obj.body)
	if err != nil {
		return int64(n), fmt.Errorf("blobstore: mem get %s: %w", key, err)
	}
	return int64(n), nil
}

// Has reports whether key is held, without counting a read.
func (m *MemBlobStore) Has(_ context.Context, key string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.objects[key]
	return ok, nil
}

// List returns every object whose key starts with prefix, in the S3 store's key
// order, so a caller cannot tell the two apart by what it walks.
func (m *MemBlobStore) List(_ context.Context, prefix string) ([]Object, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Object
	for key, obj := range m.objects {
		if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			out = append(out, Object{Key: key, Size: int64(len(obj.body)), Modified: obj.modified})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// Delete drops key. An absent key is not an error: the weekly cleanup asks
// whether an object is still referenced by deleting it, and a body another run
// removed is the outcome it wanted.
func (m *MemBlobStore) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, key)
	return nil
}

// readSized reads body in full and checks it against the caller's expected
// length. The copy belongs to the caller, so a Put stores the bytes without
// holding a buffer it is about to reuse.
func readSized(body io.Reader, size int64) ([]byte, error) {
	buf, err := io.ReadAll(body)
	if err != nil {
		return nil, err
	}
	if int64(len(buf)) != size {
		return nil, fmt.Errorf("body is %d bytes, want %d", len(buf), size)
	}
	return bytes.Clone(buf), nil
}
