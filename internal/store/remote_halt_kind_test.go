package store

import (
	"encoding/json"
	"strconv"
	"testing"
)

// legacyUnreachableRecordJSON is a remote binding as the format before
// haltKindFormat wrote it: the halt text names the episode and nothing else
// does.
const legacyUnreachableRecordJSON = `{
  "format": 15,
  "name": "api",
  "cwd": "/repo/api",
  "runner": {"kind": "opencode", "server": "zen"},
  "actor": "builder",
  "state": "needs_you",
  "halt": "zen unreachable for 31m0s; round 1 may still be running there",
  "halt_notified_round": 1
}`

// TestDecodeGivesAnOlderUnreachableHaltItsKind pins the migration: a record
// written before the field carries the episode in its halt text, and reading it
// names the kind, so the clear path recognises a halt that is already on disk
// without re-reading the text there.
//
// Mutation target: bound the migration on the kind key's presence instead of on
// out.Format < haltKindFormat and this record decodes with an empty kind, so
// every episode recorded before the field stopped being recognisable.
func TestDecodeGivesAnOlderUnreachableHaltItsKind(t *testing.T) {
	got := decodeJSON[Binding](t, legacyUnreachableRecordJSON)
	if got.RemoteHaltKind != HaltKindUnreachable {
		t.Fatalf("RemoteHaltKind = %q, want %q for a halt the old format recorded", got.RemoteHaltKind, HaltKindUnreachable)
	}
}

// TestDecodeReadsTheHaltTextOnlyForOlderFormats pins the bound from the other
// side: at the current format the text is not the episode, so a record carrying
// the marker in its halt text and no kind stays unnamed.
//
// This is the case the whole field exists for -- the server's own view.Halt
// reaches b.Halt with whatever the builder said, and a builder quoting the
// marker must not produce a binding the unreachable arm will clear.
func TestDecodeReadsTheHaltTextOnlyForOlderFormats(t *testing.T) {
	got := decodeJSON[Binding](t, `{
  "format": 16,
  "name": "api",
  "cwd": "/repo/api",
  "runner": {"kind": "opencode", "server": "zen"},
  "actor": "builder",
  "state": "needs_you",
  "halt": "zen unreachable for 31m0s; round 1 may still be running there",
  "halt_notified_round": 1
}`)
	if got.RemoteHaltKind != "" {
		t.Errorf("RemoteHaltKind = %q, want empty: at this format the text is not the episode", got.RemoteHaltKind)
	}
}

// TestDecodeLeavesAnOlderUnrelatedHaltUnnamed pins that the migration reads one
// halt and no other: every halt on a remote binding that is not an unreachable
// episode has its own reason and its own owner, and reading their text would
// take them for this one.
func TestDecodeLeavesAnOlderUnrelatedHaltUnnamed(t *testing.T) {
	for _, halt := range []string{
		"",
		"zen: binding removed by the server admin",
		"api: round 1 has run past 30m0s",
		"api: reader found no output file",
	} {
		t.Run(halt, func(t *testing.T) {
			quoted, err := json.Marshal(halt)
			if err != nil {
				t.Fatal(err)
			}
			got := decodeJSON[Binding](t, `{"format":15,"name":"api","cwd":"/repo/api","state":"needs_you","halt":`+string(quoted)+`}`)
			if got.RemoteHaltKind != "" {
				t.Errorf("RemoteHaltKind = %q for halt %q, want empty", got.RemoteHaltKind, halt)
			}
		})
	}
}

// TestDecodeGivesAnOlderBrokenHaltItsKind pins the migration's broken half: a
// break recorded before the field is recognised by the fallback text it was
// given, and reading the record names the kind. Left unnamed, such a binding
// stays NEEDS YOU until its round closes or is re-sent -- the answering view
// that would clear it asks the field and finds nothing there.
func TestDecodeGivesAnOlderBrokenHaltItsKind(t *testing.T) {
	got := decodeJSON[Binding](t, `{
  "format": 15,
  "name": "api",
  "cwd": "/repo/api",
  "runner": {"kind": "opencode", "server": "zen"},
  "actor": "builder",
  "state": "needs_you",
  "halt": `+strconv.Quote(BrokenHaltText)+`,
  "halt_notified_round": 1
}`)
	if got.RemoteHaltKind != HaltKindBroken {
		t.Fatalf("RemoteHaltKind = %q, want %q for a break the old format recorded", got.RemoteHaltKind, HaltKindBroken)
	}
}

// TestDecodeLeavesABrokenHaltTheServerNamedAlone pins the other side of the
// text match: only the fallback this binary wrote is recovered. A server that
// ships its own reason for a break produced a different sentence, and the kind
// is what says the halt came from a broken view either way -- so a record whose
// text merely resembles one must not claim the episode.
func TestDecodeLeavesABrokenHaltTheServerNamedAlone(t *testing.T) {
	for _, halt := range []string{
		"",
		"builder claude; switching to codex failed: exit status 1",
		"the builder is broken",
	} {
		t.Run(halt, func(t *testing.T) {
			quoted, err := json.Marshal(halt)
			if err != nil {
				t.Fatal(err)
			}
			got := decodeJSON[Binding](t, `{"format":15,"name":"api","cwd":"/repo/api","state":"needs_you","halt":`+string(quoted)+`}`)
			if got.RemoteHaltKind != "" {
				t.Errorf("RemoteHaltKind = %q for halt %q, want empty", got.RemoteHaltKind, halt)
			}
		})
	}
}

// TestSaveKeepsTheHaltKind pins the round trip: the kind is a field of the
// record, so it survives a save and a load rather than living only in the value
// a tick happens to be holding.
func TestSaveKeepsTheHaltKind(t *testing.T) {
	s := New(t.TempDir())
	b := newBinding("api", "/repo/api")
	b.State = StateNeedsYou
	b.Halt = "zen " + UnreachableHaltMarker + " 31m0s; round 1 may still be running there"
	b.RemoteHaltKind = HaltKindUnreachable
	if err := s.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := s.Load("api")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.RemoteHaltKind != HaltKindUnreachable {
		t.Errorf("RemoteHaltKind = %q after a save and load, want %q", got.RemoteHaltKind, HaltKindUnreachable)
	}
}

// format16BrokenRecordJSON is the record the #1035/#1043 window actually wrote:
// format 16 raised, the kind two hours later, so a build in between wrote the
// broken sentence whole with no kind beside it (#1051). Both existing tests
// load format 15, which is why the gap survived.
const format16BrokenRecordJSON = `{
  "format": 16,
  "name": "api",
  "cwd": "/repo/api",
  "runner": {"kind": "opencode", "server": "zen"},
  "actor": "builder",
  "state": "needs_you",
  "halt": "the server's builder for this round is gone; rebind before sending",
  "halt_notified_round": 1
}`

// TestFormat16BrokenTextLoadsKindBroken pins the ungated half of the split: a
// format-16 record whose halt is exactly BrokenHaltText and which carries no
// kind decodes as a broken episode. The equality is safe because this binary
// never writes that sentence without the kind already stamped, so a record
// matching it can only predate the field.
//
// Mutation check: fold the broken case back under `Format < haltKindFormat`
// and this record decodes with an empty kind, so the break is never cleared.
func TestFormat16BrokenTextLoadsKindBroken(t *testing.T) {
	got := decodeJSON[Binding](t, format16BrokenRecordJSON)
	if got.RemoteHaltKind != HaltKindBroken {
		t.Fatalf("RemoteHaltKind = %q, want %q for the broken text at this format", got.RemoteHaltKind, HaltKindBroken)
	}
	if got.Halt != BrokenHaltText {
		t.Errorf("Halt = %q, want it untouched at %q", got.Halt, BrokenHaltText)
	}
}
