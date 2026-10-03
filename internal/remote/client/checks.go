package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"github.com/fuad-daoud/relevo/internal/remote"
)

// CreateCheck posts a check run request for a served binding. It does not
// retry, ensuring repeated calls do not spawn multiple runs.
func (c *Client) CreateCheck(ctx context.Context, server, name string, req remote.CreateCheckRequest) (remote.CheckView, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	body, err := json.Marshal(req)
	if err != nil {
		return remote.CheckView{}, fmt.Errorf("marshal create check request: %w", err)
	}
	path := fmt.Sprintf("/v1/bindings/%s/checks", url.PathEscape(name))
	resp, err := c.do(ctx, server, "POST", path, body, nil, "application/json")
	if err != nil {
		return remote.CheckView{}, err
	}
	return decodeJSON[remote.CheckView](resp, "decode check view")
}

// GetCheck reads the current view of one check run on a served binding.
func (c *Client) GetCheck(ctx context.Context, server, name, id string) (remote.CheckView, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	path := fmt.Sprintf("/v1/bindings/%s/checks/%s", url.PathEscape(name), url.PathEscape(id))
	resp, err := c.do(ctx, server, "GET", path, nil, nil, "")
	if err != nil {
		return remote.CheckView{}, err
	}
	return decodeJSON[remote.CheckView](resp, "decode check view")
}

// SetGate updates the acceptance check gate and regate budget for a served
// binding.
func (c *Client) SetGate(ctx context.Context, server, name string, req remote.SetGateRequest) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal set gate request: %w", err)
	}
	path := fmt.Sprintf("/v1/bindings/%s/gate", url.PathEscape(name))
	resp, err := c.do(ctx, server, "POST", path, body, nil, "application/json")
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}
