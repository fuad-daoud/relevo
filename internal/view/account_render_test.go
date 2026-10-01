package view

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestRenderStatusShowsAccount pins status column: a row whose round drew
// an account names it on the runner line, and a row that drew none keeps the
// line it always had.
func TestRenderStatusShowsAccount(t *testing.T) {
	t.Parallel()

	base := BindingStatus{
		Name: "webshop", CWD: "/repo", Round: 2, Display: "ACTIVE",
		BuilderCandidate: "opencode/cline-pass/glm", BuilderKind: "opencode", BuilderStatus: "working",
	}

	with := base
	with.BuilderAccount = "clinepass-2"
	line := runnerLine(t, RenderStatus(Report{Bindings: []BindingStatus{with}}))
	if !strings.Contains(line, "account clinepass-2") {
		t.Errorf("runner line %q, want the account", line)
	}

	line = runnerLine(t, RenderStatus(Report{Bindings: []BindingStatus{base}}))
	if strings.Contains(line, "account") {
		t.Errorf("runner line %q, want no account", line)
	}
}

// runnerLine is the one "  runner  ..." line a one-row report renders.
func runnerLine(t *testing.T, out string) string {
	t.Helper()
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "  runner ") {
			return l
		}
	}
	t.Fatalf("no runner line in:\n%s", out)
	return ""
}

// TestBindingStatusAccountJSON: the account is omitted from the document when
// empty and present when set, so a consumer that never learned it sees the row
// it always did.
func TestBindingStatusAccountJSON(t *testing.T) {
	t.Parallel()

	b, err := json.Marshal(BindingStatus{Name: "a", Round: 1})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"account"`) {
		t.Errorf("account-less row marshals a key: %s", b)
	}

	b, err = json.Marshal(BindingStatus{Name: "a", Round: 1, BuilderAccount: "cp1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"account":"cp1"`) {
		t.Errorf("row with an account = %s, want the key", b)
	}
}
