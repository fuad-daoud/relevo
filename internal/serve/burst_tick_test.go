package serve

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/burst"
	"github.com/fuad-daoud/relevo/internal/hooks"
	"github.com/fuad-daoud/relevo/internal/remote"
)

// tickTrace is the single chronological log the tick's stages write into
// through the node, the hooks and the provider, so one burstTick pins its
// own stage order.
type tickTrace struct {
	mu    sync.Mutex
	lines []string
}

func (tr *tickTrace) add(line string) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.lines = append(tr.lines, line)
}

func (tr *tickTrace) snapshot() []string {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return append([]string(nil), tr.lines...)
}

func (tr *tickTrace) reset() {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.lines = nil
}

// tracingBurstProvider records the report call's place in the trace and
// keeps every listed node live, so the tick's merge neither drops rows nor
// owes more than the one report.
type tracingBurstProvider struct {
	trace *tickTrace
	resp  burst.ReportResponse
}

func (p *tracingBurstProvider) Report(burst.ReportRequest) (burst.ReportResponse, []string, error) {
	p.trace.add("report")
	return p.resp, nil, nil
}

// tracingHooks records dispatched events in the trace.
type tracingHooks struct{ trace *tickTrace }

func (h tracingHooks) Dispatch(_ context.Context, e hooks.Event) {
	h.trace.add("hook:" + string(e.Type))
}

// traceNode answers like a node with nothing running yet: the first view
// request refuses (no binding), the create and the start succeed, the next
// view request says the round runs, and WhoAmI answers clean. Marks name
// the call, not the path: the node binding carries a run-random owner hash
// no expected sequence can spell out. Anything else reaches the trace as an
// unexpected line, so the tick fails its order check instead of silently
// dialling an endpoint the order test does not model.
func traceNode(tr *tickTrace) http.Handler {
	var views atomic.Int64
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/bindings/"):
			if views.Add(1) == 1 {
				tr.add("binding-get")
				writeJSON(w, http.StatusNotFound, remote.ErrorBody{Code: remote.CodeNotFound, Message: "not found"})
				return
			}
			tr.add("binding-get")
			writeJSON(w, http.StatusOK, stubRoundView("nb", 1, remote.RoundRunning))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/bindings":
			tr.add("binding-create")
			writeJSON(w, http.StatusCreated, stubRoundView("nb", 1, remote.RoundRunning))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/align"):
			tr.add("round-align")
			writeJSON(w, http.StatusOK, stubRoundView("nb", 1, remote.RoundIdle))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/rounds"):
			tr.add("round-start")
			writeJSON(w, http.StatusCreated, stubRoundView("nb", 1, remote.RoundRunning))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/whoami":
			tr.add("whoami")
			writeJSON(w, http.StatusOK, remote.WhoAmI{})
		default:
			tr.add("unexpected " + r.Method + " " + r.URL.Path)
			writeJSON(w, http.StatusInternalServerError, remote.ErrorBody{Code: remote.Code("internal"), Message: "unexpected call"})
		}
	})
}

// TestBurstTickRunsThePipelineInDesignOrder pins the tick order in one
// burstTick: the requeue fires its loss hook first, placement claims the
// requeued round and the waiting one (no send dials without a claim), the
// send's create and start precede the same node's observe view request, the
// verify WhoAmI follows the observe pass, and the report answers last --
// with the confirmed counts feeding the forwarded due reason.
func TestBurstTickRunsThePipelineInDesignOrder(t *testing.T) {
	// The clock is frozen from this instant: the round the test creates
	// through its own signed client must land inside the nonce window, and
	// the tick then runs on a time the expected trace can state exactly.
	clock := time.Now().UTC()
	headKP, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	pem, err := remote.MarshalPrivate(headKP)
	if err != nil {
		t.Fatalf("marshal head client key: %v", err)
	}
	tr := &tickTrace{}
	url, fp := startStubNode(t, traceNode(tr))
	b1 := stubRow(t, "b1", url, fp)
	b2 := burstRowFor("b2", url, fp)
	env := setupTestEnv(t, func(cfg *Config) {
		cfg.Burst = burstTestPolicy()
		cfg.ClientKey = pem
		cfg.Provider = &tracingBurstProvider{
			trace: tr,
			resp: burst.ReportResponse{Version: burst.ReportVersion, Nodes: []burst.ReportNodeOut{
				{ID: "b1", Name: b1.Name, URL: b1.URL, Fingerprint: b1.Fingerprint, Cap: b1.Cap, State: "ready"},
				{ID: "b2", Name: b2.Name, URL: b2.URL, Fingerprint: b2.Fingerprint, Cap: b2.Cap, State: "ready"},
			}, ReportEvery: "30s"},
		}
		cfg.MaxBuilders = 1
		cfg.Now = func() time.Time { return clock }
		cfg.Hooks = tracingHooks{trace: tr}
	})
	env.srv.burstTable = &burst.NodeTable{Nodes: []burst.NodeRow{b1, b2}}
	rt := env.runtime(t)
	saveBurstBinding(t, rt, "zz", time.Time{}, 4242)
	resp, body := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan for api")
	requireCreated(t, resp, body, "api")
	saveLostForward(t, env, "lost", "b0", "running", 1, clock.Add(-time.Minute))
	tr.reset()

	env.srv.burstTick(context.Background())

	want := []string{
		"hook:burst_node_lost",
		"binding-get", "binding-create", "round-align", "round-start",
		"binding-get",
		"whoami",
		"report",
	}
	got := tr.snapshot()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("tick trace =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	// The round the requeue just freed is sitting on the live node again:
	// placement ran on this tick's post-requeue census, not the old one.
	replaced := burstStoredForward(loadBurstBinding(t, rt, "lost"))
	if replaced == nil || replaced.Node != "b1" || replaced.Phase != "sending" {
		t.Errorf("lost forward = %+v, want requeued then re-placed sending on b1", replaced)
	}
	if fwd := burstStoredForward(loadBurstBinding(t, rt, "api")); fwd == nil || fwd.Phase != "running" {
		t.Errorf("api forward = %+v, want the send confirmed it running", fwd)
	}
	// The verify pass ran before the report: the fresh node is verified in
	// the table the report then snapshotted.
	var row burst.NodeRow
	for _, r := range env.srv.copyBurstTable().Nodes {
		if r.ID == "b2" {
			row = r
		}
	}
	if !row.Verified {
		t.Errorf("b2 verified = false, want the tick's verify pass marked it")
	}
}
