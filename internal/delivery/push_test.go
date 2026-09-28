package delivery

import (
	"errors"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
)

// mapReader turns a fixed set of files into the read func PushText takes,
// so cases need no temp files.
func mapReader(files map[string][]byte) func(string) ([]byte, error) {
	return func(path string) ([]byte, error) {
		b, ok := files[path]
		if !ok {
			return nil, errors.New("no such file")
		}
		return b, nil
	}
}

func TestPushTextExpandsReportWithReadablePath(t *testing.T) {
	t.Parallel()

	origin := `relevo: round 1 · to MasterMind · about runner "w" (not the human)`
	e := store.LogEntry{
		Kind:    store.KindReport,
		Path:    "/x/001-report.md",
		Payload: origin + "\n\nThe runner finished round 1. Report: /x/001-report.md",
	}
	read := mapReader(map[string][]byte{"/x/001-report.md": []byte("the report body")})

	got, ok := PushText(e, store.Binding{Name: "webshop"}, read)
	if !ok {
		t.Fatal("want ok=true for an expandable kind with a readable path")
	}
	want := e.Payload + "\n\nthe report body"
	if got != want {
		t.Errorf("PushText = %q, want %q", got, want)
	}
}

// TestLogRefFollowsTheShape pins the `relevo show` section a pushed artifact
// names: a reader's report entry is its output, a writer's its report, and a
// kind with no section answers "".
func TestLogRefFollowsTheShape(t *testing.T) {
	t.Parallel()

	reader := store.Binding{Name: "atlas", Shape: store.ShapeReader}
	writer := store.Binding{Name: "webshop", Shape: store.ShapeWriter}
	report := store.LogEntry{Kind: store.KindReport, Round: 1}

	if got, want := LogRef(reader, report), "relevo show atlas --round 1 --output"; got != want {
		t.Errorf("reader LogRef = %q, want %q", got, want)
	}
	if got, want := LogRef(writer, report), "relevo show webshop --round 1 --report"; got != want {
		t.Errorf("writer LogRef = %q, want %q", got, want)
	}
	if got := LogRef(reader, store.LogEntry{Kind: store.KindPrompt, Round: 1}); got != "" {
		t.Errorf("prompt LogRef = %q, want empty", got)
	}
}

func TestPushTextDoesNotExpandDiff(t *testing.T) {
	t.Parallel()

	e := store.LogEntry{Kind: store.KindDiff, Path: "/x/001.patch", Payload: "diff payload"}
	read := mapReader(map[string][]byte{"/x/001.patch": []byte("patch body")})

	got, ok := PushText(e, store.Binding{Name: "webshop"}, read)
	if ok || got != e.Payload {
		t.Errorf("PushText(diff) = (%q, %v), want (%q, false)", got, ok, e.Payload)
	}
}

func TestPushTextDoesNotExpandWithNoPath(t *testing.T) {
	t.Parallel()

	e := store.LogEntry{Kind: store.KindReport, Payload: "no path here"}
	read := mapReader(nil)

	got, ok := PushText(e, store.Binding{Name: "webshop"}, read)
	if ok || got != e.Payload {
		t.Errorf("PushText(no path) = (%q, %v), want (%q, false)", got, ok, e.Payload)
	}
}

func TestPushTextReadErrorReturnsPayload(t *testing.T) {
	t.Parallel()

	e := store.LogEntry{Kind: store.KindReport, Path: "/missing", Payload: "payload stands alone"}
	read := mapReader(nil)

	got, ok := PushText(e, store.Binding{Name: "webshop"}, read)
	if ok || got != e.Payload {
		t.Errorf("PushText(read error) = (%q, %v), want (%q, false)", got, ok, e.Payload)
	}
}

func TestPushTextTruncatesAtNewlineWithinBudget(t *testing.T) {
	t.Parallel()

	var b strings.Builder
	for b.Len() < MaxPushBytes+1000 {
		b.WriteString("0123456789\n")
	}
	body := b.String()
	e := store.LogEntry{
		Kind:    store.KindFindings,
		Round:   4,
		Path:    "/x/004-7f2a3c1d-findings.md",
		Payload: `relevo: consult · to MasterMind · about runner "w" (not the human)`,
	}
	read := mapReader(map[string][]byte{"/x/004-7f2a3c1d-findings.md": []byte(body)})

	got, ok := PushText(e, store.Binding{Name: "webshop"}, read)
	if !ok {
		t.Fatal("want ok=true")
	}
	wantLine := "[truncated at 64 KiB -- full text: relevo show webshop --round 4 --findings 7f2a3c1d]"
	if !strings.HasSuffix(got, wantLine) {
		t.Fatalf("PushText does not end with the truncation line: %q", got[max(0, len(got)-len(wantLine)-5):])
	}

	fileText := strings.TrimPrefix(got, e.Payload+"\n\n")
	fileText = strings.TrimSuffix(fileText, wantLine)
	if len(fileText) > MaxPushBytes {
		t.Errorf("kept file text = %d bytes, want <= %d (MaxPushBytes)", len(fileText), MaxPushBytes)
	}
	if !strings.HasSuffix(fileText, "\n") {
		t.Error("truncated text must be cut back to the last newline, not mid-line")
	}
}

func TestPushTextTruncatesAtBudgetWhenNoNewline(t *testing.T) {
	t.Parallel()

	body := strings.Repeat("x", MaxPushBytes+1000) // no newline anywhere
	e := store.LogEntry{Kind: store.KindReport, Round: 1, Path: "/x/huge.md", Payload: "relevo: round 1 · to MasterMind · about runner \"w\" (not the human)"}
	read := mapReader(map[string][]byte{"/x/huge.md": []byte(body)})

	got, ok := PushText(e, store.Binding{Name: "webshop"}, read)
	if !ok {
		t.Fatal("want ok=true")
	}
	wantLine := "[truncated at 64 KiB -- full text: relevo show webshop --round 1 --report]"
	if !strings.HasSuffix(got, wantLine) {
		t.Fatalf("PushText does not end with the truncation line, got tail %q", got[max(0, len(got)-len(wantLine)-5):])
	}
	fileText := strings.TrimPrefix(got, e.Payload+"\n\n")
	fileText = strings.TrimSuffix(fileText, wantLine)
	if len(fileText) > MaxPushBytes+1 { // +1 for the appended newline before the truncation line
		t.Errorf("kept file text = %d bytes, want roughly MaxPushBytes (%d)", len(fileText), MaxPushBytes)
	}
}

func TestPushTextOriginLineIsAlwaysFirst(t *testing.T) {
	t.Parallel()

	origin := `relevo: round 1 · to MasterMind · about runner "w" (not the human)`

	cases := []struct {
		name string
		e    store.LogEntry
		read func(string) ([]byte, error)
	}{
		{
			name: "expanded",
			e:    store.LogEntry{Kind: store.KindReport, Path: "/x/001-report.md", Payload: origin + "\n\nbody pointer"},
			read: mapReader(map[string][]byte{"/x/001-report.md": []byte("the body")}),
		},
		{
			name: "diff, unexpanded",
			e:    store.LogEntry{Kind: store.KindDiff, Path: "/x/001.patch", Payload: origin + "\n\ndiff pointer"},
			read: mapReader(map[string][]byte{"/x/001.patch": []byte("patch")}),
		},
		{
			name: "read error",
			e:    store.LogEntry{Kind: store.KindReport, Path: "/missing", Payload: origin + "\n\npointer"},
			read: mapReader(nil),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := PushText(tc.e, store.Binding{Name: "webshop"}, tc.read)
			firstLine, _, _ := strings.Cut(got, "\n")
			if firstLine != origin {
				t.Errorf("first line = %q, want origin %q", firstLine, origin)
			}
		})
	}
}
