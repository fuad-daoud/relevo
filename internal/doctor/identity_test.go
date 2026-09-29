package doctor

import (
	"strings"
	"testing"
)

// TestGitIdentityCheck pins the row rule: no row without a server or
// outside a repo, ok when both keys resolve, and a warn naming exactly
// the key(s) a remote add would refuse over.
func TestGitIdentityCheck(t *testing.T) {
	cases := []gitIdentityCase{
		{
			name:    "no servers gives no row",
			in:      GitIdentityInput{InRepo: true, Name: "Ada Lovelace", Email: "ada@example.com"},
			wantRow: false,
		},
		{
			name:    "not in a repo gives no row",
			in:      GitIdentityInput{HasServers: true, Name: "Ada Lovelace", Email: "ada@example.com"},
			wantRow: false,
		},
		{
			name:         "both set is ok",
			in:           GitIdentityInput{HasServers: true, InRepo: true, Name: "Ada Lovelace", Email: "ada@example.com"},
			wantRow:      true,
			wantSeverity: SevOK,
			wantDetail:   "Ada Lovelace <ada@example.com>",
		},
		{
			name:           "email missing warns naming user.email",
			in:             GitIdentityInput{HasServers: true, InRepo: true, Name: "Ada Lovelace"},
			wantRow:        true,
			wantSeverity:   SevWarn,
			detailContains: []string{"user.email"},
			detailMissing:  []string{"user.name"},
			wantFix:        true,
		},
		{
			name:           "both missing warns naming both",
			in:             GitIdentityInput{HasServers: true, InRepo: true},
			wantRow:        true,
			wantSeverity:   SevWarn,
			detailContains: []string{"user.name", "user.email"},
			wantFix:        true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, tc.run)
	}
}

// gitIdentityCase is one TestGitIdentityCheck row.
type gitIdentityCase struct {
	name           string
	in             GitIdentityInput
	wantRow        bool
	wantSeverity   Severity
	wantDetail     string   // exact, when non-empty
	detailContains []string // substrings the detail must carry
	detailMissing  []string // substrings it must not
	wantFix        bool
}

func (tc gitIdentityCase) run(t *testing.T) {
	c, ok := GitIdentityCheck(tc.in)
	if ok != tc.wantRow {
		t.Fatalf("GitIdentityCheck ok = %v, want %v", ok, tc.wantRow)
	}
	if !tc.wantRow {
		return
	}
	if c.Name != "git identity" || c.Group != "" {
		t.Errorf("row = %+v, want the global git identity row", c)
	}
	if c.Severity != tc.wantSeverity {
		t.Errorf("severity = %v, want %v", c.Severity, tc.wantSeverity)
	}
	if tc.wantDetail != "" && c.Detail != tc.wantDetail {
		t.Errorf("detail = %q, want %q", c.Detail, tc.wantDetail)
	}
	for _, s := range tc.detailContains {
		if !strings.Contains(c.Detail, s) {
			t.Errorf("detail = %q, want it to contain %q", c.Detail, s)
		}
	}
	for _, s := range tc.detailMissing {
		if strings.Contains(c.Detail, s) {
			t.Errorf("detail = %q, want it not to contain %q", c.Detail, s)
		}
	}
	if tc.wantFix && c.Fix == "" {
		t.Error("fix is empty, want a pasteable command")
	}
}
