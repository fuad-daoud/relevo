package ingest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/legacy"
	"github.com/fuad-daoud/relevo/internal/store"
)

// DedupeStats summarises one DedupeMirrorOnce run; it is also the JSON document
// the run leaves under the kv key, which keeps the removal a one-time event.
type DedupeStats struct {
	DoneAt                        time.Time `json:"done_at"`
	MirrorBindings                int       `json:"mirror_bindings"`
	Unmapped                      int       `json:"unmapped"`
	ArtifactsDeleted              int       `json:"artifacts_deleted"`
	ArtifactsKept                 int       `json:"artifacts_kept"`
	TranscriptRoundsDeleted       int       `json:"transcript_rounds_deleted"`
	TranscriptRowsDeleted         int       `json:"transcript_rows_deleted"`
	TranscriptRoundsKept          int       `json:"transcript_rounds_kept"`
	TranscriptRoundsByStreamLines int       `json:"transcript_rounds_by_stream_lines"`
	TranscriptRowsRenamed         int       `json:"transcript_rows_renamed"`
	BackupPath                    string    `json:"backup_path"`
	VacuumErr                     string    `json:"vacuum_err"`
}

// dedupePlan is what DedupeMirror decides: the rows to remove plus the counts a
// DedupeStats reports. Nothing is deleted until DedupeMirrorOnce applies it.
type dedupePlan struct {
	artifactIDs      []string
	transcriptOwners []string
	stats            DedupeStats
}

// dedupeArtifactSuffix is the round-file name each artifact kind is sealed under,
// after the "%03d-" prefix. answer is deliberately absent: it comes from a log
// payload and is always kept.
var dedupeArtifactSuffix = map[string]string{
	db.ArtifactPlan:     "plan.md",
	db.ArtifactReport:   "report.md",
	db.ArtifactDiff:     "diff.patch",
	db.ArtifactDrift:    "drift.patch",
	db.ArtifactQuestion: "question.md",
}

// dedupeArtifactKinds is every kind DedupeMirror examines, in order: those with a
// round-file counterpart, plus answer. gate_log, ask and findings are excluded.
var dedupeArtifactKinds = []string{
	db.ArtifactPlan,
	db.ArtifactReport,
	db.ArtifactDiff,
	db.ArtifactDrift,
	db.ArtifactQuestion,
	db.ArtifactAnswer,
}

// dedupeRoundFileBase is the round-file basename kind is sealed under, or "" when
// the kind has no round file.
func dedupeRoundFileBase(kind string, number int) string {
	suffix, ok := dedupeArtifactSuffix[kind]
	if !ok {
		return ""
	}
	return fmt.Sprintf("%03d-", number) + suffix
}

// DedupeMirror builds the one-time removal plan for d's ingest mirror,
// read-only: DedupeMirrorOnce applies it.
//
// A mirror binding maps to a record when exactly one of the local owner's records
// -- live or archived -- has the same name and created_at; zero or more than one
// match leaves it unmapped, and an unmapped binding keeps every row. A mapped
// binding's artifact row is a duplicate when the record's round file for it exists,
// holds the same byte count and hashes to the artifact's own sha256. Its
// transcript is a duplicate when re-deriving it reproduces every row exactly, or
// every row is covered by the record's sealed lines (streamLinesCover). Planner
// transcripts are never examined.
//
// renames are the substitutions a cutover applied to the sealed stream files, so a
// row whose stored path predates the rewrite still matches its rewritten line.
func DedupeMirror(d *db.DB, renames []legacy.Prefix) (dedupePlan, error) {
	var plan dedupePlan

	// Filter{}'s Archived is nil, which queryBindings reads as "no constraint",
	// so this lists every mirror binding, archived ones included.
	bindings, err := d.Bindings(db.Filter{})
	if err != nil {
		return dedupePlan{}, fmt.Errorf("dedupe: bindings: %w", err)
	}

	live, err := d.RecordList("")
	if err != nil {
		return dedupePlan{}, fmt.Errorf("dedupe: records: %w", err)
	}
	archived, err := d.RecordListArchived("")
	if err != nil {
		return dedupePlan{}, fmt.Errorf("dedupe: archived records: %w", err)
	}
	records := append(live, archived...)

	for _, b := range bindings {
		plan.stats.MirrorBindings++
		record, ok := dedupeRecord(b, records)
		if !ok {
			plan.stats.Unmapped++
			continue
		}
		if err := planBinding(&plan, d, b, record, renames); err != nil {
			return dedupePlan{}, err
		}
	}

	return plan, nil
}

// dedupeRecord returns the one record that maps to b, false when zero or more than
// one do.
func dedupeRecord(b db.BindingRow, records []db.Record) (db.Record, bool) {
	var match db.Record
	found := 0
	for _, r := range records {
		if r.Name == b.Name && r.CreatedAt.Equal(b.CreatedAt) {
			match = r
			found++
		}
	}
	if found != 1 {
		return db.Record{}, false
	}
	return match, true
}

func planBinding(plan *dedupePlan, d *db.DB, b db.BindingRow, record db.Record, renames []legacy.Prefix) error {
	rounds, err := d.Rounds(b.ID)
	if err != nil {
		return fmt.Errorf("dedupe: rounds of %s: %w", b.Name, err)
	}
	for _, rd := range rounds {
		if err := planArtifacts(plan, d, record, rd); err != nil {
			return err
		}
		if err := planTranscript(plan, d, b, record, rd, renames); err != nil {
			return err
		}
	}
	return nil
}

func planArtifacts(plan *dedupePlan, d *db.DB, record db.Record, rd db.Round) error {
	for _, kind := range dedupeArtifactKinds {
		a, ok, err := d.Artifact(rd.ID, kind)
		if err != nil {
			return fmt.Errorf("dedupe: artifact %s/%s: %w", record.Name, kind, err)
		}
		if !ok {
			continue
		}

		duplicate := false
		if base := dedupeRoundFileBase(kind, rd.Number); base != "" {
			body, _, found, ferr := d.RoundFileGet(record.ID, base)
			if ferr != nil {
				return fmt.Errorf("dedupe: round file %s/%s: %w", record.Name, base, ferr)
			}
			duplicate = found && sha256Hex(body) == a.SHA256 && int64(len(body)) == a.Bytes
		}

		if duplicate {
			plan.artifactIDs = append(plan.artifactIDs, a.ID)
			plan.stats.ArtifactsDeleted++
		} else {
			plan.stats.ArtifactsKept++
		}
	}
	return nil
}

// planTranscript applies the transcript identity rule to one mirror round.
func planTranscript(plan *dedupePlan, d *db.DB, b db.BindingRow, record db.Record, rd db.Round, renames []legacy.Prefix) error {
	rows, err := d.Transcript(db.OwnerRound, rd.ID, 0, 0)
	if err != nil {
		return fmt.Errorf("dedupe: transcript of %s round %d: %w", b.Name, rd.Number, err)
	}
	if len(rows) == 0 {
		return nil
	}

	candidates, err := deriveTranscripts(d, record, rd)
	if err != nil {
		return err
	}
	for _, derived := range candidates {
		if transcriptRowsEqual(rows, derived) {
			plan.removeTranscript(rd.ID, len(rows), false, 0)
			return nil
		}
	}

	covered, renamed, err := streamLinesCover(d, record, rd, rows, renames)
	if err != nil {
		return err
	}
	if covered {
		plan.removeTranscript(rd.ID, len(rows), true, renamed)
		return nil
	}

	plan.stats.TranscriptRoundsKept++
	return nil
}

func (p *dedupePlan) removeTranscript(ownerID string, rows int, byStreamLines bool, renamed int) {
	p.transcriptOwners = append(p.transcriptOwners, ownerID)
	p.stats.TranscriptRoundsDeleted++
	p.stats.TranscriptRowsDeleted += rows
	if byStreamLines {
		p.stats.TranscriptRoundsByStreamLines++
		p.stats.TranscriptRowsRenamed += renamed
	}
}

// streamLinesCover reports whether every row is proven by the record's sealed
// round files, and how many matched only after the rename rewrite. Three cases, in
// order: a row with record JSON is covered when it is a line of the sealed builder
// stream, directly or -- with renames -- after the rewrite; a row with neither
// record JSON nor rendered text is a blank stream line and holds nothing; a row
// with rendered text but no record JSON is covered when it is a line of the sealed
// builder log verbatim (relevo migrate never rewrote .log files, so no rename
// rewrite is tried). A missing stream is not fatal. rows must be non-empty.
func streamLinesCover(d *db.DB, record db.Record, rd db.Round, rows []db.TranscriptRecord, renames []legacy.Prefix) (covered bool, renamed int, err error) {
	streamName, streamBody, found, err := sealedStream(d, record, rd.Number)
	if err != nil {
		return false, 0, err
	}
	var stream map[string]bool
	if found {
		stream, err = lineSet(record, streamName, streamBody)
		if err != nil {
			return false, 0, err
		}
	}

	var log map[string]bool
	logLoaded := false
	for _, row := range rows {
		if row.RecordJSON != "" {
			ok, renamedRow := streamCoversRecord(row.RecordJSON, stream, renames)
			if !ok {
				return false, 0, nil
			}
			if renamedRow {
				renamed++
			}
			continue
		}
		if row.Rendered == "" {
			continue
		}
		if !logLoaded {
			logLoaded = true
			log, err = sealedLogSet(d, record, rd)
			if err != nil {
				return false, 0, err
			}
		}
		if !log[row.Rendered] {
			return false, 0, nil
		}
	}
	return true, renamed, nil
}

// streamCoversRecord reports whether a row's record JSON is a line of the sealed
// builder stream; renamed is true when only the rewrite matched.
func streamCoversRecord(jsonLine string, stream map[string]bool, renames []legacy.Prefix) (ok, renamed bool) {
	if stream[jsonLine] {
		return true, false
	}
	if len(renames) == 0 {
		return false, false
	}
	rewritten := string(legacy.RewriteJSON([]byte(jsonLine), renames))
	if rewritten == jsonLine || !stream[rewritten] {
		return false, false
	}
	return true, true
}

func sealedLogSet(d *db.DB, record db.Record, rd db.Round) (map[string]bool, error) {
	logBase := builderLogPathBase(rd.Number)
	body, _, found, err := d.RoundFileGet(record.ID, logBase)
	if err != nil {
		return nil, fmt.Errorf("dedupe: round file %s/%s: %w", record.Name, logBase, err)
	}
	if !found {
		return nil, nil
	}
	return lineSet(record, logBase, body)
}

func lineSet(record db.Record, name string, body []byte) (map[string]bool, error) {
	lines, err := roundFileLines(name, body)
	if err != nil {
		return nil, fmt.Errorf("dedupe: split %s/%s: %w", record.Name, name, err)
	}
	set := make(map[string]bool, len(lines))
	for _, line := range lines {
		set[string(line)] = true
	}
	return set, nil
}

func builderLogPathBase(round int) string {
	return filepath.Base(memberStore.BuilderLogPath("x", round))
}

// runnerStreamPathBase is the round_file name a round sealed after the rename
// carries.
func runnerStreamPathBase(round int) string {
	return filepath.Base(memberStore.RunnerStreamPath("x", round))
}

// builderStreamPathBase is the pre-rename stream name: a round sealed before
// the rename carries it, and a reader must still find it.
func builderStreamPathBase(round int) string {
	return filepath.Base(memberStore.BuilderStreamPath("x", round))
}

// sealedStream reads one round's sealed stream row under whichever name it was
// sealed with: the current name first, then the pre-rename name. It reports the
// name it was found under so callers can label the round file. A miss on both
// names is found=false with a nil error; a read error is returned wrapped.
func sealedStream(d *db.DB, record db.Record, round int) (name string, body []byte, found bool, err error) {
	for _, base := range []string{runnerStreamPathBase(round), builderStreamPathBase(round)} {
		body, _, found, err = d.RoundFileGet(record.ID, base)
		if err != nil {
			return "", nil, false, fmt.Errorf("dedupe: round file %s/%s: %w", record.Name, base, err)
		}
		if found {
			return base, body, true, nil
		}
	}
	return "", nil, false, nil
}

// deriveTranscripts re-derives a mirror round's transcript rows from the record's
// sealed round files, returning every candidate the identity rule allows: the
// stream rendered under each candidate kind, and the log whenever present.
func deriveTranscripts(d *db.DB, record db.Record, rd db.Round) ([][]db.TranscriptRecord, error) {
	var out [][]db.TranscriptRecord

	streamName, streamBody, found, err := sealedStream(d, record, rd.Number)
	if err != nil {
		return nil, err
	}
	if found {
		lines, lerr := roundFileLines(streamName, streamBody)
		if lerr != nil {
			return nil, fmt.Errorf("dedupe: split %s/%s: %w", record.Name, streamName, lerr)
		}
		kinds := transcriptKinds(record, rd)
		out = make([][]db.TranscriptRecord, 0, len(kinds)+1)
		for _, kind := range kinds {
			recs, _ := streamTranscriptRecords(kind, lines, 0)
			out = append(out, recs)
		}
	}

	logBase := builderLogPathBase(rd.Number)
	body, _, found, err := d.RoundFileGet(record.ID, logBase)
	if err != nil {
		return nil, fmt.Errorf("dedupe: round file %s/%s: %w", record.Name, logBase, err)
	}
	if found {
		lines, lerr := roundFileLines(logBase, body)
		if lerr != nil {
			return nil, fmt.Errorf("dedupe: split %s/%s: %w", record.Name, logBase, lerr)
		}
		out = append(out, logOnlyTranscriptRecords(lines, 0))
	}

	return out, nil
}

// transcriptKinds is the harness kind each stream candidate is rendered under: the
// mirror round's own Harness, then the record's decoded Builder.Kind, each
// once.
func transcriptKinds(record db.Record, rd db.Round) []string {
	kinds := make([]string, 0, 2)
	seen := make(map[string]bool, 2)
	add := func(kind string) {
		if seen[kind] {
			return
		}
		seen[kind] = true
		kinds = append(kinds, kind)
	}

	if rd.Harness != nil {
		add(*rd.Harness)
	}
	var b store.Binding
	if err := json.Unmarshal([]byte(record.JSON), &b); err == nil {
		add(b.Builder.Kind)
	}
	return kinds
}

// roundFileLines splits a sealed round file's body exactly as readAppendOnly
// splits a live member, reusing that reader rather than a second splitter that
// could drift from it.
func roundFileLines(name string, body []byte) ([][]byte, error) {
	opener := func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	lines, _, _, _, err := readAppendOnly(opener, name, db.Cursor{}, false)
	if err != nil {
		return nil, err
	}
	return lines, nil
}

// transcriptRowsEqual compares seq, record JSON, rendered text and timestamp per
// index. Ids are ignored -- ingest mints a fresh ULID per row.
func transcriptRowsEqual(want, got []db.TranscriptRecord) bool {
	if len(want) != len(got) {
		return false
	}
	for i := range want {
		if want[i].Seq != got[i].Seq ||
			want[i].RecordJSON != got[i].RecordJSON ||
			want[i].Rendered != got[i].Rendered ||
			!timePtrEqual(want[i].TS, got[i].TS) {
			return false
		}
	}
	return true
}

func timePtrEqual(a, b *time.Time) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	default:
		return a.Equal(*b)
	}
}

// dedupeKVKey is the kv key a finished removal is recorded under. Its presence
// makes the removal one-time: a run that fails on the backup leaves no key, so the
// next daemon start retries.
const dedupeKVKey = "mirror-dedupe.v2"

// DedupeMirrorOnce removes the mirror rows DedupeMirror proves duplicate of a
// round_file, once per database and only after a full backup. It never half-does
// the work: an existing kv row means an earlier run finished and ran is false; an
// empty plan writes the stats and returns with no backup; otherwise the database is
// backed up first (a failure returns the error with no deletes and no kv row), one
// transaction deletes every planned row and writes the stats, and a VACUUM failure
// is recorded in stats.VacuumErr rather than returned, since the rows are gone
// either way.
func DedupeMirrorOnce(d *db.DB, backupDir string, renames []legacy.Prefix, now time.Time) (stats DedupeStats, ran bool, err error) {
	if _, ok, kerr := d.KVGet(dedupeKVKey); kerr != nil {
		return DedupeStats{}, false, fmt.Errorf("dedupe: kv get %s: %w", dedupeKVKey, kerr)
	} else if ok {
		return DedupeStats{}, false, nil
	}

	plan, err := DedupeMirror(d, renames)
	if err != nil {
		return DedupeStats{}, false, err
	}
	stats = plan.stats
	stats.DoneAt = now

	if len(plan.artifactIDs) == 0 && len(plan.transcriptOwners) == 0 {
		if err := putDedupeStats(d, stats); err != nil {
			return DedupeStats{}, false, err
		}
		return stats, true, nil
	}

	backupPath := filepath.Join(backupDir, "relevo.db.pre-dedupe-"+now.UTC().Format("20060102-150405"))
	if berr := d.BackupTo(backupPath); berr != nil {
		return DedupeStats{}, false, berr
	}
	stats.BackupPath = backupPath

	if err := d.Tx(func(tx *db.Tx) error {
		for _, id := range plan.artifactIDs {
			if derr := tx.DeleteArtifact(id); derr != nil {
				return derr
			}
		}
		for _, owner := range plan.transcriptOwners {
			if _, derr := tx.DeleteRoundTranscript(owner); derr != nil {
				return derr
			}
		}
		value, merr := json.Marshal(stats)
		if merr != nil {
			return fmt.Errorf("dedupe: marshal stats: %w", merr)
		}
		return tx.KVPut(dedupeKVKey, value)
	}); err != nil {
		return DedupeStats{}, false, err
	}

	if verr := d.Vacuum(); verr != nil {
		stats.VacuumErr = verr.Error()
	}
	return stats, true, nil
}

func putDedupeStats(d *db.DB, stats DedupeStats) error {
	value, err := json.Marshal(stats)
	if err != nil {
		return fmt.Errorf("dedupe: marshal stats: %w", err)
	}
	if err := d.KVPut(dedupeKVKey, value); err != nil {
		return fmt.Errorf("dedupe: kv put %s: %w", dedupeKVKey, err)
	}
	return nil
}
