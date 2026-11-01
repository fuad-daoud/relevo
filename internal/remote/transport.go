package remote

import (
	"context"
	"errors"
	"io"
)

type Snapshot struct {
	ContentType string
	Body        io.ReadCloser     // nil when Empty; caller closes otherwise
	Heads       map[string]string // ref -> sha the snapshot carries, always filled
	Empty       bool              // nothing newer than `since`; Body is nil
}

// TreeTransport defines the interface for moving git ref snapshots between repositories.
type TreeTransport interface {
	// Since "" means "everything reachable". Since must be a commit that is
	// an ancestor of every ref; otherwise ErrSinceUnknown.
	Snapshot(ctx context.Context, repo string, refs []string, since string) (Snapshot, error)

	// Every ref the body carries must be in refs (else ErrUnexpectedRef);
	// every ref in refs the body carries is fast-forwarded (else
	// git.ErrNotFastForward); a ref in refs the body does not carry is left
	// as it is. Returns ref -> new sha for the refs it moved. A zero-length
	// body is a no-op returning an empty map.
	Absorb(ctx context.Context, repo, contentType string, body io.Reader, refs []string) (map[string]string, error)
}

// Transport sentinels wrapped with context (%w). Callers inspect them with errors.Is.
var (
	ErrUnsupportedType = errors.New("unsupported content type")

	ErrUnexpectedRef = errors.New("unexpected ref in transport body")

	ErrSinceUnknown = errors.New("since commit is unknown or not an ancestor")
)
