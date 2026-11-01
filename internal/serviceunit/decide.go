package serviceunit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
)

// Action is what to do with an installed unit file.
type Action string

const (
	ActionAbsent Action = "absent"
	// ActionCurrent: the installed bytes are the shipped bytes.
	ActionCurrent Action = "current"
	// ActionStale: an older release wrote it unedited, so it is rewritten.
	ActionStale Action = "stale"
	// ActionEdited: no release wrote these bytes, so the user did; leave it.
	ActionEdited Action = "edited"
)

func SHA256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// DecideUnitAction compares an installed unit with the shipped bytes. before
// reports whether a sha is an older shipped version of the unit named doc
// (shipped.UnitBefore in production). For a plist, installed must already be
// canonicalised with CanonicalPlist.
func DecideUnitAction(doc string, installed []byte, present bool, current []byte, before func(doc, sha string) bool) Action {
	if !present {
		return ActionAbsent
	}
	if bytes.Equal(installed, current) {
		return ActionCurrent
	}
	if before(doc, SHA256Hex(installed)) {
		return ActionStale
	}
	return ActionEdited
}

func RenderPlist(template []byte, bin, home string) []byte {
	out := bytes.ReplaceAll(template, []byte("@BIN@"), []byte(bin))
	return bytes.ReplaceAll(out, []byte("@HOME@"), []byte(home))
}

// RenderService substitutes the install-time paths into the service
// template; path is the whole Environment=PATH value, already assembled.
func RenderService(template []byte, bin, path string) []byte {
	out := bytes.ReplaceAll(template, []byte("@BIN@"), []byte(bin))
	return bytes.ReplaceAll(out, []byte("@PATH@"), []byte(path))
}

// CanonicalService undoes RenderService so an installed service can be
// hashed against the template index. Substitution is gated on this machine's
// binary path being present: an old-format unit (whose ExecStart still says
// %h) does not carry it, and reverting that unit's fixed PATH line would
// erase the staleness the index can still recognise. Within the gate, the
// exact rendered value reverts when it matches; otherwise the line reverts
// whole, because the harness homes inside it changed between the write and
// this read.
func CanonicalService(installed []byte, bin, path string) []byte {
	if bin == "" || !bytes.Contains(installed, []byte(bin)) {
		return append([]byte(nil), installed...)
	}
	out := bytes.ReplaceAll(installed, []byte(bin), []byte("@BIN@"))
	if path != "" {
		line, reverted := []byte("Environment=PATH="+path), []byte("Environment=PATH=@PATH@")
		if bytes.Contains(out, line) {
			return bytes.ReplaceAll(out, line, reverted)
		}
		return bytes.Join(rewritePathLines(out), []byte("\n"))
	}
	return out
}

// rewritePathLines reverts every Environment=PATH= line whole: the exact
// value cannot match once harness lookups have drifted between the write
// and this read.
func rewritePathLines(installed []byte) [][]byte {
	var out [][]byte
	for _, line := range bytes.Split(installed, []byte("\n")) {
		if bytes.HasPrefix(line, []byte("Environment=PATH=")) {
			line = []byte("Environment=PATH=@PATH@")
		}
		out = append(out, line)
	}
	return out
}

// CanonicalPlist undoes RenderPlist so an installed plist can be hashed
// against the template index; the binary path goes first because it usually
// sits under home.
func CanonicalPlist(installed []byte, bin, home string) []byte {
	out := installed
	if bin != "" {
		out = bytes.ReplaceAll(out, []byte(bin), []byte("@BIN@"))
	}
	if home != "" {
		out = bytes.ReplaceAll(out, []byte(home), []byte("@HOME@"))
	}
	return out
}
