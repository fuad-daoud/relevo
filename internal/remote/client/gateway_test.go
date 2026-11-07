package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/remote"
)

// TestGotResponsePinsWhichFailuresReachedHTTP pins the classifier the tick's
// prefetch logs with a first failure: a gateway status and an HTTP error of
// any status count as an answer, while a refused dial, a deadline, a
// certificate refusal and an unknown server count as no answer.
func TestGotResponsePinsWhichFailuresReachedHTTP(t *testing.T) {
	gateway := func() error {
		resp := &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Body:       io.NopCloser(strings.NewReader("<html>unavailable</html>")),
		}
		return responseError(resp)
	}()
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"refused dial", fmt.Errorf("%w: connection refused", ErrUnreachable), false},
		{"bare deadline", context.DeadlineExceeded, false},
		{"wrapped deadline", fmt.Errorf("%w: %w", ErrUnreachable, context.DeadlineExceeded), false},
		{"certificate refusal", ErrCertChanged, false},
		{"unknown server", ErrUnknownServer, false},
		{"gateway status", gateway, true},
		{"401", &HTTPError{Status: 401, Body: remote.ErrorBody{Code: "stale", Message: "stale"}}, true},
		{"404", &HTTPError{Status: 404, Body: remote.ErrorBody{Code: "not_found", Message: "no binding"}}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := GotResponse(c.err); got != c.want {
				t.Errorf("GotResponse(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

// TestGatewayErrorKeepsTheUnreachableContract pins the carrier the gateway
// arm wraps in: the exact message text, the Unreachable identity the retry
// keys on, and the status the classifier reads without text-matching.
func TestGatewayErrorKeepsTheUnreachableContract(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusServiceUnavailable,
		Body:       io.NopCloser(strings.NewReader("<html>unavailable</html>")),
	}
	err := responseError(resp)
	if got, want := err.Error(), "remote server unreachable: server returned 503 Service Unavailable"; got != want {
		t.Errorf("err = %q, want %q", got, want)
	}
	if !errors.Is(err, ErrUnreachable) {
		t.Errorf("err = %v, want it to wrap ErrUnreachable", err)
	}
	var gatewayErr *GatewayError
	if !errors.As(err, &gatewayErr) {
		t.Fatalf("err = %v (%T), want it to carry a *GatewayError", err, err)
	}
	if gatewayErr.Status != http.StatusServiceUnavailable {
		t.Errorf("gateway status = %d, want 503", gatewayErr.Status)
	}
	if !GotResponse(err) {
		t.Errorf("GotResponse(%v) = false, want true for a gateway carrier", err)
	}
}
