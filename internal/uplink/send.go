// Package uplink sends rounds to a remote server: the client's network half,
// above the engine. The engine reaches it only through the RemoteSend hook.
package uplink

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/resolve"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/text"
)

// Ship is SendRemote's network half, unlocked: one WhoAmI answers every
// question about the server, the out ref moves, the bundle and tags are
// snapshotted, and one StartRound ships the round with verify off. It writes
// nothing local, so a caller may run it outside the state lock; the chain
// composes it with its own chain-aware record half.
func Ship(ctx context.Context, rt relevo.Runtime, b store.Binding, planBody []byte, tier, builder string, force bool, verify *bool) (remote.BindingView, string, map[string]string, error) {
	server, who, err := shipServer(ctx, rt, b, tier, builder, force)
	if err != nil {
		return remote.BindingView{}, "", nil, err
	}
	name := b.Name
	outRef, err := shipOutRef(ctx, rt, b, name)
	if err != nil {
		return remote.BindingView{}, "", nil, err
	}
	snap, err := shipSnapshot(ctx, rt, b, outRef)
	if err != nil {
		return remote.BindingView{}, "", nil, err
	}
	defer func() {
		if snap.Body != nil {
			_ = snap.Body.Close()
		}
	}()
	var bundleReader io.Reader
	if !snap.Empty && snap.Body != nil {
		bundleReader = snap.Body
	}
	tags, err := shipTags(ctx, rt, b)
	if err != nil {
		return remote.BindingView{}, "", nil, err
	}
	view, err := startRound(ctx, rt, server, name, b.Round, planBody, bundleReader, tier, builder, force, tags, who, verify)
	if err != nil {
		return remote.BindingView{}, "", nil, err
	}
	// A needs_you or broken view answers a round that could not start with a 200;
	// treat it the same way: nothing is written, so the re-send does not look like
	// it succeeded, and no prompt is recorded, which would reset the round and file
	// the next halt twice with the same text.
	if view.RoundState == remote.RoundNeedsYou || view.RoundState == remote.RoundBroken {
		return remote.BindingView{}, "", nil, fmt.Errorf("%s: round %d could not start on %s: %s", name, b.Round, server, text.OrText(view.Halt, "no reason given"))
	}
	return view, outRef, snap.Heads, nil
}

// shipServer checks the three clients a ship needs and asks the server one
// WhoAmI that answers every question the ship asks about it, refusing a
// requested tier, candidate change or force the server does not advertise.
func shipServer(ctx context.Context, rt relevo.Runtime, b store.Binding, tier, builder string, force bool) (string, remote.WhoAmI, error) {
	if rt.Remote == nil {
		return "", remote.WhoAmI{}, relevo.ErrRemoteUnavailable
	}
	if rt.Git == nil {
		return "", remote.WhoAmI{}, relevo.ErrGitRequired
	}
	if rt.Transport == nil {
		return "", remote.WhoAmI{}, errors.New("no remote transport configured")
	}
	server := b.Builder.Server
	// The cap check already ran in Send; do not repeat it here. One WhoAmI
	// answers everything this function asks the server about itself: whether a
	// requested tier or builder is supported, each of which needs a server that
	// advertises it, and whether a repeated send is safe to retry.
	who, err := rt.Remote.WhoAmI(ctx, server)
	if err != nil {
		return "", remote.WhoAmI{}, err
	}
	if tier != "" && !slices.Contains(who.Features, remote.FeatureTier) {
		return "", remote.WhoAmI{}, fmt.Errorf("%w: server %s does not carry a permission tier (pre-tier server); upgrade it or drop --tier", relevo.ErrServerPreTier, server)
	}
	if builder != "" && !slices.Contains(who.Features, remote.FeatureBuilder) {
		return "", remote.WhoAmI{}, fmt.Errorf("server %s cannot change a binding's candidate (no %q feature); upgrade it, or send without --candidate", server, remote.FeatureBuilder)
	}
	if force && !slices.Contains(who.Features, remote.FeatureForce) {
		return "", remote.WhoAmI{}, fmt.Errorf("server %s does not carry --force (pre-force server); upgrade it, or send without --force", server)
	}
	return server, who, nil
}

// shipOutRef rolls the branch head into the binding's out ref, the ref the
// bundle is cut from and the server's next round starts at.
func shipOutRef(ctx context.Context, rt relevo.Runtime, b store.Binding, name string) (string, error) {
	branchRef := b.Branch
	if !strings.HasPrefix(branchRef, "refs/heads/") {
		branchRef = "refs/heads/" + branchRef
	}
	branchSHA, ok, err := rt.Git.RefSHA(ctx, b.Repo, branchRef)
	if err != nil {
		return "", fmt.Errorf("resolve branch %s: %w", b.Branch, err)
	}
	if !ok {
		return "", fmt.Errorf("branch %s not found", b.Branch)
	}
	outRef := "refs/relevo/" + name + "/out"
	if err := rt.Git.UpdateRef(ctx, b.Repo, outRef, branchSHA, ""); err != nil {
		return "", fmt.Errorf("update ref %s: %w", outRef, err)
	}
	return outRef, nil
}

// shipSnapshot bundles the out ref relative to the last shipped commit: the
// whole bundle on a first send, a delta after. An unknown since means a
// server that forgot the base, so the send falls back to the full bundle,
// and one blown per-command budget earns one retry, never a loop.
func shipSnapshot(ctx context.Context, rt relevo.Runtime, b store.Binding, outRef string) (remote.Snapshot, error) {
	snap, err := rt.Transport.Snapshot(ctx, b.Repo, []string{outRef}, b.Builder.LastShipped)
	if err != nil {
		if errors.Is(err, remote.ErrSinceUnknown) {
			snap, err = rt.Transport.Snapshot(ctx, b.Repo, []string{outRef}, "")
		}
		// A blown per-command budget on `git bundle create` is a transient
		// miss, not a verdict: on a large or hot repo the bundle can outrun
		// the 10s every git op shares. One retry is safe here -- no round has
		// been started, a failed Snapshot removes its temp bundle, and the
		// UpdateRef above is idempotent -- and exactly one, so a genuinely
		// slow bundle still fails with the same message instead of spinning.
		//
		// If repeats arrive, the fix belongs one layer down: a dedicated
		// longer-timeout git client for the bundle transport, not a wider
		// retry here.
		if errors.Is(err, context.DeadlineExceeded) {
			snap, err = rt.Transport.Snapshot(ctx, b.Repo, []string{outRef}, b.Builder.LastShipped)
		}
		if err != nil {
			return remote.Snapshot{}, fmt.Errorf("snapshot %s: %w", outRef, err)
		}
	}
	return snap, nil
}

// shipTags reads the client's tags, which travel beside the bundle as data: a
// tag on an ancestor of the shipped branch already has its commit on the
// server. A broken repo is a real pre-send failure; no local state is written.
func shipTags(ctx context.Context, rt relevo.Runtime, b store.Binding) ([]remote.TagRef, error) {
	tagsMap, err := rt.Git.ListTags(ctx, b.Repo)
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	tags := make([]remote.TagRef, 0, len(tagsMap))
	for tagName, sha := range tagsMap {
		tags = append(tags, remote.TagRef{Name: tagName, SHA: sha})
	}
	sort.Slice(tags, func(i, j int) bool { return tags[i].Name < tags[j].Name })
	return tags, nil
}

// startRound starts the round on the server and maps a refusal to the error
// the client reports: the already-started answer reads as success, every
// other answer as the refusal it is.
func startRound(ctx context.Context, rt relevo.Runtime, server, name string, round int, planBody []byte, bundle io.Reader, tier, builder string, force bool, tags []remote.TagRef, who remote.WhoAmI, verify *bool) (remote.BindingView, error) {
	// The retry is safe only on a server that dedupes a repeated send:
	// without idempotent_send a retry could re-queue a round that did start,
	// so a gateway error is reported as unreachable instead.
	view, err := rt.Remote.StartRound(ctx, server, name, round, planBody, bundle, tier, builder, force, tags,
		slices.Contains(who.Features, remote.FeatureIdempotentSend), verify)
	if err == nil {
		return view, nil
	}
	var httpErr *client.HTTPError
	if errors.As(err, &httpErr) {
		if httpErr.Status == 409 && httpErr.Body.Code == remote.CodeRoundStarted {
			// proceed as success
			view.RoundState = remote.RoundRunning
			return view, nil
		}
		return remote.BindingView{}, startRoundRefusal(name, server, round, httpErr)
	}
	if errors.Is(err, client.ErrUnreachable) {
		cause := strings.TrimPrefix(err.Error(), client.ErrUnreachable.Error()+": ")
		return remote.BindingView{}, fmt.Errorf("%s unreachable: %s", server, cause)
	}
	return remote.BindingView{}, err
}

// startRoundRefusal reports the server's refusal as given: a round that is
// running or queued, or a binding that cannot start at all, each names
// itself in the message rather than restating as "running".
func startRoundRefusal(name, server string, round int, httpErr *client.HTTPError) error {
	switch {
	case httpErr.Status == 409 && httpErr.Body.Code == remote.CodeRoundOpen:
		return fmt.Errorf("%s: round %d could not start on %s: %s", name, round, server, httpErr.Body.Message)
	case httpErr.Status == 409 && httpErr.Body.Code == remote.CodeRoundHalted:
		return fmt.Errorf("%s: round %d could not start on %s: %s", name, round, server, httpErr.Body.Message)
	case httpErr.Status == 422 && httpErr.Body.Code == remote.CodeTierAboveMax:
		return fmt.Errorf("%w: %s", resolve.ErrTierAboveMax, httpErr.Body.Message)
	case httpErr.Status == 422 && httpErr.Body.Code == remote.CodeInvalid && resolve.TierUnsupportedMessage(httpErr.Body.Message):
		return resolve.TierUnsupportedFromWire(httpErr.Body.Message)
	case httpErr.Status == 422:
		return fmt.Errorf("%s: %s", server, httpErr.Body.Message)
	default:
		return fmt.Errorf("%s: %s", server, httpErr.Error())
	}
}

// Record is SendRemote's under-lock tail: it reloads the binding, checks
// the round did not advance, stages the plan, files the prompt entry and writes
// the round's state. The chain uses it as its own record half: no
// running-chain refusal (the chain owns the member), a chain-step note on the
// prompt entry, and no --candidate change (the chain never re-points a remote
// member's candidate).
func Record(rt relevo.Runtime, tx *store.Tx, b store.Binding, view remote.BindingView, outRef string, heads map[string]string, planBody []byte, builder string, isChain bool) (int, string, error) {
	name := b.Name

	cur, err := tx.Load(name)
	if err != nil {
		return 0, "", err
	}
	// Re-check under the lock: the chain may have started between the
	// preflight and here.
	if !isChain {
		if err := chain.RefuseRunningMember(tx, name); err != nil {
			return 0, "", err
		}
	}
	if cur.Round != b.Round {
		return 0, "", errors.New("round advanced during send; run relevo status")
	}
	sendRound := cur.Round

	now := rt.Now()
	pickLine, err := recordBuilderPick(tx, &cur, view, b.Builder.Server, builder, now)
	if err != nil {
		return 0, "", err
	}

	planPath := rt.Store.PromptPath(name, cur.Round)
	if err := relevo.StagePlan(planPath, planBody); err != nil {
		return 0, "", fmt.Errorf("write plan %s: %w", planPath, err)
	}

	note := ""
	if isChain {
		note = chain.StepNote(tx, name)
	}
	if err := tx.AppendLog(name, store.LogEntry{
		TS:        now.UTC(),
		Round:     cur.Round,
		Direction: store.DirToBuilder,
		Kind:      store.KindPrompt,
		Path:      planPath,
		Confirmed: true,
		Note:      note,
	}); err != nil {
		return 0, "", fmt.Errorf("append plan log: %w", err)
	}

	resetResendRound(&cur, view, heads, outRef, now)
	return sendRound, pickLine, tx.Save(cur)
}

// recordBuilderPick files the pick entry for a --candidate change, which the
// server applies and the client records as the server canonicalised it, so
// the next observation sees view.Candidate already recorded and writes no
// spurious switch. The pick entry is filed under the sent round, before its
// plan entry.
func recordBuilderPick(tx *store.Tx, cur *store.Binding, view remote.BindingView, server, builder string, now time.Time) (string, error) {
	if builder == "" || view.Candidate == "" || view.Candidate == cur.BuilderCandidate {
		return "", nil
	}
	pick := relevo.RemotePickEntry(now, server, view.Candidate, true, cur.Round, resolve.PlacementResolution{})
	if err := tx.AppendLog(cur.Name, pick); err != nil {
		return "", fmt.Errorf("append builder pick log: %w", err)
	}
	cur.BuilderCandidate = view.Candidate
	kind := ""
	if ref, err := candidate.ParseRef(view.Candidate); err == nil {
		kind = ref.Harness
	}
	cur.Builder.Kind = kind
	return pick.Note, nil
}

// resetResendRound resets the round the way Send's local branch does, field for
// field. A remote send that starts (or restarts) a round is the same fresh
// attempt a local one is, so the client must not keep the state a halt left
// behind: state active, halt and halt-at cleared, and the per-round
// bookkeeping that described the previous process cleared with it.
func resetResendRound(cur *store.Binding, view remote.BindingView, heads map[string]string, outRef string, now time.Time) {
	cur.RoundStartedAt = now
	cur.QueuedAt = time.Time{}
	cur.FinishPending = true
	cur.State = store.StateActive
	// The halt that stopped the previous attempt goes with the attempt, all of
	// it: reason, stamp and the kind naming its episode. A human re-send is a
	// fresh attempt, so the next halt in this round notifies again.
	*cur = relevo.ClearHaltFields(*cur)
	// An owed notification goes with the halt it was about: the re-send is the
	// human answering that halt, so the entry it still owes would land under a
	// round the human has already moved past.
	cur.OwedHalt = nil
	cur.RoundSwitches = 0
	cur.RoundExcluded = nil
	cur.RoundOOMKills = 0
	// A fresh send is a fresh process: the stall stamp, the progress clock and
	// the last land all describe the round that just ended.
	cur.StalledSince = time.Time{}
	cur.StopRequestedAt = time.Time{}
	cur.StopGraceMS = 0
	cur.LandedAt = time.Time{}
	cur.LandedPR = ""
	cur.Progress = nil
	cur.ExploringSince = time.Time{}
	cur.StaleSince = time.Time{}
	// A round that moves on leaves the last round's closed tree behind.
	cur.RoundClosedTree = ""
	if heads != nil {
		cur.Builder.LastShipped = heads[outRef]
	}
	cur.Builder.RemoteStatus = string(view.RoundState)
}

// SendRemote ships one round to a remote server: the network half outside any
// lock, then the under-lock tail that reloads the binding and writes the
// round's state.
func SendRemote(ctx context.Context, rt relevo.Runtime, b store.Binding, planBody []byte, tier, builder string, force bool) (relevo.SendResult, error) {
	view, outRef, heads, err := Ship(ctx, rt, b, planBody, tier, builder, force, nil)
	if err != nil {
		return relevo.SendResult{}, err
	}
	var sendRound int
	var pickLine string
	err = rt.Store.WithLock(func(tx *store.Tx) error {
		r, p, rerr := Record(rt, tx, b, view, outRef, heads, planBody, builder, false)
		sendRound, pickLine = r, p
		return rerr
	})
	if err != nil {
		return relevo.SendResult{}, err
	}
	return relevo.SendResult{Round: sendRound, Pick: pickLine}, nil
}
