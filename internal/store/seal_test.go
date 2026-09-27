package store

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/legacy"
	"github.com/fuad-daoud/relevo/internal/spawn"
)

// TestSealableTable pins the pure predicate: a closed, quiet round is
// sealable, each in-flight reader holds it back, and the latest closed round
// is held back too unless the binding is DONE.
func TestSealableTable(t *testing.T) {
	base := newBinding("webshop", "/home/dev/webshop")
	base.Round = 5

	cases := []struct {
		name    string
		b       Binding
		round   int
		drained bool
		want    bool
	}{
		{"closed and quiet", base, 3, false, true},
		{"the open round", base, 5, false, false},
		{"a future round", base, 6, false, false},
		{"the latest closed round is kept for its readers", base, 4, true, false},
		{"a done binding seals its latest closed round", withBinding(base, func(b *Binding) { b.State = StateDone }), 4, true, true},
		{"done is not a blocker", withBinding(base, func(b *Binding) { b.State = StateDone }), 3, false, true},
		{"stream drain of the round is not drained", withBinding(base, func(b *Binding) {
			b.Builder.StreamRound = 3
			b.Builder.PID = 4242
		}), 3, false, false},
		{"stream drain of the round is drained", withBinding(base, func(b *Binding) {
			b.Builder.StreamRound = 3
			b.Builder.PID = 4242
		}), 3, true, true},
		{"a PID==0 drain with the trailer absent", withBinding(base, func(b *Binding) {
			b.Builder.StreamRound = 3
			b.Builder.PID = 0
		}), 3, false, false},
		{"a PID==0 drain with the trailer present", withBinding(base, func(b *Binding) {
			b.Builder.StreamRound = 3
			b.Builder.PID = 0
		}), 3, true, true},
		{"a drain of another round holds nothing back", withBinding(base, func(b *Binding) {
			b.Builder.StreamRound = 2
			b.Builder.PID = 4242
		}), 3, false, true},
		{"a running consult of the round", withBinding(base, func(b *Binding) {
			b.Consults = []Consult{{ID: "aabbccdd", Round: 3, State: ConsultRunning}}
		}), 3, false, false},
		{"a spawning consult of the round", withBinding(base, func(b *Binding) {
			b.Consults = []Consult{{ID: "aabbccdd", Round: 3, State: ConsultSpawning}}
		}), 3, false, false},
		{"a done consult of the round", withBinding(base, func(b *Binding) {
			b.Consults = []Consult{{ID: "aabbccdd", Round: 3, State: ConsultDone}}
		}), 3, false, true},
		{"a silent consult of the round", withBinding(base, func(b *Binding) {
			b.Consults = []Consult{{ID: "aabbccdd", Round: 3, State: ConsultSilent}}
		}), 3, false, true},
		{"a consult of another round", withBinding(base, func(b *Binding) {
			b.Consults = []Consult{{ID: "aabbccdd", Round: 2, State: ConsultRunning}}
		}), 3, false, true},
		{"a gate run for the round", withBinding(base, func(b *Binding) {
			b.GateRun = &GateRun{PID: 7, Round: 3}
		}), 3, false, false},
		{"a gate run for another round", withBinding(base, func(b *Binding) {
			b.GateRun = &GateRun{PID: 7, Round: 2}
		}), 3, false, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Sealable(tc.b, tc.round, tc.drained); got != tc.want {
				t.Errorf("Sealable(%s, %d, %v) = %v, want %v", tc.b.Name, tc.round, tc.drained, got, tc.want)
			}
		})
	}
}

// streamPathForCase is the stream name a TestStreamDrained case writes under:
// the pre-rename name for a legacy case, the current name otherwise.
func streamPathForCase(s *Store, b Binding, drained int, legacy bool) string {
	if legacy {
		return s.BuilderStreamPath(b.Name, drained)
	}
	return s.RunnerStreamPath(b.Name, drained)
}

// TestStreamDrained pins the drain test Sealable's stream blocker takes:
// each case supplies the stream's bytes, the cursor and the file's age.
func TestStreamDrained(t *testing.T) {
	const drained = 2
	payload := `{"type":"step","part":{"time":{"end":1}}}`
	relevoOnly := payload + "\n\n" + spawn.ExitTrailer + "0\n"
	legacyTrailer := payload + "\n\n" +
		legacy.RusageTrailer + "cpu_usec=1 mem_peak=2\n\n" +
		legacy.ExitTrailer + "0\n"
	atEndOfPayload := func(s string) int64 { return int64(bytes.Index([]byte(s), []byte("}}}"))) }
	atLegacyRusage := func(s string) int64 { return int64(bytes.Index([]byte(s), []byte(legacy.RusageTrailer))) }

	cases := []struct {
		name   string
		legacy bool // true: write file under the old name (BuilderStreamPath)
		round  int
		body   string // "" leaves no stream file
		offset func(string) int64
		age    time.Duration
		want   bool
	}{
		{"a missing stream is drained", false, drained, "", nil, 0, true},
		{"another round is vacuously drained", false, drained - 1, relevoOnly, nil, 0, true},
		{"the cursor is inside the payload", false, drained, relevoOnly, func(string) int64 { return 1 }, 0, false},
		{"the cursor is at the stream's size", false, drained, relevoOnly, func(s string) int64 { return int64(len(s)) }, 0, true},
		{"no trailer, cursor at EOF", false, drained, "still flushing\n", func(s string) int64 { return int64(len(s)) }, 0, false},
		{"a legacy trailer with only trailer bytes left", false, drained, legacyTrailer, atLegacyRusage, 0, true},
		{"a legacy trailer with payload bytes left", false, drained, legacyTrailer, nil, 0, false},
		{"a just-written stream with the trailer", false, drained, legacyTrailer, atEndOfPayload, 0, false},
		{"a stale stream with the trailer", false, drained, legacyTrailer, atEndOfPayload, staleStreamAfter + time.Minute, true},
		{"a stale stream with no trailer", false, drained, payload + "\n", nil, staleStreamAfter + time.Minute, false},
		{"a pre-rename stream, cursor inside the payload", true, drained, relevoOnly, func(string) int64 { return 1 }, 0, false},
		{"a pre-rename stream with the trailer", true, drained, relevoOnly, func(s string) int64 { return int64(len(s)) }, 0, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := New(t.TempDir())
			b := newBinding("webshop", "/home/dev/webshop")
			b.Round = 4
			b.Builder.StreamRound = drained
			if err := s.Save(b); err != nil {
				t.Fatalf("Save: %v", err)
			}

			if tc.body != "" {
				path := streamPathForCase(s, b, drained, tc.legacy)
				if err := os.WriteFile(path, []byte(tc.body), bindingFileMode); err != nil {
					t.Fatalf("write stream: %v", err)
				}
				if tc.age > 0 {
					old := time.Now().Add(-tc.age)
					if err := os.Chtimes(path, old, old); err != nil {
						t.Fatal(err)
					}
				}
				if tc.offset != nil {
					b.Builder.StreamOffset = tc.offset(tc.body)
				}
			}

			if got := s.StreamDrained(b, tc.round); got != tc.want {
				t.Errorf("StreamDrained(round %d) = %v, want %v", tc.round, got, tc.want)
			}
		})
	}
}

// TestSealRoundMovesOneRound pins the seal itself: exactly round 3's round
// files and consult files become rows and leave the directory, other rounds
// and non-NNN files stay, ReadFile is unchanged, and a second seal is a no-op.
func TestSealRoundMovesOneRound(t *testing.T) {
	s := New(t.TempDir())
	b := newBinding("webshop", "/home/dev/webshop")
	b.Round = 4
	if err := s.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	sealed, kept := writeSealFixtures(t, s)

	rounds, err := s.RoundsOnDisk("webshop")
	if err != nil {
		t.Fatalf("RoundsOnDisk: %v", err)
	}
	if len(rounds) != 3 || rounds[0] != 2 || rounds[1] != 3 || rounds[2] != 4 {
		t.Fatalf("RoundsOnDisk = %v, want [2 3 4]", rounds)
	}

	var n int
	if err := s.WithLock(func(tx *Tx) error {
		var err error
		n, err = tx.SealRound("webshop", 3)
		return err
	}); err != nil {
		t.Fatalf("SealRound: %v", err)
	}
	if n != len(sealed) {
		t.Errorf("SealRound sealed %d files, want %d", n, len(sealed))
	}
	checkSealedFiles(t, s, sealed, kept)

	// A second seal of the same round finds nothing: the files are gone.
	if err := s.WithLock(func(tx *Tx) error {
		var err error
		n, err = tx.SealRound("webshop", 3)
		return err
	}); err != nil {
		t.Fatalf("SealRound again: %v", err)
	}
	if n != 0 {
		t.Errorf("second SealRound sealed %d files, want 0", n)
	}

	// RoundFiles sees both halves: the sealed names and what is still there.
	names, err := s.RoundFiles("webshop")
	if err != nil {
		t.Fatalf("RoundFiles: %v", err)
	}
	if !containsAll(names, kept) {
		t.Errorf("RoundFiles = %v, missing one of %v", names, keys(kept))
	}
	if !containsAll(names, sealed) {
		t.Errorf("RoundFiles = %v, missing a sealed name of %v", names, keys(sealed))
	}
}

func writeSealFixtures(t *testing.T, s *Store) (sealed, kept map[string][]byte) {
	t.Helper()
	sealed = map[string][]byte{
		"003-plan.md":              []byte("# round 3 plan\n"),
		"003-report.md":            []byte("report with a \x00 binary byte\n"),
		"003-done":                 nil,
		"003-builder.log":          []byte("builder stderr\n"),
		"003-builder.jsonl":        []byte("{}\n"),
		"003-gate.log":             []byte("gate passed\n"),
		"003-aabbccdd-ask.md":      []byte("the question\n"),
		"003-aabbccdd-findings.md": []byte("the findings\n"),
	}
	kept = map[string][]byte{
		"002-plan.md": []byte("round 2\n"),
		"004-plan.md": []byte("the open round\n"),
		"notes.txt":   []byte("not a round file\n"),
	}
	for _, files := range []map[string][]byte{sealed, kept} {
		for name, body := range files {
			if err := os.WriteFile(filepath.Join(s.Dir("webshop"), name), body, bindingFileMode); err != nil {
				t.Fatalf("write %s: %v", name, err)
			}
		}
	}
	return sealed, kept
}

func checkSealedFiles(t *testing.T, s *Store, sealed, kept map[string][]byte) {
	t.Helper()
	for name, body := range sealed {
		if _, err := os.Stat(filepath.Join(s.Dir("webshop"), name)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s is still on disk", name)
		}
		got, err := s.ReadFile(filepath.Join(s.Dir("webshop"), name))
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", name, err)
		}
		if !bytes.Equal(got, body) {
			t.Errorf("ReadFile(%s) = %q, want %q", name, got, body)
		}
	}
	for name := range kept {
		if _, err := os.Stat(filepath.Join(s.Dir("webshop"), name)); err != nil {
			t.Errorf("%s should have stayed on disk: %v", name, err)
		}
	}
}

// TestReadFileMissingStaysErrNotExist pins that an unsealed path still reports
// os.ReadFile's own error.
func TestReadFileMissingStaysErrNotExist(t *testing.T) {
	s := New(t.TempDir())
	b := newBinding("webshop", "/home/dev/webshop")
	b.Round = 2
	if err := s.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Never sealed: on disk not at all, in the database never.
	path := s.ReportPath("webshop", 1)
	if _, err := s.ReadFile(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ReadFile(%s) err = %v, want ErrNotExist", path, err)
	}
	if _, _, ok, err := s.StatFile(path); ok || !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("StatFile(%s) = (ok %v, err %v), want (false, ErrNotExist)", path, ok, err)
	}

	// A non-round basename, an unknown binding and a path outside the root are
	// all plain misses.
	for _, p := range []string{
		filepath.Join(s.Dir("webshop"), "bind.json.x"),
		filepath.Join(s.Dir("nobody"), "003-report.md"),
		filepath.Join(t.TempDir(), "webshop", "003-report.md"),
	} {
		if _, err := s.ReadFile(p); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("ReadFile(%s) err = %v, want ErrNotExist", p, err)
		}
	}
}
