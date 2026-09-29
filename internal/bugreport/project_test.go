package bugreport

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/fuad-daoud/relevo/internal/store"
)

// TestDefaultBundleCarriesNoContent pins the allow-list rule: the fixture hides
// a marker in the transcript path, the entry payload, the hook output and the
// status tail, and none of them may reach either rendering.
func TestDefaultBundleCarriesNoContent(t *testing.T) {
	b := fixtureBundle()
	renders := map[string]string{
		"markdown": Markdown(b),
		"json":     asJSON(t, b.Doc()),
	}
	for what, text := range renders {
		for _, marker := range []string{transcriptMarker, payloadMarker, hookOutputMarker, tailMarker} {
			if strings.Contains(text, marker) {
				t.Errorf("the default %s bundle carries %q:\n%s", what, marker, text)
			}
		}
	}
}

// TestDefaultBundleCarriesNoSecrets pins the privacy rule: the fixture seeds a
// secret of every shape into fields the bundle does carry, and the pass leaves
// none of them behind.
func TestDefaultBundleCarriesNoSecrets(t *testing.T) {
	b := fixtureBundle()
	renders := map[string]string{
		"markdown": Markdown(b),
		"json":     asJSON(t, b.Doc()),
	}
	for what, text := range renders {
		for _, secret := range []string{
			githubToken, anthropicKey, awsKey, slackToken, jwtToken, pemBlock, bearerValue,
		} {
			if strings.Contains(text, secret) {
				t.Errorf("the default %s bundle carries a seeded secret:\n%s", what, text)
			}
		}
		if strings.Contains(text, "/home/fuad") || strings.Contains(text, "contabo-01") {
			t.Errorf("the default %s bundle carries a home path or the host name:\n%s", what, text)
		}
	}
}

// TestRoundsSectionKeepsTheTail pins the rounds projection's two selections and
// its cap: five entries per binding, one binding with name, one round with
// round.
func TestRoundsSectionKeepsTheTail(t *testing.T) {
	var entries []store.LogEntry
	for i := 1; i <= 8; i++ {
		entries = append(entries, store.LogEntry{Seq: i, Round: 1, Kind: store.KindPrompt, Payload: payloadMarker})
	}
	entries = append(entries, store.LogEntry{Seq: 9, Round: 2, Kind: store.KindReport, Payload: transcriptMarker})
	logs := []BindingLog{{Name: "alpha", Entries: entries}, {Name: "beta", Entries: entries}}

	all := RoundsSection(logs, "", 0)
	if len(all.Rows) != 5*2 {
		t.Errorf("rows = %d, want the last five of each of two bindings", len(all.Rows))
	}
	if got := all.Rows[0][1]; got != "5" {
		t.Errorf("the oldest kept row has seq %s, want 5", got)
	}

	one := RoundsSection(logs, "beta", 0)
	if len(one.Rows) != 5 {
		t.Fatalf("rows = %d, want only beta's five", len(one.Rows))
	}
	for _, row := range one.Rows {
		if row[0] != "beta" {
			t.Errorf("row names %s, want beta only", row[0])
		}
	}

	round := RoundsSection(logs, "alpha", 2)
	if len(round.Rows) != 1 || round.Rows[0][3] != "2" {
		t.Errorf("rows = %v, want only alpha's round 2", round.Rows)
	}
	for _, row := range round.Rows {
		for _, cell := range row {
			if strings.Contains(cell, payloadMarker) || strings.Contains(cell, transcriptMarker) {
				t.Errorf("a rounds row carries entry content: %q", cell)
			}
		}
	}
}

// TestTruncateMarksTheCut pins the byte cap D applies to a tail: the cut is
// marked, and it lands on a rune boundary.
func TestTruncateMarksTheCut(t *testing.T) {
	short := "a short tail"
	if got := Truncate(short, 64); got != short {
		t.Errorf("Truncate below the cap = %q, want the text unchanged", got)
	}
	got := Truncate(strings.Repeat("é", 40), 21)
	if !strings.Contains(got, "truncated at 21 bytes") {
		t.Errorf("Truncate does not mark the cut: %q", got)
	}
	head, _, _ := strings.Cut(got, "\n")
	if !utf8.ValidString(head) {
		t.Errorf("Truncate cut a rune in half: %q", head)
	}
	if n := len(head); n > 21 || n < 20 {
		t.Errorf("Truncate kept %d bytes, want the cap or the rune below it", n)
	}
}

// TestTailLinesKeepsTheNewest pins the line cap --logs applies to a transcript
// tail: the last n lines survive, whole, and a text already within the cap is
// unchanged.
func TestTailLinesKeepsTheNewest(t *testing.T) {
	if got := TailLines("one\ntwo", LogTailLines); got != "one\ntwo" {
		t.Errorf("TailLines below the cap = %q, want the text unchanged", got)
	}

	lines := make([]string, 0, LogTailLines+7)
	for i := 1; i <= LogTailLines+7; i++ {
		lines = append(lines, "line")
	}
	lines[LogTailLines+6] = "newest"
	got := TailLines(strings.Join(lines, "\n"), LogTailLines)
	kept := strings.Split(got, "\n")
	if len(kept) != LogTailLines {
		t.Fatalf("TailLines kept %d lines, want %d", len(kept), LogTailLines)
	}
	if kept[len(kept)-1] != "newest" {
		t.Errorf("TailLines dropped the newest line: %q", got)
	}
}

// asJSON renders one value the way a reader of --json sees it.
func asJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(raw)
}

// TestDocMirrorsTheBundle pins that the document is built from the same facts
// the markdown renders.
func TestDocMirrorsTheBundle(t *testing.T) {
	b := fixtureBundle()
	d := b.Doc()
	if d.Title != b.Title || d.Version != b.Version || !d.Created.Equal(b.Created) {
		t.Errorf("Doc header = %+v, want %+v", d, b)
	}
	if !reflect.DeepEqual(d.Sections, b.Sections) {
		t.Error("Doc sections differ from the bundle's")
	}
}
