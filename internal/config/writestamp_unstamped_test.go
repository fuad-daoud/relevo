package config

import "testing"

func TestMigrationRecordsNoWriteStamp(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	seedNamelessCandidates(t, s, `[{"harness":"claude","provider":"anthropic","model":"sonnet","roles":["builder"]}]`)

	if changed, err := s.EnsureCandidateNames(); err != nil || !changed {
		t.Fatalf("EnsureCandidateNames = %v, %v; want a write", changed, err)
	}
	if _, ok, err := s.db.ConfigWriteStamp("candidates/sonnet"); err != nil || ok {
		t.Errorf("migration stamped candidates/sonnet: ok=%v err=%v", ok, err)
	}

	if _, err := s.As("cli", "config set").Put(Candidates,
		[]byte(`[{"name":"sonnet","harness":"claude","provider":"anthropic","model":"opus","roles":["builder"]}]`)); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.db.ConfigWriteStamp("candidates/sonnet"); err != nil || !ok {
		t.Errorf("a person's edit left no stamp: ok=%v err=%v", ok, err)
	}
}
