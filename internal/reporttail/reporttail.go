// Package reporttail decodes the structured block a builder appends to its
// report: a fenced relevo block whose fields say what the round produced.
package reporttail

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/fuad-daoud/relevo/internal/sanitize"
)

// Outcome values a trailing block's status may carry.
const (
	OutcomeDone         = "done"
	OutcomeHalted       = "halted"
	OutcomeBlocked      = "blocked"
	OutcomeDeferred     = "deferred"
	OutcomeUnstructured = "unstructured"
)

// Tail is the structured metadata decoded from a builder's trailing relevo
// block.
type Tail struct {
	Status   string
	HaltedAt string
	// ChangedPathsSet records that the changed_paths key was present, even
	// with an empty list.
	ChangedPathsSet bool
	ChangedPaths    []string
	CommandsRun     []string
	NotDone         []string
}

// Parse finds and decodes the builder's trailing relevo block. Pure; no I/O;
// every failure returns ok == false.
func Parse(report []byte) (Tail, bool) {
	tail, ok, _ := ParseWithReason(report)
	return tail, ok
}

// ParseWithReason is Parse plus the reason a fenced block was found but
// rejected. The reason is empty when there was no block, so callers can tell
// an omitted block from one they could not read.
func ParseWithReason(report []byte) (Tail, bool, string) {
	if len(report) == 0 {
		return Tail{}, false, ""
	}

	lines := SplitFenceLines(report)

	openIdx, closeIdx, reason := FindRelevoBlock(lines)
	if openIdx == -1 {
		return Tail{}, false, ""
	}
	if reason != "" {
		return Tail{}, false, reason
	}

	tail, statusRaw, hasStatus, haltedAtRaw, reject := scanTail(lines, openIdx, closeIdx)
	if reject != "" {
		return Tail{}, false, reject
	}
	if !hasStatus {
		return Tail{}, false, "tail: missing status"
	}

	status := strings.TrimSpace(UnquoteScalar(statusRaw))
	switch status {
	case OutcomeDone, OutcomeHalted, OutcomeBlocked, OutcomeDeferred:
		tail.Status = status
	default:
		return Tail{}, false, "tail: unknown status"
	}

	tail.HaltedAt = sanitize.Text(strings.TrimSpace(UnquoteScalar(haltedAtRaw)))

	return tail, true, ""
}

// scanTail walks the block's content lines, filling tail and collecting the
// status and halted_at values the caller validates. reject is empty unless a
// line could not be read.
func scanTail(lines []string, openIdx, closeIdx int) (tail Tail, statusRaw string, hasStatus bool, haltedAtRaw, reject string) {
	var openList string
	for i := openIdx + 1; i < closeIdx; i++ {
		line := strings.TrimSpace(StripComment(lines[i]))
		if line == "" {
			continue
		}
		if item, isItem := stripListItem(line); isItem {
			if openList == "" {
				return Tail{}, "", false, "", fmt.Sprintf("tail: line %d has no ':'", i+1)
			}
			if item != "" {
				setList(&tail, openList, append(listOf(tail, openList), item))
			}
			continue
		}
		colon := strings.Index(line, ":")
		if colon == -1 {
			return Tail{}, "", false, "", fmt.Sprintf("tail: line %d has no ':'", i+1)
		}
		key := strings.TrimSpace(line[:colon])
		val := strings.TrimSpace(line[colon+1:])
		openList = ""
		switch key {
		case "status":
			hasStatus = true
			statusRaw = val
		case "halted_at":
			haltedAtRaw = val
		case "changed_paths", "commands_run", "not_done":
			if key == "changed_paths" {
				tail.ChangedPathsSet = true
			}
			next, err := applyListKey(&tail, lines, i, closeIdx, key, val)
			if err != nil {
				return Tail{}, "", false, "", err.Error()
			}
			i = next
			if val == "" {
				openList = key
			}
		default:
			// Unknown keys are ignored.
		}
	}
	return tail, statusRaw, hasStatus, haltedAtRaw, ""
}

// stripListItem reports whether line is a "- item" list entry and returns the
// entry's unquoted content.
func stripListItem(line string) (string, bool) {
	if !strings.HasPrefix(line, "-") {
		return "", false
	}
	if len(line) > 1 && line[1] != ' ' && line[1] != '\t' {
		return "", false
	}
	return strings.TrimSpace(UnquoteScalar(strings.TrimSpace(line[1:]))), true
}

// applyListKey folds one list key's value into tail and returns the last line
// index it consumed. A bare key opens a block list; a bracketed value that is
// not closed on this line is joined with the lines that follow.
func applyListKey(tail *Tail, lines []string, i, closeIdx int, key, val string) (int, error) {
	if val == "" {
		setList(tail, key, nil)
		return i, nil
	}
	if !strings.HasPrefix(val, "[") || flowListDepth(val) <= 0 {
		setList(tail, key, ParseListValue(val))
		return i, nil
	}

	openLine := i
	depth := flowListDepth(val)
	joined := val
	for depth > 0 {
		i++
		if i >= closeIdx {
			return i, fmt.Errorf("tail: line %d: %s list is not closed", openLine+1, key)
		}
		cont := strings.TrimSpace(StripComment(lines[i]))
		if cont == "" {
			continue
		}
		joined += " " + cont
		depth += flowListDepth(cont)
	}
	setList(tail, key, ParseListValue(joined))
	return i, nil
}

// FindRelevoBlock locates the LAST ```relevo fence among lines and its closing
// fence, returning their indices. There is no block when openIdx is -1.
//
// reason is "" when a block was found and is well formed, and non-empty when
// a fence was found but unusable (unclosed, or followed by prose). Callers
// use it to tell a writer that omitted the block from one whose block could
// not be read.
func FindRelevoBlock(lines []string) (openIdx, closeIdx int, reason string) {
	openIdx = -1
	for i, line := range lines {
		if strings.TrimRight(line, " \t") == "```relevo" {
			openIdx = i
		}
	}
	if openIdx == -1 {
		return -1, -1, ""
	}

	closeIdx = -1
	for i := openIdx + 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], " \t") == "```" {
			closeIdx = i
			break
		}
	}
	if closeIdx == -1 {
		return openIdx, -1, "tail: unclosed fence"
	}

	for i := closeIdx + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != "" {
			return openIdx, closeIdx, "tail: prose after closing fence"
		}
	}

	return openIdx, closeIdx, ""
}

// RelevoBlock is one fenced relevo block: the line indices of its opening and
// closing fences.
type RelevoBlock struct{ Open, Close int }

// RelevoBlocks returns every fenced relevo block in lines, in file order:
// each ```relevo opening fence paired with the next ``` closing fence. An
// opening fence with no closing fence after it yields no pair and ends the
// scan; no block returns nil.
func RelevoBlocks(lines []string) []RelevoBlock {
	var blocks []RelevoBlock
	for i := 0; i < len(lines); i++ {
		if strings.TrimRight(lines[i], " \t") != "```relevo" {
			continue
		}
		closeIdx := -1
		for j := i + 1; j < len(lines); j++ {
			if strings.TrimRight(lines[j], " \t") == "```" {
				closeIdx = j
				break
			}
		}
		if closeIdx == -1 {
			return blocks
		}
		blocks = append(blocks, RelevoBlock{Open: i, Close: closeIdx})
		i = closeIdx
	}
	return blocks
}

// SplitFenceLines splits body into CR-trimmed lines, the form FindRelevoBlock
// expects. The report tail and the reviewer verdict must split the same way,
// or the two would disagree about the same bytes.
func SplitFenceLines(body []byte) []string {
	rawLines := bytes.Split(body, []byte("\n"))
	lines := make([]string, len(rawLines))
	for i, l := range rawLines {
		lines[i] = string(bytes.TrimRight(l, "\r"))
	}
	return lines
}

// StripTail returns body without its trailing relevo block. Pure; no I/O;
// total. The block is located with the same parser the round close uses, so
// "the block" means exactly what the close's outcome parse means by it.
//
//   - empty body → nil.
//   - no ```relevo fence → body byte-identical.
//   - last fence is followed by prose (FindRelevoBlock reason "tail: prose
//     after closing fence") → body byte-identical; cutting it would eat prose.
//   - otherwise (well-formed block or unclosed fence): lines before the
//     opening fence with trailing blank lines dropped, joined with "\n" and
//     terminated by exactly one "\n"; an empty slice returns nil.
func StripTail(body []byte) []byte {
	if len(body) == 0 {
		return nil
	}
	lines := SplitFenceLines(body)
	openIdx, _, reason := FindRelevoBlock(lines)
	if openIdx < 0 || reason == "tail: prose after closing fence" {
		return body
	}
	kept := lines[:openIdx]
	// Drop trailing blank lines.
	for len(kept) > 0 && strings.TrimSpace(kept[len(kept)-1]) == "" {
		kept = kept[:len(kept)-1]
	}
	if len(kept) == 0 {
		return nil
	}
	return []byte(strings.Join(kept, "\n") + "\n")
}

func listOf(tail Tail, key string) []string {
	switch key {
	case "changed_paths":
		return tail.ChangedPaths
	case "commands_run":
		return tail.CommandsRun
	case "not_done":
		return tail.NotDone
	default:
		return nil
	}
}

func setList(tail *Tail, key string, vals []string) {
	switch key {
	case "changed_paths":
		tail.ChangedPaths = vals
	case "commands_run":
		tail.CommandsRun = vals
	case "not_done":
		tail.NotDone = vals
	}
}

// scalarRune is one rune of a scanned string plus the two facts the escape
// rules give it: whether it lies outside every quoted scalar, and whether it
// opens a YAML escape instead of standing for itself.
type scalarRune struct {
	// Index and Size locate the rune in the scanned string.
	Index int
	Size  int
	Rune  rune
	// Outside marks a rune no quote encloses. Only such a rune may act as
	// syntax; a rune inside a scalar is content.
	Outside bool
	// Escape marks the rune that opens a YAML escape rather than producing
	// itself: the backslash of a double-quoted scalar, or the first of a
	// doubled pair in a single-quoted one.
	Escape bool
}

// scanScalars walks s and calls fn for every rune until fn says stop. This is
// the one place the YAML escape rules live, so every caller agrees on where a
// scalar ends: inside a " scalar a backslash escapes the next rune, which makes
// an escaped quote and an escaped backslash literal, and inside a ' scalar a
// doubled single quote is one literal quote. A caller therefore never mistakes
// an escaped quote for a closer.
func scanScalars(s string, fn func(scalarRune) bool) {
	var quote rune
	literal := false
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		sr := scalarRune{Index: i, Size: size, Rune: r, Outside: quote == 0}
		switch {
		case quote == '"' && r == '\\' && !literal:
			sr.Escape = true
			literal = true
		case quote == '\'' && r == '\'' && !literal:
			// A doubled '' is the escaped pair: the first quote marks the
			// escape and the second clears it. Any other ' closes the scalar,
			// exactly as a lone " closes a double-quoted one, so the rune after
			// it is syntax again.
			if i+size < len(s) && s[i+size] == '\'' {
				sr.Escape = true
				literal = true
			} else {
				quote = 0
			}
		case quote == '\'' && r == '\'' && literal:
			literal = false
		case literal:
			literal = false
		case quote != 0 && r == quote:
			quote = 0
		case quote == 0 && (r == '"' || r == '\''):
			quote = r
		}
		if !fn(sr) {
			return
		}
		i += size
	}
}

// StripComment drops a trailing # comment, honouring quotes and a bracketed
// flow list so a # inside either is content.
func StripComment(line string) string {
	inBracket := false
	cut := -1
	scanScalars(line, func(r scalarRune) bool {
		if !r.Outside {
			return true
		}
		switch r.Rune {
		case '[':
			inBracket = true
		case ']':
			inBracket = false
		case '#':
			if !inBracket {
				cut = r.Index
				return false
			}
		}
		return true
	})
	if cut < 0 {
		return line
	}
	return line[:cut]
}

// UnquoteScalar removes one layer of matching single or double quotes around a
// trimmed scalar and resolves the escapes inside it: an escaped quote and an
// escaped backslash in a double-quoted scalar, a doubled single quote in a
// single-quoted one.
func UnquoteScalar(s string) string {
	s = strings.TrimSpace(s)
	if len(s) < 2 {
		return s
	}
	double := s[0] == '"' && s[len(s)-1] == '"'
	single := s[0] == '\'' && s[len(s)-1] == '\''
	if !double && !single {
		return s
	}
	var b strings.Builder
	scanScalars(s, func(r scalarRune) bool {
		if r.Index == 0 || r.Index == len(s)-1 {
			return true
		}
		if !r.Escape {
			b.WriteString(s[r.Index : r.Index+r.Size])
		}
		return true
	})
	return b.String()
}

// ParseListValue reads a value as a list: a bracketed flow list is split on
// commas, anything else is one element. Quoted elements are unquoted and empty
// ones dropped.
func ParseListValue(s string) []string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		inner := s[1 : len(s)-1]
		rawElements := splitListElements(inner)
		var res []string
		for _, elem := range rawElements {
			elem = strings.TrimSpace(UnquoteScalar(elem))
			if elem != "" {
				res = append(res, elem)
			}
		}
		if len(res) == 0 {
			return nil
		}
		return res
	}

	elem := strings.TrimSpace(UnquoteScalar(s))
	if elem == "" {
		return nil
	}
	return []string{elem}
}

// splitListElements splits s on the commas outside scalars, keeping each
// element raw because ParseListValue unquotes it next.
func splitListElements(s string) []string {
	var elements []string
	start := 0
	scanScalars(s, func(r scalarRune) bool {
		if r.Outside && r.Rune == ',' {
			elements = append(elements, s[start:r.Index])
			start = r.Index + r.Size
		}
		return true
	})
	return append(elements, s[start:])
}

// flowListDepth counts unclosed [ in s, ignoring brackets inside scalars. A
// value with depth > 0 is a flow list continued on later lines.
func flowListDepth(s string) int {
	var depth int
	scanScalars(s, func(r scalarRune) bool {
		if !r.Outside {
			return true
		}
		switch r.Rune {
		case '[':
			depth++
		case ']':
			depth--
		}
		return true
	})
	return depth
}
