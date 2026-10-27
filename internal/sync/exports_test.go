package sync

import (
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// exportsRunner returns a runner over a fresh local file, so a case books
// without touching the shared database.
func exportsRunner(t *testing.T) (*Runner, db.KV) {
	t.Helper()
	_, _, local := openSplit(t)
	return &Runner{Local: local}, local
}

// TestExportsInOneHourAccumulate pins that two attempts ending in the same
// hour add into one row rather than starting a row each.
func TestExportsInOneHourAccumulate(t *testing.T) {
	t.Parallel()

	r, _ := exportsRunner(t)
	end := time.Date(2026, time.October, 27, 17, 10, 0, 0, time.UTC)
	r.recordExports(end, 3)
	r.recordExports(end.Add(20*time.Minute), 4)

	hours := readExportHours(r.Local)
	if len(hours) != 1 {
		t.Fatalf("hours = %d rows, want 1", len(hours))
	}
	if hours[0].Hour != "2026-10-27T17" {
		t.Errorf("hour = %q, want the attempt's hour", hours[0].Hour)
	}
	if hours[0].Exported != 7 {
		t.Errorf("exported = %d, want 3 + 4", hours[0].Exported)
	}
}

// TestNewHourStartsNewRow pins that an attempt ending in another hour starts
// its own row, so the rate keeps one count per hour.
func TestNewHourStartsNewRow(t *testing.T) {
	t.Parallel()

	r, _ := exportsRunner(t)
	r.recordExports(time.Date(2026, time.October, 27, 17, 50, 0, 0, time.UTC), 3)
	r.recordExports(time.Date(2026, time.October, 27, 18, 5, 0, 0, time.UTC), 4)

	hours := readExportHours(r.Local)
	if len(hours) != 2 {
		t.Fatalf("hours = %d rows, want 2", len(hours))
	}
	if hours[0].Hour != "2026-10-27T17" || hours[1].Hour != "2026-10-27T18" {
		t.Errorf("hours = %q, %q, want oldest first", hours[0].Hour, hours[1].Hour)
	}
	if hours[0].Exported != 3 || hours[1].Exported != 4 {
		t.Errorf("exported = %d, %d, want 3, 4", hours[0].Exported, hours[1].Exported)
	}
}

// TestExportHoursCapDropsOldest pins the cap: the record keeps the newest 48
// hours and drops the oldest, so a machine that syncs for months keeps a
// two-day window rather than a growing row.
func TestExportHoursCapDropsOldest(t *testing.T) {
	t.Parallel()

	r, _ := exportsRunner(t)
	base := time.Date(2026, time.August, 1, 0, 30, 0, 0, time.UTC)
	for i := range 61 {
		r.recordExports(base.Add(time.Duration(i)*time.Hour), 1)
	}

	hours := readExportHours(r.Local)
	if len(hours) != 48 {
		t.Fatalf("hours = %d rows, want 48", len(hours))
	}
	if hours[0].Hour != exportHourKey(base.Add(13*time.Hour)) {
		t.Errorf("oldest hour = %q, want the 14th booked hour", hours[0].Hour)
	}
	if hours[47].Hour != exportHourKey(base.Add(60*time.Hour)) {
		t.Errorf("newest hour = %q, want the last booked hour", hours[47].Hour)
	}
}

// TestIdleBooksWhenNoRoundOpen pins that an attempt with a known zero count
// books into both columns.
func TestIdleBooksWhenNoRoundOpen(t *testing.T) {
	t.Parallel()

	r, _ := exportsRunner(t)
	r.NoteRoundsOpen(0)
	r.recordExports(time.Date(2026, time.October, 27, 17, 10, 0, 0, time.UTC), 5)

	hours := readExportHours(r.Local)
	if len(hours) != 1 {
		t.Fatalf("hours = %d rows, want 1", len(hours))
	}
	if hours[0].Exported != 5 || hours[0].Idle != 5 {
		t.Errorf("row = %+v, want exported and idle both 5", hours[0])
	}
}

// TestUnknownCountBooksExportedOnly pins that a runner nobody noted yet is
// not idle: its attempts book exported only, so a daemon that just started
// never books its first attempt as idle while a round may be open.
func TestUnknownCountBooksExportedOnly(t *testing.T) {
	t.Parallel()

	r, _ := exportsRunner(t)
	if _, known := r.RoundsOpen(); known {
		t.Fatal("a fresh runner's count is already known")
	}
	r.recordExports(time.Date(2026, time.October, 27, 17, 10, 0, 0, time.UTC), 5)

	hours := readExportHours(r.Local)
	if len(hours) != 1 {
		t.Fatalf("hours = %d rows, want 1", len(hours))
	}
	if hours[0].Exported != 5 || hours[0].Idle != 0 {
		t.Errorf("row = %+v, want exported 5 and idle 0", hours[0])
	}
}

// TestOpenRoundsBookExportedOnly pins that an attempt with rounds open books
// exported only: something is running, so nothing here is idle.
func TestOpenRoundsBookExportedOnly(t *testing.T) {
	t.Parallel()

	r, _ := exportsRunner(t)
	r.NoteRoundsOpen(2)
	r.recordExports(time.Date(2026, time.October, 27, 17, 10, 0, 0, time.UTC), 5)

	hours := readExportHours(r.Local)
	if len(hours) != 1 {
		t.Fatalf("hours = %d rows, want 1", len(hours))
	}
	if hours[0].Exported != 5 || hours[0].Idle != 0 {
		t.Errorf("row = %+v, want exported 5 and idle 0", hours[0])
	}
}

// TestIdleExportsReturnsLastCompleteHour pins that the reader returns the
// hour before now's UTC hour, not the current one: a partial hour cannot be
// compared with a per-hour threshold.
func TestIdleExportsReturnsLastCompleteHour(t *testing.T) {
	t.Parallel()

	r, local := exportsRunner(t)
	r.NoteRoundsOpen(0)
	r.recordExports(time.Date(2026, time.October, 27, 15, 10, 0, 0, time.UTC), 5)
	r.recordExports(time.Date(2026, time.October, 27, 16, 5, 0, 0, time.UTC), 7)
	r.recordExports(time.Date(2026, time.October, 27, 17, 5, 0, 0, time.UTC), 9)

	got, ok, err := IdleExports(local, time.Date(2026, time.October, 27, 17, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("IdleExports: %v", err)
	}
	if !ok {
		t.Fatal("IdleExports found no row for the last complete hour")
	}
	if got.Hour != "2026-10-27T16" || got.Exported != 7 || got.Idle != 7 {
		t.Errorf("IdleExports = %+v, want the 16 o'clock row with 7 and 7", got)
	}
}

// TestIdleExportsMissingHourIsFalse pins that a machine with no row for the
// last complete hour reads as nothing to warn about, not an error.
func TestIdleExportsMissingHourIsFalse(t *testing.T) {
	t.Parallel()

	_, local := exportsRunner(t)
	if _, ok, err := IdleExports(local, time.Date(2026, time.October, 27, 17, 30, 0, 0, time.UTC)); err != nil || ok {
		t.Errorf("IdleExports on an empty record = (%v, %v), want (false, nil)", ok, err)
	}

	r, _ := exportsRunner(t)
	r.recordExports(time.Date(2026, time.October, 27, 12, 5, 0, 0, time.UTC), 7)
	if _, ok, err := IdleExports(r.Local, time.Date(2026, time.October, 27, 17, 30, 0, 0, time.UTC)); err != nil || ok {
		t.Errorf("IdleExports without the wanted hour = (%v, %v), want (false, nil)", ok, err)
	}
}

// TestOverIdleThreshold pins the warning line: 50 idle exports are a person's
// own hooks and edits, 51 are a machine exporting with no round open.
func TestOverIdleThreshold(t *testing.T) {
	t.Parallel()

	if (ExportHour{Idle: 50}).OverIdle() {
		t.Error("OverIdle at 50 idle exports, want false")
	}
	if !(ExportHour{Idle: 51}).OverIdle() {
		t.Error("OverIdle at 51 idle exports, want true")
	}
}

// TestMalformedExportsRowReadsEmpty pins that a row that will not parse
// reads as empty without the reader writing, and that the next booking
// replaces it: one bad write must not wedge every later read.
func TestMalformedExportsRowReadsEmpty(t *testing.T) {
	t.Parallel()

	sharedPath, _, local := openSplit(t)
	raw, err := db.OpenRaw(db.SplitPath(sharedPath))
	if err != nil {
		t.Fatalf("OpenRaw: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	// KVPut refuses invalid JSON, so the bad row goes in under it: a write
	// from before the validation, which is exactly what must not wedge us.
	if _, err := raw.Exec(`INSERT OR REPLACE INTO kv (key, value_json, updated_at) VALUES (?, ?, ?)`,
		KeyExportsHours, `{bad`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("seed a bad row: %v", err)
	}
	if _, ok, err := IdleExports(local, time.Date(2026, time.October, 27, 17, 30, 0, 0, time.UTC)); err != nil || ok {
		t.Errorf("IdleExports on a bad row = (%v, %v), want (false, nil)", ok, err)
	}
	var raw0 string
	if err := raw.QueryRow(`SELECT value_json FROM kv WHERE key = ?`, KeyExportsHours).Scan(&raw0); err != nil {
		t.Fatalf("read the bad row back: %v", err)
	}
	if raw0 != `{bad` {
		t.Errorf("the reader wrote the row: %s, want it left as {bad", raw0)
	}
	r := &Runner{Local: local}
	r.recordExports(time.Date(2026, time.October, 27, 17, 40, 0, 0, time.UTC), 3)
	got, ok, err := IdleExports(local, time.Date(2026, time.October, 27, 18, 5, 0, 0, time.UTC))
	if err != nil || !ok || got.Exported != 3 {
		t.Errorf("after a booking IdleExports = (%+v, %v, %v), want the 17 o'clock row with 3 exported", got, ok, err)
	}
}

// TestRecordAttemptBooksExports pins the booking's way in: an attempt that
// exported books its counts, and one that exported nothing leaves no row.
func TestRecordAttemptBooksExports(t *testing.T) {
	t.Parallel()

	r, local := exportsRunner(t)
	r.NoteRoundsOpen(0)
	r.RecordAttempt(time.Now(), SteadyResult{Exported: 5})

	hours := readExportHours(local)
	if len(hours) != 1 {
		t.Fatalf("hours = %d rows, want 1", len(hours))
	}
	if hours[0].Hour != exportHourKey(time.Now()) {
		t.Errorf("hour = %q, want the current hour", hours[0].Hour)
	}
	if hours[0].Exported != 5 || hours[0].Idle != 5 {
		t.Errorf("row = %+v, want exported and idle both 5", hours[0])
	}

	quiet, qlocal := exportsRunner(t)
	quiet.RecordAttempt(time.Now(), SteadyResult{Exported: 0})
	if _, ok, err := qlocal.KVGet(KeyExportsHours); err != nil || ok {
		t.Errorf("an attempt that exported nothing wrote a row: ok=%v err=%v", ok, err)
	}
}
