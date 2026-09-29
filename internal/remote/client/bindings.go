package client

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"github.com/fuad-daoud/relevo/internal/remote"
)

func getJSON[T any](c *Client, ctx context.Context, server, path, decodeMsg string) (T, error) {
	return retry(ctx, retryAttempts, retryBase, func(ctx context.Context) (T, error) {
		resp, err := c.do(ctx, server, "GET", path, nil, nil, "")
		if err != nil {
			var zero T
			return zero, err
		}
		return decodeJSON[T](resp, decodeMsg)
	})
}

func postJSON[Req, Resp any](c *Client, ctx context.Context, server, path string, req Req, marshalMsg, decodeMsg string) (Resp, error) {
	return retry(ctx, retryAttempts, retryBase, func(ctx context.Context) (Resp, error) {
		var zero Resp
		body, err := json.Marshal(req)
		if err != nil {
			return zero, fmt.Errorf("%s: %w", marshalMsg, err)
		}
		sum := sha256.Sum256(body)
		resp, err := c.do(ctx, server, "POST", path, body, sum[:], "application/json")
		if err != nil {
			return zero, err
		}
		return decodeJSON[Resp](resp, decodeMsg)
	})
}

func post[Req any](c *Client, ctx context.Context, server, path string, req Req, marshalMsg string) error {
	_, err := retry(ctx, retryAttempts, retryBase, func(ctx context.Context) (struct{}, error) {
		body, err := json.Marshal(req)
		if err != nil {
			return struct{}{}, fmt.Errorf("%s: %w", marshalMsg, err)
		}
		sum := sha256.Sum256(body)
		resp, err := c.do(ctx, server, "POST", path, body, sum[:], "application/json")
		if err != nil {
			return struct{}{}, err
		}
		_ = resp.Body.Close()
		return struct{}{}, nil
	})
	return err
}

func postNoBody[T any](c *Client, ctx context.Context, server, path, decodeMsg string) (T, error) {
	return retry(ctx, retryAttempts, retryBase, func(ctx context.Context) (T, error) {
		resp, err := c.do(ctx, server, "POST", path, nil, nil, "")
		if err != nil {
			var zero T
			return zero, err
		}
		return decodeJSON[T](resp, decodeMsg)
	})
}

func (c *Client) WhoAmI(ctx context.Context, server string) (remote.WhoAmI, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return getJSON[remote.WhoAmI](c, ctx, server, "/v1/whoami", "decode whoami")
}

func (c *Client) Candidates(ctx context.Context, server string) (remote.CandidatesResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return getJSON[remote.CandidatesResponse](c, ctx, server, "/v1/candidates", "decode candidates")
}

func (c *Client) CreateBinding(ctx context.Context, server string, req remote.CreateBindingRequest) (remote.BindingView, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return postJSON[remote.CreateBindingRequest, remote.BindingView](c, ctx, server, "/v1/bindings", req, "marshal request", "decode binding view")
}

func (c *Client) GetBinding(ctx context.Context, server, name string) (remote.BindingView, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	path := fmt.Sprintf("/v1/bindings/%s", url.PathEscape(name))
	return getJSON[remote.BindingView](c, ctx, server, path, "decode binding view")
}

func (c *Client) Ack(ctx context.Context, server, name string, round int) (remote.BindingView, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	path := fmt.Sprintf("/v1/bindings/%s/rounds/%d/ack", url.PathEscape(name), round)
	return postNoBody[remote.BindingView](c, ctx, server, path, "decode binding view")
}

func (c *Client) Unavailable(ctx context.Context, server, name, token, reason string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	path := "/v1/unavailable"
	if name != "" {
		path = fmt.Sprintf("/v1/bindings/%s/unavailable", url.PathEscape(name))
	}
	return post(c, ctx, server, path, remote.UnavailableRequest{Token: token, Reason: reason}, "marshal unavailable request")
}

// Available clears the server-wide ledger's gate on subject's provider; there
// is no binding-scoped counterpart.
func (c *Client) Available(ctx context.Context, server, subject string) (remote.AvailableResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return postJSON[remote.AvailableRequest, remote.AvailableResponse](c, ctx, server, "/v1/available", remote.AvailableRequest{Subject: subject}, "marshal available request", "decode available")
}

func (c *Client) Done(ctx context.Context, server, name string) error {
	return postOnce(ctx, c, server, fmt.Sprintf("/v1/bindings/%s/done", url.PathEscape(name)))
}

func (c *Client) Unbind(ctx context.Context, server, name string) error {
	return postOnce(ctx, c, server, fmt.Sprintf("/v1/bindings/%s/unbind", url.PathEscape(name)))
}

// postOnce does not retry: the caller's action is not safe to repeat blindly.
func postOnce(ctx context.Context, c *Client, server, path string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := c.do(ctx, server, "POST", path, nil, nil, "")
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

func (c *Client) Resume(ctx context.Context, server, name string) (remote.BindingView, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	path := fmt.Sprintf("/v1/bindings/%s/resume", url.PathEscape(name))
	resp, err := c.do(ctx, server, "POST", path, nil, nil, "")
	if err != nil {
		return remote.BindingView{}, err
	}
	return decodeJSON[remote.BindingView](resp, "decode binding view")
}

func (c *Client) Stop(ctx context.Context, server, name string) (remote.BindingView, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	path := fmt.Sprintf("/v1/bindings/%s/stop", url.PathEscape(name))
	resp, err := c.do(ctx, server, "POST", path, nil, nil, "")
	if err != nil {
		return remote.BindingView{}, err
	}
	return decodeJSON[remote.BindingView](resp, "decode binding view")
}
