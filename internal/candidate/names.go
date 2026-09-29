package candidate

import (
	"regexp"
	"strconv"
	"strings"
)

// namePattern is the shape of a candidate name: lowercase, at most 24
// characters, and never containing "/", so a name can never be a token.
var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,23}$`)

const maxNameLen = 24

func IsName(s string) bool {
	return namePattern.MatchString(s)
}

// DeriveNames returns one name per entry, in entry order, keeping an explicit
// Name verbatim. Every explicit and provider name is reserved first; an unnamed
// entry then takes the first free candidate: base, base-effort, harness-base,
// harness-base-effort, then base-2, base-3, ...
func DeriveNames(entries []Candidate) []string {
	reserved := make(map[string]bool, len(entries)*2)
	for _, e := range entries {
		if e.Name != "" {
			reserved[e.Name] = true
		}
	}
	for _, e := range entries {
		if e.Provider != "" {
			reserved[e.Provider] = true
		}
	}

	names := make([]string, len(entries))
	for i, e := range entries {
		if e.Name != "" {
			names[i] = e.Name
			continue
		}
		base, effort := deriveBase(e.Model)
		name := pickName(e.Harness, base, effort, reserved)
		reserved[name] = true
		names[i] = name
	}
	return names
}

// pickName does not reserve its result: DeriveNames reserves it.
func pickName(harnessName, base, effort string, reserved map[string]bool) string {
	tries := []string{base}
	if effort != "" {
		tries = append(tries, base+"-"+effort)
	}
	tries = append(tries, harnessName+"-"+base)
	if effort != "" {
		tries = append(tries, harnessName+"-"+base+"-"+effort)
	}
	for _, t := range tries {
		if n := truncate(t, maxNameLen); !reserved[n] && IsName(n) {
			return n
		}
	}

	for i := 2; ; i++ {
		suffix := "-" + strconv.Itoa(i)
		n := truncate(base, maxNameLen-len(suffix)) + suffix
		if !reserved[n] && IsName(n) {
			return n
		}
	}
}

func deriveBase(model string) (base, effort string) {
	seg := model
	if i := strings.LastIndex(seg, "/"); i >= 0 {
		seg = seg[i+1:]
	}
	if i := strings.IndexAny(seg, "#:"); i >= 0 {
		effort = seg[i+1:]
		seg = seg[:i]
	}
	return slug(seg), effort
}

func slug(s string) string {
	s = strings.ToLower(s)

	var b strings.Builder
	inRun := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '-' {
			b.WriteRune(r)
			inRun = false
			continue
		}
		if !inRun {
			b.WriteByte('-')
			inRun = true
		}
	}

	out := strings.TrimLeft(b.String(), "-.")
	out = truncate(out, maxNameLen)
	if out == "" {
		return "c"
	}
	return out
}

// truncate cuts s to at most n bytes, on a rune boundary.
func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}
