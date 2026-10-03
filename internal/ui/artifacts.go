package ui

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/fuad-daoud/relevo/internal/relevo"
)

// The artifacts table's column widths, from the approved board: FILE 30,
// SIZE 8, WRITTEN 10, then OPENS IN 8. artifactTableCols is the width of a
// whole row, so the cursor band covers every column.
const (
	artifactFileWidth  = 30
	artifactSizeWidth  = 8
	artifactTimeWidth  = 10
	artifactOpensWidth = 8
	artifactTableCols  = artifactFileWidth + artifactSizeWidth + artifactTimeWidth + artifactOpensWidth
)

// artifactPreviewLines caps how much of a non-markdown artifact the tab
// renders: a file is a file, not a screen.
const artifactPreviewLines = 2000

// artifactOpenKind is how a file is opened, from its extension: markdown in
// the pager, HTML and SVG in the browser, everything else in the editor.
func artifactOpenKind(rel string) string {
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".md":
		return "pager"
	case ".html", ".htm", ".svg":
		return "browser"
	default:
		return "editor"
	}
}

// artifactOpenTarget decides how one artifact opens. An .excalidraw scene with
// a companion .svg beside it opens that svg in the browser; every other file
// opens by its own extension kind, at its own path.
func artifactOpenTarget(rel, path string) (kind, target string) {
	if strings.EqualFold(filepath.Ext(rel), ".excalidraw") {
		companion := strings.TrimSuffix(path, filepath.Ext(path)) + ".svg"
		if _, err := os.Stat(companion); err == nil {
			return "browser", companion
		}
		return "editor", path
	}
	return artifactOpenKind(rel), path
}

// artifactOpensIn is the kind as the table's OPENS IN cell names it.
func artifactOpensIn(c tabContent, rel string) string {
	kind, _ := artifactOpenTarget(rel, filepath.Join(c.artifactDir, filepath.FromSlash(rel)))
	if kind == "editor" {
		return "$EDITOR"
	}
	return kind
}

// artifactEditHint is the faint line an .excalidraw scene with no companion
// shows: the command that edits it. It is empty for every other file, and for
// a scene whose companion already opens in the browser.
func artifactEditHint(c tabContent) string {
	if !strings.EqualFold(filepath.Ext(c.artifactRel), ".excalidraw") {
		return ""
	}
	path := filepath.Join(c.artifactDir, filepath.FromSlash(c.artifactRel))
	kind, target := artifactOpenTarget(c.artifactRel, path)
	if kind != "editor" {
		return ""
	}
	return "run relevo board " + target + " to edit"
}

// artifactsWord is the artifact count as prose: "1 artifact", "2 artifacts".
func artifactsWord(n int) string {
	if n == 1 {
		return "1 artifact"
	}
	return strconv.Itoa(n) + " artifacts"
}

// artifactsBody renders the artifacts tab's whole body: the table with its
// cursor band, then one faint line naming the selected file, then that file.
func artifactsBody(c tabContent) string {
	if !c.loaded {
		return "loading…"
	}
	st := styleFor(c)
	if c.err != nil {
		return st.Render("error: " + sanitizeText(c.err.Error()))
	}
	if c.empty != "" {
		return st.Render(sanitizeText(c.empty))
	}
	if len(c.artifacts) == 0 {
		return st.Render("no artifacts")
	}

	sel := 0
	for i, f := range c.artifacts {
		if f.Rel == c.artifactRel {
			sel = i
			break
		}
	}

	var b strings.Builder
	b.WriteString(faintStyle.Bold(true).Render(
		fit("FILE", artifactFileWidth) +
			fit("SIZE", artifactSizeWidth) +
			fit("WRITTEN", artifactTimeWidth) +
			fit("OPENS IN", artifactOpensWidth)))
	b.WriteByte('\n')
	for i, f := range c.artifacts {
		rel := sanitizeText(f.Rel)
		size := relevo.ArtifactSizeText(f.Size)
		when := f.MTime.Local().Format("15:04")
		opens := artifactOpensIn(c, f.Rel)
		row := fit(rel, artifactFileWidth) + fit(size, artifactSizeWidth) +
			fit(when, artifactTimeWidth) + fit(opens, artifactOpensWidth)
		if i == sel {
			b.WriteString(selBandStyle.Foreground(textStyle.GetForeground()).Bold(true).Render(row))
		} else {
			b.WriteString(textStyle.Render(fit(rel, artifactFileWidth)) +
				mutedStyle.Render(fit(size, artifactSizeWidth)) +
				mutedStyle.Render(fit(when, artifactTimeWidth)) +
				faintStyle.Render(fit(opens, artifactOpensWidth)))
		}
		b.WriteByte('\n')
	}

	b.WriteByte('\n')
	b.WriteString(faintStyle.Render(artifactCaption(c)))
	b.WriteByte('\n')
	if hint := artifactEditHint(c); hint != "" {
		b.WriteString(faintStyle.Render(hint))
		b.WriteByte('\n')
	}

	if c.artifactErr != nil {
		b.WriteString(errorStyle.Render("error: " + sanitizeText(c.artifactErr.Error())))
		return b.String()
	}
	b.WriteString(artifactFileBody(c))
	return b.String()
}

// artifactCaption is the faint line between the table and the file: the
// actor's output file, and the pre-rename summary.md, are the actor's final
// message; every other file is just its rel.
func artifactCaption(c tabContent) string {
	if c.artifactRel != c.artifactOutput && c.artifactRel != "summary.md" {
		return sanitizeText(c.artifactRel)
	}
	if c.artifactActor == "" {
		return sanitizeText(c.artifactRel) + " · the final message"
	}
	return sanitizeText(c.artifactRel) + " · the " + sanitizeText(c.artifactActor) + "'s final message"
}

// artifactFileBody renders the selected file: markdown through the cockpit's
// renderer, anything else as its first lines of plain text, or a
// "binary file, <size>" line when it is not valid UTF-8.
func artifactFileBody(c tabContent) string {
	if !utf8.ValidString(c.artifactBody) {
		return mutedStyle.Render("binary file, " + relevo.ArtifactSizeText(artifactSizeOf(c)))
	}
	text := sanitizeText(c.artifactBody)
	if artifactOpenKind(c.artifactRel) == "pager" {
		return renderMarkdown(text)
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) > artifactPreviewLines {
		lines = lines[:artifactPreviewLines]
	}
	return strings.Join(lines, "\n")
}

// artifactSizeOf is rel's size as the fetched list carries it.
func artifactSizeOf(c tabContent) int64 {
	for _, f := range c.artifacts {
		if f.Rel == c.artifactRel {
			return f.Size
		}
	}
	return int64(len(c.artifactBody))
}

// artifactCount is the fetched list's length, for the tab label and the card.
func artifactCount(c tabContent) int {
	return len(c.artifacts)
}

// artifactTotalSize is the fetched list's total size in bytes.
func artifactTotalSize(c tabContent) int64 {
	var total int64
	for _, f := range c.artifacts {
		total += f.Size
	}
	return total
}
