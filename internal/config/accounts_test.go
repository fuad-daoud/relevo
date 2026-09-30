package config

import (
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/account"
)

func TestAccountsSectionRoundTrip(t *testing.T) {
	s := openStore(t)
	body := `[
		{"name":"work","harness":"claude","groups":["anthropic"],"config_dir":"/home/fuad/.claude-work"},
		{"name":"cp1","harness":"opencode","groups":["cline-pass"],"integration":"cline-pass","label":"ClinePass"},
		{"name":"cp2","harness":"opencode","groups":["cline-pass"],"integration":"cline-pass","label":"ClinePass 2"}
	]`

	if _, err := s.As("test", "seed accounts").Put(Accounts, []byte(body)); err != nil {
		t.Fatalf("Put(accounts): %v", err)
	}
	L, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(L.Accounts) != 3 {
		t.Fatalf("Accounts = %v, want three", L.Accounts)
	}
	pool := L.Accounts.Pool(account.OpenCode, "cline-pass")
	if len(pool) != 2 || pool[0].Name != "cp1" || pool[1].Name != "cp2" {
		t.Errorf("Pool = %v, want cp1 and cp2 in config order", pool)
	}
}

func TestLoadWarnsAboutAGroupNoCandidateUses(t *testing.T) {
	s := openStore(t)
	seedSections(t, s, map[Section]string{
		Candidates: `[{"harness":"claude","provider":"anthropic","model":"sonnet"}]`,
		Accounts:   `[{"name":"work","harness":"claude","groups":["athropic"],"config_dir":"/a"}]`,
	})

	L, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	found := false
	for _, w := range L.Warnings {
		if strings.Contains(w, "athropic") {
			found = true
		}
	}
	if !found {
		t.Errorf("Warnings = %v, want one naming the unused group", L.Warnings)
	}
}

func TestValidateRefusesAnUnknownAccountKind(t *testing.T) {
	_, err := Validate(Accounts, []byte(`[{"name":"main","harness":"agy","groups":["google"]}]`))
	if err == nil {
		t.Fatal("Validate(accounts) = nil error, want a refusal")
	}
}

func TestLoadRefusesRoundRobinForAnOpenCodeGroup(t *testing.T) {
	s := openStore(t)
	seedSections(t, s, map[Section]string{
		Accounts: `[{"name":"cp1","harness":"opencode","groups":["cline-pass"],"integration":"cline-pass","label":"ClinePass"}]`,
	})

	_, err := s.As("test", "seed policy").Put(Policy, []byte(`{"accounts":{"rotation":"round-robin"}}`))
	if err == nil {
		t.Fatal("Put(policy) = nil error, want the opencode round-robin refusal")
	}
	if !strings.Contains(err.Error(), "round-robin") {
		t.Errorf("Put error %q does not name round-robin", err.Error())
	}
}
