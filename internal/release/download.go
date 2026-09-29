package release

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
	"time"
)

// DownloadTimeout is the budget for one whole HTTP request; a stalled
// connection must not hang the CLI forever.
const DownloadTimeout = 5 * time.Minute

// These bounds keep a hostile or broken endpoint from making relevo allocate without limit.
const (
	maxChecksums = 1 << 20
	maxArchive   = 256 << 20
	maxBinary    = 256 << 20
)

// Sentinel errors FetchBinary returns, each wrapped with the URL or tag by the caller side.
var (
	ErrNoChecksum       = errors.New("no checksum for the archive")
	ErrBadTag           = errors.New("not a release tag")
	ErrChecksumMismatch = errors.New("archive checksum mismatch")
	ErrNoBinary         = errors.New("archive holds no relevo binary")
	ErrTooLarge         = errors.New("response too large")
)

// Downloader fetches a release archive, verifies it against checksums.txt and
// extracts the relevo binary into a temp file beside the running executable.
type Downloader struct {
	Base   string       // download base URL; tests pass an httptest URL
	Client *http.Client // nil uses one with DownloadTimeout
}

// FetchBinary produces a verified, executable relevo binary for tag as a temp
// file in destDir. Nothing is written to disk before the checksum passes, and
// no temp file is left behind on any error.
func (d *Downloader) FetchBinary(ctx context.Context, tag, goos, goarch, destDir string) (string, error) {
	if !IsReleaseTag(tag) {
		return "", fmt.Errorf("%q: %w", tag, ErrBadTag)
	}

	archiveURL, checksumsURL := assetURLsFrom(d.Base, tag, goos, goarch)
	archiveName := path.Base(archiveURL)

	sum, err := d.fetchChecksum(ctx, checksumsURL, archiveName)
	if err != nil {
		return "", err
	}

	data, err := d.get(ctx, archiveURL, maxArchive)
	if err != nil {
		return "", err
	}
	want, err := hex.DecodeString(sum)
	if err != nil {
		return "", fmt.Errorf("%s: %w", checksumsURL, ErrNoChecksum)
	}
	got := sha256.Sum256(data)
	if subtle.ConstantTimeCompare(got[:], want) != 1 {
		return "", fmt.Errorf("%s: %w", archiveURL, ErrChecksumMismatch)
	}

	bin, err := extractBinary(data)
	if err != nil {
		return "", err
	}

	return writeTempBinary(destDir, bin)
}

// fetchChecksum returns the SHA-256 for archiveName from checksums.txt.
// Exactly one line must name it, with a 64-char lowercase-hex first field.
func (d *Downloader) fetchChecksum(ctx context.Context, url, archiveName string) (string, error) {
	body, err := d.get(ctx, url, maxChecksums)
	if err != nil {
		return "", err
	}

	matches := 0
	sum := ""
	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[1] != archiveName {
			continue
		}
		matches++
		sum = fields[0]
	}
	if matches != 1 || !isLowerHex64(sum) {
		return "", fmt.Errorf("%s: %w", url, ErrNoChecksum)
	}
	return sum, nil
}

// get performs one bounded, unauthenticated, un-retried GET.
func (d *Downloader) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	client := d.Client
	if client == nil {
		client = &http.Client{Timeout: DownloadTimeout}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrOffline, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", url, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s: %w", url, ErrTooLarge)
	}
	return data, nil
}

// extractBinary gunzips data and returns the first regular file named relevo
// at the archive root; no entry name ever becomes a filesystem path.
func extractBinary(data []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNoBinary, err)
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrNoBinary, err)
		}
		if hdr.Typeflag != tar.TypeReg || path.Clean(hdr.Name) != "relevo" {
			continue
		}
		bin, err := io.ReadAll(io.LimitReader(tr, maxBinary+1))
		if err != nil {
			return nil, err
		}
		if int64(len(bin)) > maxBinary {
			return nil, ErrTooLarge
		}
		return bin, nil
	}
	return nil, ErrNoBinary
}

// writeTempBinary creates a temp file in destDir, writes bin, and makes it
// executable; every failure removes the temp file.
func writeTempBinary(destDir string, bin []byte) (string, error) {
	f, err := os.CreateTemp(destDir, ".relevo-update-*")
	if err != nil {
		return "", err
	}
	tmp := f.Name()
	fail := func(err error) (string, error) {
		_ = f.Close()
		_ = os.Remove(tmp)
		return "", err
	}

	if _, err := f.Write(bin); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return tmp, nil
}

// isLowerHex64 reports whether s is exactly 64 lowercase hex characters.
func isLowerHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
