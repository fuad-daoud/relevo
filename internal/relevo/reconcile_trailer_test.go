package relevo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/pathscope"
	"github.com/fuad-daoud/relevo/internal/reporttail"
	"github.com/fuad-daoud/relevo/internal/store"
)

// TestQueueReportNotesIgnoredTokenTrailer pins the warning carry: a report
// whose block closes on a token-trailed fence closes with the block's status,
// and the report note records that the trailer was ignored. A clean block
// carries no such note.
func TestQueueReportNotesIgnoredTokenTrailer(t *testing.T) {
	t.Parallel()

	closeRound := func(t *testing.T, body string) store.LogEntry {
		t.Helper()
		fr := newFakeRunner()
		rt, b := seedHeadless(t, fr)
		if err := os.MkdirAll(filepath.Dir(rt.Store.ReportPath(b.Name, b.Round)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(rt.Store.ReportPath(b.Name, b.Round), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		var next store.Binding
		err := rt.Store.WithLock(func(tx *store.Tx) error {
			cur, err := tx.Load(b.Name)
			if err != nil {
				return err
			}
			entries, err := tx.ReadLog(b.Name)
			if err != nil {
				return err
			}
			next, err = queueReport(context.Background(), rt, tx, cur, entries, rt.Store.ReportPath(b.Name, b.Round), "done", "test", nil, nil, nil, nil, "", false, "", pathscope.Verdict{})
			return err
		})
		if err != nil {
			t.Fatalf("queueReport: %v", err)
		}
		if err := rt.Store.Save(next); err != nil {
			t.Fatal(err)
		}
		entries, err := rt.Store.ReadLog(b.Name)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.Round == b.Round && e.Kind == store.KindReport {
				return e
			}
		}
		t.Fatalf("no report entry for round %d in %+v", b.Round, entries)
		return store.LogEntry{}
	}

	const warning = "tail: closing fence carried leaked tokens; trailer ignored"

	t.Run("token-trailed fence closes done with the warning in the note", func(t *testing.T) {
		t.Parallel()
		body := "```relevo\n" +
			"status: done\n" +
			"halted_at: \"\"\n" +
			"changed_paths: []\n" +
			"commands_run: []\n" +
			"not_done: []\n" +
			"```<|end|><|start|>assistant<|channel|>analysis<|message|>done<|end|>\n" +
			"touch /tmp/done\n" +
			"```\n"
		entry := closeRound(t, body)
		if entry.Outcome != reporttail.OutcomeDone {
			t.Errorf("Outcome = %q, want %q", entry.Outcome, reporttail.OutcomeDone)
		}
		if !strings.Contains(entry.Note, warning) {
			t.Errorf("Note = %q, want it to contain %q", entry.Note, warning)
		}
	})

	t.Run("clean fence closes done with no warning in the note", func(t *testing.T) {
		t.Parallel()
		body := "```relevo\nstatus: done\nchanged_paths: []\ncommands_run: []\nnot_done: []\n```\n"
		entry := closeRound(t, body)
		if entry.Outcome != reporttail.OutcomeDone {
			t.Errorf("Outcome = %q, want %q", entry.Outcome, reporttail.OutcomeDone)
		}
		if strings.Contains(entry.Note, warning) {
			t.Errorf("Note = %q, want no trailer warning", entry.Note)
		}
	})
}
