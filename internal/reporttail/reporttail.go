// Package reporttail decodes the structured block a builder appends to its
// report: a fenced relevo block whose fields say what the round produced.
package reporttail

import (
	"bytes"
	"fmt"
	"strings"
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

	tail.HaltedAt = strings.TrimSpace(UnquoteScalar(haltedAtRaw))

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

// StripComment drops a trailing # comment, honouring quotes and a bracketed
// flow list so a # inside either is content.
func StripComment(line string) string {
	var inQuote rune
	inBracket := false
	for i, r := range line {
		switch {
		case inQuote != 0:
			if r == inQuote {
				inQuote = 0
			}
		case inBracket:
			switch r {
			case '"', '\'':
				inQuote = r
			case ']':
				inBracket = false
			}
		default:
			switch r {
			case '"', '\'':
				inQuote = r
			case '[':
				inBracket = true
			case '#':
				return line[:i]
			}
		}
	}
	return line
}

// UnquoteScalar removes one layer of matching single or double quotes around a
// trimmed scalar.
func UnquoteScalar(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
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

func splitListElements(s string) []string {
	var elements []string
	var current strings.Builder
	var inQuote rune

	for _, r := range s {
		switch {
		case inQuote != 0:
			if r == inQuote {
				inQuote = 0
			}
			current.WriteRune(r)
		default:
			switch r {
			case '"', '\'':
				inQuote = r
				current.WriteRune(r)
			case ',':
				elements = append(elements, current.String())
				current.Reset()
			default:
				current.WriteRune(r)
			}
		}
	}
	elements = append(elements, current.String())
	return elements
}

// flowListDepth counts unclosed [ in s, ignoring brackets inside quotes. A
// value with depth > 0 is a flow list continued on later lines.
func flowListDepth(s string) int {
	var depth int
	var inQuote rune

	for _, r := range s {
		switch {
		case inQuote != 0:
			if r == inQuote {
				inQuote = 0
			}
		default:
			switch r {
			case '"', '\'':
				inQuote = r
			case '[':
				depth++
			case ']':
				depth--
			}
		}
	}
	return depth
}
