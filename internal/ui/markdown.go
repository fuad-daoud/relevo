package ui

import (
	"strings"

	"github.com/fuad-daoud/relevo/internal/ui/widget"
)

func renderMarkdown(body string) string {
	if body == "" {
		return ""
	}
	lines := strings.Split(body, "\n")
	out := make([]string, len(lines))
	inFence := false

	for i, line := range lines {
		if strings.HasPrefix(line, "```") {
			inFence = !inFence
			out[i] = widget.FaintStyle.Render(line)
			continue
		}
		if inFence {
			out[i] = widget.MutedStyle.Render(line)
			continue
		}

		if strings.HasPrefix(line, "#") {
			n := 0
			for n < len(line) && line[n] == '#' {
				n++
			}
			if n < len(line) && line[n] == ' ' {
				x := strings.TrimLeft(line[n:], " ")
				if n <= 2 {
					out[i] = widget.AccentStyle.Bold(true).Render(x)
				} else {
					out[i] = widget.TextStyle.Bold(true).Render(x)
				}
				continue
			}
		}

		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") {
			indent := line[:len(line)-len(trimmed)]
			rest := trimmed[2:]
			out[i] = indent + widget.FaintStyle.Render("•") + " " + renderInline(rest)
			continue
		}

		if strings.HasPrefix(line, "|") {
			out[i] = widget.MutedStyle.Render(line)
			continue
		}

		out[i] = renderInline(line)
	}

	return strings.Join(out, "\n")
}

// renderInline renders inline code (`x` -> accent) and bold (**x** -> text, bold).
// Unmatched delimiters are left as is.
func renderInline(s string) string {
	if s == "" {
		return ""
	}
	var out strings.Builder
	var plain strings.Builder

	flushPlain := func() {
		if plain.Len() > 0 {
			out.WriteString(widget.TextStyle.Render(plain.String()))
			plain.Reset()
		}
	}

	for i := 0; i < len(s); {
		if s[i] == '`' {
			if next := strings.IndexByte(s[i+1:], '`'); next != -1 {
				flushPlain()
				code := s[i+1 : i+1+next]
				out.WriteString(widget.AccentStyle.Render(code))
				i = i + 1 + next + 1
				continue
			}
			plain.WriteByte(s[i])
			i++
			continue
		}

		if strings.HasPrefix(s[i:], "**") {
			if next := strings.Index(s[i+2:], "**"); next != -1 {
				flushPlain()
				bold := s[i+2 : i+2+next]
				out.WriteString(widget.TextStyle.Bold(true).Render(bold))
				i = i + 2 + next + 2
				continue
			}
			plain.WriteString("**")
			i += 2
			continue
		}

		plain.WriteByte(s[i])
		i++
	}

	flushPlain()
	return out.String()
}
