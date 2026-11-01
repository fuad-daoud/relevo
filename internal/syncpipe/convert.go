package syncpipe

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/fuad-daoud/relevo/internal/synclog"
	"github.com/fuad-daoud/relevo/internal/syncworker"
)

// The wire names the log's columns and the daemon names Go fields, so every entry
// crosses twice. Both directions are written out, not reflected: a missed field
// would silently become its zero value across the pipe, which no transport detects
// and a reader acts on anyway.

// entriesToWire renders this origin's batch for export. The batch arrives numbered
// by guess and the transport numbers again, so nothing sent can name a log position.
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

// entriesFromWire reads back numbered exports or pulled entries. Numbers are the
// transport's to hand out; the importer already refuses backwards moves from its
// mark.
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

// PublishMarks records how far this installation applied of each origin. The
// request carries no reader: the worker takes its own from hello. It carries the
// time, since only this process knows when it arrived and a mark's age decides
// whether its reader still counts.
func (c *Client) PublishMarks(marks map[string]int, at time.Time) error {
	_, err := c.call(syncworker.Request{
		Verb:    syncworker.VerbPublishMarks,
		Publish: marksToWire(marks),
		At:      &at,
	})
	return err
}

// ReadMarks answers with every published mark and each origin's size. An empty
// reply is a refusal, not an empty log: the worker always fills the field, so a
// missing one did not do what was asked.
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

// PublishTraffic writes this installation's traffic row and answers with every
// origin's held row. The request carries no origin: the worker keys from hello.
// An empty reply is a refusal: the worker always answers with rows after its own
// write.
func (c *Client) PublishTraffic(row synclog.TrafficRow) (map[string]synclog.TrafficRow, error) {
	resp, err := c.call(syncworker.Request{
		Verb:    syncworker.VerbPublishTraffic,
		Traffic: trafficRowToWire(row),
	})
	if err != nil {
		return nil, err
	}
	if resp.Traffic == nil {
		return nil, errors.New("syncpipe: the worker answered publish_traffic with no rows")
	}
	return trafficRowsFromWire(resp.Traffic), nil
}

func (c *Client) PullRange(origin string, after, upto int) ([]synclog.Entry, error) {
	resp, err := c.call(syncworker.Request{Verb: syncworker.VerbPullRange, Origin: origin, AfterSeq: after, Upto: upto})
	if err != nil {
		return nil, err
	}
	return entriesFromWire(resp.Entries), nil
}

// CompactOwn drops superseded entries to floor. The request names no origin: the
// worker compacts its own, from hello.
func (c *Client) CompactOwn(floor int) (synclog.CompactResult, error) {
	resp, err := c.call(syncworker.Request{Verb: syncworker.VerbCompact, Floor: floor})
	if err != nil {
		return synclog.CompactResult{}, err
	}
	return synclog.CompactResult{Dropped: resp.Dropped, Floor: resp.Floor}, nil
}

// KeyMeta reads the passphrase material the remote holds. A missing field is a
// refusal, not "no key": the worker always answers with the asked field, and "no
// key" would wrongly restart setup.
func (c *Client) KeyMeta(ctx context.Context) (synclog.KeyMeta, error) {
	resp, err := c.call(syncworker.Request{Verb: syncworker.VerbKeyMeta})
	if err != nil {
		return synclog.KeyMeta{}, err
	}
	if resp.KeyMeta == nil {
		return synclog.KeyMeta{}, errors.New("syncpipe: the worker answered key_meta with no key")
	}
	return keyMetaFromWire(*resp.KeyMeta), nil
}

// KeyInit writes the passphrase material where the remote holds none, answering
// with what it holds. The answer rules, not the request: two concurrent --new
// writers each write their own salt, and only the answer says which keyed the log.
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

// KeyRekey rotates the log's key to the spec's epoch, answering with what the
// remote holds.
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

// ClaimIdentity records this machine's claim on origin, answering with what the
// remote holds; an empty token only reads.
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

// marksToWire renders this reader's published positions, sorted so the same marks
// always write the same request line.
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

func trafficRowToWire(row synclog.TrafficRow) *syncworker.TrafficRow {
	return &syncworker.TrafficRow{
		Month: row.Month,
		Push:  row.Push,
		Pull:  row.Pull,
		At:    row.At,
	}
}

func trafficRowsFromWire(rows map[string]syncworker.TrafficRow) map[string]synclog.TrafficRow {
	out := make(map[string]synclog.TrafficRow, len(rows))
	for origin, row := range rows {
		out[origin] = synclog.TrafficRow{
			Month: row.Month,
			Push:  row.Push,
			Pull:  row.Pull,
			At:    row.At,
		}
	}
	return out
}

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

// statsFromWire reads back what the log holds and moved. Byte counters carry
// through: already cumulative, summing again would count every worker call twice
// per stats reply.
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
