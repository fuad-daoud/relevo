package bugreport

import (
	"os"
	"regexp"
	"strings"

	"github.com/fuad-daoud/relevo/internal/sanitize"
)

// The placeholders a rule writes. Only the secret placeholder is shared: it is
// the same bytes every renderer of a credential already uses.
const (
	userToken   = "<user>"
	hostToken   = "<host>"
	remoteToken = "<remote>"
)

// Redactor is the bundle's privacy pass: the machine facts the rules need, and
// Raw, which turns every rule off for --raw. The zero value redacts nothing it
// does not know, which is the safe half of the pass.
type Redactor struct {
	Home string
	User string
	Host string
	Raw  bool
}

// NewRedactor reads the machine facts from the process: the home directory, the
// user name and the host name. A fact it cannot read stays empty, and an empty
// name is never replaced.
func NewRedactor() Redactor {
	home, _ := os.UserHomeDir()
	host, _ := os.Hostname()
	return Redactor{Home: home, User: os.Getenv("USER"), Host: host}
}

// Text applies every rule to s, in the order the rules depend on: home paths
// first, then the user and host tokens, then remotes, then secret shapes.
func (r Redactor) Text(s string) string {
	if r.Raw {
		return s
	}
	out := r.redactPaths(s)
	out = replaceToken(out, r.User, userToken)
	out = replaceToken(out, r.Host, hostToken)
	out = redactRemotes(out)
	return redactSecrets(out)
}

// Redact returns b with every string it carries rewritten, and the lines the
// header prints about the pass filled in. A raw redactor marks the bundle raw
// and leaves every byte as it was.
func Redact(b Bundle, r Redactor) Bundle {
	out := b
	out.Raw = r.Raw
	if r.Raw {
		out.Privacy = []string{"not redacted (--raw): this bundle carries paths, names and secrets as they were recorded"}
		return out
	}
	out.Title = r.Text(b.Title)
	out.Sections = make([]Section, 0, len(b.Sections))
	for _, s := range b.Sections {
		out.Sections = append(out.Sections, r.section(s))
	}
	out.Omissions = make([]Omission, 0, len(b.Omissions))
	for _, o := range b.Omissions {
		out.Omissions = append(out.Omissions, Omission{Section: o.Section, Reason: r.Text(o.Reason)})
	}
	out.Privacy = r.privacy()
	return out
}

// section rewrites every string one section carries.
func (r Redactor) section(s Section) Section {
	out := Section{
		Name:    s.Name,
		Omitted: r.Text(s.Omitted),
		Columns: make([]string, 0, len(s.Columns)),
		Rows:    make([][]string, 0, len(s.Rows)),
		Lines:   make([]string, 0, len(s.Lines)),
	}
	for _, c := range s.Columns {
		out.Columns = append(out.Columns, r.Text(c))
	}
	for _, row := range s.Rows {
		cells := make([]string, 0, len(row))
		for _, cell := range row {
			cells = append(cells, r.Text(cell))
		}
		out.Rows = append(out.Rows, cells)
	}
	for _, l := range s.Lines {
		out.Lines = append(out.Lines, r.Text(l))
	}
	return out
}

// privacy is what the header states about the pass: the placeholders it wrote,
// and which name it left alone because replacing a name that short would take
// ordinary words with it.
func (r Redactor) privacy() []string {
	lines := []string{"redacted: home paths to ~, the user name to " + userToken +
		", the host name to " + hostToken + ", remotes and secret-shaped strings to placeholders"}
	if len(r.User) < 3 {
		lines = append(lines, "user name is shorter than 3 characters and was not replaced")
	}
	if len(r.Host) < 3 {
		lines = append(lines, "host name is shorter than 3 characters and was not replaced")
	}
	return lines
}

// homeRe matches the home prefix of any account, not just this one: /home/<u>
// or /Users/<u>, so a path recorded on another machine shortens too.
var homeRe = regexp.MustCompile(`(^|[^A-Za-z0-9_.-])(?:/Users|/home)/[A-Za-z0-9._-]+`)

// redactPaths shortens every home path to ~. The literal $HOME is shortened
// too: a report that quotes a shell command names the home directory either way.
func (r Redactor) redactPaths(s string) string {
	if r.Home != "" {
		s = strings.ReplaceAll(s, r.Home, "~")
	}
	s = strings.ReplaceAll(s, "$HOME", "~")
	return homeRe.ReplaceAllString(s, "${1}~")
}

// tokenBytes are the bytes a whole name is made of: a name is only replaced
// when a run of these equals it, so "fuad" never eats "fuad-daoud".
func tokenByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	case b == '_' || b == '-' || b >= 0x80:
		return true
	}
	return false
}

// replaceToken replaces every whole occurrence of name. A name shorter than
// three characters is not replaced at all: two letters are ordinary words, and
// replacing them would mangle the bundle it is meant to protect.
func replaceToken(s, name, repl string) string {
	if len(name) < 3 || !strings.Contains(s, name) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if !tokenByte(s[i]) {
			b.WriteByte(s[i])
			i++
			continue
		}
		j := i
		for j < len(s) && tokenByte(s[j]) {
			j++
		}
		if s[i:j] == name {
			b.WriteString(repl)
		} else {
			b.WriteString(s[i:j])
		}
		i = j
	}
	return b.String()
}

// remoteRe matches a git remote or a URL, credentials included.
var remoteRe = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s"'<>]+|[A-Za-z0-9._-]+@[A-Za-z0-9._-]+:[^\s"'<>]+`)

// redactRemotes replaces every remote and URL, keeping the one exception: the
// project's own address, so a bundle can still say where it is filed.
func redactRemotes(s string) string {
	return remoteRe.ReplaceAllStringFunc(s, func(m string) string {
		if strings.Contains(m, "github.com/fuad-daoud/relevo") {
			return m
		}
		return remoteToken
	})
}

// secretRe are the secret shapes: one rule per shape, in the order they must
// run -- a shape that is a special case of a later one comes first.
var secretRe = []*regexp.Regexp{
	regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{8,}`),
	regexp.MustCompile(`sk-[A-Za-z0-9_-]{20,}`),
	regexp.MustCompile(`(?:ghp_|gho_|ghs_|github_pat_)[A-Za-z0-9_]{16,}`),
	regexp.MustCompile(`AKIA[A-Z0-9]{12,}`),
	regexp.MustCompile(`xox[baprs]-[A-Za-z0-9-]{10,}`),
	regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}(?:\.[A-Za-z0-9_-]+)+`),
	regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`),
	regexp.MustCompile(`(?i)Bearer\s+[A-Za-z0-9._~+/=-]{8,}`),
	regexp.MustCompile(`(?i)\b(?:key|token|secret|password)=[^\s&,;"']+`),
}

// runRe matches a run long enough to be a base64 or base62 secret. A run never
// spans a path separator: a long path segment is one segment, so a state root
// under /tmp that happens to mix case and digits is not mistaken for a token.
var runRe = regexp.MustCompile(`[A-Za-z0-9+]{40,}={0,2}`)

// redactSecrets replaces every secret-shaped string. A long run is only a
// secret when it cannot be a commit id: hex of 40 characters or fewer is kept,
// which is what lets a bundle name the commit it was built from.
func redactSecrets(s string) string {
	for _, re := range secretRe {
		s = re.ReplaceAllString(s, sanitize.Redacted)
	}
	return runRe.ReplaceAllStringFunc(s, func(m string) string {
		core := strings.TrimRight(m, "=")
		if len(core) <= 40 && isHex(core) {
			return m
		}
		if !mixesCaseAndDigits(core) {
			return m
		}
		return sanitize.Redacted
	})
}

// isHex reports whether s is hexadecimal.
func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return len(s) > 0
}

// mixesCaseAndDigits reports whether s carries an upper-case letter, a
// lower-case letter and a digit -- the mix a base64 secret has and a word, a
// path or a hexadecimal id does not.
func mixesCaseAndDigits(s string) bool {
	var upper, lower, digit bool
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z':
			upper = true
		case c >= 'a' && c <= 'z':
			lower = true
		case c >= '0' && c <= '9':
			digit = true
		}
	}
	return upper && lower && digit
}
