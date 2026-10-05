package relevo

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
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/store"
)

// remoteShipped is what remoteShip produced: the server's answer and the two
// facts the record half needs. The snapshot's body has already been closed, so
// only the heads survive.
type remoteShipped struct {
	view   remote.BindingView
	outRef string
	heads  map[string]string
}

// remoteShip is sendRemote's network half, unlocked: one WhoAmI answers every
// question about the server, the out ref moves, the bundle and tags are
// snapshotted, and one StartRound ships the round with verify off. It writes
// nothing local, so a caller may run it outside the state lock; the chain
// composes it with its own chain-aware record half.
func remoteShip(ctx context.Context, rt Runtime, b store.Binding, planBody []byte, tier, builder string, force bool, verify *bool) (remoteShipped, error) {
	if rt.Remote == nil {
		return remoteShipped{}, ErrRemoteUnavailable
	}
	if rt.Git == nil {
		return remoteShipped{}, ErrGitRequired
	}
	if rt.Transport == nil {
		return remoteShipped{}, errors.New("no remote transport configured")
	}

	server := b.Builder.Server

	// The cap check already ran in Send; do not repeat it here. One WhoAmI
	// answers everything this function asks the server about itself: whether a
	// requested tier or builder is supported, each of which needs a server that
	// advertises it, and whether a repeated send is safe to retry (#373 §4.4).
	who, err := rt.Remote.WhoAmI(ctx, server)
	if err != nil {
		return remoteShipped{}, err
	}
	if tier != "" && !slices.Contains(who.Features, remote.FeatureTier) {
		return remoteShipped{}, fmt.Errorf("%w: server %s does not carry a permission tier (pre-tier server); upgrade it or drop --tier", ErrServerPreTier, server)
	}
	if builder != "" && !slices.Contains(who.Features, remote.FeatureBuilder) {
		return remoteShipped{}, fmt.Errorf("server %s cannot change a binding's candidate (no %q feature); upgrade it, or send without --candidate", server, remote.FeatureBuilder)
	}
	if force && !slices.Contains(who.Features, remote.FeatureForce) {
		return remoteShipped{}, fmt.Errorf("server %s does not carry --force (pre-force server); upgrade it, or send without --force", server)
	}

	name := b.Name

	// 1. rt.Git.UpdateRef(b.Repo, "refs/relevo/"+b.Name+"/out", RefSHA("refs/heads/"+b.Branch), "")
	branchRef := b.Branch
	if !strings.HasPrefix(branchRef, "refs/heads/") {
		branchRef = "refs/heads/" + branchRef
	}
	branchSHA, ok, err := rt.Git.RefSHA(ctx, b.Repo, branchRef)
	if err != nil {
		return remoteShipped{}, fmt.Errorf("resolve branch %s: %w", b.Branch, err)
	}
	if !ok {
		return remoteShipped{}, fmt.Errorf("branch %s not found", b.Branch)
	}
	outRef := "refs/relevo/" + name + "/out"
	if err := rt.Git.UpdateRef(ctx, b.Repo, outRef, branchSHA, ""); err != nil {
		return remoteShipped{}, fmt.Errorf("update ref %s: %w", outRef, err)
	}

	// 2. snap := rt.Transport.Snapshot(b.Repo, ["refs/relevo/<name>/out"], b.Builder.LastShipped)
	// ErrSinceUnknown -> treat as first send: Snapshot with since ""
	snap, err := rt.Transport.Snapshot(ctx, b.Repo, []string{outRef}, b.Builder.LastShipped)
	if err != nil {
		if errors.Is(err, remote.ErrSinceUnknown) {
			snap, err = rt.Transport.Snapshot(ctx, b.Repo, []string{outRef}, "")
		}
		if err != nil {
			return remoteShipped{}, fmt.Errorf("snapshot %s: %w", outRef, err)
		}
	}
	heads := snap.Heads
	defer func() {
		if snap.Body != nil {
			_ = snap.Body.Close()
		}
	}()

	var bundleReader io.Reader
	if !snap.Empty && snap.Body != nil {
		bundleReader = snap.Body
	}

	// 2b. The client's tags travel as data beside the bundle (#242): a tag on
	// an ancestor of the shipped branch already has its commit on the server.
	// A broken repo is a real pre-send failure; no local state is written.
	tagsMap, err := rt.Git.ListTags(ctx, b.Repo)
	if err != nil {
		return remoteShipped{}, fmt.Errorf("list tags: %w", err)
	}
	tags := make([]remote.TagRef, 0, len(tagsMap))
	for tagName, sha := range tagsMap {
		tags = append(tags, remote.TagRef{Name: tagName, SHA: sha})
	}
	sort.Slice(tags, func(i, j int) bool { return tags[i].Name < tags[j].Name })

	// 3. view, err := rt.Remote.StartRound(ctx, server, name, b.Round, planBody, snap.Body or nil when Empty, tier, builder, tags)
	// The retry is safe only on a server that dedupes a repeated send (#373
	// §4.5): without idempotent_send a retry could re-queue a round that did
	// start, so a gateway error is reported as unreachable instead.
	view, err := rt.Remote.StartRound(ctx, server, name, b.Round, planBody, bundleReader, tier, builder, force, tags,
		slices.Contains(who.Features, remote.FeatureIdempotentSend), verify)
	if err != nil {
		var httpErr *client.HTTPError
		if errors.As(err, &httpErr) {
			if httpErr.Status == 409 && httpErr.Body.Code == remote.CodeRoundStarted {
				// proceed as success
				view.RoundState = remote.RoundRunning
			} else if httpErr.Status == 409 && httpErr.Body.Code == remote.CodeRoundOpen {
				// round_open is the server's refusal to start the round as
				// asked: a running or queued round, or a binding that cannot
				// start at all. Its message says which, so it is reported as
				// given rather than restated as "running".
				return remoteShipped{}, fmt.Errorf("%s: round %d could not start on %s: %s", name, b.Round, server, httpErr.Body.Message)
			} else if httpErr.Status == 409 && httpErr.Body.Code == remote.CodeRoundHalted {
				return remoteShipped{}, fmt.Errorf("%s: round %d could not start on %s: %s", name, b.Round, server, httpErr.Body.Message)
			} else if httpErr.Status == 422 && httpErr.Body.Code == remote.CodeTierAboveMax {
				return remoteShipped{}, fmt.Errorf("%w: %s", ErrTierAboveMax, httpErr.Body.Message)
			} else if httpErr.Status == 422 {
				return remoteShipped{}, fmt.Errorf("%s: %s", server, httpErr.Body.Message)
			} else {
				return remoteShipped{}, fmt.Errorf("%s: %s", server, httpErr.Error())
			}
		} else if errors.Is(err, client.ErrUnreachable) {
			cause := strings.TrimPrefix(err.Error(), client.ErrUnreachable.Error()+": ")
			return remoteShipped{}, fmt.Errorf("%s unreachable: %s", server, cause)
		} else {
			return remoteShipped{}, err
		}
	}

	// A server built before this plan may still answer 201 with a
	// needs_you view for a round that could not start, rather than the 409
	// round_halted handled above; treat it the same way. Nothing is
	// written: the human's re-send must not look like it succeeded.
	if view.RoundState == remote.RoundNeedsYou {
		return remoteShipped{}, fmt.Errorf("%s: round %d could not start on %s: %s", name, b.Round, server, orText(view.Halt, "no reason given"))
	}

	return remoteShipped{view: view, outRef: outRef, heads: heads}, nil
}

// remoteRecord is sendRemote's under-lock tail: it reloads the binding, checks
// the round did not advance, stages the plan, files the prompt entry and writes
// the round's state. chain makes it the chain's own record half: no
// running-chain refusal (the chain owns the member), a chain-step note on the
// prompt entry, and no --candidate change (the chain never re-points a remote
// member's candidate).
func remoteRecord(rt Runtime, tx *store.Tx, b store.Binding, ship remoteShipped, planBody []byte, builder string, chain bool) (int, string, error) {
	name := b.Name
	server := b.Builder.Server
	view := ship.view

	cur, err := tx.Load(name)
	if err != nil {
		return 0, "", err
	}
	// Re-check under the lock: the chain may have started between the
	// preflight and here.
	if !chain {
		if err := refuseRunningChainMember(tx, name); err != nil {
			return 0, "", err
		}
	}
	if cur.Round != b.Round {
		return 0, "", errors.New("round advanced during send; run relevo status")
	}
	sendRound := cur.Round

	now := rt.Now()
	pickLine := ""

	// A --candidate change is applied by the server (the served path's own
	// §5.2 write). The client records the candidate the server
	// canonicalised, so the next observeRemote sees view.Candidate ==
	// b.BuilderCandidate and writes no spurious "switched on <server>".
	// The pick entry is filed under the sent round, before its plan entry.
	if !chain && builder != "" && view.Candidate != "" && view.Candidate != cur.BuilderCandidate {
		pick := remotePickEntry(now, server, view.Candidate, true, cur.Round, PlacementResolution{})
		if err := tx.AppendLog(name, pick); err != nil {
			return 0, "", fmt.Errorf("append builder pick log: %w", err)
		}
		pickLine = pick.Note
		cur.BuilderCandidate = view.Candidate
		kind := ""
		if ref, err := candidate.ParseRef(view.Candidate); err == nil {
			kind = ref.Harness
		}
		cur.Builder.Kind = kind
	}

	planPath := rt.Store.PromptPath(name, cur.Round)
	if err := stagePlan(planPath, planBody); err != nil {
		return 0, "", fmt.Errorf("write plan %s: %w", planPath, err)
	}

	note := ""
	if chain {
		note = chainStepNote(tx, name)
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

	// The send half resets the round the way Send's local branch does, field for
	// field. A remote send that starts (or restarts) a round is the same fresh
	// attempt a local one is, so the client must not keep the state a halt left
	// behind: state active, halt and halt-at cleared, and the per-round
	// bookkeeping that described the previous process cleared with it.
	cur.RoundStartedAt = now
	cur.QueuedAt = time.Time{}
	cur.FinishPending = true
	cur.State = store.StateActive
	cur.Halt = ""
	cur.HaltAt = time.Time{}
	// A human re-send is a fresh attempt: the next halt in this round notifies
	// again, and the round gets a full switch budget.
	cur.HaltNotifiedRound = 0
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
	if ship.heads != nil {
		cur.Builder.LastShipped = ship.heads[ship.outRef]
	}
	cur.Builder.RemoteStatus = string(view.RoundState)
	return sendRound, pickLine, tx.Save(cur)
}

func sendRemote(ctx context.Context, rt Runtime, b store.Binding, planBody []byte, tier, builder string, force bool) (SendResult, error) {
	ship, err := remoteShip(ctx, rt, b, planBody, tier, builder, force, nil)
	if err != nil {
		return SendResult{}, err
	}
	var sendRound int
	var pickLine string
	err = rt.Store.WithLock(func(tx *store.Tx) error {
		r, p, rerr := remoteRecord(rt, tx, b, ship, planBody, builder, false)
		sendRound, pickLine = r, p
		return rerr
	})
	if err != nil {
		return SendResult{}, err
	}
	return SendResult{Round: sendRound, Pick: pickLine}, nil
}
