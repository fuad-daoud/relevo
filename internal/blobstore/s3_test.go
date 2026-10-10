package blobstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The values below are AWS's own documented SigV4 example for GET Object: the
// example keys, the 20130524T000000Z timestamp, and the signature the endpoint
// computes. Pinning them means the signer here agrees with a vector published
// by the service being signed for, not with a signature this package produced.
const (
	awsExampleKeyID    = "AKIAIOSFODNN7EXAMPLE"
	awsExampleSecret   = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	awsExampleStamp    = "20130524T000000Z"
	awsExampleScope    = "20130524/us-east-1/s3/aws4_request"
	awsExampleCanonSHA = "7344ae5b7ee6c3e7e6b0fe0640412a37625d1fbfff95c48bbb2dc43964946972"
	awsExampleSig      = "f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"
)

// awsExampleCanonicalRequest is the canonical request exactly as AWS prints it
// for the GET Object example: a range header is signed alongside the host and
// the two x-amz- headers, so the header set is not the minimal one this client
// normally sends.
const awsExampleCanonicalRequest = "GET\n/test.txt\n\n" +
	"host:examplebucket.s3.amazonaws.com\n" +
	"range:bytes=0-9\n" +
	"x-amz-content-sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855\n" +
	"x-amz-date:20130524T000000Z\n\n" +
	"host;range;x-amz-content-sha256;x-amz-date\n" +
	"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// TestS3SignatureMatchesKnownVector pins the signer against AWS's documented
// GET example at every step it can be wrong: the canonical request's bytes, its
// hash inside the string to sign, and the resulting signature. A signature that
// happened to match by luck would survive the first two, so both are checked.
func TestS3SignatureMatchesKnownVector(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://examplebucket.s3.amazonaws.com/test.txt", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Host = "examplebucket.s3.amazonaws.com"
	req.Header.Set("Range", "bytes=0-9")
	req.Header.Set("x-amz-content-sha256", emptyPayloadHash)
	req.Header.Set("x-amz-date", awsExampleStamp)

	names := signedHeaders(req)
	wantNames := "host;range;x-amz-content-sha256;x-amz-date"
	if got := strings.Join(names, ";"); got != wantNames {
		t.Errorf("signed headers = %q, want %q", got, wantNames)
	}

	canonical := canonicalRequest(req, emptyPayloadHash, names)
	if canonical != awsExampleCanonicalRequest {
		t.Fatalf("canonical request =\n%q\nwant\n%q", canonical, awsExampleCanonicalRequest)
	}
	if sum := sha256Hex([]byte(canonical)); sum != awsExampleCanonSHA {
		t.Errorf("canonical request hash = %s, want the documented %s", sum, awsExampleCanonSHA)
	}

	// The example signs in us-east-1 because that is the region its bucket is
	// in; this client signs auto. The signer takes the region, so the vector
	// can be checked at the region it was published for.
	at := time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)
	signRequest(req, awsExampleKeyID, awsExampleSecret, "us-east-1", "s3", emptyPayloadHash, at)

	want := "AWS4-HMAC-SHA256 Credential=" + awsExampleKeyID + "/" + awsExampleScope +
		", SignedHeaders=" + wantNames + ", Signature=" + awsExampleSig
	if got := req.Header.Get("Authorization"); got != want {
		t.Errorf("Authorization =\n%s\nwant\n%s", got, want)
	}
}

// TestS3SignatureIsStableAcrossHeaders pins that a header the transport adds on
// its own does not move the signature: the header set is fixed, so signing
// whatever happens to be present would make the same request sign differently
// depending on which defaults the http.Client had.
func TestS3SignatureIsStableAcrossHeaders(t *testing.T) {
	newReq := func() *http.Request {
		req, err := http.NewRequest(http.MethodGet, "https://example.com/bucket/k", nil)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.Header.Set("x-amz-content-sha256", emptyPayloadHash)
		return req
	}
	at := time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)

	plain := newReq()
	signRequest(plain, "k", "s", s3Region, s3Service, emptyPayloadHash, at)

	withExtras := newReq()
	withExtras.Header.Set("User-Agent", "Go-http-client/1.1")
	withExtras.Header.Set("Accept-Encoding", "gzip")
	signRequest(withExtras, "k", "s", s3Region, s3Service, emptyPayloadHash, at)

	if plain.Header.Get("Authorization") != withExtras.Header.Get("Authorization") {
		t.Error("the signature changed with transport headers added")
	}
}

// TestS3PutHashesTheBodyItSends pins that the payload hash in
// x-amz-content-sha256 is the digest of the bytes actually put on the wire, and
// not the hash of the empty body a signed GET would carry. A client that
// signed one body and sent another would store an object its own key does not
// describe, and the importer's check would refuse it forever.
func TestS3PutHashesTheBodyItSends(t *testing.T) {
	body := strings.Repeat("runner transcript payload ", 500)
	want := sha256.Sum256([]byte(body))

	var (
		gotHash   string
		gotBody   []byte
		gotPath   string
		gotMethod string
		gotAuth   string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHash = r.Header.Get("x-amz-content-sha256")
		gotPath = r.URL.Path
		gotMethod = r.Method
		gotAuth = r.Header.Get("Authorization")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	store := NewS3Store(S3Config{Endpoint: srv.URL, Bucket: "bodies", KeyID: "k", Secret: "s"})
	const key = "laptop/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	if err := store.Put(context.Background(), key, strings.NewReader(body), int64(len(body))); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if gotMethod != http.MethodPut {
		t.Errorf("method = %s, want PUT", gotMethod)
	}
	if gotPath != "/bodies/"+key {
		t.Errorf("path = %s, want the path-style /bodies/%s", gotPath, key)
	}
	if gotHash != hex.EncodeToString(want[:]) {
		t.Errorf("x-amz-content-sha256 = %s, want the body's digest %s", gotHash, hex.EncodeToString(want[:]))
	}
	if string(gotBody) != body {
		t.Errorf("sent %d bytes, want the body's %d", len(gotBody), len(body))
	}
	if !strings.HasPrefix(gotAuth, "AWS4-HMAC-SHA256 ") {
		t.Errorf("Authorization = %q, want a SigV4 header", gotAuth)
	}
}
