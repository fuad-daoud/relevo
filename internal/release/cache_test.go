package release

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

func testKV(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// TestLoadMissingAndMalformed pins that neither a missing nor an unreadable
// cache is an error: a cache that cannot be read must only fail to inform.
func TestLoadMissingAndMalformed(t *testing.T) {
	tests := []struct {
		name    string
		content string
		write   bool
	}{
		{name: "missing row"},
		{name: "wrong shape", write: true, content: `{"latest": 7}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			kv := testKV(t)
			if tc.write {
				if err := kv.KVPut(cacheKey, []byte(tc.content)); err != nil {
					t.Fatalf("put cache fixture: %v", err)
				}
			}

			c, ok, err := Load(kv)
			if err != nil {
				t.Fatalf("Load = error %v, want nil: a corrupt cache must never fail a caller", err)
			}
			if ok {
				t.Errorf("Load ok = true, want false for %s", tc.name)
			}
			if c != (Cache{}) {
				t.Errorf("Load = %+v, want the zero Cache", c)
			}
		})
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	kv := testKV(t)
	want := Cache{
		Latest:    "v0.7.0",
		CheckedAt: time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC),
		Source:    DefaultEndpoint,
	}
	if err := Save(kv, want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, ok, err := Load(kv)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !ok {
		t.Fatal("Load ok = false after Save, want true")
	}
	if got.Latest != want.Latest || got.Source != want.Source || !got.CheckedAt.Equal(want.CheckedAt) {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}

	if _, ok, err := kv.KVGet("release-check"); err != nil || !ok {
		t.Errorf("KVGet(release-check) = (_, %v, %v), want the row", ok, err)
	}
}

// TestStaleTTL is a fake clock either side of TTL.
func TestStaleTTL(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		checkedAt time.Time
		ok        bool
		want      bool
	}{
		{name: "no cache at all", ok: false, want: true},
		{name: "TTL less one second", checkedAt: now.Add(-(TTL - time.Second)), ok: true, want: false},
		{name: "exactly TTL", checkedAt: now.Add(-TTL), ok: true, want: false},
		{name: "TTL plus one second", checkedAt: now.Add(-(TTL + time.Second)), ok: true, want: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Stale(Cache{Latest: "v0.7.0", CheckedAt: tc.checkedAt}, tc.ok, now, TTL)
			if got != tc.want {
				t.Errorf("Stale(checkedAt %s, ok %v) = %v, want %v", tc.checkedAt, tc.ok, got, tc.want)
			}
		})
	}
}
