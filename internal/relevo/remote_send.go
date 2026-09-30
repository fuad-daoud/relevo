package relevo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/store"
)

func sendRemote(ctx context.Context, rt Runtime, b store.Binding, planBody []byte, tier, builder string, force bool) (SendResult, error) {
	if rt.Remote == nil {
		return SendResult{}, ErrRemoteUnavailable
	}
	if rt.Git == nil {
		return SendResult{}, ErrGitRequired
	}
	if rt.Transport == nil {
		return SendResult{}, errors.New("no remote transport configured")
	}

	server := b.Builder.Server

	// The cap check already ran in Send; do not repeat it here. One WhoAmI
	// answers everything this function asks the server about itself: whether a
	// requested tier or builder is supported, each of which needs a server that
	// advertises it, and whether a repeated send is safe to retry (#373 §4.4).
	who, err := rt.Remote.WhoAmI(ctx, server)
	if err != nil {
		return SendResult{}, err
	}
	if tier != "" && !slices.Contains(who.Features, remote.FeatureTier) {
		return SendResult{}, fmt.Errorf("%w: server %s does not carry a permission tier (pre-tier server); upgrade it or drop --tier", ErrServerPreTier, server)
	}
	if builder != "" && !slices.Contains(who.Features, remote.FeatureBuilder) {
		return SendResult{}, fmt.Errorf("server %s cannot change a binding's candidate (no %q feature); upgrade it, or send without --candidate", server, remote.FeatureBuilder)
	}
	if force && !slices.Contains(who.Features, remote.FeatureForce) {
		return SendResult{}, fmt.Errorf("server %s does not carry --force (pre-force server); upgrade it, or send without --force", server)
	}

	name := b.Name

	// 1. rt.Git.UpdateRef(b.Repo, "refs/relevo/"+b.Name+"/out", RefSHA("refs/heads/"+b.Branch), "")
	branchRef := b.Branch
	if !strings.HasPrefix(branchRef, "refs/heads/") {
		branchRef = "refs/heads/" + branchRef
	}
	branchSHA, ok, err := rt.Git.RefSHA(ctx, b.Repo, branchRef)
	if err != nil {
		return SendResult{}, fmt.Errorf("resolve branch %s: %w", b.Branch, err)
	}
	if !ok {
		return SendResult{}, fmt.Errorf("branch %s not found", b.Branch)
	}
	outRef := "refs/relevo/" + name + "/out"
	if err := rt.Git.UpdateRef(ctx, b.Repo, outRef, branchSHA, ""); err != nil {
		return SendResult{}, fmt.Errorf("update ref %s: %w", outRef, err)
	}

	// 2. snap := rt.Transport.Snapshot(b.Repo, ["refs/relevo/<name>/out"], b.Builder.LastShipped)
	// ErrSinceUnknown -> treat as first send: Snapshot with since ""
	snap, err := rt.Transport.Snapshot(ctx, b.Repo, []string{outRef}, b.Builder.LastShipped)
	if err != nil {
		if errors.Is(err, remote.ErrSinceUnknown) {
			snap, err = rt.Transport.Snapshot(ctx, b.Repo, []string{outRef}, "")
		}
		if err != nil {
			return SendResult{}, fmt.Errorf("snapshot %s: %w", outRef, err)
		}
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

	// 2b. The client's tags travel as data beside the bundle (#242): a tag on
	// an ancestor of the shipped branch already has its commit on the server.
	// A broken repo is a real pre-send failure; no local state is written.
	tagsMap, err := rt.Git.ListTags(ctx, b.Repo)
	if err != nil {
		return SendResult{}, fmt.Errorf("list tags: %w", err)
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
		slices.Contains(who.Features, remote.FeatureIdempotentSend))
	if err != nil {
		var httpErr *client.HTTPError
		if errors.As(err, &httpErr) {
			if httpErr.Status == 409 && httpErr.Body.Code == remote.CodeRoundStarted {
				// proceed as success
				view.RoundState = remote.RoundRunning
			} else if httpErr.Status == 409 && httpErr.Body.Code == remote.CodeRoundOpen {
				return SendResult{}, fmt.Errorf("round %d is running on %s", b.Round, server)
			} else if httpErr.Status == 409 && httpErr.Body.Code == remote.CodeRoundHalted {
				return SendResult{}, fmt.Errorf("%s: round %d could not start on %s: %s", name, b.Round, server, httpErr.Body.Message)
			} else if httpErr.Status == 422 && httpErr.Body.Code == remote.CodeTierAboveMax {
				return SendResult{}, fmt.Errorf("%w: %s", ErrTierAboveMax, httpErr.Body.Message)
			} else if httpErr.Status == 422 {
				return SendResult{}, fmt.Errorf("%s: %s", server, httpErr.Body.Message)
			} else {
				return SendResult{}, fmt.Errorf("%s: %s", server, httpErr.Error())
			}
		} else if errors.Is(err, client.ErrUnreachable) {
			cause := strings.TrimPrefix(err.Error(), client.ErrUnreachable.Error()+": ")
			return SendResult{}, fmt.Errorf("%s unreachable: %s", server, cause)
		} else {
			return SendResult{}, err
		}
	}

	// A server built before this plan may still answer 201 with a
	// needs_you view for a round that could not start, rather than the 409
	// round_halted handled above; treat it the same way. Nothing is
	// written: the human's re-send must not look like it succeeded.
	if view.RoundState == remote.RoundNeedsYou {
		return SendResult{}, fmt.Errorf("%s: round %d could not start on %s: %s", name, b.Round, server, orText(view.Halt, "no reason given"))
	}

	// UNDER the lock:
	// 4. reload b; if b.Round != the round sent -> error "round advanced during send; run relevo status" (nothing recorded)
	// 5. write PromptPath(name, round) = planBody; AppendLog plan to_builder; b.RoundStartedAt = now;
	// b.State = active; b.Halt = ""; b.Builder.LastShipped = snap.Heads["refs/relevo/<name>/out"];
	// b.Builder.RemoteStatus = string(view.RoundState); Save.
	var sendRound int
	var pickLine string
	err = rt.Store.WithLock(func(tx *store.Tx) error {
		cur, err := tx.Load(name)
		if err != nil {
			return err
		}
		if cur.Round != b.Round {
			return errors.New("round advanced during send; run relevo status")
		}
		sendRound = cur.Round

		now := rt.Now()

		// A --candidate change is applied by the server (the served path's own
		// §5.2 write). The client records the candidate the server
		// canonicalised, so the next observeRemote sees view.Candidate ==
		// b.BuilderCandidate and writes no spurious "switched on <server>".
		// The pick entry is filed under the sent round, before its plan entry.
		if builder != "" && view.Candidate != "" && view.Candidate != cur.BuilderCandidate {
			pick := remotePickEntry(now, server, view.Candidate, true, cur.Round, PlacementResolution{})
			if err := tx.AppendLog(name, pick); err != nil {
				return fmt.Errorf("append builder pick log: %w", err)
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
			return fmt.Errorf("write plan %s: %w", planPath, err)
		}

		if err := tx.AppendLog(name, store.LogEntry{
			TS:        now.UTC(),
			Round:     cur.Round,
			Direction: store.DirToBuilder,
			Kind:      store.KindPrompt,
			Path:      planPath,
			Confirmed: true,
		}); err != nil {
			return fmt.Errorf("append plan log: %w", err)
		}

		cur.RoundStartedAt = now
		cur.State = store.StateActive
		cur.Halt = ""
		if snap.Heads != nil {
			cur.Builder.LastShipped = snap.Heads[outRef]
		}
		cur.Builder.RemoteStatus = string(view.RoundState)
		return tx.Save(cur)
	})
	if err != nil {
		return SendResult{}, err
	}
	return SendResult{Round: sendRound, Pick: pickLine}, nil
}
