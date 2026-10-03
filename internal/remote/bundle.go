package remote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/store"
)

var _ TreeTransport = (*BundleTransport)(nil)

// BundleTransport implements TreeTransport using git bundles.
type BundleTransport struct {
	git *git.Client
	tmp string
	// ownerUID/ownerGID, when hasOwner is set, are the tenant identity every
	// temp bundle file is Lchown'ed to before git touches it: a git process
	// running as the tenant must be able to read the bundle it is handed.
	ownerUID uint32
	ownerGID uint32
	hasOwner bool
}

// BundleOption configures a BundleTransport.
type BundleOption func(*BundleTransport)

// WithOwnerTmp marks the transport user-mode: its temp bundle files are created
// in dir and Lchown'ed to uid/gid before git touches them. A none-mode
// transport takes no option and behaves exactly as before.
func WithOwnerTmp(dir string, uid, gid uint32) BundleOption {
	return func(t *BundleTransport) {
		if dir != "" {
			t.tmp = dir
		}
		t.ownerUID, t.ownerGID, t.hasOwner = uid, gid, true
	}
}

// NewBundleTransport creates a new BundleTransport using g and tmpDir for temporary bundle files.
// If tmpDir is "", the state root's tmp directory (<root>/tmp) is used, falling back to os.TempDir().
func NewBundleTransport(g *git.Client, tmpDir string, opts ...BundleOption) *BundleTransport {
	if tmpDir == "" {
		if stTmp, err := store.TempDir(""); err == nil {
			tmpDir = stTmp
		} else {
			tmpDir = os.TempDir()
		}
	}
	t := &BundleTransport{
		git: g,
		tmp: tmpDir,
	}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

// chownTemp hands a freshly created temp bundle to the tenant. A none-mode
// transport leaves it as the serve uid.
func (t *BundleTransport) chownTemp(path string) error {
	if !t.hasOwner {
		return nil
	}
	return os.Lchown(path, int(t.ownerUID), int(t.ownerGID))
}

type fileRemover struct {
	*os.File
	path string
	once sync.Once
}

func (f *fileRemover) Close() error {
	var err error
	f.once.Do(func() {
		err = f.File.Close()
		_ = os.Remove(f.path)
	})
	return err
}

// Snapshot packages every ref in refs relative to since into a git bundle.
func (t *BundleTransport) Snapshot(ctx context.Context, repo string, refs []string, since string) (Snapshot, error) {
	f, err := os.CreateTemp(t.tmp, "relevo-bundle-*.bundle")
	if err != nil {
		return Snapshot{}, err
	}
	tmpPath := f.Name()
	_ = f.Close()
	if err := t.chownTemp(tmpPath); err != nil {
		_ = os.Remove(tmpPath)
		return Snapshot{}, err
	}

	heads, empty, err := t.git.BundleCreate(ctx, repo, tmpPath, refs, since)
	if err != nil {
		_ = os.Remove(tmpPath)
		if errors.Is(err, git.ErrRefMissing) && strings.Contains(err.Error(), "since is not an ancestor") {
			return Snapshot{}, fmt.Errorf("%w: %v", ErrSinceUnknown, err)
		}
		return Snapshot{}, err
	}

	if empty {
		_ = os.Remove(tmpPath)
		return Snapshot{
			ContentType: ContentTypeGitBundle,
			Heads:       heads,
			Empty:       true,
		}, nil
	}

	opened, err := openBundleRegular(tmpPath)
	if err != nil {
		_ = os.Remove(tmpPath)
		return Snapshot{}, err
	}

	return Snapshot{
		ContentType: ContentTypeGitBundle,
		Body:        &fileRemover{File: opened, path: tmpPath},
		Heads:       heads,
		Empty:       false,
	}, nil
}

// Absorb applies a bundle body to repo, updating the specified refs.
func (t *BundleTransport) Absorb(ctx context.Context, repo, contentType string, body io.Reader, refs []string) (map[string]string, error) {
	if contentType != ContentTypeGitBundle {
		return nil, ErrUnsupportedType
	}

	tmpFile, err := os.CreateTemp(t.tmp, "relevo-bundle-*.bundle")
	if err != nil {
		return nil, err
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)
	if err := t.chownTemp(tmpPath); err != nil {
		_ = tmpFile.Close()
		return nil, err
	}

	n, err := io.Copy(tmpFile, body)
	_ = tmpFile.Close()
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return map[string]string{}, nil
	}

	heads, err := t.git.BundleHeads(ctx, repo, tmpPath)
	if err != nil {
		return nil, err
	}

	allowed := make(map[string]bool, len(refs))
	for _, r := range refs {
		allowed[r] = true
	}
	for ref := range heads {
		if !allowed[ref] {
			return nil, fmt.Errorf("%w: ref %s", ErrUnexpectedRef, ref)
		}
	}

	return t.git.FetchBundle(ctx, repo, tmpPath, refs)
}

// openBundleRegular re-opens a just-written temp bundle for reading: O_NOFOLLOW
// so a symlink swapped in where the bundle was cannot redirect the read out of
// the temp directory, and a handle Stat so anything that is not a regular file
// (a fifo that would block, a device) is refused. The temp dir may be
// tenant-writable, so both checks are needed.
func openBundleRegular(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|noFollow, 0)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("bundle %s is not a regular file", path)
	}
	return f, nil
}
