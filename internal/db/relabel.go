package db

// Sets the feature label on finished bindings. Reading and writing are
// separate phases so a refusal or a dry run is the plan alone and cannot reach
// a write.

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrRelabelRefused reports ids the batch cannot touch: unknown, or owned by
// another installation. Nothing is written when it is returned.
var ErrRelabelRefused = errors.New("relabel refused")

// RelabelEntry names one binding id and the label it should carry; Line is the
// 1-based input line, for the refusal text.
type RelabelEntry struct {
	ID, Feature string
	Line        int
}

type RelabelParams struct {
	Entries   []RelabelEntry
	Overwrite bool
	DryRun    bool
}

// RelabelChange is one binding a run changes, or in a dry run would change.
// Old is empty when the binding had no label.
type RelabelChange struct {
	ID, Name, Old, New string
}

// RelabelCounts is what the run changed, or in a dry run what it would.
type RelabelCounts struct {
	DryRun           bool            `json:"dry_run"`
	Requested        int             `json:"requested"`
	Labelled         int             `json:"labelled"`
	Unchanged        int             `json:"unchanged"`
	SkippedLabelled  int             `json:"skipped_labelled"`
	RecordsRewritten int             `json:"records_rewritten"`
	Changes          []RelabelChange `json:"-"`
}

type relabelRecord struct{ id, json string }

type relabelPlan struct {
	counts  RelabelCounts
	labels  []relabelWrite
	records []relabelRecord
}

type relabelWrite struct{ id, feature string }

func (d *DB) Relabel(p RelabelParams) (RelabelCounts, error) {
	var counts RelabelCounts
	err := d.Tx(func(t *Tx) error {
		var err error
		counts, err = t.Relabel(p)
		return err
	})
	return counts, err
}

// Relabel plans the whole run from reads, then applies it only when the params
// are not a dry run.
func (t *Tx) Relabel(p RelabelParams) (RelabelCounts, error) {
	plan, err := t.planRelabel(p)
	if err != nil {
		return RelabelCounts{}, err
	}
	if p.DryRun {
		return plan.counts, nil
	}
	if err := t.applyRelabel(plan); err != nil {
		return RelabelCounts{}, err
	}
	return plan.counts, nil
}

type relabelBinding struct {
	name, createdAt string
	feature         sql.Null[string]
	origin          string
}

func (t *Tx) planRelabel(p RelabelParams) (relabelPlan, error) {
	plan := relabelPlan{counts: RelabelCounts{DryRun: p.DryRun, Requested: len(p.Entries)}}
	found := make([]relabelBinding, len(p.Entries))
	var refusals []string
	for i, e := range p.Entries {
		b, refusal, err := t.relabelLookup(e)
		if err != nil {
			return plan, err
		}
		if refusal != "" {
			refusals = append(refusals, refusal)
		}
		found[i] = b
	}
	if len(refusals) > 0 {
		return plan, fmt.Errorf("db: relabel: %w: %s", ErrRelabelRefused, strings.Join(refusals, "; "))
	}
	for i, e := range p.Entries {
		if err := t.classifyRelabel(&plan, p, e, found[i]); err != nil {
			return plan, err
		}
	}
	return plan, nil
}

func (t *Tx) relabelLookup(e RelabelEntry) (relabelBinding, string, error) {
	var b relabelBinding
	err := t.queryRow(`SELECT name, created_at, feature, origin FROM binding WHERE id = ?`, e.ID).
		Scan(&b.name, &b.createdAt, &b.feature, &b.origin)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return b, fmt.Sprintf("line %d: unknown id %s", e.Line, e.ID), nil
	case err != nil:
		return b, "", fmt.Errorf("db: relabel: read binding %s: %w", e.ID, mapBusy(err))
	case b.origin != t.origin && b.origin != "":
		return b, fmt.Sprintf("line %d: id %s belongs to origin %s", e.Line, e.ID, b.origin), nil
	}
	return b, "", nil
}

func (t *Tx) classifyRelabel(plan *relabelPlan, p RelabelParams, e RelabelEntry, b relabelBinding) error {
	switch {
	case b.feature.Valid && b.feature.V == e.Feature:
		plan.counts.Unchanged++
		return nil
	case b.feature.Valid && !p.Overwrite:
		plan.counts.SkippedLabelled++
		return nil
	}
	recs, err := t.relabelRecords(b, e.Feature)
	if err != nil {
		return err
	}
	plan.counts.Labelled++
	plan.counts.RecordsRewritten += len(recs)
	plan.labels = append(plan.labels, relabelWrite{e.ID, e.Feature})
	plan.records = append(plan.records, recs...)
	plan.counts.Changes = append(plan.counts.Changes, RelabelChange{ID: e.ID, Name: b.name, Old: b.feature.V, New: e.Feature})
	return nil
}

// relabelRecords finds the record rows of one binding and returns those whose
// JSON changes. Binding and record share no id, so a record is the binding's
// when it carries the binding's name and a created_at that formats to the
// binding's created_at; names are reused across time, so the name alone never
// decides. A record with no usable created_at matches nothing.
func (t *Tx) relabelRecords(b relabelBinding, feature string) ([]relabelRecord, error) {
	rows, err := t.queryIDValues(`SELECT id, record_json FROM binding_record WHERE `+originScope+` AND name = ?`, t.origin, b.name)
	if err != nil {
		return nil, err
	}
	var out []relabelRecord
	for _, r := range rows {
		var head struct {
			CreatedAt time.Time `json:"created_at"`
		}
		if json.Unmarshal([]byte(r.value), &head) != nil || head.CreatedAt.IsZero() || formatTime(head.CreatedAt) != b.createdAt {
			continue
		}
		if next, changed := setRecordFeature([]byte(r.value), feature); changed {
			out = append(out, relabelRecord{r.id, string(next)})
		}
	}
	return out, nil
}

// setRecordFeature rewrites the feature key byte-preservingly, appending the
// key when the object has none. changed is false when the bytes would not move
// or raw is not one JSON object.
func setRecordFeature(raw []byte, feature string) (out []byte, changed bool) {
	enc := encodeJSONString(feature)
	seen := false
	out, mapped, ok := mapObject(raw, func(key string, val json.RawMessage) (json.RawMessage, bool) {
		if key != "feature" {
			return val, false
		}
		seen = true
		return enc, !bytes.Equal(val, enc)
	})
	if !ok {
		return raw, false
	}
	if !seen {
		sep := ","
		if bytes.Equal(out, []byte("{}")) {
			sep = ""
		}
		out = append(append(append(out[:len(out)-1:len(out)-1], sep...), `"feature":`...), append(enc, '}')...)
		return out, true
	}
	return out, mapped
}

func (t *Tx) applyRelabel(plan relabelPlan) error {
	for _, w := range plan.labels {
		if _, err := t.exec(`UPDATE binding SET origin = ?, feature = ? WHERE id = ?`, t.origin, w.feature, w.id); err != nil {
			return fmt.Errorf("db: relabel: update binding %s: %w", w.id, mapBusy(err))
		}
	}
	for _, r := range plan.records {
		if _, err := t.exec(`UPDATE binding_record SET origin = ?, record_json = ? WHERE id = ?`, t.origin, r.json, r.id); err != nil {
			return fmt.Errorf("db: relabel: update record %s: %w", r.id, mapBusy(err))
		}
	}
	return nil
}
