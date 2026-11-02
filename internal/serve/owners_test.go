package serve

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// TestStrayBindingsEntryWarnsOncePerEntry pins the warn-once dedupe: two
// strays (a non-hash directory and a stray file) driven through every
// per-owner walk repeatedly warn once per entry, and a later stray still
// warns.
func TestStrayBindingsEntryWarnsOncePerEntry(t *testing.T) {
	strayBindingsWarned = sync.Map{}

	s, root := newTestServer(t, 0)
	bindingsDir := filepath.Join(root, "bindings")
	if err := os.MkdirAll(bindingsDir, 0o755); err != nil {
		t.Fatalf("mkdir bindings: %v", err)
	}
	if err := os.Mkdir(filepath.Join(bindingsDir, "not-a-hash-dir"), 0o755); err != nil {
		t.Fatalf("mkdir stray dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bindingsDir, "stray-file"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write stray file: %v", err)
	}

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	noop := func(remote.ClientID, string, []store.Binding) {}
	scan := func() {
		t.Helper()
		if _, err := s.ownerRoots(); err != nil {
			t.Fatalf("ownerRoots: %v", err)
		}
		if err := s.walkOwners(noop); err != nil {
			t.Fatalf("walkOwners: %v", err)
		}
		if err := s.settleAllServed(); err != nil {
			t.Fatalf("settleAllServed: %v", err)
		}
		if _, err := s.collectSettled(context.Background()); err != nil {
			t.Fatalf("collectSettled: %v", err)
		}
		if _, err := s.heldCPUs("", nil, ""); err != nil {
			t.Fatalf("heldCPUs: %v", err)
		}
		warnStrayBindingsEntry("not-a-hash-dir")
		warnStrayBindingsEntry("stray-file")
	}

	for range 3 {
		scan()
	}

	if n := strings.Count(buf.String(), "unexpected entry in bindings dir"); n != 2 {
		t.Fatalf("stray warnings after three passes = %d, want 2:\n%s", n, buf.String())
	}
	for _, name := range []string{"not-a-hash-dir", "stray-file"} {
		if n := strings.Count(buf.String(), name); n != 1 {
			t.Errorf("%s warned %d times, want 1", name, n)
		}
	}

	if err := os.Mkdir(filepath.Join(bindingsDir, "late-stray"), 0o755); err != nil {
		t.Fatalf("mkdir late stray: %v", err)
	}
	scan()

	if n := strings.Count(buf.String(), "unexpected entry in bindings dir"); n != 3 {
		t.Fatalf("stray warnings after a new entry = %d, want 3:\n%s", n, buf.String())
	}
	if n := strings.Count(buf.String(), "late-stray"); n != 1 {
		t.Errorf("late-stray warned %d times, want 1", n)
	}
}
