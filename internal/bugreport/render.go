package bugreport

import (
	"fmt"
	"strings"
	"time"
	"unicode"
)

// Title is the header line the bundle and the issue carry.
func Title(version string, e LastError, ok bool) string {
	if !ok || e.Code == "" {
		return fmt.Sprintf("relevo %s: bug report", version)
	}
	return fmt.Sprintf("relevo %s: %s in %s", version, e.Code, e.Verb)
}

// Doc is the bundle's JSON document: the same facts Markdown renders.
type Doc struct {
	Title     string     `json:"title"`
	Version   string     `json:"version"`
	Created   time.Time  `json:"created"`
	Raw       bool       `json:"raw"`
	Privacy   []string   `json:"privacy,omitempty"`
	Sections  []Section  `json:"sections"`
	Omissions []Omission `json:"omissions,omitempty"`
}

// Doc returns b as the JSON document `--json` prints. Sections is never nil,
// so an empty bundle marshals an empty list rather than null.
func (b Bundle) Doc() Doc {
	d := Doc{
		Title:     b.Title,
		Version:   b.Version,
		Created:   b.Created.UTC(),
		Raw:       b.Raw,
		Privacy:   b.Privacy,
		Sections:  b.Sections,
		Omissions: b.Omissions,
	}
	if d.Sections == nil {
		d.Sections = []Section{}
	}
	return d
}

func Markdown(b Bundle) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s\n\n", b.Title)
	fmt.Fprintf(&sb, "created: %s\n", b.Created.UTC().Format(time.RFC3339))
	fmt.Fprintf(&sb, "relevo: %s\n", b.Version)
	for _, p := range b.Privacy {
		fmt.Fprintf(&sb, "privacy: %s\n", p)
	}
	for _, s := range b.Sections {
		writeSection(&sb, s)
	}
	if len(b.Omissions) > 0 {
		sb.WriteString("\n")
		for _, o := range b.Omissions {
			fmt.Fprintf(&sb, "%s: omitted: %s\n", o.Section, o.Reason)
		}
	}
	return sb.String()
}

// MarkdownCapped renders the bundle for a file GitHub accepts: at or under
// limit the bytes are Markdown's own, over it the render is cut on a line
// boundary. A bundle that cannot be cut is an error naming its size and the
// limit rather than a silently truncated file.
func MarkdownCapped(b Bundle, limit int) (string, error) {
	full := Markdown(b)
	if limit <= 0 || len(full) <= limit {
		return full, nil
	}
	marker := fmt.Sprintf("[body cut at %d bytes to fit GitHub's issue-body limit; run 'relevo bugreport --stdout' for the full render]", limit)
	keep := limit - len(marker) - 1
	if keep <= 0 {
		return "", uncuttable(len(full), limit)
	}
	idx := strings.LastIndex(full[:keep], "\n")
	if idx < 0 {
		return "", uncuttable(len(full), limit)
	}
	return full[:idx+1] + marker + "\n", nil
}

// uncuttable is the error a bundle that cannot be cut on a line boundary
// fails with.
func uncuttable(size, limit int) error {
	return fmt.Errorf("bundle is %d bytes; its first line alone does not fit GitHub's %d-byte issue-body limit", size, limit)
}

// writeSection renders one part: its heading, then its prose lines, then its table.
func writeSection(sb *strings.Builder, s Section) {
	fmt.Fprintf(sb, "\n## %s\n\n", sectionTitle(s.Name))
	if s.Omitted != "" {
		fmt.Fprintf(sb, "%s: omitted: %s\n", s.Name, s.Omitted)
		return
	}
	for _, l := range s.Lines {
		sb.WriteString(l + "\n")
	}
	if len(s.Columns) == 0 {
		return
	}
	if len(s.Lines) > 0 {
		sb.WriteString("\n")
	}
	writeTable(sb, s)
}

func writeTable(sb *strings.Builder, s Section) {
	sb.WriteString("| " + strings.Join(cells(s.Columns), " | ") + " |\n")
	sep := make([]string, len(s.Columns))
	for i := range sep {
		sep[i] = "---"
	}
	sb.WriteString("| " + strings.Join(sep, " | ") + " |\n")
	for _, row := range s.Rows {
		sb.WriteString("| " + strings.Join(cells(row), " | ") + " |\n")
	}
}

// cells makes every cell safe for one markdown table row.
func cells(cells []string) []string {
	out := make([]string, 0, len(cells))
	for _, c := range cells {
		c = strings.ReplaceAll(c, "\n", " ")
		out = append(out, strings.ReplaceAll(c, "|", `\|`))
	}
	return out
}

func sectionTitle(name string) string {
	runes := []rune(strings.ReplaceAll(name, "_", " "))
	if len(runes) == 0 {
		return name
	}
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}
