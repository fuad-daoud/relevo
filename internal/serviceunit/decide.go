package serviceunit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
)

// Action is what to do with an installed unit file.
type Action string

const (
	// ActionAbsent: no unit is installed, so nothing is written.
	ActionAbsent Action = "absent"
	// ActionCurrent: the installed bytes are the shipped bytes.
	ActionCurrent Action = "current"
	// ActionStale: an older release wrote it unedited, so it is rewritten.
	ActionStale Action = "stale"
	// ActionEdited: no release wrote these bytes, so the user did; leave it.
	ActionEdited Action = "edited"
)

// SHA256Hex is the lowercase hex sha256 of b.
func SHA256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// DecideUnitAction compares an installed unit with the shipped bytes. before
// reports whether a sha is an older shipped version of the unit named doc
// (ShippedUnitBefore in production). For a plist, installed must already be
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

// RenderPlist substitutes the install-time paths into the plist template.
func RenderPlist(template []byte, bin, home string) []byte {
	out := bytes.ReplaceAll(template, []byte("@BIN@"), []byte(bin))
	return bytes.ReplaceAll(out, []byte("@HOME@"), []byte(home))
}

// CanonicalPlist undoes RenderPlist so an installed plist can be hashed
// against the template index. The binary path goes first because it usually
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
