package serve

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/fuad-daoud/relevo/internal/burst"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/store"
)

// A forwarded round the head watches lives on its node until the observe
// path brings it home. observeForwards polls each running or closed forward
// without s.mu and applies the answer only while the forward still matches,
// the way the adopt and send paths apply theirs.

// observeWork is one forward the collect pass saw, with the owner root the
// apply needs and the forward copy the match checks against.
type observeWork struct {
	ownerPath string
	id        remote.ClientID
	name      string
	fwd       store.Forward
}

// nodeLiveKey names one head binding's cached node view: the owner root
// disambiguates names that repeat across owners.
func nodeLiveKey(root, name string) string {
	return root + "\x00" + name
}

// observeForwards brings closed node rounds home and refreshes the cached
// node live views. It returns how many forwards closed or acked, which the
// tick threads into the report reason the way it threads sends.
func (s *Server) observeForwards(ctx context.Context) int {
	if !s.burstOn() {
		return 0
	}
	now := s.cfg.Now().UTC()
	t := s.copyBurstTable()
	if t == nil {
		return 0
	}
	rows := make(map[string]burst.NodeRow, len(t.Nodes))
	for _, r := range t.Nodes {
		rows[r.ID] = r
	}
	var work []observeWork
	_ = s.walkOwners(func(id remote.ClientID, ownerPath string, bindings []store.Binding) {
		for _, b := range bindings {
			fwd := burstStoredForward(b)
			if fwd == nil || (fwd.Phase != "running" && fwd.Phase != "closed") {
				continue
			}
			work = append(work, observeWork{ownerPath: ownerPath, id: id, name: b.Name, fwd: *fwd})
		}
	})
	// A node that no longer holds a closed forward has no ack episode to
	// carry: the forward left by ack, requeue or parking, and the count
	// dies with it until a fresh forward starts failing again. An empty
	// work set ends every episode.
	holdingClosed := make(map[string]bool, len(work))
	for _, w := range work {
		if w.fwd.Phase == "closed" {
			holdingClosed[w.fwd.Node] = true
		}
	}
	s.burstHealth.forgetAckEpisodes(holdingClosed)
	if len(work) == 0 {
		return 0
	}
	seen := make(map[string]bool, len(work))
	dial := make([]burst.NodeRow, 0, len(work))
	for _, w := range work {
		if seen[w.fwd.Node] {
			continue
		}
		seen[w.fwd.Node] = true
		if r, ok := rows[w.fwd.Node]; ok {
			dial = append(dial, r)
		}
	}
	if len(dial) == 0 {
		return 0
	}
	cl, err := s.burstNodeClient(dial)
	if err != nil {
		slog.Warn("burst: observe skipped, client key unusable", "err", err)
		return 0
	}
	closed := 0
	for _, w := range work {
		row, ok := rows[w.fwd.Node]
		if !ok {
			continue
		}
		if s.observeOne(ctx, cl, row, w, now) {
			closed++
		}
	}
	return closed
}

// observeOne polls one forward. A view answering the forward's own round
// refreshes the node-live cache; a closed view fetches and closes; a
// waiting view halts the head binding. Every network call runs without
// s.mu, and each answer applies only while the forward still matches.
func (s *Server) observeOne(ctx context.Context, cl *client.Client, row burst.NodeRow, w observeWork, now time.Time) bool {
	view, err := cl.GetBinding(ctx, w.fwd.Node, w.fwd.NodeBinding)
	if err != nil {
		if burstForwardUnreachable(err) {
			s.noteNodeUnreachable(now, row)
		} else {
			slog.Warn("burst: observe fetch failed", "binding", w.name, "node", w.fwd.Node, "err", err)
		}
		return false
	}
	s.clearNodeProblem(row)
	// The cache is keyed by the forward's round and served as that round's
	// live, so a view answering another round must never land in it: the
	// node's next round would be served as the head round's work. The
	// switch below only warns on that mismatch after the fact, so the
	// agreement check has to gate the write here.
	if view.Live != nil && observeRoundAgrees(view, w) {
		s.cacheNodeLive(w.ownerPath, w.name, w.fwd.Round, view.Live, now)
	}
	switch view.RoundState {
	case remote.RoundQueued, remote.RoundRunning:
		s.retryForwardStop(ctx, cl, row, w, now)
		return s.observeLive(view, w)
	case remote.RoundClosed:
		return s.observeClosed(ctx, cl, row, w, view, now)
	case remote.RoundNeedsYou, remote.RoundBroken:
		s.observeNeedsYou(ctx, cl, row, w, view, now)
		return false
	default:
		slog.Warn("burst: observe answered an unexpected state", "binding", w.name, "node", w.fwd.Node, "state", view.RoundState)
		return false
	}
}

// observeRoundAgrees says whether a view answers the round its forward
// holds: an open view carries it in Round, a closed one in ClosedRound.
// Any other state is not one the live cache keys.
func observeRoundAgrees(view remote.BindingView, w observeWork) bool {
	switch view.RoundState {
	case remote.RoundQueued, remote.RoundRunning:
		return view.Round == w.fwd.Round
	case remote.RoundClosed:
		return view.ClosedRound == w.fwd.Round
	}
	return false
}

// observeLive watches an open node round. A view for a different round is
// stale: nothing was cached for it and the forward stays out.
func (s *Server) observeLive(view remote.BindingView, w observeWork) bool {
	if view.Round != w.fwd.Round {
		slog.Warn("burst: observe answered another round", "binding", w.name, "node", w.fwd.Node, "want", w.fwd.Round, "got", view.Round)
		return false
	}
	return false
}

// cacheNodeLive stores one node live view under the head binding's key. A
// nil view stores nothing: ServedLive falls through to the local row.
func (s *Server) cacheNodeLive(ownerPath, name string, round int, v *remote.LiveView, now time.Time) {
	if v == nil || s.nodeLive == nil {
		return
	}
	cp := *v
	s.nodeLive.put(nodeLiveKey(ownerPath, name), round, now, &cp)
}

// observeMatches reports whether cur under the lock is still the binding the
// fetch was for: same forward node and binding, same head and forward round,
// and a phase the observe still owns.
func observeMatches(cur store.Binding, fwd store.Forward) bool {
	have := burstStoredForward(cur)
	if have == nil {
		return false
	}
	if have.Node != fwd.Node || have.NodeBinding != fwd.NodeBinding {
		return false
	}
	if cur.Round != fwd.Round || have.Round != fwd.Round {
		return false
	}
	return have.Phase == "running" || have.Phase == "closed"
}

// observeClosedAckMatches reports whether cur still holds the closed forward
// the ack was for: same node and round, still waiting for its ack. The head
// round has advanced past the forward by then, so only the forward round
// is compared.
func observeClosedAckMatches(cur store.Binding, fwd store.Forward) bool {
	have := burstStoredForward(cur)
	if have == nil {
		return false
	}
	if have.Node != fwd.Node || have.NodeBinding != fwd.NodeBinding {
		return false
	}
	return have.Round == fwd.Round && have.Phase == "closed"
}

// observeFetch holds one closed round's downloads: the round files in
// memory and the bundle's temp file under $TMPDIR, "" when the node held
// nothing new.
type observeFetch struct {
	view   remote.BindingView
	report []byte
	diff   []byte
	log    []byte
	stream []byte
	bundle string
	dir    string
}

// observeClosed fetches a closed node round and brings it home. A fetch the
// head has not completed never closes: the round stays out for the next
// cycle. True reports a close or an ack clear, which the tick threads into
// the report reason.
func (s *Server) observeClosed(ctx context.Context, cl *client.Client, row burst.NodeRow, w observeWork, view remote.BindingView, now time.Time) bool {
	if view.ClosedRound != w.fwd.Round {
		slog.Warn("burst: observe answered another closed round", "binding", w.name, "node", w.fwd.Node, "want", w.fwd.Round, "got", view.ClosedRound)
		return false
	}
	fetch, ok := s.fetchObserveClosed(ctx, cl, row, w, view)
	if !ok {
		return false
	}
	defer func() {
		if fetch.dir != "" {
			_ = os.RemoveAll(fetch.dir)
		}
	}()
	if !s.applyObserveClosed(ctx, w, fetch, now) {
		return false
	}
	if !s.ackObserveClosed(ctx, cl, w) {
		return true
	}
	return true
}

// fetchObserveClosed downloads a closed round's files and bundle into a
// $TMPDIR directory. Report, diff, log and stream ride RoundFile; the bundle
// rides RoundBundle. A missing diff, log or stream is absent, not a failure.
// False means nothing was fetched and the round stays out.
func (s *Server) fetchObserveClosed(ctx context.Context, cl *client.Client, row burst.NodeRow, w observeWork, view remote.BindingView) (*observeFetch, bool) {
	dir, err := os.MkdirTemp("", "relevo-observe-*")
	if err != nil {
		slog.Warn("burst: observe temp dir failed", "binding", w.name, "err", err)
		return nil, false
	}
	fetch := &observeFetch{view: view, dir: dir}
	if !s.fetchObserveFile(ctx, cl, row, w, view, "report", &fetch.report) {
		_ = os.RemoveAll(dir)
		return nil, false
	}
	if fetch.report == nil && view.Stopped == "" {
		slog.Warn("burst: observe closed without a report", "binding", w.name, "node", w.fwd.Node)
		_ = os.RemoveAll(dir)
		return nil, false
	}
	for _, kind := range []string{"diff", "log", "stream"} {
		var body *[]byte
		switch kind {
		case "diff":
			body = &fetch.diff
		case "log":
			body = &fetch.log
		default:
			body = &fetch.stream
		}
		if !s.fetchObserveOptional(ctx, cl, row, w, view, kind, body) {
			_ = os.RemoveAll(dir)
			return nil, false
		}
	}
	if !s.fetchObserveBundle(ctx, cl, row, w, view, fetch) {
		_ = os.RemoveAll(dir)
		return nil, false
	}
	return fetch, true
}

// fetchObserveFile downloads one required round file into memory. A 404
// stays nil for the caller to judge; an unreachable failure marks the row;
// any other failure drops the fetch.
func (s *Server) fetchObserveFile(ctx context.Context, cl *client.Client, row burst.NodeRow, w observeWork, view remote.BindingView, kind string, dst *[]byte) bool {
	rc, err := cl.RoundFile(ctx, w.fwd.Node, w.fwd.NodeBinding, view.ClosedRound, kind)
	if err != nil {
		if isObserveNotFound(err) {
			return true
		}
		if burstForwardUnreachable(err) {
			s.noteNodeUnreachable(s.cfg.Now().UTC(), row)
		} else {
			slog.Warn("burst: observe fetch failed", "binding", w.name, "node", w.fwd.Node, "kind", kind, "err", err)
		}
		return false
	}
	defer func() { _ = rc.Close() }()
	body, err := io.ReadAll(rc)
	if err != nil {
		slog.Warn("burst: observe read failed", "binding", w.name, "node", w.fwd.Node, "kind", kind, "err", err)
		return false
	}
	*dst = body
	s.clearNodeProblem(row)
	return true
}

// fetchObserveOptional downloads one optional round file: a 404 is absent,
// an unreachable failure marks the row, any other failure drops the fetch.
func (s *Server) fetchObserveOptional(ctx context.Context, cl *client.Client, row burst.NodeRow, w observeWork, view remote.BindingView, kind string, dst *[]byte) bool {
	rc, err := cl.RoundFile(ctx, w.fwd.Node, w.fwd.NodeBinding, view.ClosedRound, kind)
	if err != nil {
		if isObserveNotFound(err) {
			return true
		}
		if burstForwardUnreachable(err) {
			s.noteNodeUnreachable(s.cfg.Now().UTC(), row)
		} else {
			slog.Warn("burst: observe fetch failed", "binding", w.name, "node", w.fwd.Node, "kind", kind, "err", err)
		}
		return false
	}
	defer func() { _ = rc.Close() }()
	body, err := io.ReadAll(rc)
	if err != nil {
		slog.Warn("burst: observe read failed", "binding", w.name, "node", w.fwd.Node, "kind", kind, "err", err)
		return false
	}
	*dst = body
	s.clearNodeProblem(row)
	return true
}

// isObserveNotFound reports whether err is the node's 404 for a round file
// the round never wrote: absent, not a failure.
func isObserveNotFound(err error) bool {
	return burstHTTPStatus(err, http.StatusNotFound)
}

// fetchObserveBundle downloads the round bundle into the fetch dir. A 204
// (nil stream) means the node held nothing new and skips the absorb.
func (s *Server) fetchObserveBundle(ctx context.Context, cl *client.Client, row burst.NodeRow, w observeWork, view remote.BindingView, fetch *observeFetch) bool {
	rc, err := cl.RoundBundle(ctx, w.fwd.Node, w.fwd.NodeBinding, view.ClosedRound, "")
	if err != nil {
		if burstForwardUnreachable(err) {
			s.noteNodeUnreachable(s.cfg.Now().UTC(), row)
		} else {
			slog.Warn("burst: observe bundle failed", "binding", w.name, "node", w.fwd.Node, "err", err)
		}
		return false
	}
	if rc == nil {
		return true
	}
	defer func() { _ = rc.Close() }()
	path := filepath.Join(fetch.dir, "bundle")
	f, err := os.Create(path)
	if err != nil {
		slog.Warn("burst: observe bundle file failed", "binding", w.name, "err", err)
		return false
	}
	if _, err := io.Copy(f, rc); err != nil {
		_ = f.Close()
		slog.Warn("burst: observe bundle write failed", "binding", w.name, "err", err)
		return false
	}
	if err := f.Close(); err != nil {
		slog.Warn("burst: observe bundle close failed", "binding", w.name, "err", err)
		return false
	}
	fetch.bundle = path
	s.clearNodeProblem(row)
	return true
}

// applyObserveClosed absorbs the bundle and writes the head-store round
// files, then records the close with Phase kept closed until the ack lands. False means the forward moved and nothing applied.
func (s *Server) applyObserveClosed(ctx context.Context, w observeWork, fetch *observeFetch, now time.Time) bool {
	st := s.ownerStore(w.ownerPath)
	cur, err := st.Load(w.name)
	if err != nil {
		slog.Warn("burst: observe close: load failed", "binding", w.name, "err", err)
		return false
	}
	if !observeMatches(cur, w.fwd) {
		return false
	}
	rt := s.runtimeAt(w.ownerPath)
	if fetch.bundle != "" {
		if !s.absorbObserveBundle(ctx, rt, cur, w.fwd, fetch) {
			return false
		}
	}
	if !writeObserveFiles(rt, cur.Name, w.fwd.Round, fetch) {
		return false
	}
	next, err := st.Load(w.name)
	if err != nil {
		return false
	}
	if !observeMatches(next, w.fwd) {
		return false
	}
	if !appendForwardedCloseFacts(rt, w.name, w.fwd, fetch, now) {
		return false
	}
	next.Round = w.fwd.Round + 1
	if !closeForwardedRound(ctx, rt, &next, w.fwd, fetch.view.DirtyCommit != "") {
		return false
	}
	have := burstStoredForward(next)
	if have == nil {
		return false
	}
	have.Phase = "closed"
	if err := st.Save(next); err != nil {
		slog.Warn("burst: observe close: save failed", "binding", w.name, "err", err)
		return false
	}
	return true
}

// absorbObserveBundle absorbs the node bundle into the head bare repo and
// fast-forwards the head branch and side ref onto the result. The bundle
// carries the node binding's refs, so the absorb names those before moving
// the head's own refs to the same commits.
func (s *Server) absorbObserveBundle(ctx context.Context, rt relevo.Runtime, cur store.Binding, fwd store.Forward, fetch *observeFetch) bool {
	if cur.Serve == nil || cur.Serve.BareRepo == "" || cur.Branch == "" {
		slog.Warn("burst: observe close: binding has no repo facts", "binding", cur.Name)
		return false
	}
	f, err := os.Open(fetch.bundle)
	if err != nil {
		slog.Warn("burst: observe bundle open failed", "binding", cur.Name, "err", err)
		return false
	}
	defer func() { _ = f.Close() }()
	nodeBranch := "refs/heads/relevo/" + fwd.NodeBinding
	refs := []string{nodeBranch}
	if fetch.view.DirtyCommit != "" {
		refs = append(refs, "refs/relevo/"+fwd.NodeBinding+"/round-"+strconv.Itoa(fwd.Round))
	}
	if _, err := rt.Transport.Absorb(ctx, cur.Serve.BareRepo, remote.ContentTypeGitBundle, f, refs); err != nil {
		slog.Warn("burst: observe absorb failed", "binding", cur.Name, "err", err)
		return false
	}
	branchSHA, ok, err := rt.Git.RefSHA(ctx, cur.Serve.BareRepo, nodeBranch)
	if err != nil || !ok {
		slog.Warn("burst: observe branch missing after absorb", "binding", cur.Name, "err", err)
		return false
	}
	if err := rt.Git.UpdateRef(ctx, cur.Serve.BareRepo, "refs/heads/"+cur.Branch, branchSHA, ""); err != nil {
		slog.Warn("burst: observe branch not moved", "binding", cur.Name, "err", err)
		return false
	}
	if fetch.view.DirtyCommit != "" {
		nodeSide := "refs/relevo/" + fwd.NodeBinding + "/round-" + strconv.Itoa(fwd.Round)
		sideSHA, ok, err := rt.Git.RefSHA(ctx, cur.Serve.BareRepo, nodeSide)
		if err != nil || !ok {
			slog.Warn("burst: observe side ref missing after absorb", "binding", cur.Name, "err", err)
			return false
		}
		headSide := "refs/relevo/" + cur.Name + "/round-" + strconv.Itoa(fwd.Round)
		if err := rt.Git.UpdateRef(ctx, cur.Serve.BareRepo, headSide, sideSHA, ""); err != nil {
			slog.Warn("burst: observe side ref not moved", "binding", cur.Name, "err", err)
			return false
		}
	}
	return true
}

// writeObserveFiles writes the fetched round files into the head store. A
// nil body is absent, not a failure, and leaves any existing file alone.
func writeObserveFiles(rt relevo.Runtime, name string, round int, fetch *observeFetch) bool {
	if !putForwardedDiff(rt, name, round, fetch.diff) {
		return false
	}
	for _, item := range []struct {
		body []byte
		path string
	}{
		{fetch.report, rt.Store.ReportPath(name, round)},
		{fetch.log, rt.Store.BuilderLogPath(name, round)},
		{fetch.stream, rt.Store.StreamPath(name, round)},
	} {
		if item.body == nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(item.path), 0o755); err != nil {
			slog.Warn("burst: observe file dir failed", "binding", name, "path", item.path, "err", err)
			return false
		}
		if err := os.WriteFile(item.path, item.body, 0o644); err != nil {
			slog.Warn("burst: observe file write failed", "binding", name, "path", item.path, "err", err)
			return false
		}
	}
	return true
}
