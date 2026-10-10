package blobstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// notFoundBody is the answer an S3-compatible endpoint gives for an object that
// is not there.
const notFoundBody = `<?xml version="1.0" encoding="UTF-8"?>
<Error><Code>NoSuchKey</Code><Message>The specified key does not exist.</Message></Error>`

// TestS3NotFoundIsErrNotFound pins that a 404 is reported as ErrNotFound from
// every operation that can see one. A missing body is the one failure the
// importer latches on rather than retries, so a client that returned a generic
// error here would have the sync loop asking a bucket for an object that will
// never arrive, forever.
func TestS3NotFoundIsErrNotFound(t *testing.T) {
	const missing = "laptop/" + "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, notFoundBody)
	}))
	defer srv.Close()
	store := NewS3Store(S3Config{Endpoint: srv.URL, Bucket: "bodies", KeyID: "k", Secret: "s"})
	ctx := context.Background()

	if _, err := store.Get(ctx, missing, io.Discard); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get on a missing object = %v, want ErrNotFound", err)
	}
	if err := store.Put(ctx, missing, strings.NewReader("x"), 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("Put refused with %v, want ErrNotFound", err)
	}
	if _, err := store.List(ctx, "laptop/"); !errors.Is(err, ErrNotFound) {
		t.Errorf("List on a missing prefix = %v, want ErrNotFound", err)
	}

	// Has is the one operation that reports absence as a value rather than an
	// error, and Delete the one that treats it as success.
	ok, err := store.Has(ctx, missing)
	if err != nil || ok {
		t.Errorf("Has on a missing object = (%v, %v), want (false, nil)", ok, err)
	}
	if err := store.Delete(ctx, missing); err != nil {
		t.Errorf("Delete of a missing object = %v, want nil", err)
	}

	// The refusal carries the status and the endpoint's own code, which says
	// which failure it was in a way the status alone does not.
	_, err = store.Get(ctx, missing, io.Discard)
	var se *s3Error
	if !errors.As(err, &se) {
		t.Fatalf("Get error is %T, want an *s3Error", err)
	}
	if se.Status != http.StatusNotFound || se.Code != "NoSuchKey" {
		t.Errorf("error = (status %d, code %q), want (404, NoSuchKey)", se.Status, se.Code)
	}
}

// TestS3CarriesNonNotFoundStatus pins that a failure that is not a missing
// object keeps its own status and code rather than collapsing into ErrNotFound:
// a refused request and a retryable one are handled in opposite ways.
func TestS3CarriesNonNotFoundStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `<Error><Code>SignatureDoesNotMatch</Code><Message>bad signature</Message></Error>`)
	}))
	defer srv.Close()
	store := NewS3Store(S3Config{Endpoint: srv.URL, Bucket: "bodies", KeyID: "k", Secret: "s"})

	_, err := store.Get(context.Background(), "laptop/"+strings.Repeat("d", 64), io.Discard)
	if err == nil {
		t.Fatal("Get against a 403 returned no error")
	}
	if errors.Is(err, ErrNotFound) {
		t.Error("a 403 reported as ErrNotFound, which would latch instead of retrying")
	}
	var se *s3Error
	if !errors.As(err, &se) {
		t.Fatalf("Get error is %T, want an *s3Error", err)
	}
	if se.Status != http.StatusForbidden || se.Code != "SignatureDoesNotMatch" {
		t.Errorf("error = (status %d, code %q), want (403, SignatureDoesNotMatch)", se.Status, se.Code)
	}
	if !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "SignatureDoesNotMatch") {
		t.Errorf("error text = %q, want it to carry the status and the code", err)
	}
}

// TestS3SecretNeverAppearsInAnError pins that the credential cannot reach a log
// through an error. An error from this client is written to the daemon's log and
// shown in `db sync status`, so a signature computation that quoted its own
// inputs would publish the bucket's secret to anyone reading them.
func TestS3SecretNeverAppearsInAnError(t *testing.T) {
	const secret = "SUPERSECRET-bucket-password-2f8a1c"

	cases := []struct {
		name       string
		closeFirst bool
		handler    http.HandlerFunc
		call       func(*S3Store) error
	}{
		{
			name: "a status the endpoint refuses",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusForbidden)
				_, _ = io.WriteString(w, `<Error><Code>SignatureDoesNotMatch</Code>`+
					`<Message>The request signature we calculated does not match. secret=`+secret+`</Message></Error>`)
			},
			call: func(s *S3Store) error {
				_, err := s.Get(context.Background(), "laptop/"+strings.Repeat("e", 64), io.Discard)
				return err
			},
		},
		{
			name: "an answer with no parseable body",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(w, "upstream said no")
			},
			call: func(s *S3Store) error {
				_, err := s.Get(context.Background(), "laptop/"+strings.Repeat("f", 64), io.Discard)
				return err
			},
		},
		{
			name: "an endpoint that is not there",
			// The server is closed before the call, so the transport fails with
			// no answer at all: the one failure with no S3 code to quote.
			closeFirst: true,
			handler:    func(w http.ResponseWriter, r *http.Request) {},
			call: func(s *S3Store) error {
				return s.Put(context.Background(), "laptop/"+strings.Repeat("1", 64), strings.NewReader("x"), 1)
			},
		},
	}
	for _, c := range cases {
		srv := httptest.NewServer(c.handler)
		store := NewS3Store(S3Config{Endpoint: srv.URL, Bucket: "bodies", KeyID: "key-id", Secret: secret})
		if c.closeFirst {
			srv.Close()
		}

		err := c.call(store)
		srv.Close()
		if err == nil {
			t.Errorf("%s: the call returned no error", c.name)
			continue
		}
		// The endpoint's own message is echoed, so a server that quoted the
		// secret in it cannot get the secret into a log through its own text.
		if strings.Contains(err.Error(), secret) {
			t.Errorf("%s: the error carries the secret: %v", c.name, err)
		}
	}
}

// TestS3ListFollowsContinuationToken pins that a listing spans every page. A
// bucket with a million objects answers in pages, and a client that read only
// the first would let the weekly cleanup delete every object past page one as
// unreferenced.
func TestS3ListFollowsContinuationToken(t *testing.T) {
	var seenTokens []string
	pages := []struct {
		token    string
		truncate bool
		next     string
		keys     []string
	}{
		{"", true, "token-1", []string{"laptop/" + strings.Repeat("1", 64), "laptop/" + strings.Repeat("2", 64)}},
		{"token-1", true, "token-2", []string{"laptop/" + strings.Repeat("3", 64)}},
		{"token-2", false, "", []string{"contabo/" + strings.Repeat("9", 64)}},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.URL.Query().Get("continuation-token")
		seenTokens = append(seenTokens, token)

		var page struct {
			truncate bool
			next     string
			keys     []string
		}
		for _, p := range pages {
			if p.token == token {
				page.truncate, page.next, page.keys = p.truncate, p.next, p.keys
			}
		}
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w, listXML(page.truncate, page.next, page.keys))
	}))
	defer srv.Close()

	store := NewS3Store(S3Config{Endpoint: srv.URL, Bucket: "bodies", KeyID: "k", Secret: "s"})
	got, err := store.List(context.Background(), "laptop/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	// The third page answers with a key outside the prefix, as a server that
	// ignores the prefix would. The client sorts what it gets and leaves the
	// filtering to the endpoint, so the ordering below is the one that matters:
	// key order across all three pages.
	want := []string{
		"contabo/" + strings.Repeat("9", 64),
		"laptop/" + strings.Repeat("1", 64),
		"laptop/" + strings.Repeat("2", 64),
		"laptop/" + strings.Repeat("3", 64),
	}
	if len(got) != len(want) {
		t.Fatalf("List returned %d objects, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Key != want[i] {
			t.Errorf("object %d = %s, want %s", i, got[i].Key, want[i])
		}
	}
	// The pages were walked in order, each with the token the last one handed
	// back, which is the only way a real listing advances.
	if len(seenTokens) != 3 || seenTokens[0] != "" || seenTokens[1] != "token-1" || seenTokens[2] != "token-2" {
		t.Errorf("continuation tokens seen = %v, want [\"\" token-1 token-2]", seenTokens)
	}
}

// listXML renders the part of a ListObjectsV2 answer the client reads. The
// bucket name and the last-modified stamps are included because a client that
// ignored the size or the timestamp would still pass on keys alone.
func listXML(truncated bool, next string, keys []string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><ListBucketResult>`)
	fmt.Fprintf(&b, "<IsTruncated>%t</IsTruncated>", truncated)
	if next != "" {
		fmt.Fprintf(&b, "<NextContinuationToken>%s</NextContinuationToken>", next)
	}
	for i, k := range keys {
		fmt.Fprintf(&b, "<Contents><Key>%s</Key><Size>%d</Size><LastModified>2026-10-10T09:00:00.000Z</LastModified></Contents>",
			k, (i+1)*10)
	}
	b.WriteString(`</ListBucketResult>`)
	return b.String()
}

// TestS3HasAndDeleteUseTheirOwnVerbs pins that Has is a HEAD and Delete a
// DELETE. A Has sent as a GET would pull a whole body across the wire to answer
// a yes-or-no question, and bodies are the largest thing this store moves.
func TestS3HasAndDeleteUseTheirOwnVerbs(t *testing.T) {
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	store := NewS3Store(S3Config{Endpoint: srv.URL, Bucket: "bodies", KeyID: "k", Secret: "s"})
	ctx := context.Background()
	key := "laptop/" + strings.Repeat("a", 64)

	ok, err := store.Has(ctx, key)
	if err != nil || !ok {
		t.Fatalf("Has = (%v, %v), want (true, nil)", ok, err)
	}
	if err := store.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	want := []string{"HEAD /bodies/" + key, "DELETE /bodies/" + key}
	for i := range want {
		if i >= len(methods) || methods[i] != want[i] {
			t.Fatalf("requests = %v, want %v", methods, want)
		}
	}
}

// TestS3GetReturnsTheBodyAndItsLength pins the write-through: what a Put stored
// comes back from a Get byte for byte, and the returned count is what a caller
// checks against the length the ref claims.
func TestS3GetReturnsTheBodyAndItsLength(t *testing.T) {
	body := strings.Repeat("runner stream body ", 400)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			_, _ = io.WriteString(w, "")
		default:
			_, _ = io.WriteString(w, body)
		}
	}))
	defer srv.Close()

	store := NewS3Store(S3Config{Endpoint: srv.URL, Bucket: "bodies", KeyID: "k", Secret: "s"})
	ctx := context.Background()
	key := "laptop/" + strings.Repeat("b", 64)

	if err := store.Put(ctx, key, strings.NewReader(body), int64(len(body))); err != nil {
		t.Fatalf("Put: %v", err)
	}
	var got strings.Builder
	n, err := store.Get(ctx, key, &got)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if n != int64(len(body)) {
		t.Errorf("Get returned %d, want %d", n, len(body))
	}
	if got.String() != body {
		t.Error("Get returned different bytes than Put stored")
	}
}

// TestS3RejectsABodyOfTheWrongLength pins that Put refuses a body whose length
// is not the one the caller declared. The object is addressed by its digest, so
// a body stored under a length its key does not describe would be fetched
// later, fail the importer's check, and latch the origin.
func TestS3RejectsABodyOfTheWrongLength(t *testing.T) {
	var reached bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	store := NewS3Store(S3Config{Endpoint: srv.URL, Bucket: "bodies", KeyID: "k", Secret: "s"})
	err := store.Put(context.Background(), "laptop/"+strings.Repeat("c", 64), strings.NewReader("four"), 99)
	if err == nil {
		t.Fatal("Put with a miscounted size returned no error")
	}
	if reached {
		t.Error("a refused Put still reached the endpoint")
	}
}

// TestS3ClockIsInjectable pins that the signing timestamp comes from the
// injected clock, which is what lets the signature be checked against a fixed
// vector at all: a signer reading the wall clock could only be tested by
// asserting that two signatures written close together were equal.
func TestS3ClockIsInjectable(t *testing.T) {
	var stamp string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stamp = r.Header.Get("x-amz-date")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	at := time.Date(2026, 10, 10, 9, 30, 0, 0, time.UTC)
	store := NewS3Store(S3Config{Endpoint: srv.URL, Bucket: "bodies", KeyID: "k", Secret: "s"})
	store.now = func() time.Time { return at }

	if _, err := store.Has(context.Background(), "laptop/"+strings.Repeat("d", 64)); err != nil {
		t.Fatalf("Has: %v", err)
	}
	if want := "20261010T093000Z"; stamp != want {
		t.Errorf("x-amz-date = %s, want the injected clock's %s", stamp, want)
	}
}
