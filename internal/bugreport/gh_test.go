package bugreport

import (
	"slices"
	"testing"
)

// TestIssueArgv pins the whole command, argument for argument: it is printed to
// a person and run by --gh, and nothing else is ever assembled.
func TestIssueArgv(t *testing.T) {
	got := IssueArgv("relevo 0.4.2: bug report", "/state/bugreports/bugreport-20260930T004312Z.md")
	want := []string{
		"gh", "issue", "create",
		"--repo", "fuad-daoud/relevo",
		"--title", "relevo 0.4.2: bug report",
		"--body-file", "/state/bugreports/bugreport-20260930T004312Z.md",
		"--label", "bug",
	}
	if !slices.Equal(got, want) {
		t.Errorf("IssueArgv =\n%q\nwant\n%q", got, want)
	}
}

// TestShellLineQuotes pins the printed line: safe arguments stay bare, and
// everything a shell would act on is single-quoted, including a title with an
// apostrophe, a space and a dollar.
func TestShellLineQuotes(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want string
	}{
		{
			name: "safe arguments stay bare",
			argv: IssueArgv("bug report", "/state/bugreports/bugreport-20260930T004312Z.md"),
			want: "gh issue create --repo fuad-daoud/relevo --title 'bug report' " +
				"--body-file /state/bugreports/bugreport-20260930T004312Z.md --label bug",
		},
		{
			name: "the title's own quoting is POSIX",
			argv: []string{"gh", "issue", "create", "--title", "it's relevo's fault"},
			want: `gh issue create --title 'it'\''s relevo'\''s fault'`,
		},
		{
			name: "a dollar is quoted so it cannot expand",
			argv: []string{"--title", "$HOME failed"},
			want: `--title '$HOME failed'`,
		},
		{
			name: "an empty argument survives as one",
			argv: []string{"--title", ""},
			want: "--title ''",
		},
		{
			name: "a path with spaces is quoted",
			argv: []string{"--body-file", "/tmp/my state/bug report.md"},
			want: `--body-file '/tmp/my state/bug report.md'`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ShellLine(tc.argv); got != tc.want {
				t.Errorf("ShellLine(%q) = %q, want %q", tc.argv, got, tc.want)
			}
		})
	}
}
