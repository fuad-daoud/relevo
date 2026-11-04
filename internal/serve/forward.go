package serve

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"time"

	"github.com/fuad-daoud/relevo/internal/burst"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/resolve"
	"github.com/fuad-daoud/relevo/internal/store"
)

// A sending forward is a claim the node has not answered yet. sendForwards
// ships each one and confirms it on the node's yes; a refusal returns the
// round to the local queue, while a transport failure keeps the claim for
// the next tick. Every network call runs without s.mu, and each answer
// applies only while the forward still matches, the way the adopt and
// requeue paths apply theirs.

// sendWork is one sending forward with the binding that claimed it and the
// ship base the collect pass saw. The ship base rides along because the
// confirm must not advance past what this pass shipped.
type sendWork struct {
	ownerPath string
	id        remote.ClientID
	b         store.Binding
	since     string
}

// sendForwards ships every sending forward to its node and returns how many
// the nodes confirmed. Rows that are not verified or not ready take no
// forward: an unverified node never proved it is the pinned peer, and a
// draining one is going away.
func (s *Server) sendForwards(ctx context.Context, now time.Time) int {
	if !s.burstOn() {
		return 0
	}
	t := s.copyBurstTable()
	if t == nil {
		return 0
	}
	rows := make(map[string]burst.NodeRow, len(t.Nodes))
	for _, r := range t.Nodes {
		rows[r.ID] = r
	}
	var pending []sendWork
	_ = s.walkOwners(func(id remote.ClientID, ownerPath string, bindings []store.Binding) {
		for _, b := range bindings {
			f := burstStoredForward(b)
			if f == nil || f.Phase != "sending" {
				continue
			}
			if f.StopRequested {
				s.closeStopRequestedSend(ctx, ownerPath, b.Name, *f)
				continue
			}
			row, ok := rows[f.Node]
			if !ok || !row.Verified || row.State != "ready" {
				continue
			}
			var since string
			if b.Burst != nil {
				since = b.Burst.Shipped[f.Node]
			}
			pending = append(pending, sendWork{ownerPath: ownerPath, id: id, b: b, since: since})
		}
	})
	if len(pending) == 0 {
		return 0
	}
	seen := make(map[string]bool, len(pending))
	dial := make([]burst.NodeRow, 0, len(pending))
	for _, p := range pending {
		n := p.b.Burst.Forward.Node
		if !seen[n] {
			seen[n] = true
			dial = append(dial, rows[n])
		}
	}
	cl, err := s.burstNodeClient(dial)
	if err != nil {
		slog.Warn("burst: send skipped, client key unusable", "err", err)
		return 0
	}
	confirmed := 0
	for _, p := range pending {
		if s.sendOne(ctx, cl, rows[p.b.Burst.Forward.Node], p, now) {
			confirmed++
		}
	}
	return confirmed
}

// forwardShipPatience bounds a claim the head cannot ship. Most causes clear by
// the next tick, but one that does not would hold the node's seat for as long
// as the head runs.
const forwardShipPatience = 5 * time.Minute

var errForwardNotShipped = errors.New("nothing to ship")

// sendOne ships one sending forward: the node binding is created when
// missing, its round count moved up to the head's, and the round starts with
// the head's own plan, candidate, tier and tags. A started round confirms, and
// so does a conflict when the node holds this very round. A refusal requeues,
// and anything else keeps the claim for the next tick.
// A send that never confirms restores the staged forward ref, so the next
// send stages fresh instead of wedging on this attempt's leftover.
func (s *Server) sendOne(ctx context.Context, cl *client.Client, row burst.NodeRow, w sendWork, now time.Time) bool {
	fwd := *w.b.Burst.Forward
	rt := s.runtimeAt(w.ownerPath)
	ship, stage, ok := s.shipForward(ctx, rt, w.b, fwd, w.since)
	if !ok {
		restoreForwardStage(ctx, rt, stage)
		if now.Sub(fwd.ClaimedAt) > forwardShipPatience {
			s.refuseForward(ctx, w, fwd, now, "burst: forward could not ship, requeued", errForwardNotShipped)
		}
		return false
	}
	defer func() {
		if ship.snap.Body != nil {
			_ = ship.snap.Body.Close()
		}
	}()
	created, ok := s.ensureNodeBinding(ctx, cl, row, w, ship, now)
	if !ok {
		restoreForwardStage(ctx, rt, stage)
		return false
	}
	var bundle io.Reader
	if !ship.snap.Empty && ship.snap.Body != nil {
		bundle = ship.snap.Body
	}
	if _, aerr := cl.AlignRound(ctx, row.ID, fwd.NodeBinding, fwd.Round); aerr != nil {
		s.noteSendFailure(ctx, row, w, fwd, now, aerr)
		restoreForwardStage(ctx, rt, stage)
		return false
	}
	_, serr := cl.StartRound(ctx, row.ID, fwd.NodeBinding, fwd.Round, ship.plan,
		bundle, ship.tier, w.b.BuilderCandidate, false, ship.tags, false, nil)
	if burstHTTPStatus(serr, http.StatusConflict) {
		serr = conflictUnlessHeld(ctx, cl, row, fwd, serr)
	}
	if serr == nil {
		if s.confirmSend(w, row, ship.outSHA, now, created) {
			return true
		}
		restoreForwardStage(ctx, rt, stage)
		return false
	}
	s.noteSendFailure(ctx, row, w, fwd, now, serr)
	restoreForwardStage(ctx, rt, stage)
	return false
}

// conflictUnlessHeld reads what a conflict on a start means. It is the answer
// to a resend of a round the node already took, and just as much to a round it
// will never run, and only the first may confirm: a forward believed running
// on a node that has no such round waits forever and holds the node open.
func conflictUnlessHeld(ctx context.Context, cl *client.Client, row burst.NodeRow, fwd store.Forward, conflict error) error {
	view, err := cl.GetBinding(ctx, row.ID, fwd.NodeBinding)
	if err != nil {
		return err
	}
	if observeRoundAgrees(view, observeWork{fwd: fwd}) {
		return nil
	}
	return conflict
}

// noteSendFailure sorts a send the node did not take: a refusal or a conflict
// requeues, and anything else keeps the claim for the next tick.
func (s *Server) noteSendFailure(ctx context.Context, row burst.NodeRow, w sendWork, fwd store.Forward, now time.Time, err error) {
	if burstForwardRefused(err) || burstHTTPStatus(err, http.StatusConflict) {
		s.refuseForward(ctx, w, fwd, now, "burst: node refused a forward, requeued", err)
		return
	}
	if burstForwardUnreachable(err) {
		s.noteNodeUnreachable(now, row)
		return
	}
	slog.Warn("burst: send forward failed, kept sending", "owner", string(w.id),
		"binding", w.b.Name, "node", fwd.Node, "err", err)
}

// closeStopRequestedSend closes a sending forward the stop reached before
// the send: the head never contacts the node and the round closes the way
// stopping a queued local round closes it. The match is re-checked under the
// store lock, so a result that landed in between wins instead.
func (s *Server) closeStopRequestedSend(ctx context.Context, ownerPath, name string, fwd store.Forward) {
	st := s.ownerStore(ownerPath)
	rt := s.runtimeAt(ownerPath)
	if err := st.WithLock(func(tx *store.Tx) error {
		closed, err := relevo.CloseStopRequestedForward(ctx, rt, tx, name, fwd, "dequeued")
		if err != nil {
			return err
		}
		if !closed {
			slog.Info("burst: stop-requested send already moved", "binding", name, "node", fwd.Node)
		}
		return nil
	}); err != nil {
		slog.Warn("burst: stop-requested send not closed", "binding", name, "node", fwd.Node, "err", err)
	}
}

// forwardShip is everything one send ships: the round's staged plan, the
// bundle cut from the out ref since the node last took one, the out ref's
// own sha the confirm sets as the next base, and the tier and tags the
// round carries.
type forwardShip struct {
	plan   []byte
	snap   remote.Snapshot
	outSHA string
	repoID string
	tier   string
	tags   []remote.TagRef
}

// forwardStage is one staged forward ref: what shipForward wrote so a send
// that never confirms can put the ref back. The served cleanup drops only
// refs/relevo/<name>/*, which a forward ref never matches, and the requeue
// clears only the claim, so without the restore a staged ref would wedge
// the next send's availability guard.
type forwardStage struct {
	bare     string
	ref      string
	prior    string
	hadPrior bool
	staged   string
}

// restoreForwardStage puts back the forward ref a send staged but never
// confirmed. An absent ref deletes; a present one compare-and-swaps to the
// prior value, so a concurrent stage that moved the ref keeps it: no
// retry, no delete. A failed put-back only warns: the send's answer already
// decided the round.
func restoreForwardStage(ctx context.Context, rt relevo.Runtime, stage *forwardStage) {
	if stage == nil {
		return
	}
	if !stage.hadPrior {
		if err := rt.Git.DeleteRef(ctx, stage.bare, stage.ref); err != nil {
			slog.Warn("burst: staged forward ref not removed", "ref", stage.ref, "err", err)
		}
		return
	}
	if err := rt.Git.UpdateRef(ctx, stage.bare, stage.ref, stage.prior, stage.staged); err != nil {
		slog.Warn("burst: staged forward ref not restored", "ref", stage.ref, "err", err)
	}
}

// shipForward reads one forward's ship without touching the network. A
// binding without repo facts, a missing plan or out ref, unreadable tags
// or a failed snapshot keeps the forward sending for the next tick; an
// unknown ship base falls back to the full bundle, the way the client's
// own send does. Every path that staged carries the stage, even when the
// snapshot fails; paths that never staged carry none.
func (s *Server) shipForward(ctx context.Context, rt relevo.Runtime, b store.Binding, fwd store.Forward, since string) (*forwardShip, *forwardStage, bool) {
	if b.Serve == nil || b.Serve.BareRepo == "" || b.Serve.RepoID == "" {
		slog.Warn("burst: send forward: binding has no repo facts", "binding", b.Name)
		return nil, nil, false
	}
	plan, err := os.ReadFile(rt.Store.PromptPath(b.Name, fwd.Round))
	if err != nil {
		slog.Warn("burst: send forward: plan missing", "binding", b.Name, "round", fwd.Round, "err", err)
		return nil, nil, false
	}
	bare, repoID := b.Serve.BareRepo, b.Serve.RepoID
	tags, err := forwardTags(ctx, rt, bare)
	if err != nil {
		slog.Warn("burst: send forward: tags unreadable", "binding", b.Name, "err", err)
		return nil, nil, false
	}
	outRef := "refs/relevo/" + b.Name + "/out"
	outSHA, ok, err := rt.Git.RefSHA(ctx, bare, outRef)
	if err != nil || !ok {
		slog.Warn("burst: send forward: out ref missing", "binding", b.Name, "ref", outRef, "err", err)
		return nil, nil, false
	}
	// The node absorbs the bundle only into its own out ref, so the head
	// stages the same tree under the forward's ref before cutting the
	// bundle. A ref the head neither shipped last nor just staged is
	// another binding's out ref sharing the name: forwarding never
	// clobbers it, and the claim waits. The compare-and-swap carries the
	// read value, so a concurrent writer fails the stage instead of
	// losing its ref.
	fwdRef := "refs/relevo/" + fwd.NodeBinding + "/out"
	cur, ok, err := rt.Git.RefSHA(ctx, bare, fwdRef)
	if err != nil || (ok && cur != since && cur != outSHA) {
		slog.Warn("burst: send forward: forward ref not available", "binding", b.Name, "ref", fwdRef, "err", err)
		return nil, nil, false
	}
	stage := &forwardStage{bare: bare, ref: fwdRef, prior: cur, hadPrior: ok, staged: outSHA}
	if err := rt.Git.UpdateRef(ctx, bare, fwdRef, outSHA, cur); err != nil {
		slog.Warn("burst: send forward: forward ref not staged", "binding", b.Name, "ref", fwdRef, "err", err)
		return nil, nil, false
	}
	snap, err := rt.Transport.Snapshot(ctx, bare, []string{fwdRef}, since)
	if errors.Is(err, remote.ErrSinceUnknown) {
		snap, err = rt.Transport.Snapshot(ctx, bare, []string{fwdRef}, "")
	}
	if err != nil {
		slog.Warn("burst: send forward: snapshot failed", "binding", b.Name, "err", err)
		return nil, stage, false
	}
	return &forwardShip{
		plan: plan, snap: snap, outSHA: outSHA, repoID: repoID,
		tier: string(resolve.EffectiveTier(b)), tags: tags,
	}, stage, true
}

// forwardTags lists the bare repo's tags for the ship, sorted by name the
// way the client's own send ships them.
func forwardTags(ctx context.Context, rt relevo.Runtime, bare string) ([]remote.TagRef, error) {
	listed, err := rt.Git.ListTags(ctx, bare)
	if err != nil {
		return nil, err
	}
	tags := make([]remote.TagRef, 0, len(listed))
	for name, sha := range listed {
		tags = append(tags, remote.TagRef{Name: name, SHA: sha})
	}
	sort.Slice(tags, func(i, j int) bool { return tags[i].Name < tags[j].Name })
	return tags, nil
}

// ensureNodeBinding creates the round's binding on the node when it is
// missing, and reports whether this call created it: a start over a binding
// this send made is a cold forward, whatever the node's binding looks like
// when the confirm reads it back. A present binding is left alone: the start
// below answers an identical retry without sending twice. A refusal returns
// the round to the local queue, where it can run instead; a transport failure
// keeps the claim for the next tick.
func (s *Server) ensureNodeBinding(ctx context.Context, cl *client.Client, row burst.NodeRow, w sendWork, ship *forwardShip, now time.Time) (created bool, ok bool) {
	fwd := *w.b.Burst.Forward
	_, gerr := cl.GetBinding(ctx, row.ID, fwd.NodeBinding)
	if gerr == nil {
		return false, true
	}
	if !burstHTTPStatus(gerr, http.StatusNotFound) {
		if burstForwardRefused(gerr) {
			s.refuseForward(ctx, w, fwd, now, "burst: node refused a forward binding, requeued", gerr)
			return false, false
		}
		if burstForwardUnreachable(gerr) {
			s.noteNodeUnreachable(now, row)
		} else {
			slog.Warn("burst: node binding check failed, kept sending", "owner", string(w.id),
				"binding", w.b.Name, "node", fwd.Node, "err", gerr)
		}
		return false, false
	}
	req := remote.CreateBindingRequest{
		Name: fwd.NodeBinding, RepoID: ship.repoID, BaseCommit: ship.outSHA,
		Candidate: w.b.BuilderCandidate, Tier: ship.tier, Role: w.b.Role,
	}
	_, cerr := cl.CreateBinding(ctx, row.ID, req)
	if cerr == nil {
		return true, true
	}
	if burstHTTPStatus(cerr, http.StatusConflict) {
		// The binding was not this call's doing: another send, or an older
		// round the node still knows it by.
		return false, true
	}
	if burstForwardRefused(cerr) {
		s.refuseForward(ctx, w, fwd, now, "burst: node refused a forward binding, requeued", cerr)
		return false, false
	}
	if burstForwardUnreachable(cerr) {
		s.noteNodeUnreachable(now, row)
	} else {
		slog.Warn("burst: forward binding create failed, kept sending", "owner", string(w.id),
			"binding", w.b.Name, "node", fwd.Node, "err", cerr)
	}
	return false, false
}

// confirmSend marks a sent forward running after the node's yes. The
// binding is re-read and the mark lands only while the forward still
// matches, so a result for a superseded claim is dropped. The table learns
// the send after the binding does, each under its own lock: no lock spans
// both, so a report merging between them still wins the row guard.
//
// created says whether this send made the node binding. The entry it writes
// is the round's start evidence, so a retry that found the binding standing
// after an earlier call created it must not read as warm: the forward that
// created the binding stays cold until some send to this node was confirmed.
func (s *Server) confirmSend(w sendWork, row burst.NodeRow, outSHA string, now time.Time, created bool) bool {
	fwd := *w.b.Burst.Forward
	st := s.ownerStore(w.ownerPath)
	b, err := st.Load(w.b.Name)
	if err != nil {
		slog.Warn("burst: confirm forward: load failed", "binding", w.b.Name, "err", err)
		return false
	}
	have := burstStoredForward(b)
	if have == nil || have.Phase != "sending" || have.Node != fwd.Node || b.Round != fwd.Round {
		return false
	}
	cold := created || b.Burst.Shipped[have.Node] == ""
	have.Phase = "running"
	have.SentAt = now
	if b.Burst.Shipped == nil {
		b.Burst.Shipped = map[string]string{}
	}
	b.Burst.Shipped[have.Node] = outSHA
	b.Burst.NodeBindings = append(b.Burst.NodeBindings, have.NodeBinding)
	if err := st.Save(b); err != nil {
		slog.Warn("burst: confirm forward: save failed", "binding", w.b.Name, "err", err)
		return false
	}
	word := "warm"
	if cold {
		word = "cold"
	}
	rt := s.runtimeAt(w.ownerPath)
	if err := rt.Store.AppendLog(w.b.Name, store.LogEntry{
		TS: now, Round: have.Round, Direction: store.DirToMasterMind,
		Kind: store.KindQueue, Confirmed: true,
		Note: store.ForwardedNotePrefix + have.Node + " (" + word + ")",
	}); err != nil {
		// A note that will not land never fails the send: the round is on
		// the node whatever the head log says, and a dropped fact reads as
		// facts missing, not as a send that did not happen.
		slog.Warn("burst: confirm forward: send note not appended", "binding", w.b.Name,
			"round", have.Round, "node", have.Node, "err", err)
	}
	s.noteForwardSent(row, now)
	s.clearNodeProblem(row)
	return true
}

// burstHTTPStatus reports whether err is an HTTP answer with the given
// status. Anything else -- a dial failure, a timeout, a local error -- is
// not an answer at all.
func burstHTTPStatus(err error, status int) bool {
	var herr *client.HTTPError
	return errors.As(err, &herr) && herr.Status == status
}

// burstForwardRefused reports whether err is a definite refusal: a 4xx that
// is not a confirm, so the node will not take the round and it waits
// locally again. Anything else stays sending for the next tick.
func burstForwardRefused(err error) bool {
	var herr *client.HTTPError
	if !errors.As(err, &herr) {
		return false
	}
	return herr.Status >= 400 && herr.Status < 500 && herr.Status != http.StatusConflict
}

// burstForwardUnreachable reports whether err is a dial failure, a timeout
// or a server-side failure: the node may still take the round, so the
// forward stays sending and the row carries unreachable until it answers.
func burstForwardUnreachable(err error) bool {
	if errors.Is(err, client.ErrUnreachable) {
		return true
	}
	var herr *client.HTTPError
	return errors.As(err, &herr) && herr.Status >= 500
}

// updateBurstRow applies one mark to the row under s.mu. The mark lands
// only while the row still carries the checked URL and fingerprint, so a
// report that merged mid-pass wins; the whole table saves after, the way
// the verify pass saves its own.
func (s *Server) updateBurstRow(row burst.NodeRow, apply func(*burst.NodeRow)) {
	s.mu.Lock()
	t := s.burstTable
	if t == nil {
		s.mu.Unlock()
		return
	}
	if r := burstTableRow(t, row.ID); r != nil && r.URL == row.URL && r.Fingerprint == row.Fingerprint {
		apply(r)
	}
	cp := *t
	cp.Nodes = append([]burst.NodeRow(nil), t.Nodes...)
	s.mu.Unlock()
	if err := saveBurstTable(s.cfg.DB.LocalOrSelf(), &cp); err != nil {
		slog.Warn("burst: node table not saved", "err", err)
	}
}

// noteNodeUnreachable records a dial, timeout or server failure on the row.
// ProblemSince is stamped only on the first failure of the episode: a
// standing outage keeps the time it started, which is what the lost-node
// bound measures from.
func (s *Server) noteNodeUnreachable(now time.Time, row burst.NodeRow) {
	s.updateBurstRow(row, func(r *burst.NodeRow) {
		if r.Problem != burst.ReportProblemUnreachable {
			r.Problem = burst.ReportProblemUnreachable
			r.ProblemSince = now
		}
	})
}

// clearNodeProblem clears the row's problem after a success: any good
// answer proves the node reachable again, whatever the last failure was.
func (s *Server) clearNodeProblem(row burst.NodeRow) {
	s.updateBurstRow(row, func(r *burst.NodeRow) {
		r.Problem = ""
		r.ProblemSince = time.Time{}
	})
}

// noteForwardSent stamps the row's forwarded mark after the node took the
// round, so the next report carries when each node last received one.
func (s *Server) noteForwardSent(row burst.NodeRow, now time.Time) {
	at := now
	s.updateBurstRow(row, func(r *burst.NodeRow) {
		r.ForwardedAt = &at
	})
}

// refuseForward returns a round a node refused to the queue and holds it out
// of placement for a while, so the next tick does not claim it for the same
// refusal again.
func (s *Server) refuseForward(ctx context.Context, w sendWork, fwd store.Forward, now time.Time, msg string, err error) {
	slog.Warn(msg, "owner", string(w.id), "binding", w.b.Name, "node", fwd.Node, "err", err)
	since := fwd.QueuedSince
	if since.IsZero() {
		since = now
	}
	s.burstHealth.noteRefused(burstRefusalKey(string(w.id)+"/"+w.b.Name, since), now)
	s.requeueOne(ctx, w.id, w.ownerPath, w.b.Name, fwd, now)
}

// withoutHeldRefusals drops from placement every waiting round a node
// refused inside the hold.
func (s *Server) withoutHeldRefusals(waiting []burst.ReportWaiting, now time.Time) []burst.ReportWaiting {
	out := waiting[:0:0]
	for _, w := range waiting {
		if !s.burstHealth.refusalHeld(burstRefusalKey(w.ID, w.Since), now) {
			out = append(out, w)
		}
	}
	return out
}
