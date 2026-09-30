package bugreport

import "strings"

// GhBodyLimit is GitHub's issue-body limit in bytes, the ceiling a file handed
// to `gh issue create --body-file` must fit inside. It is counted in bytes,
// which is conservative against GitHub's 65,536-character limit.
const GhBodyLimit = 65536

// IssueArgv is the exact command that files a bundle. The default behaviour
// prints it as a line and --gh runs it: no other command is ever assembled, and
// filing is never silent.
func IssueArgv(title, path string) []string {
	return []string{
		"gh", "issue", "create",
		"--repo", "fuad-daoud/relevo",
		"--title", title,
		"--body-file", path,
		"--label", "bug",
	}
}

// ShellLine renders argv as one POSIX command line a person can paste. Every
// argument that a shell would change is single-quoted, and an embedded quote is
// spelled the POSIX way.
func ShellLine(argv []string) string {
	parts := make([]string, 0, len(argv))
	for _, a := range argv {
		parts = append(parts, shellQuote(a))
	}
	return strings.Join(parts, " ")
}

// shellQuote leaves an argument alone when every byte is one a shell passes
// through unchanged, and quotes it otherwise. The empty argument is quoted: it
// has to reach the command as an argument, not disappear.
func shellQuote(a string) string {
	if a != "" && strings.IndexFunc(a, unsafeShellByte) < 0 {
		return a
	}
	return "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
}

// unsafeShellByte reports whether r would be read as syntax, a separator or a
// space by a POSIX shell rather than as part of one word.
func unsafeShellByte(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return false
	}
	return !strings.ContainsRune("-._/:@%+=,~", r)
}
