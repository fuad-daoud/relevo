package sync

// Adopting an identity: `enable --as X` makes a fresh machine continue as X,
// an installation whose machine is gone.
//
// Nothing reaches the remote under any id but X, and nothing reaches it under X
// until X's log has been imported in full. The machine's own temporary identity
// therefore never appends, publishes a mark or writes a meta row: the join runs
// as far as its import and stops, with X's log landing as a foreign origin's,
// which the importer already knows how to do, snapshots and floors included.
// Only then does the machine become X, in one swap, and X's first append waits
// for the owner to restart the daemon, which reads its origin at open.

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

// KeyAdopted is the fact an adoption leaves: the identity taken and the token
// the claim on it carries.
const KeyAdopted = "sync.adopted.v1"

// KeyIdentityTaken is the fact a steady attempt leaves when the claim on this
// machine's adopted identity holds another machine's token.
const KeyIdentityTaken = "sync.identity_taken.v1"

// KeyAdoptPending is the fact written in the database swap transaction holding
// the adopted identity and the claim token.
const KeyAdoptPending = db.KeyAdoptPending

// PendingAdopt is the fact stored under KeyAdoptPending while an adoption is
// in flight between the database swap and the installation file rewrite.
type PendingAdopt = db.PendingAdopt

// adoptQuietFor is how long an identity must have been silent, both in its log
// and in the marks its reader published, before another machine may take it.
const adoptQuietFor = 24 * time.Hour

// adoptSkew is how far in the future a stamp may sit and still count as live,
// the same allowance synclog gives a mark.
const adoptSkew = time.Hour

// adoptIgnoredDocs are the document signals a fresh machine always carries:
// every open signals the config document and the enable's own store step the
// secrets one, and neither is a change the machine made under its old identity.
var adoptIgnoredDocs = []string{"config", SecretsDocName}

var (
	// ErrAdoptionLive is an adoption of an identity that wrote or read within the
	// day: a machine is still using it.
	ErrAdoptionLive = errors.New("sync: that identity was active within the last 24 hours: wait, or run relevo db sync disable on the machine that holds it")
	// ErrAdoptionStampAhead is an adoption of an identity whose log or marks are
	// stamped further in the future than the skew allows.
	ErrAdoptionStampAhead = errors.New("sync: that identity's log or marks are stamped in the future: its clock is wrong or the stamp is forged; fix that clock or run relevo db sync disable on the machine that holds it")
	// ErrAdoptionSelf is an adoption of the identity the machine already has.
	ErrAdoptionSelf = errors.New("sync: this machine already is that installation: run relevo db sync enable without --as")
	// ErrAdoptionClaimed is an adoption another machine claimed first.
	ErrAdoptionClaimed = errors.New("sync: another machine claimed that identity first")
	// ErrAdoptionIncomplete is an adoption whose import of the identity's log
	// stopped before the head the remote holds.
	ErrAdoptionIncomplete = errors.New("sync: the identity's log was not imported in full: unlock or resolve what the import held, then run the enable again")
	// ErrIdentityTaken is a steady attempt on a machine whose adopted identity
	// another machine has claimed since.
	ErrIdentityTaken = errors.New("sync: another machine adopted this installation's identity: run relevo db sync disable on one of the two machines")
)

// AdoptionRefusal decides whether an identity is quiet enough to take. lastEntryAt
// is the time of the identity's newest log entry and lastMarkAt the newest mark
// its reader published, either zero when there is none. A stamp within the last
// day, or in the future by no more than the skew the marks allow, is a machine
// still using the identity; a stamp further ahead than the skew is refused too,
// since it is a wrong clock or a forged stamp and neither is quiet.
//
// The decision trusts both the joiner's clock (now) and the remote's stamps. A
// fast joiner clock cannot be told apart from a quiet identity without a time
// source the remote does not offer, and that is accepted: a holder of the group
// key can already write as any origin.
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
	// Identity writes the new identity beside the database. It runs after the
	// database swap and nothing here depends on the daemon having read it.
	Identity func(id string) error
	// NewToken makes the random token the claim carries; nil draws one.
	NewToken func() string
}

// Adopted is the fact an adoption leaves behind.
type Adopted struct {
	ID    string    `json:"id"`
	Token string    `json:"token"`
	At    time.Time `json:"at"`
}

// ReadAdopted reads the adoption fact; ok is false on a machine that never
// adopted. A fact that does not parse is an error: the claim check must not
// silently turn itself off.
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

// IdentityTaken reads the fact a failed claim check leaves: the identity whose
// claim another machine holds, empty when none does.
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

// CheckAdoptedClaim compares the claim on this machine's adopted identity with
// its own token. A machine that never adopted has nothing to check. A mismatch
// is the double win a last-writer-wins push allows, found: the fact is left for
// the status surface and the caller stops both halves of the exchange. A claim
// that cannot be read is the transport's failure and fences nothing.
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

// checkAdoptPending refuses enables other than enable --as X when adoption of X is pending.
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

// finishAdopt rewrites the installation identity file, sets the own head fact,
// and clears the pending adoption fact.
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
// committed: it skips blockers, import and swap, re-checks the claim on the
// remote with the stored token, writes the installation file, sets the own head
// from the remote, and clears the pending fact.
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
	// Without the adopted fact CheckAdoptedClaim has nothing to compare, so a
	// fact that cannot be read or written refuses rather than skipping the check.
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

// enableAdopt is Enable for an adoption. It refuses before it stores anything it
// can know without the remote, then claims, imports and swaps, and leaves the
// machine off: the owner restarts the daemon and runs enable again, which
// resumes the join as the adopted identity and turns the machine on.
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

// adoptClaim decides the identity is free and claims it, returning the token
// the claim carries. A rerun reuses the token the earlier run stored, so its own
// claim is not another machine's.
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

// lastEntryAt is the time of an origin's newest entry, zero when it has none.
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

// confirmAdoption is the last look before the swap: the log's head has been
// imported in full, and the claim still holds this machine's token. The claim is
// read again here rather than trusted from the write, because the import took
// long enough for another machine's push to land over it.
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
