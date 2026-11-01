package relevo

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/text"
)

// This file is the cockpit's audit seam: a stored revision's changes in
// human words, and the whole-document check the cockpit runs before it
// offers one.

// ChangeLine is one revision change, rendered for a human: the op glyph, the
// subject the path belongs to, what is left of the path under it, and the two
// sides of the value.
type ChangeLine struct {
	Op      string // "+" add, "-" remove, "~" change, "*" set (secrets)
	Subject string // what changed, in human words
	Field   string // the rest of the path under the subject, "" when none
	Before  string // display text of the old value, "" for + and *
	After   string // display text of the new value, "" for - and *
}

// RollbackRefused reports a roll back the cockpit refuses: the revision's
// document would not pass the checks a cockpit edit passes, so writing it back
// would leave a config the form itself would have refused.
type RollbackRefused struct {
	Rev    int64
	Reason string
}

func (e *RollbackRefused) Error() string {
	return fmt.Sprintf("can't roll back to #%d: %s", e.Rev, e.Reason)
}

// auditMaxValue is the number of runes a rendered value keeps before its
// ellipsis, the same 60 config's cutValue keeps.
const auditMaxValue = 60

// DescribeChange renders one stored change for a human. A candidate's name
// is looked up in after for a change and an add, and in before for a remove:
// that is the side that carries the entry.
func DescribeChange(c config.Change, before, after config.Doc) ChangeLine {
	named := after
	if c.Op == "remove" {
		named = before
	}
	subject, field, ms := changeSubject(c.Path, named)

	line := ChangeLine{Op: changeOp(c.Op), Subject: subject, Field: field}
	switch c.Op {
	case "add":
		line.After = changeValue(c.After, ms)
	case "remove":
		line.Before = changeValue(c.Before, ms)
	default:
		line.Before = changeValue(c.Before, ms)
		line.After = changeValue(c.After, ms)
	}
	return line
}

func changeOp(op string) string {
	switch op {
	case "add":
		return "+"
	case "remove":
		return "-"
	case "change":
		return "~"
	case "set":
		return "*"
	}
	return op
}

// changeSubject applies the subject-and-field rules, first match wins. ms
// reports a policy path whose last segment ended in "_ms", so its number is
// spoken with a unit.
func changeSubject(path string, doc config.Doc) (subject, field string, ms bool) {
	if i, rest, ok := candidatePath(path); ok {
		name := candidateNameAt(doc, i)
		if name == "" {
			name = fmt.Sprintf("candidates[%d]", i)
		}
		if rest == "" {
			return "candidate " + name, "", false
		}
		return name, rest, false
	}
	if a, rest, ok := pathUnder(path, "actors"); ok {
		if rest == "" {
			return "actor " + a, "", false
		}
		return "actor " + a, rest, false
	}
	if a, rest, ok := pathUnder(path, "agents"); ok {
		if rest == "" {
			return "agent " + a, "", false
		}
		return "agent " + a, rest, false
	}
	if s, rest, ok := pathUnder(path, "servers"); ok {
		if rest == "" {
			return "server " + s, "", false
		}
		return "server " + s, rest, false
	}
	if rest, ok := strings.CutPrefix(path, "policy."); ok && rest != "" {
		s, ms := stripMS(rest)
		return s, "", ms
	}
	return path, "", false
}

func candidatePath(path string) (i int, rest string, ok bool) {
	body, ok := strings.CutPrefix(path, "candidates[")
	if !ok {
		return 0, "", false
	}
	end := strings.IndexByte(body, ']')
	if end < 0 {
		return 0, "", false
	}
	n, err := strconv.Atoi(body[:end])
	if err != nil || n < 0 {
		return 0, "", false
	}
	switch after := body[end+1:]; {
	case after == "":
		return n, "", true
	case strings.HasPrefix(after, "."):
		return n, after[1:], true
	}
	return 0, "", false
}

// pathUnder splits "<prefix><a>" and "<prefix><a>.<rest>", where prefix is
// the section name without its dot.
func pathUnder(path, prefix string) (a, rest string, ok bool) {
	body, ok := strings.CutPrefix(path, prefix)
	if !ok || body == "" {
		return "", "", false
	}
	after := ""
	switch {
	case body[0] == '[':
		end := strings.IndexByte(body, ']')
		if end < 0 {
			return "", "", false
		}
		if err := json.Unmarshal([]byte(body[1:end]), &a); err != nil {
			return "", "", false
		}
		after = body[end+1:]
	case body[0] == '.':
		body = body[1:]
		if body == "" {
			return "", "", false
		}
		i := strings.IndexAny(body, ".[")
		if i < 0 {
			return body, "", true
		}
		a, after = body[:i], body[i:]
	default:
		return "", "", false
	}
	switch {
	case after == "":
		return a, "", true
	case after[0] == '.':
		return a, after[1:], true
	case after[0] == '[':
		return a, after, true
	}
	return "", "", false
}

func stripMS(rest string) (string, bool) {
	segs := strings.Split(rest, ".")
	last := segs[len(segs)-1]
	if !strings.HasSuffix(last, "_ms") {
		return rest, false
	}
	segs[len(segs)-1] = strings.TrimSuffix(last, "_ms")
	return strings.Join(segs, "."), true
}

// candidateNameAt is candidate i's name in doc, "" when the section does not
// decode or the index is absent.
func candidateNameAt(doc config.Doc, i int) string {
	body, ok := doc[config.Candidates]
	if !ok {
		return ""
	}
	var cands []candidate.Candidate
	if err := json.Unmarshal(body, &cands); err != nil {
		return ""
	}
	if i < 0 || i >= len(cands) {
		return ""
	}
	return cands[i].Name
}

// changeValue renders one side of a change, cut to auditMaxValue runes. ms
// is true for a policy path whose last segment ended in "_ms".
func changeValue(raw json.RawMessage, ms bool) string {
	if len(raw) == 0 {
		return ""
	}
	v, ok := config.DecodeValue(raw)
	if !ok {
		return cutAudit(string(raw))
	}
	return cutAudit(valueText(v, ms))
}

// valueText is one decoded JSON value in words: a string unquoted, a bool
// on or off, an object's top-level scalar members as "key value" pairs in key
// order with a nested member as "key …", and an array's scalar items.
func valueText(v any, ms bool) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case string:
		return t
	case bool:
		if t {
			return "on"
		}
		return "off"
	case json.Number:
		if ms {
			return t.String() + "ms"
		}
		return t.String()
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			if isScalar(t[k]) {
				parts = append(parts, k+" "+valueText(t[k], false))
			} else {
				parts = append(parts, k+" …")
			}
		}
		return strings.Join(parts, ", ")
	case []any:
		parts := make([]string, 0, len(t))
		for _, item := range t {
			if isScalar(item) {
				parts = append(parts, valueText(item, false))
			}
		}
		return strings.Join(parts, ", ")
	}
	return fmt.Sprintf("%v", v)
}

func isScalar(v any) bool {
	switch v.(type) {
	case map[string]any, []any:
		return false
	}
	return true
}

// cutAudit shortens s to auditMaxValue runes, as config's cutValue does.
func cutAudit(s string) string {
	runes := []rune(s)
	if len(runes) <= auditMaxValue {
		return s
	}
	return string(runes[:auditMaxValue-1]) + "…"
}

// RevisionChanges renders revision rev's stored changes, in stored order.
// The document the change was made to is revision rev-1's; revision 1 has
// none, so its changes read against an empty document.
func RevisionChanges(s *config.Store, rev int64) ([]ChangeLine, error) {
	row, err := s.Revision(rev)
	if err != nil {
		return nil, err
	}
	var changes []config.Change
	if err := json.Unmarshal(row.Changes, &changes); err != nil {
		return nil, fmt.Errorf("revision #%d changes: %w", rev, err)
	}
	after, err := s.RevisionDoc(rev)
	if err != nil {
		return nil, err
	}
	before := config.Doc{}
	if rev > 1 {
		if before, err = s.RevisionDoc(rev - 1); err != nil {
			return nil, err
		}
	}

	out := make([]ChangeLine, 0, len(changes))
	for _, c := range changes {
		out = append(out, DescribeChange(c, before, after))
	}
	return out, nil
}

// CheckDoc runs the whole-document checks a cockpit edit runs, over a stored
// document: the provider lock on every candidate, then the cross-section dry
// run. A missing section is empty.
func CheckDoc(doc config.Doc) error {
	d, err := configDocOf(doc)
	if err != nil {
		return err
	}
	for _, c := range d.Candidates {
		if err := checkProvider(c.Harness, c.Provider); err != nil {
			return err
		}
	}
	return dryRun(d.Candidates, d.Actors, d.Agents, d.Policy)
}

// configDocOf decodes a document's four editable sections the way
// LoadConfigDoc decodes the stored ones.
func configDocOf(doc config.Doc) (ConfigDoc, error) {
	d := ConfigDoc{
		Actors: map[string]roles.Actor{},
		Agents: map[string]roles.AgentEntry{},
	}

	if body, ok := doc[config.Candidates]; ok {
		if err := json.Unmarshal(body, &d.Candidates); err != nil {
			return ConfigDoc{}, err
		}
	}
	if body, ok := doc[config.Actors]; ok {
		a, _, err := roles.ParseActors(body)
		if err != nil {
			return ConfigDoc{}, err
		}
		d.Actors = a
	}
	if body, ok := doc[config.Agents]; ok {
		a, _, err := roles.ParseAgents(body)
		if err != nil {
			return ConfigDoc{}, err
		}
		d.Agents = a
	}
	if body, ok := doc[config.Policy]; ok {
		p, _, err := policy.Parse(config.FileName(config.Policy), body)
		if err != nil {
			return ConfigDoc{}, err
		}
		d.Policy = p
	}
	return d, nil
}

// RollbackPreview renders what rolling back to rev would change, and refuses
// when the result would not pass CheckDoc. config.ErrNoChange and
// config.ErrNoRevision pass through unwrapped, so the caller can tell "nothing
// to do" from "no such revision".
func RollbackPreview(s *config.Store, rev int64) ([]ChangeLine, error) {
	changes, err := s.RollbackPlan(rev)
	if err != nil {
		return nil, err
	}
	snap, err := s.RevisionDoc(rev)
	if err != nil {
		return nil, err
	}
	cur, err := s.Current()
	if err != nil {
		return nil, err
	}
	if e := CheckDoc(snap); e != nil {
		return nil, &RollbackRefused{Rev: rev, Reason: text.FirstRaw(e.Error())}
	}

	out := make([]ChangeLine, 0, len(changes))
	for _, c := range changes {
		out = append(out, DescribeChange(c, cur, snap))
	}
	return out, nil
}
