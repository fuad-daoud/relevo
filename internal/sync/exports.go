package sync

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// KeyExportsHours is the one local row the hourly export counts live in: a
// JSON list of ExportHour, oldest first, capped at the newest maxExportHours.
// One row holds the whole record because the cap is by count, not by key, so
// nothing needs pruning by key.
const KeyExportsHours = "sync.exports.hours"

// IdleExportsWarn is the hourly idle-export count that earns a warning. The
// line is not zero because a person's own session hooks and config edits
// export a few entries with no round open.
const IdleExportsWarn = 50

// maxExportHours caps the hourly record: two days of hours is enough to say
// whether a machine keeps exporting with no round open, and the row stays
// small enough to rewrite on every attempt.
const maxExportHours = 48

// ExportHour is one UTC hour's export counts: every entry the attempts ending
// in that hour exported, and among them the ones exported while no round was
// open. A machine that exported nothing in an hour has no row for it.
type ExportHour struct {
	Hour     string `json:"hour"`
	Exported int    `json:"exported"`
	Idle     int    `json:"idle"`
}

func (h ExportHour) OverIdle() bool { return h.Idle > IdleExportsWarn }

// exportHourKey is the hour a.End falls in, UTC, so attempts on machines in
// any zone book the same hour.
func exportHourKey(t time.Time) string { return t.UTC().Format("2006-01-02T15") }

// NoteRoundsOpen records how many rounds are open now. It is nil-safe because
// attempts run on the sync goroutine while the daemon ticks. Zero counts only
// after the first note; until then the count is unknown, so a daemon that just
// started never books its first attempt as idle while a round may be open.
func (r *Runner) NoteRoundsOpen(n int) {
	if r == nil {
		return
	}
	r.roundsOpen.Store(int64(n) + 1)
}

// RoundsOpen returns the last noted open-round count. The second value is
// false until the first note, which is what keeps the first attempt out of
// the idle books.
func (r *Runner) RoundsOpen() (int, bool) {
	if r == nil {
		return 0, false
	}
	v := r.roundsOpen.Load()
	if v == 0 {
		return 0, false
	}
	return int(v) - 1, true
}

// recordExports books one attempt's exports into the hour holding end. An
// attempt that exported nothing books nothing new, so it cannot drop the
// hours already there.
func (r *Runner) recordExports(end time.Time, exported int) {
	if r == nil || r.Local == nil || exported <= 0 {
		return
	}
	hours := readExportHours(r.Local)
	key := exportHourKey(end)
	at := -1
	for i := range hours {
		if hours[i].Hour == key {
			at = i
		}
	}
	if at < 0 {
		hours = append(hours, ExportHour{Hour: key})
		at = len(hours) - 1
	}
	hours[at].Exported += exported
	if open, known := r.RoundsOpen(); known && open == 0 {
		hours[at].Idle += exported
	}
	if len(hours) > maxExportHours {
		hours = append([]ExportHour(nil), hours[len(hours)-maxExportHours:]...)
	}
	body, err := json.Marshal(hours)
	if err != nil {
		slog.Warn("sync: encode a marker", "key", KeyExportsHours, "err", err)
		return
	}
	if err := r.Local.KVPut(KeyExportsHours, body); err != nil {
		slog.Warn("sync: write a marker", "key", KeyExportsHours, "err", err)
	}
}

// readExportHours reads the hourly record. A row that will not parse reads
// as empty: the worst it costs is one window's counts, and a machine whose
// marker cannot be read must not stop booking.
func readExportHours(kv db.KV) []ExportHour {
	body, ok, err := kv.KVGet(KeyExportsHours)
	if err != nil {
		slog.Warn("sync: read the exports marker", "err", err)
		return nil
	}
	if !ok {
		return nil
	}
	var hours []ExportHour
	if err := json.Unmarshal(body, &hours); err != nil {
		slog.Warn("sync: read the exports marker", "err", err)
		return nil
	}
	return hours
}

// IdleExports returns the last complete hour's counts: the hour before now's UTC
// hour, because a partial hour cannot be compared with a per-hour threshold. It
// returns false when that hour has no row, which is a machine that exported
// nothing then: nothing to warn about, not an error. A row that will not parse
// reads as empty and is left for the next booking to replace: a read never writes.
func IdleExports(kv db.KV, now time.Time) (ExportHour, bool, error) {
	want := now.UTC().Add(-time.Hour).Format("2006-01-02T15")
	body, ok, err := kv.KVGet(KeyExportsHours)
	if err != nil {
		return ExportHour{}, false, fmt.Errorf("sync: exports marker: %w", err)
	}
	if !ok {
		return ExportHour{}, false, nil
	}
	var hours []ExportHour
	if err := json.Unmarshal(body, &hours); err != nil {
		slog.Warn("sync: read the exports marker", "err", err)
		return ExportHour{}, false, nil
	}
	for _, h := range hours {
		if h.Hour == want {
			return h, true, nil
		}
	}
	return ExportHour{}, false, nil
}
