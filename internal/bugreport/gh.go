package bugreport

import "strings"

// GhBodyLimit is GitHub's issue-body limit in bytes: a file handed to
// `gh issue create --body-file` must fit inside it. Bytes are conservative
// against the character limit.
const GhBodyLimit = 65536

// IssueArgv is the exact command that files a bundle: no other command is
// ever assembled, and filing is never silent.
func IssueArgv(title, path string) []string {
	return []string{
		"gh", "issue", "create",
		"--repo", "fuad-daoud/relevo",
		"--title", title,
		"--body-file", path,
		"--label", "bug",
	}
}

// ShellLine renders argv as one POSIX command line a person can paste.
func ShellLine(argv []string) string {
	parts := make([]string, 0, len(argv))
	for _, a := range argv {
		parts = append(parts, shellQuote(a))
	}
	return strings.Join(parts, " ")
}

// shellQuote leaves an argument alone when a shell passes every byte through
// unchanged, and quotes it otherwise; the empty argument is quoted so it
// still reaches the command.
func shellQuote(a string) string {
	if a != "" && strings.IndexFunc(a, unsafeShellByte) < 0 {
		return a
	}
	return "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
}

func unsafeShellByte(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return false
	}
	return !strings.ContainsRune("-._/:@%+=,~", r)
}
