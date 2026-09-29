package store

import (
	"os"
	"testing"
)

const streamPathRound = 1

// streamPathCase is one StreamPath resolution case: setup places the round's
// stream (or none) in a freshly seeded store, and wantNew says whether the
// resolver must answer with the new name.
type streamPathCase struct {
	name    string
	setup   func(t *testing.T, s *Store, binding string)
	wantNew bool
}

// streamPathCases builds the six resolution cases: neither name, each name
// alone on disk, each name alone as a sealed row, and both on disk.
func streamPathCases() []streamPathCase {
	body := []byte("stream data\n")
	write := func(t *testing.T, path string) {
		t.Helper()
		if err := os.WriteFile(path, body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	seal := func(t *testing.T, s *Store, binding, path string) {
		t.Helper()
		if err := s.WithLock(func(tx *Tx) error {
			return tx.PutRoundFile(binding, streamPathRound, path, body)
		}); err != nil {
			t.Fatal(err)
		}
	}
	disk := func(method func(*Store, string, int) string) func(*testing.T, *Store, string) {
		return func(t *testing.T, s *Store, binding string) { write(t, method(s, binding, streamPathRound)) }
	}
	row := func(method func(*Store, string, int) string) func(*testing.T, *Store, string) {
		return func(t *testing.T, s *Store, binding string) { seal(t, s, binding, method(s, binding, streamPathRound)) }
	}
	return []streamPathCase{
		{"neither name exists returns the new name", func(*testing.T, *Store, string) {}, true},
		{"new name on disk returns the new path", disk((*Store).RunnerStreamPath), true},
		{"only old name on disk returns the old path", disk((*Store).BuilderStreamPath), false},
		{"only new name as a sealed row returns the new path", row((*Store).RunnerStreamPath), true},
		{"only old name as a sealed row returns the old path", row((*Store).BuilderStreamPath), false},
		{"both on disk returns the new path", func(t *testing.T, s *Store, binding string) {
			write(t, s.RunnerStreamPath(binding, streamPathRound))
			write(t, s.BuilderStreamPath(binding, streamPathRound))
		}, true},
	}
}

// TestStreamPathResolvesTheRoundStream pins StreamPath's resolution rule: the
// new name wins when present, the old name is the fallback, and a fresh round
// gets the new name.
func TestStreamPathResolvesTheRoundStream(t *testing.T) {
	for _, tc := range streamPathCases() {
		t.Run(tc.name, func(t *testing.T) {
			s, binding := seedBinding(t)
			tc.setup(t, s, binding)
			want := s.BuilderStreamPath(binding, streamPathRound)
			if tc.wantNew {
				want = s.RunnerStreamPath(binding, streamPathRound)
			}
			if got := s.StreamPath(binding, streamPathRound); got != want {
				t.Errorf("StreamPath = %q, want %q", got, want)
			}
		})
	}
}
