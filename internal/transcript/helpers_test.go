package transcript

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func streamFixture(t *testing.T, name string, n int) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "usage", "testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if n > len(lines) {
		t.Fatalf("fixture %s has %d lines; want line %d", name, len(lines), n)
	}
	return lines[n-1]
}

func fixture(name string) func(t *testing.T) []byte {
	return func(t *testing.T) []byte {
		t.Helper()
		data, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatalf("read fixture %s: %v", name, err)
		}
		return data
	}
}

func raw(s string) func(*testing.T) []byte {
	return func(*testing.T) []byte { return []byte(s) }
}
