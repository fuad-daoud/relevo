// Package client is relevo's remote protocol client: it signs requests to a
// relevo server and decodes its replies.
package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/fuad-daoud/relevo/internal/remote"
)

var (
	ErrUnreachable   = errors.New("remote server unreachable")
	ErrCertChanged   = errors.New("pinned fingerprint mismatch")
	ErrUnknownServer = errors.New("unknown server")
	ErrVersion       = errors.New("server rejected protocol version (426)")
)

// Version is this client's build version, sent as remote.HeaderClientVersion on
// every request. The server logs it and never signs or rejects on it.
var Version = ""

type HTTPError struct {
	Status int
	Body   remote.ErrorBody
}

func (e *HTTPError) Error() string {
	if e.Body.Message != "" {
		return e.Body.Message
	}
	return fmt.Sprintf("http %d: %s", e.Status, e.Body.Code)
}

type Client struct {
	servers remote.Servers
	key     remote.Keypair
	now     func() time.Time

	mu          sync.Mutex
	httpClients map[string]*http.Client
}

func New(servers remote.Servers, key remote.Keypair, now func() time.Time) *Client {
	if now == nil {
		now = time.Now
	}
	return &Client{
		servers:     servers,
		key:         key,
		now:         now,
		httpClients: make(map[string]*http.Client),
	}
}

func fingerprintOf(der []byte) string {
	sum := sha256.Sum256(der)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (c *Client) getHTTPClient(entry remote.ServerEntry) *http.Client {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := fmt.Sprintf("%s|%s|%s|%v", entry.URL, entry.Fingerprint, entry.CA, entry.Insecure)
	if cl, ok := c.httpClients[key]; ok {
		return cl
	}
	cl := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsConfigFor(entry)}}
	c.httpClients[key] = cl
	return cl
}

// tlsConfigFor pins a fingerprint by verifying the presented leaf itself, so
// no CA is needed; the pin replaces chain verification.
func tlsConfigFor(entry remote.ServerEntry) *tls.Config {
	switch {
	case entry.Insecure:
		return &tls.Config{InsecureSkipVerify: true}
	case entry.CA == "system":
		return &tls.Config{}
	default:
		return &tls.Config{
			InsecureSkipVerify: true,
			VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
				if len(rawCerts) == 0 {
					return errors.New("no certificates presented")
				}
				if !strings.EqualFold(fingerprintOf(rawCerts[0]), entry.Fingerprint) {
					return ErrCertChanged
				}
				return nil
			},
		}
	}
}

func (c *Client) do(ctx context.Context, server, method, pathWithQuery string, bodyBytes, bodySHA []byte, contentType string) (*http.Response, error) {
	var reader io.Reader
	var length int64 = -1
	if bodyBytes != nil {
		reader = bytes.NewReader(bodyBytes)
		length = int64(len(bodyBytes))
	}
	if len(bodySHA) == 0 {
		sum := sha256.Sum256(bodyBytes)
		bodySHA = sum[:]
	}
	return c.doRequest(ctx, server, method, pathWithQuery, reader, length, bodySHA, contentType)
}

func (c *Client) doRequest(ctx context.Context, server, method, pathWithQuery string, body io.Reader, contentLength int64, bodySHA []byte, contentType string) (*http.Response, error) {
	entry, ok := c.servers[server]
	if !ok {
		return nil, ErrUnknownServer
	}
	req, err := c.newRequest(ctx, entry, method, pathWithQuery, body, contentLength, bodySHA, contentType)
	if err != nil {
		return nil, err
	}
	resp, err := c.getHTTPClient(entry).Do(req)
	if err != nil {
		if errors.Is(err, ErrCertChanged) || strings.Contains(err.Error(), ErrCertChanged.Error()) {
			return nil, ErrCertChanged
		}
		return nil, fmt.Errorf("%w: %w", ErrUnreachable, err)
	}
	if err := responseError(resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *Client) newRequest(ctx context.Context, entry remote.ServerEntry, method, pathWithQuery string, body io.Reader, contentLength int64, bodySHA []byte, contentType string) (*http.Request, error) {
	fullURL := strings.TrimRight(entry.URL, "/") + pathWithQuery
	u, err := url.Parse(fullURL)
	if err != nil {
		return nil, fmt.Errorf("parse url: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, method, fullURL, body)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	if contentLength >= 0 {
		req.ContentLength = contentLength
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if Version != "" {
		req.Header.Set(remote.HeaderClientVersion, Version)
	}
	nonce, err := remote.NewNonce()
	if err != nil {
		return nil, fmt.Errorf("new nonce: %w", err)
	}
	for k, vv := range remote.Sign(c.key, method, u.RequestURI(), bodySHA, c.now(), nonce) {
		for _, v := range vv {
			req.Header.Add(k, v)
		}
	}
	return req, nil
}

// responseError maps a non-success status to the error doRequest returns. A
// gateway status is a redeploy, not an answer, so its CDN HTML body is dropped.
func responseError(resp *http.Response) error {
	if resp.StatusCode == http.StatusUpgradeRequired {
		_ = resp.Body.Close()
		return ErrVersion
	}
	if isGatewayStatus(resp.StatusCode) {
		_ = resp.Body.Close()
		text := http.StatusText(resp.StatusCode)
		if text == "" {
			text = "gateway error"
		}
		return fmt.Errorf("%w: server returned %d %s", ErrUnreachable, resp.StatusCode, text)
	}
	if resp.StatusCode < 300 || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	defer func() { _ = resp.Body.Close() }()
	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	var errBody remote.ErrorBody
	if jsonErr := json.Unmarshal(bodyBytes, &errBody); jsonErr == nil && (errBody.Code != "" || errBody.Message != "") {
		return &HTTPError{Status: resp.StatusCode, Body: errBody}
	}
	return &HTTPError{
		Status: resp.StatusCode,
		Body: remote.ErrorBody{
			Code:    remote.Code(http.StatusText(resp.StatusCode)),
			Message: strings.TrimSpace(string(bodyBytes)),
		},
	}
}

// isGatewayStatus covers Cloudflare's 520-527 and 530 beside 502, 503 and 504.
func isGatewayStatus(status int) bool {
	switch status {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	case 530:
		return true
	}
	return status >= 520 && status <= 527
}

func decodeJSON[T any](resp *http.Response, what string) (T, error) {
	defer func() { _ = resp.Body.Close() }()
	var v T
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return v, fmt.Errorf("decode %s: %w", what, err)
	}
	return v, nil
}

const (
	retryAttempts = 4
	retryBase     = time.Second
)

// sleep waits for d or until ctx is done; it is a variable so tests can replace it.
var sleep = func(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// retry calls f until it succeeds or attempts run out, retrying only an error
// that wraps ErrUnreachable; the last error is what the caller sees.
func retry[T any](ctx context.Context, attempts int, base time.Duration, f func(context.Context) (T, error)) (T, error) {
	var err error
	for i := 0; i < attempts; i++ {
		var out T
		out, err = f(ctx)
		if err == nil {
			return out, nil
		}
		if !errors.Is(err, ErrUnreachable) || i == attempts-1 {
			break
		}
		if serr := sleep(ctx, base<<uint(i)); serr != nil {
			break
		}
	}
	var zero T
	return zero, err
}
