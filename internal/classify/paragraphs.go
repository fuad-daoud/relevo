package classify

import (
	"bytes"
	"strings"
)

const (
	MaxParagraphs = 120    // one noul question each; ~120 tokens per question keeps the 64k total budget
	MaxStateBytes = 96_000 // ~24k tokens at 4 bytes/token, under the documented 32k state ceiling
)

func IsFence(line string) bool {
	trimmed := strings.TrimRight(line, " \t")
	n := 0
	for n < len(trimmed) && trimmed[n] == '`' {
		n++
	}
	if n < 3 {
		return false
	}
	rest := trimmed[n:]
	return !strings.Contains(rest, "`")
}

// Split turns text into paragraphs: CR stripped from every line; a fence line
// opens a fenced paragraph that runs to the next fence line, and is in none.
func Split(text []byte) []Paragraph {
	if len(text) == 0 {
		return nil
	}

	rawLines := bytes.Split(text, []byte("\n"))
	var paras []Paragraph
	inFence := false
	var curLines []string
	curStart := 0

	flush := func(kind Kind) {
		if len(curLines) == 0 {
			curLines = nil
			return
		}
		paras = append(paras, Paragraph{
			Index: len(paras),
			Kind:  kind,
			Text:  strings.Join(curLines, "\n"),
			Line:  curStart,
			Lines: len(curLines),
		})
		curLines = nil
	}

	for i, rawLine := range rawLines {
		lineNo := i + 1
		line := string(bytes.TrimRight(rawLine, "\r"))
		if IsFence(line) {
			if inFence {
				flush(KindFenced)
				inFence = false
			} else {
				flush(KindProse)
				inFence = true
				curStart = lineNo + 1
			}
			continue
		}
		if inFence {
			curLines = append(curLines, line)
			continue
		}
		if strings.TrimSpace(line) == "" {
			flush(KindProse)
			continue
		}
		if len(curLines) == 0 {
			curStart = lineNo
		}
		curLines = append(curLines, line)
	}

	if inFence {
		flush(KindFenced)
	} else {
		flush(KindProse)
	}

	if len(paras) == 0 {
		return nil
	}
	return paras
}

// Trim keeps at most MaxParagraphs paragraphs within MaxStateBytes, taking from
// the head and tail alternately so both ends of a long report are seen.
func Trim(paras []Paragraph) (kept []Paragraph, partial bool) {
	totalBytes := 0
	for _, p := range paras {
		totalBytes += len(p.Text)
	}
	if len(paras) <= MaxParagraphs && totalBytes <= MaxStateBytes {
		return paras, false
	}

	keep := make(map[int]bool)
	curBytes := 0
	lo, hi := 0, len(paras)-1
	takeHead := true

	for lo <= hi && len(keep) < MaxParagraphs {
		idx := lo
		if !takeHead {
			idx = hi
		}
		p := paras[idx]
		if curBytes+len(p.Text) > MaxStateBytes {
			break
		}
		keep[idx] = true
		curBytes += len(p.Text)
		if takeHead {
			lo++
		} else {
			hi--
		}
		takeHead = !takeHead
	}

	for i, p := range paras {
		if keep[i] {
			kept = append(kept, p)
		}
	}
	return kept, true
}
