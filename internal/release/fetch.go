package release

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// ErrOffline is what Latest returns when the request never reached the
// endpoint: no answer, as opposed to a malformed one.
var ErrOffline = errors.New("release check: offline")

// DefaultEndpoint is the GitHub releases API for relevo's newest release.
const DefaultEndpoint = "https://api.github.com/repos/fuad-daoud/relevo/releases/latest"

// defaultTimeout is the whole budget for one check: the daemon ticks every
// two seconds and must never be held up by a slow endpoint.
const defaultTimeout = 5 * time.Second

const maxBody = 1 << 20

// Fetcher returns the latest published release tag.
type Fetcher interface {
	Latest(ctx context.Context) (string, error)
}

type httpFetcher struct {
	endpoint string
	client   *http.Client
}

// Source is the endpoint a fetch reads: RELEVO_RELEASE_API when set, else DefaultEndpoint.
func Source() string {
	if endpoint := os.Getenv("RELEVO_RELEASE_API"); endpoint != "" {
		return endpoint
	}
	return DefaultEndpoint
}

// NewHTTPFetcher reads the GitHub releases API. The endpoint is overridable
// with RELEVO_RELEASE_API, so tests and air-gapped installs can point it elsewhere.
func NewHTTPFetcher(endpoint string, timeout time.Duration) Fetcher {
	if endpoint == "" {
		endpoint = Source()
	}
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &httpFetcher{endpoint: endpoint, client: &http.Client{Timeout: timeout}}
}

// Latest GETs the endpoint and returns its tag_name. No auth, no retry: a
// failure is the caller's to swallow and try again later.
func (f *httpFetcher) Latest(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("release check: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := f.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrOffline, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("release check: %s: %s", f.endpoint, resp.Status)
	}

	var payload struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&payload); err != nil {
		return "", fmt.Errorf("release check: decode %s: %w", f.endpoint, err)
	}
	if payload.TagName == "" {
		return "", fmt.Errorf("release check: %s: no tag_name", f.endpoint)
	}
	return payload.TagName, nil
}
