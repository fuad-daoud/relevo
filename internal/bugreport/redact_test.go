package bugreport

import (
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/sanitize"
)

// baseRedactor is the machine every rule row runs against: one home, one user
// name and one host name, all long enough to replace.
var baseRedactor = Redactor{Home: "/home/fuad", User: "fuad", Host: "contabo-01"}

// redactCases is the rule table: one row per rule of the pass, and the rows
// that pin what it must leave alone.
var redactCases = []struct {
	name string
	r    Redactor
	in   string
	want string
}{
	{
		name: "this machine's home becomes a tilde",
		r:    baseRedactor,
		in:   "/home/fuad/.local/state/relevo/relevo.db",
		want: "~/.local/state/relevo/relevo.db",
	},
	{
		name: "another account's home becomes a tilde",
		r:    baseRedactor,
		in:   "read /home/someone/work/out.txt",
		want: "read ~/work/out.txt",
	},
	{
		name: "a macOS home becomes a tilde",
		r:    baseRedactor,
		in:   "/Users/someone/Library/Application Support/relevo",
		want: "~/Library/Application Support/relevo",
	},
	{
		name: "the literal $HOME becomes a tilde",
		r:    baseRedactor,
		in:   "cd $HOME/x",
		want: "cd ~/x",
	},
	{
		name: "the user name becomes the user placeholder",
		r:    baseRedactor,
		in:   "fuad@contabo-01 ran it",
		want: "<user>@<host> ran it",
	},
	{
		name: "the host name becomes the host placeholder",
		r:    baseRedactor,
		in:   "on contabo-01, at port 22",
		want: "on <host>, at port 22",
	},
	{
		name: "a name inside a longer token is not a whole token",
		r:    baseRedactor,
		in:   "github.com/fuad-daoud/relevo",
		want: "github.com/fuad-daoud/relevo",
	},
	{
		name: "a two-letter user name is not replaced",
		r:    Redactor{Home: "/home/fu", User: "fu", Host: "h1"},
		in:   "fu ran it on h1",
		want: "fu ran it on h1",
	},
	{
		name: "an ssh remote becomes the remote placeholder",
		r:    baseRedactor,
		in:   "origin\tgit@github.com:someone/other.git (fetch)",
		want: "origin\t<remote> (fetch)",
	},
	{
		name: "an https url becomes the remote placeholder",
		r:    baseRedactor,
		in:   "https://example.com/x/y?z=1",
		want: "<remote>",
	},
	{
		name: "url credentials go with the url",
		r:    baseRedactor,
		// The scanner-visible prefix is split from the body: a contiguous
		// token-shaped literal trips repository secret scanning, which reads
		// the blobs, though the value exists only to be redacted.
		in:   "https://bob:ghp_" + "16C7e42F292c6912E7710c838347Ae178B4a@example.com/git",
		want: "<remote>",
	},
	{
		name: "the project's own url is kept",
		r:    baseRedactor,
		in:   "see https://github.com/fuad-daoud/relevo/issues/709",
		want: "see https://github.com/fuad-daoud/relevo/issues/709",
	},
	{
		name: "an anthropic key is redacted",
		r:    baseRedactor,
		in:   "key sk-ant-" + "api03-AbCdEfGhIjKlMnOpQrStUvWx",
		want: "key " + sanitize.Redacted,
	},
	{
		name: "an openai-shaped key is redacted",
		r:    baseRedactor,
		in:   "OPENAI_API_KEY=sk-proj-" + "AbCdEfGhIjKlMnOpQrStUvWx",
		want: "OPENAI_API_KEY=" + sanitize.Redacted,
	},
	{
		name: "a github token is redacted",
		r:    baseRedactor,
		in:   "ghp_" + "16C7e42F292c6912E7710c838347Ae178B4a",
		want: sanitize.Redacted,
	},
	{
		name: "a github pat is redacted",
		r:    baseRedactor,
		in:   "github_pat_" + "11ABCDEFG0abcdefghijkl_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789abcdef",
		want: sanitize.Redacted,
	},
	{
		name: "an aws key id is redacted",
		r:    baseRedactor,
		in:   "AKIAIOSFODNN7EXAMPLE",
		want: sanitize.Redacted,
	},
	{
		name: "a slack token is redacted",
		r:    baseRedactor,
		in:   "xox" + "b-123456789012-abcdefghijklmnop",
		want: sanitize.Redacted,
	},
	{
		name: "a jwt is redacted",
		r:    baseRedactor,
		in:   "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U",
		want: sanitize.Redacted,
	},
	{
		name: "a private key block is redacted",
		r:    baseRedactor,
		in:   "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEAx7\n-----END RSA PRIVATE KEY-----",
		want: sanitize.Redacted,
	},
	{
		name: "a bearer header is redacted",
		r:    baseRedactor,
		in:   "Authorization: Bearer abcdefgh12345678",
		want: "Authorization: " + sanitize.Redacted,
	},
	{
		name: "a key=value pair is redacted",
		r:    baseRedactor,
		in:   "url?token=abc123&other=1",
		want: "url?" + sanitize.Redacted + "&other=1",
	},
	{
		name: "a long mixed-case run is redacted",
		r:    baseRedactor,
		in:   "QWxhZGRpbjpvcGVuIHNlc2FtZQ0YzAbCdEfGhIjKlMnOpQrSt",
		want: sanitize.Redacted,
	},
	{
		name: "a forty-character commit id is kept",
		r:    baseRedactor,
		in:   "commit 857a8fa2b7d0d1b6b7b5e40c5f2a0de3f4c7a1b9",
		want: "commit 857a8fa2b7d0d1b6b7b5e40c5f2a0de3f4c7a1b9",
	},
	{
		name: "a mixed-case forty-character hex id is kept",
		r:    baseRedactor,
		in:   "id a1B2c3D4e5F6a7B8c9D0e1F2a3B4c5D6e7F8a9B0",
		want: "id a1B2c3D4e5F6a7B8c9D0e1F2a3B4c5D6e7F8a9B0",
	},
	{
		name: "a long lower-case word run is kept",
		r:    baseRedactor,
		in:   strings.Repeat("a", 64),
		want: strings.Repeat("a", 64),
	},
	{
		name: "a raw redactor leaves every rule alone",
		r:    Redactor{Home: "/home/fuad", User: "fuad", Host: "contabo-01", Raw: true},
		in:   "fuad@contabo-01 /home/fuad/x git@github.com:o/r sk-ant-" + "api03-AbCdEfGhIjKlMnOp",
		want: "fuad@contabo-01 /home/fuad/x git@github.com:o/r sk-ant-" + "api03-AbCdEfGhIjKlMnOp",
	},
}

// TestRedactRules pins one row per rule: what the pass rewrites, and the three
// things it must leave alone -- a name too short to replace, a word the user
// name is a substring of, and the project's own address.
func TestRedactRules(t *testing.T) {
	for _, tc := range redactCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.r.Text(tc.in); got != tc.want {
				t.Errorf("Text(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestRedactMarksItsWork pins what the pass tells a reader about itself: the
// placeholder line, the name it was too short to replace, and the raw bundle's
// own warning.
func TestRedactMarksItsWork(t *testing.T) {
	raw := Redact(Bundle{Sections: []Section{{Name: SectionStatus, Lines: []string{"/home/fuad/x"}}}},
		Redactor{Home: "/home/fuad", User: "fuad", Host: "contabo-01", Raw: true})
	if !raw.Raw {
		t.Error("a raw redactor must mark the bundle raw")
	}
	if got := raw.Sections[0].Lines[0]; got != "/home/fuad/x" {
		t.Errorf("a raw bundle changed a line: %q", got)
	}
	if len(raw.Privacy) != 1 || !strings.Contains(raw.Privacy[0], "--raw") {
		t.Errorf("a raw bundle must say so in its header, got %q", raw.Privacy)
	}

	short := Redact(Bundle{}, Redactor{User: "fu", Host: "h"})
	if len(short.Privacy) != 3 {
		t.Fatalf("Privacy = %q, want the pass line and one for each short name", short.Privacy)
	}
	for _, line := range short.Privacy[1:] {
		if !strings.Contains(line, "shorter than 3 characters") {
			t.Errorf("a short name must be accounted for, got %q", line)
		}
	}
}

// TestRedactBundleRewritesEveryString checks the pass reaches rows, lines,
// headings and omission reasons alike, so no rendering can carry what the
// markdown does not.
func TestRedactBundleRewritesEveryString(t *testing.T) {
	b := Bundle{
		Title: "relevo 0.1: bug report",
		Sections: []Section{{
			Name:    SectionStatus,
			Columns: []string{"binding", "cwd"},
			Rows:    [][]string{{"alpha", "/home/fuad/work"}},
			Lines:   []string{"tail: git@github.com:o/r"},
		}},
		Omissions: []Omission{{Section: SectionJournal, Reason: "cannot read /home/fuad/x"}},
	}
	got := Redact(b, Redactor{Home: "/home/fuad"})
	if got.Sections[0].Rows[0][1] != "~/work" {
		t.Errorf("a row cell was not rewritten: %q", got.Sections[0].Rows[0][1])
	}
	if got.Sections[0].Lines[0] != "tail: <remote>" {
		t.Errorf("a line was not rewritten: %q", got.Sections[0].Lines[0])
	}
	if got.Omissions[0].Reason != "cannot read ~/x" {
		t.Errorf("an omission reason was not rewritten: %q", got.Omissions[0].Reason)
	}
}
