package blobstore

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// R2 and every S3-compatible endpoint read region "auto"; it is part of the
// signature's credential scope but never sent as a header.
const (
	s3Region  = "auto"
	s3Service = "s3"
)

// emptyPayloadHash is the hex sha256 of no bytes. A request with no body still
// carries it, because the value is what the signature covers and a header left
// out would change the signature rather than the payload.
const emptyPayloadHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// s3TimeFormat is the ISO8601 basic format SigV4 puts in x-amz-date, and the
// date part of the credential scope is its first eight characters.
const s3TimeFormat = "20060102T150405Z"

// S3Config is what NewS3Store needs to reach a bucket. The endpoint carries no
// bucket of its own: R2 hands out one account-scoped URL and names the bucket
// separately.
type S3Config struct {
	Endpoint string
	Bucket   string
	KeyID    string
	Secret   string
	Client   *http.Client
}

// S3Store is a BlobStore over an S3-compatible endpoint, signing every request
// with SigV4 by hand: the five operations here are few enough that a signing
// library and its dependency tree would cost more than the code it saved.
type S3Store struct {
	Endpoint string
	Bucket   string
	KeyID    string
	Secret   string
	Client   *http.Client
	now      func() time.Time
}

// NewS3Store returns a store for cfg, clocked by the wall clock. A nil Client
// takes http.DefaultClient, so the caller supplies a transport only when it
// needs one.
func NewS3Store(cfg S3Config) *S3Store {
	client := cfg.Client
	if client == nil {
		client = http.DefaultClient
	}
	return &S3Store{
		Endpoint: strings.TrimRight(cfg.Endpoint, "/"),
		Bucket:   cfg.Bucket,
		KeyID:    cfg.KeyID,
		Secret:   cfg.Secret,
		Client:   client,
		now:      time.Now,
	}
}

// Put stores body under key. The body is buffered whole before it is sent,
// because SigV4 covers a hash of the exact bytes on the wire and a streamed
// body would have to be hashed twice; a body is a few megabytes at most.
func (s *S3Store) Put(ctx context.Context, key string, body io.Reader, size int64) error {
	buf, err := readSized(body, size)
	if err != nil {
		return fmt.Errorf("blobstore: s3 put %s: %w", key, err)
	}
	sum := sha256Hex(buf)
	req, err := s.newRequest(ctx, http.MethodPut, key, nil)
	if err != nil {
		return err
	}
	req.ContentLength = int64(len(buf))
	req.Body = io.NopCloser(bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/octet-stream")
	_, err = s.do(req, sum)
	return err
}

// Get writes the body under key to w and reports how many bytes it wrote, or
// ErrNotFound when the endpoint says the object is not there.
func (s *S3Store) Get(ctx context.Context, key string, w io.Writer) (int64, error) {
	req, err := s.newRequest(ctx, http.MethodGet, key, nil)
	if err != nil {
		return 0, err
	}
	resp, err := s.do(req, emptyPayloadHash)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	n, err := io.Copy(w, resp.Body)
	if err != nil {
		return n, fmt.Errorf("blobstore: s3 get %s: %w", key, err)
	}
	return n, nil
}

// Has reports whether key is in the bucket. It is a HEAD rather than a ranged
// GET because the answer is all that is wanted: a body would cross the wire to
// be discarded, and bodies are the largest thing this store moves.
func (s *S3Store) Has(ctx context.Context, key string) (bool, error) {
	req, err := s.newRequest(ctx, http.MethodHead, key, nil)
	if err != nil {
		return false, err
	}
	resp, err := s.do(req, emptyPayloadHash)
	if err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, err
	}
	_ = resp.Body.Close()
	return true, nil
}

// Delete removes key. An object already gone is not an error: the cleanup asks
// by deleting, and a body a concurrent run removed is the answer it wanted.
func (s *S3Store) Delete(ctx context.Context, key string) error {
	req, err := s.newRequest(ctx, http.MethodDelete, key, nil)
	if err != nil {
		return err
	}
	_, err = s.do(req, emptyPayloadHash)
	if err != nil && isNotFound(err) {
		return nil
	}
	return err
}

// listResult is the part of a ListObjectsV2 answer this store reads. A bucket
// holding a million objects under one prefix answers in pages, so the token and
// the truncation flag are what make the listing complete.
type listResult struct {
	IsTruncated           bool     `xml:"IsTruncated"`
	NextContinuationToken string   `xml:"NextContinuationToken"`
	Contents              []object `xml:"Contents"`
}

type object struct {
	Key          string    `xml:"Key"`
	Size         int64     `xml:"Size"`
	LastModified time.Time `xml:"LastModified"`
}

// listPageCap bounds how many pages one List walks. A server that keeps
// reporting truncation would otherwise loop forever holding the caller; the
// listing it has by then is still the whole of what the bucket answered so far,
// and the next call starts over rather than hanging here.
const listPageCap = 1000

// List returns every object under prefix, in key order, following the
// continuation tokens until the endpoint reports the listing complete.
func (s *S3Store) List(ctx context.Context, prefix string) ([]Object, error) {
	var out []Object
	token := ""
	for page := 0; page < listPageCap; page++ {
		q := url.Values{"list-type": {"2"}, "prefix": {prefix}}
		if token != "" {
			q.Set("continuation-token", token)
		}
		req, err := s.newRequest(ctx, http.MethodGet, "", q)
		if err != nil {
			return nil, err
		}
		resp, err := s.do(req, emptyPayloadHash)
		if err != nil {
			return nil, err
		}
		var page1 listResult
		derr := xml.NewDecoder(resp.Body).Decode(&page1)
		_ = resp.Body.Close()
		if derr != nil {
			return nil, fmt.Errorf("blobstore: s3 list %s: %w", prefix, derr)
		}
		for _, c := range page1.Contents {
			out = append(out, Object{Key: c.Key, Size: c.Size, Modified: c.LastModified.UTC()})
		}
		if !page1.IsTruncated || page1.NextContinuationToken == "" {
			break
		}
		token = page1.NextContinuationToken
	}
	// S3 answers a listing in key order already, but a listing assembled over
	// several pages carries no such guarantee and the in-memory fake has none,
	// and the weekly cleanup walks a prefix expecting the same order from either
	// implementation.
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// newRequest builds a signed-ready request for one object key, or for the
// bucket itself when key is empty (which is how a listing is addressed).
func (s *S3Store) newRequest(ctx context.Context, method, key string, query url.Values) (*http.Request, error) {
	raw := s.Endpoint + "/" + s.Bucket
	if key != "" {
		raw += "/" + key
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("blobstore: s3 %s %s: bad url: %w", method, key, err)
	}
	if query != nil {
		u.RawQuery = query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("blobstore: s3 %s %s: %w", method, key, err)
	}
	return req, nil
}

// s3Error is a failed S3 answer. The endpoint's own <Code> is kept because it
// says which failure it was -- NoSuchKey, AccessDenied, SignatureDoesNotMatch --
// and the status alone does not. The secret is never among the fields, so it
// cannot reach a log through this error.
type s3Error struct {
	Status int
	Code   string
	Msg    string
}

func (e *s3Error) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("blobstore: s3: %d: %s", e.Status, e.Msg)
	}
	return fmt.Sprintf("blobstore: s3: %d %s: %s", e.Status, e.Code, e.Msg)
}

// Is makes a 404 report as ErrNotFound, the one condition the caller handles by
// latching rather than retrying.
func (e *s3Error) Is(target error) bool {
	return target == ErrNotFound && e.Status == http.StatusNotFound
}

// isNotFound reports whether err is the missing-object refusal, whether it came
// from an endpoint's status or from the in-memory fake.
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrNotFound) {
		return true
	}
	var se *s3Error
	return errors.As(err, &se) && se.Status == http.StatusNotFound
}

// errorBody is the <Error> document every S3-compatible endpoint answers a
// failure with.
type errorBody struct {
	Code    string `xml:"Code"`
	Message string `xml:"Message"`
}

// An error from this client is written to the daemon's log and shown by
// `db sync status`, so it must not carry the bucket's secret. The endpoint's own
// message is echoed because it says which failure it was, and a message that
// quotes the secret -- a misconfigured endpoint echoing the request back, a
// proxy printing its headers -- is redacted rather than passed through.

// do signs and sends one request, returning a 2xx response for the caller to
// close. Every non-2xx becomes an *s3Error carrying the status and the body's
// own code; a 404 also matches ErrNotFound.
func (s *S3Store) do(req *http.Request, payloadHash string) (*http.Response, error) {
	req.Header.Set("x-amz-content-sha256", payloadHash)
	signRequest(req, s.KeyID, s.Secret, s3Region, s3Service, payloadHash, s.now().UTC())
	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("blobstore: s3 %s %s: %w", req.Method, req.URL.Path, err)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	var parsed errorBody
	_ = xml.Unmarshal(body, &parsed)
	msg := parsed.Message
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	// An empty secret would make ReplaceAll match at every position and shred
	// the message, so a store configured without one redacts nothing.
	if s.Secret != "" {
		msg = strings.ReplaceAll(msg, s.Secret, "[redacted]")
	}
	return nil, &s3Error{Status: resp.StatusCode, Code: parsed.Code, Msg: msg}
}
