// Package patch is a small unified-diff reader sized for relevo's own
// captured patches: it numbers each line by its post-image (new-file) line
// number, so a review comment can anchor to a path:line.
package patch

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Patch is a parsed unified diff: one File per "diff --git" block, in the order they appeared.
type Patch struct {
	Files []File
}

// File's Path is the post-image path: the "b/" side of "diff --git", overridden by a "+++ b/<path>" line.
type File struct {
	Path  string
	Hunks []Hunk
}

type Hunk struct {
	OldStart int
	OldLines int
	NewStart int
	NewLines int
	Header   string // the "@@ ... @@" line, verbatim
	Lines    []Line
}

type Line struct {
	Kind byte   // ' ', '+', or '-'
	Text string // the line's text, without the leading Kind byte
	New  int    // post-image line number; 0 for a '-' line
}

// HasLine reports whether path has a ' ' or '+' line at post-image line number line.
func (p Patch) HasLine(path string, line int) (Hunk, Line, bool) {
	for _, f := range p.Files {
		if f.Path != path {
			continue
		}
		for _, h := range f.Hunks {
			for _, l := range h.Lines {
				if (l.Kind == ' ' || l.Kind == '+') && l.New == line {
					return h, l, true
				}
			}
		}
	}
	return Hunk{}, Line{}, false
}

var hunkHeaderRE = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@(.*)$`)

// rawFile and rawHunk carry everything Parse and Annotate both need, including the raw header lines.
type rawFile struct {
	headerLines []string
	path        string
	hunks       []rawHunk
}

type rawHunk struct {
	headerLine string
	oldStart   int
	oldLines   int
	newStart   int
	newLines   int
	nextNew    int
	lines      []Line
}

func extractGitDiffPath(line string) string {
	const marker = " b/"
	if idx := strings.LastIndex(line, marker); idx != -1 {
		return line[idx+len(marker):]
	}
	return strings.TrimPrefix(line, "diff --git ")
}

type parseState struct {
	files         []rawFile
	cur           *rawFile
	curHunk       *rawHunk
	hunkStartLine int
	inHunk        bool
}

// consume dispatches line by its prefix; anything unrecognised is ignored.
func (st *parseState) consume(line string, lineNum int) error {
	if strings.HasPrefix(line, "diff --git ") {
		if err := st.flushHunk(); err != nil {
			return err
		}
		st.flushFile()
		st.cur = &rawFile{path: extractGitDiffPath(line), headerLines: []string{line}}
		st.curHunk = nil
		st.inHunk = false
		return nil
	}
	if st.cur == nil {
		return nil
	}
	if !st.inHunk && strings.HasPrefix(line, "--- ") {
		st.cur.headerLines = append(st.cur.headerLines, line)
		return nil
	}
	if !st.inHunk && strings.HasPrefix(line, "+++ ") {
		st.cur.headerLines = append(st.cur.headerLines, line)
		rest := strings.TrimPrefix(line, "+++ ")
		st.cur.path = strings.TrimPrefix(rest, "b/") // a rename can disagree with "diff --git"
		return nil
	}
	if m := hunkHeaderRE.FindStringSubmatch(line); m != nil {
		if err := st.flushHunk(); err != nil {
			return err
		}
		oldStart, _ := strconv.Atoi(m[1])
		oldLines := 1
		if m[2] != "" {
			oldLines, _ = strconv.Atoi(m[2])
		}
		newStart, _ := strconv.Atoi(m[3])
		newLines := 1
		if m[4] != "" {
			newLines, _ = strconv.Atoi(m[4])
		}
		st.cur.hunks = append(st.cur.hunks, rawHunk{
			headerLine: line,
			oldStart:   oldStart,
			oldLines:   oldLines,
			newStart:   newStart,
			newLines:   newLines,
			nextNew:    newStart,
		})
		st.curHunk = &st.cur.hunks[len(st.cur.hunks)-1]
		st.hunkStartLine = lineNum
		st.inHunk = true
		return nil
	}
	if st.inHunk && len(line) > 0 && (line[0] == ' ' || line[0] == '+' || line[0] == '-') {
		kind := line[0]
		text := line[1:]
		newVal := 0
		if kind != '-' {
			newVal = st.curHunk.nextNew
			st.curHunk.nextNew++
		}
		st.curHunk.lines = append(st.curHunk.lines, Line{Kind: kind, Text: text, New: newVal})
	}
	return nil
}

// flushHunk checks the just-finished hunk's declared new-line count against what was collected.
func (st *parseState) flushHunk() error {
	if st.curHunk == nil {
		return nil
	}
	found := 0
	for _, l := range st.curHunk.lines {
		if l.Kind == ' ' || l.Kind == '+' {
			found++
		}
	}
	if found != st.curHunk.newLines {
		return fmt.Errorf("hunk at %s line %d: expected %d new lines, found %d", st.cur.path, st.hunkStartLine, st.curHunk.newLines, found)
	}
	return nil
}

func (st *parseState) flushFile() {
	if st.cur != nil {
		st.files = append(st.files, *st.cur)
	}
}

func parseRaw(b []byte) ([]rawFile, error) {
	lines := strings.Split(string(b), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	var st parseState
	for i, line := range lines {
		if err := st.consume(line, i+1); err != nil {
			return nil, err
		}
	}
	if err := st.flushHunk(); err != nil {
		return nil, err
	}
	st.flushFile()

	return st.files, nil
}

func Parse(b []byte) (Patch, error) {
	raws, err := parseRaw(b)
	if err != nil {
		return Patch{}, err
	}
	var p Patch
	for _, rf := range raws {
		f := File{Path: rf.path}
		for _, rh := range rf.hunks {
			f.Hunks = append(f.Hunks, Hunk{
				OldStart: rh.oldStart,
				OldLines: rh.oldLines,
				NewStart: rh.newStart,
				NewLines: rh.newLines,
				Header:   rh.headerLine,
				Lines:    rh.lines,
			})
		}
		p.Files = append(p.Files, f)
	}
	return p, nil
}
