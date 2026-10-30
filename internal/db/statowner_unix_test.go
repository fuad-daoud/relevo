//go:build unix

package db

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStatOwnerNamesTheFilesOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	uid, gid, ok := StatOwner(fi)
	if !ok || uid != uint32(os.Getuid()) || gid != uint32(os.Getgid()) {
		t.Fatalf("StatOwner = %d, %d, %v; want %d, %d, true", uid, gid, ok, os.Getuid(), os.Getgid())
	}
}

func TestStatOwnerWithoutAStatReportsNoOwner(t *testing.T) {
	if _, _, ok := StatOwner(bareInfo{}); ok {
		t.Fatal("StatOwner on a FileInfo with no Stat_t reported an owner")
	}
}

type bareInfo struct{}

func (bareInfo) Name() string       { return "bare" }
func (bareInfo) Size() int64        { return 0 }
func (bareInfo) Mode() os.FileMode  { return 0 }
func (bareInfo) ModTime() time.Time { return time.Time{} }
func (bareInfo) IsDir() bool        { return false }
func (bareInfo) Sys() any           { return nil }
