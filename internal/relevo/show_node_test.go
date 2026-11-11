package relevo

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/store"
)

// errNodeFetch is the error a test remote answers with: any read path that
// reaches past the head store fails the test, so a node-fetch fallback
// inserted anywhere Show can reach turns these tests red.
var errNodeFetch = errors.New("node fetch refused")

// failingNodeFake is a remote that refuses every fetch: a Show that renders
// from the head store never calls it.
func failingNodeFake() *fakeRemote {
	return &fakeRemote{getBindingErr: errNodeFetch, roundFileErr: errNodeFetch}
}

// stageNodeFile writes one head-store file, creating its directory.
func stageNodeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestShowRemoteBindingReadsHeadStoreFiles pins the no-fallback rule: a
// remote binding still renders every section from the head store's own
// files, with the remote refusing all fetches. The read paths this covers
// are showLive's file closures over rt.Store in show.go (searched with rg -n
// "GetBinding|RoundFile" internal/relevo/show*.go, which names no production
// fetcher) and the chain-check gate fallback behind a present gate log.
func TestShowRemoteBindingReadsHeadStoreFiles(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	api := store.Binding{
		Name: "api", CWD: "/repo", Round: 2, State: store.StateActive,
		BuilderCandidate: testAgyRef,
		Builder:          store.Endpoint{Mode: store.ModeRemote, Server: "zen", RemoteStatus: "running"},
	}
	if err := st.Save(api); err != nil {
		t.Fatalf("Save api: %v", err)
	}
	rev := store.Binding{
		Name: "rev", CWD: "/repo", Round: 2, State: store.StateActive, Role: "reviewer", Shape: store.ShapeReader,
		BuilderCandidate: testAgyRef,
		Builder:          store.Endpoint{Mode: store.ModeRemote, Server: "zen", RemoteStatus: "running"},
	}
	if err := st.Save(rev); err != nil {
		t.Fatalf("Save rev: %v", err)
	}

	stageNodeFile(t, st.PromptPath("api", 1), "# plan\n")
	stageNodeFile(t, st.ReportPath("api", 1), "# report\n")
	stageNodeFile(t, st.BuilderLogPath("api", 1), "log line\n")
	stageNodeFile(t, st.GateLogPath("api", 1), "gate log\n")
	stageNodeFile(t, st.FindingsPath("api", 1, "c1"), "findings body\n")
	stageNodeFile(t, st.PromptPath("api", 2), "# plan 2\n")
	putRow := func(name string, round int, path, body string) {
		t.Helper()
		if err := st.WithLock(func(tx *store.Tx) error {
			return tx.PutRoundFile(name, round, path, []byte(body))
		}); err != nil {
			t.Fatalf("PutRoundFile %s: %v", path, err)
		}
	}
	putRow("api", 1, st.DiffPath("api", 1), "diff body\n")
	putRow("api", 1, st.DriftPath("api", 1), "drift body\n")

	// Round 2 has a stream and no builder log, so its transcript renders the
	// stream from the head store.
	agy := jsonlLine(t, "agy.jsonl", 5)
	claude := jsonlLine(t, "claude.jsonl", 5) + jsonlLine(t, "claude.jsonl", 6)
	stream := agy + claude
	stageNodeFile(t, st.RunnerStreamPath("api", 2), stream)
	segs := []store.StreamSegment{{Start: 0, Kind: "agy"}, {Start: int64(len(agy)), Kind: "claude"}}
	putRow("api", 2, st.BuilderSegmentsPath("api", 2), mustJSON(t, segs))

	for _, e := range []store.LogEntry{
		{TS: baseTime, Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt, Confirmed: true},
		{TS: baseTime.Add(time.Minute), Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Confirmed: true, Outcome: "done"},
		{TS: baseTime.Add(2 * time.Minute), Round: 2, Direction: store.DirToBuilder, Kind: store.KindPrompt, Confirmed: true},
	} {
		if err := st.AppendLog("api", e); err != nil {
			t.Fatalf("AppendLog api: %v", err)
		}
	}
	for _, e := range []store.LogEntry{
		{TS: baseTime, Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt, Confirmed: true},
		{TS: baseTime.Add(time.Minute), Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Confirmed: true, Outcome: "done"},
	} {
		if err := st.AppendLog("rev", e); err != nil {
			t.Fatalf("AppendLog rev: %v", err)
		}
	}
	artifactDir := st.ArtifactDir("rev", 1, "reviewer")
	if err := os.MkdirAll(filepath.Join(artifactDir, "site"), 0o755); err != nil {
		t.Fatalf("mkdir artifacts: %v", err)
	}
	stageNodeFile(t, filepath.Join(artifactDir, "findings.md"), "# the findings\n")
	stageNodeFile(t, filepath.Join(artifactDir, "site", "index.html"), "<html>\n")

	fr := failingNodeFake()
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	show := func(name string, round int, section ShowSection, findingsID, artifact string) ShowResult {
		t.Helper()
		res, err := Show(context.Background(), rt, ShowOptions{
			Name: name, Round: round, Section: section, Peek: true,
			FindingsID: findingsID, ArtifactRel: artifact,
		})
		if err != nil {
			t.Fatalf("Show %s %s round %d: %v", name, section, round, err)
		}
		return res
	}

	textCases := []struct {
		name     string
		round    int
		section  ShowSection
		findings string
		artifact string
		want     string
	}{
		{"api", 1, ShowPrompt, "", "", "# plan\n"},
		{"api", 1, ShowReport, "", "", "# report\n"},
		{"api", 1, ShowDiff, "", "", "diff body\n"},
		{"api", 1, ShowDrift, "", "", "drift body\n"},
		{"api", 1, ShowTranscript, "", "", "log line\n"},
		{"api", 1, ShowGate, "", "", "gate log\n"},
		{"api", 1, ShowFindings, "c1", "", "findings body\n"},
		{"rev", 1, ShowReport, "", "", "# the findings\n"},
		{"rev", 1, ShowOutput, "", "", "# the findings\n"},
		{"rev", 1, ShowArtifacts, "", "site/index.html", "<html>\n"},
	}
	for _, tt := range textCases {
		if got := show(tt.name, tt.round, tt.section, tt.findings, tt.artifact); got.Text != tt.want || got.Missing {
			t.Errorf("Show %s %s = %q missing=%v, want %q", tt.name, tt.section, got.Text, got.Missing, tt.want)
		}
	}

	if got := show("api", 2, ShowTranscript, "", ""); got.Missing || string(got.Text) != string(renderStream([]byte(stream), segs, "")) {
		t.Errorf("Show api transcript round 2 = %q missing=%v, want the rendered head-store stream", got.Text, got.Missing)
	}

	if got := show("api", 1, ShowLog, "", ""); len(got.Events) != 2 {
		t.Errorf("Show api log events = %d, want the head store's 2 round-1 entries", len(got.Events))
	}

	artifacts := show("rev", 1, ShowArtifacts, "", "")
	if len(artifacts.Artifacts) != 2 || artifacts.Artifacts[0].Rel != "findings.md" || artifacts.Artifacts[1].Rel != "site/index.html" {
		t.Errorf("Show rev artifacts = %+v, want findings.md then site/index.html", artifacts.Artifacts)
	}

	if len(fr.calls) != 0 {
		t.Errorf("remote calls = %v, want none: a remote binding reads the head store", fr.calls)
	}
}

// TestShowPulledChainMemberReadsHeadStoreFiles pins the chain-member half
// of the no-fallback rule: after the existing chain-pull path installs a
// member's rounds, Show reads the member's head-store files, with the
// server refusing all fetches.
func TestShowPulledChainMemberReadsHeadStoreFiles(t *testing.T) {
	t.Parallel()

	fr := chainPullFake(chainPullView("shop", string(chain.StatusRunning), 1, 0, 0))
	rt := chainPullRuntime(t, fr)
	seedServerChain(t, rt, "shop")
	pullRounds(t, rt, "shop")

	refused := failingNodeFake()
	rt.Remote = refused
	show := func(section ShowSection) ShowResult {
		t.Helper()
		res, err := Show(context.Background(), rt, ShowOptions{Name: "shop", Round: 1, Section: section, Peek: true})
		if err != nil {
			t.Fatalf("Show shop %s: %v", section, err)
		}
		return res
	}
	if got := show(ShowPrompt); got.Text != "the round prompt\n" || got.Missing {
		t.Errorf("prompt = %q missing=%v, want the pulled head-store prompt", got.Text, got.Missing)
	}
	if got := show(ShowReport); !strings.Contains(got.Text, "finished the plan") || got.Missing {
		t.Errorf("report = %q missing=%v, want the pulled head-store report", got.Text, got.Missing)
	}
	if got := show(ShowTranscript); !strings.Contains(got.Text, "builder log") || got.Missing {
		t.Errorf("transcript = %q missing=%v, want the pulled head-store log", got.Text, got.Missing)
	}
	if len(refused.calls) != 0 {
		t.Errorf("remote calls = %v, want none: a pulled member reads the head store", refused.calls)
	}
}

// TestShowTraceChainReadsHeadStore pins the trace half: a chain still
// renders its trace from the head store's chain rows, with the remote
// refusing all fetches.
func TestShowTraceChainReadsHeadStore(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	traceChain(t, rt)

	fr := failingNodeFake()
	rt.Remote = fr
	res, err := Show(context.Background(), rt, ShowOptions{Name: "shop", Section: ShowTrace})
	if err != nil {
		t.Fatalf("Show trace: %v", err)
	}
	const want = "build r1 → review\n" +
		"review r1  verdict=changes → correct\n" +
		"correct r1 → build-fix\n" +
		"build-fix r2 → review\n" +
		"review r2  verdict=pass → done\n"
	if res.Text != want || res.Trace == nil {
		t.Errorf("trace = %q trace-doc %v, want the head store's five transitions", res.Text, res.Trace != nil)
	}
	if len(fr.calls) != 0 {
		t.Errorf("remote calls = %v, want none: the trace reads the head store", fr.calls)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	body, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(body)
}
