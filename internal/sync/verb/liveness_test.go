package verb

import (
	"testing"
	"time"
)

func TestExportDue(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		exported time.Time
		at       time.Time
		owed     bool
		want     bool
	}{
		{"nothing exported yet", time.Time{}, base, false, true},
		{"inside the export gap", base, base.Add(ExportDebounce - time.Second), false, false},
		{"at the export gap", base, base.Add(ExportDebounce), false, true},
		{"owed history inside the short gap", base, base.Add(OwedDebounce - time.Second), true, false},
		{"owed history at the short gap", base, base.Add(OwedDebounce), true, true},
		{"nothing owed at the short gap", base, base.Add(OwedDebounce), false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			l := Liveness{ExportedAt: c.exported}
			if got := l.ExportDue(c.at, c.owed); got != c.want {
				t.Errorf("ExportDue(%v, owed=%v) = %v, want %v", c.at.Sub(c.exported), c.owed, got, c.want)
			}
		})
	}
}

// TestBeginStampsOnlyAnExport pins that a pull-only attempt leaves the export
// stamp where it was: the gap measures from the last export, not the last pull.
func TestBeginStampsOnlyAnExport(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	var l Liveness
	l.Begin(base, true)
	if !l.ExportedAt.Equal(base) || !l.Export {
		t.Fatalf("an exporting Begin left ExportedAt %v, Export %v", l.ExportedAt, l.Export)
	}
	later := base.Add(time.Minute)
	l.Begin(later, false)
	if !l.StartedAt.Equal(later) || l.Export {
		t.Errorf("a pull-only Begin left StartedAt %v, Export %v", l.StartedAt, l.Export)
	}
	if !l.ExportedAt.Equal(base) {
		t.Errorf("a pull-only Begin moved ExportedAt to %v, want %v", l.ExportedAt, base)
	}
}
