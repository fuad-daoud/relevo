package chatlabel

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

func TestLabelString(t *testing.T) {
	tests := []struct {
		name string
		l    Label
		want string
	}{
		{"nothing", Label{}, "-"},
		{"text only", Label{Text: "my chat"}, "my chat"},
		{"link only", Label{Link: "https://claude.ai/code/session_01TEST"}, "https://claude.ai/code/session_01TEST"},
		{"both", Label{Text: "my chat", Link: "https://claude.ai/code/session_01TEST"}, "my chat · https://claude.ai/code/session_01TEST"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.l.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestOpencode(t *testing.T) {
	if got := Opencode([]byte("My session\n")); got.Text != "My session" {
		t.Errorf("Text = %q, want %q", got.Text, "My session")
	}
	if got := Opencode(nil); got != (Label{}) {
		t.Errorf("empty output gave %+v, want the zero Label", got)
	}
	if got, want := OpencodeQuery("ses_a'b"), "select title from session_v2 where id = 'ses_a''b' union all select title from session where id = 'ses_a''b' and not exists (select 1 from session_v2 where id = 'ses_a''b')"; got != want {
		t.Errorf("OpencodeQuery = %q, want %q", got, want)
	}
}

func TestOpencodeLegacyQuery(t *testing.T) {
	if got, want := OpencodeLegacyQuery("ses_a'b"), "select title from session where id = 'ses_a''b'"; got != want {
		t.Errorf("OpencodeLegacyQuery = %q, want %q", got, want)
	}
}

// fakeExec records every invocation and answers with fixed output.
type fakeExec struct {
	calls [][]string
	out   []byte
	err   error
}

func (f *fakeExec) Run(_ context.Context, bin string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{bin}, args...))
	if f.err != nil {
		return nil, f.err
	}
	return f.out, nil
}

func TestResolveOpencode(t *testing.T) {
	fake := &fakeExec{out: []byte("MasterMind round 2\n")}
	res := Resolver{Exec: fake, OpencodeDB: "/tmp/opencode.db"}

	label := res.Resolve(context.Background(), "opencode", "ses_abc123")
	if label.Text != "MasterMind round 2" {
		t.Errorf("Text = %q, want %q", label.Text, "MasterMind round 2")
	}
	if label.Link != "" {
		t.Errorf("Link = %q, want empty", label.Link)
	}
	wantArgs := []string{"sqlite3", "-readonly", "/tmp/opencode.db", OpencodeQuery("ses_abc123")}
	if len(fake.calls) != 1 {
		t.Fatalf("Exec ran %d times, want 1", len(fake.calls))
	}
	if !slices.Equal(fake.calls[0], wantArgs) {
		t.Errorf("Exec ran %q, want %q", fake.calls[0], wantArgs)
	}

	if got := res.Resolve(context.Background(), "opencode", "bad id"); got != (Label{}) {
		t.Errorf("invalid session id gave %+v, want the zero Label", got)
	}
	if len(fake.calls) != 1 {
		t.Errorf("invalid session id reached Exec: %q", fake.calls[1:])
	}

	noExec := Resolver{OpencodeDB: "/tmp/opencode.db"}
	if got := noExec.Resolve(context.Background(), "opencode", "ses_abc123"); got != (Label{}) {
		t.Errorf("nil Exec gave %+v, want the zero Label", got)
	}
}

// fallbackExec fails its first Run and answers the second.
type fallbackExec struct {
	calls [][]string
	out   []byte
}

func (f *fallbackExec) Run(_ context.Context, bin string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{bin}, args...))
	if len(f.calls) == 1 {
		return nil, errors.New("no such table: session_v2")
	}
	return f.out, nil
}

func TestResolveOpencodeLegacyFallback(t *testing.T) {
	fake := &fallbackExec{out: []byte("Pre-2 title\n")}
	res := Resolver{Exec: fake, OpencodeDB: "/tmp/opencode.db"}

	label := res.Resolve(context.Background(), "opencode", "ses_old")
	if label.Text != "Pre-2 title" {
		t.Errorf("Text = %q, want %q", label.Text, "Pre-2 title")
	}
	if len(fake.calls) != 2 {
		t.Fatalf("Exec ran %d times, want 2", len(fake.calls))
	}
	wantSecond := []string{"sqlite3", "-readonly", "/tmp/opencode.db", OpencodeLegacyQuery("ses_old")}
	if !slices.Equal(fake.calls[1], wantSecond) {
		t.Errorf("second Exec ran %q, want %q", fake.calls[1], wantSecond)
	}
}

// cliExec is usage.Exec over the real sqlite3 binary.
type cliExec struct{}

func (cliExec) Run(ctx context.Context, bin string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, bin, args...).Output()
}

// opencodeFixture builds a fixture db: no test may touch the real opencode.db.
func opencodeFixture(t *testing.T, stmts ...string) string {
	t.Helper()
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skipf("sqlite3 not on PATH: %v", err)
	}
	path := filepath.Join(t.TempDir(), "opencode.db")
	for _, stmt := range stmts {
		out, err := exec.Command("sqlite3", path, stmt).CombinedOutput()
		if err != nil {
			t.Fatalf("sqlite3 %q: %v: %s", stmt, err, out)
		}
	}
	return path
}

// TestResolveOpencodeV2Title pins that OpenCode 2.0.14's session_v2 wins over the legacy table.
func TestResolveOpencodeV2Title(t *testing.T) {
	db := opencodeFixture(t,
		"create table session (id text, title text)",
		"create table session_v2 (id text, title text)",
		"insert into session values ('ses_both', 'Legacy title')",
		"insert into session values ('ses_legacyonly', 'Legacy-only title')",
		"insert into session_v2 values ('ses_both', 'V2 title')",
	)
	res := Resolver{Exec: cliExec{}, OpencodeDB: db}

	if got := res.Resolve(context.Background(), "opencode", "ses_both"); got.Text != "V2 title" {
		t.Errorf("session_v2 title = %q, want %q", got.Text, "V2 title")
	}
	if got := res.Resolve(context.Background(), "opencode", "ses_legacyonly"); got.Text != "Legacy-only title" {
		t.Errorf("legacy title = %q, want %q", got.Text, "Legacy-only title")
	}
}

func TestResolveOpencodePre2(t *testing.T) {
	db := opencodeFixture(t,
		"create table session (id text, title text)",
		"insert into session values ('ses_old', 'Pre-2 title')",
	)
	res := Resolver{Exec: cliExec{}, OpencodeDB: db}

	if got := res.Resolve(context.Background(), "opencode", "ses_old"); got.Text != "Pre-2 title" {
		t.Errorf("pre-2.0 title = %q, want %q", got.Text, "Pre-2 title")
	}
}

func TestResolveOther(t *testing.T) {
	res := Resolver{}
	if got := res.Resolve(context.Background(), "agy", "x"); got != (Label{}) {
		t.Errorf("agy gave %+v, want the zero Label", got)
	}
}
