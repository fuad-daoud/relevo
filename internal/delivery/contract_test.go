package delivery

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
)

// update rewrites every testdata/contract/*.golden file this test binary
// touches. No other test in this package declares an "update" flag (verified
// with grep before adding it).
var update = flag.Bool("update", false, "rewrite testdata/contract/*.golden")

func assertGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	filename := name
	if !strings.HasSuffix(filename, ".golden") {
		filename += ".golden"
	}
	path := filepath.Join("testdata", "contract", filename)

	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir golden dir: %v", err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("write golden %s: %v", path, err)
		}
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden file %s: re-run with 'go test ./internal/relevo -run Contract -update' to generate", path)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("golden mismatch in %s: re-run with 'go test ./internal/relevo -run Contract -update' to update\n--- got ---\n%s\n--- want ---\n%s",
			path, got, want)
	}
}

// TestContractPushText pins C8b: PushText for each store log kind it
// expands (report, findings, edge) and one it does not (plan). read is a
// fake that returns a fixed, deterministic body naming the path it was asked
// for, so the golden needs no real file on disk and no normalization.
func TestContractPushText(t *testing.T) {
	t.Parallel()

	read := func(path string) ([]byte, error) {
		return []byte(fmt.Sprintf("file body for %s\n", path)), nil
	}

	cases := []struct {
		name string
		e    store.LogEntry
	}{
		{
			"report",
			store.LogEntry{Kind: store.KindReport, Round: 1, Path: "/x/001-report.md", Payload: "origin: webshop round 1"},
		},
		{
			"findings",
			store.LogEntry{Kind: store.KindFindings, Round: 1, Path: "/x/001-7f2a3c1d-findings.md", Payload: "origin: webshop round 1"},
		},
		{
			"edge",
			store.LogEntry{Kind: store.KindEdge, Round: 1, Path: "/x/001-edge.md", Payload: "origin: webshop round 1"},
		},
		{
			// KindPrompt is not expandable: PushText must return the payload
			// unchanged and ok=false, even though Path is set.
			"plan-not-expanded",
			store.LogEntry{Kind: store.KindPrompt, Round: 1, Path: "/x/001-plan.md", Payload: "origin: webshop round 1"},
		},
	}

	for _, c := range cases {
		text, ok := PushText(c.e, "webshop", read)
		got := fmt.Sprintf("ok=%v\n%s", ok, text)
		assertGolden(t, "push-"+c.name, []byte(got))
	}
}
