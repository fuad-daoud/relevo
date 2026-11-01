package sync

// Adopting an identity: `enable --as X` makes a fresh machine continue as X, an
// installation whose machine is gone. Nothing reaches the remote but under X, and
// nothing under X until X's log imports in full: the temporary identity never
// appends, marks or writes meta, and the machine becomes X in one swap, its first
// append waiting for the daemon restart reading its origin at open.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// KeyAdopted is the fact an adoption leaves: identity taken and its claim token.
const KeyAdopted = "sync.adopted.v1"

// KeyIdentityTaken is the fact a steady attempt leaves when this adopted identity's
// claim holds another machine's token.
const KeyIdentityTaken = "sync.identity_taken.v1"

// KeyAdoptPending is the fact the database swap transaction writes, holding adopted
// identity and claim token.
const KeyAdoptPending = db.KeyAdoptPending

// PendingAdopt is the fact under KeyAdoptPending while an adoption is in flight
// between database swap and installation file rewrite.
type PendingAdopt = db.PendingAdopt

// adoptQuietFor is how long an identity must have been silent in log and published
// marks before another machine may take it.
const adoptQuietFor = 24 * time.Hour

// adoptSkew is how far in the future a stamp may sit and still count as live, the
// same allowance synclog gives a mark.
const adoptSkew = time.Hour

// adoptIgnoredDocs are the document signals a fresh machine always carries: every
// open signals the config document, and the enable's own store step signals the
// secrets one; neither is a change the machine made under its old identity.
var adoptIgnoredDocs = []string{"config", SecretsDocName}

var (
	// ErrAdoptionLive is an adoption of an identity active within the day.
	ErrAdoptionLive = errors.New("sync: that identity was active within the last 24 hours: wait, or run relevo db sync disable on the machine that holds it")
	// ErrAdoptionStampAhead is an adoption of an identity whose log or marks are stamped
	// further ahead than the skew allows: a wrong clock or forged stamp, never quiet.
	ErrAdoptionStampAhead = errors.New("sync: that identity's log or marks are stamped in the future: its clock is wrong or the stamp is forged; fix that clock or run relevo db sync disable on the machine that holds it")
	// ErrAdoptionSelf is an adoption of the identity already held.
	ErrAdoptionSelf = errors.New("sync: this machine already is that installation: run relevo db sync enable without --as")
	// ErrAdoptionClaimed is an adoption another machine claimed first.
	ErrAdoptionClaimed = errors.New("sync: another machine claimed that identity first")
	// ErrAdoptionIncomplete is an adoption whose import stopped before the remote's
	// head; unlock or resolve the held import, then run the enable again.
	ErrAdoptionIncomplete = errors.New("sync: the identity's log was not imported in full: unlock or resolve what the import held, then run the enable again")
	// ErrIdentityTaken is a steady attempt on an adopted identity another machine
	// claimed since.
	ErrIdentityTaken = errors.New("sync: another machine adopted this installation's identity: run relevo db sync disable on one of the two machines")
)

// AdoptionRefusal decides whether an identity is quiet enough to take from its
// newest entry and mark times, zero when none. Within the last day -- or future
// within the marks' skew -- is still using it; further ahead is a wrong clock or
// forged stamp, never quiet. A fast joiner clock reads as quiet, which is
// accepted: a group-key holder can already write as any origin.
func AdoptionRefusal(now, lastEntryAt, lastMarkAt time.Time) error {
	for _, at := range []time.Time{lastEntryAt, lastMarkAt} {
		if at.IsZero() {
			continue
		}
		age := now.Sub(at)
		if age < -adoptSkew {
			return ErrAdoptionStampAhead
		}
		if age < adoptQuietFor {
			return ErrAdoptionLive
		}
	}
	return nil
}

// Adoption is what `enable --as` asks of an Enabler.
type Adoption struct {
	// ID is the installation to continue as.
	ID string
	// Identity writes the new identity beside the database, after the swap;
	// nothing here depends on the daemon having read it.
	Identity func(id string) error
	// NewToken makes the claim's random token; nil draws one.
	NewToken func() string
}

// Adopted is the fact an adoption leaves behind: identity, token and time.
type Adopted struct {
	ID    string    `json:"id"`
	Token string    `json:"token"`
	At    time.Time `json:"at"`
}

// ReadAdopted reads the adoption fact; ok is false when never adopted. An
// unparsable fact is an error, so the claim check never silently turns off.
func ReadAdopted(local db.KV) (Adopted, bool, error) {
	body, ok, err := local.KVGet(KeyAdopted)
	if err != nil || !ok {
		return Adopted{}, false, err
	}
	var a Adopted
	if err := json.Unmarshal(body, &a); err != nil || a.ID == "" || a.Token == "" {
		return Adopted{}, false, fmt.Errorf("sync: the adoption fact does not parse: %w", db.ErrInvalid)
	}
	return a, true, nil
}

func writeAdopted(local db.KV, a Adopted) error {
	body, err := json.Marshal(a)
	if err != nil {
		return fmt.Errorf("sync: adoption fact: %w", err)
	}
	if err := local.KVPut(KeyAdopted, body); err != nil {
		return fmt.Errorf("sync: adoption fact: %w", err)
	}
	return nil
}

// IdentityTaken reads the fact a failed claim check leaves, empty when none does.
func IdentityTaken(local db.KV) string {
	body, ok, err := local.KVGet(KeyIdentityTaken)
	if err != nil || !ok {
		return ""
	}
	var id string
	if json.Unmarshal(body, &id) != nil {
		return ""
	}
	return id
}

// ReadAdoptPending reads the pending adoption fact across the given kv handles.
func ReadAdoptPending(kvs ...db.KV) (PendingAdopt, bool, error) {
	for _, kv := range kvs {
		if kv == nil {
			continue
		}
		body, ok, err := kv.KVGet(KeyAdoptPending)
		if err != nil {
			return PendingAdopt{}, false, err
		}
		if ok {
			var p PendingAdopt
			if err := json.Unmarshal(body, &p); err != nil || p.ID == "" {
				return PendingAdopt{}, false, fmt.Errorf("sync: the pending adoption fact does not parse: %w", db.ErrInvalid)
			}
			return p, true, nil
		}
	}
	return PendingAdopt{}, false, nil
}

// ClearAdoptPending removes the pending adoption fact from the given handles.
func ClearAdoptPending(kvs ...db.KV) error {
	for _, kv := range kvs {
		if kv == nil {
			continue
		}
		if err := kv.KVDelete(KeyAdoptPending); err != nil {
			return fmt.Errorf("sync: clear pending adoption fact: %w", err)
		}
	}
	return nil
}

// AdoptPending returns the identity whose adoption is pending, empty when none.
func AdoptPending(kvs ...db.KV) string {
	p, ok, err := ReadAdoptPending(kvs...)
	if err != nil || !ok {
		return ""
	}
	return p.ID
}

// CheckAdoptedClaim compares the claim on this adopted identity with its own
// token; never-adopted has nothing to check. A mismatch leaves the fact for the
// status surface and stops both halves; an unreadable claim is the transport's
// failure and fences nothing.
func CheckAdoptedClaim(ctx context.Context, local db.KV, transport synclog.LogTransport) error {
	adopted, ok, err := ReadAdopted(local)
	if err != nil || !ok {
		return err
	}
	winner, _, err := transport.ClaimIdentity(ctx, adopted.ID, "", time.Now())
	if err != nil {
		return fmt.Errorf("sync: read the identity claim: %w", err)
	}
	if winner == "" || winner == adopted.Token {
		_ = local.KVDelete(KeyIdentityTaken)
		return nil
	}
	fact, _ := json.Marshal(adopted.ID)
	if perr := local.KVPut(KeyIdentityTaken, fact); perr != nil {
		return fmt.Errorf("%w (and the fact could not be stored: %w)", ErrIdentityTaken, perr)
	}
	return ErrIdentityTaken
}

func newClaimToken() string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		panic(fmt.Sprintf("sync: draw a claim token: %v", err))
	}
	return hex.EncodeToString(raw)
}

// checkAdoptPending refuses enables other than enable --as X while X is pending.
func (e *Enabler) checkAdoptPending() error {
	pendingID := AdoptPending(e.Request.Shared, e.Request.Local)
	if pendingID == "" {
		return nil
	}
	if e.Adopt == nil || e.Adopt.ID != pendingID || e.Request.Join {
		return fmt.Errorf("sync: adoption of %s is unfinished: run relevo db sync enable --as %s", pendingID, pendingID)
	}
	return nil
}

// finishAdopt rewrites the installation identity file, sets the own head, and
// clears the pending adoption fact.
func (e *Enabler) finishAdopt(id string, head int) error {
	if e.Adopt != nil && e.Adopt.Identity != nil {
		if err := e.Adopt.Identity(id); err != nil {
			return fmt.Errorf("sync: adopt: write the identity: %w", err)
		}
	}
	if head > 0 {
		if err := e.Request.Local.KVPut(synclog.OwnHeadFact, []byte(strconv.Itoa(head))); err != nil {
			return fmt.Errorf("sync: adopt: set the own head: %w", err)
		}
	}
	return ClearAdoptPending(e.Request.Shared, e.Request.Local)
}

// resumeAdopt finishes an interrupted adoption whose database swap already
// committed: it re-checks the remote claim with the stored token, writes the
// installation file, sets the own head from the remote, and clears the pending fact.
func (e *Enabler) resumeAdopt(plan EnablePlan, pending PendingAdopt, now time.Time) (EnableResult, error) {
	res := EnableResult{Remote: plan.Remote}
	a := e.Adopt
	if err := e.store(plan, now); err != nil {
		return res, err
	}
	transport, err := e.transport()
	if err != nil {
		return res, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), DefaultTimeout)
	defer cancel()
	// Without the adopted fact CheckAdoptedClaim has nothing to compare, so an
	// unreadable or unwritable fact refuses rather than skipping the check.
	_, ok, err := ReadAdopted(e.Request.Local)
	if err != nil {
		return res, err
	}
	if !ok {
		if err := writeAdopted(e.Request.Local, Adopted{ID: a.ID, Token: pending.Token, At: now.UTC()}); err != nil {
			return res, err
		}
	}
	if err := CheckAdoptedClaim(ctx, e.Request.Local, transport); err != nil {
		return res, err
	}
	state, err := transport.ReadMarks()
	if err != nil {
		return res, fmt.Errorf("sync: adopt: read the log: %w", err)
	}
	if err := e.finishAdopt(a.ID, state.Heads[a.ID]); err != nil {
		return res, err
	}
	res.Adopted = a.ID
	return res, nil
}

// enableAdopt is Enable for an adoption: refuse what is knowable without the remote,
// then claim, import and swap, leaving the machine off until the owner restarts the
// daemon and enables again to resume the join as the adopted identity.
func (e *Enabler) enableAdopt(plan EnablePlan, now time.Time) (EnableResult, error) {
	res := EnableResult{Remote: plan.Remote}
	a := e.Adopt
	shared := e.Request.Shared
	pending, pendingOK, err := ReadAdoptPending(shared, e.Request.Local)
	if err != nil {
		return res, err
	}
	if pendingOK {
		if pending.ID != a.ID {
			return res, fmt.Errorf("sync: adoption of %s is unfinished: run relevo db sync enable --as %s", pending.ID, pending.ID)
		}
		originOK, err := shared.DatabaseOriginIs(a.ID)
		if err != nil {
			return res, err
		}
		if originOK {
			return e.resumeAdopt(plan, pending, now)
		}
	}
	if a.ID == "" || a.ID == shared.Origin() {
		return res, ErrAdoptionSelf
	}
	blockers, err := shared.AdoptionBlockers(adoptIgnoredDocs...)
	if err != nil {
		return res, err
	}
	if !blockers.Empty() {
		return res, fmt.Errorf("sync: this machine holds changes of its own (%s): enable --as takes a fresh machine: %w",
			blockers, db.ErrAdoptionDirty)
	}
	if err := e.store(plan, now); err != nil {
		return res, err
	}
	transport, err := e.transport()
	if err != nil {
		return res, err
	}
	token, err := e.adoptClaim(transport, now)
	if err != nil {
		return res, err
	}
	e.adopting = true
	imported, err := e.joinImport(transport, now)
	res.Imported = imported
	if err != nil {
		return res, fmt.Errorf("sync: enable: import: %w", err)
	}
	head, err := e.confirmAdoption(transport, token)
	if err != nil {
		return res, err
	}
	if err := shared.AdoptIdentity(a.ID, token); err != nil {
		return res, err
	}
	if err := e.finishAdopt(a.ID, head); err != nil {
		return res, err
	}
	res.Adopted = a.ID
	return res, nil
}

// adoptClaim decides the identity is free and claims it, returning the claim token.
// A rerun reuses the stored token, so its own claim is never another machine's.
func (e *Enabler) adoptClaim(transport synclog.LogTransport, now time.Time) (string, error) {
	a := e.Adopt
	ctx, cancel := context.WithTimeout(context.Background(), DefaultTimeout)
	defer cancel()
	state, err := transport.ReadMarks()
	if err != nil {
		return "", fmt.Errorf("sync: adopt: read the log: %w", err)
	}
	lastEntry, err := lastEntryAt(transport, a.ID, state.Heads[a.ID])
	if err != nil {
		return "", err
	}
	var lastMark time.Time
	for _, m := range state.Marks {
		if m.Reader == a.ID && m.At.After(lastMark) {
			lastMark = m.At
		}
	}
	if err := AdoptionRefusal(now, lastEntry, lastMark); err != nil {
		return "", err
	}
	token := ""
	if held, ok, err := ReadAdopted(e.Request.Local); err != nil {
		return "", err
	} else if ok && held.ID == a.ID {
		token = held.Token
	}
	if token == "" {
		token = newClaimToken()
		if a.NewToken != nil {
			token = a.NewToken()
		}
	}
	winner, _, err := transport.ClaimIdentity(ctx, a.ID, token, now)
	if err != nil {
		return "", fmt.Errorf("sync: adopt: claim the identity: %w", err)
	}
	if winner != token {
		return "", ErrAdoptionClaimed
	}
	if err := writeAdopted(e.Request.Local, Adopted{ID: a.ID, Token: token, At: now.UTC()}); err != nil {
		return "", err
	}
	return token, nil
}

// lastEntryAt is the time of an origin's newest entry, zero when none.
func lastEntryAt(transport synclog.LogTransport, origin string, head int) (time.Time, error) {
	if head < 1 {
		return time.Time{}, nil
	}
	entries, err := transport.PullRange(origin, head-1, head)
	if err != nil {
		return time.Time{}, fmt.Errorf("sync: adopt: read the identity's newest entry: %w", err)
	}
	var newest time.Time
	for _, entry := range entries {
		if entry.At.After(newest) {
			newest = entry.At
		}
	}
	return newest, nil
}

// confirmAdoption is the last look before the swap: head imported in full, claim
// still holding this token. The claim reads again here, not trusted from the write,
// because the import outlasts another machine's push landing over it.
func (e *Enabler) confirmAdoption(transport synclog.LogTransport, token string) (int, error) {
	a := e.Adopt
	ctx, cancel := context.WithTimeout(context.Background(), DefaultTimeout)
	defer cancel()
	state, err := transport.ReadMarks()
	if err != nil {
		return 0, fmt.Errorf("sync: adopt: read the log: %w", err)
	}
	head := state.Heads[a.ID]
	mark, _, err := e.Request.Shared.ImportMark(a.ID)
	if err != nil {
		return 0, err
	}
	if mark < head {
		return 0, ErrAdoptionIncomplete
	}
	winner, _, err := transport.ClaimIdentity(ctx, a.ID, "", e.now())
	if err != nil {
		return 0, fmt.Errorf("sync: adopt: read the identity claim: %w", err)
	}
	if winner != token {
		return 0, ErrAdoptionClaimed
	}
	return head, nil
}
