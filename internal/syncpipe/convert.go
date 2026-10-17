package syncpipe

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/fuad-daoud/relevo/internal/synclog"
	"github.com/fuad-daoud/relevo/internal/syncworker"
)

// The wire names the log's own columns and the daemon names Go fields, so every
// entry crosses this boundary twice. Both directions are written out rather
// than reflected over, because a field the reflection missed would be one that
// silently became its zero value on the far side of a pipe -- which is the
// failure a transport cannot detect and a reader would act on anyway.

// entriesToWire renders this origin's batch for an export. A batch arrives
// numbered whatever the writer guessed, and the transport numbers it again, so
// nothing the caller sent can name a position in the log.
func entriesToWire(entries []synclog.Entry) []syncworker.Entry {
	if len(entries) == 0 {
		return nil
	}
	out := make([]syncworker.Entry, 0, len(entries))
	for _, e := range entries {
		var afterOrigin string
		var afterSeq int
		if e.After != nil {
			afterOrigin = e.After.Origin
			afterSeq = e.After.Seq
		}
		out = append(out, syncworker.Entry{
			Origin:        e.Origin,
			Seq:           e.Seq,
			Batch:         e.Batch,
			Tbl:           e.Table,
			PK:            e.PK,
			Op:            string(e.Op),
			SchemaVersion: e.SchemaVersion,
			Body:          e.Body,
			At:            e.At,
			MAC:           e.MAC,
			AfterOrigin:   afterOrigin,
			AfterSeq:      afterSeq,
		})
	}
	return out
}

// entriesFromWire reads back what an export numbered or a pull returned. The
// entries are taken as they arrive rather than checked here: the numbers are
// the transport's to hand out, and the importer already refuses to go backwards
// from the mark it holds.
func entriesFromWire(entries []syncworker.Entry) []synclog.Entry {
	if len(entries) == 0 {
		return nil
	}
	out := make([]synclog.Entry, 0, len(entries))
	for _, e := range entries {
		var after *synclog.Ref
		if e.AfterOrigin != "" || e.AfterSeq > 0 {
			after = &synclog.Ref{
				Origin: e.AfterOrigin,
				Seq:    e.AfterSeq,
			}
		}
		out = append(out, synclog.Entry{
			Origin:        e.Origin,
			Seq:           e.Seq,
			Batch:         e.Batch,
			Table:         e.Tbl,
			PK:            e.PK,
			Op:            synclog.Op(e.Op),
			SchemaVersion: e.SchemaVersion,
			Body:          e.Body,
			At:            e.At,
			MAC:           e.MAC,
			After:         after,
		})
	}
	return out
}

// PublishMarks records how far this installation has applied of each origin.
//
// The request carries no reader: the worker takes its own from hello, so a
// caller here cannot name a machine that is not this one. What it does carry is
// the time, because only this process knows when it reached those sequences and
// a mark's age is what decides whether its reader still counts towards a floor.
func (c *Client) PublishMarks(marks map[string]int, at time.Time) error {
	_, err := c.call(syncworker.Request{
		Verb:    syncworker.VerbPublishMarks,
		Publish: marksToWire(marks),
		At:      &at,
	})
	return err
}

// ReadMarks answers with every published mark and each origin's size.
//
// A reply with nothing in it is a refusal rather than an empty log: the worker
// always fills the field it answers with, so a reply without one is a worker
// that did not do what was asked. Reading it as "no reader has published"
// would floor every origin at nothing.
func (c *Client) ReadMarks() (synclog.MarkState, error) {
	resp, err := c.call(syncworker.Request{Verb: syncworker.VerbReadMarks})
	if err != nil {
		return synclog.MarkState{}, err
	}
	if resp.Marks == nil {
		return synclog.MarkState{}, errors.New("syncpipe: the worker answered read_marks with no marks")
	}
	return markStateFromWire(*resp.Marks), nil
}

// PullRange reads one page of an origin's entries in (after, upto].
func (c *Client) PullRange(origin string, after, upto int) ([]synclog.Entry, error) {
	resp, err := c.call(syncworker.Request{Verb: syncworker.VerbPullRange, Origin: origin, AfterSeq: after, Upto: upto})
	if err != nil {
		return nil, err
	}
	return entriesFromWire(resp.Entries), nil
}

// CompactOwn drops this installation's superseded entries up to floor. The
// request names no origin: the worker compacts its own, from hello.
func (c *Client) CompactOwn(floor int) (synclog.CompactResult, error) {
	resp, err := c.call(syncworker.Request{Verb: syncworker.VerbCompact, Floor: floor})
	if err != nil {
		return synclog.CompactResult{}, err
	}
	return synclog.CompactResult{Dropped: resp.Dropped, Floor: resp.Floor}, nil
}

// KeyMeta reads the passphrase material the remote holds.
func (c *Client) KeyMeta(ctx context.Context) (synclog.KeyMeta, error) {
	resp, err := c.call(syncworker.Request{Verb: syncworker.VerbKeyMeta})
	if err != nil {
		return synclog.KeyMeta{}, err
	}
	if resp.KeyMeta == nil {
		// The worker always answers with the field it was asked about, so an
		// answer without one is a worker that did not do what was asked. Reading
		// it as "no key" would take the caller down the setup path again.
		return synclog.KeyMeta{}, errors.New("syncpipe: the worker answered key_meta with no key")
	}
	return keyMetaFromWire(*resp.KeyMeta), nil
}

// KeyInit writes the passphrase material where the remote holds none and
// answers with what it holds afterwards.
//
// The answer is used rather than the request on purpose: two machines running
// --new at once each write their own salt, and only the answer tells either of
// them which one the log is actually keyed by.
func (c *Client) KeyInit(ctx context.Context, key synclog.KeyMeta) (synclog.KeyMeta, error) {
	resp, err := c.call(syncworker.Request{
		Verb:    syncworker.VerbKeyInit,
		KeyMeta: keyToWire(key),
	})
	if err != nil {
		return synclog.KeyMeta{}, err
	}
	if resp.KeyMeta == nil {
		return synclog.KeyMeta{}, errors.New("syncpipe: the worker answered key_init with no key")
	}
	return keyMetaFromWire(*resp.KeyMeta), nil
}

// keyToWire renders the passphrase material for a key_init or a key_rekey.
func keyToWire(key synclog.KeyMeta) *syncworker.KeyMeta {
	return &syncworker.KeyMeta{
		Salt:    key.Salt,
		KDF:     key.KDF,
		Check:   key.Check,
		Present: key.Present,
		Epoch:   key.Epoch,
		Wrapped: key.Wrapped,
	}
}

// keyMetaFromWire reads back the passphrase material the worker answered with.
func keyMetaFromWire(k syncworker.KeyMeta) synclog.KeyMeta {
	return synclog.KeyMeta{
		Salt:    k.Salt,
		KDF:     k.KDF,
		Check:   k.Check,
		Present: k.Present,
		Epoch:   k.Epoch,
		Wrapped: k.Wrapped,
	}
}

// KeyRekey rotates the log's key to the epoch the spec names and answers with
// what the remote holds afterwards.
func (c *Client) KeyRekey(ctx context.Context, spec synclog.KeyRekeySpec) (synclog.KeyMeta, error) {
	resp, err := c.call(syncworker.Request{
		Verb:    syncworker.VerbKeyRekey,
		Epoch:   spec.From,
		KeyMeta: keyToWire(spec.Key),
		Cuts:    cutsToWire(spec.Cuts),
	})
	if err != nil {
		return synclog.KeyMeta{}, err
	}
	if resp.KeyMeta == nil {
		return synclog.KeyMeta{}, errors.New("syncpipe: the worker answered key_rekey with no key")
	}
	return keyMetaFromWire(*resp.KeyMeta), nil
}

// PublishEpochCut extends this installation's own cut to the one named, on the
// worker side forward only and within the log.
func (c *Client) PublishEpochCut(ctx context.Context, cut synclog.EpochCut) error {
	_, err := c.call(syncworker.Request{
		Verb: syncworker.VerbKeyCut,
		Cut: &syncworker.EpochCut{
			Epoch: cut.Epoch, Seq: cut.Seq, Tag: cut.Tag,
		},
	})
	return err
}

// ClaimIdentity records this machine's claim on origin and answers with the
// claim the remote holds afterwards; an empty token only reads it.
func (c *Client) ClaimIdentity(ctx context.Context, origin, token string, at time.Time) (string, time.Time, error) {
	resp, err := c.call(syncworker.Request{
		Verb:       syncworker.VerbClaim,
		Origin:     origin,
		ClaimToken: token,
		At:         &at,
	})
	if err != nil {
		return "", time.Time{}, err
	}
	var claimedAt time.Time
	if resp.ClaimAt != nil {
		claimedAt = *resp.ClaimAt
	}
	return resp.ClaimToken, claimedAt, nil
}

// cutsToWire renders the cuts a key_rekey writes.
func cutsToWire(cuts map[string]synclog.EpochCut) map[string]syncworker.EpochCut {
	if len(cuts) == 0 {
		return nil
	}
	out := make(map[string]syncworker.EpochCut, len(cuts))
	for origin, cut := range cuts {
		out[origin] = syncworker.EpochCut{Epoch: cut.Epoch, Seq: cut.Seq, Tag: cut.Tag}
	}
	return out
}

// epochCutsFromWire reads back the cuts a read_marks answered with. The two
// shapes name the same three fields, so the map carries through one row at a
// time, the way every conversion above does.
func epochCutsFromWire(cuts map[string]syncworker.EpochCut) map[string]synclog.EpochCut {
	if len(cuts) == 0 {
		return nil
	}
	out := make(map[string]synclog.EpochCut, len(cuts))
	for origin, cut := range cuts {
		out[origin] = synclog.EpochCut{Epoch: cut.Epoch, Seq: cut.Seq, Tag: cut.Tag}
	}
	return out
}

// marksToWire renders this reader's published positions.
//
// The order is sorted so a request's line is the same for the same marks every
// time: the wire is what a golden line and a failing diff are read against, and
// a map's own order would make it neither.
func marksToWire(marks map[string]int) []syncworker.Mark {
	if len(marks) == 0 {
		return nil
	}
	origins := make([]string, 0, len(marks))
	for origin := range marks {
		origins = append(origins, origin)
	}
	sort.Strings(origins)
	out := make([]syncworker.Mark, 0, len(origins))
	for _, origin := range origins {
		out = append(out, syncworker.Mark{Origin: origin, Seq: marks[origin]})
	}
	return out
}

// markStateFromWire reads back what a read_marks answered with.
func markStateFromWire(s syncworker.MarkState) synclog.MarkState {
	out := synclog.MarkState{
		Floors:     s.Floors,
		Heads:      s.Heads,
		Entries:    s.Entries,
		Sizes:      s.Sizes,
		SignedFrom: s.SignedFrom,
		SignedTags: s.SignedTags,
		EpochCuts:  epochCutsFromWire(s.EpochCuts),
	}
	for _, m := range s.Marks {
		out.Marks = append(out.Marks, synclog.Mark{
			Reader: m.Reader,
			Origin: m.Origin,
			Seq:    m.Seq,
			At:     m.At,
		})
	}
	return out
}

// headFromWire reads back a page of an origin's latest state per row.
func headFromWire(rows []syncworker.HeadRow) []synclog.HeadRow {
	if len(rows) == 0 {
		return nil
	}
	out := make([]synclog.HeadRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, synclog.HeadRow{
			Table: r.Tbl,
			PK:    r.PK,
			Seq:   r.Seq,
			Hash:  r.Hash,
		})
	}
	return out
}

// statsFromWire reads back what the log holds, and what the worker has moved.
//
// The four byte counters are carried through rather than summed here: they are
// already cumulative, and adding to them a second time would count every call
// the worker made once per stats reply the daemon asked for.
func statsFromWire(s syncworker.Stats) synclog.Stats {
	return synclog.Stats{
		Entries:       s.Entries,
		Origins:       s.Origins,
		Seq:           s.Seq,
		TursoSent:     s.TursoSent,
		TursoReceived: s.TursoReceived,
		R2Put:         s.R2Put,
		R2Get:         s.R2Get,
	}
}
