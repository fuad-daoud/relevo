package ingest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
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
	db.ArtifactPrompt:   "prompt.md",
	db.ArtifactReport:   "report.md",
	db.ArtifactDiff:     "diff.patch",
	db.ArtifactDrift:    "drift.patch",
	db.ArtifactQuestion: "question.md",
}

// promptArtifactLegacySuffix is the round-file name a prompt was sealed under
// before the rename: a round sealed before it still answers that name.
const promptArtifactLegacySuffix = "plan.md"

// dedupeArtifactKinds is every kind DedupeMirror examines, in order: those with a
// round-file counterpart, plus answer. gate_log, ask and findings are excluded.
var dedupeArtifactKinds = []string{
	db.ArtifactPrompt,
	db.ArtifactReport,
	db.ArtifactDiff,
	db.ArtifactDrift,
	db.ArtifactQuestion,
	db.ArtifactAnswer,
}

// dedupeRoundFileBases is the round-file basenames kind is sealed under: both
// names for a prompt, which a round may have sealed under either, and one for
// every other kind. A kind with no round file yields none.
func dedupeRoundFileBases(kind string, number int) []string {
	suffix, ok := dedupeArtifactSuffix[kind]
	if !ok {
		return nil
	}
	prefix := fmt.Sprintf("%03d-", number)
	bases := []string{prefix + suffix}
	if kind == db.ArtifactPrompt {
		bases = append(bases, prefix+promptArtifactLegacySuffix)
	}
	return bases
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
// every row is covered by the record's sealed lines (streamLinesCover). MasterMind
// transcripts are never examined.
func DedupeMirror(d *db.DB) (dedupePlan, error) {
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
		if err := planBinding(&plan, d, b, record); err != nil {
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

func planBinding(plan *dedupePlan, d *db.DB, b db.BindingRow, record db.Record) error {
	rounds, err := d.Rounds(b.ID)
	if err != nil {
		return fmt.Errorf("dedupe: rounds of %s: %w", b.Name, err)
	}
	for _, rd := range rounds {
		if err := planArtifacts(plan, d, record, rd); err != nil {
			return err
		}
		if err := planTranscript(plan, d, b, record, rd); err != nil {
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
		for _, base := range dedupeRoundFileBases(kind, rd.Number) {
			body, _, found, ferr := d.RoundFileGet(record.ID, base)
			if ferr != nil {
				return fmt.Errorf("dedupe: round file %s/%s: %w", record.Name, base, ferr)
			}
			if found && sha256Hex(body) == a.SHA256 && int64(len(body)) == a.Bytes {
				duplicate = true
				break
			}
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
func planTranscript(plan *dedupePlan, d *db.DB, b db.BindingRow, record db.Record, rd db.Round) error {
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
			plan.removeTranscript(rd.ID, len(rows), false)
			return nil
		}
	}

	covered, err := streamLinesCover(d, record, rd, rows)
	if err != nil {
		return err
	}
	if covered {
		plan.removeTranscript(rd.ID, len(rows), true)
		return nil
	}

	plan.stats.TranscriptRoundsKept++
	return nil
}

func (p *dedupePlan) removeTranscript(ownerID string, rows int, byStreamLines bool) {
	p.transcriptOwners = append(p.transcriptOwners, ownerID)
	p.stats.TranscriptRoundsDeleted++
	p.stats.TranscriptRowsDeleted += rows
	if byStreamLines {
		p.stats.TranscriptRoundsByStreamLines++
	}
}

// streamLinesCover reports whether every row is proven by the record's sealed
// round files. Three cases, in order: a row with record JSON is covered when it is
// a line of the sealed builder stream; a row with neither record JSON nor rendered
// text is a blank stream line and holds nothing; a row with rendered text but no
// record JSON is covered when it is a line of the sealed builder log verbatim. A
// missing stream is not fatal. rows must be non-empty.
func streamLinesCover(d *db.DB, record db.Record, rd db.Round, rows []db.TranscriptRecord) (covered bool, err error) {
	streamName, streamBody, found, err := sealedStream(d, record, rd.Number)
	if err != nil {
		return false, err
	}
	var stream map[string]bool
	if found {
		stream, err = lineSet(record, streamName, streamBody)
		if err != nil {
			return false, err
		}
	}

	var log map[string]bool
	logLoaded := false
	for _, row := range rows {
		if row.RecordJSON != "" {
			if !streamCoversRecord(row.RecordJSON, stream) {
				return false, nil
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
				return false, err
			}
		}
		if !log[row.Rendered] {
			return false, nil
		}
	}
	return true, nil
}

// streamCoversRecord reports whether a row's record JSON is a line of the sealed
// builder stream.
func streamCoversRecord(jsonLine string, stream map[string]bool) bool {
	return stream[jsonLine]
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
	opener := func() (io.ReadSeekCloser, error) { return seekCloser{bytes.NewReader(body)}, nil }
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
// transaction deletes every planned row, the marker is written last, and a VACUUM
// failure is recorded in stats.VacuumErr rather than returned, since the rows are
// gone either way.
//
// The marker is machine-local and the rows are shared history, so they are two
// writes against two files rather than one transaction: the marker goes to the
// local file the split put it in, because a shared copy would tell another
// installation's pass that this machine's rows were already deduped.
func DedupeMirrorOnce(d *db.DB, backupDir string, now time.Time) (stats DedupeStats, ran bool, err error) {
	local := d.LocalOrSelf()
	if _, ok, kerr := local.KVGet(dedupeKVKey); kerr != nil {
		return DedupeStats{}, false, fmt.Errorf("dedupe: kv get %s: %w", dedupeKVKey, kerr)
	} else if ok {
		return DedupeStats{}, false, nil
	}

	plan, err := DedupeMirror(d)
	if err != nil {
		return DedupeStats{}, false, err
	}
	stats = plan.stats
	stats.DoneAt = now

	if len(plan.artifactIDs) == 0 && len(plan.transcriptOwners) == 0 {
		if err := putDedupeStats(local, stats); err != nil {
			return stats, false, err
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
		return nil
	}); err != nil {
		return DedupeStats{}, false, err
	}

	if verr := d.Vacuum(); verr != nil {
		stats.VacuumErr = verr.Error()
	}
	// Last, so a failure anywhere before it leaves the next pass retrying: the
	// rows are already gone, so the retry plans nothing and writes the marker.
	// An earlier pass's marker is never re-stamped, because the pass returns
	// before here when it finds one.
	if err := putDedupeStats(local, stats); err != nil {
		return stats, false, err
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
