package client

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/remote"
)

// roundFileDeadline is a variable so a test can shorten the download deadline.
var roundFileDeadline = 2 * time.Minute

// deadlineBody ties a download's cancel function to its response body, so the
// per-attempt deadline stops its timer when the caller closes the body.
type deadlineBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (d *deadlineBody) Close() error {
	err := d.ReadCloser.Close()
	d.cancel()
	return err
}

func startRoundDeadline(size int64) time.Duration {
	d := 30*time.Second + time.Duration(size/(256*1024))*time.Second
	if d > 10*time.Minute {
		d = 10 * time.Minute
	}
	return d
}

// spoolStartRound writes the multipart body once to a temp file so every retry
// re-reads the same plan and bundle bytes; on error the temp file is removed.
func spoolStartRound(round int, plan []byte, bundle io.Reader, tier, candidate string, force bool, tags []remote.TagRef, verify *bool) (tmp *os.File, size int64, bodySHA []byte, contentType string, err error) {
	tmp, err = os.CreateTemp("", "relevo-start-round-*.tmp")
	if err != nil {
		return nil, 0, nil, "", fmt.Errorf("create temp file: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()

	hasher := sha256.New()
	mw := multipart.NewWriter(io.MultiWriter(tmp, hasher))
	if err = writeStartRoundFields(mw, round, plan, tier, candidate, force, tags, verify); err != nil {
		return nil, 0, nil, "", err
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

func writeStartRoundFields(mw *multipart.Writer, round int, plan []byte, tier, candidate string, force bool, tags []remote.TagRef, verify *bool) error {
	if err := mw.WriteField("round", strconv.Itoa(round)); err != nil {
		return fmt.Errorf("write round field: %w", err)
	}
	if err := mw.WriteField("plan", string(plan)); err != nil {
		return fmt.Errorf("write plan field: %w", err)
	}
	if tier != "" {
		if err := mw.WriteField("tier", tier); err != nil {
			return fmt.Errorf("write tier field: %w", err)
		}
	}
	if candidate != "" {
		if err := mw.WriteField("candidate", candidate); err != nil {
			return fmt.Errorf("write candidate field: %w", err)
		}
	}
	if force {
		if err := mw.WriteField("force", "1"); err != nil {
			return fmt.Errorf("write force field: %w", err)
		}
	}
	if verify != nil {
		v := "0"
		if *verify {
			v = "1"
		}
		if err := mw.WriteField("verify", v); err != nil {
			return fmt.Errorf("write verify field: %w", err)
		}
	}
	if len(tags) == 0 {
		return nil
	}
	encoded, err := json.Marshal(tags)
	if err != nil {
		return fmt.Errorf("marshal tags: %w", err)
	}
	if err := mw.WriteField("tags", string(encoded)); err != nil {
		return fmt.Errorf("write tags field: %w", err)
	}
	return nil
}

func copyBundleField(mw *multipart.Writer, bundle io.Reader) error {
	if bundle == nil {
		return nil
	}
	part, err := mw.CreateFormFile("bundle", "bundle.bundle")
	if err != nil {
		return fmt.Errorf("create bundle form file: %w", err)
	}
	if _, err := io.Copy(part, bundle); err != nil {
		return fmt.Errorf("copy bundle: %w", err)
	}
	return nil
}

// StartRound begins a round on the server. tags nil or empty omits the field;
// candidate "" leaves the binding's builder unchanged; force true carries the
// --force opt-out of the seed cap; verify nil takes the server's own policy.
// retryOnUnreachable is safe only when the server advertised
// remote.FeatureIdempotentSend.
func (c *Client) StartRound(ctx context.Context, server, name string, round int, plan []byte, bundle io.Reader, tier, candidate string, force bool, tags []remote.TagRef, retryOnUnreachable bool, verify *bool) (remote.BindingView, error) {
	tmp, size, bodySHA, contentType, err := spoolStartRound(round, plan, bundle, tier, candidate, force, tags, verify)
	if err != nil {
		return remote.BindingView{}, err
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()

	path := fmt.Sprintf("/v1/bindings/%s/rounds", url.PathEscape(name))
	attempt := func(ctx context.Context) (remote.BindingView, error) {
		return c.startRoundAttempt(ctx, server, path, tmp, size, bodySHA, contentType)
	}
	if retryOnUnreachable {
		return retry(ctx, retryAttempts, retryBase, attempt)
	}
	return attempt(ctx)
}

func (c *Client) startRoundAttempt(ctx context.Context, server, path string, tmp *os.File, size int64, bodySHA []byte, contentType string) (remote.BindingView, error) {
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return remote.BindingView{}, fmt.Errorf("seek start: %w", err)
	}
	actx, cancel := context.WithTimeout(ctx, startRoundDeadline(size))
	defer cancel()

	// LimitReader rather than tmp itself: http closes a body that is an
	// io.ReadCloser, and a retry needs the spooled file open.
	resp, err := c.doRequest(actx, server, "POST", path, io.LimitReader(tmp, size), size, bodySHA, contentType)
	if err != nil {
		return remote.BindingView{}, err
	}
	return decodeJSON[remote.BindingView](resp, "decode binding view")
}

func (c *Client) RoundFile(ctx context.Context, server, name string, round int, kind string) (io.ReadCloser, error) {
	path := fmt.Sprintf("/v1/bindings/%s/rounds/%d/files/%s", url.PathEscape(name), round, url.PathEscape(kind))
	return retry(ctx, retryAttempts, retryBase, func(ctx context.Context) (io.ReadCloser, error) {
		actx, cancel := context.WithTimeout(ctx, roundFileDeadline)
		resp, err := c.do(actx, server, "GET", path, nil, nil, "")
		if err != nil {
			cancel()
			return nil, err
		}
		return &deadlineBody{ReadCloser: resp.Body, cancel: cancel}, nil
	})
}

type roundFileResult struct {
	body      io.ReadCloser
	fileRange remote.FileRange
}

func (c *Client) RoundFileFrom(ctx context.Context, server, name string, round int, kind string, from int64) (io.ReadCloser, remote.FileRange, error) {
	path := fmt.Sprintf("/v1/bindings/%s/rounds/%d/files/%s?from=%d", url.PathEscape(name), round, url.PathEscape(kind), from)
	res, err := retry(ctx, retryAttempts, retryBase, func(ctx context.Context) (roundFileResult, error) {
		actx, cancel := context.WithTimeout(ctx, roundFileDeadline)
		resp, err := c.do(actx, server, "GET", path, nil, nil, "")
		if err != nil {
			cancel()
			return roundFileResult{}, err
		}
		return roundFileResult{
			body:      &deadlineBody{ReadCloser: resp.Body, cancel: cancel},
			fileRange: remote.ParseFileRange(resp.Header),
		}, nil
	})
	if err != nil {
		return nil, remote.FileRange{}, err
	}
	return res.body, res.fileRange, nil
}

// escapeRel path-escapes each segment of a slash-separated artifact rel on its
// own, so the "/" separators survive the round trip and a rel that is one
// segment stays one segment.
func escapeRel(rel string) string {
	parts := strings.Split(rel, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// RoundArtifacts lists a closed reader round's artifact files.
func (c *Client) RoundArtifacts(ctx context.Context, server, name string, round int) (remote.ArtifactList, error) {
	path := fmt.Sprintf("/v1/bindings/%s/rounds/%d/artifacts", url.PathEscape(name), round)
	return retry(ctx, retryAttempts, retryBase, func(ctx context.Context) (remote.ArtifactList, error) {
		actx, cancel := context.WithTimeout(ctx, roundFileDeadline)
		defer cancel()
		resp, err := c.do(actx, server, "GET", path, nil, nil, "")
		if err != nil {
			return remote.ArtifactList{}, err
		}
		return decodeJSON[remote.ArtifactList](resp, "decode artifact list")
	})
}

// RoundArtifact downloads one artifact, rel exactly as RoundArtifacts listed
// it. The rel is escaped one segment at a time, so a nested rel keeps its
// slashes.
func (c *Client) RoundArtifact(ctx context.Context, server, name string, round int, rel string) (io.ReadCloser, error) {
	path := fmt.Sprintf("/v1/bindings/%s/rounds/%d/artifacts/%s", url.PathEscape(name), round, escapeRel(rel))
	return retry(ctx, retryAttempts, retryBase, func(ctx context.Context) (io.ReadCloser, error) {
		actx, cancel := context.WithTimeout(ctx, roundFileDeadline)
		resp, err := c.do(actx, server, "GET", path, nil, nil, "")
		if err != nil {
			cancel()
			return nil, err
		}
		return &deadlineBody{ReadCloser: resp.Body, cancel: cancel}, nil
	})
}

// RoundBundle streams a round's git bundle, with (nil, nil) on 204 No Content.
func (c *Client) RoundBundle(ctx context.Context, server, name string, round int, since string) (io.ReadCloser, error) {
	path := fmt.Sprintf("/v1/bindings/%s/rounds/%d/bundle", url.PathEscape(name), round)
	if since != "" {
		path += "?since=" + url.QueryEscape(since)
	}
	return retry(ctx, retryAttempts, retryBase, func(ctx context.Context) (io.ReadCloser, error) {
		actx, cancel := context.WithTimeout(ctx, roundFileDeadline)
		resp, err := c.do(actx, server, "GET", path, nil, nil, "")
		if err != nil {
			cancel()
			return nil, err
		}
		if resp.StatusCode == http.StatusNoContent {
			_ = resp.Body.Close()
			cancel()
			return nil, nil
		}
		return &deadlineBody{ReadCloser: resp.Body, cancel: cancel}, nil
	})
}
