package client

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/url"
	"os"
	"time"

	"github.com/fuad-daoud/relevo/internal/remote"
)

// spoolCreateChain writes the create's multipart body once to a temp file so a
// retry re-reads the same chain JSON and bundle bytes; on error the temp file
// is removed.
func spoolCreateChain(req remote.CreateChainRequest, bundle io.Reader) (tmp *os.File, size int64, bodySHA []byte, contentType string, err error) {
	tmp, err = os.CreateTemp("", "relevo-create-chain-*.tmp")
	if err != nil {
		return nil, 0, nil, "", fmt.Errorf("create temp file: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()

	encoded, err := json.Marshal(req)
	if err != nil {
		return nil, 0, nil, "", fmt.Errorf("marshal chain request: %w", err)
	}

	hasher := sha256.New()
	mw := multipart.NewWriter(io.MultiWriter(tmp, hasher))
	if err = mw.WriteField("chain", string(encoded)); err != nil {
		return nil, 0, nil, "", fmt.Errorf("write chain field: %w", err)
	}
	if err = copyBundleField(mw, bundle); err != nil {
		return nil, 0, nil, "", err
	}
	if err = mw.Close(); err != nil {
		return nil, 0, nil, "", fmt.Errorf("close multipart writer: %w", err)
	}
	if size, err = tmp.Seek(0, io.SeekEnd); err != nil {
		return nil, 0, nil, "", fmt.Errorf("seek end: %w", err)
	}
	return tmp, size, hasher.Sum(nil), mw.FormDataContentType(), nil
}

// CreateChain hands a whole chain to a server: a multipart POST /v1/chains
// with the "chain" JSON field and, when bundle is non-nil, one "bundle" file
// part. The body is spooled so a retry re-reads it.
func (c *Client) CreateChain(ctx context.Context, server string, req remote.CreateChainRequest, bundle io.Reader) (remote.ChainView, error) {
	tmp, size, bodySHA, contentType, err := spoolCreateChain(req, bundle)
	if err != nil {
		return remote.ChainView{}, err
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()

	attempt := func(ctx context.Context) (remote.ChainView, error) {
		return c.createChainAttempt(ctx, server, tmp, size, bodySHA, contentType)
	}
	return retry(ctx, retryAttempts, retryBase, attempt)
}

func (c *Client) createChainAttempt(ctx context.Context, server string, tmp *os.File, size int64, bodySHA []byte, contentType string) (remote.ChainView, error) {
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return remote.ChainView{}, fmt.Errorf("seek start: %w", err)
	}
	actx, cancel := context.WithTimeout(ctx, startRoundDeadline(size))
	defer cancel()

	// LimitReader rather than tmp itself: http closes a body that is an
	// io.ReadCloser, and a retry needs the spooled file open.
	resp, err := c.doRequest(actx, server, "POST", "/v1/chains", io.LimitReader(tmp, size), size, bodySHA, contentType)
	if err != nil {
		return remote.ChainView{}, err
	}
	return decodeJSON[remote.ChainView](resp, "decode chain view")
}

// GetChain reads a chain's view.
func (c *Client) GetChain(ctx context.Context, server, name string) (remote.ChainView, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	path := fmt.Sprintf("/v1/chains/%s", url.PathEscape(name))
	return getJSON[remote.ChainView](c, ctx, server, path, "decode chain view")
}

// ChainStop stops a chain; it does not retry, matching the binding Stop.
func (c *Client) ChainStop(ctx context.Context, server, name string) (remote.ChainStopResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	path := fmt.Sprintf("/v1/chains/%s/stop", url.PathEscape(name))
	resp, err := c.do(ctx, server, "POST", path, nil, nil, "")
	if err != nil {
		return remote.ChainStopResponse{}, err
	}
	return decodeJSON[remote.ChainStopResponse](resp, "decode chain stop")
}

// ChainResume applies a resume request to a chain; it does not retry, matching
// the binding Resume.
func (c *Client) ChainResume(ctx context.Context, server, name string, req remote.ChainResumeRequest) (remote.ChainView, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	body, err := json.Marshal(req)
	if err != nil {
		return remote.ChainView{}, fmt.Errorf("marshal chain resume request: %w", err)
	}
	sum := sha256.Sum256(body)
	path := fmt.Sprintf("/v1/chains/%s/resume", url.PathEscape(name))
	resp, err := c.do(ctx, server, "POST", path, body, sum[:], "application/json")
	if err != nil {
		return remote.ChainView{}, err
	}
	return decodeJSON[remote.ChainView](resp, "decode chain view")
}

// ChainDone marks a chain done; it does not retry, matching the binding Done.
func (c *Client) ChainDone(ctx context.Context, server, name string) error {
	return postOnce(ctx, c, server, fmt.Sprintf("/v1/chains/%s/done", url.PathEscape(name)))
}
