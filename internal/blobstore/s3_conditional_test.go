package blobstore

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// preconditionFailedBody is the answer an S3-compatible endpoint gives a
// create-only PUT whose key is already there.
const preconditionFailedBody = `<?xml version="1.0" encoding="UTF-8"?>
<Error><Code>PreconditionFailed</Code><Message>At least one of the pre-conditions you specified did not hold.</Message></Error>`

// conditionalConflictBody is the 409 variant the same refusal takes on
// endpoints that answer a conflicting conditional write with a conflict status.
const conditionalConflictBody = `<?xml version="1.0" encoding="UTF-8"?>
<Error><Code>ConditionalRequestConflict</Code><Message>The conditional request conflicted with the object.</Message></Error>`

// condBucket is a fake endpoint holding one object per key, refusing a PUT that
// carries If-None-Match: * on an existing key the way a bucket does, and
// counting the requests that reach it. The map is the test's window on what was
// stored, so the assertions read the store itself rather than a fetch.
type condBucket struct {
	store    *S3Store
	mu       sync.Mutex
	objects  map[string][]byte
	requests int
	sawCond  string
}

func newCondBucket(t *testing.T) *condBucket {
	t.Helper()
	b := &condBucket{objects: make(map[string][]byte)}
	srv := httptest.NewServer(b.handler())
	t.Cleanup(srv.Close)
	b.store = NewS3Store(S3Config{Endpoint: srv.URL, Bucket: "bodies", KeyID: "k", Secret: "s"})
	return b
}

func (b *condBucket) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.requests++
		b.sawCond = r.Header.Get("If-None-Match")
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPut {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if b.sawCond != "" {
			if _, ok := b.objects[r.URL.Path]; ok {
				w.WriteHeader(http.StatusPreconditionFailed)
				_, _ = io.WriteString(w, preconditionFailedBody)
				return
			}
		}
		b.objects[r.URL.Path] = body
		w.WriteHeader(http.StatusOK)
	}
}

// TestS3PutIfAbsentCreates pins the winning create: the request carried the
// If-None-Match condition, the endpoint held nothing under the key, and the
// body landed exactly as sent.
func TestS3PutIfAbsentCreates(t *testing.T) {
	b := newCondBucket(t)
	key := "laptop/" + strings.Repeat("a", 64)
	body := "the body the first writer sent"

	created, err := b.store.PutIfAbsent(context.Background(), key, strings.NewReader(body), int64(len(body)))
	if err != nil || !created {
		t.Fatalf("PutIfAbsent = (%v, %v), want (true, nil)", err, created)
	}
	if b.sawCond != "*" {
		t.Errorf("the PUT carried If-None-Match %q, want *", b.sawCond)
	}
	if got := string(b.objects["/bodies/"+key]); got != body {
		t.Errorf("stored %q, want %q", got, body)
	}
}

// TestS3PutIfAbsentExisting pins the refusal: an existing key answers
// created=false with no error, and the object the first writer stored is
// untouched. Dropping the header or reading the 412 as a fault would turn this
// red.
func TestS3PutIfAbsentExisting(t *testing.T) {
	b := newCondBucket(t)
	key := "laptop/" + strings.Repeat("b", 64)
	const first = "the body the first writer stored"
	b.objects["/bodies/"+key] = []byte(first)

	created, err := b.store.PutIfAbsent(context.Background(), key, strings.NewReader("a second writer's body"), 22)
	if err != nil || created {
		t.Errorf("PutIfAbsent on an existing key = (%v, %v), want (false, nil)", err, created)
	}
	if got := string(b.objects["/bodies/"+key]); got != first {
		t.Errorf("the stored body changed to %q", got)
	}
}

// TestS3PutIfAbsentConflict409 pins that the conflict variant of the refusal is
// read the same way as 412: not created, no error. Endpoints differ on which of
// the two statuses a conditional write conflict takes, so reading only one would
// turn another endpoint's ordinary refusal into a retryable fault.
func TestS3PutIfAbsentConflict409(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, conditionalConflictBody)
	}))
	defer srv.Close()
	store := NewS3Store(S3Config{Endpoint: srv.URL, Bucket: "bodies", KeyID: "k", Secret: "s"})

	created, err := store.PutIfAbsent(context.Background(),
		"laptop/"+strings.Repeat("c", 64), strings.NewReader("x"), 1)
	if err != nil || created {
		t.Errorf("a 409 conflict = (%v, %v), want (false, nil)", err, created)
	}
}

// TestS3PutIfAbsentOtherErrors pins that only the two conflict statuses read as
// a refusal of the create: anything else stays an error, classified as it would
// be for the other verbs, and the secret never rides out through the text.
func TestS3PutIfAbsentOtherErrors(t *testing.T) {
	const secret = "SUPERSECRET-conditional-password-3b7d"
	key := "laptop/" + strings.Repeat("d", 64)

	cases := []struct {
		name   string
		status int
		body   string
		match  error
	}{
		{
			name:   "a 500",
			status: http.StatusInternalServerError,
			body:   "upstream said no",
		},
		{
			name:   "a 404",
			status: http.StatusNotFound,
			body:   notFoundBody,
			match:  ErrNotFound,
		},
		{
			name:   "a 403",
			status: http.StatusForbidden,
			body:   `<Error><Code>SignatureDoesNotMatch</Code><Message>secret=` + secret + `</Message></Error>`,
			match:  ErrCredentialsRefused,
		},
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.status)
			_, _ = io.WriteString(w, c.body)
		}))
		store := NewS3Store(S3Config{Endpoint: srv.URL, Bucket: "bodies", KeyID: "key-id", Secret: secret})
		created, err := store.PutIfAbsent(context.Background(), key, strings.NewReader("x"), 1)
		srv.Close()

		if err == nil {
			t.Errorf("%s: the call returned no error", c.name)
			continue
		}
		if created {
			t.Errorf("%s: an error read as created", c.name)
		}
		if c.match != nil && !errors.Is(err, c.match) {
			t.Errorf("%s: error %v does not match %v", c.name, err, c.match)
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("%s: the error carries the secret: %v", c.name, err)
		}
	}
}

// TestS3PutIfAbsentWrongSize pins that the size is checked as in Put, before any
// request leaves: a body of the wrong length is refused with the endpoint never
// asked, because no answer from it could change what the caller meant to send.
func TestS3PutIfAbsentWrongSize(t *testing.T) {
	b := newCondBucket(t)
	key := "laptop/" + strings.Repeat("e", 64)
	before := b.requests

	created, err := b.store.PutIfAbsent(context.Background(), key, strings.NewReader("four"), 99)
	if err == nil {
		t.Fatal("a miscounted size returned no error")
	}
	if created {
		t.Error("an error read as created")
	}
	if b.requests != before {
		t.Error("a refused size check still reached the endpoint")
	}
}

// TestS3PutStaysUnconditional pins that a plain Put carries no If-None-Match
// header and overwrites: overwrite is what Put promises its callers, and a
// bucket that saw the condition would turn every second put of an unchanged
// body into a 412.
func TestS3PutStaysUnconditional(t *testing.T) {
	b := newCondBucket(t)
	key := "laptop/" + strings.Repeat("f", 64)
	b.objects["/bodies/"+key] = []byte("the object already there")

	if err := b.store.Put(context.Background(), key, strings.NewReader("the overwriting body"), 20); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if b.sawCond != "" {
		t.Errorf("a plain Put carried If-None-Match %q", b.sawCond)
	}
	if got := string(b.objects["/bodies/"+key]); got != "the overwriting body" {
		t.Errorf("stored %q, want the overwrite", got)
	}
}
