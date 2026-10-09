package relevo

import (
	"context"
	"os"
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
)

func TestStatusRowCarriesNextInputs(t *testing.T) {
	t.Parallel()
	rt := newRuntime(t)
	if err := rt.Store.Save(store.Binding{Name: "api", CWD: "/repo", Round: 2, State: store.StateActive}); err != nil {
		t.Fatal(err)
	}
	prompt := rt.Store.PromptPath("api", 1)
	for _, e := range []store.LogEntry{
		{TS: rt.Now(), Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt, Path: prompt},
		{TS: rt.Now(), Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Path: "/x/001-report.md"},
	} {
		if err := rt.Store.AppendLog("api", e); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatal(err)
	}
	if got := rep.Bindings[0].PromptPath; got != prompt {
		t.Errorf("PromptPath = %q, want %q", got, prompt)
	}

	q := rt.Store.QuestionPath("api", 2)
	if err := os.WriteFile(q, []byte("\nWhich branch?\nmore\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := rt.Store.AppendLog("api", store.LogEntry{TS: rt.Now(), Round: 2, Direction: store.DirToMasterMind, Kind: store.KindQuestion, Path: q}); err != nil {
		t.Fatal(err)
	}
	rep, err = Status(context.Background(), rt)
	if err != nil {
		t.Fatal(err)
	}
	if got := rep.Bindings[0].Question; got != "Which branch?" {
		t.Errorf("Question = %q", got)
	}
}
