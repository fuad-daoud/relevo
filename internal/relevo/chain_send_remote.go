package relevo

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/store"
)

// chainSendMember sends one round to a chain member, the one start-or-stage
// helper every send to a member goes through. A local member starts its round
// through sendChainRound, exactly as before; a remote member is staged -- its
// prompt file is written at PromptPath(member, member.Round) and the member is
// returned unchanged, with no prompt entry and no binding write. The unlocked
// chainSendPending step ships the staged file.
//
// The caller holds the state lock: a local start and the chain row that named
// it are written in the same critical section. A remote stage only writes the
// round's file; the step's own short lock does the recording.
func chainSendMember(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, text string) (store.Binding, error) {
	if b.Builder.Remote() {
		return chainStageRemote(ctx, rt, b, text)
	}
	return sendChainRound(ctx, rt, tx, b, text)
}

// chainStageRemote writes a remote member's staged round the one way every
// staging goes: the text lands at the member's own round path, RoundBaselineHead
// is the head of the branch the ship publishes as refs/relevo/<name>/out -- the
// commit the plan starts at and the server's next round is cut from -- and the
// previous round's closed tree is cleared, exactly the three writes a local
// sendChainRound makes for the same round. A missing or unreadable directory is
// the stage failure the resume's halt names.
func chainStageRemote(ctx context.Context, rt Runtime, b store.Binding, text string) (store.Binding, error) {
	path := rt.Store.PromptPath(b.Name, b.Round)
	if err := stagePlan(path, []byte(text)); err != nil {
		return b, fmt.Errorf("%s: stage plan at %s: %w", b.Name, path, err)
	}
	head, err := chainBranchHead(ctx, rt, b)
	if err != nil {
		return b, err
	}
	b.RoundBaselineHead = head
	b.RoundClosedTree = ""
	return b, nil
}

// chainBranchHead resolves the head of the branch the member's ship publishes
// as refs/relevo/<name>/out: the commit the server's next round is cut from and
// the plan-start commit a plan that begins here records. It is a local ref
// lookup, never a network call. A repo with no git configured leaves the head
// empty rather than failing the stage; a repo that refuses the lookup does not.
func chainBranchHead(ctx context.Context, rt Runtime, b store.Binding) (string, error) {
	if rt.Git == nil {
		return "", nil
	}
	branchRef := b.Branch
	if !strings.HasPrefix(branchRef, "refs/heads/") {
		branchRef = "refs/heads/" + branchRef
	}
	sha, ok, err := rt.Git.RefSHA(ctx, b.Repo, branchRef)
	if err != nil {
		return "", fmt.Errorf("%s: resolve %s: %w", b.Name, branchRef, err)
	}
	if !ok {
		return "", fmt.Errorf("%s: branch %s not found", b.Name, b.Branch)
	}
	return sha, nil
}

// chainSendPending is the unlocked step that ships the rounds a chain has
// staged for a remote member: plan 1, every later plan, corrections and
// repairs alike. It walks the running chains and, for one whose awaiting
// member is remote, has no prompt entry for that round and has a staged prompt
// file, ships the file in one remoteShip and then records the member's round
// under a short lock.
//
// A member in NEEDS YOU, a missing staged file and a non-remote member are
// skipped silently. A second concurrent or repeated run is a no-op (the entry
// now exists) or an identical-plan retry the server dedupes.
func chainSendPending(ctx context.Context, rt Runtime) error {
	if rt.Remote == nil {
		return nil
	}
	chains, err := rt.Store.Chains()
	if err != nil {
		return err
	}
	for _, c := range chains {
		if c.Status != string(chain.StatusRunning) {
			continue
		}
		// The server drives a server chain's sends and the chain pull collects
		// them, so the pending-send step leaves it alone.
		if chainOnServer(c) {
			continue
		}
		if c.AwaitingMember == "" {
			continue
		}
		name := chainMemberName(c, c.AwaitingMember)
		if name == "" {
			continue
		}
		b, err := rt.Store.Load(name)
		if err != nil {
			continue
		}
		if !b.Builder.Remote() || b.State == store.StateNeedsYou {
			continue
		}
		round := b.Round
		if c.AwaitingRound != round {
			continue
		}
		entries, err := rt.Store.ReadLog(name)
		if err != nil {
			continue
		}
		if HasPromptEntry(entries, round) {
			continue
		}
		path := rt.Store.PromptPath(name, round)
		body, err := rt.Store.ReadFile(path)
		if err != nil {
			continue
		}
		if err := chainRemoteSend(ctx, rt, b, round, body); err != nil {
			if herr := chainRemoteSendFailed(ctx, rt, b, err); herr != nil {
				return herr
			}
		}
	}
	return nil
}

// chainRemoteSend ships one staged round to a chain's remote member: remoteShip
// outside any lock, then the chain-aware record half under a short lock that
// re-checks the state (the chain is still running and still awaiting this
// round, and no prompt entry exists yet). A re-check that fails is a no-op.
func chainRemoteSend(ctx context.Context, rt Runtime, b store.Binding, round int, body []byte) error {
	verify := false
	ship, err := remoteShip(ctx, rt, b, body, "", "", false, &verify)
	if err != nil {
		return err
	}
	return rt.Store.WithLock(func(tx *store.Tx) error {
		c, err := tx.ChainByMember(b.Name)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if c.Status != string(chain.StatusRunning) {
			return nil
		}
		if chainMemberName(c, c.AwaitingMember) != b.Name || c.AwaitingRound != round {
			return nil
		}
		entries, err := tx.ReadLog(b.Name)
		if err != nil {
			return err
		}
		if HasPromptEntry(entries, round) {
			return nil
		}
		_, _, err = remoteRecord(rt, tx, b, ship, body, "", true)
		return err
	})
}

// chainRemoteSendFailed records a failed ship the way the plan pins it: the
// member goes NEEDS YOU with the "builder send failed" halt, and the chain ends
// with the sweep's own terminal shape and the reason "member <name> could not
// start". One trace row, one delivery. The outcome is recorded, not returned;
// only a store write failure is returned.
func chainRemoteSendFailed(ctx context.Context, rt Runtime, b store.Binding, sendErr error) error {
	return rt.Store.WithLock(func(tx *store.Tx) error {
		c, err := tx.ChainByMember(b.Name)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if c.Status != string(chain.StatusRunning) {
			return nil
		}
		cur, err := tx.Load(b.Name)
		if err != nil {
			return err
		}
		cur.State = store.StateNeedsYou
		cur.Halt = "builder send failed: " + sendErr.Error()
		cur.HaltAt = rt.Now().UTC()
		if err := tx.Save(cur); err != nil {
			return err
		}
		part := chainPartOf(c, b.Name)
		reason := fmt.Sprintf("member %s could not start: %v", b.Name, sendErr)
		return chainSweepHalt(ctx, rt, tx, c, part, b.Name, reason)
	})
}
