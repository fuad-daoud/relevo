package release

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// These tests are httptest-only: none may reach the network or github.com.
const (
	testTag     = "v0.13.0"
	testArchive = "relevo_v0.13.0_linux_amd64.tar.gz"
	fakeBinary  = "#!/bin/sh\necho fake\n"
)

type tarEntry struct {
	name     string
	data     []byte
	typeflag byte
	linkname string
}

func tarGz(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		hdr := &tar.Header{
			Name:     e.name,
			Mode:     0o755,
			Size:     int64(len(e.data)),
			Typeflag: e.typeflag,
			Linkname: e.linkname,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header %s: %v", e.name, err)
		}
		if _, err := tw.Write(e.data); err != nil {
			t.Fatalf("write %s: %v", e.name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buf.Bytes()
}

func checksumLine(data []byte, name string) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]) + "  " + name + "\n"
}

type assetServer struct {
	archive       []byte
	checksums     []byte
	archiveStatus int
}

// newAssetServer serves /<tag>/checksums.txt and /<tag>/<archive> and returns the base URL.
func newAssetServer(t *testing.T, s assetServer) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/"+testTag+"/", func(w http.ResponseWriter, r *http.Request) {
		switch path.Base(r.URL.Path) {
		case "checksums.txt":
			_, _ = w.Write(s.checksums)
		case testArchive:
			if s.archiveStatus != 0 {
				http.Error(w, "no archive", s.archiveStatus)
				return
			}
			_, _ = w.Write(s.archive)
		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

// assertEmptyDir pins that no temp file is left behind on any failure path.
func assertEmptyDir(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read temp dir: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("destDir %s is not empty: %v", dir, names)
	}
}

func TestFetchBinaryHappyPath(t *testing.T) {
	archive := tarGz(t,
		tarEntry{name: "relevo", data: []byte(fakeBinary), typeflag: tar.TypeReg},
		tarEntry{name: "README.md", data: []byte("read me\n"), typeflag: tar.TypeReg},
	)
	base := newAssetServer(t, assetServer{
		archive:   archive,
		checksums: []byte(checksumLine(archive, testArchive)),
	})
	dest := t.TempDir()

	got, err := (&Downloader{Base: base}).FetchBinary(context.Background(), testTag, "linux", "amd64", dest)
	if err != nil {
		t.Fatalf("FetchBinary: %v", err)
	}
	if filepath.Clean(filepath.Dir(got)) != filepath.Clean(dest) {
		t.Errorf("binary in %s, want %s", filepath.Dir(got), dest)
	}
	info, err := os.Stat(got)
	if err != nil {
		t.Fatalf("stat %s: %v", got, err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v, want 0755", info.Mode().Perm())
	}
	data, err := os.ReadFile(got)
	if err != nil {
		t.Fatalf("read %s: %v", got, err)
	}
	if string(data) != fakeBinary {
		t.Errorf("contents = %q, want %q", data, fakeBinary)
	}
}

type fetchBinaryRefusal struct {
	name          string
	entries       []tarEntry
	checksums     func(archive []byte) []byte
	archiveStatus int
	wantErr       error
	wantSubstr    string
}

// fetchBinaryRefusals is every way FetchBinary must refuse and write nothing.
var fetchBinaryRefusals = []fetchBinaryRefusal{
	{
		name:    "wrong checksum is refused before anything is written",
		entries: []tarEntry{{name: "relevo", data: []byte(fakeBinary), typeflag: tar.TypeReg}},
		checksums: func([]byte) []byte {
			return []byte(checksumLine([]byte("something else"), testArchive))
		},
		wantErr: ErrChecksumMismatch,
	},
	{
		name:    "checksums.txt without a line for the archive",
		entries: []tarEntry{{name: "relevo", data: []byte(fakeBinary), typeflag: tar.TypeReg}},
		checksums: func(archive []byte) []byte {
			return []byte(checksumLine(archive, "relevo_v0.12.0_linux_amd64.tar.gz"))
		},
		wantErr: ErrNoChecksum,
	},
	{
		name:      "archive with no relevo at the root",
		entries:   []tarEntry{{name: "README.md", data: []byte("read me\n"), typeflag: tar.TypeReg}},
		checksums: func(archive []byte) []byte { return []byte(checksumLine(archive, testArchive)) },
		wantErr:   ErrNoBinary,
	},
	{
		// Only the archive root counts, not "../relevo" or "sub/relevo".
		name: "a relevo outside the archive root is not the binary",
		entries: []tarEntry{
			{name: "../relevo", data: []byte(fakeBinary), typeflag: tar.TypeReg},
			{name: "sub/relevo", data: []byte(fakeBinary), typeflag: tar.TypeReg},
		},
		checksums: func(archive []byte) []byte { return []byte(checksumLine(archive, testArchive)) },
		wantErr:   ErrNoBinary,
	},
	{
		name:      "a symlink named relevo is skipped: only a regular file is the binary",
		entries:   []tarEntry{{name: "relevo", typeflag: tar.TypeSymlink, linkname: "/bin/sh"}},
		checksums: func(archive []byte) []byte { return []byte(checksumLine(archive, testArchive)) },
		wantErr:   ErrNoBinary,
	},
	{
		name:    "a duplicate checksum line",
		entries: []tarEntry{{name: "relevo", data: []byte(fakeBinary), typeflag: tar.TypeReg}},
		checksums: func(archive []byte) []byte {
			line := checksumLine(archive, testArchive)
			return []byte(line + line)
		},
		wantErr: ErrNoChecksum,
	},
	{
		name:    "an uppercase hash is not 64 lowercase hex characters",
		entries: []tarEntry{{name: "relevo", data: []byte(fakeBinary), typeflag: tar.TypeReg}},
		checksums: func(archive []byte) []byte {
			sum := sha256.Sum256(archive)
			return []byte(strings.ToUpper(hex.EncodeToString(sum[:])) + "  " + testArchive + "\n")
		},
		wantErr: ErrNoChecksum,
	},
	{
		name:    "a 63-character hash is not 64 lowercase hex characters",
		entries: []tarEntry{{name: "relevo", data: []byte(fakeBinary), typeflag: tar.TypeReg}},
		checksums: func(archive []byte) []byte {
			sum := sha256.Sum256(archive)
			good := hex.EncodeToString(sum[:])
			return []byte(good[:63] + "  " + testArchive + "\n")
		},
		wantErr: ErrNoChecksum,
	},
	{
		name:          "a non-200 archive response names the status",
		entries:       []tarEntry{{name: "relevo", data: []byte(fakeBinary), typeflag: tar.TypeReg}},
		checksums:     func(archive []byte) []byte { return []byte(checksumLine(archive, testArchive)) },
		archiveStatus: http.StatusNotFound,
		wantSubstr:    "404",
	},
}

func TestFetchBinaryRefusals(t *testing.T) {
	for _, tc := range fetchBinaryRefusals {
		t.Run(tc.name, func(t *testing.T) {
			archive := tarGz(t, tc.entries...)
			base := newAssetServer(t, assetServer{
				archive:       archive,
				checksums:     tc.checksums(archive),
				archiveStatus: tc.archiveStatus,
			})
			dest := t.TempDir()

			_, err := (&Downloader{Base: base}).FetchBinary(context.Background(), testTag, "linux", "amd64", dest)
			switch {
			case tc.wantErr != nil:
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("FetchBinary = %v, want %v", err, tc.wantErr)
				}
			case tc.wantSubstr != "":
				if err == nil || !strings.Contains(err.Error(), tc.wantSubstr) {
					t.Fatalf("FetchBinary = %v, want an error naming %q", err, tc.wantSubstr)
				}
			}
			assertEmptyDir(t, dest)
		})
	}
}

// TestFetchBinaryRefusesNonReleaseTag pins that a bad tag is refused before
// any request; the counter proves the server was never asked.
func TestFetchBinaryRefusesNonReleaseTag(t *testing.T) {
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	dest := t.TempDir()

	_, err := (&Downloader{Base: srv.URL}).FetchBinary(context.Background(), "v1.2.3/../x", "linux", "amd64", dest)
	if !errors.Is(err, ErrBadTag) {
		t.Fatalf("FetchBinary = %v, want ErrBadTag", err)
	}
	if got := requests.Load(); got != 0 {
		t.Errorf("server saw %d request(s), want 0: a bad tag must never reach the network", got)
	}
	assertEmptyDir(t, dest)
}
